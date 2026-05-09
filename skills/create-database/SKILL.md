---
name: create-database
description: Create a managed PostgreSQL or MariaDB database in a Kube-DC project, configure access for workloads via environment variables and secrets, optionally expose externally via Gateway or LoadBalancer, and back up / restore via kubectl.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- **Quota**: verify sufficient storage and pod capacity — use the `check-quota` skill

## Steps

### 1. Create KdcDatabase

Apply the template with user's parameters:
- **Engine**: `postgresql` (default) or `mariadb`
- **Version**: PostgreSQL 14-17, MariaDB 10.11/11.4
- **Replicas**: 2+ for HA (recommended for production)
- **Expose**: `internal` (default), `gateway` (TLS passthrough), or `loadbalancer`

See @pg-template.yaml for PostgreSQL and @mariadb-template.yaml for MariaDB.

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: {db-name}
  namespace: {project-namespace}
spec:
  engine: postgresql
  version: "16"
  replicas: 2
  cpu: "1"
  memory: 2Gi
  storage: 10Gi
  databaseName: {database-name}
  username: app
```

### 2. Wait for Ready

```bash
kubectl get kdcdb {db-name} -n {project-namespace} -w
```

### 3. Configure Application Access

The database auto-creates a credential secret. Mount it in your workload:

**PostgreSQL** — secret: `{db-name}-app`, key: `password`
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-app
        key: password
  - name: DB_HOST
    value: "{db-name}-rw.{project-namespace}.svc"
  - name: DB_PORT
    value: "5432"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database-name}"
```

**MariaDB** — secret: `{db-name}-password`, key: `password`
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-password
        key: password
  - name: DB_HOST
    value: "{db-name}.{project-namespace}.svc"
  - name: DB_PORT
    value: "3306"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database-name}"
