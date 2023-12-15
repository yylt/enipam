package vpc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg"
	"github.com/yylt/enipam/pkg/infra"
	"github.com/yylt/enipam/pkg/ippool"
	eniv1alpha1 "github.com/yylt/enipam/pkg/k8s/apis/eni.io/v1alpha1"
	sync "github.com/yylt/enipam/pkg/lock"
	"github.com/yylt/enipam/pkg/node"
	"github.com/yylt/enipam/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Manager struct {
	ctx context.Context

	cfg *VpcCfg

	client.Client

	// handler
	ops *infraops

	mu sync.RWMutex

	// key: name
	subnats map[string]*Sninfo

	ch chan any

	// infra api
	api infra.Client

	// ippool api
	poolmg ippool.Manager

	// node api
	nodemg node.Manager
}

func NewManager(cfg *VpcCfg, mgr ctrl.Manager, ctx context.Context, pool ippool.Manager, nodemg node.Manager, api infra.Client) (*Manager, error) {
	var (
		v = &Manager{
			ctx:     ctx,
			cfg:     cfg,
			Client:  mgr.GetClient(),
			subnats: map[string]*Sninfo{},
			api:     api,
			poolmg:  pool,
			nodemg:  nodemg,
		}
	)
	if cfg.DefaultProjectId == "" || cfg.DefaultSubnatId == "" {
		return nil, fmt.Errorf("must set default projectid and subnatid")
	}
	v.ops = NewInfra(ctx, cfg.WorkerNumber, nodemg, v.api)

	pool.RegistCallback(v.callback)
	nodemg.RegistCallback(v.callback)

	return v, v.probe(mgr)
}

func (v *Manager) GetCallback() util.CallbackFn {
	return v.callback
}

func (v *Manager) NeedLeaderElection() bool {
	return true
}

// create default subnat after start.
func (v *Manager) Start(ctx context.Context) error {
	var (
		subnat        = &eniv1alpha1.Subnet{}
		defaultsubnat *eniv1alpha1.Subnet
		nsname        = types.NamespacedName{Name: defaultSubnat}
		err           error
	)
	// timer trigger
	go func() {
		v.processWork()
	}()

	err = util.Backoff(func() error {
		defaultsubnat, err = v.defaultEniSubnat()
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = util.Backoff(func() error {
		err = v.Client.Get(v.ctx, nsname, subnat)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return v.Client.Create(v.ctx, defaultsubnat)
			}
			return err
		}
		if !reflect.DeepEqual(subnat.Spec, defaultsubnat.Spec) {
			klog.Warningf("default subnat different!, oldspec is %#v, newspec is %#v", subnat.Spec, defaultsubnat.Spec)
		}

		return err
	})
	return err
}

// regist reconcile
// make trigger which
func (v *Manager) probe(mgr ctrl.Manager) error {
	err := mgr.Add(v)
	if err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&eniv1alpha1.Subnet{}).
		Complete(v)
}

// 1 according to nodename and namesapces, annotations node and CRUD ippool
// 2 vpcEvent trigger
func (v *Manager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var (
		in  = &eniv1alpha1.Subnet{}
		err error
	)

	namespaceName := req.NamespacedName
	if err = v.Get(ctx, namespaceName, in); err != nil {
		klog.Info("get vpc %s failed: %v", namespaceName.Name, err)
		return ctrl.Result{}, nil
	}
	if in.Spec.Project == "" || in.Spec.Vpc == "" || in.Spec.Subnet == "" {
		klog.Errorf("subnat \"%s\" has one of null in spec about project, vpc, subnat", namespaceName.Name)
		return ctrl.Result{}, nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	sn, ok := v.subnats[in.Name]
	if !ok {
		sn = &Sninfo{
			Node:      hashset.New(),
			Namespace: hashset.New(),
			Id:        in.Spec.Subnet,
		}
		v.subnats[in.Name] = sn
	}
	if sn.Deleted {
		klog.Infof("subnat \"%s\" is being deleted", sn.Name)
		if v.couldRemove(in.Name) {
			klog.Infof("subnat \"%s\" could remove, delete finalize", sn.Name)
			inCopy := in.DeepCopy()
			controllerutil.RemoveFinalizer(inCopy, pkg.FinializerController)
			return ctrl.Result{}, v.Client.Patch(v.ctx, inCopy, client.MergeFrom(in))
		}
		return ctrl.Result{}, nil
	}

	sn.projectId = in.Spec.Project
	sn.vpcId = in.Spec.Vpc
	sn.Id = in.Spec.Subnet

	if in.Spec.PreAllocated != nil {
		sn.PreAllocated = *in.Spec.PreAllocated
	} else {
		sn.PreAllocated = v.cfg.PreAllocated
	}
	if in.Spec.MinAvaliable != nil {
		sn.MinAvaliab = *in.Spec.MinAvaliable
	} else {
		sn.MinAvaliab = v.cfg.MinAvaliable
	}

	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		sn.Deleted = true
		return ctrl.Result{}, nil
	}

	var (
		nslist, nolist []string
	)
	// when ns is null thus mean all namespace
	// but update all namespace in trigger, not here.
	if in.Spec.NamespaceName != nil {
		nslist = in.Spec.NamespaceName
	} else {
		nslist = in.Status.NamespaceName
		sn.allnamespace = true
	}
	for _, ns := range nslist {
		sn.Namespace.Add(ns)
	}

	// when node is null thus mean all node which handle by nodemg
	// but update in trigger, not here.
	if in.Spec.NodeName != nil {
		nolist = in.Spec.NodeName
	} else {
		nolist = in.Status.NodeName
		sn.allnode = true
	}
	for _, no := range nolist {
		sn.Node.Add(no)
	}

	return ctrl.Result{}, nil
}

