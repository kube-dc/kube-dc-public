# Day-2 Operations

A `ServiceOperation` asks the platform to perform one action on a service, such
as a backup, a scale change, a switchover or an upgrade. Each operation is an
immutable record: you create it once, the platform checks it against your plan
and the service's state, runs it, and records the outcome in its status.

This page explains how operations run and lists every PostgreSQL operation
type with its parameters, the plan fields it needs and its limits. To restore a
backup into a new service, create a new `ManagedService` with `restoreFrom`
instead; see [Backups and Restore](postgresql-backup-restore.md).

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
| `spec.serviceUID` | Set it | The `ManagedService` UID. See [Service identity](#service-identity) |
| `spec.type` | Yes | The operation type. See [Operation types](#operation-types) |
| `spec.idempotencyKey` | Yes | 1 to 128 characters that identify this request across retries |
| `spec.parameters` | Depends on the type | The type's parameters |
| `spec.execution.window` | No | `Immediate`, the default, or `NextPlanWindow`. See [Execution windows](#execution-windows) |

The whole `spec` is immutable. An attempt to change it is refused with
`ServiceOperation spec is immutable`.

## How an operation runs

| `status.phase` | Set when |
|----------------|----------|
| `Pending` | The operation waits. `status.reason` names the cause, for example `OperationConflict` (another operation of the same group is active), `AwaitingMaintenanceWindow`, or `ClassNotVerified` (the class or plan is disabled or not verified) |
| `Preflight` | The service is not yet placed with an accepted configuration (`PendingCommit`), or capacity for the operation is still being requested |
| `AwaitingApproval` | The plan does not auto-approve this type (`ApprovalRequired`) |
| `Accepted` | The queueing checks described below passed and the operation is queued for execution. Checks at execution time can still refuse it |
| `Running` | Execution has started |
| `Succeeded` | Execution completed. `status.reason` is `Completed` |
| `Rejected` | A check refused the operation. `status.reason` names the check. A service identity or plan entitlement check can also reject an operation after it was accepted, and execution can begin before the displayed phase shows `Running`, so `Rejected` alone does not prove that nothing was executed |
| `Failed` | Execution started and ended without success. `status.reason` and `status.message` say why |
| `Cancelled` | The operation was withdrawn before execution |

Before an operation is queued, the platform checks, in this order: the service
identity, a cancel request, whether the plan allows the type, the idempotency
key, whether the service is placed, whether the service is frozen, other active
operations of the same group, approval, and the execution window.

Checks specific to the type, such as plan bounds and the health of the service,
run before execution starts and again immediately before every change the
operation makes:

- When a check refuses before execution starts, the operation ends `Rejected`
  with the check's reason, for example `StorageNotExpandable`.
- When a check refuses after execution has started, the operation normally ends
  `Failed` with reason `PreflightFailed`, and `status.message` names the check.
  An in-place restore that has begun recovery is the exception: it can stay
  `Running` while it retries. Keep that operation and ask your provider.

Read the outcome:

```bash
kubectl get serviceoperation orders-db-parameters-1 -n my-project \
  -o jsonpath='phase={.status.phase} reason={.status.reason}{"\n"}message={.status.message}{"\n"}started={.status.startedAt} completed={.status.completedAt}{"\n"}result={.status.result}{"\n"}'
kubectl get events -n my-project --field-selector involvedObject.name=orders-db-parameters-1
```

| Field | Contents |
|-------|----------|
| `status.reason`, `status.message` | Why the operation is in its phase |
| `status.acceptedAt`, `status.startedAt`, `status.completedAt` | Timestamps |
| `status.checkpoints` | Steps the operation has recorded |
| `status.result` | A bounded result map. Keys that look like passwords, secrets or tokens are removed |

After an operation succeeds, values it changed appear in the service's
`status.effectiveConfiguration`, not in `spec.parameters`. Do not copy them
back into the manifest.

### Service identity

- If `serviceUID` does not match the current service of that name, the
  operation is `Rejected` with `ServiceIdentityChanged`. Create a new operation
  for the new service.
- An operation without `serviceUID` can pass API admission. `RestoreInPlace`
  and rotations of an existing-user role are refused without it; other
  operations then act on whichever service holds the name. Always set it.

### Retries and idempotency

- To retry a submission whose result you did not see, apply the same manifest
  again, with the same name and `idempotencyKey`. The existing record is kept
  and the action is not repeated.
- A new operation with the same `idempotencyKey` as an earlier operation on the
  same service is `Rejected` with `OperationConflict`, unless the earlier one
  ended `Failed` or `Rejected`.
- Before you create a new operation after `Failed` or `Rejected`, make sure the
  earlier one did not already make changes: execution can begin before the
  displayed phase shows `Running`. When you cannot tell, keep the operation and
  ask your provider for its execution outcome. Once retrying is safe, fix the
  cause and create a new operation with a new name and a new `idempotencyKey`.
- An accepted operation is not rejected because its result is slow to appear; it
  stays queued.
- A Project identity can delete an operation record only after it reaches
  `Succeeded`, `Failed`, `Rejected` or `Cancelled`.

### Execution windows

| `execution.window` | When the operation may start |
|--------------------|------------------------------|
| `Immediate` | As soon as its checks pass |
| `NextPlanWindow` | Inside the plan's recurring maintenance window. Until then the operation is `Pending` with reason `AwaitingMaintenanceWindow`, and the message names the window and the time until it opens |

The plan's `maintenance` field defines the window with `maintenance.day` (`Mon`
to `Sun`, or `Any`), `maintenance.startHour` in UTC and
`maintenance.durationMinutes`. If your plan has no `maintenance` window, an
operation with `NextPlanWindow` is `Rejected` with `PlanNotEntitled`. The
approval check comes before the window check, so neither window skips
approval.

### Approval

Types listed in the plan's `operations.autoApprove` run without approval. Every
other allowed type stops in `AwaitingApproval` with reason `ApprovalRequired`
before it is queued.

Approval belongs to your provider. The status message names an approval
annotation, but admission refuses that annotation from Project identities. Ask
your provider to approve the operation, or cancel it if you no longer need it.

### Cancel an operation

You can ask to withdraw an operation you created until it starts running:

```bash
kubectl annotate serviceoperation orders-db-parameters-1 -n my-project services.kube-dc.com/cancel=true
```

- If execution has not started, the operation ends `Cancelled` with reason
  `Cancelled` and the message `withdrawn before execution`.
- A `Running` operation continues and cannot be cancelled.
- If execution has advanced before the operation's displayed phase caught up,
  the cancel request can be refused with a message that starts with
  `cancel refused:`. The operation then continues.
- Operations created by a `ServiceCredentialPolicy` carry the label
  `services.kube-dc.com/managed-by`, and Project identities cannot change them,
  so you cannot cancel them.

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
| `Backup` | None | `backup.enabled` | Backups must also be on for the service (`parameters.backup.enabled`). See [Backups and Restore](postgresql-backup-restore.md#take-a-backup-now) |
| `RestoreInPlace` | `engineUID`, `acknowledgeDataLoss: true`, and exactly one of `backupID` or `targetTime` | `capacity.allocationProtocol: v1`, `backup.enabled`, and the backup's major version: an image of that version in `allowedImages` or, when `allowedImages` is empty, `engineVersion` equal to it | Destructive. See [Restore in place](postgresql-backup-restore.md#restore-in-place) |
| `Scale` | `instances`: integer | `topology.minInstances`, `topology.maxInstances` | See [Scale](#scale) |
| `Resize` | `cpu`, `memory`: quantity strings. Omit one to keep its current value | `capacity.computeBounds`, `capacity.allocationProtocol: v1` | See [Resize](#resize) |
| `ExpandStorage` | `size`: quantity | `capacity.maxStorage` | Refused on storage that cannot expand. See [Expand storage](#expand-storage) |
| `Switchover` | `targetInstance`: optional, a ready replica | None | Two or more instances and a healthy service |
| `Failover` | `targetInstance`: optional, a running replica | None | Two or more instances. Can lose data |
| `RotateCredentials` | `role`; `resyncRootUID` only after an in-place restore | `credentials.allowExistingUsers` for an existing-user role | See [Credentials and Rotation](postgresql-credentials.md) |
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
[Create a PostgreSQL Service](postgresql-create.md#parameter-reference).

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
