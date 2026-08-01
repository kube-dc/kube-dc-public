# Examples

These examples lead to complete, supported guides instead of duplicating YAML
that can drift from the product.

A Kube-DC **Project** is the customer workload boundary. Kubernetes implements
it as a backing namespace named `{organization}-{project}`. Use a **Managed Cluster**
when an example needs CRDs, operators, additional namespaces, or cluster-scoped
RBAC.

## Start Here

| Goal | Guide |
|------|-------|
| Deploy a small web application | [Deploy Your First Application](deploy-first-app.md) |
| Build a fuller application stack | [Deploy a WordPress Stack](deploy-wordpress-stack.md) |
| Create and connect to a VM | [Create a Virtual Machine](creating-vm.md) |
| Provision PostgreSQL or MariaDB | [Managed Databases](managed-databases.md) |
| Create a Managed Cluster | [Provision a Managed Cluster](provisioning-cluster.md) |

## Networking

| Goal | Guide |
|------|-------|
| Publish HTTP or HTTPS with a hostname | [Service Exposure](service-exposure.md#part-1-gateway-routes) |
| Expose selected TCP or UDP ports | [Service Exposure](service-exposure.md#part-2-eip-based-exposure-both-project-types) |
| Map an address directly to a VM | [External and Floating IPs](public-floating-ips.md) |
| Keep communication private | [Private Networking](private-networking.md) |

An external IP can be cloud-internal or public. Check the address type before
describing an endpoint as internet-accessible.

## Storage and Data

| Goal | Guide |
|------|-------|
| Attach block storage to a Pod or VM | [Block Storage](block-storage.md) |
| Create and use an S3-compatible bucket | [Object Storage](object-storage.md) |
| Configure database backups | [Managed Databases](managed-databases.md#backups) |
| Design a recovery plan | [Data Protection and Recovery](backups-snapshots.md) |

## Security and Automation

| Goal | Guide |
|------|-------|
| Store application secrets | [Secrets Manager](secrets-manager.md) |
| Request a certificate | [Certificate Management](certificate-manager.md) |
| Create an encryption key | [Key Management](kms.md) |
| Deliver from Git | [GitOps](gitops.md) |
| Grant team access | [User and Group Management](team-management.md) |

## Before Applying YAML

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
