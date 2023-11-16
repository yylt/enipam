// Copyright 2023 Authors of enipam
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	runtimeWebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	ciliumv1alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	spiderpoolv2beta1 "github.com/spidernet-io/spiderpool/pkg/k8s/apis/spiderpool.spidernet.io/v2beta1"
	eniv1alpha1 "github.com/yylt/enipam/pkg/k8s/apis/eni.io/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(spiderpoolv2beta1.AddToScheme(scheme))
	utilruntime.Must(ciliumv1alpha1.AddToScheme(scheme))
	utilruntime.Must(eniv1alpha1.AddToScheme(scheme))
}

func newCRDManager(cfg *Config) (ctrl.Manager, error) {

	config := ctrl.GetConfigOrDie()
	config.Burst = 200
	config.QPS = 100

	// cache read node, the node just use matedata
	cacheopt := cache.Options{
		Scheme: scheme,
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Node{}: {
				Transform: TransNode,
			},
			&corev1.Namespace{}: {},
		},
	}

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Cache:  cacheopt,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		LeaderElection:         cfg.EnableLease,
		LeaderElectionID:       "df5bc7af.eni.io",
		HealthProbeBindAddress: "0",
		WebhookServer: runtimeWebhook.NewServer(runtimeWebhook.Options{
			Port: 0,
		}),
	})
	if err != nil {
		return nil, err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.Exitf("unable to set up health check: %s", err)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		klog.Errorf("unable to set up ready check: %s", err)
	}

	return mgr, nil
}

func TransNode(in interface{}) (out interface{}, err error) {
	v, ok := in.(*corev1.Node)
	if ok {
		AddressesCopy := make([]corev1.NodeAddress, len(v.Status.Addresses))
		copy(AddressesCopy, v.Status.Addresses)

		return &corev1.Node{
			TypeMeta:   v.TypeMeta,
			ObjectMeta: v.ObjectMeta,
			Spec:       *v.Spec.DeepCopy(),
			Status: corev1.NodeStatus{
				Addresses: AddressesCopy,
			},
		}, nil
	}
	return nil, fmt.Errorf("it is not node type")
}
