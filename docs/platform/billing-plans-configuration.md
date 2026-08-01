# Billing Plans & Resource Quota Configuration

This guide explains how to configure billing plans, resource quotas, and EIP limits for Kube-DC organizations using the `billing-plans` ConfigMap.

---

## Overview

Kube-DC enforces organization-level resource limits using four mechanisms:

1. **HierarchicalResourceQuota (HRQ)** — Aggregates resource usage across all Project backing namespaces within an Organization. Enforced at Pod scheduling time.
2. **LimitRange** — Provides default CPU/memory requests and limits for containers that don't specify them. Required for HRQ to work correctly.
3. **EIP Quota** — Limits the number of public External IPs an Organization can allocate.
4. **Object Storage Quota** — Manages S3 storage limits via Rook-Ceph `CephObjectStoreUser` quotas.

All four are driven by a single ConfigMap: `billing-plans` in the `kube-dc` namespace.

### Billing Provider Feature Flag

The quota system is **decoupled** from payment collection. The installation has
one active provider. `BILLING_PROVIDER` supplies the bootstrap/default value,
and current installations can persist the selected mode through the platform
billing configuration.

| Value | Behavior |
|-------|----------|
| `none` (default) | **Quota-only mode.** Plans load from ConfigMap, HRQ/LimitRange/EIP quotas enforced. No payment flow. Plan assignment via `kubectl` annotations. |
| `stripe` | Full Stripe integration: checkout sessions, webhooks, customer portal, subscription CRUD. |
| `whmcs` | WHMCS is the billing system of record. The shipped provisioning module sends signed create, change, suspend, unsuspend, and terminate events; purchase actions stay in WHMCS rather than the Kube-DC console. |

When the active provider is `none`:
- `GET /api/billing/config` returns `{ provider: "none", features: { quotas: true, checkout: false, portal: false, ... } }`
- `GET /api/billing/plans`, `/addons`, `/quota-usage`, `/quota-status`, `/organization-subscription` all work normally
- Subscription management endpoints (`POST/PUT/DELETE /organization-subscription`, `/verify-checkout`, `/webhook`, `/customer-portal`) are **not mounted**
- The frontend hides Subscribe/Cancel/Manage Payment buttons automatically

To assign a plan manually without a payment provider:
```bash
kubectl annotate organization/<org-name> -n <org-namespace> \
  billing.kube-dc.com/plan-id=dev-pool \
  billing.kube-dc.com/subscription=active \
  --overwrite
```

### How It Works

```
billing-plans ConfigMap (kube-dc namespace)
        │
        ▼
Organization Controller (watches ConfigMap for changes)
        │
        ├─► HierarchicalResourceQuota (Organization namespace)
        │       └─► Enforced across all child Project backing namespaces
        │
        ├─► LimitRange (Organization namespace)
        │       └─► Propagated by HNC to all Project backing namespaces
        │
        ├─► EIP Quota (checked on EIP creation)
        │
        └─► CephObjectStoreUser (rook-ceph namespace)
                └─► S3 storage quota enforced server-side by Ceph RGW
```

When a billing plan is assigned to an organization (via annotations), the controller:

1. Reads the plan definition from the ConfigMap
2. Computes resource limits (base plan + addons + system overhead + burst ratio)
3. Creates/updates the `plan-quota` HRQ and `default-resource-limits` LimitRange in the organization namespace
4. Creates/updates the `CephObjectStoreUser` in the `rook-ceph` namespace with the plan's `objectStorage` quota

**Live updates:** Editing the ConfigMap emits a change event that queues affected Organizations for reconciliation; no controller restart is required.

---

## Prerequisites

- Hierarchical Namespace Controller (HNC) installed with HRQ support
- HNC configured to propagate `LimitRange` resources (`mode: Propagate`)
- Project backing namespaces configured as children of the Organization namespace through the HNC hierarchy
- (Optional) Rook-Ceph installed for Object Storage (S3) quota enforcement

---

## ConfigMap Reference

Create the ConfigMap in the `kube-dc` namespace:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: billing-plans
  namespace: kube-dc
