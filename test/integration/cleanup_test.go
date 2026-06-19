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

// Preservation of peers whose source-cluster IS present in spec is covered by
// the unit test TestDeleteStaleSourceClusters_NoStaleEntriesIsNoOp in
// internal/peer/reconciler_test.go. An equivalent integration assertion would
// require backing the source-cluster with real nodes; otherwise the existing
// per-pair ReconcilePeers sweep (independent of the new cleanup) computes
// desired=[] and removes the planted peer as an in-pair orphan, masking the
// behaviour this layer is meant to verify.

// TestCleanupStaleSourceClusters_SweepsClustersOutsideSpec verifies the
// cross-CR cleanup property: a peer left in a cluster that is in the
// operator's registry (because another ClusterMesh names it) but is NOT
// in THIS mesh's spec.Clusters must still be swept. This is the case
// that breaks when cleanup only walks spec.Clusters: the "removed
// target" half of the stale-peer problem.
//
// Setup: this mesh's spec contains "local" plus a placeholder
// "ghost-elsewhere" that is not in the registry (the CRD requires at
// least two cluster entries; ghost-elsewhere satisfies that without
// covering the "remote" cluster the registry knows about). The peer
// under test is planted in remote — which is reachable through the
// registry (sibling-mesh kubeconfig in real deployments) but is NOT
// referenced by this mesh's spec. The cross-CR sweep must visit remote
// and delete the peer.
func TestCleanupStaleSourceClusters_SweepsClustersOutsideSpec(t *testing.T) {
	ctx := context.Background()

	mesh := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cleanup-cross-cr-mesh",
			Namespace: "default",
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "local",
					Local:           true,
					AllowedNetworks: []string{"10.1.0.0/16", "10.100.0.0/24"},
				},
				{
					// Placeholder to satisfy the CRD's minItems=2 on
					// spec.clusters. Not in the registry — the
					// registry exposes "local" and "remote", so this
					// entry is effectively ignored by reconcile.
					Name:            "ghost-elsewhere",
					AllowedNetworks: []string{"10.2.0.0/16", "10.100.1.0/24"},
				},
			},
		},
	}
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	// Plant a peer in remote whose source-cluster is not in this mesh's
	// spec. Without cross-cluster sweep, reconcile would never visit
	// remote (it's not in spec) and the peer would persist forever.
	stalePeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "stale--ghost--in-remote",
			Labels: peer.Labels(mesh.Name, "ghost-cluster"),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: []string{"10.77.0.0/16"},
			PublicKey:  "pubkey-stale-cross-cr",
		},
	}
	require.NoError(t, globalEnv.remoteClient.Create(ctx, stalePeer))

	mustReconcile(t, mesh)
	mustReconcile(t, mesh)

	assertPeerExists(t, globalEnv.remoteClient, "stale--ghost--in-remote", false)
}

