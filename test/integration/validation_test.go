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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// TestOverlappingNetworks_NoPeersCreated verifies that when two ClusterMesh
// objects declare overlapping pod CIDRs for DIFFERENT clusters, the reconciler
// sets NetworksOverlap=True on the conflicting mesh and does not create any
// Peers. (Same-name clusters across meshes are the legitimate shared-hub case
// and are covered by the unit tests in internal/validation/mesh_test.go.)
func TestOverlappingNetworks_NoPeersCreated(t *testing.T) {
	ctx := context.Background()

	// mesh-a "local-a" and mesh-b "local-b" both claim 10.0.0.0/16 — overlap
	// between two genuinely different clusters that happen to pick the same
	// pod CIDR.
	meshA := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overlap-mesh-a",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "local-a",
					Local:           true,
					AllowedNetworks: []string{"10.0.0.0/16", "10.100.0.0/24"},
				},
				{
					Name:            "remote-a",
					AllowedNetworks: []string{"10.3.0.0/16", "10.100.1.0/24"},
				},
			},
		},
	}

	meshB := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overlap-mesh-b",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "local-b",
					Local:           true,
					AllowedNetworks: []string{"10.0.0.0/16", "10.100.2.0/24"},
				},
				{
					Name:            "remote-b",
					AllowedNetworks: []string{"10.4.0.0/16", "10.100.3.0/24"},
				},
			},
		},
	}

	createMesh(t, meshA)
	t.Cleanup(func() { deleteMesh(t, meshA) })

	createMesh(t, meshB)
	t.Cleanup(func() { deleteMesh(t, meshB) })

	// First reconcile adds the finalizer; second performs real work.
	mustReconcile(t, meshA)
	mustReconcile(t, meshA)

	// mesh-a must have NetworksOverlap=True.
	waitForCondition(t, globalEnv.localClient, meshA, "NetworksOverlap", metav1.ConditionTrue, eventuallyTimeout)

	got := &v1alpha1.ClusterMesh{}
	require.NoError(t, globalEnv.localClient.Get(ctx, client.ObjectKeyFromObject(meshA), got))

	cond := apimeta.FindStatusCondition(got.Status.Conditions, "NetworksOverlap")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// No Peers should exist for mesh-a in either cluster.
	localPeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, localPeers,
		client.MatchingLabels{peer.LabelMesh: meshA.Name},
	))
	assert.Empty(t, localPeers.Items, "expected no peers in local cluster for mesh-a")

	remotePeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, remotePeers,
		client.MatchingLabels{peer.LabelMesh: meshA.Name},
	))
	assert.Empty(t, remotePeers.Items, "expected no peers in remote cluster for mesh-a")
}

// TestInvalidNodeWGIP_NodeSkipped verifies that a node whose WireGuard IP is
// outside every entry of the cluster's AllowedNetworks is skipped: only 1 Peer
// is created in the remote cluster (for the valid node), and the local cluster
// status shows skippedNodes=1.
func TestInvalidNodeWGIP_NodeSkipped(t *testing.T) {
	ctx := context.Background()

	// valid-node-wgip: WireGuard IP within 10.100.0.0/24.
	validNode := makeNode("wgip-valid-node", "10.1.0.0/24", "10.100.0.10/32", "pubkey-wgip-valid", "")
	// invalid-node-wgip: WireGuard IP NOT within 10.100.0.0/24 (it's in 10.200.0.0/24).
	invalidNode := makeNode("wgip-invalid-node", "10.1.1.0/24", "10.200.0.10/32", "pubkey-wgip-invalid", "")

	createNode(t, globalEnv.localClient, validNode)
	createNode(t, globalEnv.localClient, invalidNode)

	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, validNode)
		_ = globalEnv.localClient.Delete(ctx, invalidNode)
	})

	mesh := simpleMeshSpec("wgip-skip-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// Exactly 1 Peer in remote cluster (for the valid local node only).
	waitForPeerCount(t, globalEnv.remoteClient, mesh.Name, "local", 1, eventuallyTimeout)

	remotePeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, remotePeers,
		client.MatchingLabels{
			peer.LabelMesh:          mesh.Name,
			peer.LabelSourceCluster: "local",
		},
	))
	assert.Len(t, remotePeers.Items, 1, "expected exactly 1 peer for the valid node")

	// Status: local cluster must report skippedNodes=1.
	waitForCondition(t, globalEnv.localClient, mesh, "Ready", metav1.ConditionTrue, eventuallyTimeout)

	got := &v1alpha1.ClusterMesh{}
	require.NoError(t, globalEnv.localClient.Get(ctx, client.ObjectKeyFromObject(mesh), got))

	statusByName := make(map[string]v1alpha1.ClusterStatus, len(got.Status.Clusters))
	for _, cs := range got.Status.Clusters {
		statusByName[cs.Name] = cs
	}

	localStatus, ok := statusByName["local"]
	require.True(t, ok, "expected status entry for 'local' cluster")
	assert.Equal(t, 1, localStatus.RegisteredPeers)
	assert.Equal(t, 1, localStatus.SkippedNodes)
}

