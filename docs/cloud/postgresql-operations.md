# PostgreSQL operations

As a Project administrator or developer, use this reference to change PostgreSQL capacity, settings, topology, and engine versions.
Each operation lists its parameters, plan requirements, and limits.
For backups and recovery, see [PostgreSQL backups and recovery](postgresql-backup-restore.md).
For the shared request lifecycle, see [Request a managed service operation](managed-services-operations.md).

## Before you begin

- The `admin` or `developer` role. `project-manager` and `user` can read
  operations but cannot create, change or cancel them. See
  [Project roles](managed-services.md#project-roles).
- The service UID:

  ```bash
  kubectl get managedservice orders-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
  ```

- The values of your plan's `operations.allowed`, `operations.autoApprove` and
  `maintenance` fields, and of the plan fields each operation type needs. You
  cannot read the plan; ask your provider.

Throughout this page, replace `my-project` with your Project's backing
namespace.

## Create an operation

This example changes one PostgreSQL setting and resets another to its managed
default. Save it as `orders-db-parameters-1.yaml`:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-parameters-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: UpdateParameters
  idempotencyKey: orders-db-parameters-1
  parameters:
    parameters:
      work_mem: 16MB
    resetParameters:
      - statement_timeout
  execution:
    window: Immediate
```

```bash
kubectl apply -f orders-db-parameters-1.yaml
kubectl get serviceoperation orders-db-parameters-1 -n my-project -w
```

| Field | Required | Description |
|-------|----------|-------------|
| `metadata.name` | Yes | A name for this one request |
| `spec.serviceRef.name` | Yes | The `ManagedService` name |
| `spec.serviceUID` | Yes | The `ManagedService` UID. See [Service identity](managed-services.md#the-service-uid) |
| `spec.type` | Yes | The operation type. See [Operation types](#operation-types) |
| `spec.idempotencyKey` | Yes | 1 to 128 characters that identify this request across retries |
| `spec.parameters` | Depends on the type | The type's parameters |
| `spec.execution.window` | No | `Immediate`, the default, or `NextPlanWindow`. See [Execution windows](managed-services-operations.md#approval-and-execution-windows) |

The whole `spec` is immutable. An attempt to change it is refused with
`ServiceOperation spec is immutable`.

## How an operation runs

See [Operation phases and results](managed-services-operations.md#read-the-result).
PostgreSQL checks plan bounds and engine health before execution and before each change.
A refusal after execution starts normally ends in `Failed` with reason `PreflightFailed`.
An in-place restore that has started recovery can remain `Running` while it retries. Keep that operation and contact your provider.

<span id="service-identity" />
<span id="retries-and-idempotency" />
<span id="execution-windows" />
<span id="approval" />
<span id="cancel-an-operation" />

Shared request rules apply to PostgreSQL:

- [Pin the service UID](managed-services.md#the-service-uid) before submitting a request.
- [Retry with the same name and key](managed-services-operations.md#retries-and-idempotency) when a submission's outcome is uncertain.
- [Check approval and execution windows](managed-services-operations.md#approval-and-execution-windows) when a request waits.
- [Request cancellation](managed-services-operations.md#cancel-a-waiting-operation) before execution starts.

### Operations that wait for each other

Operations on the same service run one at a time within a group. A later
operation waits in `Pending` with `OperationConflict` while an operation of the
same group is `Accepted` or `Running`.

| Group | Types |
|-------|-------|
| Backup | `Backup` |
| Restore | `RestoreInPlace` |
| Topology | `Scale`, `Resize`, `Switchover`, `Failover`, `MinorUpgrade`, `MajorUpgrade`, `Hibernate`, `Resume` |
| Storage | `ExpandStorage` |
| Credentials | `RotateCredentials` |
| Parameters | `UpdateParameters` |

While a service is hibernated, every type except `Resume` and `Hibernate` is
`Rejected` with `ClusterHibernated`.

## Operation types

Every type must be listed in your plan's `operations.allowed`; otherwise the
operation is `Rejected` with `PlanNotEntitled`. The third column lists the other
plan fields a type depends on.

| Type | Parameters | Other plan fields | Requirements and limits |
|------|------------|-------------------|-------------------------|
| `Backup` | None | `backup.enabled` | Backups must also be on for the service (`parameters.backup.enabled`). See [Backups and restore](postgresql-backup-restore.md#take-a-backup-now) |
| `RestoreInPlace` | `engineUID`, `acknowledgeDataLoss: true`, and exactly one of `backupID` or `targetTime` | `capacity.allocationProtocol: v1`, `backup.enabled`, and the backup's major version: an image of that version in `allowedImages` or, when `allowedImages` is empty, `engineVersion` equal to it | Destructive. See [Restore in place](postgresql-backup-restore.md#restore-in-place) |
| `Scale` | `instances`: integer | `topology.minInstances`, `topology.maxInstances` | See [Scale](#scale) |
| `Resize` | `cpu`, `memory`: quantity strings. Omit one to keep its current value | `capacity.computeBounds`, `capacity.allocationProtocol: v1` | See [Resize](#resize) |
| `ExpandStorage` | `size`: quantity | `capacity.maxStorage` | Refused on storage that cannot expand. See [Expand storage](#expand-storage) |
| `Switchover` | `targetInstance`: optional, a ready replica | None | Two or more instances and a healthy service |
| `Failover` | `targetInstance`: optional, a running replica | None | Two or more instances. Can lose data |
| `RotateCredentials` | `role`; `resyncRootUID` only after an in-place restore | `credentials.allowExistingUsers` for an existing-user role | See [Credentials and rotation](postgresql-credentials.md) |
| `MinorUpgrade` | `imageName` | `allowedImages` | Same major version only. See [Upgrades](#upgrades) |
| `MajorUpgrade` | `imageName` | `allowedImages`, `backup.enabled` | The database is offline during the upgrade. See [Upgrades](#upgrades) |
| `UpdateParameters` | `parameters`: map of settings to string values; `resetParameters`: list of setting names | None | See [PostgreSQL settings](#postgresql-settings) |
| `Hibernate` | None | None | A healthy service. See [Hibernate and resume](#hibernate-and-resume) |
| `Resume` | None | None | Only for a hibernated service |

Reasons that type-specific checks report when they refuse an operation before
execution starts:

| Reason | Reported when |
|--------|---------------|
| `InvalidParameters` | A parameter is missing, malformed or not valid for this type |
| `NoChange` | The service already has the requested value or state |
| `PlanLimitExceeded` | A value is outside the plan's bounds, or more instances are requested than schedulable nodes exist |
| `ClusterNotHealthy` | The type needs a healthy service |
| `ClusterHibernated` | The service is hibernated. Run `Resume` first |
| `NoReplica` | No suitable replica exists for a switchover, failover or scale-in |
| `ImageNotAllowed` | The upgrade image is not in the plan's `allowedImages` |
| `MajorVersionChange` | A `MinorUpgrade` image has a different major version |
| `StorageNotExpandable` | The service's storage class does not support expansion |
| `BackupNotConfigured` | Backups are not configured for the service |
| `BackupPreflightFailed` | A `MajorUpgrade` found no completed backup with healthy archiving |

### Scale

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-scale-2
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Scale
  idempotencyKey: orders-db-scale-2
  parameters:
    instances: 2
  execution:
    window: Immediate
```

- `instances` must be within the plan's `topology.minInstances` and
  `topology.maxInstances`, or the operation is `Rejected` with
  `PlanLimitExceeded`.
- A service with two or more instances keeps them on separate hosts. Growing to
  more instances than the service has schedulable nodes is `Rejected` with
  `PlanLimitExceeded`.
- Scaling out adds instances one at a time. A new replica gets the service's
  current CPU and memory.
- Scaling in retires the highest-numbered replicas and deletes their data
  volumes. If the primary would be retired, the platform switches over to the
  lowest-numbered ready replica first.

### Resize

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-resize-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Resize
  idempotencyKey: orders-db-resize-1
  parameters:
    # Per instance, within the plan's capacity.computeBounds.
    cpu: 750m
    memory: 1536Mi
  execution:
    window: Immediate
```

`cpu` and `memory` set the request and limit of each instance; use whole
millicores for `cpu` and whole bytes for `memory`. The platform first reserves
capacity for the change, then updates the instances. Connections may be
interrupted during the rolling update. A service is not resized while it is
unhealthy.

### Expand storage

`ExpandStorage` takes `size`, a quantity larger than the current size and at
most the plan's `capacity.maxStorage`. It is `Rejected` with
`StorageNotExpandable` unless the service's storage class supports expansion;
the service reports this as `"true"` or `"false"` in
`status.engineDetails.storageExpansion`. Volumes never shrink.

### Switchover and failover

`Switchover` promotes a ready replica while the primary is healthy, without data
loss. `Failover` promotes a running replica without waiting for the current
primary. Use `Failover` only when the primary is unhealthy.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-switchover-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Switchover
  idempotencyKey: orders-db-switchover-1
  # Omit parameters to let the platform choose a ready replica.
  execution:
    window: Immediate
```

- The current primary is reported in `status.engineDetails.currentPrimary`.
- Applications must reconnect after either operation.
- A `Failover` can discard transactions that the old primary committed but had
  not yet streamed to the new primary. The service may stay degraded until the
  old primary rejoins.

### Upgrades

Both upgrade types take an exact image name that must be in your plan's
`allowedImages`. Ask your provider for the image names.

- **`MinorUpgrade`** moves the service to another image of the same PostgreSQL
  major version. Replicas are updated first, then the primary is switched over.
  Moving to a different image reference in `allowedImages` rolls the instances
  even when the PostgreSQL patch version stays the same. A new service starts on
  the newest image its plan offers for its major version, so an advance of the
  PostgreSQL patch version is possible only after your provider adds an image
  with a newer patch version to the plan.
- **`MajorUpgrade`** moves the service to a higher major version. It needs a
  healthy service, backups configured, and a completed backup with healthy
  archiving. The database is unavailable for the whole upgrade, and extensions
  in use must exist in the target image. The operation reports success only
  after a backup on the new major version has been created and verified. The
  backup taken before the upgrade keeps its original major version.

This example schedules a major upgrade for the plan's maintenance window:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-major-upgrade-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: MajorUpgrade
  idempotencyKey: orders-db-major-upgrade-1
  parameters:
    # An image from your plan's allowedImages with a higher major version.
    imageName: REPLACE_WITH_IMAGE_FROM_YOUR_PLAN
  execution:
    window: NextPlanWindow
```

### PostgreSQL settings

`UpdateParameters` changes allow-listed settings; the list is in
[Create a PostgreSQL service](postgresql-create.md#parameter-reference).

- Give every value in `parameters` as a string.
- `resetParameters` returns the listed settings to their managed defaults. It
  must be a non-empty list, and one operation cannot set and reset the same
  setting.
- Settings that need a restart are applied with a rolling restart and a
  switchover. Other settings are reloaded without a restart.
- A completed `UpdateParameters` takes precedence over the same key in the
  manifest.

### Hibernate and resume

`Hibernate` stops every instance and keeps the data volumes. While the service
is hibernated, connections fail and `Ready` is `False`. `Resume` starts the
instances again from the retained volumes.

## Limits

These cases are not yet qualified. Test them before you rely on them:

- A successful `ExpandStorage`. Only the refusal on storage that cannot expand
  has been qualified.
- Waiting for and running in a `NextPlanWindow` maintenance window.
- Cancelling an operation that you created.
- A `MinorUpgrade` that advances the PostgreSQL patch version. Only a change of
  image reference with the same patch version has been qualified.
- Recovery after an interrupted `MinorUpgrade` or `MajorUpgrade`.
- `Resize` when capacity is not available, and recovery after an interrupted
  `Resize`.
- `Scale`, `Switchover` and `Failover` after a lost node or during other
  failures.
- `UpdateParameters` requests that fail partway, and their retries.

Every disruptive operation, including scaling in, `Resize`, `Switchover`,
`Failover`, upgrades and settings that need a restart, can interrupt existing
connections. Applications must reconnect and retry.
