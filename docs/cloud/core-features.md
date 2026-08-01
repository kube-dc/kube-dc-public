# Platform Capabilities

Kube-DC gives teams a governed place to run applications, virtual machines,
databases, and Managed Clusters without exposing the platform cluster itself.

The customer model is:

```text
Organization
└── Project
    ├── Applications
    ├── Virtual machines
    ├── Managed databases
    └── Managed Clusters
```

An **Organization** owns identity, billing, and shared quota. A **Project** is
the day-to-day workload boundary. In Kubernetes, that Project is implemented as a
backing namespace named `{organization}-{project}`, with Project RBAC and isolated VPC
networking. A **Managed Cluster** is different: it has its own Kubernetes API
and is the right place for CRDs, operators, multiple namespaces, and
cluster-scoped administration.

See [Core Concepts](core-concepts.md) for the complete resource model.

## Projects

A Project provides a focused Kubernetes environment for application teams:

- Deployments, StatefulSets, Jobs, Services, and autoscaling
- KubeVirt virtual machines
- Persistent and object storage
- Managed PostgreSQL and MariaDB
- Project-scoped identities, roles, secrets, certificates, and encryption keys
- Cloud or public gateway networking with explicit inbound exposure
- Optional Project quota within the Organization's shared plan

Project users cannot create namespaces, CRDs, ClusterRoles, or StorageClasses.
Container exec and attach are blocked, CronJobs are read-only, and
NetworkPolicy is not a self-service Project control. These boundaries protect
the shared platform.

Read [Projects](kubernetes-projects.md) before adapting a Helm chart
or operator for a Project.

## Managed Clusters

A Managed Cluster gives you a separate Kubernetes API and control plane while
Kube-DC operates its lifecycle. Use one when your workload needs:

- CRDs or Kubernetes operators
- Multiple namespaces
- ClusterRoles or other cluster-scoped resources
- Scheduled controllers such as GitOps agents
- Kubernetes version and worker-pool control
- Privileged platform software that cannot run in a shared Project

Managed Cluster workers run as virtual machines inside the parent Project and
consume its quota. Start with [Provision a Managed Cluster](provisioning-cluster.md).

## Applications

Projects support ordinary container workloads and Helm charts that stay within
the Project boundary. Kube-DC applies resource defaults when a container omits
requests or limits, and the Project network keeps private workload traffic
isolated.

- [Deploy Your First Application](deploy-first-app.md)
- [Service Exposure](service-exposure.md)
- [GitOps](gitops.md)

## Virtual Machines

KubeVirt provides VM lifecycle, console access, persistent disks, and Project
networking alongside container workloads. Public access remains explicit: use a
LoadBalancer Service for selected ports or a Floating IP for direct VM access.

- [Create a Virtual Machine](creating-vm.md)
- [Connect to a Virtual Machine](connecting-vm.md)
- [VM Lifecycle](vm-lifecycle.md)

## Managed Databases

Kube-DC can provision and operate PostgreSQL and MariaDB inside a Project.
Database configuration can include replication, scheduled backups, restore
workflows, and Project-scoped credentials. Availability depends on the replica
count and the application connection strategy; a single replica is not highly
available.

See [Managed Databases](managed-databases.md).

## Networking

Every Project has an isolated VPC and a gateway EIP. The Project network type
selects the default external address pool; it does not decide which workload
types the Project can run.

Use:

- Gateway routes for hostname-based HTTP, HTTPS, or TLS passthrough
- LoadBalancer Services for selected TCP or UDP ports
- Floating IPs for one-to-one VM address mapping

An EIP may be cloud-internal or public. Do not assume that every external
address is reachable from the internet.

- [Networking Overview](networking-overview.md)
- [Service Exposure](service-exposure.md)
- [External and Floating IPs](public-floating-ips.md)

## Storage and Data Protection

Projects can use block storage for VMs and containers and S3-compatible object
storage for application data. Backup behavior belongs to the service that owns
the data: managed database backup, Managed Cluster etcd snapshots, and
application-level file or object backup are separate workflows.

- [Block Storage](block-storage.md)
- [Object Storage](object-storage.md)
- [Data Protection and Recovery](backups-snapshots.md)

## Identity and Security

Organization membership controls who can see the environment. Organization
Groups grant a standard or custom Project role to a team. Project admission
policies then enforce the shared-platform boundary independently of the UI.

- [User and Group Management](team-management.md)
- [Security Restrictions](security-restrictions.md)
- [Secrets Manager](secrets-manager.md)
- [Key Management](kms.md)
- [Certificate Management](certificate-manager.md)

## Billing and Quota

An Organization's plan is shared across its Projects. Organization admins can
add a Project cap when one team needs a smaller budget, but Project users cannot
edit the platform-managed ResourceQuota objects directly. Plan values and
optional capabilities can vary by installation; use the console's Billing page
as the source of truth.

See [Billing and Usage](billing-usage.md).

:::info Product status
A page should describe a capability as available only when it is exposed in the
current console or documented API. Preview, Pilot, provider-specific, and
roadmap capabilities must be labeled explicitly.
:::
