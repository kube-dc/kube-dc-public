# PRD: Hierarchical Resource Quota Integration for Kube-DC

## Status: Frozen
## Branch: `pricing`
## Priority: High
## Last Updated: 2026-02-07

---

## 1. Overview

### 1.1 Problem Statement

Kube-DC currently has a billing system with Stripe integration that defines subscription plans (Dev Pool, Pro Pool, Scale Pool) with resource limits (CPU, memory, storage, IPv4), but **no actual Kubernetes-level enforcement** of these limits. The `usage` field in the subscription data is hardcoded to `{ used: 0, limit: N }` (mock data). Organizations can exceed their plan limits without any restriction.

Additionally, the existing `OrganizationProjectsLimit` (default: 3) is a simple integer count — it limits the *number* of projects but not the *aggregate resource consumption* across projects.

### 1.2 Goal

Integrate [Hierarchical Namespace Controller (HNC)](https://github.com/pfnet/hierarchical-namespaces) with `HierarchicalResourceQuota` (HRQ) to enforce organization-level resource quotas that aggregate across all project namespaces. When a user selects a billing plan, a corresponding HRQ is created/updated on the organization namespace, and Kubernetes natively enforces the limits across all child project namespaces.

### 1.3 Success Criteria

- Plan selection via UI creates a real `HierarchicalResourceQuota` on the organization namespace
- Resource consumption across all projects in an organization is capped by the HRQ
- Users see real-time quota usage in the Billing UI (not mock data)
- Plan upgrades/downgrades dynamically update the HRQ
- Turbo add-ons correctly increment HRQ limits
- Existing organizations are migrated seamlessly

---

## 2. Current Architecture Analysis

### 2.1 Namespace Hierarchy (Already Exists)

```
Organization Namespace: shalb              ← Organization CR lives here
  ├── Project Namespace: shalb-demo        ← Created by Project controller
  ├── Project Namespace: shalb-dev         ← Created by Project controller
  └── Project Namespace: shalb-envoy       ← Created by Project controller
```

**Key files:**
- `api/kube-dc.com/v1/organization_types.go` — Organization CRD (namespace = org name)
- `api/kube-dc.com/v1/project_types.go` — Project CRD (namespace = org name, creates child ns `{org}-{project}`)
- `internal/project/helpers.go:94` — `ProjectNamespaceName()` returns `p.Namespace + "-" + p.Name`
- `internal/project/res_namespace.go` — Creates project namespace with annotation `kube-dc.com/project: {org}/{project}`

### 2.2 Existing Billing System

**Backend (Node.js):**
- `ui/backend/controllers/billing/subscriptionController.js` — Stripe integration, plan definitions, organization annotation management
- `ui/backend/controllers/billing/billingController.js` — Billing API proxy
- `ui/backend/routes/billing.js` — Route definitions

**Frontend (React/PatternFly):**
- `ui/frontend/src/app/ManageOrganization/Billing/Billing.tsx` — Main billing UI (1133 lines)
- `ui/frontend/src/app/ManageOrganization/Billing/SubscribePlanModal.tsx` — Plan selection modal
- `ui/frontend/src/app/ManageOrganization/Billing/api.ts` — Billing API client
- `ui/frontend/src/app/ManageOrganization/Billing/types.ts` — TypeScript types and plan definitions

**Current Plans:**

| Plan | CPU | Memory | Storage | Object Storage | IPv4 | Price |
|------|-----|--------|---------|----------------|------|-------|
| Dev Pool | 4 vCPU | 8 GB | 60 GB | 20 GB | 1 | €19/mo |
| Pro Pool | 8 vCPU | 24 GB | 160 GB | 100 GB | 1 | €49/mo |
| Scale Pool | 16 vCPU | 56 GB | 320 GB | 500 GB | 3 | €99/mo |

**Turbo Add-ons:**

| Add-on | CPU | Memory | Price |
|--------|-----|--------|-------|
| Turbo x1 | +2 vCPU | +4 GB | €9/mo |
| Turbo x2 | +4 vCPU | +8 GB | €16/mo |

**Subscription data** is stored as Organization annotations:
```
billing.kube-dc.com/subscription: active
billing.kube-dc.com/plan-id: pro-pool
billing.kube-dc.com/plan-name: Pro Pool
billing.kube-dc.com/stripe-subscription-id: sub_xxx
billing.kube-dc.com/addons: [{"addonId":"turbo-x1","quantity":1}]
```

### 2.3 Existing Limits

- `MasterConfigSpec.OrganizationProjectsLimit` (default: 3) — Checked in `CheckOrganizationLimits()` in `internal/controller/kube-dc.com/organization_controller.go:229`
- Project controller requeues with 30s delay when limit exceeded (`project_controller.go:148`)

### 2.4 Gap Analysis

| Capability | Current State | Target State |
|-----------|---------------|--------------|
| Plan resource limits | Defined in code, not enforced | Enforced via HRQ |
| Usage tracking | Mock data (all zeros) | Real K8s ResourceQuota aggregation |
| Cross-project aggregation | None | HRQ aggregates across child namespaces |
| Enforcement | None (advisory only) | Hard limits at pod scheduling time |
| Plan change propagation | Annotations only | HRQ spec update |
| Add-on resource addition | Annotations only | HRQ spec update |

---

## 3. HNC Integration Design

### 3.1 HNC Component: pfnet/hierarchical-namespaces

**Repository:** https://github.com/pfnet/hierarchical-namespaces  
**Latest Release:** `v1.1.0-pfnet.10`  
**Image:** `ghcr.io/pfnet/hierarchical-namespaces/controller:v1.1.0-pfnet.10`  
**HRQ CRD API:** `hnc.x-k8s.io/v1alpha2`

HNC provides:
1. **Namespace hierarchy** — Parent-child relationships between namespaces
2. **Policy propagation** — RBAC, NetworkPolicies, Secrets propagated to children
3. **HierarchicalResourceQuota (HRQ)** — Aggregate quota across a namespace subtree

### 3.2 Namespace Hierarchy Mapping

HNC requires setting parent-child relationships on namespaces. The Kube-DC hierarchy maps naturally:

```
shalb (Organization namespace)          ← HRQ "plan-quota" applied here
├── shalb-demo (Project namespace)      ← child of shalb
├── shalb-dev (Project namespace)       ← child of shalb
└── shalb-envoy (Project namespace)     ← child of shalb
```

**HNC hierarchy is set via annotations/labels on namespaces:**
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: shalb-demo
  labels:
    shalb.tree.hnc.x-k8s.io/depth: "1"    # Set by HNC controller
  annotations:
    hnc.x-k8s.io/subnamespace-of: shalb    # OR use HierarchyConfiguration
```

**OR via HierarchyConfiguration:**
```yaml
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HierarchyConfiguration
metadata:
  name: hierarchy
  namespace: shalb-demo
spec:
  parent: shalb
```

### 3.3 HierarchicalResourceQuota (HRQ)

Applied on the organization namespace, limits are enforced across ALL child project namespaces:

```yaml
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HierarchicalResourceQuota
metadata:
  name: plan-quota
  namespace: shalb                    # Organization namespace
  labels:
    billing.kube-dc.com/auto-managed: "true"
    billing.kube-dc.com/plan-id: "pro-pool"
spec:
  hard:
    requests.cpu: "8"                 # From plan: 8 vCPU
    requests.memory: "24Gi"           # From plan: 24 GB
    limits.cpu: "16"                  # 2x burst for pro-pool (see §3.6)
    limits.memory: "48Gi"             # 2x burst for pro-pool
    requests.storage: "160Gi"         # From plan: 160 GB storage
    pods: "200"                       # Per-plan: 100/200/500
    services.loadbalancers: "5"       # Per-plan: 3/5/10
```

**HRQ Status (populated by HNC controller):**
```yaml
status:
  hard:
    requests.cpu: "8"
    requests.memory: "24Gi"
    ...
  used:
    requests.cpu: "2500m"             # Aggregated across all child namespaces
    requests.memory: "6Gi"
    ...
```

### 3.4 LimitRange — Required Companion to HRQ

#### 3.4.1 Why LimitRange is Mandatory

**Critical Kubernetes behavior:** When a `ResourceQuota` (or HRQ, which creates internal `ResourceQuota` objects named `hrq.hnc.x-k8s.io` in each child namespace) constrains `cpu` or `memory`, Kubernetes **rejects** any pod that does not explicitly set `requests` or `limits` for those resources:

> *For `cpu` and `memory` resources, ResourceQuotas enforce that **every** (new) pod in that namespace sets a limit for that resource. If you don't, the control plane may reject admission for that Pod.*
> — [Kubernetes Documentation](https://kubernetes.io/docs/concepts/policy/resource-quotas/)

This means:
- Once HRQ is applied to an organization namespace, **all project namespaces get internal ResourceQuotas**
- Any pod (container, KubeVirt VM, cloud-shell job, VPC DNS pod) created **without** explicit `resources.requests.cpu` / `resources.requests.memory` will be **rejected** by the API server
- **LimitRange solves this** by providing default `requests` and `limits` for containers that don't specify them

#### 3.4.2 Current State — No LimitRange Exists

The kube-dc codebase has **zero** LimitRange resources. Key workloads that currently omit explicit resource requests:

| Workload | File | Sets requests/limits? |
|----------|------|----------------------|
| KubeVirt VMs | `AddNewVMModal.tsx:377-380` | Sets `domain.cpu.cores` and `domain.memory.guest` but **NOT** pod-level `resources.requests` (KubeVirt auto-generates these from domain spec) |
| Cloud-shell SSH jobs | `cloudshell-job.tmpl.yaml` | Likely no resource requests |
| VPC DNS pods | `res_vpc_dns.go` | Created by Kube-OVN, may lack requests |
| User-deployed pods | User YAML | No guarantee of resource requests |

**KubeVirt note:** KubeVirt's `virt-controller` translates `domain.cpu.cores` and `domain.memory.guest` into pod-level `resources.requests` and `resources.limits` automatically. So KubeVirt VMs **should** work with ResourceQuota, but this must be verified.

#### 3.4.3 LimitRange Design

Create a `LimitRange` in the **organization namespace** and use **HNC propagation** to automatically copy it to all project namespaces.

```yaml
apiVersion: v1
kind: LimitRange
metadata:
  name: default-resource-limits
  namespace: shalb                    # Organization namespace
  labels:
    billing.kube-dc.com/auto-managed: "true"
spec:
  limits:
    # Default requests/limits for Containers
    - type: Container
      default:                        # Default limits (if not specified)
        cpu: 500m
        memory: 512Mi
      defaultRequest:                 # Default requests (if not specified)
        cpu: 100m
        memory: 128Mi
      max:                            # Max per container (prevents single container from consuming entire quota)
        cpu: "8"
        memory: 32Gi
      min:                            # Minimum per container
        cpu: 10m
        memory: 16Mi
    # Default requests/limits for Pods
    - type: Pod
      max:                            # Max per pod (all containers combined)
        cpu: "16"
        memory: 64Gi
    # PVC size limits
    - type: PersistentVolumeClaim
      max:
        storage: 500Gi
      min:
        storage: 1Gi
```

**How this works with HRQ:**

```
1. User creates pod WITHOUT resources.requests/limits
2. LimitRange admission controller applies defaults (cpu: 100m, memory: 128Mi)
3. HRQ admission controller checks aggregated usage against quota
4. Pod is admitted if within quota, rejected if over quota

Order of admission controllers: LimitRange → ResourceQuota/HRQ
(LimitRange runs BEFORE ResourceQuota, so defaults are applied first)
```

#### 3.4.4 HNC Propagation of LimitRange

HNC can propagate LimitRange objects from parent namespace to all child namespaces. This is the **recommended approach** because:

1. **Single source of truth** — Define once in org namespace, propagated to all projects
2. **Automatic updates** — Change the LimitRange in org namespace → HNC updates all children
3. **No controller code needed** — HNC handles propagation natively
4. **Immutable in children** — HNC admission webhook prevents modification of propagated copies

Configure HNC to propagate LimitRange:

```yaml
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HNCConfiguration
metadata:
  name: config
spec:
  resources:
    - resource: limitranges
      mode: Propagate              # Propagate from org → project namespaces
```

After this, a LimitRange created in `shalb` (org namespace) will be automatically copied to `shalb-demo`, `shalb-dev`, `shalb-envoy` (all project namespaces).

#### 3.4.5 LimitRange as Standalone Kubernetes Object (Hybrid Approach)

LimitRange is a **standard Kubernetes object** — no need to embed it into the Organization CRD. Instead, the controller auto-creates a LimitRange with plan-based defaults, and the org admin can override it by creating their own.

**Design: controller auto-creation + user override**

1. When a billing plan is activated, the Organization controller checks if a LimitRange named `default-resource-limits` exists in the org namespace
2. If **no LimitRange exists** → controller creates one with plan-based defaults and labels it `billing.kube-dc.com/auto-managed: "true"`
3. If a **user-created LimitRange exists** (without the `auto-managed` label) → controller does NOT overwrite it, user owns the resource policy
4. If the **auto-managed LimitRange exists** and the plan changes → controller updates it with new plan defaults
5. HNC propagates the LimitRange (regardless of who created it) to all project namespaces

**Ownership detection via labels:**

```yaml
# Controller-managed (auto-created, will be updated on plan change)
labels:
  billing.kube-dc.com/auto-managed: "true"
  billing.kube-dc.com/plan-id: "pro-pool"

# User-managed (controller will NOT touch this)
# No billing.kube-dc.com/auto-managed label
```

**Benefits of this approach:**
- **No CRD changes** — Organization spec stays clean, no API bloat
- **Standard Kubernetes** — org admins who know K8s can use native LimitRange directly
- **Auto-defaults for everyone** — users who don't know/care get sensible defaults from their plan
- **User can customize** — just create/edit a LimitRange in the org namespace, controller backs off
- **Future-proof** — any new LimitRange features work immediately without CRD updates
- **Separation of concerns** — billing/identity (Organization CRD) separate from resource policy (LimitRange)

**Edge cases:**

- **User edits auto-managed LimitRange:** Controller will overwrite it back to plan defaults on the next reconciliation (the `auto-managed` label is still present). This is enforced behavior. The controller **should emit a Kubernetes Event** (via `EventRecorder`) when it detects and overwrites user changes to an auto-managed resource, so the action is auditable and visible in `kubectl describe limitrange`.
- **User removes the `auto-managed` label:** Controller treats it as user-managed and stops updating it. This is acceptable — the HRQ still enforces aggregate quota. The user effectively "detached" the LimitRange from plan auto-updates. This is a valid power-user action, not a bug.

**Plan-based auto-defaults** (used when controller creates the LimitRange):

| Plan | Default Request CPU | Default Request Memory | Default Limit CPU | Default Limit Memory | Max CPU | Max Memory |
|------|--------------------|-----------------------|-------------------|---------------------|---------|-----------|
| Dev Pool (4 CPU) | 100m | 128Mi | 500m | 512Mi | 2 | 4Gi |
| Pro Pool (8 CPU) | 250m | 256Mi | 500m | 512Mi | 4 | 12Gi |
| Scale Pool (16 CPU) | 500m | 512Mi | 1 | 1Gi | 8 | 32Gi |

**Rationale:** Smaller plans need lower `max` to prevent a single container from consuming the entire quota. Container defaults should be reasonable for typical workloads — not too low (causes constant OOMKill) or too high (wastes quota).

**Intentional: LimitRange max < HRQ burst limits.** For Dev Pool, HRQ allows 12 CPU burst (3x), but LimitRange caps a single container at 2 CPU and a single pod at 4 CPU. This means a user needs at least 3 pods (4+4+4) to utilize the full burst capacity. This is **by design** — it prevents a single runaway pod from starving all other workloads in the organization. Users with "single heavy task" workloads (e.g., a large build job) are limited to the per-pod max, even if the HRQ has headroom. If this is too restrictive for a specific org, the org-admin can override the LimitRange (see edge cases above).

#### 3.4.6 LimitRange Propagation via HNC

HNC propagation is the recommended approach:

1. **Create LimitRange in org namespace** → HNC auto-copies to all project namespaces
2. **Single source of truth** — change once, propagated everywhere
3. **Immutable in children** — HNC admission webhook prevents users from modifying propagated copies
4. **Automatic cleanup** — delete from org namespace, HNC removes all copies
5. **Works regardless of who created it** — both controller-managed and user-managed LimitRanges are propagated

**Alternative (not recommended):** Create LimitRange directly in each project namespace via Project controller. This requires:
- Extra controller code per project
- Synchronization when org defaults change (must update all projects)
- More RBAC surface area

**Recommendation:** Use HNC propagation. Fall back to direct creation only if HNC propagation proves problematic.

### 3.5 Per-Project Sub-Quotas (Org-Admin Managed)

HRQ enforces the **aggregate** limit across all projects. But org-admins also need to limit individual projects — e.g., "dev project gets max 2 CPU, production project gets 6 CPU" within an 8 CPU org total.

#### 3.5.1 Design: Standard Kubernetes ResourceQuota

Per-project quotas use **standard Kubernetes `ResourceQuota`** — no custom CRDs. This works because:

1. HRQ creates internal ResourceQuota objects in child namespaces (managed by HNC, names prefixed `hrq-*`)
2. Org-admin creates **additional** ResourceQuota in the project namespace
3. Kubernetes applies **all** ResourceQuotas in a namespace — the most restrictive limit wins per resource
4. HRQ still enforces the org-level cap (sum of all projects can't exceed org total)

```
Organization: acme-corp (HRQ: 8 CPU total)
  ├── acme-corp-dev        ResourceQuota: 2 CPU    ← org-admin sets this
  ├── acme-corp-staging    ResourceQuota: 2 CPU    ← org-admin sets this
  └── acme-corp-prod       (no ResourceQuota)      ← gets remainder up to HRQ limit
```

In this example:
- `acme-corp-dev` can use **at most** 2 CPU (ResourceQuota caps it)
- `acme-corp-prod` can use up to 8 CPU minus whatever dev+staging consume (HRQ caps it)
- Total across all projects can never exceed 8 CPU (HRQ enforces)

#### 3.5.2 RBAC: Org-Admin Can Manage ResourceQuota in Project Namespaces

Org-admin needs write access to `resourcequotas` in project namespaces. This is added to the existing org-admin Role:

```yaml
# Added to the org-admin Role in each project namespace
- apiGroups: [""]
  resources: ["resourcequotas"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

**Constraints:**
- Org-admin **can** set per-project ResourceQuota (limits individual projects)
- Org-admin **cannot** modify HRQ-managed internal ResourceQuota (names prefixed `hrq-*`, managed by HNC)
- Org-admin **cannot** set per-project limits higher than the org HRQ (Kubernetes enforces: the most restrictive wins)
- HNC admission webhook blocks modification of HRQ-internal ResourceQuota objects

#### 3.5.3 Example: Per-Project ResourceQuota

```yaml
# Created by org-admin in the project namespace
apiVersion: v1
kind: ResourceQuota
metadata:
  name: project-quota
  namespace: acme-corp-dev           # project namespace
spec:
  hard:
    requests.cpu: "2"
    requests.memory: "4Gi"
    limits.cpu: "4"
    limits.memory: "8Gi"
    requests.storage: "20Gi"
    pods: "50"
```

This coexists with the HRQ-managed internal ResourceQuota. The effective limit per resource is `min(project-quota, hrq-internal-quota)`.

#### 3.5.4 UI Integration (Optional)

The console can expose per-project quota management under **Project Settings → Resource Limits**:

- Show current project usage vs project quota vs org HRQ remaining
- Form to set per-project limits (creates/updates ResourceQuota via backend API)
- Validation: warn if sum of all project quotas exceeds org HRQ total
- Backend endpoint: `PUT /api/projects/:namespace/quota` — creates ResourceQuota using SA token

> **Note:** UI for per-project quota management is **needed but late-stage** (post-MVP). For MVP, org-admins use `kubectl` directly since this is standard Kubernetes. The UI page should be implemented after the core HRQ + LimitRange + subscription lifecycle is stable.

#### 3.5.5 Interaction with Subscription Lifecycle

| Subscription Status | Org HRQ | Per-Project ResourceQuota |
|--------------------|---------|--------------------------|
| `active` | Full plan limits | Org-admin managed, respected |
| `suspended` | Minimal (0.5 CPU) | Still exists but effectively capped by HRQ |
| `canceled` | Minimal (0.5 CPU) | Still exists, workloads scaled to 0 anyway |
| Re-subscribe | Full plan limits restored | Per-project quotas still in place, unmodified |

Per-project ResourceQuotas are **not touched** by the subscription lifecycle — they are org-admin's responsibility. Only the org-level HRQ changes with subscription status.

### 3.6 Plan-to-HRQ Resource Mapping

**Burst ratios vary by plan tier** — dev workloads are bursty and low-risk, production workloads need predictability:

| Plan | Burst Ratio (limits/requests) | Reasoning |
|------|------------------------------|----------|
| Dev Pool | 3x | Dev/sandbox workloads are bursty, low overcommit risk |
| Pro Pool | 2x | Balanced burst for general workloads |
| Scale Pool | 1.5x | Production workloads need predictability, less overcommit |

```javascript
// Burst ratio per plan tier
const BURST_RATIOS = {
    'dev-pool': 3,
    'pro-pool': 2,
    'scale-pool': 1.5,
};

// Mapping function: Plan resources → HRQ spec.hard
function planToHRQ(plan, addons = []) {
    let totalCpu = plan.resources.cpu;
    let totalMemory = plan.resources.memory;
    
    for (const addon of addons) {
        const addonDef = TURBO_ADDONS.find(a => a.id === addon.addonId);
        if (addonDef) {
            totalCpu += addonDef.resources.cpu * (addon.quantity || 1);
            totalMemory += addonDef.resources.memory * (addon.quantity || 1);
        }
    }

    const burst = BURST_RATIOS[plan.id] || 2;
    
    return {
        'requests.cpu': `${totalCpu}`,
        'requests.memory': `${totalMemory}Gi`,
        'limits.cpu': `${totalCpu * burst}`,             // per-plan burst
        'limits.memory': `${totalMemory * burst}Gi`,     // per-plan burst
        'requests.storage': `${plan.resources.storage}Gi`,
        'pods': plan.id === 'dev-pool' ? '100' : plan.id === 'pro-pool' ? '200' : '500',
        'services.loadbalancers': plan.id === 'dev-pool' ? '3' : plan.id === 'pro-pool' ? '5' : '10',
    };
}
```

**Resulting HRQ values per plan:**

| | Dev Pool (3x burst) | Pro Pool (2x burst) | Scale Pool (1.5x burst) |
|---|---|---|---|
| `requests.cpu` | 4 | 8 | 16 |
| `limits.cpu` | 12 | 16 | 24 |
| `requests.memory` | 8Gi | 24Gi | 64Gi |
| `limits.memory` | 24Gi | 48Gi | 96Gi |
| `requests.storage` | 60Gi | 160Gi | 500Gi |
| `pods` | 100 | 200 | 500 |
| `services.loadbalancers` | 3 | 5 | 10 |

---

## 4. Implementation Plan

### Phase 1: HNC Installation & Configuration

#### 4.1.1 Install HNC Controller

Add HNC deployment to the Kube-DC installer (`installer/kube-dc/templates/`):

```bash
# Install HNC controller
kubectl apply -f https://github.com/pfnet/hierarchical-namespaces/releases/download/v1.1.0-pfnet.10/default.yaml

# Install HRQ component (separate manifest)
kubectl apply -f https://github.com/pfnet/hierarchical-namespaces/releases/download/v1.1.0-pfnet.10/hrq.yaml
```

**Helm chart integration:**
- Add HNC manifests to `charts/kube-dc/templates/` or as a dependency
- Configure HNC via `HNCConfiguration` resource to propagate relevant resource types

#### 4.1.2 HNC Configuration

```yaml
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HNCConfiguration
metadata:
  name: config
spec:
  resources:
    # Propagate RBAC (HNC default)
    - resource: roles.rbac.authorization.k8s.io
      mode: Propagate
    - resource: rolebindings.rbac.authorization.k8s.io
      mode: Propagate
    # Do NOT propagate Kube-DC CRDs (managed by controllers)
    - resource: projects.kube-dc.com
      mode: Ignore
    - resource: organizations.kube-dc.com
      mode: Ignore
    # Propagate secrets for shared credentials
    - resource: secrets
      mode: Propagate
    # Propagate LimitRange from org namespace to project namespaces
    # CRITICAL: Required for HRQ enforcement — without LimitRange, pods
    # without explicit resource requests will be REJECTED by ResourceQuota
    - resource: limitranges
      mode: Propagate
```

#### 4.1.3 RBAC for HNC Resources

The kube-dc-manager service account needs permissions to manage HNC resources:

```yaml
# Add to existing ClusterRole or create new one
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-dc-hnc-manager
rules:
  - apiGroups: ["hnc.x-k8s.io"]
    resources: ["hierarchicalresourcequotas", "hierarchyconfigurations", "subnamespaceanchors"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["hnc.x-k8s.io"]
    resources: ["hierarchicalresourcequotas/status"]
    verbs: ["get"]
```

### Phase 2: Controller Changes (Go)

#### 4.2.1 Project Controller — Set Namespace Parent

**File:** `internal/project/res_namespace.go`

When creating a project namespace, set the HNC parent relationship:

```go
func NewProjectNamespace(cli client.Client, project *kubedccomv1.Project, l *logr.Logger) objmgr.KubeDcResource[*corev1.Namespace] {
    genObject := &corev1.Namespace{
        ObjectMeta: v1.ObjectMeta{
            Name: ProjectNamespaceName(project),
            Annotations: map[string]string{
                kubedccomv1.ProjectNamespaceAnnotationKey: nsProjAnnotation(project),
            },
            Labels: map[string]string{
                // HNC will set tree labels automatically once hierarchy is configured
            },
        },
    }
    // ... existing diff function ...
}
```

**New file:** `internal/project/res_hierarchy.go`

```go
package project

// Create/update HierarchyConfiguration to set parent namespace
func NewProjectHierarchy(cli client.Client, project *kubedccomv1.Project, l *logr.Logger) objmgr.KubeDcResource[*hncv1alpha2.HierarchyConfiguration] {
    genObject := &hncv1alpha2.HierarchyConfiguration{
        ObjectMeta: v1.ObjectMeta{
            Name:      "hierarchy",  // HNC requires this exact name
            Namespace: ProjectNamespaceName(project),
        },
        Spec: hncv1alpha2.HierarchyConfigurationSpec{
            Parent: project.Organization().Namespace, // org namespace as parent
        },
    }
    // ...
}
```

**Integration in `internal/project/project.go` Sync():**
```go
// After namespace creation, before VPC creation:
log.Info("create or update HNC Hierarchy")
hierarchy := NewProjectHierarchy(cli, p, log)
err = hierarchy.Sync(ctx)
projectStatusChanged = projectStatusChanged || hierarchy.StatusChanged()
if err != nil {
    return err, projectStatusChanged
}
```

#### 4.2.2 Organization Controller — Manage HRQ

**New file:** `internal/organization/res_hrq.go`

```go
package organization

import (
    hncv1alpha2 "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
    "k8s.io/apimachinery/pkg/api/resource"
)

const (
    HRQName           = "plan-quota"
    HRQManagedLabel   = "billing.kube-dc.com/managed"
    HRQPlanIdLabel    = "billing.kube-dc.com/plan-id"
)

// PlanResources holds computed resource limits for an organization
type PlanResources struct {
    RequestsCPU       resource.Quantity
    RequestsMemory    resource.Quantity
    LimitsCPU         resource.Quantity
    LimitsMemory      resource.Quantity
    RequestsStorage   resource.Quantity
    Pods              resource.Quantity
    ServicesLB        resource.Quantity
}

func NewOrganizationHRQ(cli client.Client, org *kubedccomv1.Organization, 
    planResources *PlanResources, log *logr.Logger) objmgr.KubeDcResource[*hncv1alpha2.HierarchicalResourceQuota] {
    
    if planResources == nil {
        return nil // No plan = no HRQ
    }
    
    genObject := &hncv1alpha2.HierarchicalResourceQuota{
        ObjectMeta: v1.ObjectMeta{
            Name:      HRQName,
            Namespace: org.Namespace,
            Labels: map[string]string{
                HRQManagedLabel: "true",
            },
        },
        Spec: hncv1alpha2.HierarchicalResourceQuotaSpec{
            Hard: corev1.ResourceList{
                corev1.ResourceRequestsCPU:    planResources.RequestsCPU,
                corev1.ResourceRequestsMemory: planResources.RequestsMemory,
                corev1.ResourceLimitsCPU:      planResources.LimitsCPU,
                corev1.ResourceLimitsMemory:   planResources.LimitsMemory,
                corev1.ResourceRequestsStorage: planResources.RequestsStorage,
                corev1.ResourcePods:           planResources.Pods,
                "services.loadbalancers":      planResources.ServicesLB,
            },
        },
    }
    // ...
}
```

**Reading plan from Organization annotations:**

```go
func GetPlanResourcesFromAnnotations(org *kubedccomv1.Organization) *PlanResources {
    annotations := org.GetAnnotations()
    if annotations == nil {
        return nil
    }
    
    planId := annotations["billing.kube-dc.com/plan-id"]
    status := annotations["billing.kube-dc.com/subscription"]
    
    // Active statuses that should have full HRQ (see §7.2 state machine)
    if planId == "" || (status != "active" && status != "trialing" && status != "canceling") {
        return nil
    }
    
    // Plan definitions (should match subscriptionController.js SUBSCRIPTION_PLANS)
    plans := map[string]PlanResources{
        "dev-pool": {
            RequestsCPU:     resource.MustParse("4"),
            RequestsMemory:  resource.MustParse("8Gi"),
            LimitsCPU:       resource.MustParse("12"),   // 3x burst (see §3.6)
            LimitsMemory:    resource.MustParse("24Gi"), // 3x burst
            RequestsStorage: resource.MustParse("60Gi"),
            Pods:            resource.MustParse("100"),
            ServicesLB:      resource.MustParse("3"),
        },
        "pro-pool": { /* ... */ },
        "scale-pool": { /* ... */ },
    }
    
    base, ok := plans[planId]
    if !ok {
        return nil
    }
    
    // Apply add-ons
    addonsJSON := annotations["billing.kube-dc.com/addons"]
    // Parse and add addon resources to base...
    
    return &base
}
```

#### 4.2.3 Organization Controller — Watch Annotation Changes

**File:** `internal/controller/kube-dc.com/organization_controller.go`

Update the predicate to trigger reconciliation on annotation changes (billing events):

```go
func getPredicateFuncOrg() predicate.Funcs {
    return predicate.Funcs{
        UpdateFunc: func(e event.UpdateEvent) bool {
            oldObj := e.ObjectOld.(*kubedccomv1.Organization)
            newObj := e.ObjectNew.(*kubedccomv1.Organization)

            // Existing spec/finalizer/deletion checks...
            
            // NEW: Check if billing annotations changed
            oldPlanId := oldObj.GetAnnotations()["billing.kube-dc.com/plan-id"]
            newPlanId := newObj.GetAnnotations()["billing.kube-dc.com/plan-id"]
            oldAddons := oldObj.GetAnnotations()["billing.kube-dc.com/addons"]
            newAddons := newObj.GetAnnotations()["billing.kube-dc.com/addons"]
            oldStatus := oldObj.GetAnnotations()["billing.kube-dc.com/subscription"]
            newStatus := newObj.GetAnnotations()["billing.kube-dc.com/subscription"]
            
            if oldPlanId != newPlanId || oldAddons != newAddons || oldStatus != newStatus {
                return true // Reconcile to update HRQ
            }
            
            // ... existing checks
        },
    }
}
```

#### 4.2.4 Organization Controller — Manage LimitRange

**New file:** `internal/organization/res_limitrange.go`

The LimitRange is created in the org namespace. HNC propagates it to all child project namespaces automatically.

```go
package organization

import (
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/resource"
)

const (
    LimitRangeName = "default-resource-limits"
)

// LimitRangeDefaults holds default/max resource values per plan
type LimitRangeDefaults struct {
    DefaultCPU        resource.Quantity
    DefaultMemory     resource.Quantity
    DefaultRequestCPU resource.Quantity
    DefaultRequestMem resource.Quantity
    MaxCPU            resource.Quantity
    MaxMemory         resource.Quantity
    MinCPU            resource.Quantity
    MinMemory         resource.Quantity
    MaxPodCPU         resource.Quantity
    MaxPodMemory      resource.Quantity
    MaxPVCStorage     resource.Quantity
    MinPVCStorage     resource.Quantity
}

func GetLimitRangeDefaultsForPlan(planId string) *LimitRangeDefaults {
    defaults := map[string]LimitRangeDefaults{
        "dev-pool": {
            DefaultCPU:        resource.MustParse("500m"),
            DefaultMemory:     resource.MustParse("512Mi"),
            DefaultRequestCPU: resource.MustParse("100m"),
            DefaultRequestMem: resource.MustParse("128Mi"),
            MaxCPU:            resource.MustParse("2"),
            MaxMemory:         resource.MustParse("4Gi"),
            MinCPU:            resource.MustParse("10m"),
            MinMemory:         resource.MustParse("16Mi"),
            MaxPodCPU:         resource.MustParse("4"),
            MaxPodMemory:      resource.MustParse("8Gi"),
            MaxPVCStorage:     resource.MustParse("60Gi"),
            MinPVCStorage:     resource.MustParse("1Gi"),
        },
        "pro-pool": {
            DefaultCPU:        resource.MustParse("500m"),
            DefaultMemory:     resource.MustParse("512Mi"),
            DefaultRequestCPU: resource.MustParse("250m"),
            DefaultRequestMem: resource.MustParse("256Mi"),
            MaxCPU:            resource.MustParse("4"),
            MaxMemory:         resource.MustParse("12Gi"),
            MinCPU:            resource.MustParse("10m"),
            MinMemory:         resource.MustParse("16Mi"),
            MaxPodCPU:         resource.MustParse("8"),
            MaxPodMemory:      resource.MustParse("24Gi"),
            MaxPVCStorage:     resource.MustParse("160Gi"),
            MinPVCStorage:     resource.MustParse("1Gi"),
        },
        "scale-pool": {
            DefaultCPU:        resource.MustParse("1"),
            DefaultMemory:     resource.MustParse("1Gi"),
            DefaultRequestCPU: resource.MustParse("500m"),
            DefaultRequestMem: resource.MustParse("512Mi"),
            MaxCPU:            resource.MustParse("8"),
            MaxMemory:         resource.MustParse("32Gi"),
            MinCPU:            resource.MustParse("10m"),
            MinMemory:         resource.MustParse("16Mi"),
            MaxPodCPU:         resource.MustParse("16"),
            MaxPodMemory:      resource.MustParse("56Gi"),
            MaxPVCStorage:     resource.MustParse("320Gi"),
            MinPVCStorage:     resource.MustParse("1Gi"),
        },
    }
    d, ok := defaults[planId]
    if !ok {
        return nil
    }
    return &d
}

func NewOrganizationLimitRange(cli client.Client, org *kubedccomv1.Organization,
    lrDefaults *LimitRangeDefaults, log *logr.Logger) objmgr.KubeDcResource[*corev1.LimitRange] {

    if lrDefaults == nil {
        return nil
    }

    genObject := &corev1.LimitRange{
        ObjectMeta: v1.ObjectMeta{
            Name:      LimitRangeName,
            Namespace: org.Namespace,
            Labels: map[string]string{
                HRQManagedLabel: "true",
            },
        },
        Spec: corev1.LimitRangeSpec{
            Limits: []corev1.LimitRangeItem{
                {
                    Type: corev1.LimitTypeContainer,
                    Default: corev1.ResourceList{
                        corev1.ResourceCPU:    lrDefaults.DefaultCPU,
                        corev1.ResourceMemory: lrDefaults.DefaultMemory,
                    },
                    DefaultRequest: corev1.ResourceList{
                        corev1.ResourceCPU:    lrDefaults.DefaultRequestCPU,
                        corev1.ResourceMemory: lrDefaults.DefaultRequestMem,
                    },
                    Max: corev1.ResourceList{
                        corev1.ResourceCPU:    lrDefaults.MaxCPU,
                        corev1.ResourceMemory: lrDefaults.MaxMemory,
                    },
                    Min: corev1.ResourceList{
                        corev1.ResourceCPU:    lrDefaults.MinCPU,
                        corev1.ResourceMemory: lrDefaults.MinMemory,
                    },
                },
                {
                    Type: corev1.LimitTypePod,
                    Max: corev1.ResourceList{
                        corev1.ResourceCPU:    lrDefaults.MaxPodCPU,
                        corev1.ResourceMemory: lrDefaults.MaxPodMemory,
                    },
                },
                {
                    Type: corev1.LimitTypePersistentVolumeClaim,
                    Max: corev1.ResourceList{
                        corev1.ResourceStorage: lrDefaults.MaxPVCStorage,
                    },
                    Min: corev1.ResourceList{
                        corev1.ResourceStorage: lrDefaults.MinPVCStorage,
                    },
                },
            },
        },
    }
    // ...
}
```

**Update `organization.Sync()` in `internal/organization/organization.go`:**

```go
// After existing sync steps, add HRQ and LimitRange sync:
log.V(5).Info("Sync hierarchical resource quota...")
planResources := GetPlanResourcesFromAnnotations(org)
if planResources != nil {
    hrqRes := NewOrganizationHRQ(client, org, planResources, log)
    err = hrqRes.Sync(ctx)
    orgStatusChanged = orgStatusChanged || hrqRes.StatusChanged()
    if err != nil {
        return err, orgStatusChanged
    }
}

// LimitRange MUST be synced alongside HRQ — without it, pods without
// explicit resource requests will be rejected by the ResourceQuota
// that HRQ creates internally in each child namespace.
// The LimitRange is created in the org namespace and HNC propagates
// it to all project namespaces automatically.
log.V(5).Info("Sync LimitRange for default resource requests...")
planId := org.GetAnnotations()["billing.kube-dc.com/plan-id"]
lrDefaults := GetLimitRangeDefaultsForPlan(planId)
if lrDefaults != nil {
    lrRes := NewOrganizationLimitRange(client, org, lrDefaults, log)
    err = lrRes.Sync(ctx)
    orgStatusChanged = orgStatusChanged || lrRes.StatusChanged()
    if err != nil {
        return err, orgStatusChanged
    }
}
```

### Phase 3: Backend Changes (Node.js)

#### 4.3.1 Subscription Controller — Create/Update HRQ on Plan Change

**File:** `ui/backend/controllers/billing/subscriptionController.js`

After updating Organization annotations (in `updateOrganizationSubscription`), the Go controller will automatically reconcile and create/update the HRQ. **No direct HRQ manipulation needed from the backend** — the Go controller watches annotation changes.

However, we need to update the **usage endpoint** to read real HRQ status:

```javascript
/**
 * Get real quota usage from HierarchicalResourceQuota status
 */
async function getOrganizationQuotaUsage(organization, saToken) {
    try {
        const hrqUrl = `https://${global.k8sUrl}/apis/hnc.x-k8s.io/v1alpha2/namespaces/${organization}/hierarchicalresourcequotas/plan-quota`;
        const response = await fetch(hrqUrl, {
            headers: {
                'Accept': 'application/json',
                'Authorization': `Bearer ${saToken}`,
            },
            agent: httpsAgent,
        });

        if (!response.ok) {
            return null; // HRQ not found (no plan)
        }

        const hrq = await response.json();
        const hard = hrq.status?.hard || {};
        const used = hrq.status?.used || {};

        return {
            cpu: {
                used: parseCpuValue(used['requests.cpu'] || '0'),
                limit: parseCpuValue(hard['requests.cpu'] || '0'),
            },
            memory: {
                used: parseMemoryValue(used['requests.memory'] || '0'),
                limit: parseMemoryValue(hard['requests.memory'] || '0'),
            },
            storage: {
                used: parseMemoryValue(used['requests.storage'] || '0'),
                limit: parseMemoryValue(hard['requests.storage'] || '0'),
            },
            pods: {
                used: parseInt(used['pods'] || '0'),
                limit: parseInt(hard['pods'] || '0'),
            },
        };
    } catch (error) {
        logger.error('Error fetching HRQ usage:', error.message);
        return null;
    }
}
```

**Replace mock usage in `getOrganizationSubscriptionData()`:**

```javascript
// BEFORE (mock):
usage: plan ? {
    cpu: { used: 0, limit: totalCpu },
    memory: { used: 0, limit: totalMemory },
    storage: { used: 0, limit: totalStorage },
    pods: { used: 0, limit: 200 },
} : null,