data:
  plans.yaml: |
    plans:
      <plan-id>:
        requests:
          cpu: "<cpu>"
          memory: "<memory>"
          storage: "<storage>"
        pods: <number>
        servicesLB: <number>
        burstRatio: <float>
        selfService: <boolean>          # optional; false = operator-assignable only
        gpu:                            # optional; WHMCS installations only
          <stable-profile-id>:
            shares: <integer>
            memoryMiB: <integer>
            corePercent: <integer>
        limitRange:
          defaultCPU: "<cpu>"
          defaultMemory: "<memory>"
          defaultRequestCPU: "<cpu>"
          defaultRequestMem: "<memory>"
          maxCPU: "<cpu>"
          maxMemory: "<memory>"
          minCPU: "<cpu>"
          minMemory: "<memory>"
          maxPodCPU: "<cpu>"
          maxPodMemory: "<memory>"
          maxPVCStorage: "<storage>"
          minPVCStorage: "<storage>"
    suspendedPlan:
      cpu: "<cpu>"
      memory: "<memory>"
      pods: <number>
      servicesLB: <number>
    systemOverhead:
      cpuPerProject: <millicores>
      memPerProject: <MiB>
    addons:
      <addon-id>:
        disabled: <boolean>
        cpu: "<cpu>"
        memory: "<memory>"
        storage: "<storage>"
        gpu:
          <stable-profile-id>:
            shares: <integer>
            memoryMiB: <integer>
            corePercent: <integer>
    eipQuota:
      <plan-id>: <number>
```

### Field Reference

#### `plans.<plan-id>`

Each plan defines the base resource allocation for an organization.

| Field | Description | Example |
|-------|-------------|---------|
| `requests.cpu` | Base CPU request quota | `"8"` |
| `requests.memory` | Base memory request quota | `"24Gi"` |
| `requests.storage` | Storage request quota | `"160Gi"` |
| `pods` | Maximum number of pods across all projects | `200` |
| `servicesLB` | Maximum LoadBalancer services | `100` |
| `burstRatio` | Multiplier for CPU and memory limits over requests; `1.0` makes limits equal requests | `1.0` |
| `selfService` | Purchase visibility: `false` hides the plan from tenant purchase APIs while an operator can still assign it, and organizations already on it are unaffected. Omitted means visible. | `false` |
| `gpu.<profile>.shares` | Concurrent shared-GPU workloads included in the tier. **WHMCS installations only** — see [GPU plans](#gpu-plans-whmcs-installations-only). | `1` |
| `gpu.<profile>.memoryMiB` | Aggregate GPU memory budget included in the tier | `8192` |
| `gpu.<profile>.corePercent` | Aggregate GPU compute budget included in the tier | `25` |

> **A plan carrying `gpu` grants is not inert on a cluster without a GPU
> catalog.** If the `gpu-profiles` ConfigMap is absent, or the granted profile is
> not `billingEligible`, the manager rejects the **entire** `plans.yaml` — every
> plan, `suspendedPlan`, `systemOverhead` and `eipQuota` with it — and quota
> enforcement falls back to the last known-good config. Only add `gpu` to a plan
> on a cluster whose catalog is deployed and billing-eligible.

#### `plans.<plan-id>.limitRange`

Default resource values applied to containers that don't specify their own. Without this, pods missing resource requests will be **rejected** by the quota system.

| Field | Description | Example |
|-------|-------------|---------|
| `defaultCPU` | Default CPU limit per container | `"500m"` |
| `defaultMemory` | Default memory limit per container | `"512Mi"` |
| `defaultRequestCPU` | Default CPU request per container | `"250m"` |
| `defaultRequestMem` | Default memory request per container | `"256Mi"` |
| `maxCPU` | Maximum CPU per container | `"4"` |
| `maxMemory` | Maximum memory per container | `"12Gi"` |
| `minCPU` | Minimum CPU per container | `"10m"` |
| `minMemory` | Minimum memory per container | `"16Mi"` |
| `maxPodCPU` | Maximum CPU per pod (all containers) | `"8"` |
| `maxPodMemory` | Maximum memory per pod (all containers) | `"24Gi"` |
| `maxPVCStorage` | Maximum PVC size | `"160Gi"` |
| `minPVCStorage` | Minimum PVC size | `"1Gi"` |

#### `suspendedPlan`

Minimal resources allowed when an organization's subscription is suspended.

| Field | Description | Example |
|-------|-------------|---------|
| `cpu` | CPU request and limit | `"500m"` |
| `memory` | Memory request and limit | `"1Gi"` |
| `pods` | Maximum pods | `10` |
| `servicesLB` | Maximum LoadBalancer services | `0` |

#### `systemOverhead`

Per-project overhead added to the organization's quota to account for system pods (VPC DNS, network agents, etc.).

| Field | Description | Example |
|-------|-------------|---------|
| `cpuPerProject` | Millicores added per project | `100` |
| `memPerProject` | MiB added per project | `128` |

The total overhead is `cpuPerProject × organizationProjectsLimit`. The limit
comes from `MasterConfig.OrganizationProjectsLimit`; when unset, the current
controller default is 50 Projects.

#### `addons`

Resource add-ons that can be attached to an organization via the `billing.kube-dc.com/addons` annotation.

| Field | Description | Example |
|-------|-------------|---------|
| `disabled` | Kill switch: hides the add-on from purchase APIs and prevents its annotation from granting resources. Omitted means enabled. | `true` |
| `selfService` | Purchase visibility: `false` hides the add-on from tenant purchase APIs while still allowing an operator assignment to grant resources. Omitted means visible. | `false` |
| `cpu` | Additional CPU per addon unit | `"4"` |
| `memory` | Additional memory per addon unit | `"8Gi"` |
| `storage` | Additional storage per addon unit | `"40Gi"` |
| `gpu.<profile>.shares` | Concurrent shared-GPU workloads per unit | `1` |
| `gpu.<profile>.memoryMiB` | Aggregate GPU memory budget per unit | `8192` |
| `gpu.<profile>.corePercent` | Aggregate GPU compute budget per unit | `25` |
| `providers.stripe.priceId` | Exact Stripe Price ID; legacy environment lookup remains a fallback | `price_...` |
| `providers.whmcs.configurableOptionName` | Exact, case-sensitive WHMCS quantity option mapped to this stable add-on ID | `Kube-DC Shared NVIDIA V100 8 GB` |

The D-005 pilot SKU uses the same annotation and quantity mechanics as Turbo
add-ons. It ships operator-only and unpriced: installing or upgrading kube-dc
does not assign GPU capacity, and tenants cannot purchase it:

```yaml
addons:
  gpu-v100-shared-8g:
    disabled: false
    selfService: false
    displayName: "Shared NVIDIA V100 — 8 GB"
    description: "1 concurrent GPU workload • 8 GB GPU memory • 25% compute"
    price: 0
    currency: EUR
    providers:
      stripe:
        priceId: ""
      whmcs:
        configurableOptionName: "Kube-DC Shared NVIDIA V100 8 GB"
    gpu:
      nvidia-v100-hami:
        shares: 1
        memoryMiB: 8192
        corePercent: 25
