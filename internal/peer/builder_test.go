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

package peer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
)

const (
	testPubKey        = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPodCIDR       = "10.244.1.0/24"
	testWgIP          = "10.4.0.1/32"
	testForceEndpoint = "203.0.113.1:51820"
)

// testNode creates a minimal Node for use in builder tests.
func testNode(name, podCIDR string, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: corev1.NodeSpec{
			PodCIDRs: []string{podCIDR},
		},
	}
}

// baseAnnotations returns annotations containing all required fields,
// including a valid force-endpoint so that BuildPeer succeeds by default.
// Tests that need a different endpoint source should override or delete
// the relevant key.
func baseAnnotations() map[string]string {
	return map[string]string{
		kilonode.AnnotationPublicKey:     testPubKey,
		kilonode.AnnotationWireguardIP:   testWgIP,
		kilonode.AnnotationForceEndpoint: testForceEndpoint,
	}
}

// testEntry returns a minimal ClusterEntry usable in builder tests.
// The Name matches the legacy "cluster-a" string used by tests for peer name
// and label assertions; WireguardPort is set to the well-known default so
// ExternalIP-fallback paths get a deterministic port.
func testEntry() *v1alpha1.ClusterEntry {
	return &v1alpha1.ClusterEntry{
		Name:          "cluster-a",
		WireguardPort: 51820,
	}
}

func TestBuildPeer_HappyPath(t *testing.T) {
	t.Parallel()

	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "203.0.113.1:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, peer.Name("my-mesh", "cluster-a", "worker-1"), got.Name)
	assert.Equal(t, peer.Labels("my-mesh", "cluster-a"), got.Labels)
	assert.Equal(t, testPubKey, got.Spec.PublicKey)
	assert.Equal(t, []string{testPodCIDR, testWgIP}, got.Spec.AllowedIPs)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
	assert.Equal(t, "203.0.113.1", got.Spec.Endpoint.IP)
}

func TestBuildPeer_CozystackStyleWGAnnotation(t *testing.T) {
	t.Parallel()

	// cozystack-patched Kilo writes the wireguard-ip annotation as
	// "<host-ip>/<wireguard-subnet-mask>" (e.g. "100.66.0.3/16"), not as a
	// /32 host route. BuildPeer must still emit a /32 (resp. /128) host
	// route in AllowedIPs so that each peer terminates traffic for exactly
	// one WireGuard IP — otherwise every peer would claim the entire
	// wireguard subnet and break routing.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationWireguardIP] = "100.66.0.3/16"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, []string{testPodCIDR, "100.66.0.3/32"}, got.Spec.AllowedIPs)
}

func TestBuildPeer_InvalidWireguardIP(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{
		kilonode.AnnotationPublicKey:   testPubKey,
		kilonode.AnnotationWireguardIP: "not-a-cidr",
	}

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
}

func TestBuildPeer_MissingPublicKey(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{
		kilonode.AnnotationWireguardIP: testWgIP,
	}

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "no public key annotation")
}

func TestBuildPeer_MissingWireguardIP(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{
		kilonode.AnnotationPublicKey: testPubKey,
	}

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "no wireguard-ip annotation")
}

func TestBuildPeer_NoEndpointSources_ReturnsError(t *testing.T) {
	t.Parallel()

	// Node has the wireguard-ip and public-key annotations but no
	// endpoint source (no clustermesh-endpoint, no force-endpoint, no
	// ExternalIP). The fallback chain in kilonode.ResolveEndpoint
	// returns no source and BuildPeer surfaces this as a hard error so
	// that misconfiguration is visible rather than producing an
	// endpoint-less Peer.
	annotations := baseAnnotations()
	delete(annotations, kilonode.AnnotationForceEndpoint)

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "no resolvable endpoint")
}

func TestBuildPeer_DNSEndpoint(t *testing.T) {
	t.Parallel()

	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "node.example.com:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
	assert.Equal(t, "node.example.com", got.Spec.Endpoint.DNS)
	assert.Empty(t, got.Spec.Endpoint.IP)
}

func TestBuildPeer_IPEndpoint(t *testing.T) {
	t.Parallel()

	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "198.51.100.42:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
	assert.Equal(t, "198.51.100.42", got.Spec.Endpoint.IP)
	assert.Empty(t, got.Spec.Endpoint.DNS)
}

func TestBuildAnchorPeer_WithServiceCIDR(t *testing.T) {
	t.Parallel()

	entry := &v1alpha1.ClusterEntry{
		Name:        "cluster-a",
		ServiceCIDR: "10.96.0.0/12",
	}

	node := testNode("worker-1", testPodCIDR, baseAnnotations())

	got := peer.BuildAnchorPeer("my-mesh", entry, node)

	require.NotNil(t, got)
	assert.Equal(t, peer.Name("my-mesh", "cluster-a", "anchor"), got.Name)
	assert.Equal(t, peer.Labels("my-mesh", "cluster-a"), got.Labels)
	assert.Contains(t, got.Spec.AllowedIPs, "10.96.0.0/12")
	assert.Equal(t, testPubKey, got.Spec.PublicKey)
}

