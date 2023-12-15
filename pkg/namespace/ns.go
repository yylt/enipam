package namespace

import (
	"context"
	"fmt"

	"github.com/yylt/enipam/pkg/util"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type controller struct {
	client.Client

	ctx context.Context

	postfns []util.CallbackFn
}

var _ Manager = &controller{}

func NewControllerManager(mgr ctrl.Manager, ctx context.Context) (Manager, error) {
	n := &controller{
		ctx:    ctx,
		Client: mgr.GetClient(),
	}

	err := n.probe(mgr)
	if err != nil {
		return nil, err
	}

	return n, err
}

func (n *controller) probe(mgr ctrl.Manager) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Complete(n)
}

func (n *controller) RegistCallback(fn util.CallbackFn) {
	n.postfns = append(n.postfns, fn)
}

// local optima is sufficient,
// there is no need to wait for all cr had synchronize before proceeding with the operation.
func (n *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var (
		in  = &corev1.Namespace{}
		err error
	)

	namespaceName := req.NamespacedName
	if err = n.Get(ctx, namespaceName, in); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info(fmt.Sprintf("Counld not found ns %s.", namespaceName.Name))
			return ctrl.Result{}, nil
		}
		klog.Errorf("Error get ns: %s", err)
		return ctrl.Result{}, err
	}
	for _, fn := range n.postfns {
		if fn != nil {
			fn(in)
		}
	}
	return ctrl.Result{}, nil
}
