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

	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// TestCleanupStaleSourceClusters_RemovesPeersOfRemovedCluster verifies the
// regression fix for the bug observed on dev3: when a cluster entry is
// removed from a ClusterMesh's spec.Clusters, ReconcilePeers stops being
// invoked with that source-cluster, and its Peer objects in surviving
// clusters would otherwise persist forever (with unreachable endpoints,
// confusing Kilo on the surviving nodes).
//
// The reconciler must now sweep, on every tick, every Peer in every cluster
// in spec.Clusters whose LabelSourceCluster names a cluster no longer in
// the spec.
func TestCleanupStaleSourceClusters_RemovesPeersOfRemovedCluster(t *testing.T) {
	ctx := context.Background()

	mesh := simpleMeshSpec("cleanup-stale-source-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	// Plant a stale peer in BOTH clusters: label says it belongs to this
	// mesh but its source-cluster ("ghost-cluster") is not in spec.
	// This is exactly what an older reconcile would have left behind after
	// the user shrank spec.Clusters from [local, remote, ghost-cluster] to
	// [local, remote].
	stalePeers := []*kilov1alpha1.Peer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "stale--ghost--local-side",
				Labels: peer.Labels(mesh.Name, "ghost-cluster"),
			},
			Spec: kilov1alpha1.PeerSpec{
				AllowedIPs: []string{"10.99.0.0/16"},
				PublicKey:  "pubkey-stale-local",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "stale--ghost--remote-side",
				Labels: peer.Labels(mesh.Name, "ghost-cluster"),
			},
			Spec: kilov1alpha1.PeerSpec{
				AllowedIPs: []string{"10.99.0.0/16"},
				PublicKey:  "pubkey-stale-remote",
			},
		},
	}

	require.NoError(t, globalEnv.localClient.Create(ctx, stalePeers[0].DeepCopy()))
	require.NoError(t, globalEnv.remoteClient.Create(ctx, stalePeers[1].DeepCopy()))

	// Sanity check: stale peers exist before reconcile.
	assertPeerExists(t, globalEnv.localClient, "stale--ghost--local-side", true)
	assertPeerExists(t, globalEnv.remoteClient, "stale--ghost--remote-side", true)

	// Drive reconcile: first call adds the finalizer; second performs the
	// real work including stale-source-cluster cleanup.
	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// Both stale peers must be gone from their respective clusters.
	assertPeerExists(t, globalEnv.localClient, "stale--ghost--local-side", false)
	assertPeerExists(t, globalEnv.remoteClient, "stale--ghost--remote-side", false)
}

// TestCleanupStaleSourceClusters_PreservesPeersForValidSourceClusters verifies
// the cleanup does not accidentally delete peers whose source-cluster IS
// present in spec.Clusters.
func TestCleanupStaleSourceClusters_PreservesPeersForValidSourceClusters(t *testing.T) {
	ctx := context.Background()

	mesh := simpleMeshSpec("cleanup-preserve-valid-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	// Plant a valid peer in remote whose source-cluster is "local"
	// (in spec) — the cleanup MUST leave it alone.
	validPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "valid--local--node-one",
			Labels: peer.Labels(mesh.Name, "local"),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: []string{"10.1.0.0/24"},
			PublicKey:  "pubkey-valid",
		},
	}
	require.NoError(t, globalEnv.remoteClient.Create(ctx, validPeer))

	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	// Peer still there.
	assertPeerExists(t, globalEnv.remoteClient, "valid--local--node-one", true)
}

// TestCleanupStaleSourceClusters_IgnoresPeersFromOtherMeshes verifies that
// peers labeled for a different ClusterMesh are not touched, even if their
// source-cluster is unknown to this mesh.
func TestCleanupStaleSourceClusters_IgnoresPeersFromOtherMeshes(t *testing.T) {
	ctx := context.Background()

	mesh := simpleMeshSpec("cleanup-isolation-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	// Peer belongs to a different mesh; even though its source-cluster is
	// nonsensical from our mesh's point of view, we must not touch it.
	foreignPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "foreign--ghost--node",
			Labels: peer.Labels("some-other-mesh", "ghost-cluster"),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: []string{"10.50.0.0/16"},
			PublicKey:  "pubkey-foreign",
		},
	}
	require.NoError(t, globalEnv.remoteClient.Create(ctx, foreignPeer))
	t.Cleanup(func() {
		_ = globalEnv.remoteClient.Delete(ctx, foreignPeer)
	})

	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	assertPeerExists(t, globalEnv.remoteClient, "foreign--ghost--node", true)
}

func assertPeerExists(t *testing.T, cl client.Client, name string, want bool) {
	t.Helper()

	got := &kilov1alpha1.Peer{}
	err := cl.Get(context.Background(), client.ObjectKey{Name: name}, got)

	if want {
		assert.NoError(t, err, "expected peer %q to exist", name)

		return
	}

	assert.Error(t, err, "expected peer %q to be gone", name)
}
