# Coming from KdcDatabase

If you run PostgreSQL with `KdcDatabase` (see
[Managed Databases](managed-databases.md)) and `DatabaseCredentialPolicy` (see
[Database Credentials](database-credentials.md)), this page maps what you know
to Managed Services. It also lists the differences that affect your manifests
and applications.

:::warning No migration path yet
Kube-DC does not yet provide a way to move a `KdcDatabase` to Managed Services.
A `KdcDatabase` cannot be converted into a `ManagedService`, and a
`ManagedService` cannot be restored from a `KdcDatabase` backup. Existing
`KdcDatabase` resources remain supported. To use Managed Services for an
existing database, you create a new service and copy the data with your own
tools, and then test your applications against it.
:::

- The two APIs are separate: a `KdcDatabase` does not appear as a
  `ManagedService`, and the reverse is also true.
- Managed Services does not offer MariaDB. Keep MariaDB databases on
  `KdcDatabase`.
- This chapter documents Managed Services with Kubernetes manifests and
  `kubectl`. The Databases dashboard views and the `kube-dc db credentials`
  commands described in Managed Databases and Database Credentials work with
  `KdcDatabase` and `DatabaseCredentialPolicy` resources.

## Resources

| You use today | In Managed Services |
|---------------|---------------------|
| `KdcDatabase` (`db.kube-dc.com/v1alpha1`) | `ManagedService` (`services.kube-dc.com/v1alpha1`) with the class `postgresql`, a plan and the connectivity class `tenant-native`. See [Classes and Plans](managed-services-plans.md) |
| The engine Secret `<db>-app`, or the Secret that a `DatabaseCredentialPolicy` projects | A `ServiceBinding`, which delivers a `Secret` with the connection details, the credential and the CA certificate. See [Connect Applications](postgresql-connect.md) |
| `DatabaseCredentialPolicy` (`security.kube-dc.com/v1alpha1`) | A `ServiceCredentialPolicy` for scheduled rotation, and a `ServiceBinding` for delivery. See [Credentials and Rotation](postgresql-credentials.md) |
| Native engine backup objects that you create and list | A `Backup` operation, and read-only `ServiceBackup` history records. See [Backups and Restore](postgresql-backup-restore.md) |
| Field edits, annotations and CLI commands for day-2 actions | One `ServiceOperation` per action. See [Day-2 Operations](postgresql-operations.md) |

## Service fields

| `KdcDatabase` field | `ManagedService` equivalent | Difference |
|---------------------|-----------------------------|------------|
| `spec.engine: postgresql` | `spec.classRef.name: postgresql` | The class, plan, placement and connectivity cannot change after creation |
| `spec.version` | The plan's `engineVersion`. `spec.parameters.version` is optional and must equal it | You choose a version by choosing a plan. The version is `CreateOnly`; move to another image with a `MinorUpgrade` or `MajorUpgrade` operation |
| `spec.databaseName` | `spec.parameters.database` | `CreateOnly` |
| `spec.username` | `spec.parameters.owner` | `CreateOnly`. The login is delivered through the `owner` credential role |
| `spec.cpu`, `spec.memory` | `spec.parameters.cpu`, `spec.parameters.memory` | Only within the plan's `capacity.computeBounds`. Change them with a `Resize` operation |
| `spec.storage` | `spec.parameters.storage.size` | At most the plan's `capacity.maxStorage`. Grow it with an `ExpandStorage` operation where the storage class supports expansion. Successful expansion is not yet qualified; see [Expand storage](postgresql-operations.md#expand-storage) |
| `spec.storageClassName` | `spec.parameters.storage.class` | `CreateOnly`. The default is the plan's `capacity.storageClass`; the other classes you may select are in `capacity.allowedStorageClasses` |
| `spec.replicas` | `spec.parameters.instances` | Within the plan's `topology.minInstances` and `topology.maxInstances`. Change it with a `Scale` operation |
| `spec.parameters` | `spec.parameters.postgresql.parameters`, or an `UpdateParameters` operation | Allow-listed settings only, with string values. A completed operation takes precedence over the manifest |
| `spec.backup.enabled`, `spec.backup.schedule`, `spec.backup.retentionDays` | `spec.parameters.backup.enabled`, `spec.parameters.backup.schedule`, `spec.parameters.backup.retentionDays` | Backups run only when the plan's `backup.enabled` is also `true`. `retentionDays` must be within the plan's `backup.minRetentionDays` and `backup.maxRetentionDays`, and scheduled runs must be at least `backup.minScheduleIntervalMinutes` apart. The schedule is a five-field cron expression in UTC |
| `spec.backup.s3Endpoint`, `spec.backup.s3CredentialSecret` | `spec.parameters.backup.store` | Needs the plan's `backup.allowedCustomEndpoints`. `CreateOnly` |
| `spec.expose.type: internal` | `spec.parameters.expose.type: internal` | The default |
| `spec.expose.type: gateway` | `spec.parameters.expose.type: gateway` | Needs the plan's `exposure.gateway`. See [External Access](postgresql-external-access.md) |
| `spec.expose.type: loadbalancer` | Not covered in this chapter | Ask your provider which external access options your plan offers |
| `spec.breakGlass.enableSuperuserAccess` | `spec.parameters.breakGlass.enableSuperuserAccess` | Needs the plan's `credentials.allowBreakGlass` |
| `spec.restoreFrom.backupName`, `spec.restoreFrom.targetTime` | `spec.restoreFrom.serviceRef.name`, `spec.restoreFrom.serviceUID`, and `backupID` or `targetTime` | `backupID` is the `status.backup.id` of a `ServiceBackup`, not the name of a backup object. The source is a `ManagedService` in the same Project |
| None | `spec.deletionPolicy`, `spec.deletionProtection` | New. The default policy is `Retain`. See [Status and Deletion](managed-services-status-deletion.md) |