// AFTER (real):
usage: plan ? await getOrganizationQuotaUsage(organization, saToken) : null,
```

### Phase 4: Frontend Changes (React/TypeScript)

#### 4.4.1 Billing Overview — Show Real Usage

**File:** `ui/frontend/src/app/ManageOrganization/Billing/Billing.tsx`

The `QuotaUsage` type already exists in `types.ts` and matches the HRQ data shape. The overview tab already renders usage bars. The primary change is that the backend now returns real data instead of zeros.

Additional UI improvements:
- Show "Quota enforcement: Active" badge when HRQ exists
- Warning alerts when usage exceeds 80% of limits
- Error alerts when pods fail scheduling due to quota

#### 4.4.2 Plan Selection — Show Quota Impact

**File:** `ui/frontend/src/app/ManageOrganization/Billing/SubscribePlanModal.tsx`

When changing plans, show:
- Current usage vs new plan limits
- Warning if downgrade would exceed new limits
- Confirmation dialog for downgrades that would restrict resources

### Phase 5: Subscription → HRQ Lifecycle & Permission Model

#### 4.5.1 End-to-End Flow: Who Changes What

Users **cannot** modify HRQ or LimitRange directly — they have no RBAC permissions for these resources. All quota changes flow through a **two-stage indirect reconciliation**:

```
User / Stripe
     │
     ▼
