package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/yylt/enipam/pkg"
	sync "github.com/yylt/enipam/pkg/lock"
	"github.com/yylt/enipam/pkg/util"

	"github.com/yylt/enipam/pkg/ippool"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type controller struct {
	client.Client

	ctx context.Context

	mu sync.RWMutex

	// nodename - nodeinfo
	node map[string]*NodeInfo

	// ippool
	poolmg ippool.Manager

	ch chan string

	postfns []util.CallbackFn
}

var _ Manager = &controller{}

func NewControllerManager(mgr ctrl.Manager, ctx context.Context, poolmg ippool.Manager) (Manager, error) {
	n := &controller{
		ctx:    ctx,
		Client: mgr.GetClient(),

		node:   map[string]*NodeInfo{},
		ch:     make(chan string, 16),
		poolmg: poolmg,
	}

	err := n.probe(mgr)
	if err != nil {
		return nil, err
	}

	return n, err
}

func (n *controller) handlerCallback() {

	for {
		select {
		case name, ok := <-n.ch:
			if !ok {
				return
			}
			var ori *NodeInfo
			n.mu.RLock()
			v, ok := n.node[name]
			if ok {
				ori = v.DeepCopy()
			}
			n.mu.RUnlock()
			for _, fn := range n.postfns {
				if fn != nil {
					fn(ori)
				}
			}

		case <-n.ctx.Done():
			return
		}
	}
}

func (n *controller) probe(mgr ctrl.Manager) error {
	go n.handlerCallback()
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).WithEventFilter(predicate.NewPredicateFuncs(func(object client.Object) bool {
		no, ok := object.(*corev1.Node)
		if !ok {
			return false
		}
		_, ok = no.Annotations[pkg.InstanceAnnotationKey]
		return ok
	})).Complete(n)
}

func (n *controller) RegistCallback(fn util.CallbackFn) {
	n.postfns = append(n.postfns, fn)
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

	n.mu.Lock()
	defer n.mu.Unlock()
	info, ok := n.node[in.Name]
	if !ok {
		info = new(NodeInfo)
		n.node[in.Name] = info
	}
	info.Name = in.Name

	if info.deleted {
		klog.Infof("node \"%s\" is being deleted", info.Name)
		if n.CouldRemove(in.Name) {
			klog.Infof("node \"%s\" could remove, delete finalize", info.Name)
			inCopy := in.DeepCopy()
			controllerutil.RemoveFinalizer(inCopy, pkg.FinializerController)
			return ctrl.Result{}, n.Client.Patch(n.ctx, inCopy, client.MergeFrom(in))
		}
		return ctrl.Result{}, nil
	}
	if info.NodeId == "" {
		info.NodeId = in.Annotations[pkg.InstanceAnnotationKey]
	}
	for _, address := range in.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			info.NodeIp = address.Address
			break
		}
	}
	if !in.ObjectMeta.DeletionTimestamp.IsZero() {
		info.deleted = true
		return ctrl.Result{}, nil
	}
	n.ch <- info.Name
	if !controllerutil.ContainsFinalizer(in, pkg.FinializerController) {
		inCopy := in.DeepCopy()
		controllerutil.AddFinalizer(inCopy, pkg.FinializerController)
		return ctrl.Result{}, n.Client.Patch(n.ctx, inCopy, client.MergeFrom(in))
	}
	return ctrl.Result{}, nil
}

// check node could remove
// also delete pool which assoiated with node
func (n *controller) CouldRemove(no string) bool {
	// check pools had releaed
	var err error
	n.poolmg.EachPool(func(p *ippool.Pool) error {
		if p.NodeName == no {
			err1 := n.poolmg.UpdatePool(p, util.DeleteE)
			if err1 != nil {
				err = errors.Join(err, err1)
			}
		}
		return nil
	})
	if err != nil {
		klog.Infof("node %s could not remove, msg: %v", no, err)
		return false
	}
	klog.Infof("it safe to remove node %s", no)
	return true
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
}

// Note nodeinfo not deepcopy.
func (n *controller) EachNode(fn func(*NodeInfo) error) {
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
