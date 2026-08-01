# Data Protection and Recovery

Backups in Kube-DC are service-specific. There is no single Project backup that
automatically protects every VM disk, database, object, and Kubernetes resource.

Start by identifying the data owner:

| Resource | Supported protection path | What it protects |
|----------|---------------------------|------------------|
| Managed PostgreSQL or MariaDB | Database backup and restore | Database engine data and recovery metadata |
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

## Managed Database Backups

Configure backups on the `KdcDatabase` or in the database detail view. Confirm
that the database reports a successful backup before relying on it.

A database recovery plan should record:

- backup schedule and retention
- last successful backup time
- recovery mode: restored copy or destructive in-place restore
- PostgreSQL point-in-time recovery window, when enabled
- encryption key and object-storage dependencies
- application maintenance and credential behavior during restore

A restored copy is safer for validation because it leaves the source database
running. In-place restore replaces current data and requires a maintenance
window.

See [Managed Databases: Backups](managed-databases.md#backups) for configuration
and restore procedures.

## Managed Cluster Snapshots

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

## Applications and Persistent Volumes

A PVC is storage, not a backup. For stateful applications, use a
consistency-aware tool that understands the data format, then write the backup
to a different failure domain such as [Object Storage](object-storage.md).

Examples include:

- database-native dumps for an application-managed database
- an application export followed by an object-storage upload
- a Job that mounts the PVC read-only and archives files after the application
  has quiesced writes

A storage clone in the same system is useful for testing, but it is not a
disaster-recovery copy by itself.

## Virtual Machines

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

## Object Storage

Object storage is a destination for backups, not automatically a backup of
itself. Decide whether your application needs versioning, retention, replication,
or an export to another account or provider. Test access with the same
credentials and endpoint the restore process will use.

## Define the Recovery Objective

For each production workload, record:

- **RPO**: how much recent data can be lost
- **RTO**: how long recovery may take
- backup owner and alert recipient
- retention and deletion policy
- encryption keys and credential custody
- restore order for database, files, configuration, and network exposure

Run a restore test on a schedule. A successful upload proves that a backup was
written; only a restore test proves that it is usable.

## Next Steps

- [Managed Databases](managed-databases.md)
- [Managed Clusters](cluster-management.md)
- [Object Storage](object-storage.md)
- [Block Storage](block-storage.md)
- [GitOps](gitops.md)
