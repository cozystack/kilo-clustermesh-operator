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
	"context"
	"log/slog"
	"time"

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/kilonode"
	"github.com/squat/kilo-clustermesh-operator/internal/multicluster"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	"github.com/squat/kilo-clustermesh-operator/internal/restart"
	"github.com/squat/kilo-clustermesh-operator/internal/validation"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

const finalizerName = "kilo-clustermesh.io/cleanup"

// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kilo.squat.ai,resources=peers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;create;update

// ClusterMeshReconciler reconciles a ClusterMesh object.
type ClusterMeshReconciler struct {
	client.Client

	Scheme   *runtime.Scheme
	Registry *multicluster.ClusterRegistry
	Log      *slog.Logger
	Recorder events.EventRecorder

	// Cancel terminates the manager's root context. It is fired when a
	// reconcile error indicates the remote-cluster discovery cache is
	// stale (e.g. a freshly bootstrapped tenant's Peer CRD landed after
	// our first List); kubelet then restarts the pod, rebuilding the
	// registry against current discovery. May be nil in tests.
	Cancel context.CancelFunc
}

// Reconcile implements the main reconciliation loop for ClusterMesh objects.
func (r *ClusterMeshReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With(slog.String("name", req.Name), slog.String("namespace", req.Namespace))

	err := r.reconcile(ctx, log, req)

	// A NoKindMatchError on a remote cluster's REST mapper survives the
	// lifetime of cluster.Cluster because the negative discovery entry is
	// cached. Self-cancel so kubelet rebuilds the registry against fresh
	// discovery — same recovery shape as ChangeWatcher's fingerprint-drift
	// restart.
	restart.TriggerOnStaleDiscovery(err, r.Cancel, log)

	return ctrl.Result{}, err
}

// SetupWithManager registers the controller with the manager.
func (r *ClusterMeshReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ClusterMesh{}).
		Named("clustermesh").
		WithEventFilter(predicate.Funcs{
			DeleteFunc: func(event.DeleteEvent) bool { return false },
		}).
		Complete(r)

	return errors.Wrap(err, "building clustermesh controller")
}

func (r *ClusterMeshReconciler) reconcile(ctx context.Context, log *slog.Logger, req ctrl.Request) error {
	mesh := &v1alpha1.ClusterMesh{}

	err := r.Get(ctx, req.NamespacedName, mesh)
	if err != nil {
		return errors.Wrap(client.IgnoreNotFound(err), "fetching ClusterMesh")
	}

	// Handle deletion via finalizer.
	if !mesh.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, log, mesh)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(mesh, finalizerName) {
		controllerutil.AddFinalizer(mesh, finalizerName)

		err = r.Update(ctx, mesh)
		if err != nil {
			return errors.Wrap(err, "adding finalizer")
		}

		return nil
	}

	// Mesh-level validation.
	if overlap, msg := r.validateMeshNetworks(ctx, log, mesh); overlap {
		return r.setOverlapCondition(ctx, mesh, msg)
	}

	setCondition(mesh, "NetworksOverlap", metav1.ConditionFalse, "NoOverlap", "all CIDRs are disjoint")

	// Reconcile per-cluster peers.
	clusterStatuses, err := r.reconcileAllClusters(ctx, log, mesh)
	if err != nil {
		return err
	}

	// Sweep peers whose source-cluster was removed from spec.Clusters since
	// the last reconcile. ReconcilePeers only sweeps within a single
	// (mesh, source) pair, so a removed cluster's peers would otherwise
	// persist forever in the surviving clusters.
	r.cleanupStaleSourceClusters(ctx, log, mesh)

	// Sweep peers whose owning ClusterMesh CR no longer exists. handleDeletion
	// normally cleans these via the finalizer, but the finalizer may be
	// skipped (force-delete, finalizer manually removed, operator crashloop
	// that prevented the finalizer reconcile from running). Without this
	// sweep, such peers persist forever as ghosts. Any live reconcile takes
	// the global cleanup pass so the cluster always converges.
	r.cleanupOrphanMeshPeers(ctx, log, mesh.Namespace)

	return r.updateStatus(ctx, mesh, clusterStatuses)
}

