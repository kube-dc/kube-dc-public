# Upgrade a Managed Cluster

## Constraints

- Upgrade one Kubernetes minor version at a time.
- Downgrades are not supported.
- Update the control-plane version and every worker pool's image together.
- Use the exact version/image pair offered by the live Kube-DC dashboard or
  catalog.
- Do not infer the worker image tag from the Kubernetes version.

## Procedure

1. Record the current version and pools:

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} \
  -o jsonpath='{.spec.version}{"\n"}{.spec.workers}{"\n"}'
```

2. Choose the next supported minor version and its paired worker image.

3. Patch all fields in one JSON Patch:

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"{target-version}"},
  {"op":"replace","path":"/spec/workers/0/image","value":"{paired-worker-image}"},
  {"op":"replace","path":"/spec/workers/1/image","value":"{paired-worker-image}"}
]'
```

Remove or add worker-image operations to match the actual number of pools.

4. Monitor the rolling operation:

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} -w
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes -w
```

5. Verify the declared version and the kubelet version on every node:

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} \
  -o jsonpath='{.spec.version}{"\n"}'
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes \
  -o custom-columns='NAME:.metadata.name,VERSION:.status.nodeInfo.kubeletVersion,READY:.status.conditions[?(@.type=="Ready")].status'
```

Do not promise zero disruption. Availability depends on replica counts,
PodDisruptionBudgets, storage topology, and application scheduling.
