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
	"net/http"
	"sync"

	"github.com/cockroachdb/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// resettableDynamicMapper wraps apiutil.NewDynamicRESTMapper with a
// Reset() that atomically swaps the underlying mapper for a fresh one.
//
// controller-runtime's dynamic mapper does not implement
// meta.ResettableRESTMapper on its own: the unexported *apiutil.mapper
// type exposes KindFor, RESTMapping and the rest of meta.RESTMapper but
// has no Reset method. Without this wrapper, internal/restart's
// recovery path cannot invalidate the discovery cache through the
// standard interface and silently no-ops in production.
//
// Reset reconstructs the underlying dynamic mapper. NewDynamicRESTMapper
// is lazy (it does not perform discovery at construction time, only on
// first lookup), so Reset is cheap; the next List against an uncached
// kind triggers one discovery round-trip against the target apiserver.
// On rebuild failure the previous mapper is preserved so callers still
// have a working mapper for kinds already in cache; the next Reset
// retries the rebuild.
//
// The proxy methods deliberately do not wrap the underlying mapper's
// errors. Wrapping with a non-apimachinery error type would change the
// error chain in ways errors.As consumers downstream (notably
// restart.RefreshMapperOnNoMatch, which fishes out *meta.NoKindMatchError)
// already handle, but adding a wrap layer here gives no diagnostic
// value over the upstream error and only increases chain depth.
type resettableDynamicMapper struct {
	cfg        *rest.Config
	httpClient *http.Client

	mu     sync.RWMutex
	mapper meta.RESTMapper
}

var _ meta.ResettableRESTMapper = (*resettableDynamicMapper)(nil)

// newResettableDynamicMapper builds the wrapper and pre-constructs the
// initial dynamic mapper so the first RESTMapper call does not pay the
// build cost. The signature matches cluster.Options.MapperProvider so
// it can be passed straight into cluster.New.
func newResettableDynamicMapper(cfg *rest.Config, httpClient *http.Client) (meta.RESTMapper, error) {
	m, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return nil, errors.Wrap(err, "building dynamic REST mapper")
	}

	return &resettableDynamicMapper{
		cfg:        cfg,
		httpClient: httpClient,
		mapper:     m,
	}, nil
}

// Reset replaces the underlying dynamic mapper with a freshly built
// one. On rebuild failure the existing mapper is kept and the call
// silently no-ops; the next Reset will retry. Concurrent callers see
// the swap atomically.
func (r *resettableDynamicMapper) Reset() {
	fresh, err := apiutil.NewDynamicRESTMapper(r.cfg, r.httpClient)
	if err != nil {
		return
	}

	r.mu.Lock()
	r.mapper = fresh
	r.mu.Unlock()
}

func (r *resettableDynamicMapper) KindFor(input schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return r.current().KindFor(input) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) KindsFor(input schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return r.current().KindsFor(input) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return r.current().ResourceFor(input) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return r.current().ResourcesFor(input) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	return r.current().RESTMapping(gk, versions...) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return r.current().RESTMappings(gk, versions...) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) ResourceSingularizer(resource string) (string, error) {
	return r.current().ResourceSingularizer(resource) //nolint:wrapcheck // thin proxy; wrapping would obscure errors.As consumers
}

func (r *resettableDynamicMapper) current() meta.RESTMapper {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.mapper
}
