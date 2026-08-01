---
name: manage-cluster
description: "Operate a Kube-DC Managed Cluster: download its external kubeconfig, scale or autoscale worker pools, perform supported upgrades, and inspect status."
---

## Prerequisites

- The `KdcCluster` is Ready in a Kube-DC Project.
- Know the Project's backing namespace: `{organization}-{project}`.
- Check Organization and Project quota before adding workers.
- Use values offered by the live dashboard/catalog for versions, worker images,
  infrastructure providers, and storage.

## Access the Managed Cluster

For workstation access, read the dedicated external kubeconfig Secret:

```bash
umask 077
kubectl get secret {cluster}-cp-admin-kubeconfig-external \
  -n {backing-namespace} \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/{cluster}-kubeconfig
chmod 600 /tmp/{cluster}-kubeconfig

kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
```

The Secret exists only when external API exposure is enabled. If it is absent,
use the console to enable exposure or an operator-approved private network path.
Do not rewrite the kubeconfig server field and do not use the internal Kamaji or
Cluster API Secrets as workstation substitutes.

See [kubeconfig-access.md](kubeconfig-access.md).

## Scale a Worker Pool

Inspect the current pool list before patching:

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} \
  -o jsonpath='{.spec.workers}' | jq
```

Patch only the intended replica field:

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

A merge patch replaces the entire `workers` list and can silently discard
pool settings. Use JSON Patch for individual fields. To append a pool, add a
complete pool object at `/spec/workers/-`; copy the provider, storage, and
image shape offered by the platform instead of inventing defaults.

A pool can scale to zero only while another pool has Ready workers. The API
prevents removal of the last Ready worker pool.

See [scale-workers.md](scale-workers.md).

## Autoscale Up

Autoscaling increases `replicas` for unschedulable pods whose requests fit
the pool. It does not remove nodes.

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json -p '[
  {"op":"add","path":"/spec/workers/0/autoscaling",
   "value":{"enabled":true,"minReplicas":2,"maxReplicas":8}}
]'
```

Keep `replicas` within `minReplicas` and `maxReplicas`. Inspect
`.status.workerPools[].autoscaling` for the last scale reason and any limit
such as quota, placement, maximum size, or a rolling update.

## Upgrade Kubernetes

Use the dashboard's version selector as the source of truth. Upgrade one minor
version at a time, never downgrade, and update every worker pool to the catalog
image paired with the target control-plane version.

```bash
kubectl patch kdccluster {cluster} -n {backing-namespace} --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"{target-version}"},
  {"op":"replace","path":"/spec/workers/0/image","value":"{paired-worker-image}"}
]'
```

Include one image operation per worker pool. Do not derive an image tag from the
Kubernetes version; use the exact live catalog value.

See [upgrade-version.md](upgrade-version.md).

## Managed Cluster etcd Encryption

Enable the standard integration on `KdcCluster` rather than creating a key
manually:

```yaml
spec:
  encryption:
    etcd:
      enabled: true
      kekRotation:
        enabled: true
        interval: 90d
```

When no explicit key reference is set, the platform creates and manages
`{cluster}-etcd` in the same Project. Change its rotation policy through the
`KdcCluster` spec. Do not delete or schedule deletion of this key while the
Managed Cluster or its encrypted backups depend on it.

## Monitor and Verify

```bash
kubectl get kdccluster {cluster} -n {backing-namespace} -w
kubectl describe kdccluster {cluster} -n {backing-namespace}
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
```

After scaling, compare Ready nodes with the sum of desired replicas. After an
upgrade, verify both `spec.version` and each node's
`.status.nodeInfo.kubeletVersion`. Completion time depends on images,
placement, draining, and workload disruption budgets.

## Safety

- Never print kubeconfig contents; write them with mode `0600` and remove the
  temporary file after use.
- Check quota before scaling up.
- Use JSON Patch for list entries.
- Keep the last Ready worker pool.
- Use only supported version and image pairs.
- Manage an auto-created etcd KMS key through the `KdcCluster` spec.
