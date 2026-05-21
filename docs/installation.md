# Installation

> Step-by-step guide to deploying the Kilo ClusterMesh Operator and connecting your first pair of clusters.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Operator Deployment](#operator-deployment)
- [CRD Bootstrap](#crd-bootstrap)
- [Remote Cluster Kubeconfigs](#remote-cluster-kubeconfigs)
- [Example: Cozystack Deployment](#example-cozystack-deployment)
- [Verifying Installation](#verifying-installation)
- [Uninstalling](#uninstalling)

---

## Prerequisites

Before installing the operator, make sure every cluster that will join the mesh meets the following requirements.

### Kilo on every cluster

The operator manages [Kilo](https://kilo.squat.ai) `Peer` objects — it does not install or manage Kilo itself. Kilo must already be running on every cluster that will participate in the mesh. Specifically:

- Each node must have Kilo's agent running so that the node receives the standard Kilo annotations (`kilo.squat.ai/wireguard-ip`, `kilo.squat.ai/key`) that the operator reads to build peers.
- Kilo must be configured with `--mesh-granularity=cross`. This is a Cozystack-patched granularity that assigns a WireGuard IP to **every** node, rather than electing one leader per location label. The operator's validation rejects nodes that lack per-node WireGuard IPs.

See [Per-Node Setup](./per-node-setup.md) for the exact annotations the operator requires on each node.

### Kubernetes version

The operator targets the Kubernetes API surface used by `k8s.io/api v0.35.0` and `sigs.k8s.io/controller-runtime v0.23.3` (from `go.mod`). Kubernetes 1.29 or later is recommended. No features beyond standard CRDs, RBAC, and core resources are required.

> **Note on token Secrets**: Kubernetes 1.24 and later no longer auto-creates token Secrets for ServiceAccounts. The remote-cluster RBAC step below creates an explicit `kubernetes.io/service-account-token` Secret to obtain a long-lived token. Plan for this if your clusters run 1.24+.

### Non-overlapping CIDRs

Every cluster in the mesh must use **distinct, non-overlapping** address ranges for all three CIDR types:

| CIDR type | Example cluster-a | Example cluster-b | Purpose |
| --- | --- | --- | --- |
| `podCIDR` | `10.244.0.0/16` | `10.245.0.0/16` | Pod IP space; must not overlap across clusters |
| `serviceCIDR` | `10.96.0.0/16` | `10.97.0.0/16` | Service IP space; routed to the anchor peer |
| `wireguardCIDR` | `100.66.0.0/16` | `100.67.0.0/16` | WireGuard overlay; must not overlap across clusters |

The operator's `ValidateMeshNetworks` check runs before every reconcile. If any CIDR overlaps with another cluster in the same namespace, **reconciliation stops** for all affected meshes and the `ClusterMesh` status is set to `Ready=False` with reason `NetworksOverlap`. Overlapping CIDRs is the most common reason for a stuck installation — verify them before proceeding.

---

## Operator Deployment

The operator is distributed as a Helm chart located at `charts/kilo-clustermesh-operator/` in this repository. There is no external chart repository; install directly from the source tree or from a local copy.

### Install the chart

```shell
$ helm install kilo-clustermesh-operator charts/kilo-clustermesh-operator \
    --namespace kilo-clustermesh \
    --create-namespace
```

This single command:

1. Creates the namespace (because of `--create-namespace`).
2. Creates a ServiceAccount, ClusterRole, ClusterRoleBinding, Role, and RoleBinding for the operator.
3. Deploys the operator Deployment.
4. On first start the operator applies the `ClusterMesh` CRD itself — **no separate CRD apply is needed** (see [CRD Bootstrap](#crd-bootstrap)).

### Image source

The container image is published at `ghcr.io/cozystack/kilo-clustermesh-operator`. It is a multi-stage build from `golang:1.26`, producing a static binary that runs in a `gcr.io/distroless/static:nonroot` base image as UID 65532. The image source is at `Containerfile` in the repository root.

By default the chart uses the `appVersion` from `Chart.yaml` as the image tag. For production deployments it is strongly recommended to pin to a specific commit SHA.

### Default chart values

```yaml
image:
  repository: ghcr.io/cozystack/kilo-clustermesh-operator
  tag: ""            # uses Chart appVersion when empty
  pullPolicy: IfNotPresent

replicaCount: 1

leaderElect: true    # enables leader election; required when replicaCount > 1

metricsBindAddress: ":8080"   # HTTP metrics (see note below)
metricsSecure: false           # chart default is HTTP; binary default is HTTPS
healthProbeBindAddress: ":8081"

resources:
  limits:
    cpu: 500m
    memory: 128Mi
  requests:
    cpu: 10m
    memory: 64Mi

serviceAccount:
  create: true
  name: ""
  annotations: {}
```

> **Metrics scheme difference**: The chart overrides the binary's built-in default for metrics. The binary default is `--metrics-bind-address=0` (disabled) with `--metrics-secure=true` (HTTPS). The chart values enable metrics on `:8080` over plain **HTTP**. If you require HTTPS metrics, set `metricsSecure: true` and configure the necessary TLS certificates.

### Override values example

To pin the image to a specific commit SHA and increase memory limits:

```yaml
# my-values.yaml
image:
  repository: ghcr.io/cozystack/kilo-clustermesh-operator
  tag: sha-43caba9978f26383593bedec79930c62e7ecead7
  pullPolicy: IfNotPresent

resources:
  limits:
    cpu: 500m
    memory: 256Mi
  requests:
    cpu: 10m
    memory: 64Mi
```

```shell
$ helm install kilo-clustermesh-operator charts/kilo-clustermesh-operator \
    --namespace kilo-clustermesh \
    --create-namespace \
    --values my-values.yaml
```

---

## CRD Bootstrap

The `ClusterMesh` CRD (`clustermeshes.kilo.squat.ai`) is **not** bundled in the Helm chart's `crds/` directory. Instead, the operator binary self-applies the CRD at every startup using code in `internal/crd/install.go`.

### Why this approach

Embedding the CRD in the operator binary (at `internal/crd/clustermeshes.yaml`) rather than in the chart keeps the CRD schema tightly coupled to the version of the operator that interprets it. There is no risk of the chart being upgraded without the CRD being updated, or vice versa. To upgrade the CRD schema, simply upgrade the operator; the new binary applies the new schema on startup.

### What happens at startup

On startup, before `ctrl.NewManager()` is called, the operator:

1. Reads the embedded `clustermeshes.yaml`.
2. Calls `crd.InstallOrUpdate()` — creates the CRD if absent, patches it if present.
3. Polls for `Established=True` with a 500 ms interval, up to a 30-second timeout.
4. Only after the CRD is established does the manager start.

If the API server is slow to process the CRD (e.g., high load during cluster startup), the operator may time out after 30 seconds and exit. Kubernetes will restart the pod automatically.

### Operator RBAC for CRD management

The chart's ClusterRole grants the operator's ServiceAccount:

```yaml
apiGroups: [apiextensions.k8s.io]
resources: [customresourcedefinitions]
verbs: [get, create, update]
```

This is required for self-installation. **Do not** apply the CRD manually from `internal/crd/clustermeshes.yaml` — the operator will overwrite it on startup anyway, and a pre-existing CRD with a different resource version can cause unnecessary churn.

---

## Remote Cluster Kubeconfigs

The operator runs on one **central cluster** and connects to one or more **remote clusters** over their Kubernetes APIs. For each remote cluster, the operator needs a kubeconfig Secret in its own namespace. The Secret is referenced from the `ClusterMesh` CR using the `kubeconfigSecretRef` field (see [Configuration](./configuration.md) for the full CR reference).

### What permissions the remote kubeconfig must grant

On each remote cluster, create a ServiceAccount with the following ClusterRole:

```yaml
rules:
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list, watch]
  - apiGroups: [kilo.squat.ai]
    resources: [peers]
    verbs: [get, list, watch, create, update, patch, delete]
```

The operator needs to **read nodes** to discover node annotations (WireGuard IPs, public keys, endpoints) and **write peers** to push the computed WireGuard peer configuration. It does not need access to ClusterMesh objects, Secrets, CRDs, or any other resources on the remote cluster.

### Creating the remote RBAC

Apply a manifest similar to the following on each remote cluster:

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
# Kubernetes 1.24+ does not auto-create token Secrets for ServiceAccounts.
# Create one explicitly to obtain a long-lived token.
apiVersion: v1
kind: Secret
metadata:
  name: clustermesh-reader-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: clustermesh-reader
type: kubernetes.io/service-account-token
```

```shell
$ kubectl --context remote-cluster apply --filename remote-rbac.yaml
```

### Building the kubeconfig Secret

Once the token Secret is ready on the remote cluster, extract the token and CA certificate, build a kubeconfig, and store it as a Secret in the operator's namespace on the central cluster:

```shell
# Extract token and CA from the remote cluster
$ TOKEN=$(kubectl --context remote-cluster \
    --namespace kube-system \
    get secret clustermesh-reader-token \
    --output jsonpath='{.data.token}' | base64 --decode)

$ CA=$(kubectl --context remote-cluster \
    --namespace kube-system \
    get secret clustermesh-reader-token \
    --output jsonpath='{.data.ca\.crt}')

$ SERVER=$(kubectl --context remote-cluster \
    config view --minify --output jsonpath='{.clusters[0].cluster.server}')

# Write a minimal kubeconfig to a temp file
$ cat > /tmp/remote-kubeconfig.yaml <<EOF
apiVersion: v1
kind: Config
clusters:
- name: remote-cluster
  cluster:
    server: ${SERVER}
    certificate-authority-data: ${CA}
users:
- name: clustermesh-reader
  user:
    token: ${TOKEN}
contexts:
- name: remote-cluster
  context:
    cluster: remote-cluster
    user: clustermesh-reader
current-context: remote-cluster
EOF

# Sanity-check: list nodes via the new kubeconfig
$ kubectl --kubeconfig /tmp/remote-kubeconfig.yaml get nodes

# Store the kubeconfig as a Secret in the operator namespace on the central cluster
$ kubectl --context central-cluster \
    --namespace kilo-clustermesh \
    create secret generic remote-cluster-kubeconfig \
    --from-file=kubeconfig=/tmp/remote-kubeconfig.yaml

# Clean up the temp file — it contains a long-lived token
$ rm /tmp/remote-kubeconfig.yaml
```

> **Security note**: The kubeconfig contains a long-lived bearer token. Store it only in a Kubernetes Secret (encrypted at rest if your cluster supports it) and delete the local temp file immediately after creating the Secret.

---

## Example: Cozystack Deployment

The `deploy/cozystack/` directory contains a concrete reference deployment connecting two clusters. This section walks through the same pattern using generic names (`cluster-a` for the central cluster, `cluster-b` for the remote cluster).

### CIDR plan

| | cluster-a (central) | cluster-b (remote) |
| --- | --- | --- |
| podCIDR | `10.244.0.0/16` | `10.245.0.0/16` |
| serviceCIDR | `10.96.0.0/16` | `10.97.0.0/16` |
| wireguardCIDR | `100.66.0.0/16` | `100.67.0.0/16` |
| Kilo granularity | `cross` | `cross` |

### Step 1 — Install the operator on cluster-a

```shell
$ helm install kilo-clustermesh-operator charts/kilo-clustermesh-operator \
    --kubeconfig /path/to/cluster-a/kubeconfig \
    --namespace cozy-kilo \
    --create-namespace \
    --values deploy/cozystack/values-cluster-a.yaml
```

Example values file for a pinned production image:

```yaml
image:
  repository: ghcr.io/cozystack/kilo-clustermesh-operator
  tag: sha-43caba9978f26383593bedec79930c62e7ecead7
  pullPolicy: IfNotPresent

replicaCount: 1
leaderElect: true

resources:
  limits:
    cpu: 500m
    memory: 128Mi
  requests:
    cpu: 10m
    memory: 64Mi
```

Verify the operator is running and the CRD exists:

```shell
$ kubectl --kubeconfig /path/to/cluster-a/kubeconfig \
    --namespace cozy-kilo \
    get deployment,pod

$ kubectl --kubeconfig /path/to/cluster-a/kubeconfig \
    get crd clustermeshes.kilo.squat.ai
```

The CRD is created by the operator on first start — if it does not appear within ~30 seconds, check the operator pod logs.

### Step 2 — Apply remote RBAC on cluster-b

```shell
$ kubectl --kubeconfig /path/to/cluster-b/kubeconfig \
    apply --filename remote-rbac.yaml
```

Use the template from [Remote Cluster Kubeconfigs](#remote-cluster-kubeconfigs) above. Verify the token Secret was populated (the `kubernetes.io/service-account-token` controller fills in `token` and `ca.crt` asynchronously):

```shell
$ kubectl --kubeconfig /path/to/cluster-b/kubeconfig \
    --namespace kube-system \
    get secret clustermesh-reader-token
```

### Step 3 — Build and store the kubeconfig Secret on cluster-a

Follow the kubeconfig-building steps from [Remote Cluster Kubeconfigs](#remote-cluster-kubeconfigs), targeting cluster-b as the remote and cluster-a's `cozy-kilo` namespace as the destination:

```shell
$ kubectl --kubeconfig /path/to/cluster-a/kubeconfig \
    --namespace cozy-kilo \
    create secret generic cluster-b-kubeconfig \
    --from-file=kubeconfig=/tmp/cluster-b-kubeconfig.yaml
```

### Step 4 — Apply the ClusterMesh CR on cluster-a

```yaml
# clustermesh.yaml
apiVersion: kilo.squat.ai/v1alpha1
kind: ClusterMesh
metadata:
  name: my-mesh
  namespace: cozy-kilo
spec:
  clusters:
    - name: cluster-a
      local: true
      podCIDRs:
        - 10.244.0.0/16
      wireguardCIDR: 100.66.0.0/16
      serviceCIDR: 10.96.0.0/16
    - name: cluster-b
      kubeconfigSecretRef:
        name: cluster-b-kubeconfig
        key: kubeconfig
      podCIDRs:
        - 10.245.0.0/16
      wireguardCIDR: 100.67.0.0/16
      serviceCIDR: 10.97.0.0/16
```

```shell
$ kubectl --kubeconfig /path/to/cluster-a/kubeconfig \
    apply --filename clustermesh.yaml
```

The `local: true` flag on `cluster-a` tells the operator that this entry describes the cluster the operator itself is running in — no kubeconfig Secret is needed for it. Every other entry requires `kubeconfigSecretRef`.

For the full list of fields available in the `ClusterMesh` spec, see [Configuration](./configuration.md).

---

## Verifying Installation

### 1. Check the operator pod

```shell
$ kubectl --namespace kilo-clustermesh get pod \
    --selector app.kubernetes.io/name=kilo-clustermesh-operator
```

The pod should be in `Running` state. If it is crash-looping, inspect logs — the most common startup failures are:

- Missing `POD_NAMESPACE` environment variable (set automatically by the chart via downward API; indicates the chart is not being used).
- CRD establishment timeout (API server too slow; the pod will be restarted and retry).

### 2. Check the ClusterMesh status

```shell
$ kubectl --namespace kilo-clustermesh get clustermesh my-mesh --output yaml
```

Look for the `status.conditions` section. A healthy ClusterMesh shows:

```yaml
status:
  conditions:
    - type: Ready
      status: "True"
      reason: ClusterMeshReady
```

If `Ready=False`, check the `reason` and `message` fields. Common reasons:

- `NetworksOverlap` — CIDR overlap between clusters. Verify the CIDRs in the spec.
- Any error message related to kubeconfig — check that the referenced Secret exists and contains a valid kubeconfig.

See [Troubleshooting](./troubleshooting.md) for a full list of failure modes and remediation steps.

### 3. Check Peer objects appear on both clusters

On the central cluster, Peer objects for remote-cluster nodes should appear:

```shell
$ kubectl --namespace kilo-clustermesh get peers.kilo.squat.ai
```

On the remote cluster, Peer objects for central-cluster nodes should appear:

```shell
$ kubectl --context remote-cluster get peers.kilo.squat.ai
```

If peers are absent on the remote cluster, check operator logs for reconciliation errors. Node-level issues (missing annotations, duplicate WireGuard IPs) cause individual nodes to be skipped rather than failing the entire reconcile — see [Per-Node Setup](./per-node-setup.md) for the required node annotations.

### 4. Operator logs

```shell
$ kubectl --namespace kilo-clustermesh \
    logs deployment/kilo-clustermesh-operator \
    --follow
```

Successful reconciliation produces log lines indicating peer counts per cluster. Errors are logged with structured fields identifying which cluster and which node caused the problem.

---

## Uninstalling

> **Warning**: The ClusterMesh finalizer (`kilo-clustermesh.io/cleanup`) requires the operator to be **running** when the ClusterMesh CR is deleted. If you remove the Helm chart before deleting the CR, the finalizer will never be honoured and all Peer objects will be **orphaned** on every cluster. Always delete the CR first.

### Step 1 — Delete the ClusterMesh CR

```shell
$ kubectl --namespace kilo-clustermesh \
    delete clustermesh my-mesh
```

Wait for the resource to disappear (the finalizer causes deletion to block until the operator has cleaned up Peers on all clusters):

```shell
$ kubectl --namespace kilo-clustermesh \
    get clustermesh my-mesh --watch
```

### Step 2 — Uninstall the Helm chart

```shell
$ helm uninstall kilo-clustermesh-operator \
    --namespace kilo-clustermesh
```

### Step 3 — Remove RBAC on remote clusters

```shell
$ kubectl --context remote-cluster \
    delete --filename remote-rbac.yaml
```

### Step 4 — Clean up remaining resources

The operator namespace may be shared with Kilo itself. Remove only the resources specific to the operator:

```shell
# Delete the kubeconfig Secret(s)
$ kubectl --namespace kilo-clustermesh \
    delete secret remote-cluster-kubeconfig

# Delete the CRD (not removed by chart uninstall)
$ kubectl delete crd clustermeshes.kilo.squat.ai
```

---

## Next Steps

- [Configuration](./configuration.md) — full `ClusterMesh` CRD reference (all spec fields, status conditions).
- [Per-Node Setup](./per-node-setup.md) — required node annotations and how the operator resolves WireGuard endpoints.
- [Troubleshooting](./troubleshooting.md) — diagnosing `Ready=False`, missing peers, and CIDR overlap errors.
- [README](../README.md) — project overview and quick-start summary.
