---
name: manage-cluster
description: Manage Kube-DC managed Kubernetes clusters — scale worker pools, upgrade versions, access kubeconfig, and monitor status. Covers day-2 operations via kubectl patch.
---

## Prerequisites
- KdcCluster must exist and be Ready
- Project namespace: `{org}-{project}`

## Common Operations

### Scale Worker Pool

```bash
# Scale 'workers' pool to 5 replicas (--type merge replaces entire workers array)
kubectl patch kdccluster {cluster} -n {namespace} \
  --type merge -p '{"spec":{"workers":[{"name":"workers","replicas":5}]}}'
```

**Important**: `--type merge` replaces the entire `workers` array. You MUST include ALL pools in the patch, not just the one you're scaling.

Multi-pool example:
```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 5, "cpuCores": 2, "memory": "8Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"},
      {"name": "highmem", "replicas": 2, "cpuCores": 4, "memory": "16Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"}
    ]
  }
}'
```

### Scale to Zero (Pause Workers)

```bash
kubectl patch kdccluster {cluster} -n {namespace} \
  --type merge -p '{"spec":{"workers":[{"name":"workers","replicas":0}]}}'
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

```bash
# Extract super-admin kubeconfig
kubectl get secret {cluster}-cp-admin-kubeconfig -n {namespace} \
  -o jsonpath='{.data.super-admin\.svc}' | base64 -d > /tmp/{cluster}-kubeconfig

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

## Safety
- Sequential minor version upgrades only (v1.34 → v1.35, no skipping)
- No downgrades supported
- Worker image MUST match the Kubernetes version
- Rolling update: new worker Ready before old one removed (zero downtime)
- Never expose kubeconfig contents in chat output
- Write kubeconfig to temp file with `chmod 600`
- Clean up temporary kubeconfig files after use
