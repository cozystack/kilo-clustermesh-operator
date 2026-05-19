# kilo-clustermesh-operator

Kubernetes ClusterMesh operator for [Kilo](https://github.com/squat/kilo) — connects two or more clusters into a WireGuard-based mesh network.

## Overview

The operator watches `ClusterMesh` resources and reconciles Kilo `Peer` objects so that every node in each remote cluster becomes a peer in the local cluster's WireGuard mesh. This enables cross-cluster pod-to-pod and service connectivity without a shared control plane.

Each `ClusterMesh` resource declares two or more participating clusters, including which one is local. The operator connects to each remote cluster using a kubeconfig stored in a Kubernetes Secret, lists the remote nodes, validates their CIDRs against the declared spec, and creates or updates Kilo `Peer` objects on the local cluster accordingly.

## Prerequisites

- Kubernetes 1.28+ in every participating cluster
- [Kilo](https://github.com/squat/kilo) installed in each cluster. Both upstream and the cozystack-patched build are supported; the operator accepts WireGuard IP annotations in either `<host>/32` (upstream) or `<host>/<subnet-mask>` (cozystack `cross` granularity) form.
- Each remote cluster's apiserver must be reachable from the cluster where the controller runs.
- Each node that participates in the mesh must expose its WireGuard UDP port (default `51820`, configurable per-cluster via `wireguardPort`) on a network address reachable from every other cluster (see [Per-node configuration](#per-node-configuration) below).
- Helm 3.x (for chart-based installation)

## Quick Start

Install the operator with Helm from a cloned copy of this repository:

```bash
git clone https://github.com/cozystack/kilo-clustermesh-operator.git
cd kilo-clustermesh-operator
helm install kilo-clustermesh-operator charts/kilo-clustermesh-operator \
  --namespace kilo-system \
  --create-namespace
```

Container images are published to `ghcr.io/cozystack/kilo-clustermesh-operator` and tagged with the commit SHA (`sha-<full-commit>`). Override `image.tag` in the values file to pin a specific build:

```yaml
image:
  tag: sha-<commit>
```

Create a `ClusterMesh` resource:

```yaml
apiVersion: kilo.squat.ai/v1alpha1
kind: ClusterMesh
metadata:
  name: my-mesh
  namespace: kilo-system
spec:
  clusters:
    - name: cluster-a
      local: true
      podCIDRs: ["10.1.0.0/16"]
      wireguardCIDR: "10.200.0.0/24"
      serviceCIDR: "10.96.0.0/12"
    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs: ["10.2.0.0/16"]
      wireguardCIDR: "10.200.1.0/24"
      serviceCIDR: "10.112.0.0/12"
```

## ClusterMesh CRD Reference

**Group**: `kilo.squat.ai` | **Version**: `v1alpha1` | **Kind**: `ClusterMesh`

Short name: `cm` | Scope: Namespaced

### Spec

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `clusters` | `[]ClusterEntry` | Yes | List of clusters in this mesh. Minimum 2 entries. |

### ClusterEntry

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | `string` | Yes | Unique identifier for this cluster within the mesh. Must be a valid DNS-1123 label (max 63 chars). |
| `local` | `bool` | No | Marks this as the cluster where the controller runs. Exactly one entry must be local. |
| `kubeconfigSecretRef` | `SecretKeyRef` | No | Reference to a Secret containing the kubeconfig for this cluster. Required for non-local clusters. |
| `podCIDRs` | `[]string` | Yes | Pod network CIDRs for this cluster. `Node.Spec.PodCIDRs` must be subsets of these. Supports dual-stack. Minimum 1 entry. |
| `wireguardCIDR` | `string` | Yes | CIDR for Kilo's WireGuard interface (`kilo0`). Each node's `kilo.squat.ai/wireguard-ip` must fall within this CIDR. |
| `serviceCIDR` | `string` | No | Kubernetes service network CIDR. If set, advertised via an anchor Peer so services are reachable across clusters. |
| `additionalCIDRs` | `[]string` | No | Extra CIDRs to advertise into the mesh (e.g., host-network ranges, external subnets). |

### SecretKeyRef

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | `string` | Yes | Name of the Kubernetes Secret. |
| `key` | `string` | Yes | Key within the Secret's `data` map. |

### Status

| Field | Type | Description |
| --- | --- | --- |
| `clusters` | `[]ClusterStatus` | Per-cluster reconciliation state. |
| `conditions` | `[]metav1.Condition` | Standard Kubernetes conditions. The `Ready` condition reflects overall mesh health. |

### ClusterStatus

| Field | Type | Description |
| --- | --- | --- |
| `name` | `string` | Matches `ClusterEntry.name`. |
| `registeredPeers` | `int` | Number of Kilo `Peer` objects created for this cluster's nodes. |
| `skippedNodes` | `int` | Number of nodes that failed CIDR validation and were not peered. |

## Remote Cluster Setup

The operator needs read access to nodes and write access to `peers` on each remote cluster.

Apply this manifest on every remote cluster. It creates a `ServiceAccount`, a `ClusterRole`, a `ClusterRoleBinding`, and a long-lived `Secret`-backed token (Kubernetes 1.24+ no longer auto-mints token Secrets for ServiceAccounts):

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: clustermesh-reader
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kilo-clustermesh-remote
rules:
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list, watch]
  - apiGroups: [kilo.squat.ai]
    resources: [peers]
    verbs: [get, list, watch, create, update, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: clustermesh-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kilo-clustermesh-remote
subjects:
  - kind: ServiceAccount
    name: clustermesh-reader
    namespace: kube-system
---
apiVersion: v1
kind: Secret
metadata:
  name: clustermesh-reader-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: clustermesh-reader
type: kubernetes.io/service-account-token
```

Build a kubeconfig from the ServiceAccount token and store it as a Secret on the cluster where the operator runs:

```bash
# Pull token, CA, and apiserver URL from the remote cluster
TOKEN=$(kubectl --kubeconfig "$REMOTE" --namespace kube-system \
  get secret clustermesh-reader-token --output jsonpath='{.data.token}' | base64 --decode)
CA=$(kubectl --kubeconfig "$REMOTE" --namespace kube-system \
  get secret clustermesh-reader-token --output jsonpath='{.data.ca\.crt}')
SERVER=$(kubectl --kubeconfig "$REMOTE" config view --minify \
  --output jsonpath='{.clusters[0].cluster.server}')

# Render kubeconfig to a temp file
TMP=$(mktemp); chmod 600 "$TMP"
cat > "$TMP" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: remote
  cluster:
    server: ${SERVER}
    certificate-authority-data: ${CA}
users:
- name: clustermesh-reader
  user:
    token: ${TOKEN}
contexts:
- name: remote
  context:
    cluster: remote
    user: clustermesh-reader
current-context: remote
EOF

# Sanity check, then store on the local cluster
kubectl --kubeconfig "$TMP" get nodes
kubectl --kubeconfig "$LOCAL" --namespace kilo-system create secret generic cluster-b-kubeconfig \
  --from-file=kubeconfig="$TMP"
rm "$TMP"
```

Reference the Secret in the `ClusterMesh` spec via `kubeconfigSecretRef`.

## Per-node configuration

For every node that participates in the mesh, the operator resolves a WireGuard endpoint from the following sources, in priority order. The first non-empty source wins:

1. **`kilo.squat.ai/clustermesh-endpoint` annotation** (operator-specific, preferred). Decoupled from Kilo's own behaviour, so setting it does not affect intra-cluster routing.
2. **`kilo.squat.ai/force-endpoint` annotation** (legacy fallback). Also consumed by Kilo itself, so it overrides intra-cluster peer endpoints as well — usually acceptable, but it can introduce a NAT hop for same-cluster traffic.
3. **First `ExternalIP` from `Node.Status.Addresses`** (zero-config fallback), combined with the `wireguardPort` declared in the `ClusterEntry` (default `51820`). IPv4 is preferred over IPv6. This path is useful on platforms that publish public IPs into the Node status automatically (e.g. cloud-managed clusters, or Talos with the Oracle Cloud platform driver providing 1:1 NAT addresses).

A node with **no resolvable source** is skipped with reason `NodeNoEndpoint` and counted in `SkippedNodes`. A node whose `clustermesh-endpoint` or `force-endpoint` annotation is **present but malformed** is skipped with reason `NodeEndpointInvalid` — bad values surface immediately instead of producing a Peer without an endpoint.

To set the preferred operator-specific annotation explicitly:

```bash
kubectl annotate node <node-name> \
  kilo.squat.ai/clustermesh-endpoint=<public-ip-or-dns>:<port> \
  --overwrite
```

To override the default WireGuard port for an entire cluster (when relying on the ExternalIP fallback), set `wireguardPort` on the corresponding `ClusterEntry` in the `ClusterMesh` spec.

### Triggering a reconcile after annotation changes

The operator's manager cache watches `ClusterMesh` and `Secret` objects only; it does **not** watch `Node` annotations on either the local or remote clusters. After changing a node's endpoint annotation (or any other Kilo annotation that affects peer construction), nudge the operator with a no-op write to the `ClusterMesh` resource:

```bash
kubectl --namespace kilo-system annotate clustermesh <name> \
  reconcile-trigger="$(date +%s)" \
  --overwrite
```

This is a workaround until the operator watches Node changes directly — see [Possible improvements](#possible-improvements).

## Architecture

The controller runs a single reconciliation loop triggered by changes to `ClusterMesh` resources and to the `Secret` objects they reference for remote-cluster kubeconfigs. Node-level changes (annotations, CIDRs, additions, removals) are **not** observed automatically; a manual reconcile trigger is currently required after such changes (see [Triggering a reconcile after annotation changes](#triggering-a-reconcile-after-annotation-changes)).

**Reconciliation flow:**

1. For each cluster in the spec, build a client. The local cluster uses the in-cluster config; remote clusters use the kubeconfig stored in the referenced Secret.
2. For every cluster, list all `Node` objects and validate each node's `Spec.PodCIDRs` and `kilo.squat.ai/wireguard-ip` annotation against the declared `podCIDRs` and `wireguardCIDR`.
3. For each pair (sourceCluster, targetCluster), construct per-node Kilo `Peer` objects on the targetCluster representing each source node's pod CIDR and WireGuard host IP. If `serviceCIDR` or `additionalCIDRs` are set, an additional anchor `Peer` is created carrying those cluster-wide CIDRs.
4. The Peer endpoint is resolved via the [per-node endpoint chain](#per-node-configuration): `kilo.squat.ai/clustermesh-endpoint` → `kilo.squat.ai/force-endpoint` → first ExternalIP in `Node.Status.Addresses`. Nodes without any resolvable source are skipped.
5. Delete stale `Peer` objects on each cluster that no longer correspond to any remote node.
6. Update `ClusterMeshStatus` with per-cluster peer counts and set the `Ready` condition.

Nodes that fail CIDR or WireGuard-IP validation are counted as `skippedNodes` and a Kubernetes event is emitted. The operator uses a finalizer (`kilo-clustermesh.io/cleanup`) to clean up `Peer` objects on every cluster when a `ClusterMesh` resource is deleted.

A change-watcher controller monitors `ClusterMesh` and referenced `Secret` objects and triggers a pod restart when the cluster set or any kubeconfig Secret's `resourceVersion` changes — this rebuilds the remote-cluster client registry safely.

The operator's manager cache is scoped to the operator's own namespace; cluster-wide RBAC is only required for cluster-scoped resources accessed via the multicluster registry (Nodes, Peers, CRDs, Leases).

## Possible improvements

The following items have been deliberately deferred. Issues and PRs welcome.

- **Watch `Node` annotation changes.** The reconciler currently only observes `ClusterMesh` and `Secret` objects. Changes to relevant node annotations (`clustermesh-endpoint`, `force-endpoint`, `wireguard-ip`, `public-key`) require a manual reconcile trigger. Watching Node objects on every cluster — with predicates filtering for the relevant annotation keys — would make endpoint changes propagate automatically.
- **Per-node skip annotation.** Today a node without a reachable endpoint is registered as a Peer without an endpoint, which results in silently dropped traffic. An explicit opt-out annotation (e.g. `kilo-clustermesh.io/skip=true`) and a corresponding `Skipped` reason in status would make intent visible.

## Contributing

### Run tests

```bash
# Unit tests
go test ./api/... ./pkg/... ./internal/... -race

# Integration tests (requires setup-envtest)
export KUBEBUILDER_ASSETS=$(setup-envtest use -p path)
go test ./test/integration/... -race -timeout 120s
```

### Lint

```bash
golangci-lint run
```

### Build

```bash
go build -o bin/manager ./cmd/main.go
```

### Regenerate CRDs and DeepCopy

```bash
make manifests generate
```

### Helm chart tests

```bash
helm lint charts/kilo-clustermesh-operator --strict
helm unittest charts/kilo-clustermesh-operator
```

## License

Copyright 2026 The Kilo Authors.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
