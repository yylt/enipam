// Copyright 2023 Authors of enipam
// SPDX-License-Identifier: Apache-2.0

// +kubebuilder:rbac:groups=eni.io,resources=enisubnat,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="coordination.k8s.io",resources=leases,verbs=create;get;update
// +kubebuilder:rbac:groups="apps",resources=statefulsets;deployments;replicasets;daemonsets,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="batch",resources=jobs;cronjobs,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="",resources=nodes;namespaces;endpoints;pods;configmaps,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch;create;update;patch;delete

package v1alpha1