```

Before assigning the SKU, deploy a billing-eligible matching GPU profile. Keep
`selfService: false` during the internal pilot; after the acceptance gates pass,
set the real monthly price and change it to `true` (or remove the field) to
offer self-service purchase. Quantity multiplies every dimension: quantity 2
produces 2 shares, 16384 MiB, and 50% aggregate compute.

WHMCS module 1.3.0 sends signed configurable-option values on activate, change,
and renew. The backend maps only explicitly configured option names, applies
the same add-on quantity/catalog validation as other providers, and runs active
GPU-holder reduction checks before changing annotations. Older module payloads
do not contain `configurableOptions` and preserve existing assignments.

### GPU plans (WHMCS installations only)

GPU can be sold two ways, and which one applies is decided by the billing
provider:

| provider | how GPU is sold |
|----------|-----------------|
| `whmcs` | as a **plan** — a GPU-carrying tier alongside the plain one |
| `stripe`, `none` | as an **add-on** (`gpu-v100-shared-8g` above) |

WHMCS provisions a service by plan id and carries no Kube-DC add-on quantities,
so a WHMCS customer cannot buy the GPU add-on at all — the entitlement has to be
part of the tier. Stripe has no such limit and keeps using the add-on catalog.
Publishing GPU plans on a Stripe installation would be a second, unbillable way
to buy the same entitlement, so it is refused.

The backend enforces the **live** provider (the `billing-config` ConfigMap, which
the console can change without a Helm run):

- GPU plans are withdrawn from `GET /api/billing/plans` and the tenant plan grid;
- tenant purchase routes return `403`;
- superadmin assignment returns `409 GPU_PLAN_PROVIDER_MISMATCH`;
- **lookups are untouched** — an organization already on a GPU plan keeps
  rendering quota, usage and its plan name on any provider. Only new purchases
  and assignments are refused.

#### Seeding the plans from the chart

The chart ships one GPU alternative per tier, disabled by default. They are kept
out of `billing.plans.config` deliberately, because a GPU grant reaching a
cluster without a catalog rejects the whole document (see the warning above):

```yaml
billing:
  provider: whmcs          # required — the render fails on any other provider
  plans:
    gpuVariants:
      enabled: true        # requires gpu.enabled=true as well
      plans:
        dev-pool-gpu: { ... }   # full definition, including limitRange
      eipQuota:
        dev-pool-gpu: 1
    migration:
      version: gpu-plan-variants-v1   # bump to run a new wave
      addMissingPlans:
        - dev-pool-gpu
