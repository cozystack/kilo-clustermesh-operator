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

package controller

import (
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errSentinel = errors.New("reconcile failed")

func TestSelectResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		incomplete       bool
		err              error
		wantRequeueAfter time.Duration
		wantErr          bool
	}{
		{
			name:             "error disables RequeueAfter so rate-limiter backoff applies",
			incomplete:       false,
			err:              errSentinel,
			wantRequeueAfter: time.Duration(0), // zero — let controller-runtime backoff
			wantErr:          true,
		},
		{
			name:             "incomplete triggers fast bootstrap requeue",
			incomplete:       true,
			err:              nil,
			wantRequeueAfter: bootstrapRequeueAfter,
			wantErr:          false,
		},
		{
			name:             "fully converged triggers slow periodic sync",
			incomplete:       false,
			err:              nil,
			wantRequeueAfter: syncRequeueAfter,
			wantErr:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := selectResult(tc.incomplete, tc.err)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errSentinel)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tc.wantRequeueAfter, result.RequeueAfter,
				"unexpected RequeueAfter in ctrl.Result")
		})
	}
}
