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

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// Peer is a WireGuard peer that should have access to the VPN.
type Peer struct {
	metav1.TypeMeta `json:",inline"`

	// Standard object's metadata. More info:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"` //nolint:tagliatelle // Kubernetes API convention: ObjectMeta is always serialized as "metadata"

	// Specification of the desired behavior of the Kilo Peer. More info:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/api-conventions.md#spec-and-status
	Spec PeerSpec `json:"spec"`
}

// PeerSpec is the description and configuration of a peer.
type PeerSpec struct {
	// AllowedIPs is the list of IP addresses that are allowed
	// for the given peer's tunnel.
	AllowedIPs []string `json:"allowedIPs"`
	// Endpoint is the initial endpoint for connections to the peer.
	// +optional
	Endpoint *PeerEndpoint `json:"endpoint,omitempty"`
	// PersistentKeepalive is the interval in seconds of the emission
	// of keepalive packets by the peer. This defaults to 0, which
	// disables the feature.
	// +optional
	PersistentKeepalive int `json:"persistentKeepalive,omitempty"`
	// PresharedKey is the optional symmetric encryption key for the peer.
	// +optional
	PresharedKey string `json:"presharedKey,omitempty"`
	// PublicKey is the WireGuard public key for the peer.
	PublicKey string `json:"publicKey"`
}

// PeerEndpoint represents a WireGuard endpoint, which is an IP:port tuple.
type PeerEndpoint struct {
	// DNSOrIP is a DNS name or an IP address.
	DNSOrIP `json:"dnsOrIP"`

	// Port must be a valid port number.
	Port uint32 `json:"port"`
}

// DNSOrIP represents either a DNS name or an IP address.
// When both are given, the IP address, as it is more specific, overrides the DNS name.
type DNSOrIP struct {
	// DNS must be a valid RFC 1123 subdomain.
	// +optional
	DNS string `json:"dns,omitempty"`
	// IP must be a valid IP address.
	// +optional
	IP string `json:"ip,omitempty"`
}

// +kubebuilder:object:root=true

// PeerList is a list of peers.
type PeerList struct {
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"` //nolint:tagliatelle // Kubernetes API convention: ListMeta is always serialized as "metadata"

	// List of peers.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md
	Items []Peer `json:"items"`
}
