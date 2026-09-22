# Data protection and recovery

Backups in Kube-DC are service-specific. There is no single Project backup that
automatically protects every VM disk, database, object, and Kubernetes resource.

Start by identifying the data owner:

| Resource | Supported protection path | What it protects |
|----------|---------------------------|------------------|
| Managed service (PostgreSQL, MySQL, MariaDB, ClickHouse) | Scheduled and on-demand backups, restore into a new service, or restore in place for PostgreSQL | Database data, archived WAL where the family has it, and `ServiceBackup` history records |
| Managed service (Valkey, Kafka) | No recovery facility; see the family page | Rebuildable data: caches, and topics protected by replication |
| Managed Cluster | etcd snapshots | Kubernetes API state in that Managed Cluster |
| Application files on a PVC | Application-native backup to object storage | Files selected by the application |
| Object storage bucket | Application retention, versioning, or replication policy | Objects covered by that policy |
| Project manifests | Git or another configuration repository | Desired configuration, not runtime data |
| VM or arbitrary Project PVC | No general Project-wide self-service workflow | Use an application-consistent method or a provider-approved storage workflow |

:::warning No Project-wide Velero workflow
Do not create Velero `Backup`, `Restore`, or `Schedule` resources from a
Project guide. The platform Velero installation and its namespace are
operator-owned, and a metadata-only capture is not a VM or PVC data backup.
Contact your provider when you need a platform-level recovery service.
:::

## Managed service backups

A `ManagedService` takes scheduled backups when both the plan's
`backup.enabled` and the service's backup settings are on. Take an on-demand
backup with a `Backup` operation or **Back up now** in the console, read the
history from `ServiceBackup` records or the **Backups** tab, and restore into a
new service; PostgreSQL also restores in place and offers point-in-time
recovery. See [Backups and restore](postgresql-backup-restore.md) and the
limits on each family page.

For the in-Project placement, deleting a Project deletes its services without a
final backup; see [Delete a Project](managed-services-status-deletion.md#delete-a-project).

## Databases still on db-manager

`KdcDatabase` databases are deprecated. Their backups keep running until you
migrate; configure them on the resource or in the deprecated Databases view of
the console, and confirm that the database reports a successful backup before
relying on it. See
[Migrating from db-manager databases](managed-services-migration.md).

## Managed Cluster snapshots

Managed Cluster backups are etcd snapshots. They protect Kubernetes API state,
including resources stored in etcd. They do **not** copy application data from
PersistentVolumes, external databases, or object storage.

Use the cluster detail view's danger zone to list snapshots, take an on-demand
snapshot, or start a restore. During restore, the Managed Cluster API is
temporarily unavailable and API state created after the selected snapshot is
lost. Worker workloads may continue running, but their control-plane view is
rolled back.

Before restoring:

1. Confirm the selected snapshot completed successfully.
2. Back up workload data through its owning service.
3. Record changes made after the snapshot.
4. Notify application owners of the API interruption.
5. Verify nodes, controllers, and workloads after the restore.

Scheduled snapshots require the platform's managed backup bucket to be
available. Check the cluster backup status rather than assuming backup is
enabled on every installation.

## Applications and persistent volumes

A PVC is storage, not a backup. For stateful applications, use a
consistency-aware tool that understands the data format, then write the backup
to a different failure domain such as [Object storage](object-storage.md).

Examples include:

- database-native dumps for an application-managed database
- an application export followed by an object-storage upload
- a Job that mounts the PVC read-only and archives files after the application
  has quiesced writes

A storage clone in the same system is useful for testing, but it is not a
disaster-recovery copy by itself.

## Virtual machines

Back up data from inside the guest or with an application-consistent storage
workflow approved by the provider. A VM manifest contains hardware and network
configuration; it does not contain the bytes on the attached disk.

For recoverability, keep:

- the VM manifest or build automation in version control
- guest configuration outside the VM image
- application data backups in a separate storage system
- a documented method to recreate network exposure and credentials

Do not remove VM, PVC, or snapshot finalizers to force a restore or deletion.
That can orphan storage and make recovery harder.

## Object storage

Object storage is a destination for backups, not automatically a backup of
itself. Decide whether your application needs versioning, retention, replication,
or an export to another account or provider. Test access with the same
credentials and endpoint the restore process will use.

## Define the recovery objective

For each production workload, record:

- **RPO**: how much recent data can be lost
- **RTO**: how long recovery may take
- backup owner and alert recipient
- retention and deletion policy
- encryption keys and credential custody
- restore order for database, files, configuration, and network exposure

Run a restore test on a schedule. A successful upload proves that a backup was
written; only a restore test proves that it is usable.

## Next steps

- [Managed Services: Backups and Restore](postgresql-backup-restore.md)
- [Migrating from db-manager databases](managed-services-migration.md)
- [Managed Clusters](cluster-management.md)
- [Object storage](object-storage.md)
- [Block storage](block-storage.md)
- [GitOps](gitops.md)
