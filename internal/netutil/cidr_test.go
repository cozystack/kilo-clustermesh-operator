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

package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"valid IPv4", "10.0.0.0/16", "10.0.0.0/16", false},
		{"IPv4 masks to network", "10.0.1.5/16", "10.0.0.0/16", false},
		{"valid IPv6", "fd00::/64", "fd00::/64", false},
		{"IPv4 host route", "10.0.0.1/32", "10.0.0.1/32", false},
		{"IPv6 host route", "fd00::1/128", "fd00::1/128", false},
		{"invalid string", "not-a-cidr", "", true},
		{"empty string", "", "", true},
		{"IP without mask", "10.0.0.1", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCIDR(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestCIDRContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		outer string
		inner string
		want  bool
	}{
		{"subset", "10.0.0.0/8", "10.1.0.0/16", true},
		{"equal", "10.0.0.0/16", "10.0.0.0/16", true},
		{"not subset", "10.0.0.0/16", "10.1.0.0/16", false},
		{"inner extends beyond", "10.0.0.0/16", "10.0.128.0/15", false},
		{"host in network", "10.0.0.0/24", "10.0.0.5/32", true},
		{"host outside", "10.0.0.0/24", "10.0.1.5/32", false},
		{"IPv6 subset", "fd00::/48", "fd00::/64", true},
		{"IPv6 not subset", "fd00::/64", "fd01::/64", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outer := mustParse(t, tt.outer)
			inner := mustParse(t, tt.inner)
			assert.Equal(t, tt.want, CIDRContains(outer, inner))
		})
	}
}

func TestCIDROverlaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "10.0.0.0/16", "10.0.0.0/16", true},
		{"subset", "10.0.0.0/8", "10.1.0.0/16", true},
		{"partial overlap", "10.0.0.0/15", "10.1.0.0/16", true},
		{"adjacent no overlap", "10.0.0.0/16", "10.1.0.0/16", false},
		{"completely separate", "10.0.0.0/8", "192.168.0.0/16", false},
		{"IPv6 overlap", "fd00::/48", "fd00::/64", true},
		{"IPv6 no overlap", "fd00::/48", "fd01::/48", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			netA := mustParse(t, tt.a)
			netB := mustParse(t, tt.b)
			assert.Equal(t, tt.want, CIDROverlaps(netA, netB))
		})
	}
}

func TestIsHostRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cidr string
		want bool
	}{
		{"IPv4 /32", "10.0.0.1/32", true},
		{"IPv4 /24", "10.0.0.0/24", false},
		{"IPv4 /0", "0.0.0.0/0", false},
		{"IPv6 /128", "fd00::1/128", true},
		{"IPv6 /64", "fd00::/64", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			net := mustParse(t, tt.cidr)
			assert.Equal(t, tt.want, IsHostRoute(net))
		})
	}
}

func mustParse(t *testing.T, s string) *net.IPNet {
	t.Helper()

	n, err := ParseCIDR(s)
	require.NoError(t, err)

	return n
}
