# Kube-DC Product Roadmap

> **Build Your Own AI & GPU Cloud on Any Server**  
> Transform bare-metal servers into a modern cloud with Kubernetes-native orchestration, GPU sharing, and multi-tenancy.

**Last Updated**: July 29, 2026 — status refreshed against the shipped release.

!!! warning "Dates below the Current State section are not current"
    The quarterly plan in this page was written in December 2025. Several
    milestones have shipped; the remaining target dates have **not** been
    re-baselined and should be treated as indicative rather than committed.
    For what is shipped today, use the Current State section and the
    [platform documentation](index.md).

---

## Executive Summary

| Milestone | Status | Key Deliverable |
|-----------|--------|-----------------|
| **Installer v2** | ✅ Shipped | GitOps installer — `kube-dc bootstrap init` scaffolds a Flux fleet repository ([guide](installation-guide.md)) |
| **Global Admin UI** | ✅ Shipped | Platform-wide administration console |
| **Database as a Service** | ◧ Partial | PostgreSQL (CloudNativePG) and MariaDB shipped ([docs](/cloud/managed-databases)). MongoDB and Redis not implemented |
| **S3 Object Storage** | ✅ Shipped | Rook/Ceph multi-tenant buckets ([docs](/cloud/object-storage)) |
| **GPU/AI Platform** | ◧ Pilot | Shared GPU via DRA and dedicated passthrough to VM guests, both **pilot**. HAMI and KubeFlow not implemented |
| **Billing System** | ◧ Partial | Quota enforcement and Stripe integration shipped; resource metering and usage reports outstanding |
| **Licensing** | 🔲 Not started | No node-based licensing model exists today |
| **Hybrid Cloud** | 🔲 Planned | Multi-cluster federation, DR |
| **Advanced Networking** | ◧ Partial | Tenant VLAN attachment shipped ([docs](tenant-vlan-attachment.md)); VPN, security groups and service mesh outstanding |
| **Edge Computing** | 🔲 Planned | Lightweight edge deployments |

---

## Current State (v0.5.34) ✅

### Core Platform — Complete
- **Multi-Tenancy**: Organizations, Projects, Keycloak SSO, RBAC, hierarchical namespaces
- **Networking**: Kube-OVN VPC, EIP/FIP, LoadBalancer, Envoy Gateway ingress, tenant VLAN attachment
- **Virtualization**: KubeVirt VMs, Linux/Windows support, VNC, SSH injection, storage tiers, live migration
- **Bare metal**: Metal3-provisioned worker nodes
- **KaaS**: Multi-tenant control planes (Kamaji), KubeVirt/CloudSigma workers, Cilium CNI, staged upgrades, etcd backup and encryption at rest
- **Managed databases**: PostgreSQL (CloudNativePG) and MariaDB, with credential rotation policies
- **Object storage**: S3-compatible per-project buckets on Rook-Ceph
- **Security services**: KMS keys, managed secrets, managed certificates (private CA and ACME)
- **Observability**: tenant-isolated metrics, logs, dashboards and alerts on shared Mimir/Loki/Grafana
- **Billing**: plans, per-organization quotas, Stripe subscription integration
- **UI**: web console, admin console, VM lifecycle, monitoring dashboards
- **Automation**: `kube-dc` CLI, Kubernetes API, GitOps, agent skills for AI IDEs

### Known limitations

- **Backup protects configuration metadata only** on the default `local-path`
  storage backend — VM disk and persistent-volume data are **not** captured.
  Snapshot-capable storage is required for data protection. See
  [Backups & Snapshots](/cloud/backups-snapshots).
- **GPU capabilities are pilot-stage** and gated per cluster.
- **Disconnected (air-gapped) installation is not implemented.** CLI parameters
  exist but are reserved for that work.

---

## 2026 Roadmap

### Q1 2026: Foundation & Administration

#### 📦 Installer v2.0 — *January 2026*
| Feature | Description |
|---------|-------------|
| Single-node Install | All-in-one deployment for dev/small production |
| Simplified Setup | Reduced dependencies, guided installation |
| Air-gapped Support | Offline installation capability |
| Security Hardening | Dynamic secrets, no hardcoded passwords |

#### 🖥️ Global Admin View — *February 2026*
| Feature | Description |
|---------|-------------|
| Platform Dashboard | Cluster-wide resource overview |
| Organization Management | Create/manage all organizations |
| User Administration | Global user and group management |
| System Health | Infrastructure monitoring and alerts |
| Audit Console | Platform-wide audit log viewer |

---

### Q2 2026: Managed Services

#### 🗄️ Database as a Service — *March 2026*
| Database | Features |
|----------|----------|
| **PostgreSQL** | CloudNativePG, auto-failover, continuous backups |
| **MySQL/MariaDB** | Percona Operator, clustering, PITR |
| **MongoDB** | Sharding, replica sets, automated backups |
| **Redis** | Clustering, persistence, sentinel |