// TestCleanupStaleSourceClusters_IgnoresPeersFromOtherMeshes verifies that
// peers labeled for a different but LIVING ClusterMesh are not touched by
// the per-source sweep, even if their source-cluster is unknown to the
// reconciling mesh. The foreign mesh must exist as a CR — otherwise the
// orphan-mesh sweep (a separate defense-in-depth pass) would legitimately
// delete it, and that property is covered by
// TestCleanupOrphanMeshPeers_DeletesGhostsAfterCRGone.
func TestCleanupStaleSourceClusters_IgnoresPeersFromOtherMeshes(t *testing.T) {
	ctx := context.Background()

	mesh := simpleMeshSpec("cleanup-isolation-mesh", "default")
	createMesh(t, mesh)
	t.Cleanup(func() { deleteMesh(t, mesh) })

	// Sibling ClusterMesh CR that "owns" the foreign peer below. Keeping it
	// alive in the namespace ensures the orphan-mesh sweep treats its peer
	// label as legitimate and does not delete it; the property under test
	// is per-CR isolation of the source-cluster sweep, not orphan handling.
	sibling := simpleMeshSpec("cleanup-isolation-sibling-mesh", "default")
	createMesh(t, sibling)
	t.Cleanup(func() { deleteMesh(t, sibling) })

	// Peer belongs to the sibling mesh; the reconciling mesh
	// (cleanup-isolation-mesh) must not delete it even though its
	// source-cluster ("ghost-cluster") is not part of cleanup-isolation-mesh's
	// spec — the per-source sweep is scoped to peers labelled with the
	// reconciling mesh, not foreign ones.
	foreignPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "foreign--ghost--node",
			Labels: peer.Labels(sibling.Name, "ghost-cluster"),
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

// TestCleanupOrphanMeshPeers_DeletesGhostsAfterCRGone verifies the
// defense-in-depth sweep: peers labeled for a ClusterMesh CR that no longer
// exists (because the finalizer was bypassed: force-delete, manual
// finalizer removal, operator crashloop) must be deleted by any live
// reconcile of any surviving mesh.
//
// Setup: plant a Peer labeled with a "ghost-mesh" name that has no
// corresponding ClusterMesh object. Then reconcile a live mesh — the sweep
// must walk the registry and delete that orphan.
func TestCleanupOrphanMeshPeers_DeletesGhostsAfterCRGone(t *testing.T) {
	ctx := context.Background()

	// Live mesh that drives the reconcile.
	live := simpleMeshSpec("orphan-sweep-live-mesh", "default")
	createMesh(t, live)
	t.Cleanup(func() { deleteMesh(t, live) })

	// Orphan peer: labeled with a mesh name that does NOT correspond to
	// any ClusterMesh CR in the namespace. Mirrors the production
	// failure mode where a tenant CR was force-deleted, leaving its
	// peers as ghosts no per-CR reconcile would ever revisit.
	orphan := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ghost--ghost-cluster--node-zero",
			Labels: peer.Labels("ghost-mesh-deleted", "ghost-cluster"),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: []string{"10.66.0.0/16"},
			PublicKey:  "pubkey-orphan",
		},
	}
	require.NoError(t, globalEnv.remoteClient.Create(ctx, orphan))

	// Sanity: orphan exists before reconcile.
	assertPeerExists(t, globalEnv.remoteClient, "ghost--ghost-cluster--node-zero", true)

	// First reconcile adds the finalizer; second runs the cleanup passes.
	mustReconcile(t, live)
	mustReconcile(t, live)

	assertPeerExists(t, globalEnv.remoteClient, "ghost--ghost-cluster--node-zero", false)
}

// TestCleanupOrphanMeshPeers_LeavesPeersOfLivingMeshAlone guards against
// a regression where the orphan sweep deletes peers whose mesh CR DOES
// exist. The peer in this test belongs to the live mesh itself — it must
// survive every reconcile (subject only to the per-CR sweeps that apply
// when the source-cluster is invalid).
func TestCleanupOrphanMeshPeers_LeavesPeersOfLivingMeshAlone(t *testing.T) {
	ctx := context.Background()

	live := simpleMeshSpec("orphan-sweep-keep-mesh", "default")
	createMesh(t, live)
	t.Cleanup(func() { deleteMesh(t, live) })

	// Peer belongs to the live mesh; its source-cluster IS in the mesh's
	// spec, so neither per-CR nor orphan sweep should touch it.
	live.Spec.Clusters[0].Name = "local"
	survivor := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "survivor--local--node-a",
			Labels: peer.Labels(live.Name, "local"),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: []string{"10.1.0.0/24"},
			PublicKey:  "pubkey-survivor",
		},
	}
	// Plant in a cluster that's NOT in spec (remote is not iterated by
	// the per-pair ReconcilePeers source=local because local has no
	// nodes; only the orphan sweep would touch this peer). Same idea
	// — the survivor must remain because its mesh label points at a
	// living CR.
	require.NoError(t, globalEnv.localClient.Create(ctx, survivor))
	t.Cleanup(func() {
		_ = globalEnv.localClient.Delete(ctx, survivor)
	})

	mustReconcile(t, live)
	mustReconcile(t, live)

	assertPeerExists(t, globalEnv.localClient, "survivor--local--node-a", true)
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
