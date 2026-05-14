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
	testPubKey  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPodCIDR = "10.244.1.0/24"
	testWgIP    = "10.4.0.1/32"
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

// baseAnnotations returns annotations containing all required fields.
func baseAnnotations() map[string]string {
	return map[string]string{
		kilonode.AnnotationPublicKey:   testPubKey,
		kilonode.AnnotationWireguardIP: testWgIP,
	}
}

func TestBuildPeer_HappyPath(t *testing.T) {
	t.Parallel()

	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "203.0.113.1:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

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

func TestBuildPeer_MissingPublicKey(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{
		kilonode.AnnotationWireguardIP: testWgIP,
	}

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

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

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "no wireguard-ip annotation")
}

func TestBuildPeer_WithoutEndpoint(t *testing.T) {
	t.Parallel()

	node := testNode("worker-1", testPodCIDR, baseAnnotations())

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Spec.Endpoint, "endpoint must be nil when annotation is absent")
}

func TestBuildPeer_DNSEndpoint(t *testing.T) {
	t.Parallel()

	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "node.example.com:51820"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

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

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

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

	got := peer.BuildAnchorPeer("my-mesh", "cluster-a", entry, node)

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

	got := peer.BuildAnchorPeer("my-mesh", "cluster-a", entry, node)

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

	got := peer.BuildAnchorPeer("my-mesh", "cluster-a", entry, node)

	assert.Nil(t, got, "must return nil when there are no cluster-wide CIDRs")
}

func TestParseEndpoint_InvalidFormat(t *testing.T) {
	t.Parallel()

	// Use BuildPeer with a malformed endpoint to exercise parseEndpoint's error path.
	// Since parseEndpoint is unexported, we verify via the public API: an invalid
	// endpoint annotation is silently skipped and the Peer is built without endpoint.
	annotations := baseAnnotations()
	annotations[kilonode.AnnotationForceEndpoint] = "no-colon-at-all"

	node := testNode("worker-1", testPodCIDR, annotations)

	got, err := peer.BuildPeer("my-mesh", "cluster-a", node)

	require.NoError(t, err, "invalid endpoint annotation must not cause an error")
	require.NotNil(t, got)
	assert.Nil(t, got.Spec.Endpoint, "unparseable endpoint must be silently skipped")
}
