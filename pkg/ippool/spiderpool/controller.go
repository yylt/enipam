package spiderpool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/yylt/enipam/pkg"
	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/ippool"
	"github.com/yylt/enipam/pkg/util"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	spiderpoolv2beta1 "github.com/spidernet-io/spiderpool/pkg/k8s/apis/spiderpool.spidernet.io/v2beta1"
)

type controller struct {
	client.Client
	ctx context.Context

	mu sync.RWMutex

	// key: mainip in node
	pools map[string]*ippool.Pool

	// callback channel, store mainid.
	ch chan string

	postfns []util.CallbackFn
}

var _ ippool.Manager = &controller{}

func NewControllerManager(mgr ctrl.Manager, ctx context.Context) (ippool.Manager, error) {
	n := &controller{
		Client: mgr.GetClient(),
		pools:  map[string]*ippool.Pool{},
		ch:     make(chan string, 16),
		ctx:    ctx,
	}

	err := n.probe(mgr)
	return n, err
}

func (n *controller) handlerCallback() {

	for {
		select {
		case name, ok := <-n.ch:
			if !ok {
				return
			}
			var ori *ippool.Pool
			n.mu.RLock()
			v, ok := n.pools[name]
			if ok {
				ori = v.DeepCopy()
			}
			n.mu.RUnlock()
			for _, fn := range n.postfns {
				fn(ori)
			}

		case <-n.ctx.Done():
			return
		}
	}
}
func (n *controller) probe(mgr ctrl.Manager) error {
	go n.handlerCallback()
	return ctrl.NewControllerManagedBy(mgr).
		For(&spiderpoolv2beta1.SpiderIPPool{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(object client.Object) bool {
			// filter have Interface Annotation
			annos := object.GetAnnotations()
			if _, ok := annos[ippool.SubnatAnnotationsKey]; ok {
				return true
			}
			return false
		})).
		Complete(n)
}

// local optima is sufficient,
// there is no need to wait for all cr had synchronize before proceeding with the operation.
func (n *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var (
		in  = &spiderpoolv2beta1.SpiderIPPool{}
		err error
	)

	namespaceName := req.NamespacedName
	if err = n.Get(ctx, namespaceName, in); err != nil {
		if apierrors.IsNotFound(err) {
			n.mu.Lock()
			defer n.mu.Unlock()
			delete(n.pools, req.Name)
			klog.Info(fmt.Sprintf("remove pool %s from cache", req.Name))
			return ctrl.Result{}, nil
		}
		klog.Errorf("Error get spiderpool ippool: %s", err)
		return ctrl.Result{}, err
	}

	subnat, ok := in.Annotations[ippool.SubnatAnnotationsKey]
	if !ok {
		klog.Warningf("not found annotation \"%s\" for subnat name, skip", ippool.SubnatAnnotationsKey)
		return ctrl.Result{}, nil
	}
	if len(in.Spec.NodeName) != 1 {
		klog.Warning("pool %s, the nodename should only one", in.Name)
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	mainip := in.Name
	pool, ok := n.pools[mainip]
	if !ok {
		n.pools[mainip] = &ippool.Pool{
			CapIp:     hashset.New(),
			UsedIp:    hashset.New(),
			Namespace: hashset.New(),
			Subnat:    subnat,
			NodeName:  in.Spec.NodeName[0],
		}
		pool = n.pools[mainip]
	}
	for ns := range in.Spec.NamespaceName {
		pool.Namespace.Add(ns)
	}
	pool.CapIp.Add(in.Spec.IPs)

	if in.Status.AllocatedIPs != nil {
		var (
			ipmap = map[string]interface{}{}
		)
		err = json.Unmarshal([]byte(*in.Status.AllocatedIPs), &ipmap)
		if err != nil {
			klog.Errorf("unmarshal failed on spiderpool.status.allocatedips: %s", err)
			return ctrl.Result{}, nil
		}
		for ip := range ipmap {
			pool.UsedIp.Add(ip)
		}
	}

	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(in, pkg.FinializerController) {
		inCopy := in.DeepCopy()
		controllerutil.AddFinalizer(inCopy, pkg.FinializerController)
		return ctrl.Result{}, n.Client.Patch(n.ctx, inCopy, client.MergeFrom(in))
	}

	return ctrl.Result{}, nil
}

func (n *controller) EachPool(fn func(*ippool.Pool) error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, v := range n.pools {
		err := fn(v.DeepCopy())
		if err != nil {
			return
		}
	}
}

func (n *controller) GetPoolByIp(mainip string) *ippool.Pool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	v, ok := n.pools[mainip]
	if !ok {
		return nil
	}
	return v.DeepCopy()
}

func (n *controller) GetPoolBySubnat(name string) []*ippool.Pool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var pools []*ippool.Pool
	for _, pool := range n.pools {
		if pool.Subnat == name {
			pools = append(pools, pool.DeepCopy())
		}
	}
	return pools
}

