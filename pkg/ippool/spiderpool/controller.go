package spiderpool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

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

	callfns []ippool.CallbackFn
}

var _ ippool.Manager = &controller{}

func NewControllerManager(mgr ctrl.Manager, ctx context.Context) (ippool.Manager, error) {
	n := &controller{
		Client: mgr.GetClient(),
		ctx:    ctx,
	}

	err := n.probe(mgr)
	return n, err
}

func (n *controller) probe(mgr ctrl.Manager) error {
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
			klog.Info(fmt.Sprintf("Counld not found spiderpool ippool %s.", namespaceName.Name))
			return ctrl.Result{}, nil
		} else {
			klog.Errorf("Error get spiderpool ippool: %s", err)
			return ctrl.Result{}, err
		}
	}
	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		if in.Status.AllocatedIPCount != nil && *in.Status.AllocatedIPCount == 0 {
			controllerutil.RemoveFinalizer(in, ippool.FinializerController)
		}
		return ctrl.Result{}, nil
	}

	controllerutil.AddFinalizer(in, ippool.FinializerController)

	n.mu.Lock()
	defer n.mu.Unlock()
	subnat, ok := in.Annotations[ippool.SubnatAnnotationsKey]
	if !ok {
		return ctrl.Result{}, nil
	}
	if len(in.Spec.NodeName) == 0 {
		klog.Warning("spiderippool %s nodename should not be zero.", in.Name)
	}
	mainip := in.Name
	pool, ok := n.pools[mainip]
	if !ok {
		n.pools[mainip] = &ippool.Pool{
			CapIp:     map[string]struct{}{},
			UsedIp:    map[string]struct{}{},
			Namespace: hashset.New(),
			Subnat:    subnat,
			NodeName:  in.Spec.NodeName[0],
		}
		pool = n.pools[mainip]
	}
	for ns := range in.Spec.NamespaceName {
		pool.Namespace.Add(ns)
	}
	for _, ip := range in.Spec.IPs {
		if _, exist := pool.CapIp[ip]; !exist {
			pool.CapIp[ip] = struct{}{}
		}
	}

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
			if _, exist := pool.UsedIp[ip]; !exist {
				pool.UsedIp[ip] = struct{}{}
			}
		}
	}

	return ctrl.Result{}, nil
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

// update ippool resource
// update Event: ippool.Pool depend on capip to update old
// delete Event: will mark all capip as excludeip, can not used.
func (n *controller) UpdatePool(pl *ippool.Pool, ev util.Event) error {
	// valid check
	if pl == nil || pl.MainIp == "" {
		err := fmt.Errorf("update pool failed, mainip is null")
		return err
	}

	var (
		pool   = &spiderpoolv2beta1.SpiderIPPool{}
		subnat = pl.Subnat

		ns []string

		excludips, ips []string
	)

	err := n.Get(n.ctx, types.NamespacedName{Name: pl.MainIp}, pool)

	poolCopy := pool.DeepCopy()
	// calcute before
	for _, v := range pl.Namespace.Values() {
		ns = append(ns, v.(string))
	}
	for ip := range pl.CapIp {
		ips = append(ips, ip)
	}
	for _, ip := range poolCopy.Spec.IPs {
		_, ok := pl.CapIp[ip]
		if !ok {
			excludips = append(excludips, ip)
		}
	}
	poolCopy.Spec.ExcludeIPs = excludips
	poolCopy.Spec.IPs = ips
	// update spec.Ips
	switch ev {
	case util.DeleteE:
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			return nil
		}

		if !reflect.DeepEqual(poolCopy.Spec.IPs, poolCopy.Spec.ExcludeIPs) {
			// remove all ips and update pool
			poolCopy.Spec.ExcludeIPs = poolCopy.Spec.IPs
			util.Backoff(func() error {
				return n.Client.Patch(n.ctx, poolCopy, client.MergeFrom(pool))
			})
			return fmt.Errorf("capacityIP is not equal exlucdeIP, can not remove now")
		}
		if pool.Status.AllocatedIPCount != nil && *pool.Status.AllocatedIPCount != 0 {
			return fmt.Errorf("some ip allocated, can not remove now")
		}
		// delete when notfound which mean all released.
		_ = util.Backoff(func() error {
			return n.Delete(n.ctx, pool)
		})
		return fmt.Errorf("pool resource existed")

	case util.UpdateE:
		if err != nil && apierrors.IsNotFound(err) {
			pool.ObjectMeta = metav1.ObjectMeta{
				Name: pl.MainIp,
				Annotations: map[string]string{
					ippool.SubnatAnnotationsKey: subnat,
				},
			}
			pool.Spec = spiderpoolv2beta1.IPPoolSpec{
				IPVersion:     util.Ipv4Family(), //TODO support ipv6
				IPs:           ips,
				Subnet:        "", // Required
				NodeName:      []string{pl.NodeName},
				NamespaceName: ns,
			}
			// create ippool resource
			return util.Backoff(func() error {
				return n.Client.Create(n.ctx, pool)
			})
		}
		if err != nil {
			return err
		}
		return util.Backoff(func() error {
			return n.Client.Patch(n.ctx, poolCopy, client.MergeFrom(pool))
		})
	default:
		return fmt.Errorf("ippool not support event: %v", ev)
	}
}

func (n *controller) RegistCallback(fn ippool.CallbackFn) {
	n.callfns = append(n.callfns, fn)
}
