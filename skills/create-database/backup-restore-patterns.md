# Backup & Restore Patterns

Recipes for taking on-demand backups and restoring `KdcDatabase` resources via `kubectl`. The dashboard's **Take snapshot now** and **Restore from backup** flows render the same primitives.

## Mental model

| Engine | Backup CR you reference for restore | Recovery primitive | Where archives live |
|--------|-------------------------------------|--------------------|---------------------|
| PostgreSQL | `cnpg.io/Backup` (a single base backup; CNPG continuously archives WAL alongside) | `Cluster.spec.bootstrap.recovery.backup.name` | `s3://{ns}-db-backups/databases/` |
| MariaDB | `k8s.mariadb.com/PhysicalBackup` | `MariaDB.spec.bootstrapFrom.backupRef{name, kind: PhysicalBackup}` | `s3://{ns}-db-backups/databases/{db}/` |

Both engines do recovery only at engine-CR creation time, so Kube-DC's wrapper either creates a *new* `KdcDatabase` (safe new-name path) or deletes-and-recreates the existing engine cluster + PVCs (destructive in-place path).

## On-demand backup

### PostgreSQL — create a `cnpg.io/Backup`

```bash
TS=$(date +%s) envsubst <<'YAML' | kubectl apply -f -
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: {db-name}-snap-${TS}
  namespace: {project-namespace}
  labels:
    kube-dc.com/database: {db-name}
    kube-dc.com/backup-type: manual
spec:
  cluster:
    name: {db-name}
  method: barmanObjectStore
YAML
```

### MariaDB — create a `k8s.mariadb.com/PhysicalBackup`

Always set `target: PreferReplica`. The mariadb-operator's default is strict `Replica`, which loops forever waiting for a replica-labeled pod when replication has not converged.

```bash
TS=$(date +%s) envsubst <<'YAML' | kubectl apply -f -
apiVersion: k8s.mariadb.com/v1alpha1
kind: PhysicalBackup
metadata:
  name: {db-name}-snap-${TS}
  namespace: {project-namespace}
  labels:
    kube-dc.com/database: {db-name}
    kube-dc.com/backup-type: manual
spec:
  mariaDbRef:
    name: {db-name}
  target: PreferReplica
  backoffLimit: 3
  storage:
    s3:
      bucket: {project-namespace}-db-backups
      prefix: databases/{db-name}
      endpoint: s3.kube-dc.cloud
      tls:
        enabled: true
      accessKeyIdSecretKeyRef:
        name: db-backups
        key: AWS_ACCESS_KEY_ID
      secretAccessKeySecretKeyRef:
        name: db-backups
        key: AWS_SECRET_ACCESS_KEY
YAML
```

## List backups available for restore

```bash
# PostgreSQL — completed barman backups (the names you reference for recovery)
kubectl get backup.postgresql.cnpg.io -n {project-namespace} \
  -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,BEGIN:.status.beginWal,END:.status.endWal,STOPPED:.status.stoppedAt'

# MariaDB — physical backups
kubectl get physicalbackup -n {project-namespace} \
  -o 'custom-columns=NAME:.metadata.name,COMPLETE:.status.conditions[0].status,LAST_RUN:.status.lastScheduleTime'
```

## Restore — two paths

| Path | When | Trigger surface |
|------|------|-----------------|
| **New-name (recommended)** | Verify recovery first, swap apps once you're sure | New `KdcDatabase.spec.restoreFrom` |
| **In-place (destructive)** | Same name + connection details, willing to lose live data for a few minutes | `kube-dc.com/restore-from` annotation |

### New-name restore

Create a sibling `KdcDatabase`. Original keeps running.

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: {db-name}-restored
  namespace: {project-namespace}
spec:
  engine: postgresql            # or mariadb
  version: "16"
  databaseName: {database-name}
  username: app
  cpu: "1"
  memory: 2Gi
  storage: 20Gi
  replicas: 2
  restoreFrom:
    backupName: {db-name}-snap-1778356023118   # cnpg.io/Backup or PhysicalBackup name
    sourceDatabaseName: {db-name}              # audit/UI display only
    # Optional PostgreSQL PITR — replay WAL up to this RFC 3339 instant.
    # Honored by the API for MariaDB but needs continuous binlog archival,
    # which is not yet wired in Kube-DC v0.3.x.
    # targetTime: "2026-05-09T19:30:00Z"
```

```bash
kubectl apply -f restored.yaml
kubectl get kdcdb {db-name}-restored -n {project-namespace} -w
```

### In-place restore

```bash
# Trigger restore — db-manager deletes engine cluster + PVCs and re-bootstraps
kubectl annotate kdcdb {db-name} -n {project-namespace} \
  kube-dc.com/restore-from={db-name}-snap-1778356023118 --overwrite

# PostgreSQL PITR: add target-time alongside
kubectl annotate kdcdb {db-name} -n {project-namespace} \
  kube-dc.com/restore-from={db-name}-snap-1778356023118 \
  kube-dc.com/restore-target-time=2026-05-09T19:30:00Z --overwrite
```

Both annotations are cleared once `status.restore.phase` reaches `Succeeded`.

## Monitor a restore

```bash
# Phase + last message
kubectl get kdcdb {db-name} -n {project-namespace} \
  -o jsonpath='{.status.restore.phase} — {.status.restore.message}{"\n"}'

# RestoreReady condition (True when finished)
kubectl get kdcdb {db-name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="RestoreReady")].status}{"  "}{.status.conditions[?(@.type=="RestoreReady")].message}{"\n"}'

# Engine-side progress
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-namespace} \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.message}){"\n"}{end}'
```

Phases for in-place flow: `InProgress` → `Succeeded` (annotation cleared) or `Failed` (annotation kept so the user can retry).

## PostgreSQL point-in-time recovery

CNPG ships continuous WAL archives alongside base backups. Read the recoverable window straight off the `Cluster`:

```bash
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-namespace} \
  -o jsonpath='floor: {.status.firstRecoverabilityPoint}{"\n"}last: {.status.lastSuccessfulBackup}{"\n"}'
```

Any RFC 3339 timestamp between the floor and **now** is a valid `targetTime` while `ContinuousArchiving` is `True`:

```bash
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}{"\n"}'
# True
```

If continuous archiving is unhealthy, the safe ceiling drops back to `lastSuccessfulBackup`.

## Pitfalls

- **MariaDB manual backups stuck**: missing `target: PreferReplica` makes the operator wait forever on a replica-labeled pod. Always set it.
- **Empty S3 endpoint in PhysicalBackup spec**: indicates `S3_ENDPOINT` env is unset on the db-manager pod. Backups will fail because the operator falls back to the AWS default.
- **In-place restore is destructive**: PVCs are deleted before recovery starts. The database is unavailable for a few minutes. Prefer the new-name path for production-critical data.
- **PITR target time must be ≥ snapshot's `stoppedAt`**: PostgreSQL recovery only rolls forward from a base backup. Picking a target time before the chosen snapshot will fail with *"recovery ended before configured recovery target was reached"*. The UI's date validator clamps this; if you're driving the annotation/spec by hand, verify it yourself.
- **PITR on idle clusters**: db-manager defaults `archive_timeout=5min` so even quiet databases ship a WAL file every 5 minutes. PITR granularity therefore tops out at ~5 min on idle clusters — target times in a 5-minute window where no WAL boundary fell will fail. Active databases get sub-second granularity for free.
- **PITR for MariaDB**: `targetTime` is in the API but needs continuous binlog archival via a `PointInTimeRecovery` CR — not yet in v0.3.x. Plan around base-backup granularity instead.
