import {CloudResourceModelDiagram} from '@site/src/components/Diagram/ResourceModelDiagrams';

# Platform capabilities

Kube-DC gives teams a governed place to run applications, virtual machines,
databases, and Managed Clusters. It does this without exposing the platform
cluster itself.

The customer model is:

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```text
Organization
└── Project
    ├── Applications
    ├── Virtual machines
    ├── Managed services
    └── Managed Clusters
```

</details>

<CloudResourceModelDiagram />

An **Organization** owns identity, billing, and shared quota. A **Project** is
the day-to-day workload boundary. In Kubernetes, a Project is a backing
namespace named `{organization}-{project}`, with Project RBAC and isolated VPC
networking. A **Managed Cluster** is different. It has its own Kubernetes API,
and it is the right place for CRDs, operators, multiple namespaces, and
cluster-scoped administration.

For the complete resource model, see [Core concepts](core-concepts.md).

## Projects

A Project provides a focused Kubernetes environment for application teams:

- Deployments, StatefulSets, Jobs, Services, and autoscaling
- KubeVirt virtual machines
- Persistent and object storage
- Managed services from the provider catalog
- Project-scoped identities, roles, secrets, certificates, and encryption keys
- Cloud or public gateway networking with explicit inbound exposure
- An optional Project quota within the Organization's shared plan

Project users cannot create namespaces, CRDs, ClusterRoles, or StorageClasses.
Container exec and attach are blocked, CronJobs are read-only, and
NetworkPolicy is not a self-service Project control. These boundaries protect
the shared platform.

Read [Projects](kubernetes-projects.md) before you adapt a Helm chart or an
operator for a Project.

## Managed Clusters

A Managed Cluster gives you a separate Kubernetes API and control plane, and
Kube-DC operates its lifecycle. Use one when your workload needs:

- CRDs or Kubernetes operators
- Multiple namespaces
- ClusterRoles or other cluster-scoped resources
- Scheduled controllers such as GitOps agents
- Control of the Kubernetes version and the worker pools
- Privileged platform software that cannot run in a shared Project

Managed Cluster workers run as virtual machines inside the parent Project and
consume its quota. Start with
[Provision a Managed Cluster](provisioning-cluster.md).

## Applications

Projects support ordinary container workloads, and Helm charts that stay inside
the Project boundary. When a container omits requests or limits, Kube-DC
applies resource defaults. The Project network keeps private workload traffic
isolated.

- [Deploy your first application](deploy-first-app.md)
- [Service exposure](service-exposure.md)
- [GitOps](gitops.md)

## Virtual machines

KubeVirt provides VM lifecycle, console access, persistent disks, and Project
networking alongside container workloads. Public access stays explicit. Use a
LoadBalancer Service for selected ports, or a floating IP for direct VM access.

- [Create a virtual machine](creating-vm.md)
- [Connect to a virtual machine](connecting-vm.md)
- [VM lifecycle](vm-lifecycle.md)

## Managed services

Kube-DC provisions and operates services from a provider catalog.
Families can cover databases, caches, message brokers, analytics, and applications.
Each plan sets capacity, topology, backup policy, and allowed operations.
Bindings deliver application credentials as Kubernetes Secrets.
The examples in this guide do not limit the catalog.

Availability depends on the plan's topology and on how your application handles
connections. A single instance is not highly available.

See [Managed services](managed-services.md).

## Networking

Every Project has an isolated VPC and a gateway EIP. The Project network type
selects the default external address pool. It does not decide which workload
types the Project can run.

Use:

- Gateway routes for hostname-based HTTP, HTTPS, or TLS passthrough
- LoadBalancer Services for selected TCP or UDP ports
- Floating IPs for one-to-one VM address mapping

An EIP can be cloud-internal or public. Do not assume that every external
address is reachable from the internet.

- [Networking overview](networking-overview.md)
- [Service exposure](service-exposure.md)
- [External and floating IPs](public-floating-ips.md)

## Storage and data protection

Projects can use block storage for VMs and containers, and S3-compatible object
storage for application data. Backup behavior belongs to the service that owns
the data. Managed database backup, Managed Cluster etcd snapshots, and
application-level file or object backup are separate workflows.

- [Block storage](block-storage.md)
- [Object storage](object-storage.md)
- [Data protection and recovery](backups-snapshots.md)

## Identity and security

Organization membership controls who can see the environment. Organization
Groups grant a standard or custom Project role to a team. Project admission
policies then enforce the shared-platform boundary, independently of the UI.

- [User and group management](team-management.md)
- [Security restrictions](security-restrictions.md)
- [Secrets Manager](secrets-manager.md)
- [Key management](kms.md)
- [Certificate management](certificate-manager.md)

## Billing and quota

An Organization's plan is shared across its Projects. Organization
administrators can add a Project cap when one team needs a smaller budget.
Project users cannot edit the platform-managed ResourceQuota objects directly.
Plan values and optional capabilities vary by installation. Use the console's
Billing page as the source of truth.

See [Billing and usage](billing-usage.md).