func TestBuildAnchorPeer_WithAdditionalCIDRs(t *testing.T) {
	t.Parallel()

	entry := &v1alpha1.ClusterEntry{
		Name:            "cluster-a",
		ServiceCIDR:     "10.96.0.0/12",
		AdditionalCIDRs: []string{"192.168.100.0/24", "172.16.0.0/16"},
	}

	node := testNode("worker-1", testPodCIDR, baseAnnotations())

	got := peer.BuildAnchorPeer("my-mesh", entry, node)

	require.NotNil(t, got)
	assert.Equal(t, []string{"10.96.0.0/12", "192.168.100.0/24", "172.16.0.0/16"}, got.Spec.AllowedIPs)
}

func TestBuildAnchorPeer_NoAnchorCIDRs(t *testing.T) {
	t.Parallel()

	entry := &v1alpha1.ClusterEntry{
		Name:            "cluster-a",
		ServiceCIDR:     "",
		AdditionalCIDRs: nil,
	}

	node := testNode("worker-1", testPodCIDR, baseAnnotations())

	got := peer.BuildAnchorPeer("my-mesh", entry, node)

	assert.Nil(t, got, "must return nil when there are no cluster-wide CIDRs")
}

func TestBuildPeer_MalformedForceEndpoint_ReturnsError(t *testing.T) {
	t.Parallel()

	// A present-but-malformed force-endpoint annotation is treated as a
	// hard error rather than being silently skipped. This makes
	// misconfiguration visible at reconcile time instead of producing a
	// Peer without an endpoint.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "no-colon-at-all"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
}

func TestBuildPeer_ClustermeshEndpointPreferred(t *testing.T) {
	t.Parallel()

	// When both clustermesh-endpoint and force-endpoint annotations are set,
	// the operator-specific clustermesh-endpoint wins.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "203.0.113.1:51820"
	annotations[kilonode.AnnotationClustermeshEndpoint] = "198.51.100.42:60000"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(60000), got.Spec.Endpoint.Port)
	assert.Equal(t, "198.51.100.42", got.Spec.Endpoint.IP)
}

func TestBuildPeer_ExternalIPFallback(t *testing.T) {
	t.Parallel()

	// With no endpoint annotations on the node, BuildPeer must synthesise
	// the endpoint from Node.Status.Addresses (ExternalIP, preferring IPv4)
	// combined with entry.WireguardPort.
	annotations := baseAnnotations()
	delete(annotations, kilonode.AnnotationForceEndpoint)

	node := testNode("worker-1", testPodCIDR, annotations)
	node.Status.Addresses = []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: corev1.NodeExternalIP, Address: "203.0.113.99"},
	}

	entry := &v1alpha1.ClusterEntry{Name: "cluster-a", WireguardPort: 51820}

	got, err := peer.BuildPeer("my-mesh", entry, node)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
	assert.Equal(t, "203.0.113.99", got.Spec.Endpoint.IP)
}

func TestBuildPeer_MalformedClustermeshEndpoint_ReturnsError(t *testing.T) {
	t.Parallel()

	// A present-but-malformed clustermesh-endpoint annotation is a hard
	// error, even if force-endpoint is also set. Strict validation on the
	// highest-priority source prevents typos from silently falling
	// through to a lower-priority source.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationClustermeshEndpoint] = "garbage"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.Error(t, err)
	assert.Nil(t, got)
}

func TestBuildAnchorPeer_NoEndpointSource_ReturnsNil(t *testing.T) {
	t.Parallel()

	// An anchor peer without an endpoint cannot terminate cross-cluster
	// traffic for its CIDRs, so BuildAnchorPeer returns nil when the
	// anchor node has no resolvable endpoint.
	entry := &v1alpha1.ClusterEntry{
		Name:        "cluster-a",
		ServiceCIDR: "10.96.0.0/12",
	}

	annotations := baseAnnotations()
	delete(annotations, kilonode.AnnotationForceEndpoint)

	node := testNode("worker-1", testPodCIDR, annotations)

	got := peer.BuildAnchorPeer("my-mesh", entry, node)

	assert.Nil(t, got, "anchor without resolvable endpoint must be nil")
}

func TestBuildAnchorPeer_ExternalIPFallback(t *testing.T) {
	t.Parallel()

	// The anchor peer participates in the same fallback chain — when the
	// anchor node has no annotations but does have an ExternalIP, the
	// endpoint is synthesised from Node.Status.Addresses.
	entry := &v1alpha1.ClusterEntry{
		Name:          "cluster-a",
		ServiceCIDR:   "10.96.0.0/12",
		WireguardPort: 51820,
	}

	annotations := baseAnnotations()
	delete(annotations, kilonode.AnnotationForceEndpoint)

	node := testNode("worker-1", testPodCIDR, annotations)
	node.Status.Addresses = []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "203.0.113.99"},
	}

	got := peer.BuildAnchorPeer("my-mesh", entry, node)

	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, "203.0.113.99", got.Spec.Endpoint.IP)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
}

func TestBuildPeer_BracketedDNSEndpoint(t *testing.T) {
	t.Parallel()

	// A bracketed DNS name like [dns.example.com]:51820 is unusual but valid input
	// for net.JoinHostPort. buildDNSOrIP must strip the brackets and return the
	// clean hostname — not "[dns.example.com]" — in the DNS field.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "[dns.example.com]:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", testEntry(), node)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Endpoint)
	assert.Equal(t, uint32(51820), got.Spec.Endpoint.Port)
	assert.Equal(t, "dns.example.com", got.Spec.Endpoint.DNS,
		"brackets must be stripped from the DNS field; got %q", got.Spec.Endpoint.DNS)
	assert.Empty(t, got.Spec.Endpoint.IP)
}
