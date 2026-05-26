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

package kilonode

import (
	"context"
	"net"
	"strconv"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureForceEndpoint sets kilo.squat.ai/force-endpoint=<InternalIPv4>:<port>
// on a node when none of the operator-recognised endpoint sources are
// available (clustermesh-endpoint annotation, force-endpoint annotation,
// or a NodeExternalIP address). This lets the operator bring meshed
// clusters online even when nodes only have InternalIPs — for example
// OpenStack tenants that intentionally skip floating IPs because every
// node in every participating cluster lives on the same underlay
// network and the InternalIPs are mutually routable.
//
// Returns true if the node was patched (caller may want to refresh its
// in-memory copy of the annotations map). The Node argument is mutated
// in place on success.
//
// Skips silently and returns false when:
//   - any explicit endpoint source is already set,
//   - no IPv4 InternalIP is present (we don't auto-pick IPv6 because
//     not every underlay reaches the same IPv6 prefix).
func EnsureForceEndpoint(ctx context.Context, kubeClient client.Client, node *corev1.Node, port uint16) (bool, error) {
	if _, ok := node.Annotations[AnnotationClustermeshEndpoint]; ok {
		return false, nil
	}

	if _, ok := node.Annotations[AnnotationForceEndpoint]; ok {
		return false, nil
	}

	var internalIP string

	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeExternalIP {
			if ip := net.ParseIP(addr.Address); ip != nil {
				return false, nil
			}
		}

		if addr.Type == corev1.NodeInternalIP && internalIP == "" {
			if ip := net.ParseIP(addr.Address); ip != nil && ip.To4() != nil {
				internalIP = addr.Address
			}
		}
	}

	if internalIP == "" {
		return false, nil
	}

	if port == 0 {
		port = DefaultWireguardPort
	}

	endpoint := net.JoinHostPort(internalIP, strconv.FormatUint(uint64(port), 10))

	patch := client.MergeFrom(node.DeepCopy())

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Annotations[AnnotationForceEndpoint] = endpoint

	err := kubeClient.Patch(ctx, node, patch)
	if err != nil {
		return false, errors.Wrapf(err, "patching force-endpoint on node %q", node.Name)
	}

	return true, nil
}
