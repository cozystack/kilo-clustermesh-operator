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
// extraAllowedIPs lets the caller fold cluster-wide CIDRs (serviceCIDR and
// any AdditionalCIDRs declared on the ClusterEntry) into the first valid
// node's Peer. This replaces the old "anchor Peer" pattern, which emitted
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
			AllowedIPs: allowedIPs,
			PublicKey:  pubKey,
			Endpoint:   endpoint,
		},
	}

	return peer, nil
}

// CollectAnchorCIDRs returns the cluster-wide CIDRs (serviceCIDR plus any
// AdditionalCIDRs) declared on a ClusterEntry. The caller is expected to
// pass these as extraAllowedIPs to BuildPeer for a single (anchor) node —
// see the commentary on BuildPeer for the WireGuard pubkey-dedup
// rationale that motivates folding cluster-wide CIDRs into a node Peer
// rather than emitting a separate anchor Peer.
func CollectAnchorCIDRs(entry *v1alpha1.ClusterEntry) []string {
	var cidrs []string

	if entry.ServiceCIDR != "" {
		cidrs = append(cidrs, entry.ServiceCIDR)
	}

	cidrs = append(cidrs, entry.AdditionalCIDRs...)

	return cidrs
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
