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

// Package containerfile contains tests that validate the repository's
// Containerfile metadata labels.
package containerfile_test

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot returns the absolute path to the repository root by walking up from
// this test file's location until a go.mod is found.
func repoRoot(t *testing.T) string {
	t.Helper()

	_, callerFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller returned no file info")

	dir := filepath.Dir(callerFile)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")

		dir = parent
	}
}

func TestContainerfileImageSourceLabel(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	containerfilePath := filepath.Join(root, "Containerfile")

	f, err := os.Open(containerfilePath)
	require.NoError(t, err, "opening Containerfile")

	defer f.Close()

	const labelKey = "org.opencontainers.image.source"
	const wantOwner = "cozystack/kilo-clustermesh-operator"

	var found bool

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "LABEL") {
			continue
		}

		if !strings.Contains(line, labelKey) {
			continue
		}

		found = true
		assert.Contains(t, line, wantOwner,
			"org.opencontainers.image.source label must reference cozystack/kilo-clustermesh-operator, got: %q", line)
	}

	require.NoError(t, scanner.Err(), "scanning Containerfile")
	require.True(t, found, "org.opencontainers.image.source LABEL not found in Containerfile")
}
