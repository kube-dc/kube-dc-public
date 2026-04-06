# Scaling Worker Pools

## Key Concepts

- `--type merge` replaces the **entire** `workers` array — include ALL pools
- `--type=json` patches specific fields — safer for targeted changes
- Scaling to 0 keeps control plane running (cost savings)
- Rolling update: new worker Ready before old one removed

## Scale Single Pool (Merge Patch)

```bash
kubectl patch kdccluster {cluster} -n {namespace} \
  --type merge -p '{"spec":{"workers":[{"name":"workers","replicas":{count}}]}}'
```

## Scale with Multiple Pools (Merge Patch)

MUST include all pools:

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 3, "cpuCores": 2, "memory": "8Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"},
      {"name": "highmem", "replicas": 2, "cpuCores": 4, "memory": "16Gi",
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
kubectl patch kdccluster {cluster} -n {namespace} \
  --type merge -p '{"spec":{"workers":[{"name":"workers","replicas":0}]}}'
```

## Add a New Worker Pool

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type merge -p '{
  "spec": {
    "workers": [
      {"name": "workers", "replicas": 3, "cpuCores": 2, "memory": "8Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"},
      {"name": "gpu-pool", "replicas": 1, "cpuCores": 8, "memory": "32Gi",
       "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2",
       "infrastructureProvider": "kubevirt", "storageType": "datavolume"}
    ]
  }
}'
```
