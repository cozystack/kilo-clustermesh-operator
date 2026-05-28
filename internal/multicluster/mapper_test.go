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

package multicluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// TestResettableDynamicMapperImplementsResettable is the load-bearing
// invariant of the entire stale-discovery recovery path: the mapper we
// hand to controller-runtime must satisfy meta.ResettableRESTMapper so
// restart.RefreshMapperOnNoMatch can call Reset() through the standard
// interface. Without this, the recovery path silently no-ops in
// production.
func TestResettableDynamicMapperImplementsResettable(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:6443"}

	httpClient, err := rest.HTTPClientFor(cfg)
	require.NoError(t, err)

	m, err := newResettableDynamicMapper(cfg, httpClient)
	require.NoError(t, err)

	_, ok := m.(meta.ResettableRESTMapper)
	assert.True(t, ok, "newResettableDynamicMapper must satisfy meta.ResettableRESTMapper; otherwise restart.RefreshMapperOnNoMatch is a no-op")
}

// TestRawDynamicMapperIsNotResettable documents the upstream gap this
// wrapper exists to close. If a future controller-runtime release adds
// Reset() to the dynamic mapper, this test will flip and the wrapper
// can be deleted. Failing here means the wrapper is no longer needed.
func TestRawDynamicMapperIsNotResettable(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:6443"}

	httpClient, err := rest.HTTPClientFor(cfg)
	require.NoError(t, err)

	raw, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	require.NoError(t, err)

	_, ok := raw.(meta.ResettableRESTMapper)
	assert.False(t, ok, "apiutil.NewDynamicRESTMapper now implements meta.ResettableRESTMapper — the resettableDynamicMapper wrapper can be removed")
}

// TestResettableDynamicMapperResetReplacesUnderlying verifies that
// Reset swaps the underlying mapper for a new instance, rather than
// being a no-op stub.
func TestResettableDynamicMapperResetReplacesUnderlying(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:6443"}

	httpClient, err := rest.HTTPClientFor(cfg)
	require.NoError(t, err)

	mAny, err := newResettableDynamicMapper(cfg, httpClient)
	require.NoError(t, err)

	wrapper, ok := mAny.(*resettableDynamicMapper)
	require.True(t, ok)

	before := wrapper.current()
	wrapper.Reset()
	after := wrapper.current()

	assert.NotSame(t, before, after, "Reset must replace the underlying mapper with a fresh instance")
}
