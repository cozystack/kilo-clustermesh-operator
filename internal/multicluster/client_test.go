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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-server:6443
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: test-token
`

func TestRestConfigFromSecret_ValidKubeconfig(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte(testKubeconfig)},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	cfg, err := RestConfigFromSecret(context.Background(), fc, "default", v1alpha1.SecretKeyRef{
		Name: "test-kubeconfig",
		Key:  "kubeconfig",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://test-server:6443", cfg.Host)
}

func TestRestConfigFromSecret_SecretNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := RestConfigFromSecret(context.Background(), fc, "default", v1alpha1.SecretKeyRef{
		Name: "nonexistent",
		Key:  "kubeconfig",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting kubeconfig secret")
}

func TestRestConfigFromSecret_MissingKey(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"wrong-key": []byte(testKubeconfig)},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	_, err := RestConfigFromSecret(context.Background(), fc, "default", v1alpha1.SecretKeyRef{
		Name: "test-kubeconfig",
		Key:  "kubeconfig",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "has no key")
}

func TestRestConfigFromSecret_InvalidKubeconfig(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte("not valid yaml at all {{{")},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	_, err := RestConfigFromSecret(context.Background(), fc, "default", v1alpha1.SecretKeyRef{
		Name: "test-kubeconfig",
		Key:  "kubeconfig",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing kubeconfig")
}
