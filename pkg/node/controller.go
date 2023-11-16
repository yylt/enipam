package node

import (
	"context"
	"fmt"
	"time"

	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/infra"
	"github.com/yylt/enipam/pkg/ippool"
	"github.com/yylt/enipam/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type CallbackFn func(util.Event, *NodeInfo)

type Subnater interface {
	GetInfoById(*Sninfo)

	IterInfo(fn func(*Sninfo) error)
}

type Manager interface {
	// callback on node event
	// delete event and update event
	RegistCallback(CallbackFn)

	// iter by name, if name is "", will iter all
	IterNode(fn func(*NodeInfo) error)

	// update node.subnatns
	UpdateNode(sn *Sninfo) error

	// get allocatedinfo now
	GetAllocated() *AllocatedInfo

	// search by info ip/id/name
	GetNode(info *NodeInfo)

	InjectSubnat(Subnater)
}

type controller struct {
	client.Client

	ctx context.Context

	// ippool manager
	poolmg ippool.Manager

	mu sync.RWMutex

	// nodename - nodeinfo
	node map[string]*NodeInfo

	callfns []CallbackFn

	// infra api,
	api infra.Client

	//
	Subnater Subnater

	// trigger
	nodeTrigger *util.Trigger
}

var _ Manager = &controller{}

func NewControllerManager(mgr ctrl.Manager, ctx context.Context, poolmg ippool.Manager, api infra.Client) (Manager, error) {
	n := &controller{
		ctx:    ctx,
		Client: mgr.GetClient(),

		node: map[string]*NodeInfo{},
		//ipnode: map[string]string{},
		poolmg: poolmg,
		api:    api,
	}

	err := n.probe(mgr)
	if err != nil {
		return nil, err
	}
	n.poolmg.RegistCallback(n.handlerOnPool)

	triggerNode, err := util.NewTrigger(util.Parameters{
		Name:        "update node",
		MinInterval: time.Millisecond * 10,
		TriggerFunc: n.triggerHandler,
	})
	if err != nil {
		return nil, err
	}
	n.nodeTrigger = triggerNode
	return n, err
}

func (n *controller) InjectSubnat(s Subnater) {
	n.Subnater = s
}

// handler ippool event callback
func (n *controller) handlerOnPool(e util.Event, pool *ippool.Pool) {
	// trigger by ippool event
	n.nodeTrigger.Trigger()
}

func (n *controller) probe(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).WithEventFilter(predicate.NewPredicateFuncs(func(object client.Object) bool {
		no, ok := object.(*corev1.Node)
		if !ok {
			return false
		}
		_, ok = no.Annotations[InstanceAnnotationKey]
		return ok
	})).Complete(n)
}

// local optima is sufficient,
// there is no need to wait for all cr had synchronize before proceeding with the operation.
func (n *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var (
		in  = &corev1.Node{}
		err error
	)

	namespaceName := req.NamespacedName
	if err = n.Get(ctx, namespaceName, in); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info(fmt.Sprintf("Counld not found node %s.", namespaceName.Name))
			return ctrl.Result{}, nil
		} else {
			klog.Errorf("Error get node: %s", err)
			return ctrl.Result{}, err
		}
	}
	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		n.mu.Lock()
		v, ok := n.node[in.Name]
		if ok {
			v.deleted = true
		}
		n.mu.Unlock()
		return ctrl.Result{}, nil
	}

	controllerutil.AddFinalizer(in, FinializerController)

	n.mu.Lock()
	info, ok := n.node[in.Name]
	if !ok {
		info = new(NodeInfo)
		info.subnat = hashset.New()
		n.node[in.Name] = info
	}
	n.mu.Unlock()

	if info.NodeId == "" {
		info.NodeId = in.Annotations[InstanceAnnotationKey]
	}
	for _, address := range in.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			info.NodeIp = address.Address
			break
		}
	}

	info.Name = in.Name
	info.deleted = false

	// update event, not care retcode
	for _, fn := range n.callfns {
		fn(util.UpdateE, info)
	}
	// trigger by node
	n.nodeTrigger.Trigger()
	return ctrl.Result{}, nil
}

