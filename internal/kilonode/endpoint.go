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
	"net"
	"strconv"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
)

const defaultWireguardPort = 51820

// ResolveEndpoint determines the WireGuard endpoint string ("host:port") for a node.
// Sources are tried in priority order; the first non-empty source wins. A malformed
// annotation value is a hard error (do not fall through to the next source).
// fallbackPort is the UDP port used when synthesising the endpoint from Node.Status.Addresses;
// if 0, defaults to 51820.
// Returns ("", false, nil) when no source yields a value.
// Returns (endpoint, true, nil) on success.
// Returns ("", false, err) when an annotation is present but cannot be parsed as host:port.
func ResolveEndpoint(node *corev1.Node, fallbackPort uint16) (string, bool, error) {
	// Source 1: operator-specific clustermesh-endpoint annotation (highest priority).
	if val, ok := node.Annotations[AnnotationClustermeshEndpoint]; ok && val != "" {
		err := validateHostPort(val)
		if err != nil {
			return "", false, errors.Wrapf(err, "annotation %q on node %q has invalid value %q",
				AnnotationClustermeshEndpoint, node.Name, val)
		}

		return val, true, nil
	}

	// Source 2: Kilo's force-endpoint annotation.
	if val, ok := node.Annotations[AnnotationForceEndpoint]; ok && val != "" {
		err := validateHostPort(val)
		if err != nil {
			return "", false, errors.Wrapf(err, "annotation %q on node %q has invalid value %q",
				AnnotationForceEndpoint, node.Name, val)
		}

		return val, true, nil
	}

	// Source 3: Node.Status.Addresses ExternalIP, preferring IPv4 over IPv6.
	port := fallbackPort
	if port == 0 {
		port = defaultWireguardPort
	}

	if endpoint, ok := resolveFromExternalIPs(node.Status.Addresses, port); ok {
		return endpoint, true, nil
	}

	return "", false, nil
}

// validateHostPort checks that s is a well-formed "host:port" string by
// calling net.SplitHostPort and verifying the port is a valid uint16.
// It does not perform DNS resolution.
func validateHostPort(s string) error {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return errors.Wrapf(err, "not a valid host:port")
	}

	if host == "" {
		return errors.New("host part is empty")
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return errors.Wrapf(err, "port %q is not a valid uint16", portStr)
	}

	if port == 0 {
		return errors.New("port must be non-zero")
	}

	return nil
}

// resolveFromExternalIPs scans the address list for ExternalIP entries,
// preferring IPv4. Returns the first IPv4 ExternalIP if any, otherwise the
// first IPv6 ExternalIP. The endpoint is formatted as "host:port" using
// net.JoinHostPort (which handles IPv6 bracketing automatically).
func resolveFromExternalIPs(addresses []corev1.NodeAddress, port uint16) (string, bool) {
	portStr := strconv.FormatUint(uint64(port), 10)

	var firstIPv6 string

	for _, addr := range addresses {
		if addr.Type != corev1.NodeExternalIP {
			continue
		}

		parsedIP := net.ParseIP(addr.Address)
		if parsedIP == nil {
			// Non-parseable address — skip it silently; not our concern here.
			continue
		}

		if parsedIP.To4() != nil {
			// IPv4 found — return immediately (highest preference).
			return net.JoinHostPort(addr.Address, portStr), true
		}

		// IPv6: record the first one but keep scanning for an IPv4.
		if firstIPv6 == "" {
			firstIPv6 = addr.Address
		}
	}

	if firstIPv6 != "" {
		return net.JoinHostPort(firstIPv6, portStr), true
	}

	return "", false
}