┌─────────────────────────────────────────────────────┐
│  Stage 1: Node.js Backend                           │
│  (runs as kube-dc-backend ServiceAccount)           │
│                                                     │
│  Authenticates via:                                 │
│    • User JWT token (UI actions)                    │
│    • Stripe webhook signature (Stripe events)       │
│                                                     │
│  Action: PATCH Organization annotations             │
│    billing.kube-dc.com/plan-id: "pro-pool"          │
│    billing.kube-dc.com/subscription: "active"       │
│    billing.kube-dc.com/addons: [...]                │
└──────────────────────┬──────────────────────────────┘
                       │ annotation change triggers reconcile
                       ▼
┌─────────────────────────────────────────────────────┐
│  Stage 2: Go Organization Controller                │
│  (runs as kube-dc-manager ServiceAccount)           │
│                                                     │
│  Watches: Organization annotation changes           │
│                                                     │
│  Action: Create/Update/Delete HRQ + LimitRange      │
│    based on billing.kube-dc.com/plan-id annotation  │
└─────────────────────────────────────────────────────┘
```

**Why this design:**
- **Security:** Users never touch HRQ/LimitRange directly. Only privileged ServiceAccounts can.
- **Stripe is the source of truth:** Payment confirmation (Stripe webhook) → annotations → HRQ. No payment = no quota.
- **Idempotent:** The Go controller reconciles to desired state on every annotation change. If the backend crashes mid-update, the next reconcile fixes it.
- **Auditable:** All changes flow through Organization annotations with timestamps, visible in `kubectl describe organization`.

#### 4.5.2 Subscription Flows in Detail

**Flow A: New Subscription (user initiates via UI)**

```
1. User clicks "Subscribe to Pro Pool" in UI
2. Frontend POST /api/billing/organization-subscription { planId: "pro-pool" }
3. Backend verifies JWT → isOrgAdmin(token)
4. Backend creates Stripe Checkout Session (redirect to Stripe payment page)
5. User completes payment on Stripe
6. Frontend calls POST /api/billing/verify-checkout { sessionId }
7. Backend retrieves Stripe session, verifies payment/trial status
8. Backend PATCHes Organization annotations (using SA token):
     billing.kube-dc.com/subscription: "active"
     billing.kube-dc.com/plan-id: "pro-pool"
     billing.kube-dc.com/stripe-subscription-id: "sub_..."