func (n *controller) GetNode(no *NodeInfo) {
	if no == nil {
		return
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, info := range n.node {
		if info.NodeIp == no.NodeIp {
			no.Name = info.Name
			no.NodeId = info.NodeId
			return
		}
		if info.NodeId == no.NodeId {
			no.Name = info.Name
			no.NodeIp = info.NodeIp
			return
		}
		if info.Name == no.Name {
			no.NodeId = info.NodeId
			no.NodeIp = info.NodeIp
			return
		}
	}
	return
}

// update subnat info on every node.
// subnatns not used yet.
func (n *controller) UpdateNode(sninfo *Sninfo) error {
	if sninfo == nil {
		return fmt.Errorf("subnat is null or nodenmae is null")
	}
	var (
		err error
	)
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, info := range n.node {
		if !sninfo.Node.Contains(info.Name) {
			continue
		}
		info.subnat.Add(sninfo.Name)
		// check pool hadnot used.
		if sninfo.Deleted {
			pools := n.poolmg.GetPoolBySubnat(sninfo.Name)
			for _, p := range pools {
				if len(p.UsedIp) != 0 {
					err = fmt.Errorf("pool %s not released yet.", p.MainIp)
				}
			}
		}
	}
	return err
}

// Note nodeinfo not deepcopy.
func (n *controller) IterNode(fn func(*NodeInfo) error) {
	var (
		err error
	)
	n.mu.RLock()
	defer n.mu.RUnlock()

	for _, ninfo := range n.node {
		err = fn(ninfo)
		if err != nil {
			return
		}
	}
}

// 1 update pool, include subnat and namespaces
func (n *controller) triggerHandler() {
	var (
		inst         = &infra.Instance{}
		subnatDelete = map[string]struct{}{}

		ev util.Event
	)
	n.Subnater.IterInfo(func(s *Sninfo) error {
		if s.Deleted {
			subnatDelete[s.Name] = struct{}{}
		}
		return nil
	})
	n.mu.RLock()
	defer n.mu.RUnlock()
	for noname, noinfo := range n.node {

		inst.DefaultIp = noinfo.NodeIp
		inst.Id = noinfo.NodeId
		inst.NodeName = noname

		// regist to infra api which sync instance
		n.api.AddInstance(inst)

		newinst := n.api.GetInstance(infra.FilterOpt{Instance: noinfo.NodeId})
		if newinst == nil {
			continue
		}
		// update ippool namesapce and capip
		// ippool should be deleted when subnat is removed.
		for _, ifinfo := range newinst.Interface {
			pool := n.getPool(noinfo, ifinfo)
			if pool == nil {
				continue
			}
			if _, ok := subnatDelete[pool.Subnat]; ok {
				ev = util.DeleteE
			} else {
				ev = util.UpdateE
			}
			err := n.poolmg.UpdatePool(pool, ev)
			if err != nil {
				klog.Errorf("update pool failed: %v", err)
			}
		}
	}
}

// callback on node event.
// 1 delete event: true-noderemove done; false noderemove notready
// 1 update event:
func (n *controller) RegistCallback(fn CallbackFn) {
	n.callfns = append(n.callfns, fn)
}

// record allocated info about node-interface-iplist
// 1 update pool by add more secondip from infra api
// 2 return allocatedinfo which include had allocatedinfo
func (n *controller) GetAllocated() *AllocatedInfo {
	var (
		ainfo = &AllocatedInfo{
			PodAllocated:  map[string]*ippool.Pool{},
			NodeAllocated: map[string]*hashset.Set{},
			NodeRemove:    map[string]struct{}{},
		}
	)
	n.mu.RLock()
	defer n.mu.RUnlock()

	// trigger ippool delete, it will call apiserver
	for _, noinfo := range n.node {
		inst := n.api.GetInstance(infra.FilterOpt{Ip: noinfo.NodeIp})
		if inst == nil {
			continue
		}

		// main interface
		for _, ifinfo := range inst.Interface {
			if ifinfo == nil || ifinfo.Ip == "" {
				continue
			}
			pool := n.poolmg.GetPoolByIp(ifinfo.Ip)
			ainfo.PodAllocated[ifinfo.Ip] = pool
		}

		if noinfo.deleted {
			ainfo.NodeRemove[noinfo.NodeIp] = struct{}{}
			continue
		}
		// nodeallocated
		snset := hashset.New()
		ainfo.NodeAllocated[noinfo.NodeIp] = snset
		snset.Add(noinfo.subnat.Values()...)
	}

	return ainfo
}

func (n *controller) getPool(noinfo *NodeInfo, mainif *infra.Interface) *ippool.Pool {
	if noinfo == nil || mainif == nil {
		return nil
	}
	var (
		pool = &ippool.Pool{
			NodeName:  noinfo.Name,
			Namespace: hashset.New(),
			CapIp:     map[string]struct{}{},
		}
		sninfo = &Sninfo{}
	)
	for _, sinfo := range mainif.Second {
		pool.CapIp[sinfo.Ip] = struct{}{}
	}
	//Note. same subnat can not be in same node.
	sninfo.Id = mainif.SubnatId
	n.Subnater.GetInfoById(sninfo)
	if sninfo.Name == "" {
		klog.Errorf("can not get subnat info from id %s", mainif.SubnatId)
		return nil
	}
	pool.Subnat = sninfo.Name
	pool.Namespace = sninfo.Nemaspace
	return pool
}
