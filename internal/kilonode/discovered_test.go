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

package kilonode_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
)

// nodeWithDiscovered returns a Node carrying the kilo.squat.ai/discovered-endpoints
// annotation with the given raw JSON value (empty string means no annotation).
func nodeWithDiscovered(name, rawJSON string) *corev1.Node {
	annotations := map[string]string{}
	if rawJSON != "" {
		annotations[kilonode.AnnotationDiscoveredEndpoints] = rawJSON
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
	}
}

func TestDiscoveredEndpointsByKey(t *testing.T) {
	t.Parallel()

	const pubKeyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	tests := []struct {
		name  string
		nodes []*corev1.Node
		want  map[string]string
	}{
		{
			name: "single valid global endpoint is parsed",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{"`+pubKeyA+`":{"IP":"203.0.113.7","Port":51820}}`),
			},
			want: map[string]string{pubKeyA: "203.0.113.7:51820"},
		},
		{
			name: "malformed annotation is skipped silently",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{not valid json`),
			},
			want: map[string]string{},
		},
		{
			name: "loopback IP is filtered out",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{"`+pubKeyA+`":{"IP":"127.0.0.1","Port":51820}}`),
			},
			want: map[string]string{},
		},
		{
			name: "link-local IP is filtered out",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{"`+pubKeyA+`":{"IP":"169.254.1.1","Port":51820}}`),
			},
			want: map[string]string{},
		},
		{
			name: "entry with empty IP or zero port is skipped",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{"`+pubKeyA+`":{"IP":"","Port":51820}}`),
				nodeWithDiscovered("n2", `{"`+pubKeyA+`":{"IP":"203.0.113.9","Port":0}}`),
			},
			want: map[string]string{},
		},
		{
			name:  "no annotation yields empty map",
			nodes: []*corev1.Node{nodeWithDiscovered("n1", "")},
			want:  map[string]string{},
		},
		{
			// Multiple nodes observing the same key report the same NAT egress
			// (the documented invariant), so the deduplicated result is stable
			// regardless of which node the fake client lists first.
			name: "same key observed by multiple nodes deduplicates to one entry",
			nodes: []*corev1.Node{
				nodeWithDiscovered("n1", `{"`+pubKeyA+`":{"IP":"203.0.113.7","Port":51820}}`),
				nodeWithDiscovered("n2", `{"`+pubKeyA+`":{"IP":"203.0.113.7","Port":51820}}`),
			},
			want: map[string]string{pubKeyA: "203.0.113.7:51820"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scheme := ensureScheme(t)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, n := range tc.nodes {
				builder = builder.WithObjects(n)
			}

			fc := builder.Build()

			got, err := kilonode.DiscoveredEndpointsByKey(context.Background(), fc)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