9. Go controller sees annotation change → reconciles:
     → Creates HierarchicalResourceQuota (plan-quota) in org namespace
     → Creates LimitRange (default-resource-limits) in org namespace
     → HNC propagates LimitRange to all project namespaces
```

**Flow B: Plan Change (upgrade/downgrade)**

```
1. User clicks "Change to Scale Pool" in UI
2. Frontend PUT /api/billing/organization-subscription { planId: "scale-pool" }
3. Backend verifies JWT → isOrgAdmin(token)
4. Backend updates Stripe subscription (proration applied)
5. Backend PATCHes Organization annotations:
     billing.kube-dc.com/plan-id: "scale-pool"    ← changed
6. Go controller sees plan-id changed → reconciles:
     → Updates HRQ spec.hard with new plan limits
     → Updates auto-managed LimitRange with new plan defaults
       (or skips if user has their own LimitRange)
```

**Flow C: Cancellation**

```
1. User clicks "Cancel Subscription" in UI
2. Frontend DELETE /api/billing/organization-subscription
3. Backend verifies JWT → isOrgAdmin(token)
4. Backend sets Stripe subscription to cancel_at_period_end: true
5. Backend PATCHes Organization annotations:
     billing.kube-dc.com/subscription: "canceling"   ← not "canceled" yet
     billing.kube-dc.com/plan-id: "pro-pool"          ← KEPT until period end
