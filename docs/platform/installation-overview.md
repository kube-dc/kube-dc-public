# Kube-DC Installation Overview

Kube-DC transforms bare-metal servers into a fully managed cloud platform. This page covers the reference architecture, hardware and network prerequisites, and the high-level deployment workflow. For step-by-step instructions, proceed to the [Installation Guide](installation-guide.md).

## Reference Architecture

A production Kube-DC deployment consists of **three server nodes** forming a highly available management cluster, plus an optional **bastion host** for out-of-band administration.

```
                         Internet
                            │
                    ┌───────┴───────┐
                    │  Provider GW  │  (public IPv4 subnet)
                    └───────┬───────┘
                            │
              ┌─────────────┼─────────────┐
              │             │             │
         master-1      master-2      master-3
       (control-plane) (control-plane) (control-plane)
              │             │             │
   ┌───────── │─────────────│─────────────│──── Management VLAN ──────┐
   │          │             │             │                           │
   │     ┌────┴────┐   ┌────┴────┐   ┌────┴────┐                      │
   │     │ RKE2    │   │ RKE2    │   │ RKE2    │                      │
   │     │ Kube-DC │   │ KubeVirt│   │ Kamaji  │                      │
   │     │ OVN-DB  │   │ OVN-DB  │   │ OVN-DB  │              [bastion]
   │     └─────────┘   └─────────┘   └─────────┘              (optional)
   │          │             │             │                           │
   └───────── │─────────────│─────────────│───────────────────────────┘
              │             │             │
   ┌───────── │─────────────│─────────────│──── Cloud VLAN ──────────┐
   │     Project VPCs, NAT gateways, VM traffic                      │
   └─────────────────────────────────────────────────────────────────┘
              │             │             │
   ┌───────── │─────────────│─────────────│──── Provider VLAN ───────┐
   │     Public IPs: EIPs, FIPs, Service LoadBalancers               │
   │     MetalLB floating IP for Envoy Gateway (HA)                  │
   └─────────────────────────────────────────────────────────────────┘
```

All three nodes run both the Kubernetes control plane and Kube-DC workloads. Worker nodes can be added later — either manually or via **Metal3 bare-metal provisioning** — for additional compute capacity.

## Hardware Prerequisites

| Role | Qty | CPU | RAM | Storage | Notes |
|------|-----|-----|-----|---------|-------|
| **Server node** | 3 | 16+ cores | 64+ GB | 500 GB+ SSD | Control plane + workloads |
| **Bastion** | 0–1 | 2+ cores | 4+ GB | 50 GB | SSH jump host, administration |

**Minimum for evaluation:** 3 nodes with 8 cores / 32 GB each.
**Operating system:** Ubuntu 24.04 LTS on all nodes.

Each server node must have at least **one VLAN-capable network interface** connected to a switch (or virtual switch) that carries the three required VLANs.

## Network Architecture

Kube-DC requires **three isolated Layer 2 networks**, typically delivered as VLANs on a shared trunk interface:

| Network | Example CIDR | VLAN | Purpose |
|---------|-------------|------|---------|
| **Management** | `192.168.0.0/18` | native or tagged | Node-to-node communication, Kubernetes API, etcd, bastion access |
| **Cloud** | `10.64.0.0/16` | tagged | Internal cloud fabric — project VPCs, NAT gateways, VM-to-VM traffic |
| **Provider** | `<your-public-subnet>/24` | tagged | Public IPv4 — EIPs, Floating IPs, Service LoadBalancers |

### Management Network

The management network carries all Kubernetes cluster traffic:

- **RKE2 API server** (port 6443, 9345)
- **etcd** replication between control-plane nodes
- **OVN Southbound/Northbound** databases
- **SSH** access from the bastion host

Assign a static IP to each node on this network. Do **not** use DHCP — static addressing is required for stable etcd and OVN operation.

### Cloud Network (Kube-OVN Underlay)

The cloud network is an underlay VLAN managed by Kube-OVN. It provides:

- **Per-project VPC isolation** — each project gets its own virtual subnet
- **NAT gateways** — projects share outbound connectivity via OVN NAT
- **VM traffic** — KubeVirt VMs communicate over this fabric

This is a **large private range** (e.g., `/16`) because every project VPC, NAT gateway, and logical router port consumes addresses from it.

### Provider Network (Public IPv4)

