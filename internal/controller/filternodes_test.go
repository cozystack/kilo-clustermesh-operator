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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
)

type noopEventRecorder struct{}

func (noopEventRecorder) Eventf(_ runtime.Object, _ runtime.Object, _, _, _, _ string, _ ...interface{}) {
}

// A node skipped for a WireGuard-IP collision is handed to ReconcilePeers for
// retention only when it would otherwise be peerable. An invalid loser must
// not keep stale routes advertised for the duration of the collision.
func TestFilterNodes_RetainsOnlyPeerableDuplicates(t *testing.T) {
	t.Parallel()

	r := &ClusterMeshReconciler{Recorder: noopEventRecorder{}}
	mesh := &v1alpha1.ClusterMesh{}
	entry := &v1alpha1.ClusterEntry{
		Name:            "src",
		AllowedNetworks: []string{"10.244.0.0/16", "10.4.0.0/16"},
	}

	// All three share keepaliveTestNode's WireGuard IP; node-a wins by name.
	winner := keepaliveTestNode("node-a")
	peerableLoser := keepaliveTestNode("node-b")
	invalidLoser := keepaliveTestNode("node-c")
	invalidLoser.Spec.PodCIDRs = []string{"192.168.0.0/24"} // outside AllowedNetworks

	nodes := []corev1.Node{*winner, *peerableLoser, *invalidLoser}

	valid, retained, skipped, _ := r.filterNodes(slog.New(slog.DiscardHandler), mesh, nodes, entry)

	assert.Len(t, valid, 1)
	assert.Equal(t, "node-a", valid[0].Name)
	assert.Equal(t, []string{"node-b"}, retained, "only the peerable loser is retained")
	assert.Equal(t, 2, skipped)
}

// TestFilterNodes_IneligibleNodeDoesNotWinCollision pins the winner of a
// WireGuard-IP collision to an eligible node. An invalid node (here missing its
// public key) that sorts first by name must not win the IP and starve the valid
// node of its Peer: the collision is resolved only among nodes that pass
// ValidateNode, so the valid higher-named node becomes the winner and the
// invalid one is skipped on its own merit.
func TestFilterNodes_IneligibleNodeDoesNotWinCollision(t *testing.T) {
	t.Parallel()

	r := &ClusterMeshReconciler{Recorder: noopEventRecorder{}}
	mesh := &v1alpha1.ClusterMesh{}
	entry := &v1alpha1.ClusterEntry{
		Name:            "src",
		AllowedNetworks: []string{"10.244.0.0/16", "10.4.0.0/16"},
	}

	// node-a sorts first but is ineligible (no public key); node-b is valid.
	// Both claim keepaliveTestNode's WireGuard IP.
	ineligible := keepaliveTestNode("node-a")
	delete(ineligible.Annotations, kilonode.AnnotationPublicKey)
	validNode := keepaliveTestNode("node-b")

	nodes := []corev1.Node{*ineligible, *validNode}

	valid, retained, skipped, _ := r.filterNodes(slog.New(slog.DiscardHandler), mesh, nodes, entry)

	require.Len(t, valid, 1)
	assert.Equal(t, "node-b", valid[0].Name, "the valid node must win the collision, not the lower-named invalid one")
	assert.Empty(t, retained, "the sole eligible claimant is not a duplicate")
	assert.Equal(t, 1, skipped, "only the ineligible node is skipped")
}
