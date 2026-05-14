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

package validation

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/netutil"
)

// NodeSkipReason describes why a node was skipped during validation.
type NodeSkipReason string

const (
	ReasonNoPodCIDR         NodeSkipReason = "NodeNoPodCIDR"
	ReasonPodCIDROutOfRange NodeSkipReason = "NodePodCIDROutOfRange"
	ReasonNoWireguardIP     NodeSkipReason = "NodeNoWireguardIP"
	ReasonWGIPInvalid       NodeSkipReason = "WGIPInvalid"
	ReasonWGIPOutOfRange    NodeSkipReason = "WGIPOutOfRange"
	ReasonWGIPDuplicate     NodeSkipReason = "WGIPDuplicate"
	ReasonNoPublicKey       NodeSkipReason = "NodeNoPublicKey"
)

// ValidateNode checks whether a node is eligible to be peered.
// It validates the node's PodCIDR against the cluster's PodCIDRs,
// and the node's WireGuard IP against the cluster's WireguardCIDR.
// Returns (true, reason, message) if the node should be skipped, (false, "", "") if valid.
func ValidateNode(node *corev1.Node, entry *v1alpha1.ClusterEntry) (bool, NodeSkipReason, string) {
	if skip, reason, msg := validatePodCIDR(node, entry); skip {
		return true, reason, msg
	}

	if skip, reason, msg := validateWireguardIP(node, entry); skip {
		return true, reason, msg
	}

	if skip, reason, msg := validatePublicKey(node); skip {
		return true, reason, msg
	}

	return false, "", ""
}

func validatePodCIDR(node *corev1.Node, entry *v1alpha1.ClusterEntry) (bool, NodeSkipReason, string) {
	if len(node.Spec.PodCIDRs) == 0 {
		return true, ReasonNoPodCIDR, fmt.Sprintf("node %q has no PodCIDRs assigned", node.Name)
	}

	nodeCIDR, err := netutil.ParseCIDR(node.Spec.PodCIDRs[0])
	if err != nil {
		return true, ReasonNoPodCIDR, fmt.Sprintf("node %q has invalid PodCIDR %q: %v", node.Name, node.Spec.PodCIDRs[0], err)
	}

	for _, clusterCIDRStr := range entry.PodCIDRs {
		clusterCIDR, err := netutil.ParseCIDR(clusterCIDRStr)
		if err != nil {
			continue
		}

		if netutil.CIDRContains(clusterCIDR, nodeCIDR) {
			return false, "", ""
		}
	}

	return true, ReasonPodCIDROutOfRange, fmt.Sprintf(
		"node %q PodCIDR %q is not a subset of any cluster PodCIDR %v",
		node.Name, node.Spec.PodCIDRs[0], entry.PodCIDRs,
	)
}

func validateWireguardIP(node *corev1.Node, entry *v1alpha1.ClusterEntry) (bool, NodeSkipReason, string) {
	wgIP, ok := node.Annotations[kilonode.AnnotationWireguardIP]
	if !ok || wgIP == "" {
		return true, ReasonNoWireguardIP, fmt.Sprintf(
			"node %q is missing annotation %q", node.Name, kilonode.AnnotationWireguardIP,
		)
	}

	wgNet, err := netutil.ParseCIDR(wgIP)
	if err != nil {
		return true, ReasonWGIPInvalid, fmt.Sprintf(
			"node %q annotation %q value %q is not a valid CIDR: %v",
			node.Name, kilonode.AnnotationWireguardIP, wgIP, err,
		)
	}

	if !netutil.IsHostRoute(wgNet) {
		return true, ReasonWGIPInvalid, fmt.Sprintf(
			"node %q annotation %q value %q is not a host route (/32 or /128)",
			node.Name, kilonode.AnnotationWireguardIP, wgIP,
		)
	}

	wgCIDR, err := netutil.ParseCIDR(entry.WireguardCIDR)
	if err != nil {
		return true, ReasonWGIPOutOfRange, fmt.Sprintf(
			"cluster WireguardCIDR %q is invalid: %v", entry.WireguardCIDR, err,
		)
	}

	if !netutil.CIDRContains(wgCIDR, wgNet) {
		return true, ReasonWGIPOutOfRange, fmt.Sprintf(
			"node %q WireGuard IP %q is not within cluster WireguardCIDR %q",
			node.Name, wgIP, entry.WireguardCIDR,
		)
	}

	return false, "", ""
}

func validatePublicKey(node *corev1.Node) (bool, NodeSkipReason, string) {
	key, ok := node.Annotations[kilonode.AnnotationPublicKey]
	if !ok || key == "" {
		return true, ReasonNoPublicKey, fmt.Sprintf(
			"node %q is missing annotation %q", node.Name, kilonode.AnnotationPublicKey,
		)
	}

	return false, "", ""
}

// FindDuplicateWGIPs returns the names of nodes that have duplicate
// kilo.squat.ai/wireguard-ip annotation values. The first node with
// a given IP is kept; subsequent duplicates are returned as a map of
// node name to NodeSkipReason.
func FindDuplicateWGIPs(nodes []*corev1.Node) map[string]NodeSkipReason {
	seen := make(map[string]string) // ip → first node name
	duplicates := make(map[string]NodeSkipReason)

	for _, node := range nodes {
		wgIP, ok := node.Annotations[kilonode.AnnotationWireguardIP]
		if !ok || wgIP == "" {
			continue
		}

		if _, exists := seen[wgIP]; exists {
			duplicates[node.Name] = ReasonWGIPDuplicate
		} else {
			seen[wgIP] = node.Name
		}
	}

	return duplicates
}
