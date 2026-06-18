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

package peer

import (
	"context"
	"reflect"
	"sort"

	"github.com/cockroachdb/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// ReconcilePeers ensures the remote cluster has exactly the desired set of Peers
// for the given mesh and source cluster. It creates, updates, and deletes as needed.
//
// WireGuard identifies peers exclusively by public key — having two Peer CRDs with
// the same publicKey causes the second write to silently overwrite the first's
// AllowedIPs with an empty set, breaking routing. This function prevents that by
// checking the global peer list before creating new Peer CRDs: if a Peer with the
// same publicKey already exists under a different name (created by a prior reconcile
// for another mesh), the desired AllowedIPs are merged into that existing Peer
// instead of creating a duplicate.
func ReconcilePeers(
	ctx context.Context,
	remoteClient client.Client,
	meshName string,
	sourceCluster string,
	desired []*kilov1alpha1.Peer,
) error {
	// Build a global map of all peers by publicKey so we can detect cross-mesh
	// duplicates before creating new ones.
	allByPubKey, err := buildAllPeersByPublicKey(ctx, remoteClient)
	if err != nil {
		return errors.Wrap(err, "listing all peers for publicKey deduplication")
	}

	existing := &kilov1alpha1.PeerList{}
	selector := OrphanSelector(meshName, sourceCluster)

	err = remoteClient.List(ctx, existing, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return errors.Wrap(err, "listing existing peers")
	}

	desiredByName := buildDesiredMap(desired)
	existingByName := buildExistingMap(existing)

	errs := make([]error, 0, len(desiredByName)+len(existingByName))

	errs = append(errs, reconcileDesired(ctx, remoteClient, desiredByName, existingByName, allByPubKey)...)
	errs = append(errs, deleteOrphans(ctx, remoteClient, desiredByName, existingByName)...)

	return errors.Join(errs...)
}

// DeleteStaleSourceClusters removes every Peer in targetClient labeled with
// LabelMesh=meshName whose LabelSourceCluster does not appear in
// validSourceClusters. It closes the gap left by ReconcilePeers: that function
// only sweeps orphans within a single (mesh, source-cluster) pair, so once a
// cluster entry is removed from a ClusterMesh's spec.Clusters, no per-pair
// sweep ever runs for it and its Peer objects would otherwise persist forever,
// confusing Kilo on the surviving clusters with unreachable endpoints and
// conflicting AllowedIPs.
//
// Pass the names of all cluster entries currently present in the mesh's spec —
// peers labeled with any other source-cluster will be deleted. Pass an empty
// slice to delete every peer for the mesh in this target.
//
// IsNotFound errors during delete are ignored: a concurrent reconcile may have
// already removed the same orphan, and the desired end state is the same.
func DeleteStaleSourceClusters(
	ctx context.Context,
	targetClient client.Client,
	meshName string,
	validSourceClusters []string,
) error {
	existing := &kilov1alpha1.PeerList{}

	err := targetClient.List(ctx, existing, client.MatchingLabels{LabelMesh: meshName})
	if err != nil {
		return errors.Wrapf(err, "listing peers for mesh %q", meshName)
	}

	valid := make(map[string]struct{}, len(validSourceClusters))
	for _, name := range validSourceClusters {
		valid[name] = struct{}{}
	}

	errs := make([]error, 0, len(existing.Items))

	for i := range existing.Items {
		item := &existing.Items[i]

		src := item.Labels[LabelSourceCluster]
		if _, ok := valid[src]; ok {
			continue
		}

		err := targetClient.Delete(ctx, item)
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, errors.Wrapf(err, "deleting stale peer %q (source=%q)", item.Name, src))
		}
	}

	return errors.Join(errs...)
}

