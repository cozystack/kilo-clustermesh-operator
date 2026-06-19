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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/validation"
)

// makeCluster is a helper that constructs a ClusterEntry, folding the pod,
// wireguard, service and any additional CIDRs into the flat AllowedNetworks
// list. Empty CIDR strings are dropped so that the legacy `serviceCIDR == ""`
// call sites continue to express "no service CIDR" rather than injecting an
// unparseable empty entry.
func makeCluster(name, podCIDR, wireguardCIDR, serviceCIDR string, additionalCIDRs ...string) v1alpha1.ClusterEntry {
	networks := make([]string, 0, 3+len(additionalCIDRs))

	for _, cidr := range append([]string{podCIDR, wireguardCIDR, serviceCIDR}, additionalCIDRs...) {
		if cidr != "" {
			networks = append(networks, cidr)
		}
	}

	return v1alpha1.ClusterEntry{
		Name:            name,
		AllowedNetworks: networks,
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
					Name:            "cluster-a",
					AllowedNetworks: []string{"not-a-cidr", "10.4.0.0/24"},
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
					AllowedNetworks: []string{"10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/12", "172.16.0.0/12"},
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
		{
			// hub-and-spoke topology: one shared cluster ("ceph") participates in
			// two independent meshes pairing it with different tenants. Its CIDRs
			// appear identically in both ClusterMesh resources by design and must
			// not be flagged as cross-mesh overlap. Intra-mesh checks within each
			// mesh still ensure tenant CIDRs do not collide with the hub.
			name: "shared hub cluster in two meshes is not an overlap",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a-ceph",
					makeCluster("tenant-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/16"),
					makeCluster("ceph", "10.247.0.0/16", "100.66.0.0/16", "10.99.0.0/16"),
				),
				makeMesh("mesh-b-ceph",
					makeCluster("tenant-b", "10.1.0.0/16", "10.4.1.0/24", "10.112.0.0/16"),
					makeCluster("ceph", "10.247.0.0/16", "100.66.0.0/16", "10.99.0.0/16"),
				),
			},
			wantErr: false,
		},
		{
			// Two different tenants accidentally pick the same pod CIDR; the
			// shared-cluster-name exception MUST NOT mask this conflict because
			// the colliding entries belong to different clusters.
			name: "different clusters with same pod CIDR across meshes still flagged",
			meshes: []v1alpha1.ClusterMesh{
				makeMesh("mesh-a",
					makeCluster("tenant-a", "10.0.0.0/16", "10.4.0.0/24", "10.96.0.0/16"),
					makeCluster("ceph", "10.247.0.0/16", "100.66.0.0/16", "10.99.0.0/16"),
				),
				makeMesh("mesh-b",
					makeCluster("tenant-b", "10.0.0.0/16", "10.4.1.0/24", "10.112.0.0/16"),
					makeCluster("ceph", "10.247.0.0/16", "100.66.0.0/16", "10.99.0.0/16"),
				),
			},
			wantErr:     true,
			errContains: []string{"mesh-a", "mesh-b", "tenant-a", "tenant-b"},
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

// repoRootForValidation returns the repository root by walking up from the
// directory of the current test file until a go.mod is found.
// It mirrors the pattern used in internal/containerfile/containerfile_test.go.
func repoRootForValidation(t *testing.T) string {
	t.Helper()

	_, callerFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller returned no file info")

	dir := filepath.Dir(callerFile)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")

		dir = parent
	}
}

// extractFirstClusterMeshYAML finds the first YAML fenced block in src that
// contains a ClusterMesh document (identified by "kind: ClusterMesh") and
// returns its content.
func extractFirstClusterMeshYAML(src string) (string, bool) {
	const fence = "```yaml"

	rest := src

	for {
		start := strings.Index(rest, fence)
		if start < 0 {
			return "", false
		}

		rest = rest[start+len(fence):]

		end := strings.Index(rest, "```")
		if end < 0 {
			return "", false
		}

		block := rest[:end]
		rest = rest[end+3:]

		if strings.Contains(block, "kind: ClusterMesh") {
			return block, true
		}
	}
}

// TestREADMEQuickStartManifestIsValid is a regression guard that ensures the
// ClusterMesh manifest in the Quick Start section of README.md uses
// non-overlapping CIDRs and would pass ValidateClusterNetworks.
func TestREADMEQuickStartManifestIsValid(t *testing.T) {
	t.Parallel()

	root := repoRootForValidation(t)
	readmePath := filepath.Join(root, "README.md")

	src, err := os.ReadFile(readmePath)
	require.NoError(t, err, "reading README.md")

	yamlBlock, found := extractFirstClusterMeshYAML(string(src))
	require.True(t, found, "no ClusterMesh YAML block found in README.md")

	var mesh v1alpha1.ClusterMesh
	require.NoError(t, yaml.Unmarshal([]byte(yamlBlock), &mesh), "unmarshalling ClusterMesh YAML from README.md")

	require.NotEmpty(t, mesh.Spec.Clusters, "README ClusterMesh spec has no clusters")

	err = validation.ValidateClusterNetworks(mesh.Spec.Clusters)
	assert.NoError(t, err, "README Quick Start ClusterMesh has overlapping CIDRs")
}
