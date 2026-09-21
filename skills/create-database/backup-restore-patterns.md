# Backup and restore patterns for managed services

Backups are the platform's job once the plan enables them; your job is to
verify that you can restore. All examples pin the service UID.

## Take a backup now

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: "{service-name}-backup-{date}"
  namespace: "{backing-namespace}"
spec:
  serviceRef:
    name: "{service-name}"
  serviceUID: "{service-uid}"
  type: Backup
  idempotencyKey: "{service-name}-backup-{date}"
  execution:
    window: Immediate
```

```bash
kubectl apply -f backup.yaml
kubectl get serviceoperation {service-name}-backup-{date} -n {backing-namespace} -w   # Succeeded
kubectl get servicebackups -n {backing-namespace}                                       # history, read-only
```

Kafka's `Backup` exports metadata (topics, configuration, ACLs, quotas,
offsets) only; there is no message backup.

## Restore into a new service (every family)

Create a new `ManagedService` with `spec.restoreFrom`, using the source
service's UID and a `Completed` backup's `status.backup.id`:

```yaml
spec:
  classRef: { name: postgresql }
  planRef: { name: postgresql-development }
  placement: { mode: ProviderShared }
  connectivity: { classRef: { name: tenant-native } }
  restoreFrom:
    serviceRef:
      name: "{source-service}"
    serviceUID: "{source-service-uid}"
    backupID: "{status.backup.id of the ServiceBackup}"
    # or, PostgreSQL only: targetTime: "2026-01-15T10:40:00Z"
  topology: { instances: 1 }
  storage: { size: 20Gi }            # at least the source size
  parameters:
    database: "{same-as-source}"
    owner: "{same-as-source}"
  deletionPolicy: Retain
```

Then create new bindings for the new service (its own UID), verify the data,
move the applications. The source is not changed. A `RestoreToNew`
`ServiceOperation` on the source does the same with a typed `spec.restore`
block.

## Restore in place (PostgreSQL only, destructive)

Requires `deletionProtection: false` set in a separate update first, the exact
`engineUID` from `status.engineDetails.engineUID`, and an explicit data-loss
acknowledgement. Exactly one of `backupID` or `targetTime`.

```yaml
spec:
  serviceRef: { name: "{service-name}" }
  serviceUID: "{service-uid}"
  type: RestoreInPlace
  idempotencyKey: "{service-name}-restore-{date}"
  parameters:
    engineUID: "{status.engineDetails.engineUID}"
    acknowledgeDataLoss: true
    backupID: "{backup-id}"
```

Applications reconnect afterwards; credentials survive. Recreate any SQL login
you created yourself if it is missing after the restore.

## Point-in-time recovery

PostgreSQL only, inside a verified recovery window
(`status.recovery` of the `ServiceBackup` records shows `earliest`/`latest`).
Use `targetTime` in RFC 3339 UTC with whole seconds. MySQL, MariaDB,
ClickHouse and Valkey restore the snapshot as taken.

## Recovery plan checklist

- Plan and service both have backups enabled; the `BackupReady` condition is
  `True`.
- Last successful backup time is recent (`status.lastBackup`).
- Retention covers the recovery objective (1 to 35 days on the standard
  plans).
- A restore into a new service has been rehearsed.
- Deleting the Project deletes every in-Project service and its data without
  a final backup, whatever the deletion policy says.
