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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
)

// keepaliveTestNode builds a node that passes BuildPeer's requirements
// (public key, wireguard-ip, pod CIDR, force-endpoint).
func keepaliveTestNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				kilonode.AnnotationPublicKey:     "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				kilonode.AnnotationWireguardIP:   "10.4.0.1/32",
				kilonode.AnnotationForceEndpoint: "203.0.113.1:51820",
			},
		},
		Spec: corev1.NodeSpec{
			PodCIDRs: []string{"10.244.1.0/24"},
		},
	}
}

func TestMeshPersistentKeepalive(t *testing.T) {
	t.Parallel()

	mesh := &v1alpha1.ClusterMesh{
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "ceph", PersistentKeepalive: 0},
				{Name: "tenant", PersistentKeepalive: 25},
			},
		},
	}

	assert.Equal(t, 25, meshPersistentKeepalive(mesh),
		"mesh-wide keepalive must be the max across all entries")
}

func TestMeshPersistentKeepalive_AllZero(t *testing.T) {
	t.Parallel()

	mesh := &v1alpha1.ClusterMesh{
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "a", PersistentKeepalive: 0},
				{Name: "b", PersistentKeepalive: 0},
			},
		},
	}

	assert.Equal(t, 0, meshPersistentKeepalive(mesh),
		"no NAT cluster declared keepalive → mesh-wide keepalive is 0")
}

// TestBuildDesiredPeers_CephSidePeerInheritsMeshKeepalive reproduces the
// production NAT scenario: the mesh has a Ceph cluster (PK=0, public endpoints)
// and a NAT'd tenant cluster (PK=25). The Ceph-side peer for a tenant node is
// built from the tenant's SELF entry, whose own PersistentKeepalive is 0. The
// mesh-wide floor must still be applied so the Ceph→tenant direction has a
// keepalive, otherwise the NAT mapping expires and the tunnel flaps.
func TestBuildDesiredPeers_CephSidePeerInheritsMeshKeepalive(t *testing.T) {
	t.Parallel()

	mesh := &v1alpha1.ClusterMesh{
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "ceph", PersistentKeepalive: 0},
				{Name: "tenant", PersistentKeepalive: 25},
			},
		},
	}

	// Peers for the tenant cluster are derived from the tenant's SELF entry
	// (PK=0). These are the peers that get pushed into Ceph (the ceph-side
	// peers).
	tenantEntry := &mesh.Spec.Clusters[1]
	nodes := []*corev1.Node{keepaliveTestNode("tenant-worker-1")}

	peers, err := buildDesiredPeers(mesh.Name, tenantEntry, nodes, meshPersistentKeepalive(mesh))

	require.NoError(t, err)
	require.Len(t, peers, 1)
	assert.Equal(t, 25, peers[0].Spec.PersistentKeepalive,
		"ceph-side peer derived from the tenant SELF entry (PK=0) must still get the mesh-wide PK=25")
}
