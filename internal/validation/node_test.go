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
	Name:            "test-cluster",
	AllowedNetworks: []string{"10.0.0.0/16", "10.4.0.0/24"},
}

func baseAnnotations() map[string]string {
	return map[string]string{
		kilonode.AnnotationWireguardIP:   "10.4.0.1/32",
		kilonode.AnnotationPublicKey:     "dGVzdGtleQo=",
		kilonode.AnnotationForceEndpoint: "203.0.113.1:51820",
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
			// Regression guard: the operator previously rejected annotations with a
			// prefix length other than /32 (or /128) via an IsHostRoute check. That
			// check was intentionally dropped to support cozystack-patched Kilo, which
			// writes the full subnet mask (e.g. "10.4.0.1/24") into the annotation.
			// Only the host portion of the address is now validated against AllowedNetworks.
			name: "wireguard IP with subnet mask (cozystack-Kilo style)",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP:   "10.4.0.1/24",
				kilonode.AnnotationPublicKey:     "dGVzdGtleQo=",
				kilonode.AnnotationForceEndpoint: "203.0.113.1:51820",
			}),
			entry:       baseEntry,
			wantSkipped: false,
		},
		{
			name: "no endpoint source skips node",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP: "10.4.0.1/32",
				kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonNoEndpoint,
		},
		{
			name: "malformed clustermesh-endpoint skips node",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP:         "10.4.0.1/32",
				kilonode.AnnotationPublicKey:           "dGVzdGtleQo=",
				kilonode.AnnotationClustermeshEndpoint: "garbage",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonEndpointInvalid,
		},
		{
			name: "malformed force-endpoint skips node",
			node: makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
				kilonode.AnnotationWireguardIP:   "10.4.0.1/32",
				kilonode.AnnotationPublicKey:     "dGVzdGtleQo=",
				kilonode.AnnotationForceEndpoint: "no-colon-at-all",
			}),
			entry:       baseEntry,
			wantSkipped: true,
			wantReason:  validation.ReasonEndpointInvalid,
		},
		{
			name: "ExternalIP-only is accepted",
			node: func() *corev1.Node {
				n := makeNode("node-1", []string{"10.0.1.0/24"}, map[string]string{
					kilonode.AnnotationWireguardIP: "10.4.0.1/32",
					kilonode.AnnotationPublicKey:   "dGVzdGtleQo=",
				})
				n.Status.Addresses = []corev1.NodeAddress{
					{Type: corev1.NodeExternalIP, Address: "203.0.113.42"},
				}
				return n
			}(),
			entry:       baseEntry,
			wantSkipped: false,
		},
		{
			name: "wireguard IP outside allowedNetworks",
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
		{
			// Same host IP with different prefix lengths must be detected as duplicate.
			// cozystack-Kilo writes "10.4.0.1/16"; upstream Kilo writes "10.4.0.1/32".
			// Both result in AllowedIPs = 10.4.0.1/32, so they conflict.
			name: "same host IP different prefix lengths is a duplicate",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/16"}),
				makeNode("node-2", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
			},
			wantDuplicates: map[string]validation.NodeSkipReason{
				"node-2": validation.ReasonWGIPDuplicate,
			},
		},
		{
			// Sanity check: different host IPs with same prefix length must not collide.
			name: "different host IPs are not duplicates",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
				makeNode("node-2", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.2/32"}),
			},
			wantDuplicates: map[string]validation.NodeSkipReason{},
		},
		{
			// Invalid annotation must not collide with a valid annotation that happens
			// to share no parseable IP. Invalid values fall back to raw-string keying
			// so they can still detect identical-invalid copies but never match a valid IP.
			name: "invalid annotation does not match valid annotation",
			nodes: []*corev1.Node{
				makeNode("node-1", nil, map[string]string{kilonode.AnnotationWireguardIP: "not-an-ip"}),
				makeNode("node-2", nil, map[string]string{kilonode.AnnotationWireguardIP: "10.4.0.1/32"}),
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

func TestIsTransient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		reason validation.NodeSkipReason
		want   bool
	}{
		// Bootstrap-pending states that resolve as kubelet / kilo daemon catch up.
		{validation.ReasonNoPodCIDR, true},
		{validation.ReasonNoWireguardIP, true},
		{validation.ReasonNoPublicKey, true},
		{validation.ReasonNoEndpoint, true},

		// Permanent config / data errors. The operator should not requeue
		// silently — these require human intervention.
		{validation.ReasonPodCIDROutOfRange, false},
		{validation.ReasonWGIPInvalid, false},
		{validation.ReasonWGIPOutOfRange, false},
		{validation.ReasonWGIPDuplicate, false},
		{validation.ReasonEndpointInvalid, false},

		// Unknown reason defaults to permanent — fail closed.
		{validation.NodeSkipReason("UnknownReason"), false},
		{validation.NodeSkipReason(""), false},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.reason), func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, validation.IsTransient(testCase.reason))
		})
	}
}
