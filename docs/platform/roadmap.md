# Product Direction

This page describes product maturity and direction without promising release
dates. Deployment capabilities still depend on the enabled Fleet components,
infrastructure, and storage or network topology.

For operational truth, use the feature's documentation and the version pinned
in your Fleet repository. A roadmap entry is not an API or support contract.

## Capability status

| Area | Status | Current scope |
|---|---|---|
| Organizations and Projects | Available | Identity, Project RBAC, quota hierarchy, and isolated Project VPCs |
| Virtual machines | Available | Linux and Windows VM lifecycle, images, console access, and storage profiles |
| Managed Clusters | Available | Kamaji control planes, supported worker providers, upgrades, etcd backup, and optional etcd encryption |
| Managed databases | Available | PostgreSQL and MariaDB workflows documented in the cloud guide |
| Object storage | Available when configured | Per-Project S3-compatible buckets backed by Rook Ceph |
| External networking | Available when configured | Cloud/public EIPs, FIPs, LoadBalancer Services, Gateway routes, and delegated datacenter VLANs |
| Observability | Available when configured | Organization views over Project metrics, logs, alerts, and dashboards |
| Billing and quota | Available | Quota-only, Stripe, and WHMCS operating modes; plans and add-ons from platform configuration |
| Managed security services | Available when configured | Managed secrets, certificates, KMS keys, and private-CA integration through OpenBao |
| GPU workloads | Pilot | Gated shared-Pod and dedicated-device workflows; hardware and profile support is deployment-specific |
| Bare-metal worker provisioning | Deployment-specific | Metal3 workflows for qualified hardware and networks |

**Available** means the implementation and operator workflow exist. It does not
mean every installation enables the component. **Pilot** means operators must
qualify hardware, versions, isolation controls, and rollback behavior before
offering the capability broadly.

## Current product priorities

The project is concentrating on:

- making installation, upgrade, backup, and recovery workflows repeatable
  through the CLI and Fleet GitOps model;
- strengthening Project and external-network isolation, admission policy, and
  auditability;
- improving the Managed Cluster lifecycle, worker-provider coverage, and
  disaster-recovery evidence;
- turning GPU support from qualified pilots into explicit, supportable hardware
  profiles;
- improving usage attribution and operational reporting without weakening quota
  enforcement;
- keeping the console, CLI, API, and documentation aligned around Organization,
  Project, and Managed Cluster terminology.

## Longer-term exploration

These areas are exploratory and should not be treated as committed features:

- multi-site federation and cross-site disaster recovery;
- network peering, VPN gateways, and higher-level security-group workflows;
- disconnected installation and broader edge or ARM deployment profiles;
- workload migration from external virtualization platforms;
- additional managed data services and policy-driven automation.

## How status changes

A capability should move from exploratory to pilot, or from pilot to available,
only when it has:

1. a documented user and operator journey;
2. versioned configuration and API behavior;
3. automated validation for its supported topology;
4. observable failure states and a tested rollback or recovery path;
5. explicit security, data-protection, and support boundaries.

Track implementation work and provide feedback through
[GitHub Discussions](https://github.com/kube-dc/kube-dc-public/discussions).
