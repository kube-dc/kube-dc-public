# Backup & Restore Patterns

Recipes for taking on-demand backups and restoring `KdcDatabase` resources via
`kubectl`. The dashboard's **Take snapshot now** and **Restore from backup**
flows render the same primitives.

## Mental model

| Engine | Backup CR you reference for restore | Recovery primitive | Where archives live |
|--------|-------------------------------------|--------------------|---------------------|
| PostgreSQL | `cnpg.io/Backup` (a single base backup; CNPG continuously archives WAL alongside) | `Cluster.spec.bootstrap.recovery.backup.name` | `s3://{backing-namespace}-db-backups/databases/{database}/` |
| MariaDB | `k8s.mariadb.com/PhysicalBackup` | `MariaDB.spec.bootstrapFrom.backupRef{name, kind: PhysicalBackup}` | `s3://{backing-namespace}-db-backups/databases/{database}/` |

Both engines recover only when the engine custom resource is created. Kube-DC
therefore either creates a new `KdcDatabase` (the safer new-name path) or
deletes and recreates the existing engine cluster and PVCs (the destructive
in-place path).

## On-demand backup

Enable and reconcile the database's backup configuration before using these
recipes. The default `db-backups` ObjectBucketClaim and credential Secret are
created by that flow.

### PostgreSQL — create a `cnpg.io/Backup`

```bash
TS=$(date +%s) envsubst <<'YAML' | kubectl apply -f -
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: {db-name}-snap-${TS}
  namespace: {project-backing-namespace}
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

Always set `target: PreferReplica`. The mariadb-operator's default is strict
`Replica` and can wait indefinitely when no replica is available. Copy the S3
bucket, prefix, endpoint, TLS choice, and credential references from the
controller-created `{db-name}-scheduled` PhysicalBackup. The example below
shows the default bucket layout; do not invent provider endpoint values.

```bash
TS=$(date +%s) envsubst <<'YAML' | kubectl apply -f -
apiVersion: k8s.mariadb.com/v1alpha1
kind: PhysicalBackup
metadata:
  name: {db-name}-snap-${TS}
  namespace: {project-backing-namespace}
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
      bucket: {project-backing-namespace}-db-backups
      prefix: databases/{db-name}/
      # MariaDB expects host[:port], without http:// or https://.
      endpoint: "{configured-s3-hostname}"
      tls:
        enabled: {configured-tls-enabled}
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
kubectl get backup.postgresql.cnpg.io -n {project-backing-namespace} \
  -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,BEGIN:.status.beginWal,END:.status.endWal,STOPPED:.status.stoppedAt'

# MariaDB — physical backups
kubectl get physicalbackup -n {project-backing-namespace} \
  -o 'custom-columns=NAME:.metadata.name,COMPLETE:.status.conditions[0].status,LAST_RUN:.status.lastScheduleTime'
```

## Restore — two paths

| Path | When | Trigger surface |
|------|------|-----------------|
| **New-name (recommended)** | Verify recovery first, swap apps once you're sure | New `KdcDatabase.spec.restoreFrom` |
| **In-place (destructive)** | Same name + connection details, accepting an outage and replacement of the live database state | `kube-dc.com/restore-from` annotation |

### New-name restore

Create a sibling `KdcDatabase`. Original keeps running.

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: "{db-name}-restored"
  namespace: "{project-backing-namespace}"
spec:
  engine: postgresql            # or mariadb
  version: "16"
  databaseName: "{database-name}"
  username: app
  cpu: "1"
  memory: 2Gi
  storage: 20Gi
  replicas: 2
  restoreFrom:
    backupName: "{backup-name}"                  # cnpg.io/Backup or PhysicalBackup name
    sourceDatabaseName: "{db-name}"              # audit/UI display only
    # Optional PostgreSQL PITR — replay WAL up to this RFC 3339 instant.
    # PostgreSQL only. The current API ignores targetTime for MariaDB.
    # targetTime: "2026-05-09T19:30:00Z"
```

```bash
kubectl apply -f restored.yaml
kubectl get kdcdb {db-name}-restored -n {project-backing-namespace} -w
```

### In-place restore

```bash
# Trigger restore — db-manager deletes engine cluster + PVCs and re-bootstraps
kubectl annotate kdcdb {db-name} -n {project-backing-namespace} \
  kube-dc.com/restore-from={backup-name} --overwrite

# PostgreSQL PITR: add target-time alongside
kubectl annotate kdcdb {db-name} -n {project-backing-namespace} \
  kube-dc.com/restore-from={backup-name} \
  kube-dc.com/restore-target-time=2026-05-09T19:30:00Z --overwrite
```

Both annotations are cleared once `status.restore.phase` reaches `Succeeded`.

## Monitor a restore

```bash
# Phase + last message
kubectl get kdcdb {db-name} -n {project-backing-namespace} \
  -o jsonpath='{.status.restore.phase} — {.status.restore.message}{"\n"}'

# RestoreReady condition (True when finished)
kubectl get kdcdb {db-name} -n {project-backing-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="RestoreReady")].status}{"  "}{.status.conditions[?(@.type=="RestoreReady")].message}{"\n"}'

# Engine-side progress
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-backing-namespace} \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.message}){"\n"}{end}'
```

Phases for in-place flow: `InProgress` → `Succeeded` (annotation cleared) or `Failed` (annotation kept so the user can retry).

## PostgreSQL point-in-time recovery

CNPG ships continuous WAL archives alongside base backups. Read the recoverable window straight off the `Cluster`:

```bash
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-backing-namespace} \
  -o jsonpath='floor: {.status.firstRecoverabilityPoint}{"\n"}last: {.status.lastSuccessfulBackup}{"\n"}'
```

A PostgreSQL `targetTime` must be inside the recoverable window and have sufficient archived WAL. First confirm `ContinuousArchiving=True`:

```bash
kubectl get cluster.postgresql.cnpg.io {db-name} -n {project-backing-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}{"\n"}'
# True
```

If continuous archiving is unhealthy, the safe ceiling drops back to `lastSuccessfulBackup`.

## Pitfalls

- **MariaDB manual backups stuck**: missing `target: PreferReplica` makes the operator wait forever on a replica-labeled pod. Always set it.
- **S3 endpoint**: use the endpoint rendered on the database's scheduled `PhysicalBackup` or supplied by the provider. MariaDB's field omits the URL scheme; set `tls.enabled` consistently. Never copy another installation's endpoint.
- **In-place restore is destructive**: PVCs are deleted before recovery starts. The database is unavailable until recovery and readiness complete. Prefer the new-name path for production-critical data.
- **PITR target time must be ≥ snapshot's `stoppedAt`**: PostgreSQL recovery only rolls forward from a base backup. Picking a target time before the chosen snapshot will fail with *"recovery ended before configured recovery target was reached"*. The UI's date validator clamps this; if you're driving the annotation/spec by hand, verify it yourself.
- **PITR on idle clusters**: db-manager defaults `archive_timeout=5min` so even quiet databases ship a WAL file every 5 minutes. PITR granularity therefore tops out at ~5 min on idle clusters — target times in a 5-minute window where no WAL boundary fell will fail. Active databases get sub-second granularity for free.
- **PITR for MariaDB**: the current API ignores `targetTime`; plan around completed PhysicalBackup recovery instead.
