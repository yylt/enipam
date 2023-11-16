package vpc

import (
	"context"
	"fmt"
	"reflect"
	"time"

	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/infra"
	eniv1alpha1 "github.com/yylt/enipam/pkg/k8s/apis/eni.io/v1alpha1"
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

	nodemg node.Manager

	// handler event concurrent
	ops *infraops

	mu sync.RWMutex

	// subnat - {ns+node}
	subnats map[string]*node.Sninfo

	// nodename - subnatset
	//nodes map[string]*hashset.Set

	trigger *util.Trigger

	vpctrigger *util.Trigger
	// IaaS api
	api infra.Client
}

func NewManager(cfg *VpcCfg, mgr ctrl.Manager, ctx context.Context, nodemg node.Manager, api infra.Client) (*Manager, error) {
	var (
		v = &Manager{
			nodemg:  nodemg,
			ctx:     ctx,
			cfg:     cfg,
			Client:  mgr.GetClient(),
			subnats: map[string]*node.Sninfo{},
			api:     api,
		}
		err error
	)

	infra, err := NewInfra(ctx, cfg.WorkerNumber, nodemg, v.api)
	if err != nil {
		return nil, err
	}
	v.ops = infra

	trigger, err := util.NewTrigger(util.Parameters{
		Name:        "node trigger",
		TriggerFunc: v.triggerNodeHandler,
		MinInterval: time.Millisecond * 100,
	})
	if err != nil {
		return nil, err
	}
	v.trigger = trigger

	vpctrigger, err := util.NewTrigger(util.Parameters{
		Name:        "vpc trigger",
		TriggerFunc: v.triggerVpcHandler,
		MinInterval: time.Millisecond * 100,
	})
	if err != nil {
		return nil, err
	}
	v.vpctrigger = vpctrigger

	// regist nodemanager callback
	v.nodemg.RegistCallback(v.handlerOnNode)

	return v, v.probe(mgr)

}

func (v *Manager) NeedLeaderElection() bool {
	return true
}

// create default subnat after start.
func (v *Manager) Start(ctx context.Context) error {
	var (
		subnat        = &eniv1alpha1.EniSubnet{}
		defaultsubnat *eniv1alpha1.EniSubnet
		nsname        = types.NamespacedName{Name: defaultSubnat}
		err           error
	)

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
			// TODO update?
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
		For(&eniv1alpha1.EniSubnet{}).
		Complete(v)
}

// 1 according to nodename and namesapces, annotations node and CRUD ippool
// 2 vpcEvent trigger
func (v *Manager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var (
		in  = &eniv1alpha1.EniSubnet{}
		err error
	)

	namespaceName := req.NamespacedName
	if err = v.Get(ctx, namespaceName, in); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info(fmt.Sprintf("Counld not found node %s.", namespaceName.Name))
			return ctrl.Result{}, nil
		} else {
			klog.Errorf("Error get node: %s", err)
			return ctrl.Result{}, err
		}
	}

	controllerutil.AddFinalizer(in, node.FinializerController)
	v.mu.Lock()
	sn, ok := v.subnats[in.Name]
	if !ok {
		sn = &node.Sninfo{
			Node:      hashset.New(),
			Nemaspace: hashset.New(),
			Id:        in.Spec.Subnet,
		}
		v.subnats[in.Name] = sn
	}
	if in.Spec.PreAllocated != nil {
		sn.PreAllocated = *in.Spec.PreAllocated
	}
	if in.Spec.MinAvaliable != nil {
		sn.MinAvaliab = *in.Spec.MinAvaliable
	}

	// when ns is null thus mean all namespace
	// but update all namespace in trigger, not here.
	for _, ns := range in.Status.NamespaceName {
		sn.Nemaspace.Add(ns)
	}

	// when node is null thus mean all node which handle by nodemg
	// but update in trigger, not here too.
	for _, no := range in.Status.NodeName {
		sn.Node.Add(no)
	}
	v.mu.Unlock()

	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		sn.Deleted = true
		// removeFinalize after allnode is ready.
		err := v.nodemg.UpdateNode(sn)
		if err == nil {
			controllerutil.RemoveFinalizer(in, node.FinializerController)
		}
		klog.Errorf("delete subnat failed: %s", err)
		return ctrl.Result{}, nil
	}
	v.vpctrigger.Trigger()

	return ctrl.Result{}, nil
}

