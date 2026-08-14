# Observability — Platform Operator Guide

**Audience:** Cluster operators and platform engineers  
**Scope:** Day-2 operations for the shared observability stack

---

## 1. Overview

The platform ships a **shared, multi-tenant observability stack** covering metrics, logs,
and alerting for all Kube-DC Organizations on a cluster. The key properties from an
operator perspective:

- **Shared services with tenant-aware scoping.** Grafana, Mimir, Loki, and
  Alertmanager are shared. Identity claims, datasource headers, and backend tenant IDs
  scope each request; operators must keep those controls aligned and test them.
- **Organizations are tenants; Projects are data scopes.** Metrics and logs are stored
  under namespace-derived backend tenant IDs. An Organization view federates its
  Organization namespace and accessible Project backing namespaces.
- **GitOps managed.** Component versions, dashboards, alert rules, and capacity limits
  live in `kube-dc-fleet`. Apply durable changes through Git and Flux.
- **Controller managed.** Organization reconciliation provisions Grafana state and
  datasource scope. Readiness still depends on Keycloak, the authorization path, storage,
  and the observability backends.

### 1.1 Component overview

| Component | Role |
|---|---|
| **Mimir** | Multi-tenant time-series store. Metrics use namespace-derived backend tenant IDs and Organization views query the authorized IDs through tenant federation. |
| **Loki** | Multi-tenant log store. Pod logs from eligible nodes are routed to the backend tenant ID derived from their source namespace. |
| **Alertmanager** (Mimir's) | Multi-tenant alerting. Each Organization manages its alert routes through Grafana. |
| **Grafana** | Single deployment with one Grafana Organization per Kube-DC Organization. Platform admins log in through SSO; Organization users enter through the Kube-DC console. |
| **kube-prometheus-stack** | Prometheus hot tier, cluster-level alerting rules, and Grafana. Retention is configured per cluster. |
| **Alloy** | The log collector runs as a DaemonSet on eligible nodes; the metrics scraper runs as a Deployment. |

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

clusters/<cluster>/platform/
  kustomization.yaml      # Per-cluster overlay (Grafana settings, secrets)
```

> **The per-tenant metrics write path is enabled per cluster.** `cortex-tenant`
> + `alloy-metrics` (the components that route each Project's metrics into its
> own Mimir tenant) are **not** in the shared `platform/monitoring` root — their
> cloud-sized values would over-provision small clusters. A capable cluster
> opts in with a `monitoring-writepath.yaml` Flux Kustomization that pulls the
> shared `platform/monitoring-writepath` bundle. `kube-dc bootstrap init`
> **scaffolds this by default** for new installs wherever Mimir is present
> (any non-disabled object-storage mode), so a fresh cluster's tenant Grafana
> Orgs show metrics — not just logs — out of the box. Without it, tenant metrics
> dashboards render empty while logs work.

---

## 2. Product and backend identity model

### 2.1 Product and storage identities

| Identity | Effective view |
|---|---|
| Kube-DC Organization | Federated metrics and logs from the Organization namespace and its authorized Projects |
| Project | Metrics and logs for the Project backing namespace, named `{organization}-{project}` |
| `system` backend tenant ID | Explicitly allowlisted platform namespaces and cluster-scoped data; platform-administrator use only |

The observability backends use namespace-derived **backend tenant IDs** as storage
and request-routing keys. They are implementation details, not additional product
tenants: the product tenant is the Organization, and a Project is its governed
workload boundary.

Routing is automatic after the collectors and controller reconcile:

- metrics use the Kubernetes `namespace` label to select a backend tenant ID;
- logs are tagged with their source namespace during collection;
- an explicit allowlist collapses platform namespaces such as `monitoring`,
  `kube-system`, and `kube-dc` into the `system` backend tenant ID;
- metrics without a namespace label and cluster-wide Kubernetes events also route
  to `system`;
- Organization datasource configuration federates the relevant backend tenant IDs.

### 2.2 Adding or removing a Project

No separate observability registration is required. When a Project changes,
`kube-dc-manager` updates the Organization's Grafana datasource scope. Allow for
controller reconciliation, collector discovery, and the next scrape or log
batch before expecting data.

## 3. Dashboards

### 3.1 How dashboards work

Platform dashboards are stored as JSON files in
`kube-dc-fleet/platform/monitoring/dashboards/` and deployed as Kubernetes
ConfigMaps through Kustomize. The `kube-dc-manager` controller distributes them
to the appropriate Grafana Organizations on every reconciliation.

Dashboards are **declarative**: the Git JSON is the source of truth. Manual edits made in the
Grafana UI are overwritten on the next reconcile. Organizations that need custom
dashboards should create new ones in their Grafana Organization. Those dashboards
are persisted in the Grafana database and are not touched by the controller.

### 3.2 Currently shipped dashboards

| Dashboard | Grafana folder | Shown to |
|---|---|---|
| Namespace Resource Usage | Platform | All Organizations (home dashboard) |
| Logs Explorer | Platform | All Organizations |
| Active Alerts | Platform | All Organizations |
| Shared GPU Workloads | Platform / Accelerators | All Organizations |
| Storage / Ceph | Storage | Platform administrators only |
| GPU Operations | Platform / Accelerators | Platform administrators only |

### 3.3 Adding a platform dashboard

1. **Create the dashboard JSON.**  
   Export from Grafana (`Share → Export → Save to file`) or author from scratch.  
   Requirements:
   - Must have a stable, unique `uid` field (e.g. `kube-dc-my-new-dashboard`).
   - Use the well-known datasource UIDs — do not embed a datasource name:
     - `mimir` — federated metrics (queries across all Organization backend tenant IDs)
     - `mimir-alerts` — alert rule management for one backend tenant ID at a time
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
         kube-dc.com/grafana-folder: "Platform"
         kube-dc.com/grafana-scope: "all-tenants"   # see §3.4
   ```

4. **Commit and push.** Watch the Flux Kustomization and the Organization
   controller logs until both reconciliation loops have observed the change.

5. **Request a new Organization reconcile** if needed:
   ```bash
   kubectl annotate organization <org-name> reconcile-trigger="$(date)" --overwrite -n <org-name>
   ```

### 3.4 Scope annotation

Control which Grafana Organizations receive a dashboard via the
`kube-dc.com/grafana-scope` annotation:

| Value | Distributed to |
|---|---|
| `all-tenants` (default) | Every Kube-DC Organization's Grafana Organization |
| `main-only` | Grafana Organization 1 only (platform admin view) |
| `tenants:acme,foo,bar` | Explicit list of Kube-DC Organization slugs |

### 3.5 Setting a home dashboard

Add the annotation `kube-dc.com/grafana-home: "true"` to a dashboard's ConfigMap entry.
Only one dashboard per scope should carry this annotation; if multiple do, the last one
wins (alphabetical ConfigMap order).

### 3.6 Editing an existing dashboard

Edit the JSON file in `kube-dc-fleet/platform/monitoring/dashboards/`, commit, and push.
Keep the `uid` field unchanged — changing the UID causes a new dashboard to be created
and the old one to remain (orphaned). If you need to retire a dashboard, delete the JSON
and ConfigMap entry; then manually delete it from Grafana Organizations or wait for the next full
re-provision.

---

## 4. Alert rules

### 4.1 Where rules live

| Rule type | Storage location | Who manages |
|---|---|---|
| **Platform rules** (cluster health, node, kube-system) | Prometheus PrometheusRules, evaluated by kube-prometheus-stack Prometheus | Managed via `prom-operator/values-configmap.yaml` or additional PrometheusRule CRDs |
| **Organization alert rules** | Mimir Ruler, stored under backend tenant IDs in S3 | Organization users (Grafana Alert Rules UI) or operators via `mimirtool` |

### 4.2 Adding or changing platform alert rules

Platform alerting rules are part of the `kube-prometheus-stack` chart. Add a
`PrometheusRule` manifest in `kube-dc-fleet/platform/monitoring/prom-operator/` (or via a
kustomize overlay) and commit. The Prometheus Operator observes the resource and updates the selected rule
configuration through its normal reconciliation loop.

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

### 4.3 Managing backend alert rules via `mimirtool`

For bulk operations across backend tenant IDs (for example, pushing a common baseline rule set):

```bash
# List all rule groups for one backend tenant ID
mimirtool rules list \
  --address https://mimir-ruler.kube-dc.cloud \
  --id <backend-tenant-id>

# Upload rule groups for one backend tenant ID
mimirtool rules load ./rules/my-rules.yaml \
  --address https://mimir-ruler.kube-dc.cloud \
  --id <backend-tenant-id>
```

Authentication requires a valid Keycloak access token from the `master` realm (platform
admin group). See the `kube-dc` CLI docs for obtaining tokens.

### 4.4 Default Alertmanager routing

Each Kube-DC Organization has its own Alertmanager configuration managed through Grafana
(Contact Points and Notification Policies). Platform-level Alertmanager config (for the
`system` backend tenant ID) is set in `prom-operator/values-configmap.yaml` under
`alertmanager.config`.

---

## 5. Capacity and retention

Capacity values are deployment configuration, not product constants. Read the
base values and the selected cluster overlay together before changing them:

| Area | Fleet source |
|---|---|
| Mimir limits and retention | `platform/monitoring/mimir/values-configmap.yaml` plus the cluster overlay |
| Loki limits and retention | `platform/monitoring/loki/values-configmap.yaml` plus the cluster overlay |
| Prometheus hot-tier retention | `platform/monitoring/prom-operator/` plus the cluster overlay |
| Object-store quotas | `clusters/<cluster>/cluster-config.env` and the rendered OBC resources |

Important dimensions include active series, ingestion rate and burst, block
retention, log ingestion rate and burst, stream cardinality, and log retention.
Increase them only after checking object-store capacity, compaction, query
latency, and failure recovery. A backend tenant ID normally represents one
namespace, not an entire Organization.

### 5.1 Grafana database

Grafana uses the `grafana-pg` CloudNativePG cluster in the `monitoring`
namespace. Confirm the live PVC size and backup policy rather than relying on a
copied estimate:

```bash
kubectl -n monitoring get cluster grafana-pg
kubectl -n monitoring get scheduledbackup grafana-pg-daily -o yaml
kubectl -n monitoring get backup --sort-by=.metadata.creationTimestamp
```

The current base Fleet manifest schedules `grafana-pg-daily` at 02:30 UTC and
retains backups for 30 days. A cluster overlay can change that. To request an
immediate run through the scheduled policy:

```bash
kubectl -n monitoring annotate scheduledbackup grafana-pg-daily   cnpg.io/immediateBackup="true" --overwrite
```

### 5.2 Metrics and log object storage

Mimir and Loki use S3-compatible buckets provisioned through ObjectBucketClaims.
Inspect the live claims and their backing storage before raising retention:

```bash
kubectl -n monitoring get obc
kubectl -n monitoring describe obc mimir-blocks
kubectl -n monitoring describe obc loki-chunks
```

Do not copy quota values from another cluster: storage topology, ingestion, and
retention differ between installations.

## 6. Grafana administration

### 6.1 Logging in as a platform admin

Navigate to `https://grafana.<domain>/login` and click **Sign in with Keycloak**.
This uses the `master` realm. In the current Grafana configuration, members of
the master-realm `admin` group receive `GrafanaAdmin`. The Envoy observability
authorization policy must recognize the same platform-admin claim; verify both
paths after identity or monitoring changes.

### 6.2 Inspecting an Organization's Grafana space

1. Log in as platform admin (§6.1).
2. Open the **Organization switcher** (top-left globe icon or via `Admin → Organizations`).
3. Switch to the target Grafana Organization. You now see its dashboards,
   datasources, and alert rules as its users do.
4. **Switch back** to `Main Org.` when finished to avoid accidental edits in
   the Organization's space.

### 6.3 Manually triggering Organization reconciliation

If an Organization's dashboards, datasources, or membership is stale, force a
reconcile:

```bash
kubectl annotate organization <org-slug> \
  reconcile-trigger="$(date)" --overwrite -n <org-slug>
```

Watch the `kube-dc-manager` logs for the result:
```bash
kubectl logs -n kube-dc deployment/kube-dc-manager -c manager --follow | grep <org-slug>
```

### 6.4 Token lifetime

Per-Organization realms default to 15-minute access tokens in
`internal/organization/helpers.go`. The Fleet bootstrap workflow ensures that
the master realm is at least 15 minutes through
`bootstrap/setup-keycloak-oidc.sh`. Inspect the live realm before assuming an
exact value because an operator can configure a longer lifetime.

Shorter tokens reduce the replay window but increase refresh and login pressure.
Treat a lifetime change as identity configuration and test the console, Grafana,
CLI, and long-running API clients.

---

## 7. Troubleshooting

### 7.1 Organization users see no data in dashboards

1. **Verify the Grafana Organization.** Its name appears in the top-left and
   should match the user's Kube-DC Organization slug. If it does not, the
   membership was not provisioned; trigger reconciliation (§6.3).

2. **Check the Mimir datasource.** As platform admin, switch to the affected
   Grafana Organization and open
   **Configuration → Datasources**. The `Mimir` datasource should exist and the
   **Custom HTTP Headers** section should show an `X-Scope-OrgID` value containing the
   Organization ID and its Project backing namespace IDs (for example,
   `shalb|shalb-docs|shalb-jumbolot`). If it shows only the Organization ID,
   trigger reconciliation (§6.3) after confirming the Projects exist.

3. **Check the metrics write path.** Look for rejected writes or tenant-header
   errors for the backing namespace in Mimir distributor logs:
   ```bash
   kubectl logs -n monitoring \
     -l app.kubernetes.io/component=distributor \
     --tail=100 | grep <backing-namespace>
   ```
   No matching error does not prove ingestion. Check the Alloy metrics collector
   next:
   ```bash
   kubectl logs -n monitoring \
     -l app.kubernetes.io/name=alloy-metrics --tail=100
   ```

### 7.2 Alert Rules tab shows 404 or is empty

This happens when the `Mimir Alerts` datasource (the per-backend-tenant alerting datasource)
is missing or misconfigured. As platform admin:

1. Switch to the affected Grafana Organization.
2. Open **Configuration → Datasources** and confirm `Mimir Alerts` exists alongside `Mimir`.
3. If it's missing, trigger reconciliation (§6.3).

Also verify the `mimir-ruler` deployment is running:
```bash
kubectl get pods -n monitoring -l app.kubernetes.io/component=ruler
```

### 7.3 Dashboards missing from a Grafana Organization

Dashboards are distributed on every Organization reconciliation. If they are
missing after a new dashboard was added to the Fleet repository:

1. Confirm Flux has reconciled the ConfigMap:
   ```bash
   flux get kustomization platform -n flux-system
   kubectl get cm -n monitoring -l kube-dc.com/grafana-dashboard=true
   ```
2. Trigger reconciliation (§6.3).
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

If `cortex-tenant` is OOMKilled, inspect its current working set and restart
behavior, then adjust the memory request and limit in
`platform/monitoring/cortex-tenant/values-configmap.yaml`. Verify the pipeline after
Flux applies the change.

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

Component versions are pinned per cluster in
`clusters/<cluster>/cluster-config.env`. Upgrade procedure for each component:

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

The Fleet `bootstrap/setup-keycloak-oidc.sh` workflow reconciles the Grafana
OIDC client and other platform clients in the `master` realm. Run the script
that belongs to the checked-out Fleet version:

```bash
cd <fleet-repository>
KUBECONFIG=<management-cluster-kubeconfig>   bash bootstrap/setup-keycloak-oidc.sh <cluster>
```

Review and push any generated encrypted Fleet changes, then verify both direct
Grafana login and datasource queries.

## 10. Reference

| Topic | Path |
|---|---|
| Mimir HelmRelease + values | `platform/monitoring/mimir/` |
| Loki HelmRelease + values | `platform/monitoring/loki/` |
| Grafana / Prometheus (kube-prom-stack) | `platform/monitoring/prom-operator/` |
| Dashboard JSON sources | `platform/monitoring/dashboards/` |
| Per-cluster version pins | `clusters/<cluster>/cluster-config.env` |
