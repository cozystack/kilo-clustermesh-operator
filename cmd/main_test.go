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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

func mesh(name string, clusters ...kilov1alpha1.ClusterEntry) kilov1alpha1.ClusterMesh {
	return kilov1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kilov1alpha1.ClusterMeshSpec{Clusters: clusters},
	}
}

func entry(name string, podCIDR string) kilov1alpha1.ClusterEntry {
	return kilov1alpha1.ClusterEntry{
		Name:          name,
		PodCIDRs:      []string{podCIDR},
		WireguardCIDR: "10.4.0.0/16",
	}
}

func TestMergeClusterSpecs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		meshes []kilov1alpha1.ClusterMesh
		want   []kilov1alpha1.ClusterEntry
	}{
		{
			name: "single mesh with multiple unique clusters preserves all and order",
			meshes: []kilov1alpha1.ClusterMesh{
				mesh("mesh1",
					entry("alpha", "10.0.0.0/16"),
					entry("beta", "10.1.0.0/16"),
					entry("gamma", "10.2.0.0/16"),
				),
			},
			want: []kilov1alpha1.ClusterEntry{
				entry("alpha", "10.0.0.0/16"),
				entry("beta", "10.1.0.0/16"),
				entry("gamma", "10.2.0.0/16"),
			},
		},
		{
			name: "two meshes with no duplicate names include all clusters in input order",
			meshes: []kilov1alpha1.ClusterMesh{
				mesh("mesh1",
					entry("alpha", "10.0.0.0/16"),
					entry("beta", "10.1.0.0/16"),
				),
				mesh("mesh2",
					entry("gamma", "10.2.0.0/16"),
					entry("delta", "10.3.0.0/16"),
				),
			},
			want: []kilov1alpha1.ClusterEntry{
				entry("alpha", "10.0.0.0/16"),
				entry("beta", "10.1.0.0/16"),
				entry("gamma", "10.2.0.0/16"),
				entry("delta", "10.3.0.0/16"),
			},
		},
		{
			name: "two meshes sharing a cluster name - first occurrence wins",
			// Both mesh1 and mesh2 declare "shared" but with different podCIDRs.
			// The entry from mesh1 (10.0.0.0/16) must survive; mesh2's version (10.9.0.0/16) must be dropped.
			meshes: []kilov1alpha1.ClusterMesh{
				mesh("mesh1",
					entry("shared", "10.0.0.0/16"),
					entry("unique1", "10.1.0.0/16"),
				),
				mesh("mesh2",
					entry("shared", "10.9.0.0/16"), // duplicate - must be dropped
					entry("unique2", "10.2.0.0/16"),
				),
			},
			want: []kilov1alpha1.ClusterEntry{
				entry("shared", "10.0.0.0/16"), // first occurrence (from mesh1) wins
				entry("unique1", "10.1.0.0/16"),
				entry("unique2", "10.2.0.0/16"),
			},
		},
		{
			name: "single mesh with internal duplicates keeps only first",
			meshes: []kilov1alpha1.ClusterMesh{
				mesh("mesh1",
					entry("dup", "10.0.0.0/16"),
					entry("dup", "10.9.0.0/16"), // duplicate within same mesh - must be dropped
					entry("unique", "10.1.0.0/16"),
				),
			},
			want: []kilov1alpha1.ClusterEntry{
				entry("dup", "10.0.0.0/16"),
				entry("unique", "10.1.0.0/16"),
			},
		},
		{
			name:   "empty input returns empty output",
			meshes: []kilov1alpha1.ClusterMesh{},
			want:   nil,
		},
		{
			name: "mesh with empty clusters slice produces empty output",
			meshes: []kilov1alpha1.ClusterMesh{
				mesh("mesh1"),
			},
			want: nil,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := mergeClusterSpecs(testCase.meshes)

			assert.Equal(t, testCase.want, got.Clusters)
		})
	}
}