func (vpc *Manager) processWork() {
	var (
		// key: name
		evmap = map[string]*subnatEvent{}
	)
	for {
		select {
		case ev, ok := <-vpc.ch:
			if !ok {
				return
			}
			switch raw := ev.(type) {
			case *subnatEvent:
				evmap[raw.subnatName] = raw
			default:

			}
		case <-time.NewTicker(time.Millisecond * 200).C:
			for _, e := range evmap {
				vpc.ops.processSubnatWork(e)
			}
			clear(evmap)
		case <-vpc.ctx.Done():
			return
		}
	}
}

func (vpc *Manager) callback(v any) {
	var ()
	switch raw := v.(type) {
	case *ippool.Pool:
		vpc.mu.RLock()
		for _, sn := range vpc.subnats {
			if sn.Name == raw.Subnat {
				vpc.ch <- vpc.getSubnatEvent(sn)
			}
		}
		vpc.mu.RUnlock()
	case *node.NodeInfo:
		vpc.mu.RLock()
		for _, sn := range vpc.subnats {
			if sn.allnode || sn.Node.Contains(raw.Name) {
				vpc.ch <- vpc.getSubnatEvent(sn)
			}
		}
		vpc.mu.RUnlock()
	case *corev1.Namespace:
		vpc.mu.RLock()
		for _, sn := range vpc.subnats {
			if sn.allnamespace || sn.Namespace.Contains(raw.Name) {
				vpc.ch <- vpc.getSubnatEvent(sn)
			}
		}
		vpc.mu.RUnlock()
	}
}

// check remove safe
// also delete pool which assoiated with this
func (v *Manager) couldRemove(name string) bool {
	// check pools had releaed
	var err error
	v.poolmg.EachPool(func(p *ippool.Pool) error {
		if p.Subnat == name {
			err1 := v.poolmg.UpdatePool(p, util.DeleteE)
			if err1 != nil {
				err = errors.Join(err, err1)
			}
		}
		return nil
	})
	if err != nil {
		klog.Infof("subnat %s could not remove, retmsg: %v", name, err)
		return false
	}
	klog.Infof("safe remove subnat %s", name)
	return true
}

// set sninfo when id is equal.
func (v *Manager) getPolicy(name string) (preAllocat, minAvaliab int) {

	v.mu.RLock()
	defer v.mu.RUnlock()

	sn, ok := v.subnats[name]
	if !ok {
		return v.cfg.PreAllocated, v.cfg.MinAvaliable
	}
	return sn.PreAllocated, sn.MinAvaliab
}

func (v *Manager) getNamespace() []string {
	var (
		nslist = &corev1.NamespaceList{}
		ns     []string
	)
	err := v.Client.List(v.ctx, nslist)
	if err != nil {
		klog.Errorf("list namespace failed %s", err)
		return nil
	}
	for _, v := range nslist.Items {
		ns = append(ns, v.Name)
	}

	return ns
}

// spec from apicfg or awscfg
func (v *Manager) defaultEniSubnat() (*eniv1alpha1.Subnet, error) {
	var (
		err    error
		subnat = &eniv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{
				Name: defaultSubnat,
			},
		}
	)

	subnat.Spec = eniv1alpha1.SubnetSpec{
		IPVersion:    util.Ipv4Family(),
		Subnet:       v.cfg.DefaultSubnatId,
		Project:      v.cfg.DefaultProjectId,
		PreAllocated: &(v.cfg.PreAllocated),
		MinAvaliable: &(v.cfg.MinAvaliable),
	}

	return subnat, err

}

