/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubnetSpec defines the desired state of EniSubnet.
type SubnetSpec struct {
	// +kubebuilder:validation:Enum=4;6
	// +kubebuilder:validation:Optional
	IPVersion *int64 `json:"ipVersion,omitempty"`

	// +kubebuilder:validation:Required
	Subnet string `json:"subnet-id"`

	// vpc-id alias is network-id
	// +kubebuilder:validation:Optional
	Vpc string `json:"vpc-id"`

	// project-id alias is tenant-id
	// +kubebuilder:validation:Required
	Project string `json:"project-id"`

	// +kubebuilder:validation:Optional
	PreAllocated *int `json:"preAllocated,omitempty"`

	// +kubebuilder:validation:Optional
	MinAvaliable *int `json:"minAvaliable,omitempty"`

	// +kubebuilder:validation:Optional
	NamespaceName []string `json:"namespaceName,omitempty"`

	// +kubebuilder:validation:Optional
	NodeName []string `json:"nodeName,omitempty"`
}

// SubnetStatus defines the observed state of Subnet.
type SubnetStatus struct {
	// +kubebuilder:validation:Optional
	SubnatName *string `json:"subnat-name,omitempty"`

	// +kubebuilder:validation:Optional
	VpcName *string `json:"vpc-name,omitempty"`

	// +kubebuilder:validation:Optional
	Gateway *string `json:"gateway,omitempty"`

	// +kubebuilder:validation:Optional
	Cidr *string `json:"cidr,omitempty"`

	// +kubebuilder:validation:Optional
	NamespaceName []string `json:"namespaceName,omitempty"`

	// +kubebuilder:validation:Optional
	NodeName []string `json:"nodeName,omitempty"`
}

// +kubebuilder:resource:categories={subnat},path="subnats",scope="Cluster",shortName={esn},singular="subnat"
// +kubebuilder:printcolumn:JSONPath=".spec.vpc-id",description="vpc",name="VPC",type=string
// +kubebuilder:printcolumn:JSONPath=".spec.subnet-id",description="subnet",name="SUBNET",type=string
// +kubebuilder:printcolumn:JSONPath=".status.cidr",description="cidr",name="CIDR",type=string
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +genclient
// +genclient:nonNamespaced

// Subnet is the Schema for the subnets API.
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetSpec   `json:"spec,omitempty"`
	Status SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EniSubnetList contains a list of EniSubnet.
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Subnet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubnetList{}, &Subnet{})
}