6. Go controller sees status="canceling" → HRQ REMAINS ACTIVE until period end
   (user still has quota for the rest of the paid period)

--- At billing period end (Stripe fires webhook) ---

7. Stripe sends customer.subscription.deleted webhook
8. Backend verifies webhook signature (STRIPE_WEBHOOK_SECRET)
9. Backend PATCHes Organization annotations (using SA token):
     billing.kube-dc.com/subscription: "suspended"    ← grace period starts (see §7.2)
     billing.kube-dc.com/suspended-at: "2026-02-07T..." ← timestamp for grace tracking
     billing.kube-dc.com/plan-id: "pro-pool"           ← KEPT (for restore reference)
10. Go controller sees status="suspended" → reconciles:
      → Sets HRQ to minimal suspended quota (0.5 CPU, 1Gi)
      → Keeps LimitRange (system pods still need defaults)

--- After 7-day grace period (cron/controller timer) ---

11. Controller checks suspended-at timestamp, 7 days passed → transitions:
      billing.kube-dc.com/subscription: "canceled"
      billing.kube-dc.com/plan-id: ""                  ← cleared
      billing.kube-dc.com/addons: []                   ← cleared
12. Go controller sees status="canceled" → reconciles:
      → Keeps minimal HRQ (system pods)
      → Suspends all user workloads (scale to 0, halt VMs)
```

**Flow D: Stripe Webhook Events (server-to-server, no user involved)**

```
Stripe → POST /api/billing/webhook (raw body + Stripe-Signature header)
Backend verifies: stripe.webhooks.constructEvent(body, sig, STRIPE_WEBHOOK_SECRET)
Backend uses: ServiceAccount token (NOT user JWT — no user is present)
```

#### 4.5.3 Webhook Events to Handle

**File:** `ui/backend/controllers/billing/subscriptionController.js` (webhook handler)

> **Note:** The webhook endpoint already exists at `POST /api/billing/webhook` (line ~963 in `subscriptionController.js`). It currently handles `customer.subscription.updated`, `customer.subscription.deleted`, and `invoice.payment_failed`. The raw body is preserved for Stripe signature verification (JSON parsing is skipped for this route in `app.js`).
>
> **CRITICAL:** `checkout.session.completed` is currently **not** handled in the webhook — subscription activation relies solely on the frontend calling `POST /api/billing/verify-checkout` after Stripe redirect. This is the **#1 cause of "I paid but didn't get my upgrade" support tickets** — browsers crash, tabs close, mobile networks drop. Implementing `checkout.session.completed` in the webhook is a **must-have for Phase 1**, not a fallback. The webhook is the only reliable source of truth for payment completion. The frontend verify-checkout should become a secondary fast-path, not the primary activation mechanism.

| Stripe Event | Backend Action | Annotation Change | Go Controller Effect |
|-------------|----------------|-------------------|---------------------|
| `checkout.session.completed` | Activate subscription (**must-have**) | `plan-id`, `subscription: active` | Create HRQ + LimitRange |
| `customer.subscription.updated` | Sync status/trial (exists) | `status`, `is-trial`, `trial-end-date` | Update HRQ if plan changed |
| `customer.subscription.deleted` | Start grace period (exists) | `subscription: suspended`, `suspended-at` timestamp | Set HRQ to minimal suspended quota |
| `invoice.payment_failed` | Log warning (exists) | `subscription: past_due` (optional) | Keep HRQ (grace period) |
| `customer.subscription.trial_will_end` | Send notification (to add) | No annotation change | No HRQ change |

**On cancellation, the HRQ should NOT be deleted immediately (see §7.2 for full lifecycle).** Instead:
1. Mark subscription as `canceling` (existing behavior — keeps `plan-id`, full HRQ)
2. HRQ stays active for the remaining paid period
3. At period end (`customer.subscription.deleted`), set `suspended` → minimal HRQ (7-day grace)
4. After grace period, transition to `canceled` → workloads scaled to 0, data preserved

#### 4.5.4 Add-on Flow (Turbo x1/x2)

```
1. User clicks "Add Turbo x1" in UI
2. Frontend POST /api/billing/organization-subscription/addons { addonId: "turbo-x1" }
3. Backend verifies JWT → isOrgAdmin(token)
4. Backend adds item to Stripe subscription
5. Backend PATCHes Organization annotations:
     billing.kube-dc.com/addons: '[{"addonId":"turbo-x1","quantity":1,...}]'
6. Go controller sees addons changed → reconciles:
     → Reads base plan + addons from annotations
     → Recalculates total resources (plan.cpu + addon.cpu * quantity)
     → Updates HRQ spec.hard with new totals
