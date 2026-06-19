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
	"encoding/json"
	"net"
	"strconv"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// AnnotationDiscoveredEndpoints is the node annotation written by Kilo
	// containing the WireGuard endpoints observed via successful handshakes.
	// Value is a JSON map of base64-encoded WireGuard public key → {IP, Port}.
	AnnotationDiscoveredEndpoints = "kilo.squat.ai/discovered-endpoints"
)

// discoveredEndpointEntry is the JSON shape of a single entry in the
// kilo.squat.ai/discovered-endpoints annotation value. Kilo serialises
// these fields with upper-case keys ("IP", "Port") so the json tags must
// match exactly.
//
//nolint:tagliatelle // Kilo uses upper-case JSON keys: {"IP":"…","Port":…}
type discoveredEndpointEntry struct {
	IP   string `json:"IP"`
	Port int    `json:"Port"`
}

// DiscoveredEndpointsByKey reads kilo.squat.ai/discovered-endpoints from every
// node in the cluster reachable via kubeClient and returns a map of
// WireGuard public-key string → "host:port" endpoint string. Only entries
// where the IP parses as a valid global-scope unicast address are included.
//
// The returned map represents what endpoints peer nodes have actually observed
// for each WireGuard key via successful handshakes. For nodes behind NAT this
// is the real public/NAT IP — more accurate than kilo.squat.ai/endpoint which
// reflects the node's own interface address.
func DiscoveredEndpointsByKey(ctx context.Context, kubeClient client.Client) (map[string]string, error) {
	var nodeList corev1.NodeList

	err := kubeClient.List(ctx, &nodeList)
	if err != nil {
		return nil, errors.Wrap(err, "listing nodes for discovered-endpoint lookup")
	}

	result := make(map[string]string)

	for i := range nodeList.Items {
		raw, ok := nodeList.Items[i].Annotations[AnnotationDiscoveredEndpoints]
		if !ok || raw == "" {
			continue
		}

		var entries map[string]discoveredEndpointEntry

		unmarshalErr := json.Unmarshal([]byte(raw), &entries)
		if unmarshalErr != nil {
			// Malformed annotation — skip this node silently.
			continue
		}

		for pubKey, entry := range entries {
			if entry.IP == "" || entry.Port <= 0 {
				continue
			}

			ip := net.ParseIP(entry.IP)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}

			// First occurrence wins; all nodes that observed a handshake with
			// the same key should have seen the same source IP (the peer's NAT
			// egress), so any entry is equally authoritative.
			if _, seen := result[pubKey]; !seen {
				result[pubKey] = net.JoinHostPort(entry.IP, strconv.Itoa(entry.Port))
			}
		}
	}

	return result, nil
}
