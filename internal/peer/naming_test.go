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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"

	"github.com/squat/kilo-clustermesh-operator/internal/peer"
)

const (
	testMeshName      = "my-mesh"
	testSourceCluster = "my-cluster"
)

func TestName_SimpleInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		meshName      string
		sourceCluster string
		nodeName      string
		want          string
	}{
		{
			name:          "all lowercase alphanumeric",
			meshName:      "mesh1",
			sourceCluster: "cluster-a",
			nodeName:      "node-1",
			want:          "mesh1--cluster-a--node-1",
		},
		{
			name:          "single-char segments",
			meshName:      "m",
			sourceCluster: "c",
			nodeName:      "n",
			want:          "m--c--n",
		},
		{
			name:          "numeric segments",
			meshName:      "mesh0",
			sourceCluster: "cluster0",
			nodeName:      "node0",
			want:          "mesh0--cluster0--node0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := peer.Name(tc.meshName, tc.sourceCluster, tc.nodeName)

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestName_LongInputsTruncated(t *testing.T) {
	t.Parallel()

	// Build inputs that will produce a raw string longer than 253 chars.
	longMesh := strings.Repeat("a", 100)
	longCluster := strings.Repeat("b", 100)
	longNode := strings.Repeat("c", 100)

	got := peer.Name(longMesh, longCluster, longNode)

	assert.LessOrEqual(t, len(got), 253, "name must not exceed 253 chars")
	assert.NotEmpty(t, got)

	// Hash suffix: last segment after final "-" should be 16 hex chars.
	parts := strings.Split(got, "-")
	hashSuffix := parts[len(parts)-1]

	assert.Len(t, hashSuffix, 16, "hash suffix must be 16 hex chars")
}

func TestName_DNSSanitization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		meshName      string
		sourceCluster string
		nodeName      string
		wantContains  []string // substrings the result must contain
		wantAbsent    []string // substrings the result must NOT contain
	}{
		{
			name:          "uppercase converted to lowercase",
			meshName:      "Mesh",
			sourceCluster: "ClusterA",
			nodeName:      "NodeOne",
			wantContains:  []string{"mesh", "clustera", "nodeone"},
			wantAbsent:    []string{"M", "C", "N"},
		},
		{
			name:          "underscores replaced with dashes",
			meshName:      "my_mesh",
			sourceCluster: "my_cluster",
			nodeName:      "my_node",
			wantContains:  []string{"my-mesh", "my-cluster", "my-node"},
			wantAbsent:    []string{"_"},
		},
		{
			name:          "consecutive invalid chars collapsed to single dash",
			meshName:      "mesh__name",
			sourceCluster: "cluster",
			nodeName:      "node",
			wantAbsent:    []string{"__", "--mesh"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := peer.Name(tc.meshName, tc.sourceCluster, tc.nodeName)

			for _, sub := range tc.wantContains {
				assert.Contains(t, got, sub)
			}

			for _, sub := range tc.wantAbsent {
				assert.NotContains(t, got, sub)
			}

			assert.NotEmpty(t, got)
			assert.False(t, strings.HasPrefix(got, "-"), "must not start with dash")
			assert.False(t, strings.HasPrefix(got, "."), "must not start with dot")
			assert.False(t, strings.HasSuffix(got, "-"), "must not end with dash")
			assert.False(t, strings.HasSuffix(got, "."), "must not end with dot")
		})
	}
}

func TestName_Deterministic(t *testing.T) {
	t.Parallel()

	meshName := "test-mesh"
	sourceCluster := "source-cluster"
	nodeName := "worker-node-42"

	first := peer.Name(meshName, sourceCluster, nodeName)
	second := peer.Name(meshName, sourceCluster, nodeName)
	third := peer.Name(meshName, sourceCluster, nodeName)

	assert.Equal(t, first, second)
	assert.Equal(t, second, third)
}

func TestName_DifferentInputsProduceDifferentNames(t *testing.T) {
	t.Parallel()

	nameA := peer.Name("mesh", "clusterA", "node1")
	nameB := peer.Name("mesh", "clusterB", "node1")

	assert.NotEqual(t, nameA, nameB, "different source clusters must produce different names")
}

