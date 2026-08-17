---
title: Networking, VLAN attachment, and BGP
slug: networking
hide_title: true
description: Project VPCs, public exposure, floating addresses, physical VLAN attachment, and operator-managed BGP routing.
---

import DatasheetFigure from '@site/src/components/DatasheetFigure';
import {VlanAllocationDiagram} from '@site/src/components/Diagram/CloudFlowDiagrams';
import RoutedNetworkDiagram from '@site/src/components/Diagram/RoutedNetworkDiagram';

# DRAFT — Kube-DC Function Datasheet: Networking, VLAN attachment, and BGP

> 🚧 **Working draft — not for distribution.** Companion to the
> [platform datasheet](draft-artifact-a-datasheet.md). Claims trace to the
> [claim ledger](claim-ledger-a-cloud.md) and published product docs;
> publication gates per [datasheet-plan.md](datasheet-plan.md).

---

# Networking, VLAN attachment, and BGP

**Give each project an isolated VPC, with controlled paths to public services
and existing datacenter networks.**

Kube-DC creates a VPC for each project and connects it to the uplinks, address
space, VLANs, and routed networks managed by your datacenter team. Tenants can
expose services and use delegated connectivity without gaining control of the
underlying network fabric.

## Per-project VPCs

Every project receives its own VPC and private subnet at creation. The
software-defined network does not configure private routes between projects by
default. A service becomes reachable from another project only when it is
deliberately exposed or the operator adds a route. Pods, VMs, and
managed-cluster workers in the same project share the project network and can
reach one another directly.

## Address model

| Object | What it does |
|---|---|
| **Shared egress IP** | Every project gets outbound connectivity through a shared external address |
| **External IP (EIp)** | A dedicated external address, allocated automatically per LoadBalancer service or explicitly for a purpose |
| **Floating IP (FIp)** | Maps an external address to an individual VM — inbound and outbound — without a load balancer |

Address pools are defined by the platform team from your ranges; tenants
consume them within quota.

## Exposing services

Two documented paths, chosen per service:

- **HTTPS route (recommended for web/API/gRPC):** one annotation on a
  `LoadBalancer` Service publishes it at a hostname with a TLS
  certificate issued by the platform's configured issuer — no ingress
  controller for tenants to run, no certificate handling. Uses the
  platform's gateway, your routable address pools and DNS.
- **Direct LoadBalancer (any TCP/UDP):** the Service receives a dedicated
  external IP for protocols and ports beyond HTTP.

Managed-cluster API endpoints follow the same model: private in-VPC by
default, public HTTPS through the platform gateway on request.

## VLAN attachment: layer-2 into your datacenter

Where the datacenter fabric trunks delegated VLANs to the platform nodes
and the operator configures the bridge and VLAN pools, a project's network
can be bridged onto an existing datacenter VLAN.

- **Per-organization VLAN pools**: the platform team delegates ranges of
  VLAN IDs to organizations; tenants allocate segments from their pool
  self-service, without tickets and without being able to touch another
  organization's VLANs.
- Workloads — VMs and pods — gain a leg on the physical segment: lab
  instruments, storage networks, legacy systems, partner links, anything
  that expects layer-2 adjacency.
- Addressing on the attached segment is yours: static, your DHCP, or
  platform-managed.

<DatasheetFigure
  alt="Kube-DC VLAN allocation table showing physical segments delegated to an Organization and assigned to Projects"
  caption="Platform operators delegate physical segments to an Organization; its administrators can then assign or reassign those allocations to Projects without changing the underlying fabric."
  src={require('../platform/images/vlan-admin-2-allocations.png').default}
/>

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart LR
  subgraph ORG["Organization — VLAN pool 1700–1799 (delegated)"]
    subgraph PRJ["Project VPC"]
      VM["VM"]
      POD["Pods"]
    end
  end
  PRJ === |"VLAN 1724 — layer-2 bridge<br/>(operator-configured trunking)"| FAB["Datacenter fabric"]
  FAB --- LAB["Lab equipment"]
  FAB --- LEG["Legacy systems"]
  FAB --- LNK["Dedicated links / partners"]
```

</details>

<VlanAllocationDiagram />

## Routed networks: BGP into existing routing domains

Where platform nodes can reach an external router or firewall, Kube-DC can
attach selected project VPCs to an operator-defined routed network. Managed
gateway replicas establish eBGP, advertise only project CIDRs, and accept only
prefixes approved by your platform team. Organization administrators attach a
delegated routed network; peers, ASNs, and route policy remain platform-owned.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart LR
  W["Pods + VMs"] --> VPC["Project VPC router"]
  VPC --> GW["Two managed routing gateways"]
  GW <-->|"eBGP · Project CIDR + approved imports"| EDGE["External router / firewall"]
  EDGE --> REMOTE["Approved remote network<br/>198.51.100.0/24"]
  VPC -. "other destinations" .-> DEFAULT["Existing default gateway"] --> INTERNET["Internet · SNAT unchanged"]
```

</details>

<RoutedNetworkDiagram />

- Two gateway replicas provide active/standby forwarding and failover.
- Route maps, maximum-prefix limits, default-route rejection, and optional
  peer authentication bound what can enter or leave the platform.
- If no healthy gateway has an approved route, that destination fails closed;
  the project's normal Internet egress remains separate.

BGP connectivity is enabled and qualified per deployment because transit
VLANs, addressing, timers, and external-router behavior belong to the
datacenter network design.

## Traffic control

- Egress restrictions and allowlists are operator-configured platform
  controls — tenants request exceptions; the platform team owns the
  policy.
- Network boundaries are a property of the VPC layer: they are not
  tenant-configurable objects inside a project.
- North-south exposure happens through the objects above — an address or
  route a tenant deliberately created.

## Responsibilities

| Concern | Your platform team | Tenant |
|---|---|---|
| Fabric, uplinks, address ranges, VLAN delegation | ✅ | — |
| VPC creation and isolation | ✅ automatic | — |
| Egress policy and allowlists | ✅ | requests exceptions |
| Service exposure (LBs, routes, FIPs) | Mechanisms provided | ✅ configures per service |
| VLAN segment allocation from the org pool | Pool delegation | ✅ self-service within pool |
| BGP peers, approved prefixes and transit network | ✅ | attaches delegated routed networks |
| DNS for published hostnames | Platform zone provided | ✅ own domains via CNAME |

---

## Draft apparatus (stripped at publication)

Evidence: per-project VPC, per-LB EIPs, FIp→VM mapping observed live
(ledger rows 3, 22); HTTPS route + automatic certificate verified in the
application-stack E2E (row 7); VLAN attachment and per-org pools proven on
a customer deployment with published documentation (row 23 — keep the
"by arrangement/deployment-dependent" nature in the platform context: it
requires trunked fabric); managed BGP routing, filtering, HA and fail-closed
behavior verified on kube-dev (row 44). Not claimed: tenant-authored
NetworkPolicies, universal TLS, arbitrary cross-project routing, universal
BFD or ECMP support.
