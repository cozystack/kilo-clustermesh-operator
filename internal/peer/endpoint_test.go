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

// This file is intentionally in package peer (not peer_test) so it can access
// the unexported parseEndpoint function.
package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

func TestParseEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    *kilov1alpha1.PeerEndpoint
		wantErr bool
	}{
		{
			name: "IPv6 bracketed address",
			raw:  "[2001:db8::1]:51820",
			want: &kilov1alpha1.PeerEndpoint{
				Port:    51820,
				DNSOrIP: kilov1alpha1.DNSOrIP{IP: "2001:db8::1"},
			},
		},
		{
			name: "IPv6 loopback",
			raw:  "[::1]:51820",
			want: &kilov1alpha1.PeerEndpoint{
				Port:    51820,
				DNSOrIP: kilov1alpha1.DNSOrIP{IP: "::1"},
			},
		},
		{
			name: "IPv4 address",
			raw:  "203.0.113.1:51820",
			want: &kilov1alpha1.PeerEndpoint{
				Port:    51820,
				DNSOrIP: kilov1alpha1.DNSOrIP{IP: "203.0.113.1"},
			},
		},
		{
			name: "DNS name",
			raw:  "node.example.com:51820",
			want: &kilov1alpha1.PeerEndpoint{
				Port:    51820,
				DNSOrIP: kilov1alpha1.DNSOrIP{DNS: "node.example.com"},
			},
		},
		{
			name:    "missing colon - invalid format",
			raw:     "no-colon-at-all",
			wantErr: true,
		},
		{
			name:    "non-numeric port",
			raw:     "1.2.3.4:notaport",
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseEndpoint(testCase.raw)

			if testCase.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, testCase.want.Port, got.Port)
			assert.Equal(t, testCase.want.IP, got.IP)
			assert.Equal(t, testCase.want.DNS, got.DNS)
		})
	}
}
