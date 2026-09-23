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

## Restore into a new service

Confirm that the family and target plan support recovery. Select a completed
`ServiceBackup` whose data is still in the archive. Record its name and UID,
and the historical source name and UID from `spec.serviceRef` and
`spec.serviceUID`. The source service can be active or deleted.

Create a `RestoreToNew` operation. Do not set `ManagedService.spec.restoreFrom`
yourself; the platform generates it from this request:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: "{source-service}-restore-{date}"
  namespace: "{backing-namespace}"
spec:
  serviceRef: { name: "{source-service}" }
  serviceUID: "{source-service-uid}"
  type: RestoreToNew
  idempotencyKey: "{source-service}-restore-{date}"
  restore:
    backupRef:
      name: "{backup-record}"
      uid: "{backup-record-uid}"
    target:
      name: "{restored-service}"
      planRef: { name: "{target-plan}" }
      engineVersion: "{qualified-version}"
      placement:
        mode: ProviderShared
        dataPlaneRef: { name: "{data-plane}" }
      connectivity: { classRef: { name: tenant-native } }
      storage: { size: 20Gi }        # adjust for the selected backup
      parameters:
        database: "{same-as-source}"
        owner: "{same-as-source}"
  execution:
    window: Immediate
```

This example uses PostgreSQL parameters. Use the selected family's parameters
and a compatible target plan, full engine version, data plane, and capacity.
Validate with `kubectl apply --dry-run=server -f restore.yaml`, then apply.
Wait for the operation to succeed and the target to be ready. Create bindings
with the target's own UID and verify the recovered data before you move
applications. The target has fresh credentials. The source stays unchanged.

See [Backups and restore](../../docs/cloud/postgresql-backup-restore.md) for the
full PostgreSQL procedure and recovery checks.

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
(`status.backup.recoveryWindow` on the `ServiceBackup` shows `earliest`/`latest`).
For restore-to-new, set `spec.restore.targetTime`; for in-place restore, set
`spec.parameters.targetTime`. Use RFC 3339 UTC with whole seconds. MySQL, MariaDB,
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
