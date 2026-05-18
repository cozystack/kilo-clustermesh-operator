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
// The Peer's allowedIPs = node's PodCIDRs[0] + wireguard-ip annotation.
func BuildPeer(meshName, sourceCluster string, node *corev1.Node) (*kilov1alpha1.Peer, error) {
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

	allowedIPs := []string{node.Spec.PodCIDRs[0], netutil.HostRoute(hostIP)}

	peer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   Name(meshName, sourceCluster, node.Name),
			Labels: Labels(meshName, sourceCluster),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: allowedIPs,
			PublicKey:  pubKey,
		},
	}

	applyEndpointFromAnnotation(peer, node.Annotations[kilonode.AnnotationForceEndpoint])

	return peer, nil
}

// BuildAnchorPeer constructs a Peer that carries cluster-wide CIDRs not covered
// by per-node Peers (e.g., serviceCIDR, additionalCIDRs).
// It uses the first validated node's public key and endpoint as the anchor point.
// Returns nil when there are no cluster-wide CIDRs to advertise.
func BuildAnchorPeer(meshName, sourceCluster string, entry *v1alpha1.ClusterEntry, anchorNode *corev1.Node) *kilov1alpha1.Peer {
	anchorCIDRs := collectAnchorCIDRs(entry)
	if len(anchorCIDRs) == 0 {
		return nil
	}

	peer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   Name(meshName, sourceCluster, "anchor"),
			Labels: Labels(meshName, sourceCluster),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: anchorCIDRs,
			PublicKey:  anchorNode.Annotations[kilonode.AnnotationPublicKey],
		},
	}

	applyEndpointFromAnnotation(peer, anchorNode.Annotations[kilonode.AnnotationForceEndpoint])

	return peer
}

// collectAnchorCIDRs returns the cluster-wide CIDRs for an anchor peer.
func collectAnchorCIDRs(entry *v1alpha1.ClusterEntry) []string {
	var cidrs []string

	if entry.ServiceCIDR != "" {
		cidrs = append(cidrs, entry.ServiceCIDR)
	}

	cidrs = append(cidrs, entry.AdditionalCIDRs...)

	return cidrs
}

// applyEndpointFromAnnotation parses the endpoint annotation and sets it on the
// peer if parsing succeeds. A missing or unparseable annotation is silently ignored.
func applyEndpointFromAnnotation(peer *kilov1alpha1.Peer, endpointStr string) {
	if endpointStr == "" {
		return
	}

	endpoint, err := parseEndpoint(endpointStr)
	if err != nil {
		return
	}

	peer.Spec.Endpoint = endpoint
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

	return kilov1alpha1.DNSOrIP{DNS: host}
}