func (n *controller) deletePool(pl *ippool.Pool) error {
	var (
		pool = &spiderpoolv2beta1.SpiderIPPool{}
	)
	if pl == nil || pl.MainId == "" {
		err := fmt.Errorf("could not delete pool, id is null")
		return err
	}
	err := n.Get(n.ctx, types.NamespacedName{Name: pl.MainId}, pool)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// it is relased.
		return nil
	}
	poolCopy := pool.DeepCopy()
	// always try delete
	defer func() {
		_ = util.Backoff(func() error {
			return n.Delete(n.ctx, pool)
		})
	}()

	if !reflect.DeepEqual(poolCopy.Spec.IPs, poolCopy.Spec.ExcludeIPs) {
		// remove all ips and update pool, make sure no ip could be used.
		poolCopy.Spec.ExcludeIPs = poolCopy.Spec.IPs
		util.Backoff(func() error {
			return n.Client.Patch(n.ctx, poolCopy, client.MergeFrom(pool))
		})
		return fmt.Errorf("capacityIP is not equal exlucdeIP, can not remove now")
	}
	if pool.Status.AllocatedIPCount != nil && *pool.Status.AllocatedIPCount != 0 {
		return fmt.Errorf("some ip allocated, can not remove now")
	}

	controllerutil.RemoveFinalizer(poolCopy, pkg.FinializerController)
	util.Backoff(func() error {
		return n.Client.Patch(n.ctx, poolCopy, client.MergeFrom(pool))
	})

	return fmt.Errorf("pool resource existed")
}

func (n *controller) updatePool(pl *ippool.Pool) error {
	// valid check
	if pl.Subnat == "" {
		err := fmt.Errorf("could not update pool, subnat is null")
		return err
	}

	var (
		pool = &spiderpoolv2beta1.SpiderIPPool{}
		ns   []string

		excludips, ips []string
	)

	err := n.Get(n.ctx, types.NamespacedName{Name: pl.MainId}, pool)
	if err != nil {
		return err
	}
	if !pool.ObjectMeta.DeletionTimestamp.IsZero() {
		return fmt.Errorf("pool %v had been deleted", pl.MainId)
	}
	poolCopy := pool.DeepCopy()
	// calcute before
	for _, v := range pl.Namespace.Values() {
		ns = append(ns, v.(string))
	}
	for _, ip := range pl.CapIp.Values() {
		ips = append(ips, ip.(string))
	}
	for _, ip := range poolCopy.Spec.IPs {
		if !pl.CapIp.Contains(ip) {
			excludips = append(excludips, ip)
		}
	}

	poolCopy.Spec.NamespaceName = ns
	poolCopy.Spec.ExcludeIPs = excludips
	poolCopy.Spec.IPs = ips

	return util.Backoff(func() error {
		return n.Client.Patch(n.ctx, poolCopy, client.MergeFrom(pool))
	})
}

func (n *controller) UpdatePool(pl *ippool.Pool, ev util.Event) error {
	// valid check
	if pl == nil || pl.MainId == "" {
		err := fmt.Errorf("could not update pool, id is null")
		return err
	}

	switch ev {
	case util.DeleteE:
		pool := &spiderpoolv2beta1.SpiderIPPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: pl.MainId,
			},
		}
		return n.Delete(n.ctx, pool)
	case util.UpdateE:
		return n.updatePool(pl)
	case util.CreateE:
		pool := &spiderpoolv2beta1.SpiderIPPool{}
		err := n.Get(n.ctx, types.NamespacedName{Name: pl.MainId}, pool)

		if err != nil {
			if apierrors.IsNotFound(err) {
				pool.ObjectMeta = metav1.ObjectMeta{
					Name: pl.MainId,
					Annotations: map[string]string{
						ippool.SubnatAnnotationsKey: pl.Subnat,
					},
				}
				var ips = make([]string, pl.CapIp.Size())
				for i, ip := range pl.CapIp.Values() {
					ips[i] = ip.(string)
				}
				var nss = make([]string, pl.Namespace.Size())
				for i, ip := range pl.Namespace.Values() {
					nss[i] = ip.(string)
				}
				pool.Spec = spiderpoolv2beta1.IPPoolSpec{
					IPVersion:     util.Ipv4Family(), //TODO support ipv6
					IPs:           ips,
					Subnet:        "", // Required
					NodeName:      []string{pl.NodeName},
					NamespaceName: nss,
				}
				// create ippool resource
				return util.Backoff(func() error {
					return n.Client.Create(n.ctx, pool)
				})
			}
		}
		return err
	}
	return nil
}

func (n *controller) RegistCallback(fn util.CallbackFn) {
	n.postfns = append(n.postfns, fn)
}
