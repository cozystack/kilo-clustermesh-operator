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

package restart

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))

	return s
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestReconcile_SameFingerprint_NoCancelCalled(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)

	mesh := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh1", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "local", Local: true, PodCIDRs: []string{"10.0.0.0/16"}, WireguardCIDR: "10.4.0.0/16"},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mesh).Build()

	cancelled := false

	watcher := &ChangeWatcher{
		Client: fc,
		Cancel: func() { cancelled = true },
		Log:    testLogger(),
	}

	fp, err := watcher.ComputeFingerprint(context.Background())
	require.NoError(t, err)
	watcher.StartFingerprint = fp

	_, err = watcher.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.False(t, cancelled)
}

func TestReconcile_NewMeshAdded_CancelCalled(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)

	mesh1 := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh1", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "local", Local: true, PodCIDRs: []string{"10.0.0.0/16"}, WireguardCIDR: "10.4.0.0/16"},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mesh1).Build()

	cancelled := false

	watcher := &ChangeWatcher{
		Client: fc,
		Cancel: func() { cancelled = true },
		Log:    testLogger(),
	}

	fp, err := watcher.ComputeFingerprint(context.Background())
	require.NoError(t, err)
	watcher.StartFingerprint = fp

	mesh2 := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh2", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "remote", PodCIDRs: []string{"10.20.0.0/16"}, WireguardCIDR: "10.5.0.0/16"},
			},
		},
	}
	require.NoError(t, fc.Create(context.Background(), mesh2))

	_, err = watcher.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.True(t, cancelled)
}

func TestReconcile_SecretRVChanged_CancelCalled(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote-kubeconfig", Namespace: "default",
			ResourceVersion: "100",
		},
		Data: map[string][]byte{"kubeconfig": []byte("data")},
	}

	mesh := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh1", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{
					Name:                "remote",
					KubeconfigSecretRef: &v1alpha1.SecretKeyRef{Name: "remote-kubeconfig", Key: "kubeconfig"},
					PodCIDRs:            []string{"10.20.0.0/16"},
					WireguardCIDR:       "10.5.0.0/16",
				},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mesh, secret).Build()

	cancelled := false

	watcher := &ChangeWatcher{
		Client: fc,
		Cancel: func() { cancelled = true },
		Log:    testLogger(),
	}

	fp, err := watcher.ComputeFingerprint(context.Background())
	require.NoError(t, err)
	watcher.StartFingerprint = fp

	var updated corev1.Secret
	require.NoError(t, fc.Get(context.Background(), client.ObjectKeyFromObject(secret), &updated))

	updated.Data["kubeconfig"] = []byte("changed-data")
	require.NoError(t, fc.Update(context.Background(), &updated))

	_, err = watcher.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.True(t, cancelled)
}

func TestFingerprint_StableUnderReordering(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)

	meshA := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh-a", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "alpha", Local: true, PodCIDRs: []string{"10.0.0.0/16"}, WireguardCIDR: "10.4.0.0/16"},
				{Name: "beta", PodCIDRs: []string{"10.1.0.0/16"}, WireguardCIDR: "10.5.0.0/16"},
			},
		},
	}

	meshB := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh-a", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "beta", PodCIDRs: []string{"10.1.0.0/16"}, WireguardCIDR: "10.5.0.0/16"},
				{Name: "alpha", Local: true, PodCIDRs: []string{"10.0.0.0/16"}, WireguardCIDR: "10.4.0.0/16"},
			},
		},
	}

	fc1 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(meshA).Build()
	fc2 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(meshB).Build()

	w1 := &ChangeWatcher{Client: fc1, Log: testLogger()}
	w2 := &ChangeWatcher{Client: fc2, Log: testLogger()}

	ctx := context.Background()

	fp1, err := w1.ComputeFingerprint(ctx)
	require.NoError(t, err)

	fp2, err := w2.ComputeFingerprint(ctx)
	require.NoError(t, err)

	assert.Equal(t, fp1, fp2)
}

func TestFingerprint_NoMeshes(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	watcher := &ChangeWatcher{Client: fc, Log: testLogger()}

	fp, err := watcher.ComputeFingerprint(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, fp)
}

func TestReconcile_NilCancel_NoPanic(t *testing.T) {
	t.Parallel()

	// This test verifies that Reconcile does not panic when Cancel is nil.
	// A bootstrap ChangeWatcher (used only for fingerprint computation) has no
	// Cancel set; if any future code path calls Reconcile on it and the
	// fingerprint differs from the start fingerprint, the nil dereference must
	// be guarded.
	scheme := testScheme(t)

	mesh := &v1alpha1.ClusterMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh1", Namespace: "default"},
		Spec: v1alpha1.ClusterMeshSpec{
			Clusters: []v1alpha1.ClusterEntry{
				{Name: "local", Local: true, PodCIDRs: []string{"10.0.0.0/16"}, WireguardCIDR: "10.4.0.0/16"},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mesh).Build()

	// Set StartFingerprint to a value that will not match the freshly computed
	// fingerprint, forcing the fingerprint-changed branch to execute.
	watcher := &ChangeWatcher{
		Client:           fc,
		Cancel:           nil, // intentionally nil
		Log:              testLogger(),
		StartFingerprint: "this-will-not-match",
	}

	result, err := watcher.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}
