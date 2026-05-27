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

func TestTriggerOnStaleDiscovery_NilErr(t *testing.T) {
	called := false
	cancel := func() { called = true }

	triggered := TriggerOnStaleDiscovery(nil, cancel, testLogger())

	assert.False(t, triggered, "nil err must not trigger")
	assert.False(t, called, "cancel must not be invoked on nil err")
}

func TestTriggerOnStaleDiscovery_NilCancel(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	triggered := TriggerOnStaleDiscovery(noMatch, nil, testLogger())

	assert.False(t, triggered, "nil cancel must yield false even for NoMatch error")
}

func TestTriggerOnStaleDiscovery_UnrelatedErr(t *testing.T) {
	called := false
	cancel := func() { called = true }

	triggered := TriggerOnStaleDiscovery(errUnrelated, cancel, testLogger())

	assert.False(t, triggered, "unrelated err must not trigger")
	assert.False(t, called, "cancel must not be invoked for non-NoMatch errors")
}

func TestTriggerOnStaleDiscovery_PlainNoMatch(t *testing.T) {
	called := false
	cancel := func() { called = true }

	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	triggered := TriggerOnStaleDiscovery(noMatch, cancel, testLogger())

	assert.True(t, triggered, "plain NoKindMatchError must trigger")
	assert.True(t, called, "cancel must be invoked")
}

func TestTriggerOnStaleDiscovery_WrappedNoMatch(t *testing.T) {
	called := false
	cancel := func() { called = true }

	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}
	wrapped := cockerrors.Wrap(cockerrors.Wrap(noMatch, "listing existing peers"), "reconciling peers from \"a\" to \"b\"")

	triggered := TriggerOnStaleDiscovery(wrapped, cancel, testLogger())

	assert.True(t, triggered, "wrapped NoKindMatchError must trigger (the controller wraps the error twice in production)")
	assert.True(t, called, "cancel must be invoked")
}

func TestTriggerOnStaleDiscovery_NilLogger(t *testing.T) {
	called := false
	cancel := func() { called = true }

	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kilo.squat.ai", Kind: "Peer"}}

	assert.NotPanics(t, func() {
		TriggerOnStaleDiscovery(noMatch, cancel, nil)
	}, "nil logger must be tolerated")
	assert.True(t, called)
}