// cleanupSweepTimeout caps the per-target list/delete pass time so a single
// unreachable or slow cluster does not block the whole reconcile loop. The
// reconciler retries on every tick anyway, so a brief budget is enough to
// either make progress or move on.
const cleanupSweepTimeout = 5 * time.Second

// cleanupStaleSourceClusters walks every cluster the operator knows about
// (the merged registry built from all ClusterMesh resources, not just this
// mesh's current spec) and removes Peer objects this mesh has no business
// owning anymore. Two situations are swept:
//
//   - target cluster was removed from this mesh's spec.Clusters: the mesh
//     should hold no peers at all in that cluster, so every Peer labeled
//     with this mesh's name is deleted there.
//
//   - target cluster is still in spec: only Peers whose source-cluster
//     label points at a cluster no longer in spec are deleted.
//
// Visiting only the current spec.Clusters misses the first case — a peer
// pushed into a now-removed target stays reachable via another
// ClusterMesh's kubeconfig and would otherwise never be touched again.
// Failures are logged and swallowed: a stale peer not deleted this tick
// will be retried on the next reconcile, and we should not block the rest
// of the status update on a single client error.
func (r *ClusterMeshReconciler) cleanupStaleSourceClusters(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh) {
	validSources := make([]string, 0, len(mesh.Spec.Clusters))
	validSet := make(map[string]struct{}, len(mesh.Spec.Clusters))

	for i := range mesh.Spec.Clusters {
		name := mesh.Spec.Clusters[i].Name
		validSources = append(validSources, name)
		validSet[name] = struct{}{}
	}

	for _, name := range r.Registry.Clusters() {
		tgtClient, ok := r.Registry.Client(name)
		if !ok {
			continue
		}

		// Target cluster not in this mesh's spec → drop every Peer this
		// mesh owns there. Target still in spec → keep peers whose source
		// is still in spec, drop the rest.
		sources := validSources
		if _, inSpec := validSet[name]; !inSpec {
			sources = nil
		}

		// Per-target deadline so one unreachable cluster cannot stall the
		// whole reconcile pass — see cleanupSweepTimeout for the rationale.
		// Anonymous function so defer cancel() fires per iteration instead
		// of waiting for the enclosing reconcile to return.
		func() {
			sweepCtx, cancel := context.WithTimeout(ctx, cleanupSweepTimeout)
			defer cancel()

			err := peer.DeleteStaleSourceClusters(sweepCtx, tgtClient, mesh.Name, sources)
			if err != nil {
				log.Warn("cleaning stale source-cluster peers",
					slog.String("target", name),
					slog.String("error", err.Error()),
				)
			}
		}()
	}
}

// cleanupOrphanMeshPeers deletes Peer objects whose kilo-clustermesh.io/mesh
// label names a ClusterMesh that no longer exists in the given namespace.
// It is a global self-healing pass: it scopes by the operator-namespace of
// living ClusterMesh CRs, so it cannot accidentally clobber peers managed
// from another namespace.
//
// Why this exists: handleDeletion is the primary cleanup path, but it
// requires the finalizer to actually run. The finalizer is skipped if:
//   - the CR was force-deleted (e.g. operator was crashlooping and the user
//     manually removed the finalizer to unblock teardown),
//   - the finalizer was never present (legacy CR predating the finalizer),
//   - reconcile-time errors caused the finalizer reconcile to never make it
//     to the peer-deletion step.
//
// Without this sweep, the cluster accumulates ghost peers that no future
// reconcile would ever notice — none of the per-CR cleanup paths look at
// peers labeled for CRs other than their own.
//
// Failures are logged and swallowed: each peer is deleted independently and
// a single client error must not abort the whole pass.
func (r *ClusterMeshReconciler) cleanupOrphanMeshPeers(ctx context.Context, log *slog.Logger, namespace string) {
	living, ok := r.collectLivingMeshes(ctx, log)
	if !ok {
		return
	}

	for _, clusterName := range r.Registry.Clusters() {
		tgtClient, present := r.Registry.Client(clusterName)
		if !present {
			continue
		}

		// Per-target deadline so one unreachable cluster cannot stall the
		// whole reconcile pass — see cleanupSweepTimeout. Wrapped in an
		// anonymous function so defer cancel() fires per iteration.
		func() {
			sweepCtx, cancel := context.WithTimeout(ctx, cleanupSweepTimeout)
			defer cancel()

			r.sweepOrphanPeersInCluster(sweepCtx, log, clusterName, tgtClient, living)
		}()
	}

	// `namespace` is currently unused — see collectLivingMeshes for the
	// reasoning. Keeping the parameter on the function signature documents
	// the reconciling-mesh context for future per-namespace heuristics and
	// avoids breaking the small set of internal call sites.
	_ = namespace
}

