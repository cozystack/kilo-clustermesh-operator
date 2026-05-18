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

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/validation"
)

func makeNode(name string, podCIDRs []string, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: corev1.NodeSpec{
			PodCIDRs: podCIDRs,
		},
	}
}

var baseEntry = &v1alpha1.ClusterEntry{
	Name:          "test-cluster",
	PodCIDRs:      []string{"10.0.0.0/16"},
	WireguardCIDR: "10.4.0.0/24",
}

func baseAnnotations() map[string]string {
	return map[string]string{
		kilonode.AnnotationWireguardIP: "10.4.0.1/32",
		kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
	}
}

func TestValidateNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		node        *corev1.Node
		entry       *v1alpha1.ClusterEntry
		wantSkipped bool
		wantReason  validation.NodeSkipReason
	}{
		{
			name:        "valid node",
			node:        makeNode("node-1", []string{"10.0.1.0/24"}, baseAnnotations()),
			entry:       baseEntry,
			wantSkipped: false,
		},
		{
			name:        "no podCIDR",
			node:        makeNode("node-1", []string{}, baseAnnotations()),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonNoPodCIDR,
		},
		{
			name:        "podCIDR out of range",
			node:        makeNode("node-1", []string{"192.168.1.0/24"}, baseAnnotations()),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonPodCIDROutOfRange,
		},
		{
			name: "no wireguard IP annotation",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationPublicKey: "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonNoWireguardIP,
		},
		{
			name: "wireguard IP unparseable",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "not-a-cidr",
				kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonWGIPInvalid,
		},
		{
			name: "wireguard IP with subnet mask (cozystack-Kilo style)",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "10.4.0.1/24",
				kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: false,
		},
		{
			name: "wireguard IP outside wireguardCIDR",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "10.5.0.1/32",
				kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonWGIPOutOfRange,
		},
		{
			name: "wireguard IP host outside but network overlaps",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				// 10.5.0.1/16 → host 10.5.0.1 is outside 10.4.0.0/24
				kilonode.AnnotationWireguardIP: "10.5.0.1/16",
				kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonWGIPOutOfRange,
		},
		{
			name: "no public key annotation",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "10.4.0.1/32",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonNoPublicKey,
		},
		{
			name: "empty public key annotation",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "10.4.0.1/32",
				kilonode.AnnotationPublicKey:   "",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonNoPublicKey,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			skipped, reason, msg := validation.ValidateNode(testCase.node, testCase.entry)

			assert.Equal(t, testCase.wantSkipped, skipped)
			assert.Equal(t, testCase.wantReason, reason)

			if testCase.wantSkipped {
				assert.NotEmpty(t, msg, "skipped node must have a non-empty message")
			} else {
				assert.Empty(t, msg)
			}
		})
	}
}

func TestFindDuplicateWGIPs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		nodes          []*corev1.Node
		wantDuplicates map[string]validation.NodeSkipReason
	}{
		{
			name: "no duplicates",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
				makeNode("node-2", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.2/32"}),
				makeNode("node-3", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.3/32"}),
			},
			wantDuplicates: map[string]validation.NodeSkipReason{},
		},
		{
			name: "one duplicate",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
				makeNode("node-2", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
				makeNode("node-3", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.3/32"}),
			},
			wantDuplicates: map[string]validation.NodeSkipReason{
				"node-2": validation.ReasonWGIPDuplicate,
			},
		},
		{
			name: "missing annotation ignored",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
				makeNode("node-2", nil, map[string]string{}),
				makeNode("node-3", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.3/32"}),
			},
			wantDuplicates: map[string]validation.NodeSkipReason{},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := validation.FindDuplicateWGIPs(testCase.nodes)

			assert.Equal(t, testCase.wantDuplicates, result)
		})
	}
}
