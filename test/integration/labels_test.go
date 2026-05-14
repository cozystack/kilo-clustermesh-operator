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

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// TestLabelIsolation_TwoMeshes verifies that two independent ClusterMesh objects
// produce Peers with distinct labels and that deleting one mesh removes only its
// own Peers, leaving the other mesh's Peers untouched.
//
// Topology:
//   - mesh-alpha: local podCIDR 10.0.0.0/16, remote podCIDR 10.10.0.0/16
//   - mesh-beta:  local podCIDR 10.20.0.0/16, remote podCIDR 10.30.0.0/16
//
// All four CIDRs are disjoint so the overlap validator accepts both meshes.
func TestLabelIsolation_TwoMeshes(t *testing.T) {
	ctx := context.Background()

	// --- alpha nodes ---
	alphaLocalNode := makeNode("alpha-local-node", "10.0.0.0/24", "10.100.10.1/32", "pubkey-alpha-local", "")
	alphaRemoteNode := makeNode("alpha-remote-node", "10.10.0.0/24", "10.100.11.1/32", "pubkey-alpha-remote", "192.0.2.10:51820")

	require.NoError(t, globalEnv.localClient.Create(ctx, alphaLocalNode))
	require.NoError(t, globalEnv.remoteClient.Create(ctx, alphaRemoteNode))

	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, alphaLocalNode)
		_ = globalEnv.remoteClient.Delete(ctx, alphaRemoteNode)
	})

	// --- beta nodes ---
	betaLocalNode := makeNode("beta-local-node", "10.20.0.0/24", "10.100.20.1/32", "pubkey-beta-local", "")
	betaRemoteNode := makeNode("beta-remote-node", "10.30.0.0/24", "10.100.21.1/32", "pubkey-beta-remote", "")

	require.NoError(t, globalEnv.localClient.Create(ctx, betaLocalNode))
	require.NoError(t, globalEnv.remoteClient.Create(ctx, betaRemoteNode))

	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, betaLocalNode)
		_ = globalEnv.remoteClient.Delete(ctx, betaRemoteNode)
	})

	// --- mesh-alpha ---
	meshAlpha := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mesh-alpha",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:          "local",
					Local:         true,
					PodCIDRs:      []string{"10.0.0.0/16"},
					WireguardCIDR: "10.100.10.0/24",
				},
				{
					Name:          "remote",
					PodCIDRs:      []string{"10.10.0.0/16"},
					WireguardCIDR: "10.100.11.0/24",
				},
			},
		},
	}

	// --- mesh-beta ---
	meshBeta := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mesh-beta",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:          "local",
					Local:         true,
					PodCIDRs:      []string{"10.20.0.0/16"},
					WireguardCIDR: "10.100.20.0/24",
				},
				{
					Name:          "remote",
					PodCIDRs:      []string{"10.30.0.0/16"},
					WireguardCIDR: "10.100.21.0/24",
				},
			},
		},
	}

	createMesh(t, meshAlpha)
	createMesh(t, meshBeta)

	// Cleanup beta last (alpha is deleted mid-test to verify isolation).
	t.Cleanup(func() { deleteMesh(t, meshBeta) })

	// Reconcile alpha: finalizer pass + real work.
	mustReconcile(t, meshAlpha)
	mustReconcile(t, meshAlpha)

	// Reconcile beta: finalizer pass + real work.
	mustReconcile(t, meshBeta)
	mustReconcile(t, meshBeta)

	// --- assert mesh-alpha Peers carry the correct label ---
	waitForPeerCount(t, globalEnv.remoteClient, meshAlpha.Name, "local", 1, eventuallyTimeout)
	waitForPeerCount(t, globalEnv.localClient, meshAlpha.Name, "remote", 1, eventuallyTimeout)

	alphaPeersRemote := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, alphaPeersRemote,
		client.MatchingLabels{peer.LabelMesh: meshAlpha.Name},
	))
	for _, p := range alphaPeersRemote.Items {
		assert.Equal(t, meshAlpha.Name, p.Labels[peer.LabelMesh],
			"peer %s must carry mesh-alpha label", p.Name)
	}

	alphaPeersLocal := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, alphaPeersLocal,
		client.MatchingLabels{peer.LabelMesh: meshAlpha.Name},
	))
	for _, p := range alphaPeersLocal.Items {
		assert.Equal(t, meshAlpha.Name, p.Labels[peer.LabelMesh],
			"peer %s must carry mesh-alpha label", p.Name)
	}

	// --- assert mesh-beta Peers carry the correct label ---
	waitForPeerCount(t, globalEnv.remoteClient, meshBeta.Name, "local", 1, eventuallyTimeout)
	waitForPeerCount(t, globalEnv.localClient, meshBeta.Name, "remote", 1, eventuallyTimeout)

	betaPeersRemote := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, betaPeersRemote,
		client.MatchingLabels{peer.LabelMesh: meshBeta.Name},
	))
	for _, p := range betaPeersRemote.Items {
		assert.Equal(t, meshBeta.Name, p.Labels[peer.LabelMesh],
			"peer %s must carry mesh-beta label", p.Name)
	}

	betaPeersLocal := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, betaPeersLocal,
		client.MatchingLabels{peer.LabelMesh: meshBeta.Name},
	))
	for _, p := range betaPeersLocal.Items {
		assert.Equal(t, meshBeta.Name, p.Labels[peer.LabelMesh],
			"peer %s must carry mesh-beta label", p.Name)
	}

	// --- delete mesh-alpha and verify isolation ---
	deleteMesh(t, meshAlpha)

	// All alpha Peers must be gone from both clusters.
	alphaAfterDeletion := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, alphaAfterDeletion,
		client.MatchingLabels{peer.LabelMesh: meshAlpha.Name},
	))
	assert.Empty(t, alphaAfterDeletion.Items,
		"expected no alpha peers in remote cluster after mesh-alpha deletion")

	alphaAfterDeletionLocal := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, alphaAfterDeletionLocal,
		client.MatchingLabels{peer.LabelMesh: meshAlpha.Name},
	))
	assert.Empty(t, alphaAfterDeletionLocal.Items,
		"expected no alpha peers in local cluster after mesh-alpha deletion")

	// Beta Peers must still exist, untouched.
	betaAfterAlphaDeletion := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, betaAfterAlphaDeletion,
		client.MatchingLabels{peer.LabelMesh: meshBeta.Name},
	))
	assert.NotEmpty(t, betaAfterAlphaDeletion.Items,
		"expected beta peers in remote cluster to survive mesh-alpha deletion")

	betaAfterAlphaDeletionLocal := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, betaAfterAlphaDeletionLocal,
		client.MatchingLabels{peer.LabelMesh: meshBeta.Name},
	))
	assert.NotEmpty(t, betaAfterAlphaDeletionLocal.Items,
		"expected beta peers in local cluster to survive mesh-alpha deletion")
}
