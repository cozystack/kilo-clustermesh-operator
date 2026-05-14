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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

const (
	reconcilerTestMesh    = "test-mesh"
	reconcilerTestCluster = "cluster-a"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	sch := runtime.NewScheme()
	require.NoError(t, kilov1alpha1.AddToScheme(sch))

	return sch
}

func makePeer(name string, allowedIPs []string, publicKey string) *kilov1alpha1.Peer {
	return &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: peer.Labels(reconcilerTestMesh, reconcilerTestCluster),
		},
		Spec: kilov1alpha1.PeerSpec{
			AllowedIPs: allowedIPs,
			PublicKey:  publicKey,
		},
	}
}

func listPeers(t *testing.T, ctx context.Context, fc client.Client) []kilov1alpha1.Peer {
	t.Helper()

	result := &kilov1alpha1.PeerList{}
	err := fc.List(ctx, result, client.MatchingLabels(peer.Labels(reconcilerTestMesh, reconcilerTestCluster)))
	require.NoError(t, err)

	return result.Items
}

func TestReconcilePeers_EmptyDesiredEmptyExisting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, nil)

	require.NoError(t, err)
	assert.Empty(t, listPeers(t, ctx, fc))
}

func TestReconcilePeers_CreateNewPeers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	desired := []*kilov1alpha1.Peer{
		makePeer("peer-one", []string{"10.0.0.1/32"}, "pubkey-one"),
		makePeer("peer-two", []string{"10.0.0.2/32"}, "pubkey-two"),
	}

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, desired)

	require.NoError(t, err)

	items := listPeers(t, ctx, fc)
	assert.Len(t, items, 2)

	names := make(map[string]bool, len(items))
	for _, item := range items {
		names[item.Name] = true
	}

	assert.True(t, names["peer-one"])
	assert.True(t, names["peer-two"])
}

func TestReconcilePeers_ExistingPeerUnchanged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	existing := makePeer("peer-one", []string{"10.0.0.1/32"}, "pubkey-one")
	fc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(existing).Build()

	desired := []*kilov1alpha1.Peer{
		makePeer("peer-one", []string{"10.0.0.1/32"}, "pubkey-one"),
	}

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, desired)

	require.NoError(t, err)

	items := listPeers(t, ctx, fc)
	require.Len(t, items, 1)
	assert.Equal(t, "pubkey-one", items[0].Spec.PublicKey)
	assert.Equal(t, []string{"10.0.0.1/32"}, items[0].Spec.AllowedIPs)
}

func TestReconcilePeers_UpdateChangedPeer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	existing := makePeer("peer-one", []string{"10.0.0.1/32"}, "pubkey-old")
	fc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(existing).Build()

	desired := []*kilov1alpha1.Peer{
		makePeer("peer-one", []string{"10.0.0.1/32", "10.0.0.2/32"}, "pubkey-new"),
	}

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, desired)

	require.NoError(t, err)

	items := listPeers(t, ctx, fc)
	require.Len(t, items, 1)
	assert.Equal(t, "pubkey-new", items[0].Spec.PublicKey)
	assert.ElementsMatch(t, []string{"10.0.0.1/32", "10.0.0.2/32"}, items[0].Spec.AllowedIPs)
}

func TestReconcilePeers_DeleteOrphans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orphanOne := makePeer("orphan-one", []string{"10.0.1.1/32"}, "pubkey-x")
	orphanTwo := makePeer("orphan-two", []string{"10.0.1.2/32"}, "pubkey-y")
	fc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(orphanOne, orphanTwo).Build()

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, nil)

	require.NoError(t, err)
	assert.Empty(t, listPeers(t, ctx, fc))
}

func TestReconcilePeers_MixedCreateUpdateDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	existingChanged := makePeer("peer-update", []string{"10.0.0.5/32"}, "pubkey-old")
	orphan := makePeer("peer-orphan", []string{"10.0.0.9/32"}, "pubkey-gone")

	fc := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(existingChanged, orphan).
		Build()

	desired := []*kilov1alpha1.Peer{
		makePeer("peer-new", []string{"10.0.0.1/32"}, "pubkey-new"),
		makePeer("peer-update", []string{"10.0.0.5/32", "10.0.0.6/32"}, "pubkey-updated"),
	}

	err := peer.ReconcilePeers(ctx, fc, reconcilerTestMesh, reconcilerTestCluster, desired)

	require.NoError(t, err)

	items := listPeers(t, ctx, fc)
	require.Len(t, items, 2)

	byName := make(map[string]kilov1alpha1.Peer, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}

	// Created.
	peerNew, ok := byName["peer-new"]
	require.True(t, ok, "peer-new must exist")
	assert.Equal(t, "pubkey-new", peerNew.Spec.PublicKey)

	// Updated.
	peerUpdated, ok := byName["peer-update"]
	require.True(t, ok, "peer-update must exist")
	assert.Equal(t, "pubkey-updated", peerUpdated.Spec.PublicKey)
	assert.ElementsMatch(t, []string{"10.0.0.5/32", "10.0.0.6/32"}, peerUpdated.Spec.AllowedIPs)

	// Deleted.
	_, orphanStillPresent := byName["peer-orphan"]
	assert.False(t, orphanStillPresent, "peer-orphan must be deleted")
}