func TestName_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		meshName      string
		sourceCluster string
		nodeName      string
	}{
		{
			name:          "empty mesh name",
			meshName:      "",
			sourceCluster: "cluster",
			nodeName:      "node",
		},
		{
			name:          "empty source cluster",
			meshName:      "mesh",
			sourceCluster: "",
			nodeName:      "node",
		},
		{
			name:          "empty node name",
			meshName:      "mesh",
			sourceCluster: "cluster",
			nodeName:      "",
		},
		{
			name:          "all empty",
			meshName:      "",
			sourceCluster: "",
			nodeName:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Must not panic and must produce a result ≤ 253 chars.
			got := peer.Name(tc.meshName, tc.sourceCluster, tc.nodeName)

			require.LessOrEqual(t, len(got), 253)

			if got != "" {
				assert.False(t, strings.HasPrefix(got, "-"), "must not start with dash")
				assert.False(t, strings.HasPrefix(got, "."), "must not start with dot")
				assert.False(t, strings.HasSuffix(got, "-"), "must not end with dash")
				assert.False(t, strings.HasSuffix(got, "."), "must not end with dot")
			}
		})
	}
}

func TestLabels_ReturnsCorrectMap(t *testing.T) {
	t.Parallel()

	got := peer.Labels(testMeshName, testSourceCluster)

	require.Len(t, got, 2)
	assert.Equal(t, testMeshName, got[peer.LabelMesh])
	assert.Equal(t, testSourceCluster, got[peer.LabelSourceCluster])
}

func TestLabels_DifferentInputsProduceDifferentMaps(t *testing.T) {
	t.Parallel()

	labelsA := peer.Labels("mesh-a", "cluster-x")
	labelsB := peer.Labels("mesh-b", "cluster-x")

	assert.NotEqual(t, labelsA[peer.LabelMesh], labelsB[peer.LabelMesh])
	assert.Equal(t, labelsA[peer.LabelSourceCluster], labelsB[peer.LabelSourceCluster])
}

func TestOrphanSelector_MatchesPeerWithCorrectLabels(t *testing.T) {
	t.Parallel()

	meshName := "my-mesh"
	sourceCluster := "my-cluster"

	sel := peer.OrphanSelector(meshName, sourceCluster)

	matchingPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Labels: peer.Labels(meshName, sourceCluster),
		},
	}

	assert.True(t, sel.Matches(matchingLabels(matchingPeer.Labels)))
}

func TestOrphanSelector_DoesNotMatchPeerWithDifferentMesh(t *testing.T) {
	t.Parallel()

	sel := peer.OrphanSelector("mesh-a", "cluster-x")

	otherPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Labels: peer.Labels("mesh-b", "cluster-x"),
		},
	}

	assert.False(t, sel.Matches(matchingLabels(otherPeer.Labels)))
}

func TestOrphanSelector_DoesNotMatchPeerWithDifferentCluster(t *testing.T) {
	t.Parallel()

	sel := peer.OrphanSelector("mesh-a", "cluster-x")

	otherPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Labels: peer.Labels("mesh-a", "cluster-y"),
		},
	}

	assert.False(t, sel.Matches(matchingLabels(otherPeer.Labels)))
}

func TestOrphanSelector_DoesNotMatchPeerWithNoLabels(t *testing.T) {
	t.Parallel()

	sel := peer.OrphanSelector("mesh-a", "cluster-x")

	unlabeledPeer := &kilov1alpha1.Peer{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{},
		},
	}

	assert.False(t, sel.Matches(matchingLabels(unlabeledPeer.Labels)))
}

// matchingLabels wraps a plain map so it satisfies labels.Labels interface.
type matchingLabels map[string]string

func (m matchingLabels) Has(key string) bool              { _, ok := m[key]; return ok }
func (m matchingLabels) Get(key string) string            { return m[key] }
func (m matchingLabels) Lookup(key string) (string, bool) { v, ok := m[key]; return v, ok }
