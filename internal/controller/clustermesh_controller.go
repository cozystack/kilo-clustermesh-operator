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
	"net"
	"strconv"
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

// bootstrapRequeueAfter caps the wait between reconcile attempts while
// at least one source cluster is still bootstrapping. "Bootstrapping"
// here means any of:
//
//   - kubeconfig secret not yet merged into the registry,
//   - apiserver up but no kubelet has joined yet (OpenStack VM still
//     provisioning, etc.),
//   - nodes have joined but the kilo daemon has not yet written the
//     per-node annotations (wireguard-ip, public-key) that
//     validateNode requires.
//
// The reconciler watches only ClusterMesh CRs, not the Nodes of remote
// clusters, so a reconcile that runs while no source node is valid yet
// completes silently with no error and no useful work — controller
// runtime then has nothing to requeue on, and the next attempt has to
// wait for an external trigger (Cozystack Package controller
// re-applying the CR, etc.). That gap produced a 17-minute mesh-up
// delay in the wild for a tenant whose VM landed a few minutes after
// the operator's first reconcile, and an indefinite stall for a tenant
// whose kilo daemon needed a few extra seconds to annotate the node
// after kubelet was already Ready. A short periodic requeue is a cheap
// belt-and-braces against this race.
const bootstrapRequeueAfter = 30 * time.Second

// syncRequeueAfter is the interval at which a fully-converged mesh is
// re-reconciled even when no ClusterMesh CR event has fired. This covers
// the node scale-out case: when a new worker node joins a source cluster
// the ClusterMesh CR itself does not change, so the controller gets no
// watch event. Without this period the new node never receives a Peer
// object, and any CSI driver or workload running hostNetwork=true on that
// node cannot reach the remote cluster (observed as ceph-csi-cephfs mount
// failures with "no mds up" until a manual annotation-based reconcile was
// triggered). Five minutes is a reasonable upper bound for the worst-case
// delay: node joins → kilo daemon annotates → next sync window → Peer
// created → WireGuard tunnel up → CSI mount succeeds.
const syncRequeueAfter = 5 * time.Minute

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
}

