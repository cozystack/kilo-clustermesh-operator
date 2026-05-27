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
	"context"
	"errors"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/meta"
)

// TriggerOnStaleDiscovery cancels the manager's context when err carries a
// NoKindMatchError, asking the pod to terminate so kubelet restarts it with
// a freshly-built remote-cluster registry.
//
// Why: each remote cluster in the registry owns a controller-runtime
// cluster.Cluster, whose REST mapper caches discovery results. When a
// freshly-bootstrapped tenant cluster installs its Peer CRD only after the
// ClusterMesh CR is reconciled for the first time, the mapper caches a
// negative result and never refreshes — every subsequent List(PeerList{})
// against that target fails with NoKindMatchError until the pod restarts.
// This mirrors ChangeWatcher's self-restart pattern, which already handles
// fingerprint changes by cancelling the same context.
//
// cancel may be nil (tests/fakes) — the call is then a no-op.
// log may be nil — logging is then skipped.
// Returns true if cancel was invoked, so callers can avoid further work
// that would race with the pod shutdown.
func TriggerOnStaleDiscovery(err error, cancel context.CancelFunc, log *slog.Logger) bool {
	if err == nil || cancel == nil {
		return false
	}

	var noMatch *meta.NoKindMatchError
	if !errors.As(err, &noMatch) {
		return false
	}

	if log != nil {
		log.Info("remote cluster discovery returned NoMatchError; triggering self-restart to refresh REST mapper",
			slog.String("groupKind", noMatch.GroupKind.String()),
			slog.String("error", err.Error()),
		)
	}

	cancel()

	return true
}
