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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

// ChangeWatcher watches ClusterMesh resources and referenced Secrets.
// When the cluster configuration fingerprint changes, it cancels the
// manager's context to trigger a pod restart. The operator watches
// cluster-wide; each ClusterMesh references its kubeconfig Secret by
// name only, so Secrets are resolved in the namespace of the CR that
// references them.
type ChangeWatcher struct {
	client.Client

	Cancel           context.CancelFunc
	StartFingerprint string
	Log              *slog.Logger
}

// Reconcile recomputes the cluster configuration fingerprint.
// If it differs from the startup fingerprint, it triggers a restart.
func (w *ChangeWatcher) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	fingerprint, err := w.computeFingerprint(ctx)
	if err != nil {
		w.Log.Error("computing fingerprint", slog.String("error", err.Error()))

		return reconcile.Result{}, err
	}

	if fingerprint != w.StartFingerprint {
		w.Log.Info("cluster configuration changed, triggering restart",
			slog.String("old", w.StartFingerprint),
			slog.String("new", fingerprint),
		)

		if w.Cancel != nil {
			w.Cancel()
		}
	}

	return reconcile.Result{}, nil
}

// SetupWithManager registers the watcher with the manager.
func (w *ChangeWatcher) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		Named("change-watcher").
		For(&v1alpha1.ClusterMesh{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			w.secretToClusterMesh,
		)).
		Complete(w)

	return errors.Wrap(err, "building change-watcher controller")
}

// ComputeFingerprint calculates the fingerprint for the current state.
// Exported so main.go can call it at startup to set StartFingerprint.
func (w *ChangeWatcher) ComputeFingerprint(ctx context.Context) (string, error) {
	return w.computeFingerprint(ctx)
}

// clusterRef is the deterministic representation used for fingerprinting.
type clusterRef struct {
	Name            string `json:"name"`
	SecretNamespace string `json:"secretNamespace"`
	SecretName      string `json:"secretName"`
	SecretRV        string `json:"secretRV"` //nolint:tagliatelle // "RV" is the canonical Go abbreviation for ResourceVersion; "Rv" would be misleading
}

// secretResourceVersion fetches the ResourceVersion for a Secret; returns "" on error.
func (w *ChangeWatcher) secretResourceVersion(ctx context.Context, namespace, name string) string {
	var secret corev1.Secret

	err := w.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret)
	if err != nil {
		return ""
	}

	return secret.ResourceVersion
}

// collectRefs builds the sorted slice of clusterRefs from all ClusterMeshes.
func (w *ChangeWatcher) collectRefs(ctx context.Context, meshes []v1alpha1.ClusterMesh) []clusterRef {
	var refs []clusterRef

	for i := range meshes {
		meshNS := meshes[i].Namespace

		for j := range meshes[i].Spec.Clusters {
			c := &meshes[i].Spec.Clusters[j]
			ref := clusterRef{Name: c.Name}

			if c.KubeconfigSecretRef != nil {
				ref.SecretNamespace = meshNS
				ref.SecretName = c.KubeconfigSecretRef.Name
				ref.SecretRV = w.secretResourceVersion(ctx, meshNS, c.KubeconfigSecretRef.Name)
			}

			refs = append(refs, ref)
		}
	}

	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Name < refs[j].Name
	})

	return refs
}

func (w *ChangeWatcher) computeFingerprint(ctx context.Context) (string, error) {
	var meshes v1alpha1.ClusterMeshList

	err := w.List(ctx, &meshes)
	if err != nil {
		return "", errors.Wrap(err, "listing ClusterMeshes")
	}

	refs := w.collectRefs(ctx, meshes.Items)

	data, _ := json.Marshal(refs)
	hash := sha256.Sum256(data)

	return hex.EncodeToString(hash[:]), nil
}

// secretToClusterMesh maps a Secret change to reconcile requests for
// all ClusterMeshes that reference it. Match is by Secret's own namespace
// (which equals the namespace of the ClusterMesh that referenced it,
// because KubeconfigSecretRef has no namespace field).
func (w *ChangeWatcher) secretToClusterMesh(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	var meshes v1alpha1.ClusterMeshList

	err := w.List(ctx, &meshes, client.InNamespace(obj.GetNamespace()))
	if err != nil {
		return nil
	}

	var requests []reconcile.Request

	for i := range meshes.Items {
		for j := range meshes.Items[i].Spec.Clusters {
			c := &meshes.Items[i].Spec.Clusters[j]

			if c.KubeconfigSecretRef != nil && c.KubeconfigSecretRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      meshes.Items[i].Name,
						Namespace: meshes.Items[i].Namespace,
					},
				})
			}
		}
	}

	return requests
}