// node update will trigger,
// TODO namespace event also trigger?
func (v *Manager) handlerOnNode(e util.Event, n *node.NodeInfo) {
	v.trigger.Trigger()
}

func (v *Manager) IterInfo(fn func(*node.Sninfo) error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, info := range v.subnats {
		err := fn(info)
		if err != nil {
			return
		}
	}
}

// set sninfo when id is equal.
func (v *Manager) getPolicy(name string) (preAllocat, minAvaliab int) {

	v.mu.RLock()
	defer v.mu.RUnlock()

	sn, ok := v.subnats[name]
	if !ok {
		return 0, 0
	}
	return sn.PreAllocated, sn.MinAvaliab
}

// set sninfo when id is equal.
func (v *Manager) GetInfoById(sn *node.Sninfo) {
	if sn == nil && sn.Id != "" {
		return
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	for name, info := range v.subnats {
		if sn.Id == info.Id {
			sn.Name = name
			sn.Nemaspace = hashset.New(info.Nemaspace.Values()...)
			break
		}
	}
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

// add .status
// include node and namespace, and so on.
func (v *Manager) triggerVpcHandler() {
	var (
		in     = &eniv1alpha1.EniSubnet{}
		nslist []string
		nolist []string

		md = &infra.Metadata{}
	)
	nslist = v.getNamespace()

	// loop nodes which handle is all. thus mean not all node to be managed.
	v.nodemg.IterNode(func(ni *node.NodeInfo) error {
		if ni == nil {
			return nil
		}
		nolist = append(nolist, ni.Name)
		return nil
	})

	v.mu.RLock()
	defer v.mu.RUnlock()
	// in vpc.nodes, only record nodename setted in spec.
	// loop nodes and namespace in subnat
	for sn, info := range v.subnats {
		if info == nil || info.Nemaspace == nil || info.Node == nil {
			continue
		}
		err := util.Backoff(func() error {
			return v.Get(v.ctx, types.NamespacedName{Name: sn}, in)
		})
		if err != nil {
			klog.Warningf("get subnat %s failed: %s", sn, err)
			continue
		}
		inCopy := in.DeepCopy()

		md.ProjectId = in.Spec.Project
		md.SubnatId = in.Spec.Subnet
		md.VpcId = in.Spec.Vpc
		v.api.GetMetadata(md)
		inCopy.Status.Cidr = &md.Cidr
		inCopy.Status.VpcName = &md.Vpc
		inCopy.Status.SubnatName = &md.Subnat

		if len(inCopy.Spec.NamespaceName) == 0 {
			info.Nemaspace.Add(nslist)
			inCopy.Status.NamespaceName = nslist
		} else {
			info.Nemaspace.Add(inCopy.Spec.NamespaceName)
			inCopy.Status.NodeName = inCopy.Spec.NodeName
		}
		if len(inCopy.Spec.NodeName) == 0 {
			info.Node.Add(nolist)
			inCopy.Status.NodeName = nolist
		} else {
			info.Node.Add(inCopy.Spec.NodeName)
			inCopy.Status.NodeName = inCopy.Spec.NodeName
		}
		err = util.Backoff(func() error {
			return v.Client.Patch(v.ctx, inCopy, client.MergeFrom(in))
		})
		if err != nil {
			klog.Errorf("patch subnat failed: %v", err)
			return
		}
	}
}

// 1 get node/pod had used and calculate port(add/del) number; port(attach/assign) number
// 2 trigger portCreate
// 3 trigger portAttach/portAssign
func (v *Manager) triggerNodeHandler() {
	v.mu.RLock()
	for _, info := range v.subnats {
		if info == nil || info.Nemaspace == nil || info.Node == nil {
			continue
		}
		v.nodemg.UpdateNode(info)
	}
	v.mu.RUnlock()

	info := v.nodemg.GetAllocated()
	if info == nil {
		klog.Warningf("cluster allocated info null")
		return
	}
	event := v.NodesAllocatedEvent(info)
	if event == nil {
		return
	}
	v.ops.TriggerEvent(event)
}

// spec from apicfg or awscfg
func (v *Manager) defaultEniSubnat() (*eniv1alpha1.EniSubnet, error) {
	var (
		err    error
		subnat = &eniv1alpha1.EniSubnet{
			ObjectMeta: metav1.ObjectMeta{
				Name: defaultSubnat,
			},
		}
		md = &infra.Metadata{}
	)
	v.api.GetMetadata(md)
	if md.SubnatId == "" || md.ProjectId == "" {
		return nil, fmt.Errorf("could not get infra metadata")
	}
	subnat.Spec = eniv1alpha1.SubnetSpec{
		IPVersion:    util.Ipv4Family(),
		Subnet:       md.SubnatId,
		Project:      md.ProjectId,
		PreAllocated: &(v.cfg.PreAllocated),
		MinAvaliable: &(v.cfg.MinAvaliable),
	}
	subnat.Status = eniv1alpha1.SubnetStatus{
		SubnatName: &md.Subnat,
		VpcName:    &md.Vpc,
	}
	return subnat, err

}

// compute event
// 1 count node which number interface attach
// 2 which node ip to be delete
func (v *Manager) NodesAllocatedEvent(info *node.AllocatedInfo) *Event {
	if info == nil {
		return nil
	}
	var (
		ev = &Event{
			nodeAdd:    map[string]int{},
			podAdd:     map[string]int{},
			nodeRemove: map[string][]string{},
			podRemove:  map[string][]string{},
		}

		nodeinfo = &node.NodeInfo{}
	)

	// NodeAllocat mean: node need number of interface
	for noip, snset := range info.NodeAllocated {
		nodeinfo.NodeIp = noip
		nodeinfo.NodeId = ""
		v.nodemg.GetNode(nodeinfo)
		if nodeinfo.NodeId == "" {
			klog.Errorf("get nodeinfo failed by ip %s", noip)
			return nil
		}
		ev.nodeAdd[nodeinfo.NodeId] = snset.Size()
	}

	for noip := range info.NodeRemove {
		instance := v.api.GetInstance(infra.FilterOpt{Ip: noip})
		if instance == nil {
			klog.Errorf("get instance failed by ip %s", noip)
			return nil
		}
		for _, ifin := range instance.Interface {
			if ifin == nil || ifin.Ip == noip {
				continue
			}
			ev.nodeRemove[instance.Id] = append(ev.nodeRemove[instance.Id], ifin.Id)
		}
	}

	var (
		podadd int
		podip  = map[string]struct{}{} // cache
	)
	for mainip, poolinfo := range info.PodAllocated {
		// podinfo by per node-interface
		// . podwant = max( preallocated - cap, used + minAvaliable - cap)
		// . podremove = cap - minAvaliab - used && cap > preallocated
		if poolinfo == nil {
			continue
		}
		podadd = 0
		preAllocated, minAvaliable := v.getPolicy(poolinfo.Subnat)
		num1 := preAllocated - len(poolinfo.CapIp)
		num2 := len(poolinfo.UsedIp) + minAvaliable - len(poolinfo.CapIp)
		if num1 < num2 {
			if num2 > 0 {
				podadd = num2
			}
		} else {
			if num1 > 0 {
				podadd = num1
			}
		}

		ifinfo := v.api.GetInterface(infra.FilterOpt{Ip: mainip})
		if ifinfo == nil {
			klog.Errorf("not found interface id by ip %s", mainip)
			return nil
		}
		if podadd > 0 {
			ev.podAdd[ifinfo.Id] = podadd
		}

		delnum := len(poolinfo.CapIp) - minAvaliable - len(poolinfo.UsedIp)
		if len(poolinfo.CapIp) > preAllocated && delnum > 0 {
			for k := range podip {
				delete(podip, k)
			}
			for usedip := range poolinfo.UsedIp {
				podip[usedip] = struct{}{}
			}
			for capip := range poolinfo.CapIp {
				//TODO add remove ips to exclude in ippool
				if _, ok := podip[capip]; !ok {
					ev.podRemove[ifinfo.Id] = append(ev.podRemove[ifinfo.Id], capip)
					if len(ev.podRemove[ifinfo.Id]) == delnum {
						break
					}
				}
			}
		}
	}
	return ev
}
