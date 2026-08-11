---
title: Managed Kubernetes Clusters
slug: managed-kubernetes
sidebar_label: Managed Kubernetes
hide_title: true
description: Hosted Kubernetes control planes, worker pools, autoscaling, upgrades, access and data protection on Kube-DC.
---

import DatasheetFigure from '@site/src/components/DatasheetFigure';
import {ManagedClusterTopologyDiagram} from '@site/src/components/Diagram/CloudTopologyDiagrams';
import {ManagedKubernetesSizingDiagram, ManagedKubernetesUpgradeDiagram} from '@site/src/components/Diagram/DatasheetDiagrams';

# DRAFT — Kube-DC Function Datasheet: Managed Kubernetes Clusters

> 🚧 **Working draft — not for distribution.** Companion to the
> [platform datasheet](draft-artifact-a-datasheet.md); modeled on the
> finished CloudSigma managed-Kubernetes datasheet
> (`kubernetes-cluster/docs/managed-kubernetes-datasheet.md`) with all
> provider-specific content removed. Claims trace to the
> [claim ledger](claim-ledger-a-cloud.md) and operator source; publication
> gates per [datasheet-plan.md](datasheet-plan.md).

---

# Managed Kubernetes Clusters

**Give every team a real Kubernetes cluster — without giving every team a
control plane to operate.**

Kube-DC's managed clusters let your tenants — departments, faculties,
customers — provision full, tenant-administered Kubernetes clusters from
their projects. The platform runs the control planes; tenants own their
clusters' workloads, upgrades and access. Your platform team operates one
system, not one cluster per team.

## 1. Architecture

A managed cluster has two halves:

- **The hosted control plane** runs as managed pods on the platform
  cluster — API server, controller manager, scheduler — with a dedicated
  PKI per cluster. Tenants administer a full Kubernetes API without
  touching a master node. Control planes live on a platform-internal
  network that has no route from tenant networks; tenants reach only the
  API endpoints published to them.
- **Worker pools** run as virtual machines inside the tenant project's own
  VPC — the same isolated network that holds the project's other
  workloads. Workers join the control plane automatically at creation and
  on scale-up.

An optional **dedicated etcd datastore** per cluster (three replicas by
default) keeps a tenant's cluster state on its own instance, with
platform-managed certificate rotation. Clusters can alternatively share a
platform datastore.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart LR
  subgraph PLAT["Platform services — run by your platform team"]
    CP["Hosted control plane pods<br/>API · controller-manager · scheduler<br/>per-cluster PKI"]
    ETCD[("Dedicated etcd<br/>3 replicas by default")]
    CP --- ETCD
  end
  subgraph PRJ["Tenant project VPC"]
    W["Worker pool VMs"]
    OTHER["Project's other workloads<br/>apps · VMs · databases"]
  end
  CP === W
  T["Tenant kubectl / CI"] -- "in-VPC endpoint" --> CP
  P["Public clients"] -- "optional operator-configured<br/>HTTPS endpoint" --> CP
```

</details>

<ManagedClusterTopologyDiagram />

## 2. Provisioning

A cluster is one manifest (or a console form):

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: analytics
  namespace: acme-data-platform
spec:
  version: v1.36.1
  controlPlane:
    replicas: 1            # 2+ = multi-replica topology; availability depends on deployment
  dataStore:
    dedicated: true        # own etcd instance for this cluster
  network:
    serviceCIDR: 10.96.0.0/16
    podCIDR: 10.244.0.0/16
  workers:
    - name: general
      replicas: 3
      cpuCores: 4
      memory: 8Gi
      diskSize: 30Gi
```

The platform provisions the control plane, datastore, worker VMs, cluster
PKI and kubeconfigs, and joins the workers — no further steps. Deleting
the `KdcCluster` tears the cluster down; backup-object lifecycle follows
the configured bucket and retention policy.

## 3. Endpoints and access

- **In-VPC endpoint by default** — the cluster API is reachable from the
  owning project's network.
- **Public HTTPS endpoint on request** — one annotation publishes the API
  at a stable hostname, using the platform's gateway, DNS and certificate
  issuer; the API server certificate is re-issued to include the public
  name without manual PKI work.
- **Kubeconfigs as Kubernetes Secrets** in the owning project: an admin
  kubeconfig for the in-VPC endpoint and an external kubeconfig for the
  public one. Retrieval works from the console, the CLI, or `kubectl` —
  so CI systems can fetch cluster credentials the same way tenants do.
- **Break-glass access** — the admin kubeconfig authenticates
  independently of the platform's SSO; access still depends on endpoint,
  network and control-plane availability.

## 4. Worker pools

- Sized per pool: CPU, memory, disk, OS image, replica count; multiple
  pools per cluster for heterogeneous workloads.
- Worker OS images ship as prepared, versioned images matched to the
  Kubernetes version.
- **Per-pool autoscaling** with `minReplicas`/`maxReplicas` bounds and a
  maximum step per scaling event.
- Rolling operations respect PodDisruptionBudgets during drains, within a
  configurable drain timeout.

<DatasheetFigure
  alt="Managed Kubernetes cluster summary showing Ready status, API endpoint, kubeconfig download, control-plane replicas, worker count and encryption-at-rest status"
  caption="The cluster summary brings endpoint access, control-plane readiness, worker capacity and security status into one tenant-facing view."
  src={require('./img/S-03.png').default}
