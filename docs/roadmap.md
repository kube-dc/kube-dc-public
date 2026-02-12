# Kube-DC Product Roadmap

> **Build Your Own AI & GPU Cloud on Any Server**  
> Transform bare-metal servers into a modern cloud with Kubernetes-native orchestration, GPU sharing, and multi-tenancy.

**Last Updated**: December 9, 2025

---

## Executive Summary

| Milestone | Target Date | Key Deliverable |
|-----------|-------------|-----------------|
| **Installer v2** | Jan 2026 | Single-node & simplified installation |
| **Global Admin UI** | Feb 2026 | Platform-wide administration console |
| **Database as a Service** | Mar 2026 | PostgreSQL, MySQL, MongoDB, Redis |
| **S3 Object Storage** | Apr 2026 | Rook/Ceph multi-tenant buckets |
| **GPU/AI Platform** | May 2026 | HAMI sharing, KubeFlow integration |
| **Billing System** | Jun 2026 | Metering, pricing, usage reports |
| **Licensing** | Jul 2026 | Node-based license management |
| **Hybrid Cloud** | Sep 2026 | Multi-cluster federation, DR |
| **Advanced Networking** | Oct 2026 | VPN, Security Groups, Service Mesh |
| **Edge Computing** | Q1 2027 | Lightweight edge deployments |

---

## Current State (v0.1.35) ✅

### Core Platform — Complete
- **Multi-Tenancy**: Organizations, Projects, Keycloak SSO, RBAC
- **Networking**: Kube-OVN VPC, EIP/FIP, LoadBalancer, multi-network support
- **Virtualization**: KubeVirt VMs, Linux/Windows support, VNC, SSH injection
- **KaaS**: Multi-tenant control planes (Kamaji), KubeVirt/CloudSigma workers, Cilium CNI
- **Observability**: Prometheus metrics, Loki logging, VM monitoring charts
- **UI**: Web console, VM lifecycle, monitoring dashboards

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
