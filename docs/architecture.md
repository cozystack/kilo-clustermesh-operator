# Architecture

> Component map and reconciliation flow of the Kilo ClusterMesh Operator.

## Table of Contents

- [Overview](#overview)
- [Components](#components)
- [Reconciliation Flow](#reconciliation-flow)
- [Kilo Background](#kilo-background)
- [Anchor Peer](#anchor-peer)
- [Manager Cache Scoping](#manager-cache-scoping)
- [CRD Bootstrap](#crd-bootstrap)
- [Restart Watcher](#restart-watcher)

---

## Overview

The Kilo ClusterMesh Operator watches `ClusterMesh` custom resources in its own namespace and continuously reconciles WireGuard mesh connectivity across a fleet of Kubernetes clusters. For every node in every participating cluster it creates or updates a cluster-scoped `kilo.squat.ai/v1alpha1 Peer` object on each **remote** cluster, telling Kilo exactly which WireGuard public key, endpoint, and allowed IP ranges belong to that node. When a `ClusterMesh` resource is deleted the operator cleans up all managed `Peer` objects via a finalizer before releasing the resource.

---

## Components

### `controller` — reconciler entry point

`internal/controller/clustermesh_controller.go`

Houses `ClusterMeshReconciler`, the single controller registered with the controller-runtime manager. It reacts to `ClusterMesh` create and update events (delete events are filtered out and handled separately via a finalizer), drives the full reconciliation pipeline, and writes status conditions back to the `ClusterMesh` resource.

### `multicluster` — client cache and cluster registry

`internal/multicluster/registry.go`, `internal/multicluster/client.go`

`ClusterRegistry` holds one controller-runtime `cluster.Cluster` per participating cluster. The local cluster uses a copy of the in-cluster REST config; remote clusters build their REST configs from kubeconfig `Secret` objects referenced in the `ClusterMesh` spec. `ClusterRegistry.Client(name)` provides a ready-to-use `client.Client` for reconciling Peer objects on any cluster in the mesh.

### `validation` — node and mesh-level validation

`internal/validation/node.go`, `internal/validation/mesh.go`

Two validation layers run before any Peer objects are written:

- **`ValidateNode`** checks that a node has the required Kilo annotations (`kilo.squat.ai/wireguard-ip`, `kilo.squat.ai/key`) and that its first pod CIDR falls within the cluster's declared `podCIDRs`. Nodes that fail return a `NodeSkipReason` — see [./troubleshooting.md](./troubleshooting.md) for the full list.
- **`ValidateMeshNetworks`** performs pairwise CIDR overlap checks across **all** `ClusterMesh` objects in the operator namespace, blocking reconciliation if any two clusters share address space.

### `peer/builder` — synthesises Kilo Peer CRs

`internal/peer/builder.go`

`BuildPeer` converts a validated node into a `kilo.squat.ai/v1alpha1 Peer` spec: it resolves the node endpoint (see [Endpoint resolution chain](#reconciliation-flow)), normalises the `kilo.squat.ai/wireguard-ip` annotation to a `/32` (or `/128`) host route for `AllowedIPs`, and appends the node's first pod CIDR.

`BuildAnchorPeer` creates one additional Peer per source cluster that carries the cluster-wide `serviceCIDR` and `additionalCIDRs` — see [Anchor Peer](#anchor-peer).

### `peer/reconciler` — applies and maintains Peer CRs in remote clusters

`internal/peer/reconciler.go`

`ReconcilePeers` takes a desired list of `Peer` objects and the `client.Client` for a target cluster, then performs a three-way reconcile: create missing Peers, update changed ones, and delete any Peers whose labels (`kilo-clustermesh.io/mesh`, `kilo-clustermesh.io/source-cluster`) match the source cluster but are no longer in the desired list. Passing `nil` as the desired list deletes all managed Peers — this is how the finalizer cleans up on `ClusterMesh` deletion.

### `kilonode` — annotation constants and endpoint resolution

`internal/kilonode/annotations.go`, `internal/kilonode/endpoint.go`

Defines the annotation keys used to read Kilo metadata from nodes:

- `kilo.squat.ai/wireguard-ip` — WireGuard overlay IP assigned by Kilo
- `kilo.squat.ai/key` — WireGuard public key managed by Kilo
- `kilo.squat.ai/clustermesh-endpoint` — operator-specific cross-cluster endpoint override
- `kilo.squat.ai/force-endpoint` — Kilo's own endpoint override (also consumed by the operator)

`ResolveEndpoint` implements the three-tier endpoint resolution chain described in [./per-node-setup.md](./per-node-setup.md).

### `crd` — embedded CRD bootstrap at startup

`internal/crd/install.go`, `internal/crd/embed.go`

`InstallOrUpdate` reads the `ClusterMesh` CRD YAML embedded in the binary via `//go:embed`, applies it to the local cluster as a create-or-update, and polls until the CRD reaches `Established=True` (timeout: 30 seconds). The Helm chart does **not** ship CRDs in a `crds/` directory — see [CRD Bootstrap](#crd-bootstrap).

### `restart` — restart-on-config-change watcher

`internal/restart/watcher.go`

`ChangeWatcher` monitors `ClusterMesh` objects and their referenced kubeconfig `Secret` objects. When the cluster configuration fingerprint changes it cancels the manager context, causing the pod to exit and Kubernetes to restart it with a freshly built `ClusterRegistry` — see [Restart Watcher](#restart-watcher).

### `netutil` — CIDR helpers

`internal/netutil/cidr.go`

Utility functions for CIDR parsing and manipulation. The key function is `ParseHostInCIDR`, which accepts both `/32`-style host annotations (upstream Kilo) and `<host>/<subnet-mask>` annotations (cozystack-patched Kilo) and extracts the host IP. This is what makes the operator compatible with both Kilo variants.

---

## Reconciliation Flow

The following describes what happens end-to-end when a `ClusterMesh` resource is created or updated.

```text
ClusterMesh create/update
         │
         ▼
ClusterMeshReconciler.Reconcile()
         │
         ├─1─ validateMeshNetworks
         │       List ALL ClusterMesh objects in namespace
         │       Check pairwise CIDR overlaps → error blocks all affected meshes
         │
         ├─2─ reconcileAllClusters  [for each source cluster]
         │       a. Get srcClient from ClusterRegistry
         │       b. List Nodes on source cluster
         │       c. For each node:
         │            ValidateNode (annotations, podCIDR match)
         │            FindDuplicateWGIPs (dedup, first wins)
         │            ResolveEndpoint:
         │              1. clustermesh-endpoint annotation
         │              2. force-endpoint annotation
         │              3. Node.Status.Addresses ExternalIP + wireguardPort
         │            BuildPeer → Peer{PublicKey, AllowedIPs[wg-ip/32, podCIDR], Endpoint}
         │       d. BuildAnchorPeer (nodes[0]) → Peer{AllowedIPs[serviceCIDR, additionalCIDRs]}
         │       e. For each target cluster (≠ source):
         │            ReconcilePeers(targetClient, desired)
         │              create missing / update changed / delete orphans
         │
         └─3─ updateStatus
                 Set Ready=True + per-cluster peer counts
```

**Peer naming** follows the pattern `<mesh>--<sourceCluster>--<nodeName>`, sanitised to DNS-1123 label rules. Names exceeding 253 characters are truncated and suffixed with a SHA-256 hash to remain unique. Peers carry labels `kilo-clustermesh.io/mesh` and `kilo-clustermesh.io/source-cluster`; `ReconcilePeers` uses these labels to identify orphans for deletion.

**No Node watch.** The reconciler only watches `ClusterMesh` resources. Changes to node annotations (`kilo.squat.ai/wireguard-ip`, `kilo.squat.ai/key`, `kilo.squat.ai/clustermesh-endpoint`, `kilo.squat.ai/force-endpoint`) are **not** detected automatically. After any node annotation change, write a no-op to the `ClusterMesh` resource to trigger a new reconcile cycle.

> **Note:** Endpoint resolution is strict at each tier: a present but unparseable annotation value is a hard error that skips the node. The resolution does **not** fall through to the next source. Full details and skip reasons are in [./troubleshooting.md](./troubleshooting.md); annotation setup is in [./per-node-setup.md](./per-node-setup.md).

---

## Kilo Background

[Kilo](https://github.com/squat/kilo) is a multi-cluster network fabric that uses WireGuard to build overlay tunnels between Kubernetes nodes. It assigns each node a WireGuard IP via the `kilo.squat.ai/wireguard-ip` annotation and a WireGuard public key via `kilo.squat.ai/key`. Cross-cluster connectivity is expressed as `kilo.squat.ai/v1alpha1 Peer` objects — cluster-scoped resources describing a remote WireGuard peer (public key, endpoint, allowed CIDRs).

**Fork awareness.** This operator is designed for the [cozystack-patched Kilo fork](https://github.com/aenix-io/kilo), which uses `cross` granularity: every node receives its own WireGuard IP (not only the location leader). The upstream `kilo.squat.ai/wireguard-ip` annotation carries a `/32` host address; the cozystack fork writes `<host>/<subnet-mask>` (e.g. `100.66.0.3/16`). The operator handles both forms transparently via `netutil.ParseHostInCIDR`, always normalising `AllowedIPs` to a `/32` host route. See [../README.md](../README.md) for a full compatibility note.

---

## Anchor Peer

Cross-cluster traffic needs to reach not only pod-to-pod destinations but also cluster Services (`serviceCIDR`) and any other subnets listed in `additionalCIDRs`. Regular per-node Peers only advertise the node's WireGuard IP and its pod CIDR; they carry no information about cluster-wide CIDRs.

The **anchor peer** fills this gap. `BuildAnchorPeer` creates a single extra `Peer` per source cluster with:

- `AllowedIPs`: `serviceCIDR` + all entries in `additionalCIDRs`
- `PublicKey` / `Endpoint`: taken from the first valid node in the node list (`nodes[0]`)
- Name: `<mesh>--<sourceCluster>--anchor`

The anchor node is `nodes[0]` — the first node that passes validation. It is used only as a WireGuard public-key and endpoint carrier; traffic routed via the anchor peer's allowed CIDRs is handled by the cluster's internal routing once it enters through that node.

**Nil-return cases.** `BuildAnchorPeer` returns `nil` (and the anchor peer is silently omitted for that reconcile cycle) when `resolvePeerEndpoint` returns ANY error for the anchor node — this covers both situations below, and a malformed endpoint annotation on `nodes[0]` will also silently suppress the anchor:

1. The source cluster's `ClusterEntry` has no `serviceCIDR` and no `additionalCIDRs`.
2. The anchor node (`nodes[0]`) has no resolvable endpoint.

> **Warning:** If the anchor peer is omitted because `nodes[0]` has no endpoint, `serviceCIDR` and `additionalCIDRs` will be unreachable from other clusters for the duration of that reconcile cycle. No error event is emitted. Check `nodes[0]`'s annotations and ensure `kilo.squat.ai/clustermesh-endpoint` or `kilo.squat.ai/force-endpoint` is set if the node has no `ExternalIP`. See [./per-node-setup.md](./per-node-setup.md) for annotation setup.

For full `ClusterEntry` field reference see [./configuration.md](./configuration.md).

---

## Manager Cache Scoping

The controller-runtime `Manager` is configured with namespace-scoped caches. The operator reads `POD_NAMESPACE` from the downward API and restricts the informer cache for `ClusterMesh` objects and kubeconfig `Secret` objects to **that single namespace only**.

```go
// cmd/main.go — cache.Options.DefaultNamespaces
cache.Options{
    DefaultNamespaces: map[string]cache.Config{
        namespace: {},
    },
}
```

Cluster-scoped resources — `Node` objects, `Peer` objects, `CustomResourceDefinition` objects, and leader-election `Lease` objects — are **not** handled through the manager cache. They are accessed directly via the `ClusterRegistry`'s per-cluster clients or via raw REST calls.

This design means the operator is completely isolated to its own namespace for namespace-scoped resources and cannot accidentally observe or modify objects in other namespaces.

> **Note:** Multiple `ClusterMesh` objects in the same namespace are supported, but cluster entries are deduplicated by name at startup: if two `ClusterMesh` objects reference a cluster with the same name, the first entry encountered wins and the second is silently dropped. See [./troubleshooting.md](./troubleshooting.md) if a cluster appears to use unexpected connection details.

For deployment specifics (namespace, RBAC, `POD_NAMESPACE` setup) see [./installation.md](./installation.md).

---

## CRD Bootstrap

The `ClusterMesh` CRD YAML is embedded directly in the operator binary at compile time (`//go:embed`). On every startup, before the manager is initialised, `crd.InstallOrUpdate` applies this CRD to the local cluster and polls until the API server reports `Established=True` (maximum wait: 30 seconds).

Consequences of this design:

- **No chart-side CRDs.** The Helm chart has no `crds/` directory. Installing or upgrading the chart without running the operator will not create or update the CRD.
- **Automatic CRD upgrades.** Upgrading the operator binary (via a new Helm chart release or image tag) automatically upgrades the CRD schema on the next pod start.
- **Startup order matters.** If the CRD cannot reach `Established=True` within 30 seconds the operator exits. This can happen on a slow or overloaded API server.

See [./installation.md](./installation.md) for Helm-based deployment.

---

## Restart Watcher

`ChangeWatcher` runs alongside the manager and watches for changes to the cluster configuration that cannot be handled by a normal reconcile loop. Specifically, it detects:

- A new `ClusterMesh` object being created in the namespace
- A cluster entry being renamed or removed from an existing `ClusterMesh`
- The `ResourceVersion` of a referenced kubeconfig `Secret` changing (i.e. the kubeconfig content itself changed)

The fingerprint is a SHA-256 hash of the sorted JSON representation of `[{name, secretName, secretResourceVersion}]` for every cluster across all `ClusterMesh` objects. When the live fingerprint diverges from the fingerprint captured at startup, `ChangeWatcher` calls `Cancel()`, which stops the manager context. Kubernetes then restarts the pod, and the new process calls `buildInitialRegistry` to rebuild `ClusterRegistry` from scratch with the updated configuration.

> **Note:** This is intentional design — a pod restart on cluster configuration change, not a crash. The restart is fast (registry build is synchronous at startup) and ensures the informer caches for all remote clusters are correctly initialised. Peer-level changes (node annotation updates, CIDR changes within an existing cluster) do **not** trigger a restart; they are handled by the normal reconcile loop.

See [./troubleshooting.md](./troubleshooting.md) if the operator is restarting unexpectedly.
