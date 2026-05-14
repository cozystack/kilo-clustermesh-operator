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

// Package validation provides mesh-level CIDR validation for ClusterMesh objects.
package validation

import (
	"net"

	"github.com/cockroachdb/errors"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/netutil"
)

// parsedCIDR holds a parsed network together with its source cluster name and
// the original CIDR string, so overlap errors can carry full context.
type parsedCIDR struct {
	cluster string
	raw     string
	net     *net.IPNet
}

// parseClusters parses all CIDRs from a slice of ClusterEntry values and
// returns a flat list of parsedCIDR entries, one per CIDR string.
// Returns an error on the first invalid CIDR string encountered.
func parseClusters(clusters []v1alpha1.ClusterEntry) ([]parsedCIDR, error) {
	result := make([]parsedCIDR, 0)

	for i := range clusters {
		for _, raw := range clusters[i].AllCIDRs() {
			parsed, err := netutil.ParseCIDR(raw)
			if err != nil {
				return nil, errors.Wrapf(err, "cluster %q", clusters[i].Name)
			}

			result = append(result, parsedCIDR{
				cluster: clusters[i].Name,
				raw:     raw,
				net:     parsed,
			})
		}
	}

	return result, nil
}

// checkOverlaps performs a pairwise overlap check over a flat list of parsed
// CIDRs. Returns an error describing the first overlap found, or nil if all
// CIDRs are disjoint. The meshA and meshB parameters are used to enrich the
// error message; pass the same value (or "") for intra-mesh checks.
func checkOverlaps(cidrs []parsedCIDR, meshA, meshB string) error {
	for i := range cidrs {
		for j := i + 1; j < len(cidrs); j++ {
			cidrA, cidrB := cidrs[i], cidrs[j]

			if !netutil.CIDROverlaps(cidrA.net, cidrB.net) {
				continue
			}

			if meshA != "" && meshA != meshB {
				return errors.Newf(
					"CIDR overlap between mesh %q (cluster %q, %s) and mesh %q (cluster %q, %s)",
					meshA, cidrA.cluster, cidrA.raw,
					meshB, cidrB.cluster, cidrB.raw,
				)
			}

			return errors.Newf(
				"CIDR overlap between cluster %q (%s) and cluster %q (%s)",
				cidrA.cluster, cidrA.raw,
				cidrB.cluster, cidrB.raw,
			)
		}
	}

	return nil
}

// ValidateClusterNetworks checks that all CIDRs within a single ClusterMesh
// are pairwise disjoint. Returns an error describing the first overlap found.
func ValidateClusterNetworks(clusters []v1alpha1.ClusterEntry) error {
	cidrs, err := parseClusters(clusters)
	if err != nil {
		return err
	}

	return checkOverlaps(cidrs, "", "")
}

// ValidateMeshNetworks checks that CIDRs across multiple ClusterMesh objects
// are pairwise disjoint. Each mesh's internal CIDRs are also checked.
// Returns an error identifying which meshes and CIDRs overlap.
func ValidateMeshNetworks(meshes []v1alpha1.ClusterMesh) error {
	// First validate each mesh internally.
	for i := range meshes {
		err := ValidateClusterNetworks(meshes[i].Spec.Clusters)
		if err != nil {
			return errors.Wrapf(err, "mesh %q", meshes[i].Name)
		}
	}

	// Build per-mesh parsed CIDR lists for cross-mesh comparison.
	perMesh := make([][]parsedCIDR, len(meshes))

	for i := range meshes {
		cidrs, err := parseClusters(meshes[i].Spec.Clusters)
		if err != nil {
			return errors.Wrapf(err, "mesh %q", meshes[i].Name)
		}

		perMesh[i] = cidrs
	}

	// Check every pair of distinct meshes.
	for i := range meshes {
		for j := i + 1; j < len(meshes); j++ {
			combined := append(perMesh[i], perMesh[j]...) //nolint:gocritic // intentional: build cross-mesh slice for overlap check

			err := checkOverlaps(combined, meshes[i].Name, meshes[j].Name)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
