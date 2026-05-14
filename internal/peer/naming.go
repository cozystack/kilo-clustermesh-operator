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

package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)

const (
	// LabelMesh identifies which ClusterMesh created this Peer.
	LabelMesh = "kilo-clustermesh.io/mesh"

	// LabelSourceCluster identifies which cluster this Peer represents a node from.
	LabelSourceCluster = "kilo-clustermesh.io/source-cluster"

	// maxNameLength is the maximum length for a Kubernetes resource name.
	maxNameLength = 253
)

// Name generates a deterministic, unique Peer object name.
// Format: <mesh>--<sourceCluster>--<nodeName>, truncated + hashed if too long.
func Name(meshName, sourceCluster, nodeName string) string {
	raw := fmt.Sprintf("%s--%s--%s", meshName, sourceCluster, nodeName)

	sanitized := sanitizeDNS(raw)

	if len(sanitized) <= maxNameLength {
		return sanitized
	}

	// If too long, use a hash suffix to guarantee uniqueness.
	hash := sha256.Sum256([]byte(raw))
	hashStr := hex.EncodeToString(hash[:8]) // 16 hex chars

	// room for "-" + 16 hash chars = 17 chars
	truncated := strings.TrimRight(sanitized[:maxNameLength-17], "-.")

	return truncated + "-" + hashStr
}

// Labels returns the standard labels for a generated Peer.
func Labels(meshName, sourceCluster string) map[string]string {
	return map[string]string{
		LabelMesh:          meshName,
		LabelSourceCluster: sourceCluster,
	}
}

// OrphanSelector returns a label selector matching all Peers for a given mesh and source cluster.
func OrphanSelector(meshName, sourceCluster string) labels.Selector {
	return labels.SelectorFromSet(Labels(meshName, sourceCluster))
}

// sanitizeDNS converts input into a valid DNS-1123 subdomain name.
// It lowercases the input, replaces invalid characters with dashes,
// and trims leading/trailing dashes and dots. Consecutive dashes
// are preserved (used as intentional segment separators).
func sanitizeDNS(input string) string {
	lowered := strings.ToLower(input)

	var bld strings.Builder

	bld.Grow(len(lowered))

	for _, r := range lowered {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			bld.WriteRune(r)
		default:
			bld.WriteRune('-')
		}
	}

	result := strings.TrimLeft(bld.String(), "-.")
	result = strings.TrimRight(result, "-.")

	return result
}
