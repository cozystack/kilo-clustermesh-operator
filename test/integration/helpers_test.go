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

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

const (
	eventuallyTimeout  = 10 * time.Second
	eventuallyInterval = 100 * time.Millisecond
)

// makeNode builds a corev1.Node with the Kilo annotations required for peering.
// endpoint may be empty (no kilo.squat.ai/force-endpoint annotation is set).
func makeNode(name, podCIDR, wgIP, pubKey, endpoint string) *corev1.Node {
	annotations := map[string]string{
		kilonode.AnnotationWireguardIP: wgIP,
		kilonode.AnnotationPublicKey:   pubKey,
	}
	if endpoint != "" {
		annotations[kilonode.AnnotationForceEndpoint] = endpoint
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: corev1.NodeSpec{
			PodCIDR:  podCIDR,
			PodCIDRs: []string{podCIDR},
		},
	}
}

// reconcileOnce calls Reconcile for the given ClusterMesh and returns the error.
func reconcileOnce(t *testing.T, mesh *v1alpha1.ClusterMesh) error {
	t.Helper()

	req := ctrl.Request{}
	req.Name = mesh.Name
	req.Namespace = mesh.Namespace

	_, err := globalEnv.reconciler.Reconcile(context.Background(), req)

	return err
}

// mustReconcile calls reconcileOnce and fails the test on error.
func mustReconcile(t *testing.T, mesh *v1alpha1.ClusterMesh) {
	t.Helper()
	require.NoError(t, reconcileOnce(t, mesh))
}

// waitForCondition polls until the named condition on the ClusterMesh reaches
// the expected status, or timeout expires.
func waitForCondition(
	t *testing.T,
	cl client.Client,
	mesh *v1alpha1.ClusterMesh,
	condType string,
	status metav1.ConditionStatus,
	timeout time.Duration,
) {
	t.Helper()

	require.Eventually(t, func() bool {
		got := &v1alpha1.ClusterMesh{}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(mesh), got); err != nil {
			return false
		}

		cond := apimeta.FindStatusCondition(got.Status.Conditions, condType)
		return cond != nil && cond.Status == status
	}, timeout, eventuallyInterval, "condition %s=%s on ClusterMesh %s/%s", condType, status, mesh.Namespace, mesh.Name)
}

// waitForPeerCount polls until exactly count Peers exist for the given mesh and
// source cluster in the cluster accessed by cl.
func waitForPeerCount(
	t *testing.T,
	cl client.Client,
	meshName, sourceCluster string,
	count int,
	timeout time.Duration,
) {
	t.Helper()

	require.Eventually(t, func() bool {
		return countPeers(t, cl, meshName, sourceCluster) == count
	}, timeout, eventuallyInterval, "expected %d peers for mesh=%s source=%s", count, meshName, sourceCluster)
}

// countPeers returns the number of Peers labelled with meshName and sourceCluster.
func countPeers(t *testing.T, cl client.Client, meshName, sourceCluster string) int {
	t.Helper()

	list := &kilov1alpha1.PeerList{}
	err := cl.List(context.Background(), list,
		client.MatchingLabels{
			peer.LabelMesh:          meshName,
			peer.LabelSourceCluster: sourceCluster,
		},
	)
	require.NoError(t, err)

	return len(list.Items)
}

// createMesh creates a ClusterMesh on the local envtest cluster and returns it.
func createMesh(t *testing.T, mesh *v1alpha1.ClusterMesh) *v1alpha1.ClusterMesh {
	t.Helper()
	require.NoError(t, globalEnv.localClient.Create(context.Background(), mesh))

	return mesh
}

// deleteMesh deletes a ClusterMesh and reconciles until the finalizer is removed.
func deleteMesh(t *testing.T, mesh *v1alpha1.ClusterMesh) {
	t.Helper()

	ctx := context.Background()
	require.NoError(t, globalEnv.localClient.Delete(ctx, mesh))

	// Drive deletion: reconcile until the object is gone.
	require.Eventually(t, func() bool {
		// Each reconcile call handles the deletion finalizer.
		_, _ = globalEnv.reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(mesh),
		})

		got := &v1alpha1.ClusterMesh{}
		err := globalEnv.localClient.Get(ctx, client.ObjectKeyFromObject(mesh), got)

		return client.IgnoreNotFound(err) == nil && err != nil
	}, eventuallyTimeout, eventuallyInterval, "ClusterMesh %s/%s was not deleted", mesh.Namespace, mesh.Name)
}

// simpleMeshSpec returns a two-cluster ClusterMesh spec using the global
// "local" and "remote" registry entries with non-overlapping CIDRs.
func simpleMeshSpec(name, namespace string) *v1alpha1.ClusterMesh {
	return &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:          "local",
					Local:         true,
					PodCIDRs:      []string{"10.1.0.0/16"},
					WireguardCIDR: "10.100.0.0/24",
				},
				{
					Name:          "remote",
					PodCIDRs:      []string{"10.2.0.0/16"},
					WireguardCIDR: "10.100.1.0/24",
				},
			},
		},
	}
}
