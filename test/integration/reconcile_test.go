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

// TestHappyPath_TwoClusters verifies that a two-cluster ClusterMesh causes the
// controller to create Peers in each cluster for the other cluster's nodes.
//
// Topology:
//   - local cluster: 2 nodes (local-node-1, local-node-2)
//   - remote cluster: 2 nodes (remote-node-1, remote-node-2)
//
// After reconciliation:
//   - remote cluster must have 2 Peers (for local nodes) + 0 anchor (no serviceCIDR)
//   - local cluster must have 2 Peers (for remote nodes) + 0 anchor
//   - ClusterMesh status: registeredPeers=2 for each cluster, Ready=True
func TestHappyPath_TwoClusters(t *testing.T) {
	ctx := context.Background()

	// Create nodes in the local envtest.
	localNode1 := makeNode("local-node-1", "10.1.0.0/24", "10.100.0.1/32", "pubkey-local-1", "")
	localNode2 := makeNode("local-node-2", "10.1.1.0/24", "10.100.0.2/32", "pubkey-local-2", "")
	require.NoError(t, globalEnv.localClient.Create(ctx, localNode1))
	require.NoError(t, globalEnv.localClient.Create(ctx, localNode2))

	// Create nodes in the remote envtest.
	remoteNode1 := makeNode("remote-node-1", "10.2.0.0/24", "10.100.1.1/32", "pubkey-remote-1", "192.0.2.1:51820")
	remoteNode2 := makeNode("remote-node-2", "10.2.1.0/24", "10.100.1.2/32", "pubkey-remote-2", "")
	require.NoError(t, globalEnv.remoteClient.Create(ctx, remoteNode1))
	require.NoError(t, globalEnv.remoteClient.Create(ctx, remoteNode2))

	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, localNode1)
		_ = globalEnv.localClient.Delete(ctx, localNode2)
		_ = globalEnv.remoteClient.Delete(ctx, remoteNode1)
		_ = globalEnv.remoteClient.Delete(ctx, remoteNode2)
	})

	mesh := simpleMeshSpec("happy-path-mesh", "default")
	createMesh(t, mesh)

	t.Cleanup(func() { deleteMesh(t, mesh) })

	// First reconcile adds the finalizer only; second does the real work.
	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// --- assert Peers in the remote cluster (for local nodes) ---
	waitForPeerCount(t, globalEnv.remoteClient, mesh.Name, "local", 2, eventuallyTimeout)

	remotePeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, remotePeers,
		client.MatchingLabels{
			peer.LabelMesh:          mesh.Name,
			peer.LabelSourceCluster: "local",
		},
	))
	assert.Len(t, remotePeers.Items, 2)

	for _, p := range remotePeers.Items {
		assert.Equal(t, mesh.Name, p.Labels[peer.LabelMesh])
		assert.Equal(t, "local", p.Labels[peer.LabelSourceCluster])
		assert.NotEmpty(t, p.Spec.PublicKey)
		assert.NotEmpty(t, p.Spec.AllowedIPs)
	}

	// --- assert Peers in the local cluster (for remote nodes) ---
	waitForPeerCount(t, globalEnv.localClient, mesh.Name, "remote", 2, eventuallyTimeout)

	localPeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, localPeers,
		client.MatchingLabels{
			peer.LabelMesh:          mesh.Name,
			peer.LabelSourceCluster: "remote",
		},
	))
	assert.Len(t, localPeers.Items, 2)

	for _, p := range localPeers.Items {
		assert.Equal(t, mesh.Name, p.Labels[peer.LabelMesh])
		assert.Equal(t, "remote", p.Labels[peer.LabelSourceCluster])
		assert.NotEmpty(t, p.Spec.PublicKey)
		assert.NotEmpty(t, p.Spec.AllowedIPs)
	}

	// Verify that the peer with an explicit endpoint carries it through.
	var peerWithEndpoint *kilov1alpha1.Peer
	for i := range localPeers.Items {
		if localPeers.Items[i].Spec.Endpoint != nil {
			peerWithEndpoint = &localPeers.Items[i]
			break
		}
	}
	require.NotNil(t, peerWithEndpoint, "expected one peer to carry the force-endpoint")
	assert.Equal(t, uint32(51820), peerWithEndpoint.Spec.Endpoint.Port)

	// --- assert ClusterMesh status ---
	waitForCondition(t, globalEnv.localClient, mesh, "Ready", metav1.ConditionTrue, eventuallyTimeout)

	got := &v1alpha1.ClusterMesh{}
	require.NoError(t, globalEnv.localClient.Get(ctx, client.ObjectKeyFromObject(mesh), got))

	statusByName := make(map[string]v1alpha1.ClusterStatus, len(got.Status.Clusters))
	for _, cs := range got.Status.Clusters {
		statusByName[cs.Name] = cs
	}

	localStatus, ok := statusByName["local"]
	require.True(t, ok, "expected status entry for 'local' cluster")
	assert.Equal(t, 2, localStatus.RegisteredPeers)
	assert.Equal(t, 0, localStatus.SkippedNodes)

	remoteStatus, ok := statusByName["remote"]
	require.True(t, ok, "expected status entry for 'remote' cluster")
	assert.Equal(t, 2, remoteStatus.RegisteredPeers)
	assert.Equal(t, 0, remoteStatus.SkippedNodes)
}
