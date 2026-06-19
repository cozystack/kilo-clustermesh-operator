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

package peer

import (
	"net"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/netutil"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// BuildPeer constructs a Peer object from a validated Node.
//
// The Peer's AllowedIPs always include the node's PodCIDR plus a /32 (or
// /128) host route derived from its kilo.squat.ai/wireguard-ip annotation.
//
// extraAllowedIPs lets the caller fold cluster-wide CIDRs (the entries of
// AllowedNetworks that no individual node represents, e.g. the service CIDR
// or host-network ranges) into the first valid node's Peer. This replaces
// the old "anchor Peer" pattern, which emitted
// a SEPARATE Peer object reusing the anchor node's public key. WireGuard
// identifies peers exclusively by their public key and keeps only one
// peer entry per pubkey: the second `wg setconf` call either dropped the
// node peer's AllowedIPs (so pod-CIDR routing disappeared) or dropped the
// anchor's (so service-CIDR routing disappeared), depending on apply
// order. The resulting outage on the receiving cluster was racy and
// could survive across reconciles. Folding the cluster-wide CIDRs into
// the node peer guarantees one WG peer entry per pubkey with the full
// union of AllowedIPs, so neither half can clobber the other.
//
// extraAllowedIPs may be nil for non-anchor nodes.
func BuildPeer(meshName string, entry *v1alpha1.ClusterEntry, node *corev1.Node, extraAllowedIPs []string) (*kilov1alpha1.Peer, error) {
	pubKey := node.Annotations[kilonode.AnnotationPublicKey]
	if pubKey == "" {
		return nil, errors.Newf("node %q has no public key annotation", node.Name)
	}

	wgIP := node.Annotations[kilonode.AnnotationWireguardIP]
	if wgIP == "" {
		return nil, errors.Newf("node %q has no wireguard-ip annotation", node.Name)
	}

	// The annotation may carry the wireguard subnet mask (cozystack-Kilo) or a
	// /32 host route (upstream Kilo). In AllowedIPs each peer must claim only
	// its own host IP, so normalise to /32 (resp. /128).
	hostIP, _, err := netutil.ParseHostInCIDR(wgIP)
	if err != nil {
		return nil, errors.Wrapf(err, "node %q has invalid wireguard-ip annotation %q", node.Name, wgIP)
	}

	// PodCIDRs is populated by the kube-controller-manager once it allocates
	// a CIDR for the node; until then BuildPeer would panic on the indexing
	// below. Surface this as a clean error so the reconciler skips the
	// node via validation rather than crashloop the operator.
	if len(node.Spec.PodCIDRs) == 0 {
		return nil, errors.Newf("node %q has no PodCIDRs allocated yet", node.Name)
	}

	allowedIPs := make([]string, 0, 2+len(extraAllowedIPs))
	allowedIPs = append(allowedIPs, node.Spec.PodCIDRs[0], netutil.HostRoute(hostIP))
	allowedIPs = append(allowedIPs, extraAllowedIPs...)

	endpoint, err := resolvePeerEndpoint(node, entry.WireguardPort)
	if err != nil {
		return nil, err
	}

	peer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   Name(meshName, entry.Name, node.Name),
			Labels: Labels(meshName, entry.Name),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs:          allowedIPs,
			PublicKey:           pubKey,
			Endpoint:            endpoint,
			PersistentKeepalive: entry.PersistentKeepalive,
		},
	}

	return peer, nil
}

// CollectAnchorCIDRs returns the residual entries of entry.AllowedNetworks
// that no individual node already advertises via its own Peer — i.e. the
// cluster-wide CIDRs (service CIDR, host-network ranges, external subnets)
// that have no per-node representative. These are folded as extraAllowedIPs
// into a single (anchor) node's Peer; see the commentary on BuildPeer for the
// WireGuard pubkey-dedup rationale that motivates folding cluster-wide CIDRs
// into a node Peer rather than emitting a separate anchor Peer.
//
// An AllowedNetworks entry N is considered "covered" — and therefore omitted
// from the anchor — when some node already carries it: either node N's first
// PodCIDR is a subset of N (the pod aggregate is announced by the per-node
// Peer's PodCIDR), or the host IP of node N's kilo.squat.ai/wireguard-ip
// annotation falls within N (the WG CIDR is announced by the per-node /32 or
// /128 host route). Nodes with a missing or unparseable PodCIDR / wireguard-ip
// simply do not cover anything; they are ignored here (validateNode already
// gates them out of the peered set).
func CollectAnchorCIDRs(entry *v1alpha1.ClusterEntry, nodes []*corev1.Node) []string {
	var residual []string

	for _, networkStr := range entry.AllowedNetworks {
		network, err := netutil.ParseCIDR(networkStr)
		if err != nil {
			// Keep unparseable entries verbatim: validation never trusted
			// these for announcement either, and dropping them would
			// silently discard operator intent.
			residual = append(residual, networkStr)

			continue
		}

		if !networkCoveredByAnyNode(network, nodes) {
			residual = append(residual, networkStr)
		}
	}

	return residual
}

