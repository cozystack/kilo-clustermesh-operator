# Per-Node Setup

> Annotations the operator reads from each Node, and how endpoints are resolved.

## Table of Contents

- [Overview](#overview)
- [Required Annotations](#required-annotations)
  - [kilo.squat.ai/wireguard-ip](#kilosquataiwireguard-ip)
  - [kilo.squat.ai/key](#kilosquataikey)
- [Endpoint Resolution Chain](#endpoint-resolution-chain)
  - [Source 1 — clustermesh-endpoint (highest priority)](#source-1--clustermesh-endpoint-highest-priority)
  - [Source 2 — force-endpoint (Kilo legacy)](#source-2--force-endpoint-kilo-legacy)
  - [Source 3 — Node ExternalIP fallback](#source-3--node-externalip-fallback)
  - [Format and IPv6 bracketing](#format-and-ipv6-bracketing)
- [Strict-Invalid Behavior](#strict-invalid-behavior)
- [Examples](#examples)
- [Migrating From force-endpoint To clustermesh-endpoint](#migrating-from-force-endpoint-to-clustermesh-endpoint)

---

## Overview

The operator reads a small set of annotations from Node objects in each remote cluster to build WireGuard peers. Kilo writes most of these annotations itself as part of its normal operation, but one — `kilo.squat.ai/clustermesh-endpoint` — is operator-specific and must be set manually when you need to control the cross-cluster endpoint independently of Kilo's own routing decisions. The operator does **not** watch Node objects; after changing any node annotation you must trigger a manual reconcile (see below).

---

## Required Annotations

The two annotations below must be present and valid on every node for that node to be included in the mesh. Missing or malformed values cause the node to be skipped with a reason surfaced in the ClusterMesh status (see [Troubleshooting](./troubleshooting.md) for the full skip-reason table).

| Annotation | Constant | Written by |
| --- | --- | --- |
| `kilo.squat.ai/wireguard-ip` | `AnnotationWireguardIP` | Kilo (automatic) |
| `kilo.squat.ai/key` | `AnnotationPublicKey` | Kilo (automatic) |

### kilo.squat.ai/wireguard-ip

Carries the WireGuard interface address of the node. The operator validates that the host IP portion of this value falls within the `wireguardCIDR` declared for that cluster in the ClusterMesh spec (see [Configuration](./configuration.md)).

**Fork-aware parsing.** Two formats are accepted:

| Kilo fork | Written value | Example |
| --- | --- | --- |
| Upstream Kilo | `<host>/32` | `10.4.0.1/32` |
| cozystack-Kilo | `<host>/<subnet-mask>` | `100.66.0.3/16` |

In both cases only the **host IP** is extracted and validated. The prefix length in the annotation does not affect the mesh — `AllowedIPs` in the generated Peer is always `<host-ip>/32` (or `/128` for IPv6), regardless of what prefix was written. This prevents a cozystack-style `/16` annotation from claiming the entire subnet in another cluster's routing table.

> **Duplicate-IP gotcha.** The duplicate-IP check normalises prefix lengths before comparing. `10.4.0.1/32` and `10.4.0.1/16` resolve to the same host IP and therefore conflict. The first node in API listing order keeps its IP; later duplicates are skipped with `WGIPDuplicate`. See [Troubleshooting](./troubleshooting.md) for the full reason list.

### kilo.squat.ai/key

The WireGuard public key for the node. The value is an opaque base64 string written by Kilo. The operator passes it unchanged into the Peer object — no validation beyond non-empty is performed here.

---

## Endpoint Resolution Chain

The operator calls `ResolveEndpoint(node, fallbackPort)` for each node. Sources are tried in priority order; **the first non-empty source wins**. Evaluation is lazy: once a source provides a value (valid or malformed), no lower-priority source is consulted.

```
1. kilo.squat.ai/clustermesh-endpoint   ← operator-specific, highest priority
2. kilo.squat.ai/force-endpoint         ← Kilo's own annotation, legacy
3. Node.Status.Addresses (ExternalIP)   ← last resort, uses wireguardPort
```

### Source 1 — clustermesh-endpoint (highest priority)

`kilo.squat.ai/clustermesh-endpoint` (`AnnotationClustermeshEndpoint`) is set by operators and users. It takes precedence over everything else. Its purpose is to decouple cross-cluster endpoint selection from Kilo's intra-cluster topology decisions: changing this annotation has no effect on how Kilo routes traffic between nodes in the same cluster.

This is the recommended annotation when you need a stable, manually controlled endpoint for cross-cluster WireGuard peers.

### Source 2 — force-endpoint (Kilo legacy)

`kilo.squat.ai/force-endpoint` (`AnnotationForceEndpoint`) is Kilo's built-in annotation for overriding endpoint detection. The operator treats it as a fallback when `clustermesh-endpoint` is absent.

> **Side-effect warning.** Unlike `clustermesh-endpoint`, Kilo itself also reads `force-endpoint` and uses it for intra-cluster peer endpoint selection. Setting it can affect intra-cluster routing, including interactions with Kilo's `cross` granularity setting. Prefer `clustermesh-endpoint` when you only want to control cross-cluster endpoints.

### Source 3 — Node ExternalIP fallback

When neither annotation is set, the operator scans `Node.Status.Addresses` for entries with `Type=ExternalIP`. `InternalIP` and `Hostname` entries are ignored.

- **IPv4 preferred over IPv6.** The first IPv4 ExternalIP is used immediately. IPv6 is only selected when no IPv4 ExternalIP exists.
- **Port.** The port is taken from `ClusterEntry.wireguardPort` (default: `51820`). See [Configuration](./configuration.md) to set a non-default port.

### Format and IPv6 bracketing

All endpoint values — whether from an annotation or synthesised from an ExternalIP — must conform to Go's `net.SplitHostPort` format:

```
<host>:<port>
```

IPv6 addresses must be enclosed in square brackets:

```
[2001:db8::1]:51820
```

Bare IPv6 without brackets (e.g. `2001:db8::1:51820`) will fail parsing and the node will be skipped. When the operator synthesises an endpoint from `Node.Status.Addresses` it calls `net.JoinHostPort`, which adds brackets automatically. When you set `clustermesh-endpoint` or `force-endpoint` manually for an IPv6 host, you must add the brackets yourself.

Bracketed DNS names are also accepted:

```
[node.example.com]:51820
```

The brackets are stripped before the DNS name is placed in the Peer object.

---

## Strict-Invalid Behavior

A **present-but-malformed** annotation value is a **hard error**. The operator does not fall through to the next source. The node is excluded from the mesh and the ClusterMesh status surfaces `NodeEndpointInvalid`.

This applies to both `clustermesh-endpoint` and `force-endpoint`. Empty or absent annotations are treated as "not set" and cause the next source to be tried. A non-empty value that cannot be parsed as `host:port` (by `net.SplitHostPort`) is always an error.

**Lazy-validation gotcha.** Because evaluation stops at the first non-empty source, a malformed lower-priority annotation can go undetected. Concretely: if `clustermesh-endpoint` is present and valid, `force-endpoint` is never inspected — a typo in `force-endpoint` is silently ignored. The typo only surfaces if `clustermesh-endpoint` is later removed. See [Troubleshooting](./troubleshooting.md) for a worked example and the rationale.

---

## Examples

All examples use a node in a remote cluster. Annotations are shown as they would appear in the Node manifest. The `wireguardPort` in the ClusterMesh spec is `51820` unless noted.

### Example A — Only clustermesh-endpoint set

```yaml
metadata:
  annotations:
    kilo.squat.ai/wireguard-ip: "10.4.0.5/32"
    kilo.squat.ai/key: "abc123...base64...=="
    kilo.squat.ai/clustermesh-endpoint: "203.0.113.1:51820"
```

**Result:** endpoint = `203.0.113.1:51820` (Source 1 wins; Sources 2 and 3 are not consulted).

---

### Example B — Only force-endpoint set (Kilo legacy)

```yaml
metadata:
  annotations:
    kilo.squat.ai/wireguard-ip: "10.4.0.6/32"
    kilo.squat.ai/key: "def456...base64...=="
    kilo.squat.ai/force-endpoint: "198.51.100.1:51820"
```

**Result:** endpoint = `198.51.100.1:51820` (Source 1 absent; Source 2 wins).

> Remember: `force-endpoint` is also read by Kilo for intra-cluster peers. Prefer `clustermesh-endpoint` for cross-cluster-only control.

---

### Example C — No annotation, ExternalIP fallback with custom port

ClusterMesh spec has `wireguardPort: 51821` for this cluster.

```yaml
metadata:
  annotations:
    kilo.squat.ai/wireguard-ip: "10.4.0.7/32"
    kilo.squat.ai/key: "ghi789...base64...=="
  # no clustermesh-endpoint, no force-endpoint
status:
  addresses:
    - type: ExternalIP
      address: "203.0.113.5"
```

**Result:** endpoint = `203.0.113.5:51821` (Sources 1 and 2 absent; Source 3 finds an IPv4 ExternalIP and uses `wireguardPort`).

---

### Example D — clustermesh-endpoint wins over force-endpoint (lazy evaluation)

```yaml
metadata:
  annotations:
    kilo.squat.ai/wireguard-ip: "10.4.0.8/32"
    kilo.squat.ai/key: "jkl012...base64...=="
    kilo.squat.ai/clustermesh-endpoint: "203.0.113.10:51820"
    kilo.squat.ai/force-endpoint: "not-valid"       # malformed — but never checked
```

**Result:** endpoint = `203.0.113.10:51820`. Because Source 1 provides a valid value, Source 2 (`force-endpoint`) is never evaluated. The malformed value is silently ignored **while `clustermesh-endpoint` is present and valid**. If `clustermesh-endpoint` is removed, the malformed `force-endpoint` will then surface as `NodeEndpointInvalid`.

---

## Migrating From force-endpoint To clustermesh-endpoint

Use `clustermesh-endpoint` when you want to control cross-cluster endpoints without affecting Kilo's intra-cluster routing (e.g., nodes using the `cross` granularity setting).

**Steps:**

1. Add `clustermesh-endpoint` with the same value currently in `force-endpoint`:

   ```sh
   kubectl annotate node node-01 \
     kilo.squat.ai/clustermesh-endpoint=203.0.113.1:51820 \
     --overwrite
   ```

2. Verify the annotation is set correctly:

   ```sh
   kubectl get node node-01 \
     --output jsonpath='{.metadata.annotations.kilo\.squat\.ai/clustermesh-endpoint}'
   ```

3. Remove the old `force-endpoint` annotation (if it was set only for cross-cluster purposes):

   ```sh
   kubectl annotate node node-01 kilo.squat.ai/force-endpoint-
   ```

4. Trigger a reconcile (the operator does not watch Node objects):

   ```sh
   kubectl annotate clustermesh <mesh-name> \
     reconcile-trigger=$(date +%s) \
     --overwrite \
     --namespace <operator-namespace>
   ```

5. Confirm the ClusterMesh status shows `Ready=True` and the node appears in the peer list:

   ```sh
   kubectl get clustermesh <mesh-name> \
     --namespace <operator-namespace> \
     --output yaml
   ```

Repeat steps 1–4 for each node in the remote cluster. Verify that intra-cluster Kilo routing is unaffected after the migration.

---

*See also:*
*[Configuration](./configuration.md) — `wireguardPort` and other ClusterMesh CRD fields*
*[Troubleshooting](./troubleshooting.md) — `NodeNoEndpoint`, `NodeEndpointInvalid`, `WGIPInvalid`, and the full skip-reason table*
*[Architecture](./architecture.md) — high-level reconcile flow*
*[README](../README.md) — quick start and project overview*
