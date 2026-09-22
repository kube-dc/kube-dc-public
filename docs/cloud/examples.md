# Examples

These examples lead to complete, supported guides instead of duplicating YAML
that can drift from the product.

A Kube-DC **Project** is the customer workload boundary. Kubernetes implements
it as a backing namespace named `{organization}-{project}`. Use a **Managed Cluster**
when an example needs CRDs, operators, additional namespaces, or cluster-scoped
RBAC.

## Start here

| Goal | Guide |
|------|-------|
| Deploy a small web application | [Deploy your first application](deploy-first-app.md) |
| Build a fuller application stack | [Deploy a WordPress stack](deploy-wordpress-stack.md) |
| Create and connect to a VM | [Create a virtual machine](creating-vm.md) |
| Provision a database, cache, or broker | [Managed services](managed-services.md) |
| Create a Managed Cluster | [Provision a Managed Cluster](provisioning-cluster.md) |

## Networking

| Goal | Guide |
|------|-------|
| Publish HTTP or HTTPS with a hostname | [Service exposure](service-exposure.md#part-1-gateway-routes) |
| Expose selected TCP or UDP ports | [Service exposure](service-exposure.md#part-2-eip-based-exposure-both-project-types) |
| Map an address directly to a VM | [External and floating IPs](public-floating-ips.md) |
| Keep communication private | [Private networking](private-networking.md) |

An external IP can be cloud-internal or public. Check the address type before
describing an endpoint as internet-accessible.

## Storage and data

| Goal | Guide |
|------|-------|
| Attach block storage to a pod or a VM | [Block storage](block-storage.md) |
| Create and use an S3-compatible bucket | [Object storage](object-storage.md) |
| Configure database backups | [Backups and restore](postgresql-backup-restore.md) |
| Design a recovery plan | [Data protection and recovery](backups-snapshots.md) |

## Security and automation

| Goal | Guide |
|------|-------|
| Store application secrets | [Secrets Manager](secrets-manager.md) |
| Request a certificate | [Certificate management](certificate-manager.md) |
| Create an encryption key | [Key management](kms.md) |
| Deliver from Git | [GitOps](gitops.md) |
| Grant team access | [User and group management](team-management.md) |

## Before you apply a manifest

1. Select the target Project context with `kube-dc use`.
2. Replace example namespaces with the exact backing namespace shown by the CLI.
3. Review resource requests against Organization and Project quota.
4. Render Helm charts and reject unsupported cluster-scoped resources.
5. Apply the manifest and wait for readiness.
6. Record cleanup commands before creating persistent or billable resources.

```bash
kube-dc use
kubectl diff -f example.yaml
kubectl apply -f example.yaml
kubectl get pods,svc
```

:::warning Project boundary
Do not apply examples that create `Namespace`, CRD, ClusterRole, or
StorageClass objects with a Project kubeconfig. Adapt the chart or run it in a
Managed Cluster.
:::
