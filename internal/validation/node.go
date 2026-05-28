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
	ReasonNoEndpoint        NodeSkipReason = "NodeNoEndpoint"
	ReasonEndpointInvalid   NodeSkipReason = "NodeEndpointInvalid"
)

// IsTransient reports whether a NodeSkipReason represents a
// bootstrap-in-progress state — i.e. the validateNode failure is
// expected to resolve on its own once the kilo daemon (or the node
// controller) writes the missing annotation. Permanent skip reasons
// (CIDR overlaps, malformed annotations, duplicate WG IPs) require an
// operator change and will never resolve via retry.
//
// Used by the controller to decide whether to schedule a periodic
// requeue when the source cluster has no valid nodes: transient → yes
// (the daemon may finish setup in seconds), permanent → no (silent
// retry would burn reconciles indefinitely; the operator should see
// the WARN log and intervene).
func IsTransient(reason NodeSkipReason) bool {
	switch reason {
	// Bootstrap-pending: kilo daemon or node controller will write
	// the missing annotation, and validateNode will pass on the next
	// reconcile cycle.
	case ReasonNoPodCIDR,
		ReasonNoWireguardIP,
		ReasonNoPublicKey,
		ReasonNoEndpoint:
		return true
	// Permanent: configuration mismatch (CIDR out of range, duplicate
	// IPs) or malformed annotation values. Retry without operator
	// intervention will not change the outcome.
	case ReasonPodCIDROutOfRange,
		ReasonWGIPInvalid,
		ReasonWGIPOutOfRange,
		ReasonWGIPDuplicate,
		ReasonEndpointInvalid:
		return false
	}

	// Unknown reason: fail closed. New transient reasons should be
	// added to the first case explicitly so that "transient" is an
	// allowlist, not an oversight.
	return false
}

// ValidateNode checks whether a node is eligible to be peered.
// It validates the node's PodCIDR against the cluster's PodCIDRs,
// the node's WireGuard IP against the cluster's WireguardCIDR, and
// that the node exposes a resolvable WireGuard endpoint via the
// kilonode fallback chain.
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

	if skip, reason, msg := validateEndpoint(node, entry); skip {
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

	// The annotation may carry any prefix length. Upstream Kilo writes a /32
	// host route ("10.4.0.1/32"); the cozystack-patched Kilo writes the host
	// IP with the wireguard subnet mask ("100.66.0.3/16"). Both are accepted;
	// only the host IP is checked against the cluster's wireguardCIDR.
	hostIP, _, err := netutil.ParseHostInCIDR(wgIP)
	if err != nil {
		return true, ReasonWGIPInvalid, fmt.Sprintf(
			"node %q annotation %q value %q is not a valid CIDR: %v",
			node.Name, kilonode.AnnotationWireguardIP, wgIP, err,
		)
	}

	wgCIDR, err := netutil.ParseCIDR(entry.WireguardCIDR)
	if err != nil {
		return true, ReasonWGIPOutOfRange, fmt.Sprintf(
			"cluster WireguardCIDR %q is invalid: %v", entry.WireguardCIDR, err,
		)
	}

	if !wgCIDR.Contains(hostIP) {
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

// validateEndpoint checks that the node has a resolvable WireGuard endpoint
// via the kilonode fallback chain. A present-but-malformed annotation is a
// distinct failure mode (ReasonEndpointInvalid) from a node with no source
// at all (ReasonNoEndpoint).
func validateEndpoint(node *corev1.Node, entry *v1alpha1.ClusterEntry) (bool, NodeSkipReason, string) {
	_, found, err := kilonode.ResolveEndpoint(node, entry.WireguardPort)
	if err != nil {
		return true, ReasonEndpointInvalid, fmt.Sprintf(
			"node %q has an invalid endpoint annotation: %v", node.Name, err,
		)
	}

	if !found {
		return true, ReasonNoEndpoint, fmt.Sprintf(
			"node %q has no resolvable endpoint (no clustermesh-endpoint, force-endpoint, or ExternalIP)",
			node.Name,
		)
	}

	return false, "", ""
}

// FindDuplicateWGIPs returns the names of nodes that have duplicate
// kilo.squat.ai/wireguard-ip annotation values. Two annotations are
// considered duplicates when they resolve to the same host IP, regardless
// of the prefix length used (e.g. "10.4.0.1/16" and "10.4.0.1/32" are the
// same host IP and therefore conflict). The first node with a given host IP
// is kept; subsequent nodes with the same host IP are returned as duplicates.
//
// If an annotation cannot be parsed as a CIDR, the raw string is used as the
// dedup key so that identical-invalid copies are still caught, but an invalid
// annotation never collides with a valid one.
func FindDuplicateWGIPs(nodes []*corev1.Node) map[string]NodeSkipReason {
	seen := make(map[string]string) // normalized host IP (or raw string) → first node name
	duplicates := make(map[string]NodeSkipReason)

	for _, node := range nodes {
		wgIP, ok := node.Annotations[kilonode.AnnotationWireguardIP]
		if !ok || wgIP == "" {
			continue
		}

		// Normalize: extract the host IP so that "10.4.0.1/16" and "10.4.0.1/32"
		// map to the same key ("10.4.0.1"). Fall back to the raw string when
		// parsing fails so that identical-invalid annotations still deduplicate.
		key := normalizeWGIPKey(wgIP)

		if _, exists := seen[key]; exists {
			duplicates[node.Name] = ReasonWGIPDuplicate
		} else {
			seen[key] = node.Name
		}
	}

	return duplicates
}

// normalizeWGIPKey returns a canonical string for use as a dedup key.
// It parses the annotation as a CIDR and returns the host IP string so that
// "10.4.0.1/16" and "10.4.0.1/32" both map to "10.4.0.1". When parsing
// fails the raw annotation value is returned unchanged, ensuring identical
// invalid annotations still deduplicate without colliding with valid IPs.
func normalizeWGIPKey(wgIP string) string {
	hostIP, _, err := netutil.ParseHostInCIDR(wgIP)
	if err != nil {
		return wgIP
	}

	return hostIP.String()
}