```

`billing-plans` is seeded **once**: on upgrade the chart preserves the live
`plans.yaml`, so adding a plan to values changes nothing on an existing cluster.
`addMissingPlans` is the migration wave that copies a plan into a live
ConfigMap — it copies only what is **absent**, never replaces an operator-edited
definition, and records the completed `version` as an annotation. It also
back-fills a missing `eipQuota` entry, because a plan absent from that map gets
no public-IPv4 enforcement at all.

The render fails, with the reason named, when: the provider is not `whmcs`,
`gpu.enabled` is false, a variant omits `requests` / `limitRange` / `servicesLB`
/ `gpu`, a granted profile is unknown, disabled or not `billingEligible`, a
variant id collides with an existing plan, or a variant has no `eipQuota` entry.

A GPU alternative should otherwise be **identical** to its base tier — same
`requests`, `pods`, `servicesLB`, `limitRange`, `burstRatio`, `objectStorage`,
`ipv4` and `eipQuota` — so the two read as one choice with a GPU switch rather
than unrelated products. The tenant plan grid relies on that: it shows one half
of the catalog at a time behind a **Standard / With GPU** toggle.

Grants must be exact multiples of the profile's fixed product unit (for the
shipped V100 profile, 8192 MiB and 25% per share), or the manager rejects them.

#### Capacity

A grant is concurrent-use **entitlement, not a capacity reservation**: oversell
and workloads stay Pending rather than failing.

`maxSharesPerDevice` is an upper bound, **not** the sellable number. A share also
consumes the fixed product's memory and compute, and whichever runs out first
binds:

```
shares per device = min( deviceMemoryMiB / shareMemoryMiB,
                         100 / shareCorePercent,
                         maxSharesPerDevice )
sellable shares   = shares per device × number of devices
```

For the shipped V100 profile — an 8192 MiB / 25% product on a 32 GiB / 100%
device — that is `min(4, 4, 10) = 4` per device, so a two-GPU node sells **8**
concurrent shares, not 20. Compute this before pricing the tiers, and check how
many nodes carry the devices: GPUs concentrated on one node mean a drain or
driver upgrade removes all GPU capacity at once.

---

#### `eipQuota`

Maximum number of External IPs (EIPs) per plan.

```yaml
eipQuota:
  dev-pool: 1
  pro-pool: 1
  scale-pool: 3
