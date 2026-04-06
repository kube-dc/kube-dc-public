# Scaling Worker Pools

## Key Concepts

- **Prefer `--type=json` patch** — safer, only changes targeted fields
- `--type merge` replaces the **entire** `workers` array — include ALL pools with ALL fields
- Scaling to 0 keeps control plane running (cost savings)
- Rolling update: new worker Ready before old one removed
- **Always use `storageType: datavolume`** (production default)

## Scale Single Pool (JSON Patch — Recommended)

```bash
# Scale pool at index 0 to 5 replicas
kubectl patch kdccluster {cluster} -n {namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

## Scale with Multiple Pools (Merge Patch)

MUST include all pools with all fields:

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 3, "cpuCores": 2, "memory": "8Gi",
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

## Scale Specific Pool (JSON Patch — Safer)

```bash
# Scale pool at index 0 to 5 replicas
kubectl patch kdccluster {cluster} -n {namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

## Scale to Zero

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":0}]'
```

## Add a New Worker Pool

Must use merge patch (adds to array):

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 3, "cpuCores": 2, "memory": "8Gi",
       "diskSize": "20Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"},
      {"name": "gpu-pool", "replicas": 1, "cpuCores": 8, "memory": "32Gi",
       "diskSize": "40Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"}
    ]
  }
}'
```