// collectLivingMeshes returns the names of every ClusterMesh in the cluster,
// not just the reconciler's own namespace. The operator builds its registry
// cluster-wide (cmd.buildInitialRegistry merges entries from every
// ClusterMesh it sees), so a target cluster's Peer labelled mesh=foo could
// legitimately belong to a foo CR in any namespace — narrowing the lookup
// to one namespace would let the orphan sweep delete a peer owned by a
// living foo CR sitting elsewhere. ok==false indicates the list call itself
// failed; caller should bail without deleting anything.
func (r *ClusterMeshReconciler) collectLivingMeshes(ctx context.Context, log *slog.Logger) (map[string]struct{}, bool) {
	var meshes v1alpha1.ClusterMeshList

	err := r.List(ctx, &meshes)
	if err != nil {
		log.Warn("listing meshes for orphan sweep",
			slog.String("error", err.Error()),
		)

		return nil, false
	}

	living := make(map[string]struct{}, len(meshes.Items))
	for i := range meshes.Items {
		living[meshes.Items[i].Name] = struct{}{}
	}

	return living, true
}

// sweepOrphanPeersInCluster lists every Peer in a target cluster and deletes
// those whose kilo-clustermesh.io/mesh label names a mesh not in living.
// Listing or per-peer delete failures are logged and the sweep continues
// with the remaining peers.
func (r *ClusterMeshReconciler) sweepOrphanPeersInCluster(ctx context.Context, log *slog.Logger, clusterName string, tgtClient client.Client, living map[string]struct{}) {
	var peers kilov1alpha1.PeerList

	err := tgtClient.List(ctx, &peers)
	if err != nil {
		log.Warn("listing peers for orphan sweep",
			slog.String("target", clusterName),
			slog.String("error", err.Error()),
		)

		return
	}

	for i := range peers.Items {
		peerObj := &peers.Items[i]

		meshLabel := peerObj.Labels[peer.LabelMesh]
		if meshLabel == "" {
			continue
		}

		if _, alive := living[meshLabel]; alive {
			continue
		}

		deleteErr := tgtClient.Delete(ctx, peerObj)
		if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			log.Warn("deleting orphan peer",
				slog.String("target", clusterName),
				slog.String("peer", peerObj.Name),
				slog.String("error", deleteErr.Error()),
			)

			continue
		}

		log.Info("deleted orphan peer whose ClusterMesh CR no longer exists",
			slog.String("target", clusterName),
			slog.String("peer", peerObj.Name),
			slog.String("orphan-mesh", meshLabel),
		)
	}
}