```

---

## Example ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: billing-plans
  namespace: kube-dc
data:
  plans.yaml: |
    plans:
      dev-pool:
        displayName: "Dev Pool"
        description: "Best for: Sandbox / Dev"
        price: 19
        currency: "EUR"
        recommended: false
        objectStorage: 20
        ipv4: 1
        features:
          - "4 vCPU"
          - "8 GB RAM"
          - "60 GB NVMe Storage"
          - "20 GB Object Storage included"
          - "1 Dedicated IPv4"
          - "Managed Clusters"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "4"
          memory: "8Gi"
          storage: "60Gi"
        pods: 100
        servicesLB: 100
        burstRatio: 1.0
        limitRange:
          defaultCPU: "500m"
          defaultMemory: "512Mi"
          defaultRequestCPU: "100m"
          defaultRequestMem: "128Mi"
          maxCPU: "4"
          maxMemory: "8Gi"
          minCPU: "1m"
          minMemory: "1Mi"
          maxPodCPU: "4"
          maxPodMemory: "8Gi"
          maxPVCStorage: "60Gi"
          minPVCStorage: "10Mi"
      pro-pool:
        displayName: "Pro Pool"
        description: "Best for: Production / Teams"
        price: 49
        currency: "EUR"
        recommended: true
        objectStorage: 100
        ipv4: 1
        features:
          - "8 vCPU"
          - "24 GB RAM"
          - "160 GB NVMe Storage"
          - "100 GB Object Storage included"
          - "1 Dedicated IPv4"
          - "Managed Clusters"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "8"
          memory: "24Gi"
          storage: "160Gi"
        pods: 200
        servicesLB: 100
        burstRatio: 1.0
        limitRange:
          defaultCPU: "500m"
          defaultMemory: "512Mi"
          defaultRequestCPU: "250m"
          defaultRequestMem: "256Mi"
          maxCPU: "8"
          maxMemory: "24Gi"
          minCPU: "1m"
          minMemory: "1Mi"
          maxPodCPU: "8"
          maxPodMemory: "24Gi"
          maxPVCStorage: "160Gi"
          minPVCStorage: "10Mi"
      scale-pool:
        displayName: "Scale Pool"
        description: "Best for: High Load / VDC"
        price: 99
        currency: "EUR"
        recommended: false
        objectStorage: 500
        ipv4: 3
        features:
          - "16 vCPU"
          - "56 GB RAM"
          - "320 GB NVMe Storage"
          - "500 GB Object Storage included"
          - "3 Dedicated IPv4"
          - "Managed Clusters"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "16"
          memory: "56Gi"
          storage: "320Gi"
        pods: 500
        servicesLB: 100
        burstRatio: 1.0
        limitRange:
          defaultCPU: "1"
          defaultMemory: "1Gi"
          defaultRequestCPU: "500m"
          defaultRequestMem: "512Mi"
          maxCPU: "16"
          maxMemory: "56Gi"
          minCPU: "1m"
          minMemory: "1Mi"
          maxPodCPU: "16"
          maxPodMemory: "56Gi"
          maxPVCStorage: "320Gi"
          minPVCStorage: "10Mi"
    suspendedPlan:
      cpu: "500m"
      memory: "1Gi"
      pods: 10
      servicesLB: 0
    systemOverhead:
      cpuPerProject: 100
      memPerProject: 128
    addons:
      turbo-x1:
        displayName: "Turbo x1"
        description: "+4 GB RAM • +2 vCPU (Burst)"
        price: 9
        currency: "EUR"
        cpu: "2"
        memory: "4Gi"
        storage: "20Gi"
      turbo-x2:
        displayName: "Turbo x2"
        description: "+8 GB RAM • +4 vCPU (Burst)"
        price: 16
        currency: "EUR"
        cpu: "4"
        memory: "8Gi"
        storage: "40Gi"
    eipQuota:
      dev-pool: 1
      pro-pool: 1
      scale-pool: 3
```

Apply it:

```bash
kubectl apply -f billing-plans-configmap.yaml
```

---

## How Quotas Are Computed

### HRQ Computation

For an organization with plan `pro-pool`, 1× `turbo-x1` addon, and 3 projects:

```
Base CPU requests:     8    (from plan)
+ Addon CPU:          +2    (turbo-x1 × 1)
+ System overhead:    +0.3  (100m × 3 projects)
= Total requests.cpu:  10.3

Burst ratio:           1.0  (from plan)
limits.cpu = 10.3 × 1.0 = 10.3
```

The resulting HRQ `plan-quota`:

```yaml
spec:
  hard:
    requests.cpu:            "10300m"
    requests.memory:         "29056Mi"   # 24Gi + 4Gi addon + 384Mi overhead
    limits.cpu:              "10300m"
    limits.memory:           "29056Mi"
    requests.storage:        "180Gi"     # 160Gi + 20Gi addon
    pods:                    "200"
    services.loadbalancers:  "100"
```

### Burst Ratio

The burst ratio determines how much `limits` exceed `requests`:

