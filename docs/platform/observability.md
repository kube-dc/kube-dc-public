# Observability — Platform Operator Guide

**Audience:** Cluster operators and platform engineers  
**Scope:** Day-2 operations for the shared observability stack  
**Related internal doc:** `docs/internal/observability-architecture.md` (implementation detail)

---

## 1. Overview

The platform ships a **shared, multi-tenant observability stack** covering metrics, logs,
and alerting for all Kube-DC Organizations on a cluster. The key properties from an
operator perspective:

- **Single deployment, full tenant isolation.** One Grafana, one Mimir (metrics), one
  Loki (logs), one Alertmanager — all multi-tenant. Each Kube-DC Organization sees only
  its own data.
- **Namespace = tenant.** Every Kubernetes namespace is automatically a metrics and logs
  tenant. No registration, no mapping table. A new Project namespace begins appearing in
  its Organization's dashboards within a few minutes of creation.
- **GitOps managed.** All configuration (component versions, dashboards, alert rules,
  capacity limits) lives in `kube-dc-fleet`. Apply changes by committing and letting Flux
  reconcile.
- **Tenant isolation is automatic.** When `kube-dc-manager` reconciles an Organization,
  it provisions that Org's Grafana workspace with the correct scope. Operators do not
  need to configure per-tenant anything — it is managed by the controller.

### 1.1 Component overview