// Reconcile implements the main reconciliation loop for ClusterMesh objects.
func (r *ClusterMeshReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With(slog.String("name", req.Name), slog.String("namespace", req.Namespace))

	incomplete, err := r.reconcile(ctx, log, req)

	// A NoKindMatchError on a remote cluster's REST mapper survives the
	// lifetime of cluster.Cluster because the negative discovery entry
	// is cached. Reset every remote-cluster mapper so the next reconcile
	// re-discovers the missing kind; the source target is unknown at
	// this level (the wrapped error carries it as text only), and
	// Reset() is cheap — it only invalidates the in-memory cache and
	// the next List() pays a one-time discovery round-trip. Self-heal
	// without taking the operator pod down, which would drop the leader
	// lease and inflate to a CrashLoopBackOff after a few stale CRDs.
	for _, m := range r.Registry.Mappers() {
		restart.RefreshMapperOnNoMatch(err, m, log)
	}

	// Schedule a periodic retry. On error we leave RequeueAfter zero so
	// controller-runtime applies its own exponential backoff via the rate
	// limiter; mixing RequeueAfter with an error would defeat the backoff.
	//
	// Two intervals:
	//   - bootstrapRequeueAfter (30 s): while at least one source cluster
	//     is still bootstrapping — fast convergence during initial setup.
	//   - syncRequeueAfter (5 m): once everything is healthy — periodic
	//     sweep that picks up new nodes added to source clusters (no CR
	//     event fires when a remote node joins, so without this the new
	//     node would never get a Peer object).
	if err == nil {
		if incomplete {
			return ctrl.Result{RequeueAfter: bootstrapRequeueAfter}, nil
		}
		return ctrl.Result{RequeueAfter: syncRequeueAfter}, nil
	}

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

// reconcile is the body of Reconcile. The bool return is true when the
// reconcile completed without error but at least one source cluster was
// still bootstrapping (missing from the registry, or no nodes yet); the
// caller translates that into a RequeueAfter so the controller does not
// stall waiting for an external event on the ClusterMesh CR.
func (r *ClusterMeshReconciler) reconcile(ctx context.Context, log *slog.Logger, req ctrl.Request) (bool, error) {
	mesh := &v1alpha1.ClusterMesh{}

	err := r.Get(ctx, req.NamespacedName, mesh)
	if err != nil {
		return false, errors.Wrap(client.IgnoreNotFound(err), "fetching ClusterMesh")
	}

	// Handle deletion via finalizer.
	if !mesh.DeletionTimestamp.IsZero() {
		return false, r.handleDeletion(ctx, log, mesh)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(mesh, finalizerName) {
		controllerutil.AddFinalizer(mesh, finalizerName)

		err = r.Update(ctx, mesh)
		if err != nil {
			return false, errors.Wrap(err, "adding finalizer")
		}

		return false, nil
	}

	// Mesh-level validation.
	if overlap, msg := r.validateMeshNetworks(ctx, log, mesh); overlap {
		return false, r.setOverlapCondition(ctx, mesh, msg)
	}

	setCondition(mesh, "NetworksOverlap", metav1.ConditionFalse, "NoOverlap", "all CIDRs are disjoint")

	// Reconcile per-cluster peers.
	clusterStatuses, incomplete, err := r.reconcileAllClusters(ctx, log, mesh)
	if err != nil {
		return false, err
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

	return incomplete, r.updateStatus(ctx, mesh, clusterStatuses)
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

	for ei := range mesh.Spec.Clusters {
		entry := &mesh.Spec.Clusters[ei]
		for ti := range mesh.Spec.Clusters {
			targetEntry := &mesh.Spec.Clusters[ti]
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

// reconcileAllClusters runs the per-cluster-pair reconciliation loop. The
// bool return is true when at least one source cluster is still
// bootstrapping (missing from the registry, or zero nodes joined yet),
// so the caller can schedule a periodic requeue.
func (r *ClusterMeshReconciler) reconcileAllClusters(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh) ([]v1alpha1.ClusterStatus, bool, error) {
	statuses := make([]v1alpha1.ClusterStatus, 0, len(mesh.Spec.Clusters))

	incomplete := false

	for i := range mesh.Spec.Clusters {
		srcEntry := &mesh.Spec.Clusters[i]

		srcClient, ok := r.Registry.Client(srcEntry.Name)
		if !ok {
			log.Warn("source cluster not found in registry", slog.String("cluster", srcEntry.Name))

			incomplete = true

			continue
		}

		nodes, err := listNodes(ctx, srcClient)
		if err != nil {
			return nil, false, errors.Wrapf(err, "listing nodes for cluster %q", srcEntry.Name)
		}

		validNodes, skipped, transientSkipped := r.filterNodes(log, mesh, nodes, srcEntry)
		status := v1alpha1.ClusterStatus{Name: srcEntry.Name, SkippedNodes: skipped}

		// Source has no nodes that pass validateNode. Decide whether
		// to schedule a periodic requeue based on whether there is a
		// reason to expect recovery on its own:
		//
		//   - len(nodes) == 0 — apiserver up but no kubelet joined
		//     yet (e.g. OpenStack VM still being provisioned). Will
		//     resolve as the cluster settles. Requeue.
		//   - transientSkipped > 0 — at least one node is in a
		//     bootstrap-pending skip state (NodeNoWireguardIP /
		//     NodeNoPublicKey / NodeNoPodCIDR). The kilo daemon or node
		//     controller will write the missing annotation shortly.
		//     Requeue. (A node with no resolvable endpoint is NOT
		//     skipped — it is peered as a roaming peer — so it never
		//     contributes to this count.)
		//   - all skips are permanent (PodCIDROutOfRange, WGIPInvalid,
		//     WGIPOutOfRange, WGIPDuplicate, EndpointInvalid) — these
		//     are configuration / data errors that retry cannot fix.
		//     Logging a WARN and leaving the controller idle is
		//     better than burning 30s reconciles forever; ops needs
		//     to see the static error and act.
		//
		// In steady state ceph as a source has cephstg01 valid (the
		// location leader) and cephstg02/03 skipped by design (kilo
		// per-location granularity, NodeNoWireguardIP — classified as
		// transient by IsTransient even though it is in fact
		// permanent for those nodes). That would normally arm the
		// requeue forever, but validNodes > 0 because the leader
		// passes, so the timer does not fire. The "all transient,
		// none valid" shape only triggers during real bootstrap
		// windows.
		switch {
		case len(validNodes) == 0 && (len(nodes) == 0 || transientSkipped > 0):
			log.Info("source cluster has no valid nodes yet; will requeue",
				slog.String("cluster", srcEntry.Name),
				slog.Int("nodes", len(nodes)),
				slog.Int("skipped", skipped),
				slog.Int("transientSkipped", transientSkipped),
				slog.Duration("after", bootstrapRequeueAfter),
			)

			incomplete = true
		case len(validNodes) == 0 && skipped > 0:
			log.Warn("source cluster has no valid nodes and all skips are permanent; mesh will not converge without intervention",
				slog.String("cluster", srcEntry.Name),
				slog.Int("nodes", len(nodes)),
				slog.Int("skipped", skipped),
			)
		}

		err = r.pushPeersToTargets(ctx, log, mesh, srcEntry, validNodes, &status)
		if err != nil {
			return nil, false, err
		}

		statuses = append(statuses, status)
	}

	return statuses, incomplete, nil
}

// filterNodes validates nodes and returns valid nodes, the total count
// of skipped ones, and how many of those skips are transient (i.e.
// expected to resolve as the kilo daemon / kubelet finishes bootstrap;
// see validation.IsTransient). The caller uses transientSkipped to
// decide whether scheduling a periodic requeue would do any good.
func (r *ClusterMeshReconciler) filterNodes(log *slog.Logger, mesh *v1alpha1.ClusterMesh, nodes []corev1.Node, entry *v1alpha1.ClusterEntry) ([]*corev1.Node, int, int) {
	skipped := 0
	transientSkipped := 0
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

			// Duplicate WG IPs are always a configuration error — two
			// nodes claiming the same address. No retry will fix it.
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

			if validation.IsTransient(reason) {
				transientSkipped++
			}

			continue
		}

		valid = append(valid, node)
	}

	return valid, skipped, transientSkipped
}

// pushPeersToTargets reconciles peers for srcEntry's nodes into every other cluster.
func (r *ClusterMeshReconciler) pushPeersToTargets(ctx context.Context, log *slog.Logger, mesh *v1alpha1.ClusterMesh, srcEntry *v1alpha1.ClusterEntry, validNodes []*corev1.Node, status *v1alpha1.ClusterStatus) error {
	desired, err := buildDesiredPeers(mesh.Name, srcEntry, validNodes, meshPersistentKeepalive(mesh))
	if err != nil {
		return errors.Wrapf(err, "building peers for cluster %q", srcEntry.Name)
	}

	status.RegisteredPeers = len(desired)

	for i := range mesh.Spec.Clusters {
		tgtEntry := &mesh.Spec.Clusters[i]

		if tgtEntry.Name == srcEntry.Name {
			continue
		}

		tgtClient, ok := r.Registry.Client(tgtEntry.Name)
		if !ok {
			log.Warn("target cluster not found in registry", slog.String("cluster", tgtEntry.Name))

			continue
		}

		// Enrich peer endpoints with real NAT-observed IPs from the target
		// cluster's nodes. Kilo on every target node records the actual source
		// IP of each successful WireGuard handshake in
		// kilo.squat.ai/discovered-endpoints. For source clusters behind NAT
		// (no ExternalIP, only InternalIP) the discovered IP is the true
		// reachable endpoint, whereas the Peer spec may contain only the
		// internal address. Preferring the discovered value lets the operator
		// self-heal: after the source cluster's Kilo initiates the first
		// handshake, subsequent reconciles automatically use the correct
		// external endpoint without any manual annotation.
		//
		// Enrichment is computed independently per target cluster: each target
		// may observe different source IPs for the same peer (e.g. different
		// NAT gateways), so we must not reuse enriched peers across targets.
		pushDesired := desired

		enriched, enrichErr := r.enrichEndpointsFromDiscovered(ctx, log, tgtClient, desired)
		if enrichErr != nil {
			// Non-fatal: log and continue with the original endpoints.
			log.Warn("enriching peer endpoints from discovered-endpoints failed; using configured endpoints",
				slog.String("source", srcEntry.Name),
				slog.String("target", tgtEntry.Name),
				slog.String("error", enrichErr.Error()),
			)
		} else {
			pushDesired = enriched
		}

		err = peer.ReconcilePeers(ctx, tgtClient, mesh.Name, srcEntry.Name, pushDesired)
		if err != nil {
			return errors.Wrapf(err, "reconciling peers from %q to %q", srcEntry.Name, tgtEntry.Name)
		}
	}

	return nil
}

// enrichEndpointsFromDiscovered replaces the configured endpoint on each
// desired Peer with the endpoint observed via WireGuard handshakes on the
// target cluster's nodes, when a more-specific (non-internal) address is
// available. Source clusters behind NAT (no ExternalIP) only advertise their
// InternalIP as the configured endpoint; the discovered value is the actual
// egress IP after SNAT, which is what the target cluster must use to reach
// them.
//
// The function is best-effort: if the target cluster is unreachable or has no
// discovered-endpoint data, the original peers are returned unchanged.
func (r *ClusterMeshReconciler) enrichEndpointsFromDiscovered(
	ctx context.Context,
	log *slog.Logger,
	tgtClient client.Client,
	desired []*kilov1alpha1.Peer,
) ([]*kilov1alpha1.Peer, error) {
	discoveredByKey, lookupErr := kilonode.DiscoveredEndpointsByKey(ctx, tgtClient)
	if lookupErr != nil {
		return desired, errors.Wrap(lookupErr, "listing nodes for discovered-endpoint lookup")
	}

	if len(discoveredByKey) == 0 {
		return desired, nil
	}

	enriched := make([]*kilov1alpha1.Peer, len(desired))

	for i, peerObj := range desired {
		discoveredEndpoint, ok := discoveredByKey[peerObj.Spec.PublicKey]
		if !ok {
			enriched[i] = peerObj

			continue
		}

		// Only override when the discovered address differs from the
		// configured one. Skip if the Peer already has the right endpoint.
		configured := ""
		if peerObj.Spec.Endpoint != nil {
			configured = peerObj.Spec.Endpoint.IP
		}

		if configured == "" || discoveredEndpoint != configured+":"+strconv.Itoa(int(peerObj.Spec.Endpoint.Port)) {
			parsedEndpoint, parseErr := parseDiscoveredEndpoint(discoveredEndpoint)
			if parseErr != nil {
				log.Warn("ignoring malformed discovered endpoint",
					slog.String("peer", peerObj.Name),
					slog.String("endpoint", discoveredEndpoint),
					slog.String("error", parseErr.Error()),
				)
				enriched[i] = peerObj

				continue
			}

			updated := peerObj.DeepCopy()
			updated.Spec.Endpoint = parsedEndpoint
			enriched[i] = updated

			log.Debug("overriding peer endpoint with discovered value",
				slog.String("peer", peerObj.Name),
				slog.String("configured", configured),
				slog.String("discovered", discoveredEndpoint),
			)

			continue
		}

		enriched[i] = peerObj
	}

	return enriched, nil
}

// parseDiscoveredEndpoint parses a "host:port" string into a *kilov1alpha1.PeerEndpoint.
func parseDiscoveredEndpoint(hostPort string) (*kilov1alpha1.PeerEndpoint, error) {
	host, portStr, splitErr := net.SplitHostPort(hostPort)
	if splitErr != nil {
		return nil, errors.Wrap(splitErr, "splitting discovered endpoint host:port")
	}

	port, parseErr := strconv.ParseUint(portStr, 10, 16)
	if parseErr != nil {
		return nil, errors.Wrapf(parseErr, "parsing port in discovered endpoint %q", hostPort)
	}

	return &kilov1alpha1.PeerEndpoint{
		DNSOrIP: kilov1alpha1.DNSOrIP{IP: host},
		Port:    uint32(port),
	}, nil
}

// updateStatus sets Ready=True and writes the cluster statuses.
func (r *ClusterMeshReconciler) updateStatus(ctx context.Context, mesh *v1alpha1.ClusterMesh, statuses []v1alpha1.ClusterStatus) error {
	mesh.Status.Clusters = statuses
	setCondition(mesh, "Ready", metav1.ConditionTrue, "Reconciled", "all clusters reconciled successfully")

	return errors.Wrap(r.Status().Update(ctx, mesh), "updating status")
}

// buildDesiredPeers constructs the desired Peer slice for all valid nodes.
// The first valid node carries the cluster-wide CIDRs from AllowedNetworks
// that are not covered by any per-node value (e.g. the service CIDR or
// host-network ranges) folded into its Peer.AllowedIPs. The older design
// emitted a separate anchor Peer that reused the anchor node's WireGuard
// public key; WireGuard's per-pubkey dedup made the second `wg setconf` call
// to apply either the node or the anchor entry clobber the AllowedIPs of the
// other, silently losing pod-CIDR or service-CIDR routing in a racy way.
// Folding the anchor CIDRs into the first node Peer keeps a single WG peer
// entry per pubkey with the full union of AllowedIPs.
//
// meshKeepalive is the maximum PersistentKeepalive declared across every entry
// in the mesh; it is the keepalive floor applied to every peer so that a NAT'd
// cluster keeps its mapping refreshed in BOTH directions (the ceph-side peer of
// a tenant node is built from the tenant's SELF entry, whose own keepalive is
// 0).
func buildDesiredPeers(meshName string, entry *v1alpha1.ClusterEntry, nodes []*corev1.Node, meshKeepalive int) ([]*kilov1alpha1.Peer, error) {
	peers := make([]*kilov1alpha1.Peer, 0, len(nodes))

	anchorExtras := peer.CollectAnchorCIDRs(entry, nodes)

	for i, node := range nodes {
		var extras []string
		if i == 0 {
			extras = anchorExtras
		}

		p, err := peer.BuildPeer(meshName, entry, node, extras, meshKeepalive)
		if err != nil {
			return nil, errors.Wrapf(err, "building peer for node %q", node.Name)
		}

		peers = append(peers, p)
	}

	return peers, nil
}

// meshPersistentKeepalive returns the maximum PersistentKeepalive declared
// across every cluster entry in the mesh. When any cluster in the mesh sits
// behind NAT (and so declares a keepalive), this value is applied as a floor to
// every peer the mesh builds — including the ceph-side peers derived from a
// cluster's own entry, whose own keepalive is typically 0 — so the NAT mapping
// is refreshed in both directions and the tunnel does not flap.
func meshPersistentKeepalive(mesh *v1alpha1.ClusterMesh) int {
	keepalive := 0

	for i := range mesh.Spec.Clusters {
		keepalive = max(keepalive, mesh.Spec.Clusters[i].PersistentKeepalive)
	}

	return keepalive
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
