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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
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