| Plan | Burst ratio | Capacity model |
|---|---:|---|
| Dev Pool | 1.0x | CPU and memory limits equal the advertised request capacity |
| Pro Pool | 1.0x | CPU and memory limits equal the advertised request capacity |
| Scale Pool | 1.0x | CPU and memory limits equal the advertised request capacity |

All shipped plans use fully guaranteed sizing. This keeps HRQ accounting aligned
with the advertised capacity and with KubeVirt workers used by Managed Clusters,
which reserve their requested CPU and memory. Storage, Pod, and LoadBalancer
quotas are never multiplied by `burstRatio`.

Custom plans can set a value greater than `1.0` to let container limits exceed
the request quota. Treat that as an explicit overcommit policy: it does not
increase guaranteed capacity, and it can make the advertised capacity harder to
interpret for VM-heavy workloads.

### LimitRange Behavior

The LimitRange ensures every container has resource requests set, which is **required** by Kubernetes when a ResourceQuota is active:

1. Pod created **without** `resources.requests` → LimitRange applies defaults automatically
2. HRQ admission controller checks aggregated usage against the organization quota
3. Pod admitted if within quota; **rejected** if quota exceeded

The LimitRange is created in the Organization namespace and automatically propagated to all Project backing namespaces by HNC.

---

## Organization Annotations

Plans are assigned to organizations via annotations:

```yaml
apiVersion: kube-dc.com/v1
kind: Organization
metadata:
  name: acme-corp
  namespace: acme-corp
  annotations:
    billing.kube-dc.com/plan-id: "pro-pool"
    billing.kube-dc.com/subscription: "active"
    billing.kube-dc.com/addons: '[{"addonId":"turbo-x1","quantity":1}]'
```

### Subscription States

| Status | HRQ Behavior |
|--------|-------------|
| `active` | Full plan limits applied |
| `trialing` | Full plan limits applied |
| `canceling` | Full plan limits applied (until period ends) |
| `suspended` | Minimal quota from `suspendedPlan` (e.g., 500m CPU, 1Gi memory) |
| No annotation | No HRQ created — no quota enforcement |

---

## Per-Project Sub-Quotas

The HRQ enforces the **aggregate** limit across all Projects. Platform operators can additionally limit an individual Project with a standard Kubernetes `ResourceQuota`. The standard Project Roles have read-only quota access, so manage these objects through the platform operations or GitOps workflow:

```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: project-quota
  namespace: acme-corp-dev
spec:
  hard:
    requests.cpu: "2"
    requests.memory: "4Gi"
    limits.cpu: "4"
    limits.memory: "8Gi"
```

The effective limit per resource is `min(Project ResourceQuota, Organization HRQ remaining)`.

---

## Updating Plans

Edit the ConfigMap and apply:

```bash
kubectl edit configmap billing-plans -n kube-dc
# or
kubectl apply -f billing-plans-configmap.yaml
```

The controller watches the ConfigMap and queues affected Organizations for reconciliation when it changes. Confirm the resulting HRQs and LimitRanges before relying on the new limits.

### Adding a New Plan

Add a new entry under `plans:` with all required fields and a corresponding `eipQuota` entry:

```yaml
plans:
  enterprise-pool:
    requests:
      cpu: "32"
      memory: "128Gi"
      storage: "1Ti"
    pods: 1000
    servicesLB: 100
    burstRatio: 1.2
    limitRange:
      defaultCPU: "1"
      defaultMemory: "2Gi"
      defaultRequestCPU: "500m"
      defaultRequestMem: "1Gi"
      maxCPU: "16"
      maxMemory: "64Gi"
      minCPU: "10m"
      minMemory: "16Mi"
      maxPodCPU: "32"
      maxPodMemory: "128Gi"
      maxPVCStorage: "1Ti"
      minPVCStorage: "1Gi"
eipQuota:
  enterprise-pool: 10
```

### Modifying an Existing Plan

Change the values in the ConfigMap. All organizations on that plan will be updated automatically.

---

## Monitoring Quota Usage

### View HRQ status

```bash
kubectl describe hrq plan-quota -n <org-namespace>
```

Output shows `spec.hard` (limits) and `status.used` (current usage aggregated across all projects):