// networkCoveredByAnyNode reports whether some node already advertises network
// via its per-node Peer: either the node's first PodCIDR is a subset of
// network, or the host IP of its kilo.squat.ai/wireguard-ip annotation falls
// within network. Nodes with missing/invalid PodCIDR or wireguard-ip do not
// cover anything.
func networkCoveredByAnyNode(network *net.IPNet, nodes []*corev1.Node) bool {
	for _, node := range nodes {
		if nodeCoversNetwork(network, node) {
			return true
		}
	}

	return false
}

// nodeCoversNetwork reports whether a single node already advertises network,
// either through its first PodCIDR (subset of network) or through the host IP
// of its kilo.squat.ai/wireguard-ip annotation (contained in network). A
// missing or unparseable PodCIDR / wireguard-ip simply does not cover.
func nodeCoversNetwork(network *net.IPNet, node *corev1.Node) bool {
	if len(node.Spec.PodCIDRs) > 0 {
		nodeCIDR, err := netutil.ParseCIDR(node.Spec.PodCIDRs[0])
		if err == nil && netutil.CIDRContains(network, nodeCIDR) {
			return true
		}
	}

	wgIP := node.Annotations[kilonode.AnnotationWireguardIP]
	if wgIP == "" {
		return false
	}

	hostIP, _, err := netutil.ParseHostInCIDR(wgIP)
	if err != nil {
		return false
	}

	return network.Contains(hostIP)
}

// resolvePeerEndpoint resolves a node's WireGuard endpoint via the kilonode
// fallback chain (clustermesh-endpoint annotation → force-endpoint annotation
// → ExternalIP) and parses the result into a PeerEndpoint. A present-but-
// malformed annotation, or a node with no source at all, surfaces as an error.
func resolvePeerEndpoint(node *corev1.Node, fallbackPort uint16) (*kilov1alpha1.PeerEndpoint, error) {
	endpointStr, found, err := kilonode.ResolveEndpoint(node, fallbackPort)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving endpoint for node %q", node.Name)
	}

	if !found {
		return nil, errors.Newf("node %q has no resolvable endpoint", node.Name)
	}

	endpoint, err := parseEndpoint(endpointStr)
	if err != nil {
		return nil, errors.Wrapf(err, "parsing resolved endpoint %q for node %q", endpointStr, node.Name)
	}

	return endpoint, nil
}

// parseEndpoint parses "host:port" into a PeerEndpoint.
// Handles both IPv4 and IPv6 (bracketed) addresses and DNS names.
func parseEndpoint(raw string) (*kilov1alpha1.PeerEndpoint, error) {
	idx := strings.LastIndex(raw, ":")
	if idx < 0 {
		return nil, errors.Newf("invalid endpoint format: %q", raw)
	}

	host := raw[:idx]
	portStr := raw[idx+1:]

	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid endpoint port in %q", raw)
	}

	endpoint := &kilov1alpha1.PeerEndpoint{
		Port:    uint32(port),
		DNSOrIP: buildDNSOrIP(host),
	}

	return endpoint, nil
}

// buildDNSOrIP resolves a host string into a DNSOrIP value.
// IPv6 addresses may be bracketed (e.g. [::1]); brackets are stripped before parsing.
func buildDNSOrIP(host string) kilov1alpha1.DNSOrIP {
	cleanHost := strings.Trim(host, "[]")

	if ip := net.ParseIP(cleanHost); ip != nil {
		return kilov1alpha1.DNSOrIP{IP: cleanHost}
	}

	return kilov1alpha1.DNSOrIP{DNS: cleanHost}
}
