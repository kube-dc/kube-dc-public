---
name: manage-cluster
description: Manage Kube-DC managed Kubernetes clusters — scale worker pools, upgrade versions, access kubeconfig, and monitor status. Covers day-2 operations via kubectl patch.
---

## Prerequisites
- KdcCluster must exist and be Ready
- Project namespace: `{org}-{project}`

## Worker Pool Defaults

All KdcCluster workers use **DataVolume storage** by default. Key fields per pool:

| Field | Default / Required | Description |
|-------|-------------------|-------------|
| `name` | **required** | Pool name (e.g. `workers`) |
| `replicas` | 1 | Number of worker nodes |
| `cpuCores` | 1 | vCPUs per worker |
| `memory` | 3Gi | RAM per worker |
| `diskSize` | 20Gi | Root disk size |
| `image` | **required** | Container disk image matching K8s version |
| `storageType` | `datavolume` | **Always use `datavolume`** (default) |
| `infrastructureProvider` | `kubevirt` | Infrastructure backend |

Current image: `docker.io/shalb/ubuntu-2404-container-disk:v1.35.2`

## Common Operations

### Scale Worker Pool (JSON Patch — Recommended)

Use `--type=json` to patch specific fields without affecting others:

```bash
# Scale pool at index 0 to 5 replicas
kubectl patch kdccluster {cluster} -n {namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

### Scale Worker Pool (Merge Patch)

**Warning**: `--type merge` replaces the **entire** `workers` array. You MUST include ALL pools with ALL fields.

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 5, "cpuCores": 2, "memory": "8Gi",
       "diskSize": "20Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"},
      {"name": "highmem", "replicas": 2, "cpuCores": 4, "memory": "16Gi",
       "diskSize": "40Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"}
    ]
  }
}'
```

### Scale to Zero (Pause Workers)

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":0}]'
```

Control plane keeps running; only workers are removed.

### Upgrade Kubernetes Version

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"v1.35.0"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"}
]'
```

See @upgrade-version.md for constraints and procedures.

### Access Kubeconfig

The kubeconfig secret contains multiple keys for different access methods:

| Key | Endpoint | Use |
|-----|---------|-----|
| `admin.conf` | `https://{cluster}-cp-{ns}.kube-dc.cloud:443` | **External public URL** (recommended) |
| `super-admin.conf` | `https://{internal-ip}:6443` | Internal VPC IP |
| `super-admin.svc` | `https://{cluster}-cp.{ns}.svc:6443` | Internal Service (mgmt cluster only) |

```bash
# Extract kubeconfig with external public endpoint (recommended)
kubectl get secret {cluster}-cp-admin-kubeconfig -n {namespace} \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/{cluster}-kubeconfig
chmod 600 /tmp/{cluster}-kubeconfig

# Use it
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get pods -A
```

See @kubeconfig-access.md for details.

### Monitor Status

```bash
# Watch cluster phase
kubectl get kdccluster {cluster} -n {namespace} -w

# Check worker node status (via tenant kubeconfig)
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes

# Check cluster events
kubectl describe kdccluster {cluster} -n {namespace}
```

## Verification

After cluster operations, verify:

### After Scale
```bash
# 1. Check worker count matches desired
kubectl get kdccluster {cluster} -n {namespace} -o jsonpath='{.spec.workers[0].replicas}'
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
# Expected: Node count matches total replicas across all pools

# 2. All nodes Ready
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}'
# Expected: All True
```

### After Upgrade
```bash
# 1. Check cluster version updated
kubectl get kdccluster {cluster} -n {namespace} -o jsonpath='{.spec.version}'

# 2. Verify nodes are running new version
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.nodeInfo.kubeletVersion}{"\n"}{end}'
# Expected: All nodes on new version

# 3. Check cluster phase
kubectl get kdccluster {cluster} -n {namespace} -o jsonpath='{.status.phase}'
# Expected: Ready
```

**Success**: Node count matches, all nodes Ready, version updated.
**Failure**: `kubectl describe kdccluster {cluster} -n {namespace}` — check conditions and events.

## Safety
- Sequential minor version upgrades only (v1.34 → v1.35, no skipping)
- No downgrades supported
- Worker image MUST match the Kubernetes version
- **Always use `storageType: datavolume`** — this is the production default
- Rolling update: new worker Ready before old one removed (zero downtime)
- Prefer `--type=json` patch over merge patch to avoid dropping fields
- Never expose kubeconfig contents in chat output
- Write kubeconfig to temp file with `chmod 600`
- Clean up temporary kubeconfig files after use