// handleDeletion removes all Peers for this mesh from every cluster, then drops the finalizer.
func (r *ClusterMeshReconciler) handleDeletion(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh) error {
	if !controllerutil.ContainsFinalizer(mesh, finalizerName) {
		return nil
	}

	log.Info("deleting peers for mesh being removed")

	for _, entry := range mesh.Spec.Clusters {
		for _, targetEntry := range mesh.Spec.Clusters {
			if targetEntry.Name == entry.Name {
				continue
			}

			targetClient, ok := r.Registry.Client(targetEntry.Name)
			if !ok {
				log.Warn("cluster not found in registry during deletion", slog.String("cluster", targetEntry.Name))

				continue
			}

			err := peer.ReconcilePeers(ctx, targetClient, mesh.Name, entry.Name, nil)
			if err != nil {
				log.Error("deleting peers during cleanup",
					slog.String("source", entry.Name),
					slog.String("target", targetEntry.Name),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	controllerutil.RemoveFinalizer(mesh, finalizerName)

	return errors.Wrap(r.Update(ctx, mesh), "removing finalizer")
}

// validateMeshNetworks lists all ClusterMesh objects and checks for CIDR overlaps.
// Returns (true, message) if an overlap is detected, (false, "") otherwise.
func (r *ClusterMeshReconciler) validateMeshNetworks(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh) (bool, string) {
	allMeshes := &v1alpha1.ClusterMeshList{}

	err := r.List(ctx, allMeshes, client.InNamespace(mesh.Namespace))
	if err != nil {
		log.Error("listing ClusterMesh objects for validation", slog.String("error", err.Error()))

		return false, ""
	}

	err = validation.ValidateMeshNetworks(allMeshes.Items)
	if err != nil {
		return true, err.Error()
	}

	return false, ""
}

// setOverlapCondition sets NetworksOverlap=True and Ready=False on the status.
func (r *ClusterMeshReconciler) setOverlapCondition(ctx context.Context, mesh *v1alpha1.ClusterMesh, msg string) error {
	setCondition(mesh, "NetworksOverlap", metav1.ConditionTrue, "CIDROverlap", msg)
	setCondition(mesh, "Ready", metav1.ConditionFalse, "NetworksOverlap", "CIDR overlap detected across meshes")

	return errors.Wrap(r.Status().Update(ctx, mesh), "updating status on CIDR overlap")
}

// reconcileAllClusters runs the per-cluster-pair reconciliation loop.
func (r *ClusterMeshReconciler) reconcileAllClusters(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh) ([]v1alpha1.ClusterStatus, error) {
	statuses := make([]v1alpha1.ClusterStatus, 0, len(mesh.Spec.Clusters))

	for i := range mesh.Spec.Clusters {
		srcEntry := &mesh.Spec.Clusters[i]

		srcClient, ok := r.Registry.Client(srcEntry.Name)
		if !ok {
			log.Warn("source cluster not found in registry", slog.String("cluster", srcEntry.Name))

			continue
		}

		nodes, err := listNodes(ctx, srcClient)
		if err != nil {
			return nil, errors.Wrapf(err, "listing nodes for cluster %q", srcEntry.Name)
		}

		r.ensureNodeEndpoints(ctx, log, srcClient, srcEntry, nodes)

		validNodes, skipped := r.filterNodes(log, mesh, nodes, srcEntry)
		status := v1alpha1.ClusterStatus{Name: srcEntry.Name, SkippedNodes: skipped}

		err = r.pushPeersToTargets(ctx, log, mesh, srcEntry, validNodes, &status)
		if err != nil {
			return nil, err
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// ensureNodeEndpoints derives a kilo.squat.ai/force-endpoint annotation
// from each node's InternalIPv4 when no other endpoint source is available
// and writes it back into the source cluster. This removes the need for an
// operator-installed per-cluster annotator DaemonSet: every cluster
// referenced by a ClusterMesh resource is reachable via the same
// kubeconfig the operator already uses to publish Peer objects.
//
// Patch failures are logged and skipped, not propagated, because the
// reconciler still has useful work to do on the cluster pair even if
// a single node can't be annotated.
func (r *ClusterMeshReconciler) ensureNodeEndpoints(ctx context.Context, log *slog.Logger, srcClient client.Client, srcEntry *v1alpha1.ClusterEntry, nodes []corev1.Node) {
	for i := range nodes {
		node := &nodes[i]

		patched, err := kilonode.EnsureForceEndpoint(ctx, srcClient, node, srcEntry.WireguardPort)
		if err != nil {
			log.Warn("failed to set force-endpoint annotation",
				slog.String("cluster", srcEntry.Name),
				slog.String("node", node.Name),
				slog.String("error", err.Error()),
			)

			continue
		}

		if patched {
			log.Info("set force-endpoint annotation",
				slog.String("cluster", srcEntry.Name),
				slog.String("node", node.Name),
				slog.String("endpoint", node.Annotations[kilonode.AnnotationForceEndpoint]),
			)
		}
	}
}

// filterNodes validates nodes and returns valid nodes and the count of skipped ones.
func (r *ClusterMeshReconciler) filterNodes(log *slog.Logger, mesh *v1alpha1.ClusterMesh, nodes []corev1.Node, entry *v1alpha1.ClusterEntry) ([]*corev1.Node, int) {
	skipped := 0
	ptrs := make([]*corev1.Node, 0, len(nodes))

	for i := range nodes {
		ptrs = append(ptrs, &nodes[i])
	}

	duplicates := validation.FindDuplicateWGIPs(ptrs)
	valid := make([]*corev1.Node, 0, len(ptrs))

	for _, node := range ptrs {
		if reason, isDup := duplicates[node.Name]; isDup {
			log.Warn("skipping node with duplicate WireGuard IP",
				slog.String("node", node.Name),
				slog.String("reason", string(reason)),
			)
			r.Recorder.Eventf(mesh, nil, corev1.EventTypeWarning, string(reason), "SkipNodePeering", "node %s has duplicate WireGuard IP", node.Name)

			skipped++

			continue
		}

		skip, reason, msg := validation.ValidateNode(node, entry)
		if skip {
			log.Warn("skipping invalid node",
				slog.String("node", node.Name),
				slog.String("reason", string(reason)),
				slog.String("msg", msg),
			)
			r.Recorder.Eventf(mesh, nil, corev1.EventTypeWarning, string(reason), "SkipNodePeering", "%s", msg)

			skipped++

			continue
		}

		valid = append(valid, node)
	}

	return valid, skipped
}

// pushPeersToTargets reconciles peers for srcEntry's nodes into every other cluster.
func (r *ClusterMeshReconciler) pushPeersToTargets(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh, srcEntry *v1alpha1.ClusterEntry, validNodes []*corev1.Node, status *v1alpha1.ClusterStatus) error {
	desired, err := buildDesiredPeers(mesh.Name, srcEntry, validNodes)
	if err != nil {
		return errors.Wrapf(err, "building peers for cluster %q", srcEntry.Name)
	}

	status.RegisteredPeers = len(desired)

	for _, tgtEntry := range mesh.Spec.Clusters {
		if tgtEntry.Name == srcEntry.Name {
			continue
		}

		tgtClient, ok := r.Registry.Client(tgtEntry.Name)
		if !ok {
			log.Warn("target cluster not found in registry", slog.String("cluster", tgtEntry.Name))

			continue
		}

		err = peer.ReconcilePeers(ctx, tgtClient, mesh.Name, srcEntry.Name, desired)
		if err != nil {
			return errors.Wrapf(err, "reconciling peers from %q to %q", srcEntry.Name, tgtEntry.Name)
		}
	}

	return nil
}

// updateStatus sets Ready=True and writes the cluster statuses.
func (r *ClusterMeshReconciler) updateStatus(ctx context.Context, mesh *v1alpha1.ClusterMesh, statuses []v1alpha1.ClusterStatus) error {
	mesh.Status.Clusters = statuses
	setCondition(mesh, "Ready", metav1.ConditionTrue, "Reconciled", "all clusters reconciled successfully")

	return errors.Wrap(r.Status().Update(ctx, mesh), "updating status")
}

// buildDesiredPeers constructs the desired Peer slice for all valid nodes.
// The first valid node carries the cluster-wide CIDRs (serviceCIDR and any
// AdditionalCIDRs) folded into its Peer.AllowedIPs. The older design emitted
// a separate anchor Peer that reused the anchor node's WireGuard public key;
// WireGuard's per-pubkey dedup made the second `wg setconf` call to apply
// either the node or the anchor entry clobber the AllowedIPs of the other,
// silently losing pod-CIDR or service-CIDR routing in a racy way. Folding
// the anchor CIDRs into the first node Peer keeps a single WG peer entry
// per pubkey with the full union of AllowedIPs.
func buildDesiredPeers(meshName string, entry *v1alpha1.ClusterEntry, nodes []*corev1.Node) ([]*kilov1alpha1.Peer, error) {
	peers := make([]*kilov1alpha1.Peer, 0, len(nodes))

	anchorExtras := peer.CollectAnchorCIDRs(entry)

	for i, node := range nodes {
		var extras []string
		if i == 0 {
			extras = anchorExtras
		}

		p, err := peer.BuildPeer(meshName, entry, node, extras)
		if err != nil {
			return nil, errors.Wrapf(err, "building peer for node %q", node.Name)
		}

		peers = append(peers, p)
	}

	return peers, nil
}

// listNodes lists all nodes using the given client.
func listNodes(ctx context.Context, c client.Client) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}

	err := c.List(ctx, nodeList)
	if err != nil {
		return nil, errors.Wrap(err, "listing nodes")
	}

	return nodeList.Items, nil
}

// setCondition is a helper that calls apimeta.SetStatusCondition.
func setCondition(mesh *v1alpha1.ClusterMesh, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&mesh.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mesh.Generation,
	})
}
