# kilo-clustermesh-operator

> Kubernetes operator that connects two or more clusters into a WireGuard-based mesh network using [Kilo](https://github.com/squat/kilo).

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Requirements](#requirements)
- [Quick Start](#quick-start)
- [How It Works](#how-it-works)
- [Documentation](#documentation)
- [Project Status](#project-status)
- [License](#license)

---

## Overview

`kilo-clustermesh-operator` extends Kilo's single-cluster WireGuard mesh to span multiple Kubernetes clusters. You declare a `ClusterMesh` resource that lists all participating clusters, and the operator reconciles Kilo `Peer` objects so that every node in each remote cluster becomes a peer on the local cluster — enabling direct pod-to-pod and service connectivity across clusters without a shared control plane.

The operator runs on a single cluster and reaches remote clusters via kubeconfigs stored in Kubernetes Secrets. No second operator instance is required on remote clusters.

## Features

- **Multi-cluster WireGuard mesh** — declarative `ClusterMesh` CRD bridges any number of clusters
- **Fork-aware Kilo support** — accepts WireGuard IP annotations in both upstream (`<host>/32`) and Cozystack-patched (`<host>/<subnet-mask>`) form; normalises to host routes automatically
- **Endpoint resolution chain** — per-node endpoint determined by priority: `clustermesh-endpoint` annotation → `force-endpoint` annotation → Node `ExternalIP` combined with `wireguardPort`; nodes with no resolvable endpoint are skipped cleanly
- **Anchor peers** — a single per-cluster anchor `Peer` advertises `serviceCIDR` and `additionalCIDRs` so service and host-network ranges are reachable across clusters
- **Embedded CRD bootstrap** — the operator self-applies its CRD at startup; no separate CRD pre-install step required
- **Safe cluster reconfiguration** — a change-watcher triggers a controlled pod restart when cluster topology or kubeconfig Secrets change, rebuilding the client registry from scratch
- **Finalizer-based cleanup** — removing a `ClusterMesh` CR triggers deletion of all managed `Peer` objects on every cluster before the resource is released

## Requirements

- Kubernetes 1.28+ on every participating cluster
- [Kilo](https://github.com/squat/kilo) installed and running on every cluster (both upstream and the Cozystack-patched build are supported)
- Each node that participates in the mesh must expose its WireGuard UDP port on a network address reachable from every other cluster — by default port `51820`, configurable per cluster via `wireguardPort`
- Each remote cluster's API server must be reachable from the cluster where the operator runs
- A kubeconfig Secret for each non-local cluster, granting the operator read access to `nodes` and read/write access to `peers` on that cluster
- Helm 3.x for chart-based installation

## Quick Start

### 1. Install the operator

Clone the repository and install with Helm:

```bash
git clone https://github.com/cozystack/kilo-clustermesh-operator.git
cd kilo-clustermesh-operator
helm install kilo-clustermesh-operator charts/kilo-clustermesh-operator \
  --namespace kilo-system \
  --create-namespace
```

Container images are published to `ghcr.io/cozystack/kilo-clustermesh-operator` and tagged `sha-<full-commit>` (e.g. `sha-43caba9978f26383593bedec79930c62e7ecead7`). Pin a specific build by overriding `image.tag` in your values file:

```yaml
image:
  tag: sha-<full-commit>
```

### 2. Prepare remote-cluster credentials

On every remote cluster, create a `ServiceAccount`, `ClusterRole`, `ClusterRoleBinding`, and a long-lived token `Secret`:

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

Build a kubeconfig from the token and store it as a Secret on the cluster where the operator runs:

```bash
TOKEN=$(kubectl --kubeconfig "$REMOTE" --namespace kube-system \
  get secret clustermesh-reader-token --output jsonpath='{.data.token}' | base64 --decode)
CA=$(kubectl --kubeconfig "$REMOTE" --namespace kube-system \
  get secret clustermesh-reader-token --output jsonpath='{.data.ca\.crt}')
SERVER=$(kubectl --kubeconfig "$REMOTE" config view --minify \
  --output jsonpath='{.clusters[0].cluster.server}')

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

kubectl --kubeconfig "$TMP" get nodes
kubectl --kubeconfig "$LOCAL" --namespace kilo-system \
  create secret generic cluster-b-kubeconfig --from-file=kubeconfig="$TMP"
rm "$TMP"
```

### 3. Create a ClusterMesh resource

The two clusters must use **non-overlapping** pod CIDRs and WireGuard CIDRs. The example below uses distinct ranges for `cluster-a` and `cluster-b`:

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
      wireguardPort: 51820        # default; set explicitly if your cluster uses a different port
      serviceCIDR: "10.96.0.0/12"
    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs: ["10.2.0.0/16"]
      wireguardCIDR: "10.200.1.0/24"
      wireguardPort: 51820
      serviceCIDR: "10.112.0.0/12"
```

> **Warning:** Pod CIDRs, WireGuard CIDRs, and service CIDRs must not overlap between any two clusters in the same namespace. Overlapping CIDRs block reconciliation for all affected meshes.

> **Note:** The CRD is automatically installed by the operator at startup — you do not need to apply it separately.

## How It Works

On each reconcile cycle, the operator connects to every cluster in the `ClusterMesh` spec, lists all `Node` objects, validates each node's pod CIDR and WireGuard IP against the declared spec, and creates or updates Kilo `Peer` objects accordingly. Nodes that fail validation or have no resolvable endpoint are skipped. For each cluster that declares a `serviceCIDR` or `additionalCIDRs`, an anchor `Peer` carrying those CIDRs is also created on every other cluster. The operator uses a finalizer to clean up all managed peers when a `ClusterMesh` resource is deleted.

See [./docs/architecture.md](./docs/architecture.md) for the full reconciliation flow and component details.

> **Note:** The operator watches `ClusterMesh` and `Secret` objects only — it does **not** watch `Node` objects. After changing a node annotation (endpoint, WireGuard IP, public key), trigger a reconcile manually:
>
> ```bash
> kubectl --namespace kilo-system annotate clustermesh <name> \
>   reconcile-trigger="$(date +%s)" --overwrite
> ```

## Documentation

| Page | Description |
| --- | --- |
| [Architecture](./docs/architecture.md) | Reconciliation flow, component internals, CRD bootstrap, change-watcher |
| [Installation](./docs/installation.md) | Helm chart values, RBAC setup, image pinning, uninstall procedure |
| [Configuration](./docs/configuration.md) | Full `ClusterMesh` CRD reference, field constraints, status conditions |
| [Per-node setup](./docs/per-node-setup.md) | Endpoint resolution chain, node annotations, WireGuard IP requirements |
| [Troubleshooting](./docs/troubleshooting.md) | Common failure modes, skip reasons, CIDR overlap, stale peers |
| [Known Gaps](./docs/known-gaps.md) | Outstanding work and proposal divergences (for contributors) |

## Project Status

Alpha — the API is functional and in active use within Cozystack, but the CRD version is `v1alpha1` and breaking changes may occur before a stable release. See [docs/known-gaps.md](./docs/known-gaps.md) for outstanding work and divergences from the upstream proposal.

## License

Copyright 2026 The Kilo Authors. Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
