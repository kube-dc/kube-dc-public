---
name: check-quota
description: Check organization and project resource quota usage before deploying workloads. Covers org-level quota (CPU, memory, storage, pods, public IPv4, object storage) and per-project usage via Organization and Project status fields. Use this before creating VMs, apps, databases, clusters, or EIPs to avoid quota-exceeded errors.
---

## When to Use This Skill

Run a quota check **before**:
- Creating a VM (CPU + memory + storage consumption)
- Deploying an application (CPU + memory + pods)
- Creating a managed Kubernetes cluster (large CPU + memory + storage)
- Provisioning a database (storage + pods)
- Allocating a public EIP (publicIPv4 limit)

Also use for **troubleshooting** when workloads fail with `exceeded quota` errors.

---

## Organization-Level Quota

The `Organization` resource exposes aggregated usage across all projects in `.status.quotaUsage`. Values are refreshed every 5–7 minutes by the platform controller.

```bash
kubectl get organization {org} -n {org} \
  -o jsonpath='{.status.quotaUsage}' | jq .
```

Expected output:

```json
{
  "cpu":           { "used": "18.975", "hard": "26" },
  "memory":        { "used": "63.6Gi", "hard": "70Gi" },
  "storage":       { "used": "443.2Gi","hard": "460Gi" },
  "pods":          { "used": "33",     "hard": "500" },
  "publicIPv4":    { "used": "3",      "hard": "3" },
  "objectStorage": { "used": "",       "hard": "500Gi" },
  "lastUpdated":   "2026-04-07T20:55:42Z"
}
```

**Field reference:**
- `cpu` — cores (decimal). e.g. `"18.975"` = 18,975 millicores
- `memory` / `storage` — GiB consumed vs plan hard limit
- `publicIPv4` — count of `externalNetworkType: public` EIPs across all org namespaces
- `objectStorage` — hard limit from plan; `used` is populated asynchronously
- `lastUpdated` — timestamp of last controller refresh (up to 7 min old)

Check a single field:

```bash
# CPU remaining
kubectl get organization {org} -n {org} \
  -o jsonpath='{.status.quotaUsage.cpu}' | jq .
# → { "hard": "26", "used": "18.975" }
```

---

## Project-Level Quota

Each `Project` resource exposes per-namespace usage in `.status.quotaUsage`:

```bash
kubectl get project {project} -n {org} \
  -o jsonpath='{.status.quotaUsage}' | jq .
```

Expected output:

```json
{
  "cpu":               { "used": "6.72",    "hard": "26" },
  "memory":            { "used": "16.824Gi","hard": "70Gi" },
  "storage":           { "used": "147.4Gi", "hard": "460Gi" },
  "pods":              { "used": "12",      "hard": "500" },
  "perProjectQuotaSet": false,
  "lastUpdated":       "2026-04-07T20:55:00Z"
}
```

- `hard` shows the **org-wide limit** when `perProjectQuotaSet: false`, or the **per-project cap** when set by an admin
- `perProjectQuotaSet: true` means this project has an explicit `ResourceQuota/project-quota` that may be tighter than the org limit

All projects at a glance:

```bash
kubectl get projects -n {org} \
  -o custom-columns='PROJECT:.metadata.name,CPU_USED:.status.quotaUsage.cpu.used,CPU_HARD:.status.quotaUsage.cpu.hard,MEM_USED:.status.quotaUsage.memory.used,MEM_HARD:.status.quotaUsage.memory.hard'
```

---

## Real-Time Enforcement State

`quotaUsage` is refreshed every 5–7 min. For the live enforcement state (what Kubernetes is actively enforcing right now), query the underlying `ResourceQuota` objects:

```bash
# All quotas in a project namespace
kubectl get resourcequota -n {org}-{project}

# Detailed breakdown with usage bars
kubectl describe resourcequota -n {org}-{project}
```

The `hrq.hnc.x-k8s.io` quota is the organization-wide HNC propagated limit. The `project-quota` quota (if present) is the per-project cap set by an admin.

> **Note**: `kubectl describe resourcequota` shows raw Kubernetes units — millicores for CPU (e.g. `6720m`) and bytes for memory/storage (e.g. `18064129473`). Use `.status.quotaUsage` on the `Project` or `Organization` resource for human-readable values.

---

## Interpreting Results

### Sufficient capacity

```
cpu:     used=6.72  / hard=26    → 19.28 cores free ✅
memory:  used=16Gi  / hard=70Gi  → 54Gi free        ✅
storage: used=147Gi / hard=460Gi → 313Gi free       ✅
```

Proceed with the deployment.

### Near limit — warn user

Any field where `used / hard > 0.8` (80%) is worth flagging before proceeding with large workloads.

### At or over limit — action required

```
publicIPv4: used=3 / hard=3  → 0 free ❌
```

Options:
- **Free existing resources**: delete unused EIPs, VMs, or pods
- **Add a Turbo Add-on**: navigate to Manage Organization → Billing → Turbo Add-ons
- **Upgrade plan**: for publicIPv4, upgrade to Scale Pool (3 IPs) or higher

---

## Troubleshooting: "exceeded quota" Errors

When a workload fails with:

```
exceeded quota: plan-quota, requested: requests.cpu=500m,
used: requests.cpu=25500m, limited: requests.cpu=26
```

1. Run org quota check to find the exhausted resource:
   ```bash
   kubectl get organization {org} -n {org} -o jsonpath='{.status.quotaUsage}' | jq .
   ```
2. Run project quota check to see which project is consuming the most:
   ```bash
   kubectl get projects -n {org} \
     -o custom-columns='PROJECT:.metadata.name,CPU:.status.quotaUsage.cpu.used,MEM:.status.quotaUsage.memory.used'
   ```
3. Free capacity or expand quota before retrying.

---

## Quota Limits by Plan

| Resource | Dev Pool | Pro Pool | Scale Pool |
|----------|----------|----------|------------|
| CPU (requests) | 4 cores | 8 cores | 16 cores |
| Memory | 8 Gi | 24 Gi | 56 Gi |
| Storage | 60 Gi | 160 Gi | 320 Gi |
| Pods | 100 | 200 | 500 |
| Public IPv4 | 1 | 1 | 3 |
| Object Storage | 20 Gi | 100 Gi | 500 Gi |

Turbo x1 adds: +2 CPU, +4 Gi RAM, +20 Gi storage (€9/mo).
Turbo x2 adds: +4 CPU, +8 Gi RAM, +40 Gi storage (€16/mo).

---

## Safety

- Always check quota **before** creating resource-heavy workloads (clusters, VMs with large disks)
- `publicIPv4` quota is hard — no burst. Check before allocating any `externalNetworkType: public` EIP
- `quotaUsage.lastUpdated` may be up to 7 minutes stale; use `kubectl describe resourcequota` for real-time data when timing matters