The provider network is a **routable public subnet** obtained from your ISP, data center, or hosting provider. Kube-DC uses it for:

- **External IPs (EIPs)** — dedicated public IPs assigned to projects
- **Floating IPs (FIPs)** — portable IPs that can move between VMs and services
- **Service LoadBalancers** — Kubernetes `LoadBalancer` services get IPs from this range
- **MetalLB floating IP** — a single IP for Envoy Gateway HA across all three nodes

:::tip
Both the cloud and provider networks share the **same physical interface** via different VLAN IDs. Kube-OVN automatically creates VLAN subinterfaces and manages OVS bridges.
:::

### Network Diagram

```
Physical Server NIC (e.g., eth1)
├── VLAN 100 (Management)  ─── 192.168.0.0/18   ─── Node IPs, Kubernetes API, etcd
├── VLAN 200 (Cloud)       ─── 10.64.0.0/16     ─── Kube-OVN underlay, project VPCs
└── VLAN 300 (Provider)    ─── x.x.x.0/24       ─── Public IPs for EIP/FIP/LB
```

## Components Deployed by the Installer

The Kube-DC installer ([Cluster.dev](https://cluster.dev)) automates the deployment of all platform components on top of an existing RKE2 cluster:

| Component | Version | Purpose |
|-----------|---------|---------|
| **Kube-OVN** | v1.15.0 | Core CNI — overlay/underlay networking, VPC isolation, NAT gateways |
| **Multus CNI** | v4.1.0 | Multi-interface support for pods and VMs |
| **KubeVirt** | v1.6.0 | Virtual machine management on Kubernetes |
| **KubeVirt CDI** | v1.62.0 | VM disk image import and management |
| **Keycloak** | 24.x | Identity provider — SSO, OIDC, multi-tenant realms |
| **Cert-Manager** | v1.14.x | Automatic TLS certificate provisioning (Let's Encrypt) |
| **Envoy Gateway** | v1.2.x | API gateway — HTTPS ingress, routing, rate limiting |
| **Kamaji** | 1.0.0 | Managed Kubernetes control planes (tenant clusters) |
| **Cluster API** | v1.8.x | Declarative cluster lifecycle management |
| **Sveltos** | v0.57.x | GitOps-style addon management for tenant clusters |
| **Kyverno** | v1.15.x | Policy engine — admission control, resource validation |
| **HNC** | v1.1.0 | Hierarchical namespaces for multi-tenancy |
| **Prometheus + Grafana** | 67.x | Monitoring, alerting, dashboards |
| **Loki + Alloy** | 6.x | Log aggregation and collection |
| **Kube-DC Core** | latest | Management controllers, API, web console |
| **NoVNC** | — | Browser-based VM console access |

## Deployment Phases

The end-to-end installation consists of five phases:

### Phase 1 — Server Preparation

Provision three servers with Ubuntu 24.04 LTS. Configure networking (static IPs on the management VLAN), kernel parameters, and system prerequisites.

### Phase 2 — RKE2 Cluster Bootstrap

Install RKE2 on all three nodes as a highly available control plane (3 server nodes). Apply required node labels (`kube-ovn/role=master`, `kube-dc-manager=true`) and disable the built-in ingress controller (replaced by Envoy Gateway).

### Phase 3 — Kube-DC Deployment

Clone the Kube-DC installer template from the [public repository](https://github.com/kube-dc/kube-dc-public), customize `project.yaml` and `stack.yaml` for your environment, and run `cdev apply`. The installer deploys all components automatically in ~15–20 minutes.

### Phase 4 — Post-Deployment Configuration

After the base installation:

1. **MetalLB** — Deploy MetalLB with a floating public IP for Envoy Gateway HA
2. **Provider network** — Configure ProviderNetwork patches if nodes have different NIC names
3. **External networks** — Create the cloud VLAN and provider VLAN subnets in Kube-OVN
4. **DNS** — Point `*.yourdomain.com` wildcard DNS to the MetalLB floating IP

### Phase 5 — Optional Add-ons

- **Rook Ceph** — S3-compatible object storage
- **SSO** — Google OAuth or other identity provider via Keycloak
- **Metal3** — Bare-metal provisioning for automated worker node scaling
- **Billing** — Stripe integration for usage-based billing

---

For the complete step-by-step deployment walkthrough, proceed to the **[Installation Guide](installation-guide.md)**.