// calcute one subnat event
func (vpc *Manager) getSubnatEvent(sn *Sninfo) *subnatEvent {
	if sn == nil {
		return nil
	}
	var (
		event = subnatEvent{
			subnatId:   sn.Id,
			projectId:  sn.projectId,
			vpcId:      sn.vpcId,
			subnatName: sn.Name,
		}
		poinfo = &ippool.Pool{}
	)
	vpc.nodemg.EachNode(func(ni *node.NodeInfo) error {
		if !sn.Node.Contains(ni.Name) {
			klog.Infof("node \"%s\" not in subnat \"%s\", skip", ni.Name, sn.Name)
			return nil
		}
		var (
			noifs = vpc.getNodeInterface(ni)
		)

		// add: number interface need which equal to subnat
		// remove: which interface id to remove.
		noev := &nodeEvent{
			nodeId:    ni.NodeId,
			defaultIp: ni.NodeIp,
			nodeName:  ni.Name,
			remove:    hashset.New(),
			add:       1, // one subnat to one node
		}

		// TDOO, O(n*m) need optimize.
		vpc.poolmg.EachPool(func(p *ippool.Pool) error {
			if p.NodeName != noev.nodeName || p.Subnat != sn.Name {
				return nil
			}

			poev := &poolEvent{
				mainId: p.MainId,
			}
			// whatever append
			noev.pools = append(noev.pools, poev)

			if ni.IsDeleted() && vpc.nodemg.CouldRemove(ni.Name) {
				noev.remove.Add(ni.NodeId)
				return nil
			}

			if sn.Deleted {
				err := vpc.poolmg.UpdatePool(p, util.DeleteE)
				klog.Infof("subnat \"%s\" is deleted, delete pool \"%s\" and retmsg: %v", sn.Name, p.MainId, err)
				if err == nil {
					noev.remove.Add(ni.NodeId)
				}
				return nil
			}

			err := vpc.calPoolEvent(sn, p, poev)
			if err != nil {
				klog.Errorf("update pool failed %v", err)
				return nil
			}
			return nil
		})

		noev.add = noev.add - len(noev.pools)
		nset := hashset.New(vpc.getNamespace())
		// try create when not exist.
		for _, info := range noifs.Values() {
			poinfo.MainId = info.(string)
			if noev.remove.Contains(poinfo.MainId) {
				continue
			}
			poinfo.NodeName = ni.Name
			poinfo.Namespace = nset
			poinfo.Subnat = sn.Name
			vpc.poolmg.UpdatePool(poinfo, util.CreateE)
		}
		event.nodes = append(event.nodes, noev)
		return nil
	})
	return &event
}

// get interface ids in node
func (v *Manager) getNodeInterface(info *node.NodeInfo) *hashset.Set {
	var (
		noports = hashset.New()
	)
	inst := v.api.GetInstance(infra.Opt{Instance: info.NodeId})
	if inst == nil {
		klog.Warningf("not found instance for node \"%s\"", info.Name)
		return nil
	}
	for _, port := range inst.Interface {
		if port.Ip == info.NodeIp {
			continue
		}
		noports.Add(port.Id)
	}
	return noports
}

// update poolEvent and pool resource
func (v *Manager) calPoolEvent(sn *Sninfo, po *ippool.Pool, plev *poolEvent) error {
	if sn == nil || po == nil || plev == nil {
		return fmt.Errorf("update pool event, but params is null")
	}

	// . podwant = minAvaliable - (cap - used) <0 ? 0
	// . podremove = cap - used - preallocated <0 ? 0

	ifinfo := v.api.GetInterface(infra.Opt{Ip: po.MainId})
	if ifinfo == nil {
		return fmt.Errorf("not found interface by id %s", po.MainId)
	}
	for sip := range ifinfo.Second {
		po.CapIp.Add(sip)
	}

	preAllocated, minAvaliable := sn.PreAllocated, sn.MinAvaliab
	addnum := po.UsedIp.Size() + minAvaliable - po.CapIp.Size()
	if addnum > 0 {
		plev.add = addnum
	}

	delnum := po.CapIp.Size() - preAllocated - po.UsedIp.Size()
	if delnum > 0 {
		delset := po.CapIp.Difference(po.UsedIp)

		for i, ip := range delset.Values() {
			if i >= delnum {
				break
			}
			info, ok := ifinfo.Second[ip.(string)]
			if !ok {
				continue
			}
			// update event
			plev.remove.Add(infra.AddressPair{
				Ip:  info.Ip,
				Mac: info.Mac,
			})

			// TODO there is race on ip allocation(async)
			po.CapIp.Remove(info.Ip)
		}

	}
	return util.Backoff(func() error {
		return v.poolmg.UpdatePool(po, util.UpdateE)
	})
}