```
Spec:
  Hard:
    limits.cpu:              8300m
    limits.memory:           24960Mi
    pods:                    200
    requests.cpu:            8300m
    requests.memory:         24960Mi
    requests.storage:        160Gi
    services.loadbalancers:  100
Status:
  Used:
    limits.cpu:              6000m
    limits.memory:           15000Mi
    requests.cpu:            4280m
    requests.memory:         7937Mi
    requests.storage:        40Gi
    pods:                    12
    services.loadbalancers:  3
```

### View LimitRange

```bash
kubectl describe limitrange default-resource-limits -n <org-namespace>
```

---

## Validation

The ConfigMap is validated on load. The controller will log an error and skip HRQ sync if validation fails. All of the following are required:

- At least one plan defined under `plans:`
- Each plan must have `requests.cpu`, `requests.memory`, `requests.storage`
- Each plan must have `burstRatio > 0`
- Each plan must have complete `limitRange` settings
- `suspendedPlan.cpu` is required
- `systemOverhead.cpuPerProject > 0` and `memPerProject > 0`
- `eipQuota` must be defined

Check controller logs for errors:

```bash
kubectl logs deployment/kube-dc-manager -n kube-dc | grep -i "billing-plans\|plan"
```

---

## Subscription Lifecycle

Organizations transition through the following subscription states:

```
checkout.session.completed
        │
        ▼
    ┌──────────┐   cancel at period end   ┌───────────┐
    │  active   │ ───────────────────────► │ canceling  │
    └──────────┘                           └───────────┘
        │                                       │
        │ subscription.deleted                  │ period ends → subscription.deleted
        │ (payment failure, manual cancel)       │
        ▼                                       ▼
    ┌───────────┐    7-day grace period    ┌───────────┐
    │ suspended  │ ──────────────────────► │  canceled  │
    └───────────┘                          └───────────┘
        │                                       │
        │ re-subscribe                          │ re-subscribe
        ▼                                       ▼
    ┌──────────┐                           ┌──────────┐
    │  active   │                          │  active   │
    └──────────┘                           └──────────┘
```

### State Details

| Status | HRQ Quota | Workloads | New Deployments | S3 Quota |
|--------|-----------|-----------|-----------------|----------|
| `active` | Full plan limits | Running | Allowed | Plan's `objectStorage` |
| `trialing` | Full plan limits | Running | Allowed | Plan's `objectStorage` |
| `canceling` | Full plan limits | Running | Allowed | Plan's `objectStorage` |
| `suspended` | Minimal (100m CPU, 128Mi) | Running (grace period) | Blocked | `maxSize=0` |
| `canceled` | Minimal (100m CPU, 128Mi) | Scaled to zero | Blocked | `maxSize=0` |
| `past_due` | Full plan limits | Running | Allowed | Plan's `objectStorage` |

### Grace Period

When a subscription is deleted (via Stripe webhook), the organization enters the `suspended` state:

- **7-day grace period** — existing workloads continue running, but new deployments are blocked
- After 7 days, the controller transitions the Organization to `canceled` and suspends all workloads
- Workload suspension: Deployments/StatefulSets scaled to 0, CronJobs suspended
- Original replica counts stored in annotations for restoration on re-subscribe

### Key Annotations

| Annotation | Description |
|------------|-------------|
| `billing.kube-dc.com/subscription` | Current status (`active`, `suspended`, `canceled`, etc.) |
| `billing.kube-dc.com/plan-id` | Active plan ID |
| `billing.kube-dc.com/plan-name` | Display name |
| `billing.kube-dc.com/suspended-at` | ISO timestamp when suspension started |
| `billing.kube-dc.com/stripe-subscription-id` | Stripe subscription ID |
| `billing.kube-dc.com/stripe-customer-id` | Stripe customer ID |
| `billing.kube-dc.com/addons` | JSON array of active add-ons |

---

## API Endpoints

The billing backend exposes the following REST endpoints under `/api/billing/`:

### Subscription Management

