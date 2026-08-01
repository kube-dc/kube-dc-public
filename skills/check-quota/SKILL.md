---
name: check-quota
description: Check Kube-DC Organization and Project quota before creating workloads, VMs, databases, Managed Clusters, public IPs, or storage.
---

# Check Quota

Quota is governed at the Organization level and can be narrowed for an
individual Project. Do not embed plan names, prices, or limits in automation;
read the active values from status or the billing console.

## 1. Confirm context and scope

```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
```

A Project context should name the selected Project and set its backing namespace.
Use `kube-dc use {domain}/{organization}/{project}` if they do not agree.

## 2. Read Organization quota

```bash
kubectl -n {organization} get organization {organization} -o jsonpath='{.status.quotaUsage}' | jq .
```

The status can contain:

- `cpu`, `memory`, `storage`, and `pods` with `used` and `hard` values;
- `publicIPv4`, counted across the Organization's Projects;
- `objectStorage`;
- `accelerators` when GPU profiles are configured;
- `lastUpdated`, the observation timestamp.

CPU is normalized to cores and memory/storage to GiB. For CPU and memory, the
reported pair follows whichever requests or limits axis is closest to its cap.

An empty `objectStorage.used` value means usage is not available from the
in-cluster controller. It is not zero; consult the provider's object-storage
usage surface before provisioning a large bucket.

## 3. Read Project quota

```bash
kubectl -n {organization} get project {project} -o jsonpath='{.status.quotaUsage}' | jq .
```

`perProjectQuotaSet: true` means an explicit `project-quota` ResourceQuota is
active. Otherwise, the Project status reflects the inherited Organization
limit. Organization usage is still the sum across Projects, so headroom shown
on one Project is not reserved for it.

Compare all Projects when deciding where capacity is being consumed:

```bash
kubectl -n {organization} get projects -o custom-columns='PROJECT:.metadata.name,CPU:.status.quotaUsage.cpu.used,MEMORY:.status.quotaUsage.memory.used,STORAGE:.status.quotaUsage.storage.used,PODS:.status.quotaUsage.pods.used,UPDATED:.status.quotaUsage.lastUpdated'
```

## 4. Inspect Kubernetes enforcement state

Status is a normalized product view. When a create is being rejected, inspect
the ResourceQuota objects the API server is enforcing in the Project's backing
namespace:

```bash
kubectl -n {project-backing-namespace} get resourcequota
kubectl -n {project-backing-namespace} describe resourcequota
```

The expected sources are:

| ResourceQuota | Meaning |
|---|---|
| `hrq.hnc.x-k8s.io` | Organization quota propagated into the Project |
| `project-quota` | Optional tighter Project cap |

This view covers native Kubernetes resources such as CPU, memory, storage,
pods, and configured accelerator resource names. Public IPv4 and object-storage
accounting use separate platform/provider paths and do not appear as ordinary
ResourceQuota dimensions.

## 5. Estimate the request

Add the workload's requested capacity, not only its apparent idle usage:

- Deployment or StatefulSet: replica count multiplied by each container request.
- VM: vCPU, memory, and requested disk size.
- Database: every database instance or replica plus its storage.
- Managed Cluster: control-plane and worker-pool resources.
- Public exposure: each new public EIP that will be allocated.
- GPU: every quota dimension reported for the selected live profile.

Leave operational headroom for rollouts, node maintenance, database failover,
and autoscaling. A workload at exactly the hard limit can fail when Kubernetes
temporarily creates a replacement Pod.

## Troubleshoot quota rejection

A typical API error names the ResourceQuota and exhausted resource. Capture it,
then inspect both scopes:

```bash
kubectl -n {organization} get organization {organization} -o yaml
kubectl -n {organization} get project {project} -o yaml
kubectl -n {project-backing-namespace} describe resourcequota
```

Resolve the exhausted dimension by deleting unused resources, reducing the
request, moving work only when governance permits, or requesting a quota or
plan change from the Organization administrator. Do not retry unchanged
manifests in a loop.

## Safety

- Treat `lastUpdated` as an observation timestamp and use ResourceQuota for a
  current admission failure.
- Do not treat an absent or empty usage field as zero.
- Check shared Organization headroom as well as a Project cap.
- Confirm public IPv4 and GPU entitlement separately before creating those
  resources.
