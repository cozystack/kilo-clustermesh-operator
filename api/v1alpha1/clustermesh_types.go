/*
Copyright 2026 The Kilo Authors.

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

// ClusterMeshSpec defines the desired state of ClusterMesh.
type ClusterMeshSpec struct {
	// Clusters is the list of clusters to connect in this mesh.
	// +kubebuilder:validation:MinItems=2
	Clusters []ClusterEntry `json:"clusters"`
}

// ClusterEntry describes a single cluster participating in the mesh.
type ClusterEntry struct {
	// Name is a unique identifier for this cluster within the mesh.
	// Used as label value and status key. Must be a valid DNS-1123 label.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Local indicates this is the cluster where the controller runs.
	// Exactly one cluster in the mesh must be marked as local.
	// No kubeconfigSecretRef is needed for the local cluster.
	// +optional
	Local bool `json:"local,omitempty"`

	// KubeconfigSecretRef references a Secret containing the kubeconfig
	// for this cluster. Required for non-local clusters.
	// +optional
	KubeconfigSecretRef *SecretKeyRef `json:"kubeconfigSecretRef,omitempty"`

	// PodCIDRs is the list of pod network CIDRs for this cluster.
	// Node.Spec.PodCIDRs must be a subset of these CIDRs.
	// Multiple entries support dual-stack (IPv4 + IPv6).
	// +kubebuilder:validation:MinItems=1
	PodCIDRs []string `json:"podCIDRs"` //nolint:tagliatelle // "podCIDRs" is the canonical field name; "CIDR" is a well-known acronym

	// WireguardCIDR is the CIDR for Kilo's WireGuard interface (kilo0) addresses.
	// Each node's kilo.squat.ai/wireguard-ip must have its host IP within this CIDR.
	// The annotation may carry any prefix length (e.g. "10.4.0.1/32" upstream Kilo
	// or "10.4.0.1/16" cozystack-patched Kilo); only the host portion is validated.
	WireguardCIDR string `json:"wireguardCIDR"`

	// WireguardPort is the UDP port of Kilo's WireGuard endpoint on each node in
	// this cluster. Used as a fallback when the operator synthesises the
	// endpoint from Node.Status.Addresses (i.e. neither
	// kilo.squat.ai/clustermesh-endpoint nor kilo.squat.ai/force-endpoint is set
	// on a node). Defaults to 51820.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=51820
	// +optional
	WireguardPort uint16 `json:"wireguardPort,omitempty"`

	// ServiceCIDR is the Kubernetes service network CIDR for this cluster.
	// If set, it will be advertised via an anchor Peer so that services
	// in this cluster are reachable from other mesh members.
	// +optional
	ServiceCIDR string `json:"serviceCIDR,omitempty"`

	// AdditionalCIDRs are extra CIDRs to advertise into the mesh
	// (e.g., host-network ranges, external subnets).
	// +optional
	AdditionalCIDRs []string `json:"additionalCIDRs,omitempty"` //nolint:tagliatelle // "additionalCIDRs" is the canonical field name; "CIDR" is a well-known acronym

	// PersistentKeepalive is the interval in seconds at which WireGuard
	// sends keepalive packets to peers in this cluster. Set to a non-zero
	// value (e.g. 25) for clusters behind NAT so that the stateful NAT
	// mapping is refreshed before it expires, enabling bidirectional traffic
	// even when the cluster has no directly-routable public IP.
	// 0 disables persistent keepalive (default).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +optional
	PersistentKeepalive int `json:"persistentKeepalive,omitempty"`
}

// AllCIDRs returns the union of all CIDRs declared by this cluster entry.
// The order is: podCIDRs, wireguardCIDR, serviceCIDR (if set), additionalCIDRs.
func (c *ClusterEntry) AllCIDRs() []string {
	cidrs := make([]string, 0, len(c.PodCIDRs)+1+1+len(c.AdditionalCIDRs))
	cidrs = append(cidrs, c.PodCIDRs...)

	cidrs = append(cidrs, c.WireguardCIDR)

	if c.ServiceCIDR != "" {
		cidrs = append(cidrs, c.ServiceCIDR)
	}

	cidrs = append(cidrs, c.AdditionalCIDRs...)

	return cidrs
}

// SecretKeyRef identifies a key within a Kubernetes Secret.
type SecretKeyRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret data.
	Key string `json:"key"`
}

// ClusterMeshStatus defines the observed state of ClusterMesh.
type ClusterMeshStatus struct {
	// Clusters contains per-cluster status information.
	// +optional
	Clusters []ClusterStatus `json:"clusters,omitempty"`
	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterStatus holds the observed state for a single cluster in the mesh.
type ClusterStatus struct {
	// Name matches ClusterEntry.Name.
	Name string `json:"name"`
	// RegisteredPeers is the number of Peer objects created for this cluster's nodes.
	RegisteredPeers int `json:"registeredPeers"`
	// SkippedNodes is the number of nodes that failed validation and were not peered.
	SkippedNodes int `json:"skippedNodes"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cm
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClusterMesh is the Schema for the clustermeshes API.
type ClusterMesh struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"` //nolint:tagliatelle // "metadata" is the canonical Kubernetes field name required by the API conventions

	// spec defines the desired state of ClusterMesh.
	Spec ClusterMeshSpec `json:"spec"`

	// status defines the observed state of ClusterMesh.
	// +optional
	Status ClusterMeshStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterMeshList contains a list of ClusterMesh.
type ClusterMeshList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"` //nolint:tagliatelle // "metadata" is the canonical Kubernetes field name required by the API conventions

	Items []ClusterMesh `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterMesh{}, &ClusterMeshList{})
}