These routes are provider-dependent. Read-only catalog and quota routes remain
available in quota-only mode. Stripe mounts checkout, subscription mutation,
portal, and Stripe webhook operations. In WHMCS mode, subscription lifecycle
changes arrive from the signed WHMCS provisioning module instead of console
purchase buttons.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/organization-subscription` | Get organization subscription data with quota usage |
| `POST` | `/organization-subscription` | Create new subscription (redirects to Stripe Checkout) |
| `PUT` | `/organization-subscription` | Change plan on existing subscription |
| `DELETE` | `/organization-subscription` | Cancel subscription at period end |

### Quota & Usage

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/quota-usage` | Real-time HRQ usage + public EIP count |
| `GET` | `/quota-status` | HRQ existence and enforcement status |
| `POST` | `/simulate-downgrade` | Check if current usage fits target plan |

### Plans & Add-ons

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/plans` | List available subscription plans |
| `GET` | `/addons` | List available turbo add-ons |
| `POST` | `/organization-subscription/addons` | Add turbo add-on |
| `DELETE` | `/organization-subscription/addons/:id` | Remove turbo add-on |

### Per-Project Quota (under `/api/manage-organization/`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/projects/:id/quota` | Get project quota details (HRQ, per-project, LimitRange) |
| `PUT` | `/projects/:id/quota` | Set per-project ResourceQuota (org-admin only) |
| `DELETE` | `/projects/:id/quota` | Remove per-project ResourceQuota (org-admin only) |

Per-project quotas use standard Kubernetes `ResourceQuota` objects. They coexist with the HRQ — the most restrictive limit wins. The HNC-managed `hrq.hnc.x-k8s.io` quota is read-only; only the `project-quota` ResourceQuota can be managed through the authorized kube-dc API.

### Stripe Integration

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/verify-checkout` | Verify Stripe checkout session |
| `POST` | `/customer-portal` | Open Stripe customer portal |
| `POST` | `/webhook` | Stripe webhook handler (raw body) |

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Pods rejected with "exceeded quota" | Organization usage exceeds HRQ limits | Upgrade plan, remove addons, or delete unused workloads |
| Pods rejected with "must specify limits" | LimitRange missing or not propagated | Verify the `default-resource-limits` LimitRange exists in the Project backing namespace |
| HRQ not created | ConfigMap missing or invalid | Check controller logs, verify ConfigMap exists in `kube-dc` namespace |
| HRQ not updating after ConfigMap change | Controller not watching ConfigMap | Check controller logs for "billing-plans ConfigMap changed" message |
| EIP creation blocked | EIP quota exceeded | Check `eipQuota` setting for the plan |
| Workloads scaled to zero | Organization in `canceled` state | Re-subscribe to restore workloads |
| S3 uploads rejected (403) | Object storage quota exceeded or Organization suspended | Upgrade plan or re-subscribe |
| Subscription stuck in `suspended` | Grace period not expired yet (7 days) | Wait for grace period or re-subscribe |
| **No HRQ/LimitRange on any Organization** (quota enforcement silently off cluster-wide) | `billing-plans` ConfigMap is missing the required top-level `suspendedPlan` / `systemOverhead` / `eipQuota` sections, so `LoadPlanConfig` fails and the reconcile skips the whole quota block for every Organization | Restore the missing sections (see below). Confirm the fix: the manager logs `Loaded billing plans from ConfigMap (... plans: N)`. If `LoadPlanConfig` is failing it now logs at **ERROR** (`billing-plans ConfigMap not loaded — HRQ/LimitRange/quota enforcement is DISABLED ...`). |

### Validate the complete plan document

Older backend builds could save only the `plans` map and remove required
operator sections. Current builds preserve the full document, but an already
damaged ConfigMap must still be repaired.

Check for every required top-level section after an upgrade or plan edit:

```bash
kubectl -n kube-dc get configmap billing-plans -o jsonpath='{.data.plans\.yaml}' |
  grep -E '^(plans|suspendedPlan|systemOverhead|eipQuota):'
```

If a section is missing, stop plan edits and recover the full `plans.yaml` from
a reviewed Fleet revision or backup. Merge rather than replacing live plans,
add-ons, or promo codes. Re-enabling quota can reject new Pods or PVCs for an
Organization already above its plan, so compare current usage with the restored
limits and use a maintenance window.

Verify recovery in both controller state and generated quota resources:

```bash
kubectl -n kube-dc logs deployment/kube-dc-manager --since=15m |
  grep -E 'Loaded billing plans|billing-plans ConfigMap not loaded'
kubectl get hrq,limitrange -A
```
