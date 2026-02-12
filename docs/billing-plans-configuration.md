# Billing Plans & Resource Quota Configuration

This guide explains how to configure billing plans, resource quotas, and EIP limits for Kube-DC organizations using the `billing-plans` ConfigMap.

---

## Overview

Kube-DC enforces organization-level resource limits using four mechanisms:

1. **HierarchicalResourceQuota (HRQ)** — Aggregates resource usage across all project namespaces within an organization. Enforced at pod scheduling time.
2. **LimitRange** — Provides default CPU/memory requests and limits for containers that don't specify them. Required for HRQ to work correctly.
3. **EIP Quota** — Limits the number of public Elastic IPs an organization can allocate.
4. **Object Storage Quota** — Manages S3 storage limits via Rook-Ceph `CephObjectStoreUser` quotas.

All four are driven by a single ConfigMap: `billing-plans` in the `kube-dc` namespace.

### Billing Provider Feature Flag

The quota system is **decoupled** from any specific payment provider. A payment provider (Stripe, WHMCS, etc.) is optional and controlled by the `BILLING_PROVIDER` environment variable on the UI backend:

| Value | Behavior |
|-------|----------|
| `none` (default) | **Quota-only mode.** Plans load from ConfigMap, HRQ/LimitRange/EIP quotas enforced. No payment flow. Plan assignment via `kubectl` annotations. |
| `stripe` | Full Stripe integration: checkout sessions, webhooks, customer portal, subscription CRUD. |
| `whmcs` | *(Future)* WHMCS webhook integration. |

When `BILLING_PROVIDER=none`:
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
        ├─► HierarchicalResourceQuota (org namespace)
        │       └─► Enforced across all child project namespaces
        │
        ├─► LimitRange (org namespace)
        │       └─► Propagated by HNC to all project namespaces
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

**Live updates:** Editing the ConfigMap automatically triggers reconciliation of all organizations within seconds — no restart required.

---

## Prerequisites

- Hierarchical Namespace Controller (HNC) installed with HRQ support
- HNC configured to propagate `LimitRange` resources (`mode: Propagate`)
- Project namespaces configured as children of the organization namespace via HNC hierarchy
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
        cpu: "<cpu>"
        memory: "<memory>"
        storage: "<storage>"
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
| `burstRatio` | Multiplier for limits over requests (e.g., 2.0 = limits are 2× requests) | `2.0` |

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

The total overhead is `cpuPerProject × organizationProjectsLimit` (default: 3 projects).

#### `addons`

Resource add-ons that can be attached to an organization via the `billing.kube-dc.com/addons` annotation.

| Field | Description | Example |
|-------|-------------|---------|
| `cpu` | Additional CPU per addon unit | `"4"` |
| `memory` | Additional memory per addon unit | `"8Gi"` |
| `storage` | Additional storage per addon unit | `"40Gi"` |

#### `eipQuota`

Maximum number of Elastic IPs (EIPs) per plan.

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
          - "Nested Clusters (KubeVirt)"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "4"
          memory: "8Gi"
          storage: "60Gi"
        pods: 100
        servicesLB: 100
        burstRatio: 3.0
        limitRange:
          defaultCPU: "500m"
          defaultMemory: "512Mi"
          defaultRequestCPU: "100m"
          defaultRequestMem: "128Mi"
          maxCPU: "2"
          maxMemory: "4Gi"
          minCPU: "10m"
          minMemory: "16Mi"
          maxPodCPU: "4"
          maxPodMemory: "8Gi"
          maxPVCStorage: "60Gi"
          minPVCStorage: "1Gi"
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
          - "Nested Clusters (KubeVirt)"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "8"
          memory: "24Gi"
          storage: "160Gi"
        pods: 200
        servicesLB: 100
        burstRatio: 2.0
        limitRange:
          defaultCPU: "500m"
          defaultMemory: "512Mi"
          defaultRequestCPU: "250m"
          defaultRequestMem: "256Mi"
          maxCPU: "4"
          maxMemory: "12Gi"
          minCPU: "10m"
          minMemory: "16Mi"
          maxPodCPU: "8"
          maxPodMemory: "24Gi"
          maxPVCStorage: "160Gi"
          minPVCStorage: "1Gi"
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
          - "Nested Clusters (KubeVirt)"
          - "Unlimited 1Gbit/s Bandwidth"
        requests:
          cpu: "16"
          memory: "56Gi"
          storage: "320Gi"
        pods: 500
        servicesLB: 100
        burstRatio: 1.5
        limitRange:
          defaultCPU: "1"
          defaultMemory: "1Gi"
          defaultRequestCPU: "500m"
          defaultRequestMem: "512Mi"
          maxCPU: "8"
          maxMemory: "32Gi"
          minCPU: "10m"
          minMemory: "16Mi"
          maxPodCPU: "16"
          maxPodMemory: "56Gi"
          maxPVCStorage: "320Gi"
          minPVCStorage: "1Gi"
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

