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

package restart

import (
	"testing"

	cockerrors "github.com/cockroachdb/errors"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var errUnrelated = cockerrors.New("network blew up")

// resettableSpy is a meta.ResettableRESTMapper that counts Reset() calls
// and delegates the rest of the interface to an unused fallback. The
// helper only ever invokes Reset; the other methods are not exercised
// from production code paths under test.
type resettableSpy struct {
	meta.RESTMapper

	resets int
}

func (r *resettableSpy) Reset() { r.resets++ }

func TestRefreshMapperOnNoMatch_NilErr(t *testing.T) {
	spy := &resettableSpy{}

	called := RefreshMapperOnNoMatch(nil, spy, testLogger())

	assert.False(t, called, "nil err must not reset")
	assert.Equal(t, 0, spy.resets, "Reset must not be invoked on nil err")
}

func TestRefreshMapperOnNoMatch_NilMapper(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	called := RefreshMapperOnNoMatch(noMatch, nil, testLogger())

	assert.False(t, called, "nil mapper must yield false even for NoMatch error")
}

func TestRefreshMapperOnNoMatch_UnrelatedErr(t *testing.T) {
	spy := &resettableSpy{}

	called := RefreshMapperOnNoMatch(errUnrelated, spy, testLogger())

	assert.False(t, called, "unrelated err must not reset")
	assert.Equal(t, 0, spy.resets, "Reset must not be invoked for non-NoMatch errors")
}

func TestRefreshMapperOnNoMatch_PlainNoMatch(t *testing.T) {
	spy := &resettableSpy{}
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	called := RefreshMapperOnNoMatch(noMatch, spy, testLogger())

	assert.True(t, called, "plain NoKindMatchError must reset")
	assert.Equal(t, 1, spy.resets, "Reset must be invoked exactly once")
}

func TestRefreshMapperOnNoMatch_WrappedNoMatch(t *testing.T) {
	spy := &resettableSpy{}
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}
	wrapped := cockerrors.Wrap(cockerrors.Wrap(noMatch, "listing existing peers"), "reconciling peers from \"a\" to \"b\"")

	called := RefreshMapperOnNoMatch(wrapped, spy, testLogger())

	assert.True(t, called, "wrapped NoKindMatchError must reset (the controller wraps the error twice in production)")
	assert.Equal(t, 1, spy.resets)
}

func TestRefreshMapperOnNoMatch_NonResettableMapper(t *testing.T) {
	// A bare meta.RESTMapper that does not implement ResettableRESTMapper.
	// Production builds always get a resettable mapper from
	// apiutil.NewDynamicRESTMapper, but tests/fakes may pass anything.
	bare := stubRESTMapper{}

	called := RefreshMapperOnNoMatch(
		&meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "g", Kind: "K"}},
		bare,
		testLogger(),
	)

	assert.False(t, called, "non-resettable mapper must yield false")
}

func TestRefreshMapperOnNoMatch_NilLogger(t *testing.T) {
	spy := &resettableSpy{}
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	assert.NotPanics(t, func() {
		RefreshMapperOnNoMatch(noMatch, spy, nil)
	}, "nil logger must be tolerated")
	assert.Equal(t, 1, spy.resets)
}

// stubRESTMapper is a placeholder meta.RESTMapper that does not implement
// ResettableRESTMapper. It is used only to verify the type-assertion
// guard; none of its methods are called in tests.
type stubRESTMapper struct{ meta.RESTMapper }
