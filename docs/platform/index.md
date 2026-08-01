# Kube-DC Platform

Kube-DC is a Kubernetes-based platform for Organizations to run virtual
machines, containers, managed data services, object storage, and Managed
Clusters. This section is for the operators who install, secure, and
run the management cluster.

## Start here

| Goal | Documentation |
|---|---|
| Understand the product model | [Architecture overview](architecture-overview.md) |
| Plan an installation | [Installation overview](installation-overview.md) |
| Install the platform | [Installation guide](installation-guide.md) |
| Understand Organizations, Projects, and access | [Multi-tenancy and access control](architecture-multi-tenancy.md) |
| Design provider and Project networks | [Networking architecture](architecture-networking.md) |
| Operate internal platform endpoints | [Internal platform endpoints](internal-platform-endpoints.md) |
| Review platform controls and trust boundaries | [Security model](security-model.md) |
| Operate metrics, logs, alerts, and dashboards | [Observability](observability.md) |

## Product vocabulary

- An **Organization** is the tenant boundary for identity, membership, billing,
  shared quota, and policy.
- A **Project** is the governed workload boundary inside an Organization.
  Kubernetes implements it with a backing namespace named
  `{organization}-{project}` and a dedicated Kube-OVN VPC.
- A **Managed Cluster** is a separate Kubernetes API and control plane created
  from a Project. It has its own authorization boundary.

Use *backing namespace* only when an operator must work with the underlying
Kubernetes object. Use *cluster* only for the management cluster or a Managed
Cluster.

## Community

- [GitHub](https://github.com/kube-dc/kube-dc-public)
- [Slack](https://join.slack.com/t/kube-dc/shared_invite/zt-31mr5c6ci-W3kYQ7qGDULlGQ5QJjsxmA)
- [Cloud user guide](/)
