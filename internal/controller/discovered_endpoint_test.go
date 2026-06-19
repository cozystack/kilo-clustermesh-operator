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

package controller

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

const discoveredTestPubKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestParseDiscoveredEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hostPort string
		wantErr  bool
		wantIP   string
		wantPort uint32
	}{
		{
			name:     "valid IPv4 host:port",
			hostPort: "203.0.113.7:51820",
			wantIP:   "203.0.113.7",
			wantPort: 51820,
		},
		{
			name:     "missing port",
			hostPort: "203.0.113.7",
			wantErr:  true,
		},
		{
			name:     "non-numeric port",
			hostPort: "203.0.113.7:abc",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDiscoveredEndpoint(tc.hostPort)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantIP, got.IP)
			assert.Equal(t, tc.wantPort, got.Port)
		})
	}
}

func enrichScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	return scheme
}

func nodeWithDiscovered(name, rawJSON string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{kilonode.AnnotationDiscoveredEndpoints: rawJSON},
		},
	}
}

func desiredPeer(name, pubKey, endpointIP string, port uint32) *kilov1alpha1.Peer {
	return &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kilov1alpha1.PeerSpec{
			PublicKey:  pubKey,
			AllowedIPs: []string{"10.1.0.0/24"},
			Endpoint: &kilov1alpha1.PeerEndpoint{
				DNSOrIP: kilov1alpha1.DNSOrIP{IP: endpointIP},
				Port:    port,
			},
		},
	}
}

// TestEnrichEndpointsFromDiscovered_OverridesWhenDifferent verifies that when a
// target node has observed a different (NAT-egress) endpoint for a peer's
// public key, the enriched Peer carries the discovered endpoint instead of the
// configured one.
func TestEnrichEndpointsFromDiscovered_OverridesWhenDifferent(t *testing.T) {
	t.Parallel()

	scheme := enrichScheme(t)
	node := nodeWithDiscovered("target-1",
		`{"`+discoveredTestPubKey+`":{"IP":"198.51.100.42","Port":51820}}`,
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	r := &ClusterMeshReconciler{}
	// Configured endpoint is the source's internal IP; discovered is the real
	// NAT egress and must win.
	desired := []*kilov1alpha1.Peer{
		desiredPeer("peer-1", discoveredTestPubKey, "10.0.0.5", 51820),
	}

	got, err := r.enrichEndpointsFromDiscovered(context.Background(), discardLogger(), fc, desired)

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Spec.Endpoint)
	assert.Equal(t, "198.51.100.42", got[0].Spec.Endpoint.IP)
	assert.Equal(t, uint32(51820), got[0].Spec.Endpoint.Port)
}

// TestEnrichEndpointsFromDiscovered_KeepsWhenSame verifies that an already-
// correct configured endpoint is not rewritten when the discovered value
// matches it.
func TestEnrichEndpointsFromDiscovered_KeepsWhenSame(t *testing.T) {
	t.Parallel()

	scheme := enrichScheme(t)
	node := nodeWithDiscovered("target-1",
		`{"`+discoveredTestPubKey+`":{"IP":"203.0.113.7","Port":51820}}`,
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	r := &ClusterMeshReconciler{}
	original := desiredPeer("peer-1", discoveredTestPubKey, "203.0.113.7", 51820)
	desired := []*kilov1alpha1.Peer{original}

	got, err := r.enrichEndpointsFromDiscovered(context.Background(), discardLogger(), fc, desired)

	require.NoError(t, err)
	require.Len(t, got, 1)
	// Same value → the original Peer pointer is returned unchanged.
	assert.Same(t, original, got[0])
}

// TestEnrichEndpointsFromDiscovered_NoDiscoveredData is the best-effort
// fallback: with no discovered-endpoint annotations on any target node, the
// original peers are returned unchanged.
func TestEnrichEndpointsFromDiscovered_NoDiscoveredData(t *testing.T) {
	t.Parallel()

	scheme := enrichScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ClusterMeshReconciler{}
	original := desiredPeer("peer-1", discoveredTestPubKey, "10.0.0.5", 51820)
	desired := []*kilov1alpha1.Peer{original}

	got, err := r.enrichEndpointsFromDiscovered(context.Background(), discardLogger(), fc, desired)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Same(t, original, got[0])
}

// TestEnrichEndpointsFromDiscovered_NoMatchingKey verifies that a peer whose
// public key is not present in any discovered-endpoint map is left unchanged.
func TestEnrichEndpointsFromDiscovered_NoMatchingKey(t *testing.T) {
	t.Parallel()

	scheme := enrichScheme(t)
	node := nodeWithDiscovered("target-1",
		`{"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=":{"IP":"198.51.100.42","Port":51820}}`,
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	r := &ClusterMeshReconciler{}
	original := desiredPeer("peer-1", discoveredTestPubKey, "10.0.0.5", 51820)
	desired := []*kilov1alpha1.Peer{original}

	got, err := r.enrichEndpointsFromDiscovered(context.Background(), discardLogger(), fc, desired)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Same(t, original, got[0])
}
