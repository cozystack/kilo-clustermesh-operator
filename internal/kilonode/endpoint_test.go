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

package kilonode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
)

// makeEndpointNode creates a Node with the given annotations and Status.Addresses for endpoint tests.
func makeEndpointNode(annotations map[string]string, addresses []corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-node",
			Annotations: annotations,
		},
		Status: corev1.NodeStatus{
			Addresses: addresses,
		},
	}
}

func TestResolveEndpoint_ClustermeshAnnotationWins(t *testing.T) {
	t.Parallel()

	// clustermesh-endpoint takes priority over force-endpoint when both are set.
	node := makeEndpointNode(map[string]string{
		kilonode.AnnotationClustermeshEndpoint: "203.0.113.1:51820",
		kilonode.AnnotationForceEndpoint:       "198.51.100.1:51820",
	}, nil)

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "203.0.113.1:51820", endpoint)
}

func TestResolveEndpoint_ForceEndpointFallback(t *testing.T) {
	t.Parallel()

	// When clustermesh-endpoint is absent, force-endpoint is used.
	node := makeEndpointNode(map[string]string{
		kilonode.AnnotationForceEndpoint: "198.51.100.1:51820",
	}, nil)

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "198.51.100.1:51820", endpoint)
}

func TestResolveEndpoint_ExternalIPFallback_IPv4(t *testing.T) {
	t.Parallel()

	// No annotations; single ExternalIP (IPv4) → synthesise endpoint.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "203.0.113.5:51820", endpoint)
}

func TestResolveEndpoint_ExternalIPFallback_PrefersIPv4(t *testing.T) {
	t.Parallel()

	// Node has both IPv4 and IPv6 ExternalIPs → IPv4 must be preferred.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "2001:db8::1"},
		{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "203.0.113.5:51820", endpoint)
}

func TestResolveEndpoint_ExternalIPFallback_IPv6OnlyWhenNoIPv4(t *testing.T) {
	t.Parallel()

	// Only an IPv6 ExternalIP is available → use it with brackets.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "2001:db8::1"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "[2001:db8::1]:51820", endpoint)
}

func TestResolveEndpoint_ExternalIPFallback_DefaultPort(t *testing.T) {
	t.Parallel()

	// fallbackPort = 0 → must default to 51820.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 0)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "203.0.113.5:51820", endpoint)
}

func TestResolveEndpoint_ExternalIPFallback_CustomPort(t *testing.T) {
	t.Parallel()

	// Non-default fallback port must be used as-is.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 12345)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "203.0.113.5:12345", endpoint)
}

func TestResolveEndpoint_NoSource_ReturnsFoundFalse(t *testing.T) {
	t.Parallel()

	// No annotations, no ExternalIPs → not found, no error.
	node := makeEndpointNode(nil, nil)

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, endpoint)
}

func TestResolveEndpoint_ClustermeshAnnotationMalformed_ReturnsError(t *testing.T) {
	t.Parallel()

	// Malformed clustermesh-endpoint must return an error, not fall through.
	node := makeEndpointNode(map[string]string{
		kilonode.AnnotationClustermeshEndpoint: "not-a-valid-endpoint",
	}, nil)

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.Error(t, err)
	assert.False(t, found)
	assert.Empty(t, endpoint)
	assert.Contains(t, err.Error(), kilonode.AnnotationClustermeshEndpoint)
}

func TestResolveEndpoint_ForceEndpointMalformed_ReturnsError(t *testing.T) {
	t.Parallel()

	// Malformed force-endpoint must return an error, not fall through.
	node := makeEndpointNode(map[string]string{
		kilonode.AnnotationForceEndpoint: "no-port-here",
	}, nil)

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.Error(t, err)
	assert.False(t, found)
	assert.Empty(t, endpoint)
	assert.Contains(t, err.Error(), kilonode.AnnotationForceEndpoint)
}

func TestResolveEndpoint_IgnoresInternalIP(t *testing.T) {
	t.Parallel()

	// InternalIP addresses must NOT be used for endpoint synthesis.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, endpoint)
}

func TestResolveEndpoint_IgnoresHostname(t *testing.T) {
	t.Parallel()

	// Hostname type must NOT be treated as ExternalIP.
	node := makeEndpointNode(nil, []corev1.NodeAddress{
		{Type: corev1.NodeHostName, Address: "worker-1"},
	})

	endpoint, found, err := kilonode.ResolveEndpoint(node, 51820)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, endpoint)
}
