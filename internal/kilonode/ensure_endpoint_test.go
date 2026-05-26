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

package kilonode_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
)

func ensureScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()

	err := corev1.AddToScheme(scheme)
	require.NoError(t, err)

	return scheme
}

func nodeWithAddresses(name string, annotations map[string]string, addrs ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func TestEnsureForceEndpoint_PatchesFromInternalIPv4(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", nil,
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 51820)
	require.NoError(t, err)
	assert.True(t, patched)
	assert.Equal(t, "10.0.1.7:51820", node.Annotations[kilonode.AnnotationForceEndpoint])
}

func TestEnsureForceEndpoint_SkipsWhenExternalIPPresent(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", nil,
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 51820)
	require.NoError(t, err)
	assert.False(t, patched)
	assert.NotContains(t, node.Annotations, kilonode.AnnotationForceEndpoint)
}

func TestEnsureForceEndpoint_SkipsWhenForceEndpointAlreadySet(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", map[string]string{
		kilonode.AnnotationForceEndpoint: "operator-supplied.example.com:51820",
	},
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 51820)
	require.NoError(t, err)
	assert.False(t, patched)
	assert.Equal(t, "operator-supplied.example.com:51820", node.Annotations[kilonode.AnnotationForceEndpoint])
}

func TestEnsureForceEndpoint_SkipsWhenClustermeshAnnotationSet(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", map[string]string{
		kilonode.AnnotationClustermeshEndpoint: "203.0.113.7:51820",
	},
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 51820)
	require.NoError(t, err)
	assert.False(t, patched)
	assert.NotContains(t, node.Annotations, kilonode.AnnotationForceEndpoint)
}

func TestEnsureForceEndpoint_SkipsWhenNoIPv4InternalIP(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", nil,
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "2001:db8::7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 51820)
	require.NoError(t, err)
	assert.False(t, patched)
	assert.NotContains(t, node.Annotations, kilonode.AnnotationForceEndpoint)
}

func TestEnsureForceEndpoint_DefaultsPortToWellKnown(t *testing.T) {
	t.Parallel()

	scheme := ensureScheme(t)
	node := nodeWithAddresses("n1", nil,
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
	)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	patched, err := kilonode.EnsureForceEndpoint(context.Background(), fc, node, 0)
	require.NoError(t, err)
	assert.True(t, patched)
	assert.Equal(t, "10.0.1.7:51820", node.Annotations[kilonode.AnnotationForceEndpoint])
}
