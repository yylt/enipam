// Copyright 2023 Authors of enipam
// SPDX-License-Identifier: Apache-2.0

// +kubebuilder:rbac:groups=eni.io,resources=subnat,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="coordination.k8s.io",resources=leases,verbs=create;get;update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=spiderpool.spidernet.io,resources=spiderippools,verbs=get;list;watch;create;update;patch;delete

package v1alpha1
