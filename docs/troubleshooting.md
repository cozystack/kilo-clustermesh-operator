# Troubleshooting

> Diagnostic reference for ClusterMesh status conditions and node-skip reasons.

## Table of Contents

- [Overview](#overview)
- [Inspecting State](#inspecting-state)
- [Node Skip Reasons](#node-skip-reasons)
- [Mesh-Level Validation Errors](#mesh-level-validation-errors)
- [Status Conditions](#status-conditions)
- [Common Pitfalls](#common-pitfalls)
- [Re-Examining the Embedded CRD](#re-examining-the-embedded-crd)

---

## Overview

This page lists every symptom surfaced through ClusterMesh status conditions and node-skip reasons, with diagnostic steps for each. Node-level problems appear as `NodeSkipReason` values in Kubernetes events and operator logs. Mesh-level problems appear as status conditions on the ClusterMesh resource itself. Start with [Inspecting State](#inspecting-state) to collect the relevant output, then look up the specific reason or condition type in the tables below.

---

## Inspecting State

**View ClusterMesh status and conditions:**

```bash
kubectl --context <ctx> --namespace <ns> get clustermesh.kilo.squat.ai <name> --output yaml
```

Look at `.status.conditions` (see [Status Conditions](#status-conditions)) and `.status.clusters[*].skippedNodes`.

**Stream operator logs (includes per-node skip reasons):**

```bash
kubectl --context <ctx> --namespace <ns> logs --selector app.kubernetes.io/name=kilo-clustermesh-operator --follow
```

**Check Kubernetes events on the ClusterMesh resource:**

```bash
kubectl --context <ctx> --namespace <ns> get events
```

The controller emits a `Warning` event for every skipped node. The event `Reason` field is the `NodeSkipReason` string; the `Action` field is `SkipNodePeering`.

**Verify Peer CRs exist in a remote cluster:**

```bash
kubectl --context <remote-ctx> get peers.kilo.squat.ai
```

---

## Node Skip Reasons

The controller validates each node against the `ClusterEntry` for its cluster. Validation runs in this order: PodCIDR → WireGuard IP → Public Key → Endpoint. **A node that fails an earlier check is not checked further** — fix issues in order.

Duplicate WireGuard IP detection (`FindDuplicateWGIPs`) runs before per-node validation. The first node with a given host IP keeps its entry; later nodes are flagged `WGIPDuplicate` and skipped before `ValidateNode` is called.

| Reason | Symptom | Likely Cause | Fix |
| --- | --- | --- | --- |
| `NodeNoPodCIDR` | Node has no PodCIDRs or an unparseable first PodCIDR | CNI not yet assigned a pod subnet, or node is not schedulable | Wait for CNI assignment or check `Node.Spec.PodCIDRs` |
| `NodePodCIDROutOfRange` | Node's first PodCIDR is not a subnet of any `ClusterEntry.podCIDRs` | `ClusterEntry.podCIDRs` does not cover the node's actual pod subnet | Expand or correct `podCIDRs` in the ClusterMesh spec |
| `NodeNoWireguardIP` | `kilo.squat.ai/wireguard-ip` annotation missing or empty | Kilo has not yet assigned a WireGuard interface IP to the node | Ensure Kilo is running and `granularity: cross` is set; see [per-node-setup](./per-node-setup.md) |
| `WGIPInvalid` | `kilo.squat.ai/wireguard-ip` annotation present but not a valid CIDR | Annotation was set manually with a malformed value | Correct or remove the annotation; let Kilo re-set it |
| `WGIPOutOfRange` | Node's WireGuard host IP is not within `ClusterEntry.wireguardCIDR`, OR `wireguardCIDR` itself is invalid | Wrong `wireguardCIDR` in the spec, or node annotation points to a different subnet | Correct `wireguardCIDR` in the ClusterMesh spec to match your Kilo WireGuard subnet |
| `WGIPDuplicate` | Two or more nodes have the same WireGuard host IP (prefix length ignored) | Kilo assigned the same IP to multiple nodes due to misconfiguration; `10.4.0.1/16` and `10.4.0.1/32` are treated as the same host IP | Identify the conflicting nodes via events/logs; fix Kilo's IP assignment so each node has a unique host IP |
| `NodeNoPublicKey` | `kilo.squat.ai/key` annotation missing or empty | Kilo has not yet populated the WireGuard public key | Ensure Kilo is running; check `kubectl --context <ctx> get node <name> --output yaml` for the annotation |
| `NodeNoEndpoint` | No endpoint source found: no `clustermesh-endpoint`, no `force-endpoint`, no `ExternalIP` | Node has no external IP and no endpoint annotation set | Set `kilo.squat.ai/clustermesh-endpoint` or ensure the node has a `Node.Status.Addresses` entry of type `ExternalIP`; see [per-node-setup](./per-node-setup.md) |
| `NodeEndpointInvalid` | An endpoint annotation is present with a non-empty value that cannot be parsed as `host:port` | Typo or malformed value in `kilo.squat.ai/clustermesh-endpoint` or `kilo.squat.ai/force-endpoint` | Fix the annotation value; format is `host:port` |

> **Note on `SkippedNodes` count:** The `status.clusters[*].skippedNodes` integer counts all skip reasons together. It does not distinguish between `WGIPDuplicate` and other reasons. Use `kubectl get events` or operator logs to find per-node reasons.

---

## Mesh-Level Validation Errors

These errors are set as status conditions before any peer reconciliation begins. If a mesh-level error is present, no peers are created or updated for the affected ClusterMesh.

### `ValidateClusterNetworks`

Called for every reconcile. Checks that all CIDRs within a single ClusterMesh are pairwise disjoint. The set of checked CIDRs for each cluster entry is:

```text
podCIDRs + wireguardCIDR + serviceCIDR (if set) + additionalCIDRs
```

Even `wireguardCIDR` values from different clusters within the same mesh must not overlap each other.

**Error format:** `CIDR overlap between cluster "<A>" (<cidr>) and cluster "<B>" (<cidr>)`

### `ValidateMeshNetworks`

Called during reconcile after `ValidateClusterNetworks`. Lists **all** ClusterMesh objects in the operator's namespace and checks for cross-mesh CIDR overlaps. A CIDR that appears in mesh-a and mesh-b (even for different CIDR types) is an overlap.

**Error format:** `CIDR overlap between mesh "<A>" (cluster "<X>", <cidr>) and mesh "<B>" (cluster "<Y>", <cidr>)`

**Effect:** Sets `NetworksOverlap=True` and `Ready=False` on the affected ClusterMesh. Reconciliation stops — no peers are created or updated. Fix: correct the overlapping CIDRs in the ClusterMesh spec.

> **Warning:** If mesh-a and mesh-b share a CIDR, the mesh that triggers the overlap check will be blocked. Both meshes may need to be corrected.

---

## Status Conditions

The controller manages two condition types on every ClusterMesh resource.

| Condition Type | Status | Reason | Meaning |
| --- | --- | --- | --- |
| `Ready` | `True` | `Reconciled` | All clusters were reconciled successfully. Peer objects have been applied. |
| `Ready` | `False` | `NetworksOverlap` | CIDR overlap was detected across ClusterMesh objects in the namespace. Reconciliation was blocked. |
| `NetworksOverlap` | `True` | `CIDROverlap` | A CIDR overlap was found. The `Message` field contains the full overlap description. |
| `NetworksOverlap` | `False` | `NoOverlap` | All CIDRs are disjoint. Normal state. |

The `Ready=False/NetworksOverlap` path is the only path that actively blocks reconciliation. All other failures (node skips, unreachable clusters) are recorded in `status.clusters` and events but do not prevent the rest of the mesh from being reconciled.

To inspect conditions:

```bash
kubectl --context <ctx> --namespace <ns> get clustermesh.kilo.squat.ai <name> --output jsonpath='{.status.conditions}'
```

---

## Common Pitfalls

### `Ready=False, Reason=NetworksOverlap` with overlap message

The `Message` field in the `Ready` condition reads `"CIDR overlap detected across meshes"`. The full overlap detail is in the `NetworksOverlap` condition's `Message`. Run:

```bash
kubectl --context <ctx> --namespace <ns> get clustermesh.kilo.squat.ai <name> --output yaml
```

Look for `.status.conditions[?(@.type=="NetworksOverlap")].message` — it identifies the two clusters and the overlapping CIDR string. Fix the overlap in `Spec.Clusters[*].podCIDRs`, `wireguardCIDR`, `serviceCIDR`, or `additionalCIDRs` as indicated.

### Some nodes peer, others don't

This is nearly always a per-node annotation problem. Check:

1. `kubectl --context <ctx> get events --namespace <ns>` — look for `Warning/SkipNodePeering` events listing the affected node names and reasons.
2. `kubectl --context <ctx> get node <name> --output yaml` — verify the four annotations: `kilo.squat.ai/wireguard-ip`, `kilo.squat.ai/key`, `kilo.squat.ai/clustermesh-endpoint` (or `force-endpoint`).
3. Cross-check the node's `Spec.PodCIDRs[0]` against `ClusterEntry.podCIDRs`.

See [per-node-setup](./per-node-setup.md) for the full annotation reference.

> **Important:** The operator does not watch Node objects. Changes to node annotations are not detected automatically. After correcting any node annotation, write a no-op to the ClusterMesh resource (e.g., add or change a label) to trigger a reconcile.

### Operator restarts on Secret change

The `ChangeWatcher` computes a fingerprint at startup that covers: the names of all cluster entries across all ClusterMesh objects in the operator namespace, and the `ResourceVersion` of each referenced kubeconfig Secret. When any of the following changes, the fingerprint changes and the operator process exits (allowing Kubernetes to restart the pod):

- A new ClusterMesh is created or deleted in the namespace
- A cluster entry's `name` is changed
- The `ResourceVersion` of a referenced kubeconfig Secret changes (i.e. the Secret was updated)

This is intentional design, not a crash. The operator must restart to rebuild the multicluster client cache (`ClusterRegistry`) with fresh kubeconfigs. After restart, full reconciliation runs automatically. Expect a brief period where no peers are being updated during the restart.

### Endpoint chain lazy evaluation: malformed `force-endpoint` silently ignored

Endpoint sources are evaluated in priority order: `clustermesh-endpoint` → `force-endpoint` → `ExternalIP`. Evaluation stops at the first non-empty source that parses successfully. If `clustermesh-endpoint` is valid, `force-endpoint` is never checked — a typo in `force-endpoint` goes unnoticed. The bug only surfaces if `clustermesh-endpoint` is removed.

Conversely: if `clustermesh-endpoint` is **present but malformed**, the validator does **not** fall through to `force-endpoint`. The node is immediately skipped with `NodeEndpointInvalid`. A valid `force-endpoint` on the same node does not help.

See [per-node-setup](./per-node-setup.md) for full endpoint annotation behavior.

### Anchor peer absent, service CIDRs unreachable

If `ClusterEntry.serviceCIDR` or `additionalCIDRs` are set but remote clusters cannot reach the service network, check whether an anchor peer was created:

```bash
kubectl --context <remote-ctx> get peers.kilo.squat.ai --selector kilo.squat.ai/mesh=<mesh-name>
```

The anchor peer is built from `nodes[0]` (the first valid node in the cluster). If that node has no resolvable endpoint, `BuildAnchorPeer` returns `nil` silently — no anchor peer is created, no error is surfaced. Ensure at least one node in each cluster has a valid endpoint. See [architecture](./architecture.md) for the reconcile flow.

---

## Re-Examining the Embedded CRD

The operator installs and upgrades the ClusterMesh CRD at every startup from an embedded copy at `internal/crd/clustermeshes.yaml`. The Helm chart has no `crds/` directory. If the CRD in your cluster looks stale (missing fields, outdated validation), the operator is likely running an older image version.

To verify what CRD the running operator would apply, check the image tag and consult the corresponding release. Upgrading the operator image automatically upgrades the CRD on the next pod start.

See [configuration](./configuration.md) for the full CRD field reference and [architecture](./architecture.md) for the CRD install flow.

---

*Cross-references: [per-node-setup](./per-node-setup.md) · [configuration](./configuration.md) · [architecture](./architecture.md) · [README](../README.md)*
