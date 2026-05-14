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

// Package kilonode provides constants and helpers for Kilo node annotations.
package kilonode

const (
	// AnnotationWireguardIP is the node annotation containing the WireGuard interface IP.
	// Value format: "10.4.0.1/32" (must be a host route).
	AnnotationWireguardIP = "kilo.squat.ai/wireguard-ip"

	// AnnotationPublicKey is the node annotation containing the WireGuard public key.
	AnnotationPublicKey = "kilo.squat.ai/key"

	// AnnotationForceEndpoint is the node annotation specifying the WireGuard endpoint.
	// Value format: "203.0.113.1:51820" or "node.example.com:51820".
	AnnotationForceEndpoint = "kilo.squat.ai/force-endpoint"

	// AnnotationLocation is the node annotation for Kilo's location grouping.
	AnnotationLocation = "kilo.squat.ai/location"
)
