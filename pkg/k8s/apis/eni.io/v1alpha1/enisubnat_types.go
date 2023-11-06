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

// SubnetSpec defines the desired state of SpiderSubnet.
type SubnetSpec struct {
	// +kubebuilder:validation:Enum=4;6
	// +kubebuilder:validation:Optional
	IPVersion *int64 `json:"ipVersion,omitempty"`

	// +kubebuilder:validation:Required
	Subnet string `json:"subnet-id"`

	// +kubebuilder:validation:Required
	Vpc string `json:"vpc-id"`

	// +kubebuilder:validation:Optional
	PreAllocated *int64 `json:"preAllocated,omitempty"`

	// +kubebuilder:validation:Optional
	MinAvaliable *int64 `json:"minAvaliable,omitempty"`
}

// SubnetStatus defines the observed state of SpiderSubnet.
type SubnetStatus struct {
	// +kubebuilder:validation:Optional
	SubnatName *string `json:"subnat-name,omitempty"`

	// +kubebuilder:validation:Optional
	VpcName *string `json:"vpc-name,omitempty"`

	// +kubebuilder:validation:Optional
	Cidr *string `json:"cidr,omitempty"`

	// node:ippoolname
	// +kubebuilder:validation:Optional
	Used map[string][]string `json:"used,omitempty"`
}

// +kubebuilder:resource:categories={enisubnat},path="enisubnats",scope="Cluster",shortName={es},singular="enisubnat"
// +kubebuilder:printcolumn:JSONPath=".spec.vpc-id",description="vpc",name="VPC",type=string
// +kubebuilder:printcolumn:JSONPath=".spec.subnet-id",description="subnet",name="SUBNET",type=string
// +kubebuilder:printcolumn:JSONPath=".status.cidr",description="cidr",name="CIDR",type=string
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +genclient
// +genclient:nonNamespaced

// SpiderSubnet is the Schema for the spidersubnets API.
type EniSubnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetSpec   `json:"spec,omitempty"`
	Status SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EniSubnetList contains a list of EniSubnet.
type EniSubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EniSubnet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EniSubnetList{}, &EniSubnet{})
}