**Capabilities**: One-click provisioning, automated backups, connection pooling, performance dashboards

#### 💾 S3 Object Storage — *April 2026*
| Feature | Description |
|---------|-------------|
| Rook/Ceph Backend | Production-grade object storage |
| Multi-tenant Buckets | Per-project isolation |
| IAM Policies | Fine-grained access control |
| Lifecycle Management | Automated data retention |

#### 🎮 GPU & AI/ML Platform — *May 2026*
| Feature | Description |
|---------|-------------|
| GPU Passthrough | Full Nvidia GPU to VMs/pods |
| HAMI Integration | Fractional GPU sharing |
| vGPU Support | Virtual GPUs for multi-tenant |
| KubeFlow | ML pipeline orchestration |
| LLM Serving | Model inference infrastructure |
| Vector Databases | AI-native data stores |

---

### Q3 2026: Monetization & Operations

#### 💰 Billing System — *Quota: Done ✅ | Metering: June 2026*
| Feature | Status | Description |
|---------|--------|-------------|
| Quota Enforcement | ✅ Done | HRQ + LimitRange + EIP + S3 per plan, addons, suspension lifecycle |
| Billing Provider Decoupling | ✅ Done | `BILLING_PROVIDER` feature flag (none/stripe/whmcs) |
| Stripe Integration | ✅ Done | Checkout, webhooks, portal, subscription CRUD |
| Plans from ConfigMap | ✅ Done | Live-reloadable plan definitions, no restart needed |
| E2E Quota Tests | ✅ Done | 6 tests: create, update, suspend, delete, addons, no-plan |
| Resource Metering | 🔲 Planned | CPU, memory, storage, GPU, network usage tracking |
| Usage Reports | 🔲 Planned | Detailed analytics, export, cost attribution |
| WHMCS Integration | 🔲 Planned | Alternative billing provider support |

#### 🔐 Licensing — *July 2026*
| Feature | Description |
|---------|-------------|
| License Manager | Node-based licensing |
| Feature Gates | License-controlled features |
| Usage Tracking | Compliance reporting |
| Trial Mode | Time-limited evaluations |

#### 📊 UI Enhancements — *August 2026*
| Feature | Description |
|---------|-------------|
| KaaS Console | Cluster creation wizard |
| DBaaS Console | Database management UI |
| Storage Console | S3 bucket management |
| Billing Dashboard | Cost visibility and reports |

---

### Q4 2026: Enterprise Integration

#### ☁️ Hybrid Cloud — *September 2026*
| Feature | Description |
|---------|-------------|
| Multi-Cluster Federation | Unified management across sites |
| Cloud Bursting | Extend to AWS/Azure/GCP |
| Disaster Recovery | Cross-site replication |
| Backup Services | Automated VM/container backups |
| VMware Migration | CDI import, vjailbreak, wizard |

#### 🌐 Advanced Networking — *October 2026*
| Feature | Description |
|---------|-------------|
| Network Peering | Cross-project connectivity |
| VPN Gateway | Site-to-site VPN |
| Security Groups | Stateful firewall rules |
| Service Mesh | Istio/Linkerd integration |
| DNS Management | Custom domains, auto-DNS |

---

## 2027 Roadmap

### Q1 2027: Edge & Advanced Automation

#### 📱 Edge Computing — *Q1 2027*
| Feature | Description |
|---------|-------------|
| Edge Clusters | Lightweight K3s deployments |
| Edge-to-Core Sync | Data synchronization |
| Offline Mode | Disconnected operations |
| ARM Support | Raspberry Pi, Jetson devices |

#### 🤖 Advanced Automation — *Q2 2027*
| Feature | Description |
|---------|-------------|
| Self-Healing | Automated remediation |
| Predictive Scaling | AI-driven autoscaling |
| GitOps Native | ArgoCD/Flux integration |
| Policy as Code | OPA/Kyverno policies |

---

## Feature Timeline

```
2025 Dec     2026 Jan    Feb    Mar    Apr    May    Jun    Jul    Aug    Sep    Oct    2027 Q1
  │            │         │      │      │      │      │      │      │      │      │        │
  ▼            ▼         ▼      ▼      ▼      ▼      ▼      ▼      ▼      ▼      ▼        ▼
Current    Installer  Admin  DBaaS   S3   GPU/AI Billing License  UI   Hybrid Network   Edge
 State        v2      View                                        UX    Cloud
```

---

## Success Metrics

| Metric | Target |
|--------|--------|
| Time to First VM | < 2 minutes |
| Time to First K8s Cluster | < 5 minutes |
| Platform Uptime | 99.9% |
| VM Boot Time | < 60 seconds |
| API Response Time | < 200ms (p95) |

---

**Document Owner**: Kube-DC Product Team  
**Feedback**: [GitHub Discussions](https://github.com/kube-dc/kube-dc-public/discussions)
