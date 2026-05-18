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

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/validation"
)

// makeCluster is a helper that constructs a ClusterEntry with the given fields.
func makeCluster(name, podCIDR, wireguardCIDR, serviceCIDR string, additionalCIDRs ...string) v1alpha1.ClusterEntry {
	return v1alpha1.ClusterEntry{
		Name:            name,
		PodCIDRs:        []string{podCIDR},
		WireguardCIDR:   wireguardCIDR,
		ServiceCIDR:     serviceCIDR,
		AdditionalCIDRs: additionalCIDRs,
	}
}

// makeMesh is a helper that constructs a ClusterMesh with the given name and clusters.
func makeMesh(name string, clusters ...v1alpha1.ClusterEntry) v1alpha1.ClusterMesh {
	return v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ClusterMeshSpec{Clusters: clusters},
	}
}

func TestValidateClusterNetworks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		clusters    []v1alpha1.ClusterEntry
		wantErr     bool
		errContains []string
	}{
		{
			name: "single cluster no overlaps",
			clusters: []v1alpha1.ClusterEntry{
				makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
			},
			wantErr: false,
		},
		{
			name:     "empty cluster list",
			clusters: []v1alpha1.ClusterEntry{},
			wantErr:  false,
		},
		{
			name: "two clusters disjoint CIDRs",
			clusters: []v1alpha1.ClusterEntry{
				makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
				makeCluster("cluster-b", "10.1.0.0/16", "10.4.1.0/24", "10.112.0.0/12"),
			},
			wantErr: false,
		},
		{
			name: "two clusters overlapping serviceCIDR",
			clusters: []v1alpha1.ClusterEntry{
				makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
				makeCluster("cluster-b", "10.1.0.0/16", "10.4.1.0/24", "10.96.0.0/12"),
			},
			wantErr:     true,
			errContains: []string{"cluster-a", "cluster-b"},
		},
		{
			name: "overlap within a single cluster between serviceCIDR and additionalCIDR",
			clusters: []v1alpha1.ClusterEntry{
				makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12", "10.96.128.0/17"),
			},
			wantErr:     true,
			errContains: []string{"cluster-a"},
		},
		{
			name: "invalid CIDR string",
			clusters: []v1alpha1.ClusterEntry{
				{
					Name:          "cluster-a",
					PodCIDRs:      []string{"not-a-cidr"},
					WireguardCIDR: "10.4.0.0/24",
				},
			},
			wantErr:     true,
			errContains: []string{"cluster-a"},
		},
		{
			name: "two clusters with overlapping podCIDR",
			clusters: []v1alpha1.ClusterEntry{
				makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", ""),
				makeCluster("cluster-b", "10.0.128.0/17", "10.4.1.0/24", ""),
			},
			wantErr:     true,
			errContains: []string{"cluster-a", "cluster-b"},
		},
		{
			name: "single cluster with multiple distinct CIDRs no overlap",
			clusters: []v1alpha1.ClusterEntry{
				{
					Name:            "cluster-a",
					PodCIDRs:        []string{"10.0.0.0/16"},
					WireguardCIDR:   "10.4.0.0/24",
					ServiceCIDR:     "10.96.0.0/12",
					AdditionalCIDRs: []string{"172.16.0.0/12"},
				},
			},
			wantErr: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateClusterNetworks(testCase.clusters)

			if testCase.wantErr {
				require.Error(t, err)

				for _, fragment := range testCase.errContains {
					assert.Contains(t, err.Error(), fragment)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMeshNetworks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		meshes      []v1alpha1.ClusterMesh
		wantErr     bool
		errContains []string
	}{
		{
			name:    "empty mesh list",
			meshes:  []v1alpha1.ClusterMesh{},
			wantErr: false,
		},
		{
			name: "single mesh valid",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a",
					makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
					makeCluster("cluster-b", "10.1.0.0/16", "10.4.1.0/24", "10.112.0.0/12"),
				),
			},
			wantErr: false,
		},
		{
			name: "two meshes disjoint network plans",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a",
					makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
					makeCluster("cluster-b", "10.1.0.0/16", "10.4.1.0/24", "10.112.0.0/12"),
				),
				makeMesh("mesh-b",
					makeCluster("cluster-c", "10.2.0.0/16", "10.4.2.0/24", "172.20.0.0/16"),
					makeCluster("cluster-d", "10.3.0.0/16", "10.4.3.0/24", "172.21.0.0/16"),
				),
			},
			wantErr: false,
		},
		{
			name: "two meshes overlapping CIDR",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a",
					makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
					makeCluster("cluster-b", "10.1.0.0/16", "10.4.1.0/24", ""),
				),
				makeMesh("mesh-b",
					makeCluster("cluster-c", "10.0.0.0/16", "10.4.2.0/24", ""),
					makeCluster("cluster-d", "10.2.0.0/16", "10.4.3.0/24", ""),
				),
			},
			wantErr:     true,
			errContains: []string{"mesh-a", "mesh-b"},
		},
		{
			name: "intra-mesh overlap is caught",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a",
					makeCluster("cluster-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12"),
					makeCluster("cluster-b", "10.0.0.0/16", "10.4.1.0/24", ""),
				),
			},
			wantErr:     true,
			errContains: []string{"cluster-a", "cluster-b"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateMeshNetworks(testCase.meshes)

			if testCase.wantErr {
				require.Error(t, err)

				for _, fragment := range testCase.errContains {
					assert.Contains(t, err.Error(), fragment)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