Burst ratio:           2.0  (from plan)
limits.cpu = 10.3 × 2.0 = 20.6
```

The resulting HRQ `plan-quota`:

```yaml
spec:
  hard:
    requests.cpu:            "10300m"
    requests.memory:         "29056Mi"   # 24Gi + 4Gi addon + 384Mi overhead
    limits.cpu:              "20600m"
    limits.memory:           "58112Mi"
    requests.storage:        "180Gi"     # 160Gi + 20Gi addon
    pods:                    "200"
    services.loadbalancers:  "100"
```

### Burst Ratio

The burst ratio determines how much `limits` exceed `requests`:

| Plan | Burst Ratio | Reasoning |
|------|-------------|-----------|
| Dev Pool | 3× | Dev workloads are bursty, low overcommit risk |
| Pro Pool | 2× | Balanced burst for general workloads |
| Scale Pool | 1.5× | Production workloads need predictability |

Burst applies only to CPU and memory limits. Storage, pods, and LB quotas are not burst-multiplied.

### LimitRange Behavior

The LimitRange ensures every container has resource requests set, which is **required** by Kubernetes when a ResourceQuota is active:

1. Pod created **without** `resources.requests` → LimitRange applies defaults automatically
2. HRQ admission controller checks aggregated usage against the organization quota
3. Pod admitted if within quota; **rejected** if quota exceeded

The LimitRange is created in the organization namespace and automatically propagated to all project namespaces by HNC.

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

The HRQ enforces the **aggregate** limit across all projects. Organization admins can additionally limit individual projects using standard Kubernetes `ResourceQuota`:

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

The effective limit per resource is `min(project ResourceQuota, org HRQ remaining)`.

---

## Updating Plans

Edit the ConfigMap and apply:

```bash
kubectl edit configmap billing-plans -n kube-dc
# or
kubectl apply -f billing-plans-configmap.yaml
```

The controller automatically detects ConfigMap changes and re-reconciles all organizations. HRQs and LimitRanges are updated within seconds.

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
    limits.cpu:              16600m
    limits.memory:           49920Mi
    pods:                    200
    requests.cpu:            8300m
    requests.memory:         24960Mi
    requests.storage:        160Gi
    services.loadbalancers:  100
Status:
  Used:
    limits.cpu:              8560m
    limits.memory:           15874Mi
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
- After 7 days, the controller transitions the org to `canceled` and suspends all workloads
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

Per-project quotas use standard Kubernetes `ResourceQuota` objects. They coexist with the HRQ — the most restrictive limit wins. HRQ-managed quotas (prefixed `hrq-*`) are read-only; only the `project-quota` ResourceQuota can be managed via the API.

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
| Pods rejected with "must specify limits" | LimitRange missing or not propagated | Verify `default-resource-limits` LimitRange exists in project namespace |
| HRQ not created | ConfigMap missing or invalid | Check controller logs, verify ConfigMap exists in `kube-dc` namespace |
| HRQ not updating after ConfigMap change | Controller not watching ConfigMap | Check controller logs for "billing-plans ConfigMap changed" message |
| EIP creation blocked | EIP quota exceeded | Check `eipQuota` setting for the plan |
| Workloads scaled to zero | Organization in `canceled` state | Re-subscribe to restore workloads |
| S3 uploads rejected (403) | Object storage quota exceeded or org suspended | Upgrade plan or re-subscribe |
| Subscription stuck in `suspended` | Grace period not expired yet (7 days) | Wait for grace period or re-subscribe |
