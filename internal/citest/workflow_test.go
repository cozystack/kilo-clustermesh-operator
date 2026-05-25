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

// Package citest validates structural properties of the CI workflow files.
package citest_test

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

// TestCIWorkflowIncludesCmdPackage asserts that the unit-test job in ci.yml
// explicitly includes ./cmd/... so that tests in cmd/ (e.g. TestMergeClusterSpecs)
// are not silently skipped.
func TestCIWorkflowIncludesCmdPackage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	ciPath := filepath.Join(root, ".github", "workflows", "ci.yml")

	f, err := os.Open(ciPath)
	require.NoError(t, err, "opening ci.yml")

	defer f.Close()

	var found bool

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "go test") && strings.Contains(line, "./cmd/...") {
			found = true

			break
		}
	}

	require.NoError(t, scanner.Err(), "scanning ci.yml")
	assert.True(t, found,
		"ci.yml unit-test job must include ./cmd/... in the go test invocation; "+
			"TestMergeClusterSpecs in cmd/main_test.go is otherwise never executed in CI")
}