```

#### 4.5.5 Permission Model (RBAC)

| Actor | Identity | Can Read Organization | Can Patch Annotations | Can Manage HRQ | Can Manage LimitRange |
|-------|---------|----------------------|----------------------|----------------|----------------------|
| **User (org-admin)** | JWT from Dex/OIDC | ✅ | ❌ (via backend only) | ❌ | ❌ (in project ns; can create in org ns) |
| **User (member)** | JWT from Dex/OIDC | ✅ | ❌ | ❌ | ❌ |
| **UI Backend** | `kube-dc-backend` SA | ✅ | ✅ | ❌ | ❌ |
| **Go Controller** | `kube-dc-manager` SA | ✅ | ✅ (status only) | ✅ | ✅ |
| **Stripe Webhook** | Webhook signature | N/A (uses Backend SA) | ✅ (via Backend) | ❌ | ❌ |

**Key RBAC rules to add for HRQ integration:**

```yaml
# kube-dc-manager ServiceAccount — needs HRQ + LimitRange management
- apiGroups: ["hnc.x-k8s.io"]
  resources: ["hierarchicalresourcequotas"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["hnc.x-k8s.io"]
  resources: ["hierarchyconfigurations"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: [""]
  resources: ["limitranges"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

# kube-dc-backend ServiceAccount — reads HRQ status for usage display
- apiGroups: ["hnc.x-k8s.io"]
  resources: ["hierarchicalresourcequotas"]
  verbs: ["get", "list"]
```

**User RBAC (org-admin) — explicitly DENY HRQ writes:**

Users in org-admin group get a Role/RoleBinding in the org namespace that allows managing Projects, VMs, etc. but the Role must **NOT** include `hierarchicalresourcequotas` write permissions. HRQ is managed exclusively by the Go controller.

For LimitRange: org-admin **can** create/update LimitRange in the org namespace (this is the "user override" path from section 3.4.5). They cannot modify HNC-propagated copies in project namespaces (HNC webhook blocks this).

---

## 5. Migration Strategy

### 5.1 Existing Organizations

For organizations that already have projects:

1. **Install HNC** — HNC controller starts watching namespaces
2. **Set hierarchy** — Run migration script or controller reconciliation sets parent on all existing project namespaces
3. **Create HRQ** — Organization controller creates HRQ based on existing billing annotations
4. **Verify** — Check that `kubectl hns tree <org-namespace>` shows correct hierarchy

**Migration script:**
```bash
#!/bin/bash
# For each organization namespace
for org in $(kubectl get organizations -A -o jsonpath='{.items[*].metadata.namespace}'); do
    # For each project in the organization
    for proj_ns in $(kubectl get namespaces -l "kube-dc.com/project" -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "^${org}-"); do
        echo "Setting parent of ${proj_ns} to ${org}"
        kubectl hns set ${proj_ns} --parent ${org}
    done
done
```

### 5.2 New Organizations

New organizations get the hierarchy configured automatically:
1. Organization controller creates org namespace (existing)
2. Project controller creates project namespace (existing)
3. **NEW:** Project controller sets HNC parent on project namespace
4. **NEW:** Organization controller creates HRQ + LimitRange when billing plan is activated
5. **NEW:** HNC propagates LimitRange from org namespace → all project namespaces

---

## 6. Resource Mapping Details

### 6.1 HRQ Resource Types

| HRQ Resource | Plan Field | Notes |
|-------------|-----------|-------|
| `requests.cpu` | `resources.cpu` | Direct mapping (vCPU = Kubernetes CPU) |
| `requests.memory` | `resources.memory` | In GiB → `{N}Gi` |
| `limits.cpu` | `resources.cpu * burst` | Burst ratio: 3x (dev), 2x (pro), 1.5x (scale) |
| `limits.memory` | `resources.memory * burst` | Same per-plan burst ratio |
| `requests.storage` | `resources.storage` | PVC storage in GiB |
| `pods` | Computed | 100 for dev, 200 for pro, 500 for scale |
| `services.loadbalancers` | Computed | 3 for dev, 5 for pro, 10 for scale (LB count, not IPv4) |

> **Note:** Public IPv4 (EIP) quota is enforced separately via the EIP controller — not via HRQ. See §6.4.

### 6.2 Object Storage (Rook-Ceph S3 Quotas)

Object storage (`resources.objectStorage`) is **not enforceable via Kubernetes ResourceQuota/HRQ**. However, Rook-Ceph provides **native CRD-based quota enforcement** via `CephObjectStoreUser`, which fits the same controller-driven pattern as HRQ.

#### 6.2.1 How It Works

Rook-Ceph's `CephObjectStoreUser` CRD has built-in `spec.quotas`:

```yaml
apiVersion: ceph.rook.io/v1
kind: CephObjectStoreUser
metadata:
  name: acme-corp                      # one per organization
  namespace: rook-ceph                 # Rook-Ceph operator namespace
spec:
  store: my-store                      # CephObjectStore name
  displayName: "acme-corp"
  quotas:
    maxBuckets: 10                     # max number of buckets
    maxSize: 20G                       # total size across all buckets (plan: objectStorage)
    maxObjects: 100000                 # max object count (optional safeguard)
  capabilities:
    user: "read"
    bucket: "*"
```

Ceph RGW (RADOS Gateway) enforces these quotas server-side — S3 PUT requests that exceed the quota are rejected with `403 QuotaExceeded`.

#### 6.2.2 Integration Pattern (Same as HRQ)

```
Plan activated → controller creates/updates CephObjectStoreUser with plan quotas
Plan changed  → controller updates maxSize to new plan's objectStorage value
Plan canceled → controller sets maxSize to 0 (or deletes user)
```

**Controller logic in `organization.Sync()`** (alongside HRQ + LimitRange):

```go
// Sync Ceph S3 user quota alongside HRQ
log.V(5).Info("Sync object storage quota...")
if planResources != nil {
    s3Quota := NewOrganizationS3Quota(client, org, planResources.ObjectStorage, log)
    err = s3Quota.Sync(ctx)
    // ...
}
```

**Plan-to-S3 quota mapping:**

| Plan | `maxSize` | `maxBuckets` | `maxObjects` |
|------|----------|-------------|-------------|
| Dev Pool | 20Gi | 5 | 100,000 |
| Pro Pool | 100Gi | 20 | 1,000,000 |
| Scale Pool | 500Gi | 50 | 10,000,000 |

#### 6.2.3 Per-Bucket Quotas via ObjectBucketClaim

Users create buckets via `ObjectBucketClaim` (OBC), which also supports per-bucket quotas:

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: my-bucket
  namespace: acme-corp-dev             # project namespace
spec:
  generateBucketName: acme-corp-dev-
  storageClassName: rook-ceph-bucket
  additionalConfig:
    maxSize: "5G"                      # per-bucket limit (optional, user-configurable)
    maxObjects: "50000"
```

The user-level quota (`CephObjectStoreUser.spec.quotas.maxSize`) enforces the **org-wide total** regardless of per-bucket settings — same relationship as HRQ (org total) vs ResourceQuota (per-project).

#### 6.2.4 RBAC & Credential Management

- **`kube-dc-manager` SA** creates/updates `CephObjectStoreUser` in the `rook-ceph` namespace (needs RBAC for `cephobjectstoreusers` CRD)
- Rook operator auto-creates a **Secret** with S3 credentials (AccessKey, SecretKey) named `rook-ceph-object-user-{store}-{user}`
- Controller copies/references the credentials Secret to the org namespace so projects can access S3
- Users create `ObjectBucketClaim` in project namespaces (standard RBAC, already works with Rook)

#### 6.2.5 Subscription Lifecycle

| Status | S3 Quota Action |
|--------|----------------|
| `active` | `maxSize` = plan's `objectStorage` value |
| `suspended` | `maxSize` = `0` (block new uploads, existing data preserved) |
| `canceled` | `maxSize` = `0`, data preserved during retention period |
| Re-subscribe | `maxSize` restored to new plan value |

> **Confirmed feasible:** Rook-Ceph `CephObjectStoreUser` quotas are enforced server-side by Ceph RGW. The same annotation-driven controller pattern (Stripe → annotations → Go controller → CRD update) works for S3 quotas. Implementation can be done alongside or after HRQ integration.

### 6.3 KubeVirt VM Resources

VMs created via KubeVirt consume CPU/memory from the same quota as pods. The `requests.cpu` and `requests.memory` in the HRQ naturally cover both containers and VMs, since KubeVirt VMs run as pods with `resources.requests` set.

**Important:** Ensure VMs have proper `resources.requests` set, or they won't be counted against the quota. The Kube-DC VM templates should enforce this.

### 6.4 Public IPv4 (EIP) Quota Enforcement

Public IPv4 addresses are allocated via Kube-DC's `EIp` CRD. EIPs with `spec.externalNetworkType: public` (label `network.kube-dc.com/external-network-type: public`) consume real public IPs from the `ext-public` subnet. These are a scarce, paid resource and must be quota-controlled per organization.

#### 6.4.1 Why HRQ Can’t Track EIPs

HRQ/ResourceQuota only tracks standard Kubernetes resources (`pods`, `services.loadbalancers`, `requests.cpu`, etc.). `EIp` is a Kube-DC custom CRD — Kubernetes quota admission doesn't know about it. We need **controller-side enforcement**.

#### 6.4.2 Design: EIP Controller Quota Check

Before creating a new public EIP, the EIP controller counts existing public EIPs across all project namespaces for the organization and compares against the plan's `ipv4` limit.

```go
// In EIP controller Reconcile(), before creating OvnEip:
func checkPublicEIPQuota(ctx context.Context, cli client.Client, projectNamespace string) error {
    // 1. Get organization name from project namespace
    orgName := getOrgFromProjectNamespace(projectNamespace) // e.g., "shalb" from "shalb-demo"

    // 2. Get organization's plan ipv4 limit from annotations
    org := &kubedccomv1.Organization{}
    cli.Get(ctx, types.NamespacedName{Name: orgName, Namespace: orgName}, org)
    planId := org.GetAnnotations()["billing.kube-dc.com/plan-id"]
    plan := GetPlanResources(planId)  // plan.IPv4 = 1 (dev), 1 (pro), 3 (scale)

    // 3. Count public EIPs across ALL project namespaces for this org
    eipList := &kubedccomv1.EIpList{}
    cli.List(ctx, eipList, client.MatchingLabels{
        "network.kube-dc.com/external-network-type": "public",
    })
    // Filter to only EIPs in namespaces belonging to this org
    count := 0
    for _, eip := range eipList.Items {
        if strings.HasPrefix(eip.Namespace, orgName+"-") || eip.Namespace == orgName {
            count++
        }
    }

    // 4. Reject if over quota
    if count >= plan.IPv4 {
        return fmt.Errorf("public EIP quota exceeded: %d/%d for organization %s", count, plan.IPv4, orgName)
    }
    return nil
}
```

**Race condition mitigation:** If multiple EIPs are created simultaneously (e.g., `kubectl apply -f 5-eips.yaml`), parallel reconcile threads could each see `count=0` and all pass the check. Mitigation: the EIP controller uses a **single worker queue per organization** (controller-runtime's default concurrency is 1 per controller). If higher concurrency is configured, add a post-creation recount — after creating the OvnEip, re-list public EIPs and if `count > limit`, delete the just-created EIP and return an error (compensating transaction). In practice, EIP creation is rare and slow (OVN provisioning), so the default single-worker queue is sufficient for MVP.

#### 6.4.3 Plan-to-EIP Quota Mapping

| Plan | `resources.ipv4` | Max Public EIPs | Notes |
|------|-----------------|-----------------|-------|
| Dev Pool | 1 | 1 | 1 public IP (default-gw SNAT + shared LB) |
| Pro Pool | 1 | 1 | 1 public IP |
| Scale Pool | 3 | 3 | 3 public IPs (dedicated LBs, multiple services) |

#### 6.4.4 What Counts as a Public EIP

Only EIPs with **`spec.externalNetworkType: public`** count against the quota. EIPs with `externalNetworkType: cloud` use internal cloud IPs from `ext-cloud` subnet and are **not** quota-controlled (they're free internal addresses).

```
EIP label: network.kube-dc.com/external-network-type: public  → COUNTS against ipv4 quota
EIP label: network.kube-dc.com/external-network-type: cloud   → does NOT count
```

#### 6.4.5 Enforcement Points

| Action | Enforcement |
|--------|------------|
| Project creation (auto-creates `default-gw` EIP) | EIP controller checks quota before creating public EIP |
| User creates additional EIP via kubectl/UI | EIP controller checks quota |
| Service type=LoadBalancer (may allocate EIP) | Service controller / EIP controller checks quota |
| Plan downgrade (e.g., 3 → 1 IPv4) | Existing EIPs **not deleted** — but new ones blocked until under limit |

#### 6.4.6 Subscription Lifecycle

| Status | EIP Quota Action |
|--------|-----------------|
| `active` | Enforce plan's `ipv4` limit |
| `suspended` | Block new public EIPs (limit = 0), keep existing |
| `canceled` | Block new public EIPs, existing released when workloads scaled down |
| No plan | No quota enforcement (or limit = 0 if billing is mandatory) |

#### 6.4.7 Usage Reporting

The backend should expose public EIP count alongside HRQ usage:

```javascript
// In getOrganizationQuotaUsage():
const eipUrl = `https://${global.k8sUrl}/apis/kube-dc.com/v1/eips?labelSelector=network.kube-dc.com/external-network-type=public`;
// Filter by org namespaces, count results
return {
    // ... existing HRQ usage
    ipv4: {
        used: publicEipCount,
        limit: plan.resources.ipv4,
    },
};
```

### 6.5 Kube-DC System Resources

Resources consumed by Kube-DC infrastructure in project namespaces count against the quota. The overhead must be **explicitly accounted for** in `plan_resources.go` so users get the full advertised resources.

**System pod footprint per project namespace:**

| System Pod | CPU Request | Memory Request | Notes |
|-----------|------------|---------------|-------|
| VpcDns (CoreDNS) | 100m | 128Mi | Created by Kube-OVN per project |

**Per-project overhead:** ~100m CPU, ~128Mi memory.

**Recommended implementation in `plan_resources.go`:** Add a fixed overhead per project to the HRQ, based on `OrganizationProjectsLimit` (default: 3):

```
overhead_cpu    = projects_limit * 100m   → 300m for 3 projects
overhead_memory = projects_limit * 128Mi  → 384Mi for 3 projects
```

So if the plan advertises 4 CPU, the HRQ `requests.cpu` should be `4.3` (= 4 + 0.3 overhead). This prevents the scenario where a user sees "4 CPU" in the UI but can only schedule 3.7 CPU of their own workloads. The UI should display the **user-available** amount (plan value), not the raw HRQ value.

---

## 7. Edge Cases & Considerations

### 7.1 Quota Exceeded on Plan Downgrade

If a user downgrades from Scale Pool (16 CPU) to Dev Pool (4 CPU) but is using 10 CPU:
- **HRQ updates immediately** — new limit is 4 CPU
- **Existing pods are NOT evicted** — Kubernetes quotas only block new pod creation
- **New pods will fail** until usage drops below the new limit
- **UI must warn** before downgrade if current usage exceeds new plan

### 7.2 Subscription Expiration & Post-Cancellation Lifecycle

When a subscription ends (trial expires, cancellation at period end, or payment failure), we need a **phased wind-down** — not an instant shutdown. The goal: give users time to re-subscribe or export data, while preventing free-riding.

**Key Kubernetes constraint:** HRQ only blocks **new** pod creation. Setting HRQ to 0 does NOT evict running pods — they keep running until they crash/restart, at which point they can't come back.

#### Phase 1: Warning (before expiration)

No HRQ changes. Notifications only.

| Trigger | Action |
|---------|--------|
| 7 days before period end | Email + in-app notification: "Your subscription expires in 7 days" |
| 3 days before | Email + persistent UI banner |
| 1 day before | Email + UI banner: "Last chance to renew" |
| Trial: `customer.subscription.trial_will_end` | Stripe fires this 3 days before trial ends |

#### Phase 2: Grace Period (7 days after expiration)

**Annotation:** `billing.kube-dc.com/subscription: suspended`

The Go controller sets HRQ to a **minimal "suspended" quota** instead of deleting it:

```yaml
# Suspended quota — enough for system pods only
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HierarchicalResourceQuota
metadata:
  name: plan-quota
  namespace: acme-corp
  labels:
    billing.kube-dc.com/auto-managed: "true"
    billing.kube-dc.com/plan-id: "suspended"
spec:
  hard:
    requests.cpu: "500m"          # ~150m per project for VpcDns + buffer
    requests.memory: "1Gi"        # ~256Mi per project for VpcDns + buffer
    limits.cpu: "1"
    limits.memory: "2Gi"
    requests.storage: "0"         # block new PVCs
    pods: "10"                    # only system pods
    services.loadbalancers: "0"   # block new LBs
```

**What happens to running workloads:**
- Existing pods **keep running** (Kubernetes doesn't evict on quota change)
- If a pod crashes/restarts and there's quota headroom → it comes back
- If quota is exhausted → crashed pods can't restart, user workloads degrade naturally
- System pods (VpcDns) have priority because they're small (~50m CPU, ~64Mi memory each)
- New user workloads blocked (quota too small for anything meaningful)
- New PVCs and LoadBalancers blocked

**User experience:**
- Console shows "Subscription expired — renew to restore full access" banner
- Billing page shows "Suspended" status with one-click re-subscribe
- Users can still view resources, check logs, export data
- Users **cannot** create new VMs, deployments, or PVCs

**Controller logic in `organization.Sync()`:**

```go
subscriptionStatus := org.GetAnnotations()["billing.kube-dc.com/subscription"]

switch subscriptionStatus {
case "active", "trialing", "canceling":
    // Active plan — full HRQ from plan resources
    planResources := GetPlanResourcesFromAnnotations(org)
    // ... create/update HRQ with full limits
case "suspended":
    // Grace period — minimal quota for system pods only
    hrqRes := NewSuspendedHRQ(client, org, log)
    // ... create/update HRQ with suspended limits
case "canceled":
    // Past grace period — scale down + minimal quota
    hrqRes := NewSuspendedHRQ(client, org, log)
    // ... also trigger workload suspension (Phase 3)
default:
    // No subscription — no HRQ (no quota enforcement)
}
```

#### Phase 3: Workload Suspension (after grace period, day 8+)

**Annotation:** `billing.kube-dc.com/subscription: canceled`

The Stripe webhook fires `customer.subscription.deleted` → backend sets status to `canceled`. If the user hasn't re-subscribed within the grace period, the controller **actively suspends workloads**:

| Resource | Action | Reversible? |
|----------|--------|-------------|
| Deployments | Scale replicas to 0, save original in annotation | ✅ Restore on re-subscribe |
| StatefulSets | Scale replicas to 0, save original in annotation | ✅ Restore on re-subscribe |
| VirtualMachines | Set `runStrategy: Halted` | ✅ Set back to `Always` on re-subscribe |
| CronJobs | Set `suspend: true` | ✅ Set back to `false` on re-subscribe |
| PVCs | **Keep** (data preserved) | ✅ Data intact |
| Secrets, ConfigMaps | **Keep** | ✅ Intact |
| Services, Ingress | **Keep** (but no backends) | ✅ Intact |

**Implementation:** New function `SuspendOrgWorkloads(ctx, cli, orgNamespace)` that:
1. Lists all project namespaces for the org
2. For each project namespace, scales down all Deployments/StatefulSets, halts VMs
3. Stores original replica counts in annotations (e.g., `billing.kube-dc.com/pre-suspend-replicas: "3"`)
4. On re-subscribe, `RestoreOrgWorkloads()` reads annotations and restores everything

**HRQ stays at "suspended" quota** — system pods (VpcDns) keep running for network integrity.

#### Phase 4: Data Retention (day 30+, configurable)

After 30 days of `canceled` status with no re-subscription:
- Send final "Data deletion warning" email
- After 60 days: mark org for cleanup
- After 90 days: delete project namespaces and all data (PVCs, secrets, etc.)

> **Design decision:** Data retention period should be configurable per deployment. Cloud providers typically use 30-90 days. Self-hosted Kube-DC may have different policies.

#### Summary: Subscription Status → HRQ State

| `subscription` annotation | HRQ State | Workloads | User Can... |
|--------------------------|-----------|-----------|------------|
| `active` / `trialing` | Full plan quota | Running normally | Create, scale, everything |
| `canceling` | Full plan quota (paid period) | Running normally | Everything (until period end) |
| `suspended` (grace, 7d) | Minimal quota (0.5 CPU, 1Gi) | Running but can't scale/restart | View, export, re-subscribe |
| `canceled` (post-grace) | Minimal quota | Scaled to 0 by controller | View data, re-subscribe |
| `""` (no plan ever) | No HRQ (no enforcement) | Running freely | Everything (no billing) |

#### Stripe → Annotation State Machine

```
checkout.session.completed    → "active"
subscription.updated (active) → "active"
subscription.updated (trial)  → "trialing"
user cancels (period end)     → "canceling"
period ends / trial expires   → "suspended"  ← 7-day grace starts
grace period ends (cron/timer)→ "canceled"   ← workloads suspended
user re-subscribes at any point → "active"   ← full restore
```

> **Note on `suspended` vs `canceled` transition:** Stripe fires `customer.subscription.deleted` at period end. The backend should set `suspended` (not `canceled`) and record `billing.kube-dc.com/suspended-at` timestamp. A separate cron job or controller timer checks if 7 days have passed since `suspended-at` and transitions to `canceled` + triggers workload suspension.

### 7.3 HNC Controller Unavailability

If HNC controller is down:
- HRQ enforcement stops (no admission webhook)
- Existing pods continue running
- New pods may be created without quota checks
- **Mitigation:** HNC should be a critical system component with proper HA

### 7.4 Race Conditions

- Plan change + simultaneous pod creation: HNC admission webhook serializes checks
- Multiple addon additions: Backend should serialize Organization annotation updates
- Organization deletion with active HRQ: Project controller deletes hierarchy first, then org controller cleans up HRQ

### 7.5 HNC and Kube-OVN Interaction

HNC propagates resources from parent to child namespaces. Ensure Kube-OVN CRDs are set to `Ignore` mode in HNC configuration to prevent unwanted propagation of VPCs, Subnets, etc.

---

## 8. API Changes

### 8.1 New Backend Endpoints

```
GET  /api/billing/quota-usage          → Real-time HRQ usage from K8s API
GET  /api/billing/quota-status         → HRQ existence and health check
POST /api/billing/simulate-downgrade   → Check if downgrade is safe (usage < new limits)
```

### 8.2 Organization CRD Changes (Optional)

Consider adding quota status to Organization CRD status:

```go
type OrganizationStatus struct {
    Ready      bool               `json:"ready"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    // NEW: Quota status
    QuotaEnforced bool            `json:"quotaEnforced,omitempty"`
    QuotaPlanId   string          `json:"quotaPlanId,omitempty"`
}
```

---

## 9. Testing Plan

### 9.1 Unit Tests

- Plan-to-HRQ resource mapping
- Addon calculation logic
- Annotation parsing for plan resources

### 9.2 E2E Tests

Add to `tests/e2e/`:

1. **HRQ Creation Test:**
   - Create organization with billing plan annotation
   - Verify HRQ is created with correct limits
   - Create project, verify namespace is child of org namespace

2. **Quota Enforcement Test:**
   - Create organization with Dev Pool plan (4 CPU)
   - Create pods consuming close to 4 CPU across projects
   - Verify next pod fails with quota exceeded error

3. **Plan Upgrade Test:**
   - Start with Dev Pool (4 CPU), upgrade to Pro Pool (8 CPU)
   - Verify HRQ limits are updated
   - Verify previously blocked pods can now be created

4. **Plan Cancellation Test:**
   - Cancel subscription
   - Verify HRQ is removed or set to minimal limits
   - Verify new pod creation is blocked

### 9.3 Manual Testing

- Stripe test mode checkout → verify HRQ creation
- Turbo addon purchase → verify HRQ update
- UI quota usage display with real metrics

---

## 10. Deployment Sequence

### 10.1 Phase Rollout

1. **Phase 1 (Week 1-2):** Install HNC, configure hierarchy for existing namespaces, no enforcement
2. **Phase 2 (Week 3-4):** Add Go controller changes, create HRQ on plan selection, migration script
3. **Phase 3 (Week 5):** Update backend to read real HRQ usage, update UI
4. **Phase 4 (Week 6):** End-to-end testing, Stripe webhook integration
5. **Phase 5 (Week 7):** Production rollout with monitoring

### 10.2 Rollback Plan

- HRQ can be deleted without affecting running workloads
- LimitRange deletion does not affect running pods (only applies at admission time)
- HNC hierarchy can be removed by unsetting parent annotations
- Organization annotations remain unchanged (billing data safe)
- Backend falls back to mock usage if HRQ not found

---

## 11. Files to Create/Modify

### New Files

| File | Purpose |
|------|---------|
| `internal/project/res_hierarchy.go` | HierarchyConfiguration resource for project namespace |
| `internal/organization/res_hrq.go` | HierarchicalResourceQuota resource management |
| `internal/organization/res_limitrange.go` | LimitRange auto-creation with plan defaults, user override detection (§3.4.5) |
| `internal/organization/res_s3_quota.go` | Rook-Ceph CephObjectStoreUser quota management (§6.2) |
| `internal/organization/plan_resources.go` | Plan-to-resource mapping, burst ratios, addon calculation (§3.6) |
| `internal/organization/workload_suspend.go` | SuspendOrgWorkloads / RestoreOrgWorkloads for post-cancellation lifecycle (§7.2) |
| `charts/kube-dc/templates/hnc-install.yaml` | HNC installation manifests |
| `charts/kube-dc/templates/hnc-config.yaml` | HNC configuration (propagation rules for LimitRange, RBAC, Secrets) |
| `charts/kube-dc/templates/hnc-rbac.yaml` | RBAC for kube-dc-manager to manage HNC + LimitRange + CephObjectStoreUser |
| `tests/e2e/quota_test.go` | E2E tests for quota enforcement |
| `hack/migrate-hierarchy.sh` | Migration script for existing organizations |
| `examples/organization/04-org-defaults.yaml` | Example: Standalone LimitRange + HRQ reference (hybrid approach) |

### Modified Files

| File | Change |
|------|--------|
| `internal/project/project.go` | Add HierarchyConfiguration sync step after namespace creation |
| `internal/organization/organization.go` | Add HRQ + LimitRange + S3 quota sync steps, read plan from annotations |
| `internal/controller/kube-dc.com/organization_controller.go` | Watch billing annotation changes (`plan-id`, `addons`, `subscription`), trigger reconcile |
| `internal/controller/kube-dc.com/eip_controller.go` | Add public EIP quota check before OvnEip creation (§6.4) |
| `ui/backend/controllers/billing/subscriptionController.js` | Read real HRQ usage, add `checkout.session.completed` webhook handler, set `suspended` on deletion |
| `ui/frontend/src/app/ManageOrganization/Billing/Billing.tsx` | Show real usage, quota alerts, suspension banner |
| `ui/frontend/src/app/ManageOrganization/Billing/SubscribePlanModal.tsx` | Downgrade warning with current usage |
| `ui/frontend/src/app/ManageOrganization/Billing/types.ts` | Add quota status types |
| `go.mod` | Add HNC API + Rook-Ceph API dependencies |
| `Makefile` | Add HNC CRD generation targets |

### Go Dependencies to Add

```
sigs.k8s.io/hierarchical-namespaces/api v1.1.0-pfnet.10
github.com/rook/rook/pkg/apis                        # For CephObjectStoreUser CRD (§6.2)
```

---

## 12. Monitoring & Observability

### 12.1 Metrics

- `kube_dc_hrq_usage_ratio` — Current usage / limit per resource type per organization
- `kube_dc_hrq_sync_errors` — Count of HRQ sync failures
- `kube_dc_plan_changes_total` — Count of plan changes (upgrade/downgrade/cancel)

### 12.2 Alerts

- Organization usage > 80% of HRQ limit → Warning notification
- Organization usage > 95% of HRQ limit → Critical notification
- HRQ sync failure → Controller error alert
- HNC controller down → System alert

### 12.3 Logging

- HRQ create/update/delete events in organization controller
- Quota exceeded events surfaced to UI
- Plan change audit trail in Organization annotations (already exists via `billing.kube-dc.com/subscribed-at`)

---

## 13. Security Considerations

### 13.1 Authentication Layers

| Entry Point | Authentication Method | Identity |
|------------|----------------------|----------|
| UI API endpoints | JWT token (Dex/OIDC) | User (org-admin or member) |
| Stripe webhook | `Stripe-Signature` header verified via `STRIPE_WEBHOOK_SECRET` | Stripe server |
| Go controller | In-cluster ServiceAccount (`kube-dc-manager`) | Controller process |
| Backend K8s calls | ServiceAccount token (`/var/run/secrets/.../token`) | Backend pod |

### 13.2 RBAC Separation

- **HRQ is controller-managed only:** Users have no RBAC verbs on `hierarchicalresourcequotas`. Only `kube-dc-manager` SA can create/update/delete HRQ.
- **Billing annotations are backend-managed:** The `kube-dc-backend` SA can PATCH Organization metadata.annotations. Users cannot — their JWT-based RBAC does not include Organization write permissions on billing annotations.
- **LimitRange in org namespace:** org-admin users **can** create/update LimitRange in the org namespace (standard K8s RBAC). This is intentional — it's the "user override" path. However, they **cannot** modify HNC-propagated copies in project namespaces (HNC admission webhook blocks it).
- **HNC hierarchy:** Only `kube-dc-manager` SA can create/update `HierarchyConfiguration` objects. Users cannot re-parent namespaces.
- **Admission webhooks:** HNC webhook must be highly available. If webhook is down, Kubernetes API server rejects HNC resource mutations (fail-closed by default).

### 13.3 Stripe Webhook Security

- Webhook endpoint (`POST /api/billing/webhook`) verifies every event using `stripe.webhooks.constructEvent(body, sig, STRIPE_WEBHOOK_SECRET)`
- Raw body is preserved (Express JSON parsing is skipped for this route) — required for signature verification
- `STRIPE_WEBHOOK_SECRET` is stored as a Kubernetes Secret, mounted as env var in the backend pod
- Webhook handler uses **ServiceAccount token** (not user JWT) because there is no user context in server-to-server calls
- Failed signature verification returns 400 immediately — no annotation changes

### 13.4 Threat Mitigations

| Threat | Mitigation |
|--------|-----------|
| User directly creates HRQ to get more quota | RBAC: no write verbs on HRQ for user roles |
| User patches billing annotations to fake a plan | RBAC: users cannot PATCH Organization annotations |
| Forged Stripe webhook to activate a free plan | Stripe signature verification rejects forged events |
| User deletes LimitRange in project namespace | HNC admission webhook blocks deletion of propagated objects |
| User modifies HNC hierarchy to escape quota | RBAC: no write verbs on HierarchyConfiguration for user roles |
| Backend SA token leaked | Limit SA scope to only Organization PATCH + HRQ read; rotate regularly |

---

## 14. Open Questions

1. ~~**Free tier limits:**~~ **Resolved (see §7.2):** Organizations without a plan get **no HRQ** (no enforcement). After subscription expires, HRQ transitions to a minimal "suspended" quota (0.5 CPU, 1Gi) that allows only system pods (VpcDns). After grace period, workloads are actively scaled to 0.
2. ~~**Grace period on cancellation:**~~ **Resolved (see §7.2):** 4-phase lifecycle: `canceling` (paid period, full quota) → `suspended` (7-day grace, minimal quota) → `canceled` (workloads scaled to 0, data preserved) → cleanup (90 days, configurable).
3. ~~**Per-project sub-quotas:**~~ **Resolved (see §3.5):** Org-admin can create standard Kubernetes `ResourceQuota` in project namespaces. Coexists with HRQ — most restrictive limit wins. No custom CRDs needed. RBAC grants org-admin `resourcequotas` write in project namespaces. UI integration optional for MVP.
4. ~~**Object storage enforcement:**~~ **Resolved (see §6.2):** Rook-Ceph `CephObjectStoreUser` CRD has native `spec.quotas` (maxSize, maxBuckets, maxObjects). Ceph RGW enforces server-side. Same controller pattern as HRQ — plan annotation → Go controller → update CephObjectStoreUser quota. Implementation can be done alongside or after HRQ.
5. ~~**IPv4 tracking:**~~ **Resolved (see §6.4):** Public EIPs (`spec.externalNetworkType: public`, label `network.kube-dc.com/external-network-type: public`) are quota-controlled via EIP controller-side check — count public EIPs across org's project namespaces, compare against plan's `ipv4` limit. Cloud EIPs are not counted. No custom quota resource needed — enforced in Go controller before OvnEip creation.
6. ~~**Burst ratios:**~~ **Resolved (see §3.6):** Per-plan burst ratios: Dev Pool 3x (bursty dev workloads), Pro Pool 2x (balanced), Scale Pool 1.5x (production predictability). Configured via `BURST_RATIOS` map in planToHRQ mapping.

---

## 15. References

- [pfnet/hierarchical-namespaces](https://github.com/pfnet/hierarchical-namespaces) — Active HNC fork
- [HNC Concepts Guide](https://github.com/kubernetes-retired/hierarchical-namespaces/blob/master/docs/user-guide/concepts.md)
- [HierarchicalResourceQuota API](https://github.com/pfnet/hierarchical-namespaces/tree/master/api/v1alpha2)
- [Kubernetes ResourceQuota](https://kubernetes.io/docs/concepts/policy/resource-quotas/)
- [Stripe Billing Webhooks](https://stripe.com/docs/billing/subscriptions/webhooks)
