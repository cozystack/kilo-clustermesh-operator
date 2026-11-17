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

package kilonode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// When two nodes report different endpoints for the same key, the winner must be
// deterministic regardless of the order the nodes are listed in (the real
// informer cache List iterates a Go map). The lowest-name node wins. Identical
// values could not tell a sorted result from an unsorted one, so the two nodes
// here deliberately disagree.
func TestDiscoveredEndpointsFromNodes_DeterministicAcrossOrder(t *testing.T) {
	t.Parallel()

	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	node := func(name, ip string) corev1.Node {
		return corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					AnnotationDiscoveredEndpoints: `{"` + key + `":{"IP":"` + ip + `","Port":51820}}`,
				},
			},
		}
	}

	nodeA := node("node-a", "203.0.113.1")
	nodeB := node("node-b", "203.0.113.2")

	for _, order := range [][]corev1.Node{{nodeA, nodeB}, {nodeB, nodeA}} {
		got := discoveredEndpointsFromNodes(order)
		assert.Equal(t, "203.0.113.1:51820", got[key],
			"the lowest-name node's endpoint must win regardless of list order")
	}
}