```

See @db-connection-patterns.md for full connection string examples.

#### Bridging the auto-secret to a Helm chart's expected key name

The auto-secret stores the password under key `password`. Many off-the-shelf
Helm charts hard-code a different key name for their `existingSecret`
parameter and offer no override. When that's the case, create a small bridge
Secret aliasing the password to the chart's expected key name, and pass that
bridge as `existingSecret`:

```bash
PASSWORD=$(kubectl get secret {db-name}-password -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl create secret generic {app}-db-bridge \
  --namespace {project-namespace} \
  --from-literal={chart-expected-key}="$PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Known chart key requirements:

| Chart | Expected key in `existingSecret` |
|-------|---------------------------------|
| Bitnami WordPress | `mariadb-password` |
| Bitnami Discourse, Bitnami Joomla | `db-password` |
| Bitnami NextCloud | `mariadb-password` (when `mariadb.enabled=false`) |

Modern charts often expose `externalDatabase.existingSecretPasswordKey` (or a
similar parameter) — check `helm show values {chart}` for that field before
falling back to the bridge-Secret pattern. PostgreSQL `KdcDatabase` also
auto-creates `{db-name}-app` with key `password`; the same bridge pattern
applies for charts (e.g. some Discourse images) that expect a key like
`postgresql-password`.

### 4. External Access (Optional)

**Gateway** (recommended for production external access):
```yaml
spec:
  expose:
    type: gateway
```
→ Endpoint: `{db-name}-db-{project-namespace}.kube-dc.cloud:{port}`
→ Connect: `psql "host={db-name}-db-{project-namespace}.kube-dc.cloud port=5432 dbname={database-name} user=app sslmode=require"`

**Port-forward** (development/ad-hoc):
```bash
kubectl port-forward svc/{db-name}-rw {port}:{port} -n {project-namespace}
# Then connect to localhost:{port}
```

**LoadBalancer** (dedicated IP):
```yaml
spec:
  expose:
    type: loadbalancer
```
→ Check `status.externalEndpoint` for the allocated IP

### 5. Configure Backups (Optional, Recommended for Production)

Scheduled backups are off by default. Add `spec.backup` to enable a daily backup
to the project's auto-provisioned S3 bucket (`{project-namespace}-db-backups`):

```yaml
spec:
  backup:
    enabled: true
    schedule: "0 2 * * *"   # daily at 02:00
    retentionDays: 7
```

PostgreSQL also enables continuous WAL archiving when `backup.enabled: true`,
which is what powers PITR. See @backup-restore-patterns.md for on-demand backups,
restore (new-name and in-place flows), and PostgreSQL point-in-time recovery.

### 6. Retrieve Password

```bash
# PostgreSQL
kubectl get secret {db-name}-app -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d

# MariaDB
kubectl get secret {db-name}-password -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d
```

## Verification

After creating the database, run these checks:

```bash
# 1. Check database phase (expect: Ready)
kubectl get kdcdb {db-name} -n {project-namespace} -o jsonpath='{.status.phase}'

# 2. Check endpoint is assigned
kubectl get kdcdb {db-name} -n {project-namespace} -o jsonpath='{.status.endpoint}'
# PostgreSQL: {db-name}-rw.{project-namespace}.svc:5432
# MariaDB: {db-name}.{project-namespace}.svc:3306

# 3. Verify credential secret exists
# PostgreSQL:
kubectl get secret {db-name}-app -n {project-namespace}
# MariaDB:
kubectl get secret {db-name}-password -n {project-namespace}

# 4. Test connectivity (from a temporary pod)
# PostgreSQL:
kubectl run pg-test --rm -it --restart=Never --image=postgres:16 -n {project-namespace} -- \
  pg_isready -h {db-name}-rw.{project-namespace}.svc -p 5432
# MariaDB:
kubectl run mysql-test --rm -it --restart=Never --image=mysql:8.0 -n {project-namespace} -- \
  mysqladmin ping -h {db-name}.{project-namespace}.svc --ssl-mode=DISABLED
```

**Success**: Phase is `Ready`, endpoint assigned, secret exists, connectivity test passes.
**Failure**: If phase is `Provisioning`, wait and recheck. If `Failed`:
- `kubectl describe kdcdb {db-name} -n {project-namespace}` — check conditions and events

## Backup & Restore

For day-2 backup operations — on-demand backups, restoring (new-name and
in-place flows), and PostgreSQL point-in-time recovery — see
@backup-restore-patterns.md.

Quick reference:

```bash
# List restore-eligible backups
kubectl get backup.postgresql.cnpg.io -n {project-namespace}        # PostgreSQL
kubectl get physicalbackup -n {project-namespace}                   # MariaDB

# In-place restore (destructive — deletes engine PVCs and re-bootstraps)
kubectl annotate kdcdb {db-name} -n {project-namespace} \
  kube-dc.com/restore-from={backup-name} --overwrite

# PostgreSQL PITR — add target time alongside
kubectl annotate kdcdb {db-name} -n {project-namespace} \
  kube-dc.com/restore-from={backup-name} \
  kube-dc.com/restore-target-time={rfc3339-instant} --overwrite

# Watch restore progress (annotation clears when phase=Succeeded)
kubectl get kdcdb {db-name} -n {project-namespace} \
  -o jsonpath='{.status.restore.phase} — {.status.restore.message}{"\n"}'
```

For non-destructive restore, create a new `KdcDatabase` with `spec.restoreFrom`
instead of using the annotation — see @backup-restore-patterns.md for the full
recipe.

## Safety
- Never log database passwords in chat output
- Default to `internal` exposure unless user explicitly requests external
- Recommend 2+ replicas for production workloads
- PostgreSQL endpoint uses `-rw` suffix; MariaDB does not
- **In-place restore is destructive** — engine PVCs are deleted and recreated
  from the chosen backup. Live data is gone for the duration of the restore.
  Prefer the new-name path (`spec.restoreFrom` on a fresh `KdcDatabase`) for
  production-critical data; verify and swap apps over once you're sure.
- For MariaDB on-demand `PhysicalBackup` always set `target: PreferReplica` —
  the default `Replica` strict mode loops forever when replication has not
  converged. Backups created via the dashboard or `spec.backup` are already
  configured correctly.