| Component | Role |
|---|---|
| **Mimir** | Multi-tenant time-series store. Metrics are stored per namespace-tenant and queryable with cross-namespace federation for Org admins. |
| **Loki** | Multi-tenant log store. Pod logs from every node are automatically routed to the correct tenant. |
| **Alertmanager** (Mimir's) | Multi-tenant alerting. Each Org manages its own alert routes via the Grafana UI. |
| **Grafana** | Single deployment with one Grafana Organization per Kube-DC Org. Platform admins log in via SSO; tenant users via the kube-dc console. |
| **kube-prometheus-stack** | Prometheus hot tier (365 d local retention) plus all cluster-level alerting rules and Grafana deployment. |
| **Alloy** | Agent DaemonSet (logs) and Deployment (metrics scraper). Runs on every node. |

### 1.2 Fleet repo layout

All observability configuration lives under `kube-dc-fleet`:

```
platform/monitoring/
  mimir/                  # Mimir HelmRelease + values
  loki/                   # Loki HelmRelease + values
  prom-operator/          # kube-prom-stack HelmRelease (Grafana + Prometheus + Alertmanager)
  alloy/                  # Alloy DaemonSet (logs)
  alloy-metrics/          # Alloy Deployment (metrics)
  cortex-tenant/          # Metrics write-path proxy
  grafana-pg/             # CNPG PostgreSQL backend for Grafana
  observability-routes/   # Envoy Gateway HTTPRoutes + auth policies
  routes/                 # Grafana ingress HTTPRoute
  dashboards/             # Platform dashboards (JSON + kustomize)

clusters/cloud/platform/
  kustomization.yaml      # Per-cluster overlay (grafana.ini, secrets)
```

---

## 2. Tenant model

### 2.1 How tenants map to data

| Identity | What it sees |
|---|---|
| Kube-DC Organization | All metrics and logs from all its project namespaces, federated into one view |
| Project namespace | Metrics and logs scoped to that namespace only |
| `system` tenant | Platform infrastructure namespaces (`monitoring`, `kube-system`, etc.) — visible to platform admins only |

Tenant routing is **automatic**:
- Metrics: the `namespace` label on every scraped series determines which tenant receives it.
- Logs: pod log streams are tagged by namespace at collection time.
- System namespaces (`monitoring`, `kube-system`, `kube-dc`, `cnpg-system`, `kubevirt`, etc.)
  are collapsed into the `system` tenant. User-created namespaces that happen to share a name
  with a system namespace cannot land in the `system` tenant — the system namespace list is
  an explicit allowlist, not a regex.

### 2.2 Adding/removing a project

No action required. When a Project is created or deleted, the `kube-dc-manager` controller
automatically updates the Organization's Grafana datasource scope. The new project namespace
appears in the Org's metrics/logs within seconds.

---

## 3. Dashboards

### 3.1 How dashboards work

Platform dashboards are stored as JSON files in
`kube-dc-fleet/platform/monitoring/dashboards/` and deployed as Kubernetes ConfigMaps via
Kustomize. The `kube-dc-manager` controller distributes them into the appropriate Grafana
Organization(s) on every reconcile.

Dashboards are **declarative**: the Git JSON is the source of truth. Manual edits made in the
Grafana UI are overwritten on the next reconcile. Tenants who want custom dashboards should
create new ones within their Grafana Org (those are persisted in the Grafana DB and are
not touched by the controller).

### 3.2 Currently shipped dashboards

| Dashboard | Grafana folder | Shown to |
|---|---|---|
| Namespace Resource Usage | Tenant | All tenant Orgs (home dashboard) |
| Logs Explorer | Tenant | All tenant Orgs |
| Active Alerts | Tenant | All tenant Orgs |

### 3.3 Adding a platform dashboard

1. **Create the dashboard JSON.**  
   Export from Grafana (`Share → Export → Save to file`) or author from scratch.  
   Requirements:
   - Must have a stable, unique `uid` field (e.g. `kube-dc-my-new-dashboard`).
   - Use the well-known datasource UIDs — do not embed a datasource name:
     - `mimir` — federated metrics (queries across all Org namespaces)
     - `mimir-alerts` — alert rule management (single-tenant)
     - `loki` — federated logs
     - `alertmanager` — Alertmanager API
   - Use `$namespace` template variable populated via
     `label_values(kube_pod_info, namespace)` (not `kube_namespace_created`, which
     includes terminated namespaces).
   - Set `"editable": false` for platform-owned dashboards.

2. **Add the JSON to the fleet repo.**
   ```
   kube-dc-fleet/platform/monitoring/dashboards/<my-dashboard>.json
   ```

3. **Register it in the kustomization.**  
   Edit `kube-dc-fleet/platform/monitoring/dashboards/kustomization.yaml` and add an
   entry under `configMapGenerator`:
   ```yaml
   - name: dashboard-my-new-dashboard
     files:
       - my-dashboard.json
     options:
       labels:
         kube-dc.com/grafana-dashboard: "true"
       annotations:
         kube-dc.com/grafana-folder: "Tenant"
         kube-dc.com/grafana-scope: "all-tenants"   # see §3.4
   ```

4. **Commit and push.** Flux reconciles the ConfigMap into the cluster (usually < 1 min).
   The controller picks it up on the next Org reconcile (usually within 30 s of the
   ConfigMap becoming visible).

5. **Force immediate distribution** if needed:
   ```bash
   kubectl annotate organization <org-name> reconcile-trigger="$(date)" --overwrite -n <org-name>
   ```

### 3.4 Scope annotation

Control which Grafana Organizations receive a dashboard via the
`kube-dc.com/grafana-scope` annotation:

| Value | Distributed to |
|---|---|
| `all-tenants` (default) | Every tenant Grafana Org |
| `main-only` | Grafana Org 1 only (platform admin view) |
| `tenants:acme,foo,bar` | Explicit list of Kube-DC Org slugs |

### 3.5 Setting a home dashboard

Add the annotation `kube-dc.com/grafana-home: "true"` to a dashboard's ConfigMap entry.
Only one dashboard per scope should carry this annotation; if multiple do, the last one
wins (alphabetical ConfigMap order).

### 3.6 Editing an existing dashboard

Edit the JSON file in `kube-dc-fleet/platform/monitoring/dashboards/`, commit, and push.
Keep the `uid` field unchanged — changing the UID causes a new dashboard to be created
and the old one to remain (orphaned). If you need to retire a dashboard, delete the JSON
and ConfigMap entry; then manually delete it from Grafana Orgs or wait for the next full
re-provision.

---

## 4. Alert rules

### 4.1 Where rules live

| Rule type | Storage location | Who manages |
|---|---|---|
| **Platform rules** (cluster health, node, kube-system) | Prometheus PrometheusRules, evaluated by kube-prometheus-stack Prometheus | Managed via `prom-operator/values-configmap.yaml` or additional PrometheusRule CRDs |
| **Tenant alert rules** | Mimir Ruler, stored per-tenant in S3 | Tenant users (Grafana Alert Rules UI) or operators via `mimirtool` |

### 4.2 Adding or changing platform alert rules

Platform alerting rules are part of the `kube-prometheus-stack` chart. Add a
`PrometheusRule` manifest in `kube-dc-fleet/platform/monitoring/prom-operator/` (or via a
kustomize overlay) and commit. The kube-prometheus-stack sidecar picks up the CRD within
seconds.

Example — adding a custom platform rule:
```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: platform-custom-rules
  namespace: monitoring
  labels:
    release: prom-operator    # must match the HelmRelease label selector
spec:
  groups:
    - name: platform.custom
      interval: 1m
      rules:
        - alert: MyAlert
          expr: |
            sum(kube_pod_container_status_restarts_total{namespace="kube-dc"}) > 50
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "High restart count in kube-dc"
```

### 4.3 Managing tenant alert rules via `mimirtool`

For bulk operations across tenants (e.g. pushing a common baseline rule set):

```bash
# List all rule groups for a tenant
mimirtool rules list \
  --address https://mimir-ruler.kube-dc.cloud \
  --id <tenant-namespace>

# Upload rule groups for a tenant
mimirtool rules load ./rules/my-rules.yaml \
  --address https://mimir-ruler.kube-dc.cloud \
  --id <tenant-namespace>
```

Authentication requires a valid Keycloak access token from the `master` realm (platform
admin group). See the `kube-dc` CLI docs for obtaining tokens.

### 4.4 Default Alertmanager routing

Each Kube-DC Org has its own Alertmanager configuration managed via the Grafana UI
(Contact Points and Notification Policies). Platform-level Alertmanager config (for the
`system` tenant) is set in `prom-operator/values-configmap.yaml` under
`alertmanager.config`.

---

## 5. Capacity tuning

### 5.1 Mimir per-tenant limits

Limits are configured in `kube-dc-fleet/platform/monitoring/mimir/values-configmap.yaml`
under `mimir.structuredConfig.limits`. Changes apply on HelmRelease upgrade (Flux
reconcile).

Current operational values and their meaning:

| Limit key | Current value | What it controls |
|---|---|---|
| `max_global_series_per_user` | 250 000 | Maximum active time series per tenant. A tenant hitting this sees ingestion rejections. |
| `ingestion_rate` | 50 000 | Samples per second per tenant. |
| `ingestion_burst_size` | 200 000 | Burst ceiling over `ingestion_rate`. |
| `compactor_blocks_retention_period` | `30d` | How long metric data is retained in cold storage. |
| `max_label_names_per_series` | 30 | Label cardinality per series. |

To set a per-tenant override (e.g. raise the series limit for a high-cardinality Org):

```yaml
# in mimir/values-configmap.yaml, under mimir.structuredConfig:
overrides:
  shalb-docs:
    max_global_series_per_user: 500000
```

Commit, push, and Flux will roll out the updated Mimir config.

### 5.2 Loki per-tenant limits

In `kube-dc-fleet/platform/monitoring/loki/values-configmap.yaml` under
`loki.limits_config`:

| Limit key | Current value | What it controls |
|---|---|---|
| `ingestion_rate_mb` | 10 | MB/s log ingestion per tenant. |
| `ingestion_burst_size_mb` | 20 | Burst ceiling. |
| `max_streams_per_user` | 10 000 | Maximum concurrent label-set streams per tenant. |
| `retention_period` | `30d` | How long log data is retained. |

Per-tenant Loki overrides follow the same pattern as Mimir:
```yaml
# in loki/values-configmap.yaml, under loki.limits_config.per_tenant_override_config or
# via loki.runtimeConfig (preferred in newer Loki versions)
```

### 5.3 Grafana database

Grafana uses a PostgreSQL backend (`grafana-pg` CNPG cluster in `monitoring/`). The PVC
is sized at `10Gi`. At current tenant scale (~60 Orgs × 10 dashboards × 50 users),
actual usage is well under 100 MB. No operator action is needed for routine growth.

Backup schedule: daily at 02:00 UTC, 14-day retention, stored in S3 (same cluster Ceph
object store). To trigger a manual backup:
```bash
kubectl annotate -n monitoring scheduledbackup grafana-pg-backup \
  cnpg.io/immediateBackup="true"
```

### 5.4 Storage buckets

Mimir and Loki use dedicated S3 buckets via Ceph RGW. Bucket names and quotas are
provisioned via OBC resources in `kube-dc-fleet`. To check current usage:

```bash
# List all observability OBC buckets
kubectl get obc -n monitoring

# Check a specific bucket
kubectl get obc -n monitoring mimir-blocks -o yaml
```

System-level bucket quotas are configured in `clusters/cloud/cluster-config.env`:
```
SYSTEM_QUOTA_MIMIR_BLOCKS=200G
SYSTEM_QUOTA_LOKI_CHUNKS=200G
```

---

## 6. Grafana administration

### 6.1 Logging in as a platform admin

Navigate to `https://grafana.<domain>/login` and click **Sign in with Keycloak**. This
uses the `master` realm. Platform admins (members of the `kube-dc-admin` group) receive
`GrafanaAdmin` role and can switch into any tenant Grafana Organization via the
**Org switcher** in the top navigation bar.

### 6.2 Inspecting a tenant's Grafana Org

1. Log in as platform admin (§6.1).
2. Open the **Organization switcher** (top-left globe icon or via `Admin → Organizations`).
3. Switch to the target Org. You now see the tenant's dashboards, datasources, and alert
   rules exactly as they do.
4. **Switch back** to `Main Org.` when finished to avoid accidental edits in the tenant
   space.

### 6.3 Manually triggering Org re-provision

If a Org's dashboards, datasources, or membership is stale, force a reconcile:

```bash
kubectl annotate organization <org-slug> \
  reconcile-trigger="$(date)" --overwrite -n <org-slug>
```

Watch the `kube-dc-manager` logs for the result:
```bash
kubectl logs -n kube-dc deployment/kube-dc-manager -c manager --follow | grep <org-slug>
```

### 6.4 Token lifetime

Keycloak access tokens are set to **15 minutes** for both the `master` realm and all
per-Organization realms. This value is configured in:
- `kube-dc-fleet/scripts/bootstrap-keycloak.sh` (master realm, applied at bootstrap)
- `internal/organization/helpers.go` `DefaultKeycloakAccessTokenLifespan` constant
  (per-org realms, applied on Organization reconcile)

Increasing the TTL reduces Keycloak load but widens the window for a stolen token.
Decreasing it below 5 minutes causes noticeable friction for kubectl users.

---

## 7. Troubleshooting

### 7.1 Tenant sees no data in dashboards

1. **Verify they are in the right Org.** In Grafana, the Org name appears in the top-left.
   It should match their Kube-DC Organization slug. If it doesn't, the user's Org
   membership was not provisioned — trigger a re-provision (§6.3).

2. **Check the Mimir datasource.** As platform admin, switch to the tenant Org and open
   **Configuration → Datasources**. The `Mimir` datasource should exist and the
   **Custom HTTP Headers** section should show an `X-Scope-OrgID` value containing the
   Org name and its project namespaces (e.g. `shalb|shalb-docs|shalb-jumbolot`).
   If it shows only the Org name and no projects, the datasource header hasn't been
   updated since the projects were created — trigger a re-provision (§6.3).

3. **Check metrics are being written.** Look for the tenant in `kube-dc-manager` logs:
   ```bash
   kubectl logs -n monitoring \
     -l app.kubernetes.io/component=distributor \
     --tail=100 | grep <namespace>
   ```
   No lines → metrics from that namespace are not reaching Mimir. Check Alloy:
   ```bash
   kubectl logs -n monitoring \
     -l app.kubernetes.io/name=alloy-metrics --tail=100
   ```

### 7.2 Alert Rules tab shows 404 or is empty

This happens when the `Mimir Alerts` datasource (the single-tenant alerting datasource)
is missing or misconfigured. As platform admin:

1. Switch to the affected tenant Org.
2. Open **Configuration → Datasources** and confirm `Mimir Alerts` exists alongside `Mimir`.
3. If it's missing, trigger a re-provision (§6.3).

Also verify the `mimir-ruler` deployment is running:
```bash
kubectl get pods -n monitoring -l app.kubernetes.io/component=ruler
```

### 7.3 Dashboards missing from a tenant Org

Dashboards are distributed on every Org reconcile. If they're missing after a new
dashboard was added to the fleet repo:

1. Confirm Flux has reconciled the ConfigMap:
   ```bash
   flux get kustomization platform -n flux-system
   kubectl get cm -n monitoring -l kube-dc.com/grafana-dashboard=true
   ```
2. Trigger a re-provision (§6.3).
3. Check `kube-dc-manager` logs for `ensureDashboards` errors — the most common cause is
   a dashboard JSON missing the `uid` field.

### 7.4 Grafana pod keeps restarting

Check the CNPG `grafana-pg` cluster status first — Grafana will crash-loop if the
PostgreSQL backend is unavailable:
```bash
kubectl get cluster -n monitoring grafana-pg
kubectl get pods -n monitoring -l cnpg.io/cluster=grafana-pg
```

If PostgreSQL is healthy, check Grafana pod events and logs:
```bash
kubectl describe pod -n monitoring -l app.kubernetes.io/name=grafana
kubectl logs -n monitoring -l app.kubernetes.io/name=grafana --tail=50
```

### 7.5 Alloy / metrics pipeline

Check each layer:
```bash
# Alloy metrics scraper
kubectl logs -n monitoring -l app.kubernetes.io/name=alloy-metrics --tail=50

# cortex-tenant proxy (translates __tenant_id__ to per-tenant writes)
kubectl logs -n monitoring -l app.kubernetes.io/name=cortex-tenant --tail=50

# Mimir distributor
kubectl get pods -n monitoring -l app.kubernetes.io/component=distributor
```

If `cortex-tenant` is OOMKilled, increase its memory limit in
`platform/monitoring/cortex-tenant/values-configmap.yaml`. The observed steady-state
is ~150-200 MiB; allow at least 1Gi to absorb pipeline restarts.

### 7.6 Mimir ring health alerts

The `MimirIngesterRingMembersMismatch` and `MimirRingMembersMismatch` alerts fire when
ingester or ruler pod counts diverge from what the ring expects. Usually transient during
rolling restarts. If persistent:
```bash
kubectl get pods -n monitoring -l app.kubernetes.io/component=ingester
kubectl get pods -n monitoring -l app.kubernetes.io/component=ruler
```
Look for `Pending` or `CrashLoopBackOff` pods.

---

## 8. Component upgrades

All component versions are pinned in `clusters/cloud/cluster-config.env`. Upgrade
procedure for each component:

| Component | Variable | Notes |
|---|---|---|
| kube-prometheus-stack (Grafana + Prometheus) | `PROM_OPERATOR_VERSION` | Test in staging first. Grafana major versions may require DB schema migration. |
| Mimir | `MIMIR_VERSION` | Check Mimir upgrade docs for breaking compactor/store-gateway changes. |
| Loki | `LOKI_VERSION` | Check chunk format compatibility; breaking changes require re-ingest. |
| Alloy | `ALLOY_VERSION` | Validate log pipeline config syntax after upgrade. |

**General process:**
1. Bump the version in `cluster-config.env`.
2. Commit and push.
3. Flux reconciles the HelmRelease.
4. Verify pods come healthy: `kubectl get pods -n monitoring`.
5. Check the observability dashboards for any ingestion gap.

Do **not** bump multiple stateful components (Mimir, Loki, CNPG) in the same commit.

---

## 9. Keycloak bootstrap

The `kube-dc-fleet/scripts/bootstrap-keycloak.sh` script sets up the Grafana OIDC client
in the `master` realm. It is idempotent and safe to re-run. It also bumps the master
realm's `accessTokenLifespan` to 900 s if currently lower.

To re-run after a Keycloak reinstall:
```bash
KUBECONFIG=<cluster-kubeconfig> ./scripts/bootstrap-keycloak.sh
```

---

## 10. Reference

| Topic | Path |
|---|---|
| Mimir HelmRelease + values | `platform/monitoring/mimir/` |
| Loki HelmRelease + values | `platform/monitoring/loki/` |
| Grafana / Prometheus (kube-prom-stack) | `platform/monitoring/prom-operator/` |
| Dashboard JSON sources | `platform/monitoring/dashboards/` |
| Per-cluster version pins | `clusters/cloud/cluster-config.env` |
| Internal architecture (implementation detail) | `docs/internal/observability-architecture.md` |
| Observability multi-tenancy PRD | `docs/prd/observability-multi-tenancy.md` |
| Grafana multi-tenancy PRD | `docs/prd/grafana-multi-tenant.md` |