/>

<DatasheetFigure
  alt="Two Managed Kubernetes worker pools with replica controls and autoscaling enabled between one and four nodes"
  caption="Worker-pool autoscaling is configured independently per pool, including minimum and maximum replicas and whether idle nodes may be removed."
  src={require('./img/S-04.png').default}
/>

## 5. Control-plane and etcd right-sizing

Tenant control planes are right-sized automatically, within configured
bounds and available platform capacity. Both the control-plane pods and
the etcd members carry VerticalPodAutoscalers, enabled by default:

- Resource targets follow actual load between configured floors and
  ceilings — small clusters stay small; growing clusters get API-server
  and etcd headroom automatically.
- Scaling is quorum-aware by design: recommendations are applied in-place
  where possible, and etcd resizing respects member quorum.
  Single-replica datastores use a conservative initial-assignment mode.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart LR
  M["Usage metrics"] --> VPA["Vertical autoscaler<br/>floors ≤ target ≤ ceilings"]
  VPA -- "in-place resize<br/>where possible" --> CP["Control-plane pods"]
  VPA -- "quorum-aware" --> E[("etcd members")]
```

</details>

<ManagedKubernetesSizingDiagram />

## 6. Upgrades

Upgrades are staged and tenant-controlled:

1. The tenant (or platform team, by arrangement) sets the target version.
2. The **control plane upgrades first**, without replacing worker nodes.
3. **Worker pools roll in steps** — new nodes join, old nodes drain and
   leave, pool by pool, with PodDisruptionBudgets honored during drains.

Control plane and workers move separately, so tenants can pause between
stages, validate, and continue. Supported version skew follows upstream
Kubernetes policy.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart LR
  T["Tenant sets<br/>target version"] --> CPU["Control plane<br/>upgrades first"] --> P1["Worker pools roll in steps —<br/>join · drain · leave"] --> V["Tenant validates —<br/>pause or continue"]
```

</details>

<ManagedKubernetesUpgradeDiagram />

## 7. Snapshots and restore

- **Scheduled etcd snapshots** are enabled automatically when the
  project's backup bucket exists (daily by default; schedule and
  retention configurable per cluster), written to the project's
  S3-compatible storage.
- **On-demand snapshots** at any time — from the console or by triggering
  the snapshot job.
- **Optional envelope encryption** of snapshots with a tenant-scoped key
  reference; without it, snapshots are stored unencrypted.
- **Self-service restore**: pick a snapshot, trigger the restore, and the
  control plane returns with the cluster's API state rolled back to the
  snapshot. The tenant API is unavailable during restore.

Scope and current limits, stated plainly:

- Etcd snapshots protect **Kubernetes API state**; they do not protect
  PersistentVolume contents — protect application data through its owning
  service (database backups, volume snapshots, object storage).
- Restore currently supports **single-replica etcd datastores**;
  multi-replica restore is on the roadmap. For clusters using the default
  three-replica dedicated datastore, restore is an assisted operation
  until then.

## 8. Isolation and security

- Control planes run on a platform-internal network with no route from
  tenant networks; workers and workloads live in the tenant project's VPC.
- Each cluster has its own PKI — certificates are per-cluster, rotated by
  the platform.
- Inside their cluster, tenants hold full cluster-admin: their own RBAC,
  CRDs, operators and admission configuration, independent of the
  platform's project roles.
- The cluster inherits the project's network boundary: reaching a
  cluster's workloads from outside happens only through the exposure the
  tenant configures (LoadBalancer services, HTTPS routes).

## 9. Observability

With the platform's observability stack deployed, control-plane logs and
events of managed clusters surface in the owning organization's Grafana —
tenants watch their API servers and controllers without filing a ticket,
and the platform team monitors all control planes centrally. Coverage and
retention are operator-defined.

## 10. Responsibilities — managed Kubernetes clusters

| Concern | Your platform team (via Kube-DC) | Tenant |
|---|---|---|
| Control-plane operation, PKI, datastore | ✅ | — |
| Control-plane/etcd sizing | ✅ automatic, within configured bounds | — |
| Worker pool sizing and autoscaling bounds | — | ✅ |
| Kubernetes version and upgrade timing | Controllers execute the staged rollout | ✅ selects target and timing from supported versions |
| Etcd snapshots | Controllers execute scheduled snapshots | ✅ policy, on-demand runs, restore initiation |
| Workloads, in-cluster RBAC, CRDs, operators | — | ✅ |
| Application data protection | — | ✅ via managed services |
| Network exposure of cluster workloads | Platform provides EIPs/routes | ✅ configures |

---

## Draft apparatus (stripped at publication)

Evidence: hosted-CP provisioning, endpoints, kubeconfigs, VPA-on-by-default,
snapshot + restore verified live on the production deployment (ledger rows
8–14, 27–28); VPA semantics and upgrade staging verified in operator
source; per-pool autoscaling fields verified in the API (scaling behavior
verified on the CloudSigma-provider deployment — generic claim kept to the
feature, not to timings). Not claimed: etcd encryption at rest (backup
encryption only), cross-site DR, restore of multi-replica datastores,
provisioning timings as commitments.

📷 Captures: cluster summary = S-03; worker-pool autoscaling = S-04.
The Grafana control-plane-log view remains a separate observability capture
gap and must not be represented by S-04.
