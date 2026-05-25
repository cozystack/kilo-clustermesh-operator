# Configuration

> Complete reference for the `ClusterMesh` CRD.

## Table of Contents

- [Overview](#overview)
- [ClusterMesh resource](#clustermesh-resource)
  - [Group / Version / Kind](#group--version--kind)
  - [Spec fields](#spec-fields)
  - [Status fields](#status-fields)
- [ClusterEntry fields](#clusterentry-fields)
- [SecretKeyRef fields](#secretkeyref-fields)
- [Status conditions](#status-conditions)
- [CIDR validation rules](#cidr-validation-rules)
- [Examples](#examples)

---

## Overview

`ClusterMesh` is the only custom resource defined by this operator. It declares a set of clusters to connect into a WireGuard mesh and drives the operator's reconciliation loop. Everything configuration-related lives in `spec.clusters`; the operator writes observed state back into `status`. Node-level annotations are outside this CRD — see [per-node-setup.md](./per-node-setup.md).

---

## ClusterMesh resource

### Group / Version / Kind

| Field | Value |
|-------|-------|
| API group | `kilo.squat.ai` |
| Version | `v1alpha1` |
| Kind | `ClusterMesh` |
| Plural / short name | `clustermeshes` / `cm` |
| Scope | `Namespaced` |
| Finalizer | `kilo-clustermesh.io/cleanup` |

**kubectl** example:

```bash
kubectl get clustermeshes --namespace kilo-system
kubectl get cm --namespace kilo-system
```

### Spec fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.clusters` | `[]ClusterEntry` | Yes | — | List of clusters in the mesh. Must contain at least 2 entries (`+kubebuilder:validation:MinItems=2`). |

### Status fields

| Field | Type | Description |
|-------|------|-------------|
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see [Status conditions](#status-conditions)). `listType=map`, `listMapKey=type`. |
| `status.clusters` | `[]ClusterStatus` | Per-cluster observed state. |
| `status.clusters[].name` | `string` | Matches `ClusterEntry.name`. |
| `status.clusters[].registeredPeers` | `int` | Number of `Peer` objects built for this cluster's valid nodes (per-node peers + optional anchor peer). Set before peers are applied to target clusters. |
| `status.clusters[].skippedNodes` | `int` | Number of nodes that failed validation and were not peered. See [troubleshooting.md](./troubleshooting.md) for skip reasons. |

---

## ClusterEntry fields

Each element of `spec.clusters` is a `ClusterEntry`.

| Field | Type | Required | Default | Validation | Description |
|-------|------|----------|---------|------------|-------------|
| `name` | `string` | Yes | — | `pattern: ^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`, `maxLength: 63` | Unique cluster identifier within this mesh. DNS-1123 label format. Used as label value and status key. |
| `local` | `bool` | No | `false` | — | Marks the cluster where the operator runs. Exactly one cluster must be `local: true`. No `kubeconfigSecretRef` is needed for the local cluster. |
| `kubeconfigSecretRef` | `*SecretKeyRef` | No¹ | — | — | Reference to a Secret holding the kubeconfig for this cluster. Required for non-local clusters; ignored for the local cluster. Secret must be in the same namespace as the `ClusterMesh` resource. See [installation.md](./installation.md) for Secret setup. |
| `podCIDRs` | `[]string` | Yes | — | `minItems: 1` | Pod network CIDR(s). `Node.Spec.PodCIDRs[0]` on each node must fall within one of these CIDRs. Multiple entries support dual-stack (IPv4 + IPv6). Only `PodCIDRs[0]` is validated per node; IPv6 pod CIDRs are not placed in `AllowedIPs`. |
| `wireguardCIDR` | `string` | Yes | — | — | CIDR for Kilo's `kilo0` WireGuard interface addresses. Each node's `kilo.squat.ai/wireguard-ip` host IP must fall within this CIDR. The annotation may carry any prefix length (`/32` upstream Kilo or `/<subnet-mask>` on cozystack-patched Kilo); only the host portion is validated. |
| `wireguardPort` | `uint16` | No | `51820` | `minimum: 1`, `maximum: 65535` | UDP port for this cluster's WireGuard endpoints. Used only as a fallback when the operator synthesises an endpoint from `Node.Status.Addresses` (i.e. neither `kilo.squat.ai/clustermesh-endpoint` nor `kilo.squat.ai/force-endpoint` is set on the node). See [per-node-setup.md](./per-node-setup.md). `+kubebuilder:default=51820` |
| `serviceCIDR` | `string` | No | `""` | — | Kubernetes service network CIDR. When set, included in the anchor `Peer`'s `AllowedIPs` so services in this cluster are reachable from other mesh members. When empty, excluded from `AllCIDRs` and from CIDR overlap checks. See [architecture.md](./architecture.md) for anchor peer details. |
| `additionalCIDRs` | `[]string` | No | `[]` | — | Extra CIDRs to advertise via the anchor `Peer` (host-network ranges, external subnets, etc.). All entries are included in `AllCIDRs` for overlap validation. |

¹ `kubeconfigSecretRef` is logically required for every non-local cluster entry. The CRD marks it `+optional` at the schema level to permit the local cluster to omit it; the controller treats its absence on a non-local entry as a configuration error.

---

## SecretKeyRef fields

Embedded inside `kubeconfigSecretRef`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | Yes | Name of the Kubernetes `Secret` object in the operator namespace. |
| `key` | `string` | Yes | Key within `Secret.data` whose value contains the kubeconfig bytes. |

---

## Status conditions

Conditions use the standard `metav1.Condition` type. Two condition types are written by the controller.

### Condition types and reason values

| Type | Status | Reason | Set when |
|------|--------|--------|----------|
| `Ready` | `True` | `Reconciled` | All clusters reconciled successfully in the current pass. |
| `Ready` | `False` | `NetworksOverlap` | A CIDR overlap was detected; reconciliation was blocked. |
| `NetworksOverlap` | `True` | `CIDROverlap` | Overlap detected between CIDRs in this mesh or across meshes in the same namespace. |
| `NetworksOverlap` | `False` | `NoOverlap` | All CIDRs are disjoint; mesh passed CIDR validation. |

`Ready` and `NetworksOverlap` are always updated together: when overlap is detected, `NetworksOverlap=True/CIDROverlap` and `Ready=False/NetworksOverlap` are set atomically. On success, `NetworksOverlap=False/NoOverlap` is set before peer reconciliation, and `Ready=True/Reconciled` is set after.

> CIDR overlap in any `ClusterMesh` in the namespace blocks reconciliation of all meshes in that namespace — `ValidateMeshNetworks` does pairwise cross-mesh checking on every reconcile. See [troubleshooting.md](./troubleshooting.md).

---

## CIDR validation rules

CIDR validation is enforced by `ValidateMeshNetworks` (called on every reconcile) and `ValidateClusterNetworks` (per-mesh subset). The rules are:

1. **All CIDRs within a single `ClusterMesh` must be pairwise disjoint.** The set of CIDRs checked for each cluster entry is built by `AllCIDRs()` in the order: `podCIDRs` → `wireguardCIDR` → `serviceCIDR` (only if non-empty) → `additionalCIDRs`.

2. **CIDRs across all `ClusterMesh` objects in the same namespace must also be pairwise disjoint.** A single overlap between any two clusters in any combination of meshes fails the check for all affected meshes.

3. **`serviceCIDR` is excluded from the check when empty.** An empty `serviceCIDR` is not added to `AllCIDRs`, so it cannot cause an overlap error.

The overlap check uses `net.IPNet.Contains` both ways (`a.Contains(b.IP) || b.Contains(a.IP)`), so a sub-range of another cluster's CIDR is also an error.

---

## Examples

### Minimal

Two clusters, no service CIDRs, default WireGuard port. CIDRs are non-overlapping.

```yaml
apiVersion: kilo.squat.ai/v1alpha1
kind: ClusterMesh
metadata:
  name: prod-mesh
  namespace: kilo-system
spec:
  clusters:
    - name: cluster-a
      local: true
      podCIDRs:
        - 10.244.0.0/16
      wireguardCIDR: 100.64.0.0/16

    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs:
        - 10.245.0.0/16
      wireguardCIDR: 100.65.0.0/16
```

### Full

All fields populated: dual-stack `podCIDRs`, `serviceCIDR`, `additionalCIDRs`, and a non-default `wireguardPort`.

```yaml
apiVersion: kilo.squat.ai/v1alpha1
kind: ClusterMesh
metadata:
  name: prod-mesh
  namespace: kilo-system
spec:
  clusters:
    - name: cluster-a
      local: true
      podCIDRs:
        - 10.244.0.0/16
        - fd00:10:244::/48
      wireguardCIDR: 100.64.0.0/16
      wireguardPort: 51820        # default; listed explicitly for clarity
      serviceCIDR: 10.96.0.0/12
      additionalCIDRs:
        - 192.168.10.0/24

    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs:
        - 10.245.0.0/16
        - fd00:10:245::/48
      wireguardCIDR: 100.65.0.0/16
      wireguardPort: 52000
      serviceCIDR: 172.16.0.0/12
      additionalCIDRs:
        - 192.168.20.0/24
```

> For kubeconfig Secret setup, see [installation.md](./installation.md).
> For node-level annotations (`wireguard-ip`, `clustermesh-endpoint`, `key`), see [per-node-setup.md](./per-node-setup.md).
> For reconciliation flow and anchor peer behaviour, see [architecture.md](./architecture.md).
> Back to [README.md](../README.md).