// TestMissingPublicKey_NodeSkipped verifies that a node without the
// kilo.squat.ai/key annotation is skipped: 0 Peers are created in the remote
// cluster and the local cluster status shows skippedNodes=1.
func TestMissingPublicKey_NodeSkipped(t *testing.T) {
	ctx := context.Background()

	// makeNode without public key: override the annotation map manually so there
	// is no kilo.squat.ai/key annotation at all.
	node := makeNode("no-pubkey-node", "10.1.0.0/24", "10.100.0.20/32", "", "")
	// makeNode sets an empty string for pubKey when "" is passed; remove the key
	// entirely so validatePublicKey skips it.
	delete(node.Annotations, "kilo.squat.ai/key")

	createNode(t, globalEnv.localClient, node)
	t.Cleanup(func() { _ = globalEnv.localClient.Delete(ctx, node) })

	mesh := simpleMeshSpec("no-pubkey-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// 0 Peers in remote cluster.
	waitForPeerCount(t, globalEnv.remoteClient, mesh.Name, "local", 0, eventuallyTimeout)

	remotePeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, remotePeers,
		client.MatchingLabels{
			peer.LabelMesh:          mesh.Name,
			peer.LabelSourceCluster: "local",
		},
	))
	assert.Empty(t, remotePeers.Items, "expected no peers when node has no public key")

	// Status: skippedNodes=1 for local cluster.
	waitForCondition(t, globalEnv.localClient, mesh, "Ready", metav1.ConditionTrue, eventuallyTimeout)

	got := &v1alpha1.ClusterMesh{}
	require.NoError(t, globalEnv.localClient.Get(ctx, client.ObjectKeyFromObject(mesh), got))

	statusByName := make(map[string]v1alpha1.ClusterStatus, len(got.Status.Clusters))
	for _, cs := range got.Status.Clusters {
		statusByName[cs.Name] = cs
	}

	localStatus, ok := statusByName["local"]
	require.True(t, ok, "expected status entry for 'local' cluster")
	assert.Equal(t, 0, localStatus.RegisteredPeers)
	assert.Equal(t, 1, localStatus.SkippedNodes)
}

// TestDeletion_PeersCleanedUp verifies that deleting a ClusterMesh causes the
// reconciler to remove all Peers from every cluster and then release the finalizer.
func TestDeletion_PeersCleanedUp(t *testing.T) {
	ctx := context.Background()

	localNode := makeNode("del-local-node", "10.1.0.0/24", "10.100.0.30/32", "pubkey-del-local", "")
	remoteNode := makeNode("del-remote-node", "10.2.0.0/24", "10.100.1.30/32", "pubkey-del-remote", "192.0.2.30:51820")

	createNode(t, globalEnv.localClient, localNode)
	createNode(t, globalEnv.remoteClient, remoteNode)

	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, localNode)
		_ = globalEnv.remoteClient.Delete(ctx, remoteNode)
	})

	mesh := simpleMeshSpec("deletion-mesh", "default")
	createMesh(t, mesh)

	// Bring the mesh to a healthy reconciled state.
	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// Confirm peers exist before deletion.
	waitForPeerCount(t, globalEnv.remoteClient, mesh.Name, "local", 1, eventuallyTimeout)
	waitForPeerCount(t, globalEnv.localClient, mesh.Name, "remote", 1, eventuallyTimeout)

	// deleteMesh triggers client.Delete then drives reconciliation until the
	// object is gone (finalizer removed), using the helper from helpers_test.go.
	deleteMesh(t, mesh)

	// After deletion all Peers must be gone from both clusters.
	localPeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.localClient.List(ctx, localPeers,
		client.MatchingLabels{peer.LabelMesh: mesh.Name},
	))
	assert.Empty(t, localPeers.Items, "expected no peers in local cluster after deletion")

	remotePeers := &kilov1alpha1.PeerList{}
	require.NoError(t, globalEnv.remoteClient.List(ctx, remotePeers,
		client.MatchingLabels{peer.LabelMesh: mesh.Name},
	))
	assert.Empty(t, remotePeers.Items, "expected no peers in remote cluster after deletion")
}
