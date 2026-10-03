# Back up and restore managed services

As a Project administrator or developer, use this guide to request backups and restore a retained backup into another service.
The request format is shared. Backup contents and recovery options depend on the family and plan.

## Choose the recovery procedure

Check what the service protects before you choose a recovery method:

| Service | Backup contents | Recovery |
|---|---|---|
| [PostgreSQL](postgresql-backup-restore.md) | Base backups and write-ahead logs | Restore into another service; in-place and point-in-time recovery require support and additional checks |
| [MySQL and MariaDB](managed-services-mysql-mariadb.md#backups-and-restore) | Logical database archives | Restore into another service |
| [Valkey](managed-services-valkey.md#backups-and-restore) | RDB snapshots | Restore into another service |
| [ClickHouse](managed-services-clickhouse.md#backups-and-restore) | Native archives | Restore into another service |
| [Kafka](managed-services-kafka.md#backups) | Metadata export | Does not back up messages or provide database-style restore |
| [GitHub Actions runners](managed-services-github-runners.md) | No persistent worker workspace backup | GitHub retains workflow history and artifacts |

Scheduled backup settings belong to the family guide and the published plan.
Replication and high availability do not replace backups.
Test a restore and verify application data before you depend on a backup policy.

## Before you begin

Check these requirements:

- You have the Project `admin` or `developer` role.
- The plan allows the requested operation, and backups are configured for the service.
- For restore, the archive remains available and the target plan supports its family and format.
- The Project has enough quota for the restored service.

For approval, execution windows, retries, and cancellation, see [Request an operation](managed-services-operations.md).

## Request a backup

1. Read the source service UID:

   ```bash
   kubectl get managedservice <service> -n <project> \
     -o jsonpath='{.metadata.uid}{"\n"}'
   ```

   Replace `<service>` with the service name and `<project>` with its Project namespace.

2. Save this manifest as `service-backup.yaml`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceOperation
   metadata:
     name: service-backup-1
     namespace: <project>
   spec:
     serviceRef:
       name: <service>
     serviceUID: <service-uid>
     type: Backup
     idempotencyKey: service-backup-1
     execution:
       window: Immediate
   ```

   Replace `<service-uid>` with the UID you read. Use the same service name and Project namespace.

3. Submit the request:

   ```bash
   kubectl apply -f service-backup.yaml
   ```

4. Watch its result:

   ```bash
   kubectl get serviceoperation service-backup-1 -n <project> -w
   ```

   Wait for `Succeeded`. If it fails, read `status.reason` and `status.message`.

5. Check the corresponding backup record as described in [Backup history](#backup-history).

## Backup history

The platform records observed backups as `ServiceBackup` resources.
Project roles can read these records but cannot create, change, or delete them.
Records can remain after source deletion and can outlive the stored archive.
A `Completed` record proves completion at the recorded time, not continued recoverability.

List records for an exact source instance:

```bash
kubectl get servicebackups -n <project> \
  -l services.kube-dc.com/service-uid=<service-uid>
```

Replace `<service-uid>` with the source UID. If the source no longer exists, list the Project's records without the label filter.
Read the source name and UID from each record's `spec.serviceRef.name` and `spec.serviceUID`.

Inspect the selected record before restore:

```bash
kubectl get servicebackup <backup-record> -n <project> -o yaml
```

Replace `<backup-record>` with the record's Kubernetes name.
Check these fields:

| Field | Check |
|---|---|
| `metadata.name`, `metadata.uid` | Exact record identity for the restore request |
| `spec.serviceRef.name`, `spec.serviceUID` | Historical source identity |
| `status.backup.phase` | Must be `Completed` |
| `status.backup.id` | Physical backup identity; do not substitute it for the record name |
| `status.recovery` | Retained recovery information, including family, format, and retention deadline |

`status.lastBackup` and `status.recentBackups` on a service show only a recent subset.
Use the catalog records when selecting a restore source.

## Restore into a new service

A `RestoreToNew` operation creates a target in the same Project and leaves the source unchanged.
The source may already be deleted if its recovery information and archive remain available.
Do not write `ManagedService.spec.restoreFrom` yourself; the platform generates it for the operation.

1. Select a completed record using [Backup history](#backup-history).
2. Ask your provider for a compatible target plan, full engine release, and data plane.
3. Save this request as `restore-service.yaml`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceOperation
   metadata:
     name: restore-service-1
     namespace: <project>
   spec:
     serviceRef:
       name: <source-service>
     serviceUID: <source-uid>
     type: RestoreToNew
     idempotencyKey: restore-service-1
     restore:
       backupRef:
         name: <backup-record>
         uid: <backup-record-uid>
       target:
         name: <target-service>
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
     execution:
       window: Immediate
   ```

   Replace `<project>` with the source Project namespace.
   Copy `<source-service>`, `<source-uid>`, `<backup-record>`, and `<backup-record-uid>` from the selected record.
   Choose an unused name for `<target-service>`.
   Use provider-qualified values for `<target-plan>`, `<qualified-version>`, and `<data-plane>`.

   This example uses target plan defaults for capacity and engine settings.
   Check the family guide before submission. Set target `storage`, `compute`, `topology`, or `parameters` when the backup requires different values.
   Put engine settings under `spec.restore.target.parameters`; `spec.parameters` is invalid for this operation.

4. Validate the request:

   ```bash
   kubectl apply --dry-run=server -f restore-service.yaml
   ```

5. Submit it:

   ```bash
   kubectl apply -f restore-service.yaml
   ```

6. Watch the operation:

   ```bash
   kubectl get serviceoperation restore-service-1 -n <project> -w
   ```

7. After `Succeeded`, check that the target service is ready.
8. [Create a binding](managed-services-connect.md#create-a-binding) with the target service's UID and fresh credentials.
9. Verify the recovered data before you move application traffic.

The target uses `deletionPolicy: Retain`. Recreate any credential rotation policies for its identity.
The target plan controls approval and maintenance windows.
Schema validation does not prove that the archive can be restored.
A restore does not perform an engine major upgrade.

This request restores the selected backup's consistency point.
For PostgreSQL recovery times and destructive in-place recovery, see [PostgreSQL backups and recovery](postgresql-backup-restore.md).
Do not set `targetTime` for MySQL, MariaDB, ClickHouse, or Valkey.

## Preserve recovery before deletion

Before deleting a service, check its deletion policy and verify the backups you need.
See [Delete a managed service](managed-services-status-deletion.md).

Deleting an in-Project service's Project removes its backup records and data volumes without a final backup.
Service deletion protection does not prevent Project deletion.
Copy required data outside the Project first.
