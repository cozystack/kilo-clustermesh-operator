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

package v1alpha1_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

// TestClusterMeshGVK verifies the GVK registered for ClusterMesh.
func TestClusterMeshGVK(t *testing.T) {
	t.Parallel()

	want := schema.GroupVersion{Group: "kilo.squat.ai", Version: "v1alpha1"}
	assert.Equal(t, want, v1alpha1.GroupVersion)
	assert.Equal(t, want, v1alpha1.SchemeGroupVersion)
}

// TestClusterMeshJSONRoundTrip marshals a fully-populated ClusterMesh to JSON
// and back, then verifies the result is equal to the original.
func TestClusterMeshJSONRoundTrip(t *testing.T) {
	t.Parallel()

	secretRef := &v1alpha1.SecretKeyRef{Name: "remote-kubeconfig", Key: "kubeconfig"}
	original := v1alpha1.ClusterMesh{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kilo.squat.ai/v1alpha1",
			Kind:       "ClusterMesh",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mesh",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "local-cluster",
					Local:           true,
					AllowedNetworks: []string{"10.0.0.0/16", "fd00::/48", "172.30.0.0/24", "10.96.0.0/12", "192.168.100.0/24"},
					WireguardPort:   51820,
				},
				{
					Name:                "remote-cluster",
					KubeconfigSecretRef: secretRef,
					AllowedNetworks:     []string{"10.1.0.0/16", "172.30.1.0/24"},
					WireguardPort:       52000,
				},
			},
		},
		Status: v1alpha1.ClusterMeshStatus{
			Clusters: []v1alpha1.ClusterStatus{
				{Name: "local-cluster", RegisteredPeers: 3, SkippedNodes: 0},
				{Name: "remote-cluster", RegisteredPeers: 2, SkippedNodes: 1},
			},
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "AllPeersRegistered",
					Message:            "all clusters fully peered",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal must not fail")

	var got v1alpha1.ClusterMesh
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal must not fail")

	// Compare fields that survive a JSON round-trip (timestamps lose monotonic clock).
	assert.Equal(t, original.TypeMeta, got.TypeMeta)
	assert.Equal(t, original.Name, got.Name)
	assert.Equal(t, original.Namespace, got.Namespace)
	assert.Equal(t, original.Spec, got.Spec)
	assert.Equal(t, original.Status.Clusters, got.Status.Clusters)
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, original.Status.Conditions[0].Type, got.Status.Conditions[0].Type)
	assert.Equal(t, original.Status.Conditions[0].Status, got.Status.Conditions[0].Status)
	assert.Equal(t, original.Status.Conditions[0].Reason, got.Status.Conditions[0].Reason)
}

// TestClusterMeshDeepCopy verifies that DeepCopy produces an independent copy:
// mutations to the original must not affect the copy.
func TestClusterMeshDeepCopy(t *testing.T) {
	t.Parallel()

	original := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh", Namespace: "ns"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "c1",
					Local:           true,
					AllowedNetworks: []string{"10.0.0.0/16", "172.30.0.0/24", "192.168.0.0/24"},
				},
			},
		},
	}

	copied := original.DeepCopy()
	require.NotNil(t, copied)

	// Mutate the original after copying.
	original.Spec.Clusters[0].Name = "mutated"
	original.Spec.Clusters[0].AllowedNetworks[0] = "99.99.99.0/24"
	original.Spec.Clusters[0].AllowedNetworks[2] = "1.2.3.0/24"

	// The copy must be unchanged.
	assert.Equal(t, "c1", copied.Spec.Clusters[0].Name)
	assert.Equal(t, "10.0.0.0/16", copied.Spec.Clusters[0].AllowedNetworks[0])
	assert.Equal(t, "192.168.0.0/24", copied.Spec.Clusters[0].AllowedNetworks[2])
}

// TestClusterEntryAllCIDRs verifies that AllCIDRs returns the flat
// AllowedNetworks list verbatim, preserving order.
func TestClusterEntryAllCIDRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry v1alpha1.ClusterEntry
		want  []string
	}{
		{
			name: "single entry",
			entry: v1alpha1.ClusterEntry{
				AllowedNetworks: []string{"10.0.0.0/16"},
			},
			want: []string{"10.0.0.0/16"},
		},
		{
			name: "pod and wireguard CIDRs",
			entry: v1alpha1.ClusterEntry{
				AllowedNetworks: []string{"10.0.0.0/16", "172.30.0.0/24"},
			},
			want: []string{"10.0.0.0/16", "172.30.0.0/24"},
		},
		{
			name: "with service and additional CIDRs",
			entry: v1alpha1.ClusterEntry{
				AllowedNetworks: []string{"10.0.0.0/16", "fd00::/48", "172.30.0.0/24", "10.96.0.0/12", "192.168.0.0/24"},
			},
			want: []string{"10.0.0.0/16", "fd00::/48", "172.30.0.0/24", "10.96.0.0/12", "192.168.0.0/24"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.entry.AllCIDRs()
			assert.Equal(t, tc.want, got)
		})
	}
}
