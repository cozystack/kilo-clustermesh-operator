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

// Package netutil provides network CIDR validation and comparison utilities.
package netutil

import (
	"net"

	"github.com/cockroachdb/errors"
)

// ParseCIDR parses a CIDR string and returns the network (masked).
// Unlike net.ParseCIDR, it returns an error via cockroachdb/errors.
func ParseCIDR(s string) (*net.IPNet, error) {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid CIDR %q", s)
	}

	return network, nil
}

// CIDRContains returns true if inner is entirely within outer.
// Both the network address and the broadcast address of inner must fall within outer.
func CIDRContains(outer, inner *net.IPNet) bool {
	if !outer.Contains(inner.IP) {
		return false
	}

	lastIP := lastAddr(inner)

	return outer.Contains(lastIP)
}

// CIDROverlaps returns true if a and b share any IP addresses.
func CIDROverlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// IsHostRoute returns true if the CIDR represents a single host (/32 for IPv4, /128 for IPv6).
func IsHostRoute(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()

	return ones == bits
}

// lastAddr returns the last (broadcast) address of a CIDR.
func lastAddr(n *net.IPNet) net.IP {
	last := make(net.IP, len(n.IP))
	copy(last, n.IP)

	for i := range last {
		last[i] |= ^n.Mask[i]
	}

	return last
}
