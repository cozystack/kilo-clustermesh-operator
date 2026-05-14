# kilo-clustermesh-operator

Kubernetes ClusterMesh operator for [Kilo](https://github.com/squat/kilo) — connects two or more clusters into a WireGuard-based mesh network.

## Overview

The operator watches `ClusterMesh` resources and reconciles Kilo `Peer` objects so that every node in each remote cluster becomes a peer in the local cluster's WireGuard mesh. This enables cross-cluster pod-to-pod and service connectivity without a shared control plane.

Each `ClusterMesh` resource declares two or more participating clusters, including which one is local. The operator connects to each remote cluster using a kubeconfig stored in a Kubernetes Secret, lists the remote nodes, validates their CIDRs against the declared spec, and creates or updates Kilo `Peer` objects on the local cluster accordingly.

## Prerequisites

- Kubernetes 1.28+ in every participating cluster
- [Kilo](https://github.com/squat/kilo) installed in each cluster with `--mesh-granularity=cross`
- Each cluster must be reachable from the controller (API server endpoint)
- Helm 3.x (for chart-based installation)

## Quick Start

Install the operator via Helm:

```bash
helm install kilo-clustermesh-operator \
  oci://ghcr.io/squat/kilo-clustermesh-operator/charts/kilo-clustermesh-operator \
  --namespace kilo-system \
  --create-namespace
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
      wireguardCIDR: "10.100.0.0/24"
      serviceCIDR: "10.96.0.0/12"
    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs: ["10.2.0.0/16"]
      wireguardCIDR: "10.100.1.0/24"
      serviceCIDR: "10.96.0.0/12"
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

Apply the following `ClusterRole` on each remote cluster:

```yaml
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
```

Create a ServiceAccount, bind the role, generate a kubeconfig, then store it as a Secret in the local cluster:

```bash
kubectl --context remote-cluster create serviceaccount clustermesh-reader -n kube-system
kubectl --context remote-cluster create clusterrolebinding clustermesh-reader \
  --clusterrole=kilo-clustermesh-remote \
  --serviceaccount=kube-system:clustermesh-reader

# Generate kubeconfig from the ServiceAccount token
kubectl --context remote-cluster create token clustermesh-reader -n kube-system --duration=8760h \
  | kubectl --context local-cluster create secret generic cluster-b-kubeconfig \
    --from-literal=kubeconfig="$(kubectl config view --minify --flatten)" \
    --namespace kilo-system
```

Reference the Secret in the `ClusterMesh` spec via `kubeconfigSecretRef`.

## Architecture

The controller runs a single reconciliation loop triggered by:

- Changes to `ClusterMesh` resources
- Changes to `Node` objects in the local cluster (via label/annotation watch)

**Reconciliation flow:**

1. For each remote cluster, build a client from the referenced kubeconfig Secret.
2. List all nodes in the remote cluster and validate each node's `PodCIDRs` and WireGuard IP annotation (`kilo.squat.ai/wireguard-ip`) against the declared CIDRs in the spec.
3. For each valid remote node, create or update a Kilo `Peer` object on the local cluster using a deterministic name derived from cluster name and node name.
4. Delete stale `Peer` objects that no longer correspond to an existing node.
5. Update `ClusterMeshStatus` with per-cluster peer counts and set the `Ready` condition.

Nodes that fail CIDR validation are counted as `skippedNodes` and a Kubernetes event is emitted. The operator uses a finalizer (`kilo-clustermesh.io/cleanup`) to clean up `Peer` objects when a `ClusterMesh` resource is deleted.

Remote cluster clients are cached in a registry and reloaded when the referenced Secret changes.

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
