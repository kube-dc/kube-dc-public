# Kube-DC

<p align="center">
  <img src="docs/cloud/images/logo-readme.png" alt="Kube-DC" width="300">
</p>

<p align="center">
  <strong>A Kubernetes-native cloud platform for governed applications, virtual machines, data services, and Managed Clusters.</strong>
</p>

<p align="center">
  <a href="https://docs.kube-dc.com">Documentation</a> |
  <a href="docs/cloud/first-project.md">Get started</a> |
  <a href="docs/platform/architecture-overview.md">Architecture</a> |
  <a href="#community">Community</a>
</p>

## What Kube-DC Is

Kube-DC gives infrastructure teams one control plane for self-service cloud
resources on Kubernetes. Users work through a web console, the `kube-dc` CLI,
or Kubernetes APIs. Platform operators retain control of identity, quota,
networking, policy, and shared infrastructure.

The product model has three primary levels:

- An **Organization** owns identity, membership, billing, and shared quota.
- A **Project** is the governed workload boundary for a team or environment.
  Kube-DC backs it with a Kubernetes namespace, RBAC, a private VPC, optional
  quota, and a Project kubeconfig.
- A **Managed Cluster** has its own Kubernetes API and control plane.
  Create one inside a Project when a workload needs CRDs, operators, multiple
  namespaces, or other cluster-scoped control.

### Project or Managed Cluster?

| Choose | When you need |
|---|---|
| **Project** | Fast deployment of containers, compatible Helm charts, VMs, managed databases, storage, and platform services |
| **Managed Cluster** | A separate Kubernetes API, cluster-admin control, CRDs, operators, multiple namespaces, or cluster-scoped policy |

See [Projects](docs/cloud/kubernetes-projects.md) for the exact
Project capability boundary.

## Capabilities

- **Identity and governance:** Organizations, Projects, SSO integration,
  Organization Groups, four standard Project roles, and hierarchical quota.
- **Application and VM compute:** containers, compatible Helm workloads,
  KubeVirt virtual machines, and Managed Clusters.
- **Networking:** a private VPC per Project, HTTP/HTTPS Gateway routes, direct
  LoadBalancer services, external and floating IPs, and operator-assigned
  datacenter VLANs.
- **Data services:** managed PostgreSQL and MariaDB, block storage, S3-compatible
  object storage, snapshots, and restore workflows.
- **Security services:** managed secrets, certificates, database credential
  rotation, KMS keys, Project RBAC, and platform policy enforcement.
- **Operations:** resource usage, billing-plan quotas, audit events,
  observability, upgrades, and day-two cluster operations.

Capabilities depend on the installed platform profile and the permissions
assigned to the current user. The documentation calls out operator-only,
preview, and unsupported paths explicitly.

## Documentation

Start with the guide for your role:

- [What is Kube-DC?](docs/cloud/what-is-kube-dc.md)
- [Create your first Project](docs/cloud/first-project.md)
- [Deploy your first application](docs/cloud/deploy-first-app.md)
- [Provision a Managed Cluster](docs/cloud/provisioning-cluster.md)
- [Cloud documentation](docs/cloud/index.md)
- [Platform architecture](docs/platform/architecture-overview.md)
- [Platform installation](docs/platform/installation-overview.md)
- [AI IDE and Agent Skills integration](docs/cloud/ai-ide-integration.md)

The published site is available at [docs.kube-dc.com](https://docs.kube-dc.com).

## Community

Kube-DC is open source. Bug reports, design discussions, and contributions are
welcome.

- [GitHub Issues](https://github.com/kube-dc/kube-dc-public/issues)
- [GitHub Discussions](https://github.com/kube-dc/kube-dc-public/discussions)
- [Community and support guide](docs/cloud/community-support.md)
