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

package multicluster

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestBuild_SkipsEntryWithMissingSecret reproduces the operator-crashloop
// scenario observed during tenant teardown: a ClusterMesh CR still references
// a remote cluster whose admin-kubeconfig Secret has already been removed by
// the upstream Helm release. Before the fix, Build returned an error from the
// first such entry and the operator failed to start, blocking the finalizer
// that would otherwise release the ClusterMesh and let the rest of the
// teardown proceed. Build must now log a warning, skip that entry, and keep
// every other entry it can construct.
func TestBuild_SkipsEntryWithMissingSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Only the "ceph" secret is present. The "mesh2" entry references a
	// secret that does not exist — simulating Helm having deleted it ahead
	// of the ClusterMesh CR's finalizer.
	cephSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ceph-kubeconfig", Namespace: "tenant-root"},
		Data:       map[string][]byte{"kubeconfig": []byte(testKubeconfig)},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cephSecret).Build()

	entries := []EntrySource{
		{
			Entry: v1alpha1.ClusterEntry{
				Name: "ceph",
				KubeconfigSecretRef: &v1alpha1.SecretKeyRef{
					Name: "ceph-kubeconfig",
					Key:  "kubeconfig",
				},
			},
			MeshNamespace: "tenant-root",
		},
		{
			Entry: v1alpha1.ClusterEntry{
				Name: "mesh2",
				KubeconfigSecretRef: &v1alpha1.SecretKeyRef{
					Name: "kubernetes-switchcloud-mesh2-admin-kubeconfig",
					Key:  "super-admin.conf",
				},
			},
			MeshNamespace: "tenant-root",
		},
	}

	reg, err := Build(context.Background(), entries, &rest.Config{}, fc, scheme, discardLogger())
	require.NoError(t, err, "Build must not fail when some entries have missing secrets")
	require.NotNil(t, reg)

	clusters := reg.Clusters()
	assert.Contains(t, clusters, "ceph", "reachable entry must remain in the registry")
	assert.NotContains(t, clusters, "mesh2", "entry with missing secret must be skipped")

	_, ok := reg.Client("mesh2")
	assert.False(t, ok, "Client lookup for the skipped cluster must return ok=false so reconciler best-effort loops continue past it")
}

// TestBuild_LocalEntryDoesNotNeedSecret guards against regressing the
// fast-path: a Local: true entry must always succeed since it reuses
// localCfg, even when no Secret of the referenced name exists.
func TestBuild_LocalEntryDoesNotNeedSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	entries := []EntrySource{
		{
			Entry:         v1alpha1.ClusterEntry{Name: "local", Local: true},
			MeshNamespace: "tenant-root",
		},
	}

	reg, err := Build(context.Background(), entries, &rest.Config{}, fc, scheme, discardLogger())
	require.NoError(t, err)
	require.NotNil(t, reg)

	assert.Equal(t, "local", reg.LocalName())
	assert.Contains(t, reg.Clusters(), "local")
}

// TestBuild_NilLoggerIsAccepted ensures callers that have not yet wired a
// logger can still use Build without panicking on a nil dereference.
func TestBuild_NilLoggerIsAccepted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	entries := []EntrySource{
		{
			Entry: v1alpha1.ClusterEntry{
				Name: "missing-secret-cluster",
				KubeconfigSecretRef: &v1alpha1.SecretKeyRef{
					Name: "does-not-exist",
					Key:  "kubeconfig",
				},
			},
			MeshNamespace: "tenant-root",
		},
	}

	reg, err := Build(context.Background(), entries, &rest.Config{}, fc, scheme, nil)
	require.NoError(t, err)
	require.NotNil(t, reg)
	assert.NotContains(t, reg.Clusters(), "missing-secret-cluster")
}
