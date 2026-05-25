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

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/squat/kilo-clustermesh-operator/internal/multicluster"
	"github.com/squat/kilo-clustermesh-operator/internal/peer"
	"github.com/squat/kilo-clustermesh-operator/internal/validation"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

const finalizerName = "kilo-clustermesh.io/cleanup"

// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kilo.squat.ai,resources=clustermeshes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
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

	mesh := &v1alpha1.ClusterMesh{}

	err := r.Get(ctx, req.NamespacedName, mesh)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(client.IgnoreNotFound(err), "fetching ClusterMesh")
	}

	// Handle deletion via finalizer.
	if !mesh.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.handleDeletion(ctx, log, mesh)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(mesh, finalizerName) {
		controllerutil.AddFinalizer(mesh, finalizerName)

		err = r.Update(ctx, mesh)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "adding finalizer")
		}

		return ctrl.Result{}, nil
	}

	// Mesh-level validation.
	if overlap, msg := r.validateMeshNetworks(ctx, log, mesh); overlap {
		return ctrl.Result{}, r.setOverlapCondition(ctx, mesh, msg)
	}

	setCondition(mesh, "NetworksOverlap", metav1.ConditionFalse, "NoOverlap", "all CIDRs are disjoint")

	// Reconcile per-cluster peers.
	clusterStatuses, err := r.reconcileAllClusters(ctx, log, mesh)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateStatus(ctx, mesh, clusterStatuses)
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

// buildDesiredPeers constructs the desired Peer slice for all valid nodes plus an optional anchor.
func buildDesiredPeers(meshName string, entry *v1alpha1.ClusterEntry, nodes []*corev1.Node) ([]*kilov1alpha1.Peer, error) {
	peers := make([]*kilov1alpha1.Peer, 0, len(nodes)+1)

	for _, node := range nodes {
		p, err := peer.BuildPeer(meshName, entry, node)
		if err != nil {
			return nil, errors.Wrapf(err, "building peer for node %q", node.Name)
		}

		peers = append(peers, p)
	}

	if len(nodes) > 0 {
		if anchor := peer.BuildAnchorPeer(meshName, entry, nodes[0]); anchor != nil {
			peers = append(peers, anchor)
		}
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
