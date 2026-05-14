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

package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAddToScheme(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, AddToScheme(s))

	gvk := schema.GroupVersionKind{Group: "kilo.squat.ai", Version: "v1alpha1", Kind: "Peer"}
	obj, err := s.New(gvk)
	require.NoError(t, err)
	assert.IsType(t, &Peer{}, obj)

	listGVK := schema.GroupVersionKind{Group: "kilo.squat.ai", Version: "v1alpha1", Kind: "PeerList"}
	listObj, err := s.New(listGVK)
	require.NoError(t, err)
	assert.IsType(t, &PeerList{}, listObj)
}

func TestPeerJSONRoundTrip(t *testing.T) {
	original := Peer{
		Spec: PeerSpec{
			AllowedIPs: []string{"10.0.0.0/24", "10.1.0.0/24"},
			PublicKey:  "dGVzdC1wdWJsaWMta2V5",
			Endpoint: &PeerEndpoint{
				DNSOrIP: DNSOrIP{DNS: "peer.example.com"},
				Port:    51820,
			},
			PersistentKeepalive: 25,
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored Peer
	require.NoError(t, json.Unmarshal(data, &restored))

	assert.Equal(t, original.Spec.AllowedIPs, restored.Spec.AllowedIPs)
	assert.Equal(t, original.Spec.PublicKey, restored.Spec.PublicKey)
	require.NotNil(t, restored.Spec.Endpoint)
	assert.Equal(t, original.Spec.Endpoint.DNS, restored.Spec.Endpoint.DNS)
	assert.Equal(t, original.Spec.Endpoint.Port, restored.Spec.Endpoint.Port)
	assert.Equal(t, original.Spec.PersistentKeepalive, restored.Spec.PersistentKeepalive)
}

func TestPeerDeepCopy(t *testing.T) {
	original := &Peer{
		Spec: PeerSpec{
			AllowedIPs: []string{"10.0.0.0/24"},
			PublicKey:  "key123",
		},
	}

	copied := original.DeepCopy()

	// Modify original.
	original.Spec.AllowedIPs = append(original.Spec.AllowedIPs, "10.1.0.0/24")
	original.Spec.PublicKey = "modified"

	// Verify copy is unchanged.
	assert.Equal(t, []string{"10.0.0.0/24"}, copied.Spec.AllowedIPs)
	assert.Equal(t, "key123", copied.Spec.PublicKey)
}

func TestPeerListDeepCopy(t *testing.T) {
	original := &PeerList{
		Items: []Peer{
			{
				Spec: PeerSpec{
					AllowedIPs: []string{"192.168.0.0/24"},
					PublicKey:  "listkey",
				},
			},
		},
	}

	copied := original.DeepCopy()

	// Modify original.
	original.Items[0].Spec.PublicKey = "changed"
	original.Items = append(original.Items, Peer{})

	// Verify copy is unchanged.
	assert.Len(t, copied.Items, 1)
	assert.Equal(t, "listkey", copied.Items[0].Spec.PublicKey)
}

func TestPeerImplementsRuntimeObject(t *testing.T) {
	var _ runtime.Object = &Peer{}
	var _ runtime.Object = &PeerList{}
}
