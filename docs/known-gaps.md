# Known Gaps and Outstanding Work

> Handoff document for contributors picking up the operator after the initial POC.

This document tracks divergences from the upstream proposal
([cozystack/community#7](https://github.com/cozystack/community/pull/7)),
operational risks identified during review, and concrete follow-up work.
The operator is functional end-to-end in its current shape but is not
yet a full implementation of the proposal as written.

## Table of Contents

- [Operator Status](#operator-status)
- [Gaps Relative To The Proposal](#gaps-relative-to-the-proposal)
- [Operational Risks](#operational-risks)
- [Recommended Follow-Ups](#recommended-follow-ups)
- [Settled Design Decisions](#settled-design-decisions)
- [Proposal Text Corrections](#proposal-text-corrections)
- [References](#references)

---

## Operator Status

What works today:

- `ClusterMesh` CRD with typed CIDR fields, status conditions
  (`Ready`, `NetworksOverlap`), per-cluster registered/skipped counts
- Mesh- and cluster-level CIDR overlap validation
  (`internal/validation/mesh.go`)
- Per-node validation: PodCIDR containment, WireGuard IP containment,
  duplicate-IP dedup, public-key presence, endpoint resolvability
  (`internal/validation/node.go`)
- Three-tier endpoint resolution chain on each node:
  `kilo.squat.ai/clustermesh-endpoint` → `kilo.squat.ai/force-endpoint`
  → `Node.Status.Addresses` ExternalIP with `wireguardPort` fallback
  (`internal/kilonode/endpoint.go`)
- Per-cluster Helm-managed Peer reconciliation in remote clusters via
  kubeconfig Secrets, label-isolated, finalizer-cleaned
  (`internal/peer/`, `internal/controller/clustermesh_controller.go`)
- Anchor Peer for cluster-wide CIDRs (`serviceCIDR`, `additionalCIDRs`)
  (`internal/peer/builder.go:83-105`)
- Embedded CRD bootstrap at startup (`internal/crd/install.go`)
- Restart-on-config-change via fingerprint watcher
  (`internal/restart/watcher.go`)
- Full documentation under `docs/` (architecture, installation,
  configuration, per-node-setup, troubleshooting)

What is incomplete or divergent: see the sections below.

---

## Gaps Relative To The Proposal

### Node Watches Are Missing (Blocker)

The proposal contracts live reconciliation: any change to a Node
annotation or status in any listed cluster must trigger a reconcile of
the owning `ClusterMesh`. The operator only watches `ClusterMesh`
resources — Node-level changes are not detected automatically.

**Workaround in use**: write a no-op annotation to the `ClusterMesh`
resource to force a reconcile cycle. Cozystack provisioning automation
can do this externally, but it breaks the self-healing guarantee the
proposal advertises for standalone use.

**Source of truth**: `cmd/main.go` builds the controller without a Node
informer; `internal/controller/clustermesh_controller.go:107-119`
configures the watch source as `ClusterMesh` only.

**Effort to close**: medium. The `ClusterRegistry`
(`internal/multicluster/registry.go`) already holds `cluster.Cluster`
objects with started caches. Wire a Node informer per remote cluster
into the controller's watch set with a
`handler.EnqueueRequestsFromMapFunc` that maps remote-cluster Node
events back to local `ClusterMesh` requests, scoped to clusters that
reference the affected cluster name. Care needed around scoping so a
single Node event does not fan out to every `ClusterMesh` in the
namespace.

### CRD Schema Diverges From The Proposal Example

The proposal example uses a single flat `spec.clusters[].allowedNetworks: [...]`
list. The operator uses typed fields: `podCIDRs`, `wireguardCIDR`,
`serviceCIDR`, `additionalCIDRs`. The proposal's Open Question §6
explicitly raises this as a design choice and defers it to v1alpha2.

This is an intentional improvement, not a defect. The typed schema
gives stronger validation, better documentation, and clearer
per-cluster semantics. But the proposal text still shows the flat
list — anyone implementing against the proposal as written will
diverge from the operator. Resolution belongs in the proposal, not
in the code (see [Proposal Text Corrections](#proposal-text-corrections)).

### Secret-Change Handling Is Heavier Than Proposed

The proposal asks the controller to "re-establish the watch and
reconcile" when a kubeconfig Secret changes. The operator cancels the
manager context and lets the pod restart (`internal/restart/watcher.go`),
which rebuilds the `ClusterRegistry` from fresh Secret content.

Functionally equivalent; operationally heavier — in-flight reconciles
are dropped, and there is a brief gap in Peer maintenance during the
restart. In a hot rotation scenario, repeated pod restarts could
cause churn.

**Effort to close**: medium. Replace the restart path with a live
client-rebuild on Secret change in `ClusterRegistry`. Requires care
to invalidate in-flight reconciles and avoid using stale clients.

---

## Operational Risks

### Anchor Peer Silently Suppressed When `nodes[0]` Has No Endpoint

`BuildAnchorPeer` (`internal/peer/builder.go:83-105`) returns `nil` when
`resolvePeerEndpoint(anchorNode, ...)` returns any error — including a
malformed endpoint annotation on `nodes[0]`. The consequence: cluster-wide
CIDRs (`serviceCIDR`, `additionalCIDRs`) become unreachable from remote
clusters for the duration of that reconcile cycle, with no Event or
status Condition surfaced.

The `docs/architecture.md` Warning callout documents this behavior, but
operationally there is no signal: an operator inspecting `ClusterMesh`
status sees `Ready=True` and a reasonable `registeredPeers` count,
yet inter-cluster Service traffic silently fails.

**Effort to close**: small. In `clustermesh_controller.go`
(around the call site that invokes `BuildAnchorPeer`), distinguish
"anchor not needed" (no `serviceCIDR` and no `additionalCIDRs`) from
"anchor suppressed by endpoint failure on `nodes[0]`". For the latter,
emit a Warning Event on the `ClusterMesh` and optionally set a
`AnchorPeerSuppressed=True` status condition.

---

## Recommended Follow-Ups

Ranked by ratio of impact to effort. Each item is independently
shippable.

1. **Anchor-peer suppression Event** — small patch, surfaces a real
   silent failure mode. Add an Event emission and consider a status
   Condition. ~30 minutes including a test.

2. **Proposal text corrections** — three text edits in
   cozystack/community#7. No code change. See
   [Proposal Text Corrections](#proposal-text-corrections).

3. **Node watches** — closes the only ❌ gap against the proposal.
   Medium effort, needs careful informer scoping. Should be tracked
   as a discrete RFC if the design touches multi-cluster
   controller-runtime patterns.

4. **Live Secret-change handling** — replace pod-restart with
   client-rebuild. Removes operational footgun. Medium effort.

5. **Anchor-node selection beyond `nodes[0]`** — current logic picks
   the first validated node as the anchor; if that node loses its
   endpoint the anchor is suppressed (item 1). A more robust choice
   would iterate validated nodes until one resolves an endpoint.
   Small effort once item 1 ships.

---

## Settled Design Decisions

Do not re-litigate the following. Each was chosen deliberately after
weighing alternatives.

- **Lazy endpoint chain validation.** The three-tier chain stops at the
  first non-empty source. A malformed lower-priority annotation is
  silently ignored when a higher-priority source resolves successfully.
  The alternative (validate all present annotations eagerly) was
  considered and rejected: chain-of-responsibility semantics support
  gradual migration from `force-endpoint` to `clustermesh-endpoint`
  without breaking on legacy typos. Strict-invalid behavior on the
  WINNING source is still in effect.

- **Cozystack-patched Kilo with `cross` granularity.** The operator
  targets the cozystack fork at `aenix-io/kilo` where every node
  receives its own WireGuard IP. Do not propose switching to upstream
  Kilo's `full` or `location` granularity for this codebase.

- **Prefix-agnostic WireGuard IP validation.** The `wireguard-ip`
  annotation may carry any prefix length (upstream Kilo writes `/32`;
  cozystack-Kilo writes `<host>/<subnet-mask>`, e.g. `100.66.0.3/16`).
  Both are accepted; only the host IP is validated against
  `wireguardCIDR`, and `AllowedIPs` is always normalised to `/32`
  (or `/128` for IPv6). Do not tighten to `/32`-only — that would
  break cozystack-Kilo node validation.

- **Typed CRD fields over flat `allowedNetworks`.** See
  [Gaps Relative To The Proposal](#gaps-relative-to-the-proposal).
  Decision recorded; only the proposal text remains to be updated.

- **CRD auto-bootstrap from embedded copy.** The operator applies the
  CRD at startup via `internal/crd/install.go`; the Helm chart does
  not bundle CRDs. This is documented in
  [`./installation.md`](./installation.md) and intentional.

---

## Proposal Text Corrections

Three text-only edits needed in cozystack/community#7. No code change
in this repository.

| Item | Proposal Section | Current Text | Should Be |
|---|---|---|---|
| Public-key annotation name | §Peer construction §5 | `kilo.squat.ai/wireguard-public-key` | `kilo.squat.ai/key` (verify against `internal/kilonode/annotations.go:30`) |
| WG-IP prefix rule | §Reconciliation §3 | "has prefix length `/32` (or `/128` for IPv6)" | Allow any prefix; validate host IP against `wireguardCIDR`. Note that vanilla Kilo writes `/32` and cozystack-Kilo writes `<host>/<subnet>` |
| CRD example | §CRD: ClusterMesh | `spec.clusters[].allowedNetworks: [...]` | `spec.clusters[].podCIDRs`, `.wireguardCIDR`, `.serviceCIDR`, `.additionalCIDRs` (see [`./configuration.md`](./configuration.md) for the full schema) |

---

## References

- Proposal PR: [cozystack/community#7](https://github.com/cozystack/community/pull/7)
- Operator documentation: [`./architecture.md`](./architecture.md),
  [`./configuration.md`](./configuration.md),
  [`./per-node-setup.md`](./per-node-setup.md),
  [`./troubleshooting.md`](./troubleshooting.md)
- Upstream Kilo: https://github.com/squat/kilo
- Cozystack-patched Kilo: https://github.com/aenix-io/kilo
