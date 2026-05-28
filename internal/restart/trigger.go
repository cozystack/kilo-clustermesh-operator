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
	"errors"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/meta"
)

// RefreshMapperOnNoMatch resets the given REST mapper when err carries a
// NoKindMatchError. Without this, controller-runtime's REST mapper caches
// the negative discovery entry for the lifetime of the cluster.Cluster — a
// freshly bootstrapped tenant that installs its Peer CRD only after our
// first List(PeerList{}) would deadlock the reconcile loop forever, with
// requeue-with-backoff never refreshing discovery.
//
// Resetting the mapper instead of restarting the operator pod avoids
// kubelet CrashLoopBackOff (which inflates to a 5-minute wait after a
// handful of restarts), lets the manager keep its leader lease, and
// scopes recovery to the one cluster whose CRD state actually drifted.
// The next reconcile picks up the fresh mapping via Discovery and the
// peer push succeeds without further intervention.
//
// mapper may be nil — the call is then a no-op. If the mapper does not
// implement meta.ResettableRESTMapper (an in-memory test fake, say) the
// call also no-ops; that is acceptable because the production code path
// always builds clusters through controller-runtime's dynamic mapper.
// log may be nil — logging is then skipped.
// Returns true when Reset() was actually invoked, so callers can avoid
// double-emitting the recovery event.
func RefreshMapperOnNoMatch(err error, mapper meta.RESTMapper, log *slog.Logger) bool {
	if err == nil || mapper == nil {
		return false
	}

	var noMatch *meta.NoKindMatchError
	if !errors.As(err, &noMatch) {
		return false
	}

	resettable, ok := mapper.(meta.ResettableRESTMapper)
	if !ok {
		return false
	}

	resettable.Reset()

	if log != nil {
		log.Info("reset remote-cluster REST mapper after NoMatchError; next reconcile will refresh discovery",
			slog.String("groupKind", noMatch.GroupKind.String()),
			slog.String("error", err.Error()),
		)
	}

	return true
}