| `KdcDatabase` status | `ManagedService` status |
|----------------------|-------------------------|
| `status.phase`, `status.conditions` | `status.phase` and conditions, together with the accepted and applied revisions. See [Is the result current?](postgresql-create.md#is-the-result-current) |
| `status.endpoint`, `status.externalEndpoint` | `status.endpoints`, and the `host` and `port` keys of a binding `Secret` |
| `status.restore` | The status of the `RestoreInPlace` operation |

## Day-2 actions

| With `KdcDatabase` | With Managed Services |
|--------------------|-----------------------|
| Change `spec.version` for a major upgrade | A `MajorUpgrade` operation with an `imageName` from the plan's `allowedImages`. It needs the plan's `backup.enabled`, backups configured on the service and a completed backup. See [Upgrades](postgresql-operations.md#upgrades) |
| Create a native backup object for an on-demand backup | A `Backup` operation. See [Take a backup now](postgresql-backup-restore.md#take-a-backup-now) |
| List native backup objects | List `ServiceBackup` records. See [Backup history](postgresql-backup-restore.md#backup-history) |
| Annotate with `kube-dc.com/restore-from` for an in-place restore | A `RestoreInPlace` operation with `engineUID`, `acknowledgeDataLoss: true`, and `backupID` or `targetTime`. `deletionProtection` must be `false`. See [Restore in place](postgresql-backup-restore.md#restore-in-place) |
| `kube-dc db credentials rotate` | A `RotateCredentials` operation. See [Rotate a credential now](postgresql-credentials.md#rotate-a-credential-now) |
| `kube-dc db credentials get --show-password` | Read the binding `Secret`, or the role's `Secret` named in `status.credentials`. See [Read credential status](postgresql-credentials.md#read-credential-status) |

## Credential policies

| `DatabaseCredentialPolicy` field | `ServiceCredentialPolicy` equivalent | Difference |
|----------------------------------|--------------------------------------|------------|
| `spec.databaseRef.name` | `spec.serviceRef.name` and `spec.serviceUID` | `serviceUID` is required |
| `spec.username` of the application user | `spec.role: owner` | `spec.role: readonly` rotates the read-only login |
| `spec.username` of another existing login | `spec.existingUser.username` and `spec.existingUser.database` | Needs the plan's `credentials.allowExistingUsers`. The login must already exist and be eligible |
| `spec.mode: static-rotated` | None | A policy only rotates passwords. It never creates a login |
| `spec.rotation.interval`, for example `30d` | `spec.rotationIntervalSeconds`, from `60` to `31536000` | The default is `2592000` (30 days) |
| `spec.rotation.strategy` | None | |
| `spec.sync.enabled`, `spec.sync.targetSecretName` | A `ServiceBinding` with `delivery.secretName` | A policy never delivers a `Secret` itself |

Deleting a `DatabaseCredentialPolicy` removes the Secret it projected. Deleting a
`ServiceCredentialPolicy` of a declared role keeps its bindings and their
Secrets. See [Retire a policy](postgresql-credentials.md#retire-a-policy).

## Differences in behaviour

### Service identity and ordering

Bindings, operations and credential policies pin the service UID. Create the
service first, read its `metadata.uid`, and then create the objects that
reference it. A GitOps pipeline must read the UID and pin it. See
[The service UID](managed-services.md#the-service-uid).

### Plans

The plan decides the version (`engineVersion`, `allowedImages`), the bounds,
the operations you may request (`operations.allowed`), which of them wait for
provider approval (`operations.autoApprove`), backups (`backup.*`) and
entitlements. You cannot read the plan; ask your provider for its values. See
[What a plan decides](managed-services-plans.md#what-a-plan-decides).

### Changes after creation

Several fields that you edit on a `KdcDatabase` cannot be edited on a
`ManagedService`. Editing them on the service is refused:

- `OperationOnly` parameters change only through their matching operation:
  `instances` with `Scale`, `cpu` and `memory` with `Resize`, and
  `storage.size` with `ExpandStorage`.
- `CreateOnly` parameters, such as `version`, `database`, `owner`,
  `readonlyRole`, `storage.class` and `backup.store`, cannot change on an
  existing service. To use a different value, create a new service. A
  `MinorUpgrade` or `MajorUpgrade` moves the service to another image without
  changing `parameters.version`.

Reverting the manifest, for example with a Git rollback, does not undo a
completed operation. See [Day-2 changes](managed-services.md#day-2-changes)
and the [parameter reference](postgresql-create.md#parameter-reference).

### Connections and TLS

- A binding `Secret` uses `sslmode=verify-full` and includes `ca.crt`. The
  legacy PostgreSQL `dsn` uses `sslmode=require`, which does not check the
  server's host name. Mount the CA and keep full verification.
- Several Secret keys have different names. See
  [Moving from KdcDatabase Secret keys](postgresql-connect.md#moving-from-kdcdatabase-secret-keys).
- For Gateway access, the `KdcDatabase` guide uses `sslmode=require` and a
  port that differs from the reported endpoint. A Managed Services binding
  delivers the listener port, and the server certificate names the Gateway
  host name, so `verify-full` works. Both paths need a client with direct TLS
  negotiation. See [External Access](postgresql-external-access.md).

### Credential access and roles

Managed Services does not use the Organization-wide credential API described in
[Database Credentials](database-credentials.md#current-organization-wide-openbao-boundary).
Access to credential values follows Project Secret access; see
[The Project is the credential boundary](managed-services.md#the-project-is-the-credential-boundary).

| Action | `KdcDatabase` and `DatabaseCredentialPolicy` | Managed Services |
|--------|----------------------------------------------|------------------|
| Rotate a credential now | `admin` and `project-manager`, through the API | `admin` and `developer`, with a `RotateCredentials` operation |
| Create or delete a credential policy | `admin` and `developer` | `admin` and `developer` |
| Change an existing credential policy | `admin`, `developer` and `project-manager` | `admin`, `developer` and `project-manager` |
| Read a current password | `admin` and `project-manager` through the API; Secret readers from a synced Secret | Secret readers: `admin`, `developer` and `project-manager` |

A `RotateCredentials` operation also needs `RotateCredentials` in the plan's
`operations.allowed`, and waits for provider approval unless the plan lists it
in `operations.autoApprove`.

### Backups and restore

- You do not create native engine backup objects. Take an on-demand backup with
  a `Backup` operation.
- The backup history is kept in `ServiceBackup` records, which remain after the
  service is deleted.
- A restore selects a backup by the source service UID and the backup ID, not
  by the name of a backup object.
- Point-in-time recovery uses the verified window published on each backup
  record, which trails the present. See
  [Point-in-time recovery](postgresql-backup-restore.md#point-in-time-recovery).
- An in-place restore keeps the service name, its UID and its bindings.

### Upgrades

A major upgrade is a `MajorUpgrade` operation with an image from your plan's
`allowedImages`, not a change of the version field. It needs the plan's
`backup.enabled`, backups configured on the service and a completed backup. It
reports success only after a backup on the new major version has been created
and verified. See [Upgrades](postgresql-operations.md#upgrades).

### Deletion

The `KdcDatabase` guide warns that deleting a database removes all of its data.
A `ManagedService` has a deletion policy instead. The default `Retain` keeps the
engine and its volumes, which keep using capacity and quota. `Delete` needs a
confirmation annotation, and `deletionProtection` refuses the deletion. For the
in-Project placement, deleting the Project still deletes every service. See
[Status and Deletion](managed-services-status-deletion.md).

## What stays the same

- The Project is the credential boundary. Identities that can read Secrets in
  the Project can read database credentials.
- The platform protects the engine's own objects from Project identities.
  Change a database only through its own resources. See
  [What the platform protects](managed-databases.md#what-the-platform-protects-and-what-that-means-for-your-workloads).
- Running containers do not pick up a rotated password from environment
  variables. Restart or reload the workloads after a rotation.
