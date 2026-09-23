# Backups and restore

When both the plan's `backup.enabled` and the service's
`parameters.backup.enabled` are `true`, a PostgreSQL service takes scheduled base
backups and archives its write-ahead log (WAL) continuously to object storage.
This page covers the backup policy, on-demand backups, the backup history, and
the two ways to restore: into a new service, or in place over the existing
service, from a selected backup or a point in time.

:::warning Test your restores
Restore a backup into a new service and check your data before you depend on
it.
:::

## Before you begin

- Your plan must set `backup.enabled`, and the service's
  `parameters.backup.enabled` must stay `true`, its default. Ask your provider
  for the values of
  `backup.schedule`, `backup.retentionDays`, `backup.minRetentionDays`,
  `backup.maxRetentionDays` and `backup.minScheduleIntervalMinutes`, and whether
  the plan sets `backup.pitr`.
- The `admin` or `developer` role to change a service, create operations or
  create a service from a backup. Every Project role can read the backup
  history. See [Project roles](managed-services.md#project-roles).
- For the in-Project placement this chapter covers, deleting a Project deletes
  its services and their data volumes whatever their deletion settings, without
  a final backup, and also removes the Project's backup history records. Copy
  out any data you need first. See
  [Delete a Project](managed-services-status-deletion.md#delete-a-project).

Throughout this page, replace `my-project` with your Project's backing
namespace.

## Backup policy

Three parameters control scheduled backups. All three can change after
creation: edit them in the complete `ManagedService` manifest and apply it
again.

```yaml
spec:
  parameters:
    backup:
      enabled: true
      # Five-field cron expression, in UTC: every six hours.
      schedule: "0 */6 * * *"
      retentionDays: 14
```

| Parameter | Rules |
|-----------|-------|
| `backup.enabled` | Default `true`. Scheduled backups and WAL archiving run only when this parameter and the plan's `backup.enabled` are both `true` |
| `backup.schedule` | Five fields, in UTC. Lists, ranges, steps and day or month names work. Seconds, `@` shortcuts and time zone prefixes are refused. Runs must be at least the plan's `backup.minScheduleIntervalMinutes` apart, 60 minutes when the plan does not set it. Omit it to inherit the schedule that applies from your plan |
| `backup.retentionDays` | Must be within the plan's `backup.minRetentionDays` and `backup.maxRetentionDays`, 1 to 365 days when the plan does not set them. Omit it to inherit the retention that applies from your plan |

A value outside the plan's bounds is refused: the service reports
`Accepted=False` with reason `ParameterSchemaRejected` and a message that
contains `must be between <min> and <max> for this plan` or
`runs must be at least <minutes> minutes apart for this plan`.

What the settings do:

- **Turning backups off** removes the backup schedule and WAL archiving
  configuration. Completed backups are not deleted, but the point-in-time
  recovery window stops advancing.
- **Turning backups on** again creates the schedule with an immediate first
  backup.
- **Lowering `retentionDays`** can permanently remove older recovery points.
  Reverting the manifest, for example with a Git rollback, does not bring them
  back. Retention describes a recovery window, not an exact age at which every
  object is removed.
- **A backup can take longer** than the interval between scheduled runs.

The service reports the policy in force in `status.engineDetails`:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='enabled={.status.engineDetails.backup\.enabled} schedule={.status.engineDetails.backup\.schedule} retention={.status.engineDetails.backup\.retentionPolicy} last={.status.engineDetails.backup\.lastScheduledAt} next={.status.engineDetails.backup\.nextScheduledAt}{"\n"}'
```

| Key | Meaning |
|-----|---------|
| `backup.enabled` | Whether backups are configured on the database |
| `backup.schedule`, `backup.timeZone` | The schedule in force and its time zone, `UTC` |
| `backup.retentionPolicy` | The retention in force |
| `backup.lastScheduledAt`, `backup.nextScheduledAt` | The last and next scheduled start |
| `backup.destination` | Where backups are written |

Until an edit is applied, these keys show the previous values; confirm that the
configuration is current as described in
[Read the status](postgresql-create.md#read-the-status). A `nextScheduledAt`
value is not proof that a backup completed; use `status.lastBackup` and the
backup history for that.

## Take a backup now

Create a `Backup` operation. It takes no parameters:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-backup-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Backup
  idempotencyKey: orders-db-backup-1
  execution:
    window: Immediate
```

```bash
kubectl apply -f orders-db-backup-1.yaml
kubectl get serviceoperation orders-db-backup-1 -n my-project -w
```

When the operation succeeds, `status.result` includes `backupName`. The operation is `Rejected` with `BackupNotConfigured` when backups
are not configured for the service, and with `ClusterHibernated` while the
service is hibernated. See [Day-2 Operations](postgresql-operations.md) for
phases and approval.

To find the history record of this backup, match on `backupName`, not on the
time. A scheduled backup can complete at nearly the same time:

```bash
BACKUP_NAME=$(kubectl get serviceoperation orders-db-backup-1 -n my-project -o jsonpath='{.status.result.backupName}')
kubectl get servicebackups -n my-project \
  -o jsonpath="{range .items[?(@.spec.backupName==\"$BACKUP_NAME\")]}{.metadata.name}{\"\t\"}{.status.backup.phase}{\"\t\"}{.status.backup.id}{\"\n\"}{end}"
```

## Backup history

The platform records each observed backup of a service as a `ServiceBackup` in
the Project. Project roles can read the records but cannot create, change or
delete them. A record has no owner, so it is kept after its service is deleted.
Deleting the Project removes the records. See
[What remains after deletion](managed-services-status-deletion.md#what-remains-after-deletion).

List the backups of a service by its UID:

```bash
kubectl get servicebackups -n my-project \
  -l services.kube-dc.com/service-uid=REPLACE_WITH_SERVICE_UID
```

Show the fields you need to choose a restore point:

```bash
kubectl get servicebackups -n my-project \
  -l services.kube-dc.com/service-uid=REPLACE_WITH_SERVICE_UID \
  -o custom-columns='NAME:.metadata.name,ORIGIN:.spec.origin,PHASE:.status.backup.phase,BACKUP_ID:.status.backup.id,MAJOR:.status.backup.engineMajor,COMPLETED:.status.backup.completedAt,PITR_FROM:.status.backup.recoveryWindow.earliest,PITR_TO:.status.backup.recoveryWindow.latest'
```

To find backups of a service that no longer exists, list all records in the
Project and read the source name and UID from each record:

```bash
kubectl get servicebackups -n my-project \
  -o custom-columns='SOURCE:.spec.serviceRef.name,SOURCE_UID:.spec.serviceUID,ORIGIN:.spec.origin,PHASE:.status.backup.phase,BACKUP_ID:.status.backup.id,COMPLETED:.status.backup.completedAt'
```

| Field | Meaning |
|-------|---------|
| `spec.serviceRef.name`, `spec.serviceUID` | The service the backup belongs to. The name can be reused; the UID cannot |
| `spec.origin` | `Manual` (a `Backup` operation), `Scheduled`, `Final` (the final backup of a service deleted with `SnapshotAndDelete`) or `Other` |
| `spec.backupName` | The name the backup had when it was taken. A `Backup` operation reports it in `status.result.backupName` |
| `spec.planName` | The plan of the service at the time. Provenance only |
| `status.backup.phase` | `Pending`, `Running`, `Completed` or `Failed` |
| `status.backup.id` | Physical backup ID. In-place restore uses this value. Restore-to-new uses the catalog record name and UID |
| `status.backup.engineMajor` | The PostgreSQL major version that wrote the backup |
| `status.backup.startedAt`, `status.backup.completedAt` | When the backup started and completed |
| `status.backup.message` | A message from the backup, for example why it failed |
| `status.backup.recoveryWindow` | The verified point-in-time recovery window of this backup: `earliest`, `latest`, `timeline` and `verifiedAt`. See [Point-in-time recovery](#point-in-time-recovery) |

Only a record with phase `Completed` and a `status.backup.id` can be a restore
source. `Completed` means the backup completed at the recorded time. It does not
mean that the backup data is still retained in object storage. Before it creates
a database from a selected backup, a restore verifies that the backup exists in
the archive. Records have no automatic expiry, so a record can outlive its data.

`status.lastBackup` and `status.recentBackups` on the `ManagedService` show only
a bounded recent window. On a `ServiceBackup`, `status.backup.lastArchivedWALTime`
is a legacy diagnostic, not a point-in-time recovery limit; use
`status.backup.recoveryWindow`. The same holds for
`status.lastBackup.lastArchivedWALTime` on the `ManagedService`.

## Choose a restore

The two restore paths use different requests:

| Property | Restore into a new service | Restore in place |
|---|---|---|
| Request | `RestoreToNew` operation with `spec.restore` | `RestoreInPlace` operation with `spec.parameters` |
| Source service | Can remain active or can already be deleted | Must exist with the exact service and engine UIDs |
| Data | Creates another service | Replaces existing data. Later changes are lost |
| Identity | New service name, UID, and credentials | Existing service identity remains |
| Backup selection | Exact `ServiceBackup` name and UID, with optional `targetTime` | Physical `backupID` or `targetTime` |
| Plan policy | Target plan must support the family, version, and `RestoreToNew` | Existing plan must permit `RestoreInPlace` |

## Restore into a new service

A `RestoreToNew` operation selects a retained catalog record and an explicit target configuration.
Do not create `ManagedService.spec.restoreFrom` yourself. The platform generates that field and binds it to the restore operation.

Before you start, confirm that the target plan permits the backup's family and engine major version.
Use a full engine release qualified by that plan. A restore does not perform a major upgrade.

1. Read the selected backup record:

   ```bash
   kubectl get servicebackup <backup-record> -n <project> -o yaml
   ```

   Replace `<backup-record>` with the catalog record name and `<project>` with the Project namespace.
   Check `status.backup.phase`, `status.recovery`, and the archive retention deadline.
   Record `metadata.uid`, `spec.serviceRef.name`, and `spec.serviceUID`.

2. Save this request as `restore-orders.yaml`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceOperation
   metadata:
     name: restore-orders-1
     namespace: <project>
   spec:
     serviceRef:
       name: <source-service>
     serviceUID: <source-uid>
     type: RestoreToNew
     idempotencyKey: restore-orders-1
     restore:
       backupRef:
         name: <backup-record>
         uid: <backup-record-uid>
       target:
         name: orders-restored
         planRef:
           name: <target-plan>
         engineVersion: "<qualified-version>"
         placement:
           mode: ProviderShared
           dataPlaneRef:
             name: <data-plane>
         connectivity:
           classRef:
             name: tenant-native
         storage:
           size: 20Gi
         parameters:
           database: orders
           owner: orders
     execution:
       window: Immediate
   ```

   Replace the source and backup placeholders with values from the selected record.
   Replace `<target-plan>`, `<qualified-version>`, and `<data-plane>` with compatible values from your provider.
   Adjust storage and database names for the backup. Omitted capacity fields use target plan defaults.

3. Validate the request:

   ```bash
   kubectl apply --dry-run=server -f restore-orders.yaml
   ```

4. Submit the request:

   ```bash
   kubectl apply -f restore-orders.yaml
   ```

5. Watch the operation:

   ```bash
   kubectl get serviceoperation restore-orders-1 -n <project> -w
   ```

6. After `Succeeded`, check that `orders-restored` is `Ready`.
7. Create a binding with the restored service's own UID.
8. Verify the recovered data through that binding before you move application traffic.

The source service remains unchanged. The target receives fresh credentials and `deletionPolicy: Retain`.
Recreate required credential policies for the target.

The restore contract uses these fields:

| Field | Purpose |
|---|---|
| `spec.serviceRef.name`, `spec.serviceUID` | Identify the historical source, including a deleted source |
| `spec.restore.backupRef` | Pins the catalog record by name and UID |
| `spec.restore.targetTime` | Optional PostgreSQL recovery time within this exact backup's verified window |
| `spec.restore.target` | Defines the target plan, engine release, placement, connectivity, and optional sizing |

`spec.parameters` is invalid for `RestoreToNew`.
Use `spec.restore.target.parameters` only for engine settings.
The target plan controls approval and the maintenance window.
A dry run checks the request schema. It does not prove that the archive can be restored.

For point-in-time recovery, add a whole-second RFC 3339 `spec.restore.targetTime`.
Omit it to restore the selected backup's consistency point.
Use [Point-in-time recovery](#point-in-time-recovery) to check the window.

## Restore in place

A `RestoreInPlace` operation replaces the database engine and data of an
existing service from a backup of the same service, and keeps the service and
its connection identity.

:::danger Data after the restore point is lost
An in-place restore is destructive. Everything written after the selected
backup or time is lost. Take a backup first if you might need the current data.
:::

### Requirements

- Your plan must allow `RestoreInPlace` in `operations.allowed`, and must set
  `capacity.allocationProtocol: v1` and `backup.enabled`. It must also offer the
  PostgreSQL major version that wrote the selected backup: `allowedImages` must
  include an image of that version or, when `allowedImages` is empty,
  `engineVersion` must equal it. If the plan does not list `RestoreInPlace` in
  `operations.autoApprove`, the operation waits for approval by your provider.
- The operation must set `serviceUID`.
- `spec.deletionProtection` on the service must be `false` when the platform
  checks the operation. Turn it off in a separate update first.
- Break-glass superuser access must be off. See
  [Break-glass superuser access](postgresql-credentials.md#break-glass-superuser-access).
- The backup, or the recovery window that covers `targetTime`, must belong to a
  `Completed` backup of this service.

### Steps

We recommend this order:

1. Stop application writes to the service.
2. Take a `Backup` and wait for it to succeed, so that the current state can
   still be restored into a new service.
3. Set `deletionProtection: false` in the service manifest and apply it. Wait
   until the change is current.
4. Read the current engine UID:

   ```bash
   kubectl get managedservice orders-db -n my-project -o jsonpath='{.status.engineDetails.engineUID}{"\n"}'
   ```

5. Choose the restore point from the [backup history](#backup-history).
6. Create the operation. This example restores backup `REPLACE_WITH_BACKUP_ID`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceOperation
   metadata:
     name: orders-db-restore-in-place-1
     namespace: my-project
   spec:
     serviceRef:
       name: orders-db
     # The service's metadata.uid. Required for RestoreInPlace.
     serviceUID: REPLACE_WITH_SERVICE_UID
     type: RestoreInPlace
     idempotencyKey: orders-db-restore-in-place-1
     parameters:
       # status.engineDetails.engineUID, read just before submitting.
       engineUID: REPLACE_WITH_ENGINE_UID
       acknowledgeDataLoss: true
       # status.backup.id of a Completed backup of this service.
       backupID: REPLACE_WITH_BACKUP_ID
     execution:
       window: Immediate
   ```

   To restore to a point in time instead, replace `backupID` with `targetTime`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceOperation
   metadata:
     name: orders-db-restore-in-place-2
     namespace: my-project
   spec:
     serviceRef:
       name: orders-db
     serviceUID: REPLACE_WITH_SERVICE_UID
     type: RestoreInPlace
     idempotencyKey: orders-db-restore-in-place-2
     parameters:
       engineUID: REPLACE_WITH_ENGINE_UID
       acknowledgeDataLoss: true
       # Inside a verified recovery window of this service.
       # We recommend UTC with whole seconds.
       targetTime: "2026-01-15T10:40:00Z"
     execution:
       window: Immediate
   ```

7. Watch the operation until it reaches a final phase, then wait for the service
   to be `Ready`:

   ```bash
   kubectl get serviceoperation orders-db-restore-in-place-1 -n my-project -w
   kubectl get managedservice orders-db -n my-project -w
   ```

   A successful operation reports `restored in place with preserved connection
   material; resynchronize any existing-user policies against the new engine UID`.
8. Check your data through an existing binding.
9. If the service has existing-user credential policies, resynchronize each of
   them. See [After a restore](postgresql-credentials.md#after-a-restore).
10. Take a new `Backup`.
11. Set `deletionProtection: true` again, and resume application writes.

| Parameter | Description |
|-----------|-------------|
| `engineUID` | Must equal the service's current `status.engineDetails.engineUID` |
| `acknowledgeDataLoss` | Must be `true` |
| `backupID` | The `status.backup.id` of a `Completed` backup of this service |
| `targetTime` | A point in time in RFC 3339 format |

Set exactly one of `backupID` and `targetTime`. Any other parameter is refused.

### What stays and what changes

| Stays the same | Changes |
|----------------|---------|
| The service name and `metadata.uid` | `status.engineDetails.engineUID` |
| `ServiceBinding` objects; an owner binding and its CA were verified to keep working | The engine and its data volumes are replaced |
| Credential policies and existing backup history records | Data written after the restore point is gone |
| No additional preserved field | Backups taken afterwards use a new archive location and are recorded under the same service UID |
| No additional preserved field | Existing SQL logins managed by credential policies need a resync |

### While the restore runs

- The database is being replaced; applications should retry.
- An operation that is already running is not cancelled, and deleting its record
  does not stop it. Do not create a second restore operation.
- If the operation stays `Running` for a long time, ask your provider.

### Refusals

When the initial validation refuses an in-place restore, the operation is
`Rejected` with one of these reasons:

| Reason | Message | Fix |
|--------|---------|-----|
| `RestoreSourceInvalid` | `in-place restore requires the exact service UID, no deletion in progress and deletionProtection=false` | Set `serviceUID`, and set `deletionProtection: false` first |
| `RestoreSourceInvalid` | `in-place restore requires acknowledgeDataLoss=true, current engineUID and exactly one backupID or targetTime` | Read `engineUID` again; it changes after every in-place restore. Check the selector |
| `RestoreSourceInvalid` | `no verified same-service catalog backup covers the requested recovery point` | The backup ID does not belong to a `Completed` backup of this service, or `targetTime` is outside every verified window |
| `PlanNotEntitled` | `plan <plan> does not allow RestoreInPlace` | Your plan does not allow in-place restore |

Before you create a new operation, make sure the refused one did not already
start the restore: an in-place restore can be rejected after execution has
begun. When you cannot tell, keep the operation and ask your provider for its
outcome. When retrying is safe, fix the cause and create a new operation with a
new name and `idempotencyKey`.

## Point-in-time recovery

A `targetTime` restores the database to a point in time, using a base backup
and the archived WAL after it. The plan field `backup.pitr` advertises this
capability. For each backup, the platform publishes a window of times it has
verified against the archive.

Read the windows from the backup history:

```bash
kubectl get servicebackups -n my-project \
  -l services.kube-dc.com/service-uid=REPLACE_WITH_SERVICE_UID \
  -o custom-columns='BACKUP_ID:.status.backup.id,COMPLETED:.status.backup.completedAt,EARLIEST:.status.backup.recoveryWindow.earliest,LATEST:.status.backup.recoveryWindow.latest,TIMELINE:.status.backup.recoveryWindow.timeline,VERIFIED:.status.backup.recoveryWindow.verifiedAt'
```

How a target is chosen and used:

- The target must be inside the `earliest` to `latest` window of a `Completed`
  backup of the service, and after that backup's completion.
- `RestoreToNew` uses the exact backup selected in `restore.backupRef`.
- An in-place restore selects a suitable same-service backup for its requested time.
- Recovery stops before the first transaction committed after the target.
- We recommend UTC with whole seconds, for example `2026-01-15T10:40:00Z`.

The window trails the present:

- Recent changes become restorable only after their WAL has been archived and
  the platform has verified it. A target from the last few minutes can be
  refused; wait until `latest` passes your target, then submit a new request.
- When backups are turned off, the window stops advancing.

A target outside every verified window is refused before any database is
created or replaced. Read the restore operation's `status.reason` and `status.message` for the refusal.

## Limits

These cases are not yet qualified. Do not rely on them without testing, and ask
your provider if you need them:

- Point-in-time recovery to a time across a PostgreSQL timeline change. A
  switchover, failover or in-place restore starts a new timeline. We recommend
  taking a new backup after such an event.
- Point-in-time recovery when WAL archiving had gaps or failures.
- Restoring a backup whose retention period has expired.
- Creating a new service from a backup that was taken after an in-place restore.
- Recovery from an interruption at every stage of an in-place restore, and the
  remaining failure and retry cases.
