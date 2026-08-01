# Scale Managed Cluster Worker Pools

## Read Before Writing

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} \
  -o jsonpath='{.spec.workers}' | jq
```

Worker pools are a list. A merge patch replaces that whole list. Prefer JSON
Patch so unrelated provider, image, storage, label, taint, drain, and
autoscaling settings remain intact.

## Change a Replica Count

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

Confirm index `0` is the intended pool before applying.

## Append a Pool

Use `/spec/workers/-` and include every field required by the live provider
and catalog:

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json -p '[
  {"op":"add","path":"/spec/workers/-","value":{
    "name":"{pool-name}",
    "replicas":2,
    "cpuCores":{cpu-cores},
    "memory":"{memory}",
    "diskSize":"{disk-size}",
    "image":"{catalog-worker-image}",
    "architecture":"{architecture}",
    "infrastructureProvider":"{provider}",
    "storageType":"{storage-type}"
  }}
]'
```

Copy provider-specific values from an existing pool or the creation dashboard.
Do not hard-code a globally current image or storage type.

## Scale a Pool to Zero

A pool may reach zero only while another pool has Ready workers:

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":0}]'
```

The control plane remains available, but the guard prevents removal of the last
Ready worker pool.

## Verify

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} -w
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
```

Wait for the `KdcCluster` to return to Ready and confirm the expected nodes
are Ready. Provisioning time depends on quota, placement, image availability,
and workload drain behavior.
