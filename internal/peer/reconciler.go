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
//
// retainedNodeNames names nodes that are present but transiently skipped this
// reconcile (currently only WireGuard-IP collisions): their existing Peer is
// kept rather than orphan-deleted, unless keeping it would conflict with a
// desired (winner) Peer. See retainTransientPeers.
func ReconcilePeers(
	ctx context.Context,
	remoteClient client.Client,
	meshName string,
	sourceCluster string,
	desired []*kilov1alpha1.Peer,
	retainedNodeNames ...string,
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

	retainTransientPeers(meshName, sourceCluster, retainedNodeNames, desiredByName, existingByName)

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
			errs = append(errs, createOrMerge(ctx, remoteClient, name, desiredPeer, allByPubKey)...)

			continue
		}

		// Preserve a previously resolved endpoint when the desired spec has
		// none and the peer is still the same machine (same public key). This
		// only applies to an already-published Peer: a node joining the mesh
		// has no Peer yet, so there is nothing to preserve. The desired
		// endpoint turns nil for an established node when neither its
		// annotations nor its ExternalIP resolve any more and the target's
		// discovered-endpoints enrichment finds nothing for its key;
		// overwriting the known endpoint with nil then tears the WireGuard
		// endpoint down and churns the peer. Keep the last known-good endpoint
		// instead.
		//
		// This is a hint field only: for a NAT'd peer Kilo uses the discovered
		// endpoint regardless. It is kept with no expiry, so guard on the public
		// key, otherwise a node rebuilt under the same name with a fresh key but
		// no endpoint yet would inherit the old machine's address. A genuine
		// roaming peer starts with no endpoint and so has nothing to preserve.
		//
		// Work on a copy of the spec: desiredPeer is shared across all target
		// clusters (built once per source), so mutating it here would leak one
		// target's endpoint into the next target's Peer.
		newSpec := desiredPeer.Spec
		if newSpec.Endpoint == nil &&
			existingPeer.Spec.Endpoint != nil &&
			existingPeer.Spec.PublicKey == newSpec.PublicKey {
			newSpec.Endpoint = existingPeer.Spec.Endpoint
		}

		if peerSpecEqual(existingPeer.Spec, newSpec) {
			continue
		}

		existingPeer.Spec = newSpec

		err := remoteClient.Update(ctx, existingPeer)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "updating peer %s", name))
		}
	}

	return errs
}

// createOrMerge either creates a new Peer or, when a Peer with the same
// publicKey already exists under a different name, merges the desired
// AllowedIPs into that canonical Peer. This prevents WireGuard from seeing
// two peers with identical public keys, which would silently clear the
// AllowedIPs of the first entry.
func createOrMerge(
	ctx context.Context,
	remoteClient client.Client,
	name string,
	desiredPeer *kilov1alpha1.Peer,
	allByPubKey map[string]*kilov1alpha1.Peer,
) []error {
	canonical, hasDup := allByPubKey[desiredPeer.Spec.PublicKey]
	if hasDup && canonical.Name != name {
		return mergeIntoCanonical(ctx, remoteClient, canonical, desiredPeer.Spec.AllowedIPs, name)
	}

	err := remoteClient.Create(ctx, desiredPeer.DeepCopy())
	if err != nil {
		return []error{errors.Wrapf(err, "creating peer %s", name)}
	}

	return nil
}

// mergeIntoCanonical merges newIPs into an existing canonical Peer's AllowedIPs.
func mergeIntoCanonical(
	ctx context.Context,
	remoteClient client.Client,
	canonical *kilov1alpha1.Peer,
	newIPs []string,
	collidingName string,
) []error {
	merged := mergeAllowedIPs(canonical.Spec.AllowedIPs, newIPs)
	if allowedIPsEqual(merged, canonical.Spec.AllowedIPs) {
		return nil
	}

	updated := canonical.DeepCopy()
	updated.Spec.AllowedIPs = merged

	err := remoteClient.Update(ctx, updated)
	if err != nil {
		return []error{errors.Wrapf(err, "merging AllowedIPs into canonical peer %s (publicKey collision with %s)", canonical.Name, collidingName)}
	}

	return nil
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

// retainTransientPeers keeps the existing Peer of a node that is present but
// transiently skipped this reconcile (a WireGuard-IP collision while
// annotations propagate). Such a Peer would otherwise be orphan-deleted, only
// to be recreated once the collision clears, churning the tunnel. It is folded
// into desiredByName as its own current spec, so reconcileDesired sees it as
// unchanged (peerSpecEqual) and deleteOrphans leaves it alone.
//
// Retention is guarded: if the node's existing Peer advertises an AllowedIP a
// desired (winner) Peer also claims, keeping it would leave two Peers with
// different public keys claiming one AllowedIP, which WireGuard routes
// non-deterministically. In that case the Peer is NOT retained and is left to
// deleteOrphans — the correct outcome, since one IP cannot route to two peers.
//
// The conflict check is exact-string equality on AllowedIPs. BuildPeer
// canonicalizes every AllowedIP (the WireGuard IP is always a /32 via
// HostRoute's ip.String()), so a shared host route is always the same string
// on winner and loser and is reliably caught. The check does NOT detect CIDR
// containment: a winner /32 that falls inside a retained peer's wider CIDR is a
// different string and would be retained. That overlap is only reachable when
// AllowedNetworks is configured so a pod/service CIDR overlaps the WireGuard
// range — a misconfiguration the normal disjoint split avoids.
func retainTransientPeers(
	meshName, sourceCluster string,
	retainedNodeNames []string,
	desiredByName map[string]*kilov1alpha1.Peer,
	existingByName map[string]*kilov1alpha1.Peer,
) {
	if len(retainedNodeNames) == 0 {
		return
	}

	claimed := claimedAllowedIPs(desiredByName)

	for _, nodeName := range retainedNodeNames {
		name := Name(meshName, sourceCluster, nodeName)

		if _, alreadyDesired := desiredByName[name]; alreadyDesired {
			continue
		}

		existingPeer, ok := existingByName[name]
		if !ok {
			continue
		}

		if conflictsWithClaimed(existingPeer.Spec.AllowedIPs, claimed) {
			continue
		}

		desiredByName[name] = existingPeer
	}
}

// claimedAllowedIPs collects every AllowedIP string advertised by a desired Peer.
func claimedAllowedIPs(desiredByName map[string]*kilov1alpha1.Peer) map[string]struct{} {
	claimed := make(map[string]struct{})

	for _, p := range desiredByName {
		for _, ip := range p.Spec.AllowedIPs {
			claimed[ip] = struct{}{}
		}
	}

	return claimed
}

// conflictsWithClaimed reports whether any of allowedIPs is already claimed by a
// desired Peer.
func conflictsWithClaimed(allowedIPs []string, claimed map[string]struct{}) bool {
	for _, ip := range allowedIPs {
		if _, ok := claimed[ip]; ok {
			return true
		}
	}

	return false
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

	err := remoteClient.List(ctx, all)
	if err != nil {
		return nil, errors.Wrap(err, "listing all peers")
	}

	result := make(map[string]*kilov1alpha1.Peer, len(all.Items))

	for i := range all.Items {
		p := &all.Items[i]

		// Alphabetically lowest name becomes the canonical Peer for this publicKey.
		existing, seen := result[p.Spec.PublicKey]
		if !seen || p.Name < existing.Name {
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