func reconcileDesired(
	ctx context.Context,
	remoteClient client.Client,
	desiredByName map[string]*kilov1alpha1.Peer,
	existingByName map[string]*kilov1alpha1.Peer,
	allByPubKey map[string]*kilov1alpha1.Peer,
) []error {
	var errs []error

	for name, desiredPeer := range desiredByName {
		existingPeer, exists := existingByName[name]
		if !exists {
			// Before creating, check whether a Peer with the same publicKey already
			// exists under a different name (created by another mesh's reconcile).
			// If so, merge our AllowedIPs into that canonical Peer instead of
			// creating a duplicate that would corrupt WireGuard's peer table.
			if canonical, hasDup := allByPubKey[desiredPeer.Spec.PublicKey]; hasDup && canonical.Name != name {
				merged := mergeAllowedIPs(canonical.Spec.AllowedIPs, desiredPeer.Spec.AllowedIPs)
				if !allowedIPsEqual(merged, canonical.Spec.AllowedIPs) {
					updated := canonical.DeepCopy()
					updated.Spec.AllowedIPs = merged
					if err := remoteClient.Update(ctx, updated); err != nil {
						errs = append(errs, errors.Wrapf(err, "merging AllowedIPs into canonical peer %s (publicKey collision with %s)", canonical.Name, name))
					}
				}
				continue
			}

			err := remoteClient.Create(ctx, desiredPeer.DeepCopy())
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "creating peer %s", name))
			}

			continue
		}

		if !peerSpecEqual(existingPeer.Spec, desiredPeer.Spec) {
			existingPeer.Spec = desiredPeer.Spec

			err := remoteClient.Update(ctx, existingPeer)
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "updating peer %s", name))
			}
		}
	}

	return errs
}

func deleteOrphans(
	ctx context.Context,
	remoteClient client.Client,
	desiredByName map[string]*kilov1alpha1.Peer,
	existingByName map[string]*kilov1alpha1.Peer,
) []error {
	var errs []error

	for name, existingPeer := range existingByName {
		if _, wanted := desiredByName[name]; !wanted {
			err := remoteClient.Delete(ctx, existingPeer)
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "deleting orphan peer %s", name))
			}
		}
	}

	return errs
}

func buildDesiredMap(desired []*kilov1alpha1.Peer) map[string]*kilov1alpha1.Peer {
	result := make(map[string]*kilov1alpha1.Peer, len(desired))

	for _, peer := range desired {
		result[peer.Name] = peer
	}

	return result
}

func buildExistingMap(existing *kilov1alpha1.PeerList) map[string]*kilov1alpha1.Peer {
	result := make(map[string]*kilov1alpha1.Peer, len(existing.Items))

	for idx := range existing.Items {
		result[existing.Items[idx].Name] = &existing.Items[idx]
	}

	return result
}

// peerSpecEqual returns true when two PeerSpec values are semantically equal.
// AllowedIPs are compared after sorting to allow order-independent equality.
func peerSpecEqual(specA, specB kilov1alpha1.PeerSpec) bool {
	if specA.PublicKey != specB.PublicKey {
		return false
	}

	if specA.PersistentKeepalive != specB.PersistentKeepalive {
		return false
	}

	if specA.PresharedKey != specB.PresharedKey {
		return false
	}

	if !reflect.DeepEqual(specA.Endpoint, specB.Endpoint) {
		return false
	}

	return allowedIPsEqual(specA.AllowedIPs, specB.AllowedIPs)
}

func allowedIPsEqual(ipsA, ipsB []string) bool {
	if len(ipsA) != len(ipsB) {
		return false
	}

	sortedA := make([]string, len(ipsA))
	sortedB := make([]string, len(ipsB))

	copy(sortedA, ipsA)
	copy(sortedB, ipsB)

	sort.Strings(sortedA)
	sort.Strings(sortedB)

	return reflect.DeepEqual(sortedA, sortedB)
}

// buildAllPeersByPublicKey returns a map of publicKey → Peer covering every Peer
// object in the cluster, regardless of labels. Used to detect cross-mesh publicKey
// collisions before creating new Peer CRDs.
func buildAllPeersByPublicKey(ctx context.Context, remoteClient client.Client) (map[string]*kilov1alpha1.Peer, error) {
	all := &kilov1alpha1.PeerList{}
	if err := remoteClient.List(ctx, all); err != nil {
		return nil, err
	}

	result := make(map[string]*kilov1alpha1.Peer, len(all.Items))
	for i := range all.Items {
		p := &all.Items[i]
		// First one wins (alphabetically lowest name becomes canonical).
		if existing, seen := result[p.Spec.PublicKey]; !seen || p.Name < existing.Name {
			result[p.Spec.PublicKey] = p
		}
	}

	return result, nil
}

// mergeAllowedIPs returns the union of two AllowedIPs slices, sorted, with
// duplicates removed.
func mergeAllowedIPs(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, ip := range a {
		seen[ip] = struct{}{}
	}
	for _, ip := range b {
		seen[ip] = struct{}{}
	}

	merged := make([]string, 0, len(seen))
	for ip := range seen {
		merged = append(merged, ip)
	}
	sort.Strings(merged)

	return merged
}
