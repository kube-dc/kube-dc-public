# Create a PostgreSQL Service

This page creates a PostgreSQL `ManagedService`, explains every parameter the
PostgreSQL class accepts, and shows how to read the service's status.

## Before you begin

- The `admin` or `developer` role in the Project. See
  [Managed Services](managed-services.md#project-roles).
- The class, plan and connectivity class names. This page uses `postgresql`,
  `postgresql-platform-ha` and `tenant-native`. See
  [Classes and Plans](managed-services-plans.md).
- A plan whose `allowedPlacementModes` includes `ProviderShared` and whose
  `allowedConnectivityClasses` includes `tenant-native`.
- The values of your plan's bounds (instances, storage, compute), which you get
  from your provider.
- Enough Project quota for the instances, storage and backups you request.

Throughout this page, replace `my-project` with your Project's backing
namespace.

```bash
kubectl auth can-i create managedservices.services.kube-dc.com -n my-project
```

## Create the service

Save this manifest as `orders-db.yaml`:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: orders-db
  namespace: my-project
spec:
  classRef:
    name: postgresql
  planRef:
    name: postgresql-platform-ha
  # Set placement explicitly; the API default is TenantCluster.
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  parameters:
    database: orders
    owner: orders
    # Must be within the plan's topology bounds.
    instances: 2
    storage:
      # Per instance; must not exceed the plan's capacity.maxStorage.
      size: 10Gi
    readonlyRole: true
    # Backups need backup.enabled: true on the plan.
    backup:
      enabled: true
    postgresql:
      parameters:
        work_mem: 8MB
    # Optional, only when the plan has capacity.computeBounds:
    # cpu: 500m
    # memory: 1Gi
  deletionPolicy: Retain
  deletionProtection: true
```

Check the manifest with a server-side dry run, then apply it:

```bash
kubectl apply --dry-run=server -f orders-db.yaml
kubectl apply -f orders-db.yaml
kubectl get managedservice orders-db -n my-project -w
```

The dry run checks the API schema and admission policies only. The class
schema, plan bounds and capacity are checked after the object is created, and
the result appears in status. Wait until `PHASE` is `Ready` and `READY` is
`True`, then confirm the result is current as described in
[Read the status](#read-the-status).

Record the service UID. Bindings, operations and credential policies need it:

```bash
kubectl get managedservice orders-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
```

## Service fields

| Field | Required | Description |
|-------|----------|-------------|
| `spec.classRef.name` | Yes | The class, `postgresql`. Cannot change |
| `spec.planRef.name` | Yes | The plan, for example `postgresql-platform-ha`. Cannot change |
| `spec.placement.mode` | Yes | Set `ProviderShared`, which runs the service inside your Project. Your plan's `allowedPlacementModes` must include it. If you omit the field, the API default `TenantCluster` is used. Cannot change |
| `spec.connectivity.classRef.name` | Yes | The connectivity class, `tenant-native`. Your plan's `allowedConnectivityClasses` must include it. Cannot change |
| `spec.parameters` | No | Class parameters. See [Parameter reference](#parameter-reference) |
| `spec.deletionPolicy` | No | What deleting the `ManagedService` does to the engine and its data. Default `Retain`. Must be one of the plan's `allowedDeletionPolicies` |
| `spec.deletionProtection` | No | While `true`, deleting the `ManagedService` is refused. Set it to `false` in a separate update before you delete |
| `spec.restoreFrom` | No | Creates the service from a backup of another service. Not covered on this page. Cannot change |

The deletion policies behave as follows:

| `deletionPolicy` | Effect of deleting the `ManagedService` |
|------------------|------------------------------------------|
| `Retain` | Management stops. The engine, its volumes and its data stay in the Project and can keep using capacity and quota |
| `SnapshotAndDelete` | The platform takes a final backup and then removes the engine and its volumes. This needs working backups. If the engine no longer exists, so that no final backup can be taken, the data volumes are retained instead of deleted. Handling of a final backup that fails is not yet qualified; ask your provider |
| `Delete` | The engine and its volumes are removed and the data is lost. The service must also carry the annotation `services.kube-dc.com/confirm-delete` set to the service name, or deletion waits with reason `DeletionConfirmationNeeded` |

For a service in the Project's own namespace, none of these settings apply
when the whole Project is deleted. See
[Deleting services and Projects](managed-services.md#deleting-services-and-projects)
and [Status and Deletion](managed-services-status-deletion.md).

## Parameter reference

These are the parameters of the `postgresql` class. Unknown parameters are
refused. The "Changes after creation" column gives each parameter's mutation
class; see [Change a service after creation](#change-a-service-after-creation).

| Parameter | Type and format | Default | Changes after creation |
|-----------|-----------------|---------|------------------------|
| `version` | String: `"14"` to `"18"` | The plan's `engineVersion` | `CreateOnly` |
| `database` | String matching `^[a-z_][a-z0-9_]*$`, up to 63 characters | `app` | `CreateOnly` |
| `owner` | String matching `^[a-z_][a-z0-9_]*$`, up to 63 characters | `app` | `CreateOnly` |
| `readonlyRole` | Boolean | `true` | `CreateOnly` |
| `instances` | Integer, 1 to 9, within the plan's topology bounds | The plan's `topology.defaultInstances` | `OperationOnly` (`Scale`) |
| `cpu` | Quantity string, whole milliCPU, for example `500m` | The plan's `capacity.cpu` | `OperationOnly` (`Resize`) |
| `memory` | Quantity string, whole bytes, for example `1Gi` | The plan's `capacity.memory` | `OperationOnly` (`Resize`) |
| `storage.size` | Quantity string, for example `10Gi` | The plan's `capacity.storage` | `OperationOnly` (`ExpandStorage`) |
| `storage.class` | Storage class name | The plan's `capacity.storageClass` | `CreateOnly` |
| `postgresql.parameters` | Map of allow-listed settings; values are strings | Managed defaults | `OnlineDesired`, or the `UpdateParameters` operation |
| `backup.enabled` | Boolean | `true` | `OnlineDesired` |
| `backup.schedule` | Five-field cron expression in UTC | The plan's `backup.schedule` | `OnlineDesired` |
| `backup.retentionDays` | Integer | The plan's `backup.retentionDays` | `OnlineDesired` |
| `backup.store` | Object: `endpoint`, `bucket`, `secretName`, `secretUID` | None | `CreateOnly` |
| `pooler.enabled` | Boolean | `false` | `OnlineDesired` |
| `pooler.instances` | Integer, 1 to 3 | `1` | `OnlineDesired` |
| `pooler.mode` | `session` or `transaction` | `transaction` | `OnlineDesired` |
| `breakGlass.enableSuperuserAccess` | Boolean | `false` | `OnlineDesired` |
| `expose.type` | String | `internal` | `OnlineDesired` |

Details:

- **`version`**: the PostgreSQL major version. It must equal the plan's
  `engineVersion`, so the simplest choice is to omit it. A new service starts
  on the newest image the plan allows for that major version.
- **`database`** and **`owner`**: the application database created at bootstrap
  and the login that owns it. The owner's credential is the `owner` binding
  role.
- **`readonlyRole`**: creates a `readonly` login with the `pg_read_all_data`
  privilege, delivered through the `readonly` binding role.
- **`instances`**: one primary plus replicas. With two or more instances on a
  plan with `topology.ha: true`, the platform fails over automatically.
- **`cpu`** and **`memory`**: requests and limits per instance. You can choose
  values only when the plan has `capacity.computeBounds`; otherwise only the
  plan defaults are accepted.
- **`storage.size`**: requested per instance, at most the plan's
  `capacity.maxStorage`. On a local-disk storage class the requested size is
  not a hard filesystem limit.
- **`storage.class`**: the plan default or one of its
  `capacity.allowedStorageClasses`.
  Existing volumes never move to another class.
- **`postgresql.parameters`**: settings that reload online are `work_mem`,
  `maintenance_work_mem`, `effective_cache_size`,
  `log_min_duration_statement`, `timezone`, `statement_timeout`,
  `idle_in_transaction_session_timeout` and `archive_timeout`. Settings that
  cause a rolling restart of the instances are `shared_buffers`,
  `max_connections`, `max_worker_processes`, `max_wal_senders`,
  `max_replication_slots`, `huge_pages`, `max_prepared_transactions` and
  `max_locks_per_transaction`. Give every value as a string with exact units.
  `timezone` accepts IANA names and `UTC`.
- **`backup.*`**: backups require the plan's `backup.enabled: true`. The schedule and
  retention must be within the plan's backup bounds. Omit a field to inherit
  the plan value. Lowering `retentionDays` can permanently remove old recovery
  points.
- **`backup.store`**: a backup destination of your own. It requires the plan's
  `backup.allowedCustomEndpoints` and an existing `Opaque` Secret in the
  Project with `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Keep that
  Secret for as long as you may need to recover. The destination cannot change
  later.
- **`pooler.*`**: runs PgBouncer in front of the primary and publishes a
  `pooled` endpoint. Each pooler replica adds 100m CPU and 128Mi memory to the
  capacity the service uses. `transaction` mode multiplexes more clients. Use `session`
  mode for anything that relies on session state, such as prepared statements,
  advisory locks, `LISTEN`/`NOTIFY` or temporary tables. Turning the pooler
  off is refused while a binding uses the `pooled` endpoint.
- **`breakGlass.enableSuperuserAccess`**: emergency `postgres` superuser
  access. It requires the plan's `credentials.allowBreakGlass` and is off by
  default. It is not covered on this page.
- **`expose.type`**: keep the default `internal` unless you need Gateway
  access. The value `gateway` requires the plan field `exposure.gateway`; see
  [External Access](postgresql-external-access.md). Other values require plan
  entitlements and are not covered in this chapter.

## Change a service after creation

### `OnlineDesired` parameters

Edit the manifest and apply the complete `ManagedService` again, keeping every
other field unchanged:

```bash
kubectl apply -f orders-db.yaml
```

Settings that need a restart roll through the instances one at a time.
Applications should reconnect and retry.

For PostgreSQL settings, a completed `UpdateParameters` operation takes
precedence over the manifest. After such an operation, editing or removing
that key in `spec.parameters.postgresql.parameters` does not change its
effective value; use another `UpdateParameters` operation. Its
`resetParameters` list resets the named settings to their managed defaults. A
setting cannot be set and reset in the same operation.

### `CreateOnly` and `OperationOnly` parameters

An edit to one of these parameters on the `ManagedService` is refused when you
apply it:

```text
parameters instances are CreateOnly, OperationOnly or ReplaceOrMigrate for class postgresql and cannot be edited on the ManagedService; OperationOnly values change through a ServiceOperation
```

If an edit gets past admission, the service reports `Reconciled=False` with
reason `ParameterMutationRejected`, and the last accepted configuration stays
in force. Revert the edit.

Change `OperationOnly` values with a `ServiceOperation`:

| Parameter | Operation `type` | Operation `parameters` | Plan requirement |
|-----------|------------------|------------------------|------------------|
| `instances` | `Scale` | `instances` | Within `topology` bounds |
| `cpu`, `memory` | `Resize` | `cpu`, `memory` (either one may be omitted to keep its current value) | `capacity.computeBounds` and `capacity.allocationProtocol: v1` |
| `storage.size` | `ExpandStorage` | `size`, larger than the current size | At most `capacity.maxStorage`, and the service's `status.engineDetails.storageExpansion` must be `"true"`. The operation is refused on local-path storage. Successful expansion is not yet qualified; ask your provider before you rely on it |
| `postgresql.parameters` | `UpdateParameters` | `parameters`, `resetParameters` | None beyond the allow-list |

Every operation type must also be in the plan's `operations.allowed`. This
example scales `orders-db` to three instances:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-scale-3
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Scale
  idempotencyKey: orders-db-scale-3
  parameters:
    instances: 3
  execution:
    window: Immediate
```

```bash
kubectl apply -f orders-db-scale-3.yaml
kubectl get serviceoperation orders-db-scale-3 -n my-project -w
```

An operation moves through `Pending`, `Preflight`, `AwaitingApproval`,
`Accepted` and `Running` to one of `Succeeded`, `Failed`, `Rejected` or
`Cancelled`. Only `Succeeded` means the change was made. Read `status.reason`,
`status.message` and `status.result` for details.

- The operation spec is immutable. To retry a submission whose result you did
  not see, apply the same manifest again with the same name and
  `idempotencyKey`. For a new, deliberate change, use a new name and a new key.
- `execution.window: NextPlanWindow` waits for the plan's maintenance window.
  `Immediate` does not skip approval.
- To withdraw an operation that has not started running, set the annotation
  `services.kube-dc.com/cancel: "true"` on it.
- An operation record can be deleted only after it reaches a final phase.
  Deleting a record does not stop work that has already started.

After an operation succeeds, the new value appears in
`status.effectiveConfiguration`, not in `spec.parameters`. Do not copy it back
into the manifest.

## Read the status

`kubectl get managedservice` shows the phase and the `Ready` condition:

```bash
kubectl get managedservice orders-db -n my-project
```

### Phases

| `status.phase` | Meaning |
|----------------|---------|
| `Pending` | The service is not placed yet. The `Placed` condition gives the reason |
| `Rejected` | The service was refused before it was placed. The `Accepted` condition gives the reason |
| `Placing` | Placement is in progress |
| `Provisioning` | The service is placed, but not every required condition is `True` yet. The `Ready` condition's message lists what it is waiting for |
| `Ready` | `Accepted`, `Placed`, `Reconciled` and `ConnectivityReady` are `True`; `BackupReady` is also `True` when the plan and the service enable backups; and the service is not frozen |
| `Degraded` | The service is not ready and the engine reports itself degraded. Read the conditions |
| `Failed` | The engine reports a failure, or the platform cannot continue with the service's placement or restore. Read the conditions |
| `Frozen` | The `Frozen` condition is `True` and new operations are refused. Ask your provider |
| `Deleting` | The `ManagedService` is being deleted. See [Status during deletion](managed-services-status-deletion.md#status-during-deletion) |
| `Retained` | Read the conditions |
| `Hibernated` | A `Hibernate` operation stopped compute; the data volumes are kept. `Ready` is `False` with reason `Hibernated`. Create a `Resume` operation to start it again |

### Conditions

| Condition | `True` means |
|-----------|--------------|
| `Accepted` | The class, plan, parameter schema and entitlements accept the current spec |
| `Placed` | The service has a committed placement |
| `Reconciled` | The accepted configuration is applied and healthy |
| `EngineReady` | The engine itself is healthy, independent of pending changes |
| `ConnectivityReady` | The service's endpoints were probed successfully |
| `BackupReady` | Whether the service's backup checks pass. Read its reason and message; confirm a requested backup through the outcome of its `Backup` operation |
| `Ready` | Every condition the plan requires is true |
| `Stale` | The last observation is older than the staleness threshold, so the status shown is last-known |
| `Frozen` | Destructive changes are blocked and new operations are refused |
| `CatalogPinned` | The service's pinned catalog revisions match the current catalog |

Print them with their generations and reasons:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='generation={.metadata.generation}{"\n"}{range .status.conditions[*]}{.type}={.status} observedGeneration={.observedGeneration} reason={.reason} {.message}{"\n"}{end}'
```

### Is the result current?

A `Ready=True` condition from an earlier spec does not mean your latest change
was applied. After every change, check all of the following:

1. The `Accepted` and `Ready` conditions are `True`, and their
   `observedGeneration` equals `metadata.generation`.
2. `status.acceptedRevision` equals `status.appliedRevision`, and
   `status.acceptedGeneration` equals `status.appliedGeneration`.
3. `Stale` is not `True`.
4. `status.lastObservedAt` is recent. If it is more than four minutes old, treat
   the whole status as last-known, even when `Stale` is not `True`, and ask your
   provider if it does not advance.

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='accepted={.status.acceptedRevision}/{.status.acceptedGeneration} applied={.status.appliedRevision}/{.status.appliedGeneration} observed={.status.lastObservedAt}{"\n"}'
```

An invalid edit can leave the phase and the applied revision unchanged while
`Accepted=False` reports why the new spec was refused.

### Other status fields

| Field | Contents |
|-------|----------|
| `status.effectiveConfiguration` | The configuration in force, including values set by operations |
| `status.engineDetails` | Engine observations, such as `engineUID`, `currentPrimary`, `readyInstances`, `storageClass`, `storageExpansion` and `hibernated` |
| `status.endpoints` | Named endpoints with addresses, TLS details and probe results. See [Connect Applications](postgresql-connect.md) |
| `status.credentials` | Credential roles with their Secret name, version and fingerprint. Never the values |
| `status.lastBackup`, `status.recentBackups` | The newest verified backup and a short recent window. This is not the full backup history |
| `status.operationSummary` | The active, last successful and last failed operation |

Events for the service are recorded in the Project:

```bash
kubectl get events -n my-project --field-selector involvedObject.name=orders-db
```

## Common refusals

| Where it shows | Reason or message | Meaning and fix |
|----------------|-------------------|-----------------|
| `kubectl apply` error | `parameters ... are CreateOnly, OperationOnly or ReplaceOrMigrate ...` | You edited a parameter that cannot change on the `ManagedService`. Revert it, or use the matching operation |
| `kubectl apply` error | `classRef is immutable`, `planRef is immutable ...`, `placement is immutable ...`, `connectivity is immutable after creation ...`, `restoreFrom is create-only` | These fields are fixed at creation. Create a new service instead |
| `kubectl delete` error | `deletionProtection is enabled on this ManagedService; set spec.deletionProtection=false first` | Set `deletionProtection: false`, apply, then delete |
| `Forbidden` from the API | RBAC | Your Project role cannot write this resource. See [Project roles](managed-services.md#project-roles) |
| `Accepted=False`, phase `Rejected` | `PlanNotEntitled` | The plan was not found, is disabled or not verified, or does not allow the requested placement mode, connectivity class or deletion policy. For example, if `placement.mode` is omitted and the plan's `allowedPlacementModes` does not include `TenantCluster`, the message is `plan <plan> does not allow placement mode TenantCluster` |
| `Accepted=False`, phase `Rejected` | `ClassNotVerified` | The class name is wrong or the class is unavailable |
| `Accepted=False`, phase `Rejected` | `ConnectivityClassAbsent` | The connectivity class name does not exist |
| `Accepted=False`, phase `Rejected` | `ParameterSchemaRejected` | A parameter is invalid for the class or outside the plan's bounds. Examples: `spec.parameters.version: must equal the plan engine version "..."`, `spec.parameters.instances: must be between <min> and <max> for plan <plan>`, `spec.parameters.storage.size: must not exceed the plan maximum ...`, or an unknown parameter reported as `additional properties ...` |
| `Accepted=False`, phase `Rejected` | `PlanQuotaExceeded` | The Project already has the plan's `maxInstancesPerProject` services: `plan <plan> allows <n> instances per project` |
| `Placed=False`, phase `Pending` | `NoQualifiedDataPlane` | The platform has nowhere to place a service of this plan right now. Ask your provider |
| `Placed=False` | `ReservationRejected` | The platform could not secure capacity for the service. Ask your provider |
| `Reconciled=False` | `ParameterMutationRejected` | A `CreateOnly` or `OperationOnly` parameter differs from the accepted configuration. Revert it |
| Operation phase `Rejected` | `PlanNotEntitled` | The plan does not allow this operation type, or the operation asked for something outside the plan, such as an image not in `allowedImages` |
| Operation phase `Rejected` | `ServiceIdentityChanged` | `serviceUID` does not match the current service of that name |
| Operation phase `Rejected` | `OperationConflict` | Another operation already used the same `idempotencyKey` |
| Operation phase `AwaitingApproval` | `ApprovalRequired` | The plan requires provider approval for this operation type. Ask your provider |
| Operation phase `Pending` | `OperationConflict`, `AwaitingMaintenanceWindow` | Waiting for another active operation that conflicts with this operation, or for the maintenance window |

The phases in this table apply to a service that has not been placed yet. When
a later edit to a placed service is refused, the service keeps its current
phase and `Accepted=False` reports the refusal.

A refused service stays visible with its reason. Some refusals, such as
`PlanQuotaExceeded`, clear without an edit once their cause is gone. When the
fix needs a different value for a `CreateOnly` or `OperationOnly` parameter or
for a catalog field, which cannot be edited after the object exists, delete the
refused service and create a corrected one. Set `deletionProtection: false`
first if you enabled it.
