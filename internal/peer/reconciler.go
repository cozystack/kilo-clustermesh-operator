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
	"sigs.k8s.io/controller-runtime/pkg/client"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// ReconcilePeers ensures the remote cluster has exactly the desired set of Peers
// for the given mesh and source cluster. It creates, updates, and deletes as needed.
func ReconcilePeers(
	ctx context.Context,
	remoteClient client.Client,
	meshName string,
	sourceCluster string,
	desired []*kilov1alpha1.Peer,
) error {
	existing := &kilov1alpha1.PeerList{}
	selector := OrphanSelector(meshName, sourceCluster)

	err := remoteClient.List(ctx, existing, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return errors.Wrap(err, "listing existing peers")
	}

	desiredByName := buildDesiredMap(desired)
	existingByName := buildExistingMap(existing)

	errs := make([]error, 0, len(desiredByName)+len(existingByName))

	errs = append(errs, reconcileDesired(ctx, remoteClient, desiredByName, existingByName)...)
	errs = append(errs, deleteOrphans(ctx, remoteClient, desiredByName, existingByName)...)

	return errors.Join(errs...)
}

func reconcileDesired(
	ctx context.Context,
	remoteClient client.Client,
	desiredByName map[string]*kilov1alpha1.Peer,
	existingByName map[string]*kilov1alpha1.Peer,
) []error {
	var errs []error

	for name, desiredPeer := range desiredByName {
		existingPeer, exists := existingByName[name]
		if !exists {
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
