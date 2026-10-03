# Create a PostgreSQL service

As a Project administrator or developer, create a PostgreSQL service from a published plan with a Kubernetes manifest.
Use the parameter reference to configure its database, replicas, backups, and connection pool. The console's creation sheet submits the same manifest; see
[Use the console](managed-services-console.md).

## Before you begin

- The `admin` or `developer` role in the Project. See
  [Managed services](managed-services.md#project-roles).
- The class, plan and connectivity class names. This page uses `postgresql`,
  `postgresql-production` and `tenant-native`; plan names differ between
  installations. See [Classes and plans](managed-services-plans.md).
- A plan whose `allowedPlacementModes` includes `ProviderShared` and whose
  `allowedConnectivityClasses` includes `tenant-native`.
- The values of your plan's bounds (instances, storage, compute), which you get
  from your provider or from the sliders of the console's Size step.
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
    name: postgresql-production
  # Set placement explicitly; the API default is TenantCluster, which the
  # published plans do not allow.
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  # Sizing. Each value must be within the plan's bounds; omit a field to take
  # the plan default. These change later only through operations.
  topology:
    instances: 3
  compute:
    cpu: "1"
    memory: 2Gi
  storage:
    size: 20Gi
  # Engine parameters of the postgresql class.
  parameters:
    database: orders
    owner: orders
    readonlyRole: true
    # Backups need backup.enabled: true on the plan.
    backup:
      enabled: true
    postgresql:
      parameters:
        work_mem: 8MB
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
| `spec.planRef.name` | Yes | The plan, for example `postgresql-production`. Cannot change |
| `spec.placement.mode` | Yes | Set `ProviderShared`, which runs the service inside your Project. Your plan's `allowedPlacementModes` must include it. If you omit the field, the API default `TenantCluster` is used and the published plans refuse it. Cannot change |
| `spec.connectivity.classRef.name` | Yes | The connectivity class, `tenant-native`. Your plan's `allowedConnectivityClasses` must include it. Cannot change |
| `spec.engineVersion` | No | The PostgreSQL major version, for example `"17"`. It must equal the plan's `engineVersion`, so the simplest choice is to omit it. Changes only through a `MinorUpgrade` or `MajorUpgrade` operation |
| `spec.topology.instances` | No | One primary plus replicas, within the plan's `topology` bounds. Default: the plan's `topology.defaultInstances`. Changes only through a `Scale` operation |
| `spec.compute.cpu`, `spec.compute.memory` | No | Requests and limits per instance, as quantity strings (`500m`, `1Gi`). Only within the plan's `capacity.computeBounds`; a plan without bounds accepts only its defaults. Change through a `Resize` operation |
| `spec.storage.size` | No | The data volume per instance, at most the plan's `capacity.maxStorage`. Default: the plan's `capacity.storage`. Grows through an `ExpandStorage` operation; never shrinks |
| `spec.storage.class` | No | The plan default or one of its `capacity.allowedStorageClasses`. Cannot change; existing volumes never move to another class |
| `spec.parameters` | No | Engine parameters of the class. See [Parameter reference](#parameter-reference) |
| `spec.maintenance.applyImmediately` | No | Apply declarative changes now instead of waiting for the plan's maintenance window. It never bypasses approval |
| `spec.retry` | No | Request one new attempt for a failed declarative change, naming its attempt ID from status |
| `spec.deletionPolicy` | No | What deleting the `ManagedService` does to the engine and its data. Default `Retain`. Must be one of the plan's `allowedDeletionPolicies` |
| `spec.deletionProtection` | No | While `true`, deleting the `ManagedService` is refused. Set it to `false` in a separate update before you delete |
| `spec.restoreFrom` | Platform-generated | The platform sets this field for a catalog-backed `RestoreToNew` operation. See [Backups and restore](postgresql-backup-restore.md#restore-into-a-new-service) |

The sizing fields used to live under `spec.parameters`. Manifests that still
carry `parameters.cpu`, `parameters.memory`, `parameters.instances`,
`parameters.storage` or `parameters.version` are refused at admission with a
message naming the field to use instead.

The deletion policies behave as follows:

| `deletionPolicy` | Effect of deleting the `ManagedService` |
|------------------|------------------------------------------|
| `Retain` | Management stops. The engine, its volumes and its data stay in the Project and can keep using capacity and quota |
| `SnapshotAndDelete` | The platform takes a final backup and then removes the engine and its volumes. This needs working backups. If the engine no longer exists, so that no final backup can be taken, the data volumes are retained instead of deleted |
| `Delete` | The engine and its volumes are removed and the data is lost. The service must also carry the annotation `services.kube-dc.com/confirm-delete` set to the service name, or deletion waits with reason `DeletionConfirmationNeeded` |

For a service in the Project's own namespace, none of these settings apply
when the whole Project is deleted. See
[Delete a service or a Project](managed-services.md#delete-a-service-or-a-project)
and [Status and deletion](postgresql-deletion.md).

## Parameter reference

These are the engine parameters of the `postgresql` class, under
`spec.parameters`. Unknown parameters are refused. The last column gives each
parameter's mutation class; see
[Change a service after creation](#change-a-service-after-creation).

| Parameter | Type and format | Default | Changes after creation |
|-----------|-----------------|---------|------------------------|
| `database` | String matching `^[a-z_][a-z0-9_]*$`, up to 63 characters | `app` | `CreateOnly` |
| `owner` | String matching `^[a-z_][a-z0-9_]*$`, up to 63 characters | `app` | `CreateOnly` |
| `readonlyRole` | Boolean | `true` | `CreateOnly` |
| `postgresql.parameters` | Map of allow-listed settings; values are strings | Managed defaults | `OnlineDesired`, or the `UpdateParameters` operation |
| `backup.enabled` | Boolean | `true` | `OnlineDesired` |
| `backup.schedule` | Five-field cron expression in UTC | The plan's `backup.schedule` | `OnlineDesired` |
| `backup.retentionDays` | Integer | The plan's `backup.retentionDays` | `OnlineDesired` |
| `backup.store` | Object: `endpoint`, `bucket`, `secretName`, `secretUID` | None | `CreateOnly` |
| `pooler.enabled` | Boolean | `false` | `OnlineDesired` |
| `pooler.instances` | Integer, 1 to 3 | `1` | `OnlineDesired` |
| `pooler.mode` | `session` or `transaction` | `transaction` | `OnlineDesired` |
| `pooler.readOnly` | Boolean: also pool the replicas as the `pooled-read-only` endpoint | `false` | `OnlineDesired` |
| `breakGlass.enableSuperuserAccess` | Boolean | `false` | `OnlineDesired` |
| `expose.type` | `internal`, `loadbalancer` or `gateway` | `internal` | `OnlineDesired` |

Details:

- **`database`** and **`owner`**: the application database created at bootstrap
  and the login that owns it. The owner's credential is the `owner` binding
  role.
- **`readonlyRole`**: creates a `readonly` login with the `pg_read_all_data`
  privilege, delivered through the `readonly` binding role.
- **`postgresql.parameters`**: settings that reload online are `work_mem`,
  `maintenance_work_mem`, `effective_cache_size`,
  `log_min_duration_statement`, `timezone`, `statement_timeout`,
  `idle_in_transaction_session_timeout` and `archive_timeout`. Settings that
  cause a rolling restart of the instances are `shared_buffers`,
  `max_connections`, `max_worker_processes`, `max_wal_senders`,
  `max_replication_slots`, `huge_pages`, `max_prepared_transactions` and
  `max_locks_per_transaction`. Give every value as a string with exact units.
  `timezone` accepts IANA names and `UTC`.
- **`backup.*`**: backups require the plan's `backup.enabled: true`. The
  schedule and retention must be within the plan's backup bounds. Omit a field
  to inherit the plan value. Lowering `retentionDays` can permanently remove
  old recovery points.
- **`backup.store`**: a backup destination of your own. It requires the plan's
  `backup.allowedCustomEndpoints` and an existing `Opaque` Secret in the
  Project with `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Keep that
  Secret for as long as you may need to recover. The destination cannot change
  later.
- **`pooler.*`**: runs PgBouncer in front of the primary and publishes a
  `pooled` endpoint. Each pooler replica adds 100m CPU and 128Mi memory to the
  capacity the service uses. `transaction` mode multiplexes more clients. Use
  `session` mode for anything that relies on session state, such as prepared
  statements, advisory locks, `LISTEN`/`NOTIFY` or temporary tables. Turning
  the pooler off is refused while a binding uses the `pooled` endpoint.
- **`breakGlass.enableSuperuserAccess`**: emergency `postgres` superuser
  access. It requires the plan's `credentials.allowBreakGlass` and is off by
  default.
- **`expose.type`**: keep the default `internal` unless you need access from
  outside the Project. `gateway` requires the plan field `exposure.gateway`;
  see [External access](postgresql-external-access.md). `loadbalancer` requires
  the plan's `exposure.allowPublicLoadBalancer` and a public IPv4 address from
  your organization's quota.

## Change a service after creation

### `OnlineDesired` parameters

Edit the manifest and apply the complete `ManagedService` again, keeping every
other field unchanged, or change the value in the console's **Settings** tab
and use **Review & apply**:

```bash
kubectl apply -f orders-db.yaml
```

Settings that need a restart roll through the instances one at a time.
Applications should reconnect and retry. Declarative changes wait for the
plan's maintenance window unless the plan applies them immediately or you set
`spec.maintenance.applyImmediately: true`.

For PostgreSQL settings, a completed `UpdateParameters` operation takes
precedence over the manifest. After such an operation, editing or removing
that key in `spec.parameters.postgresql.parameters` does not change its
effective value; use another `UpdateParameters` operation. Its
`resetParameters` list resets the named settings to their managed defaults. A
setting cannot be set and reset in the same operation.

### Sizing, version and `CreateOnly` parameters

An edit to one of these fields on the `ManagedService` is refused when you
apply it:

```text
parameters instances are CreateOnly, OperationOnly or ReplaceOrMigrate for class postgresql and cannot be edited on the ManagedService; OperationOnly values change through a ServiceOperation
```

If an edit gets past admission, the service reports `Reconciled=False` with
reason `ParameterMutationRejected`, and the last accepted configuration stays
in force. Revert the edit.

Change them with a `ServiceOperation`, or with the matching action in the
console:

| Field | Operation `type` | Operation `parameters` | Plan requirement |
|-------|------------------|------------------------|------------------|
| `spec.topology.instances` | `Scale` | `instances` | Within `topology` bounds |
| `spec.compute.cpu`, `spec.compute.memory` | `Resize` | `cpu`, `memory` (either one may be omitted to keep its current value) | `capacity.computeBounds` and `capacity.allocationProtocol: v1` |
| `spec.storage.size` | `ExpandStorage` | `size`, larger than the current size | At most `capacity.maxStorage`, and the service's `status.engineDetails.storageExpansion` must be `"true"`. Refused on storage that cannot expand |
| `spec.engineVersion` | `MinorUpgrade`, `MajorUpgrade` | `imageName` from the plan's `allowedImages` | See [Upgrades](postgresql-operations.md#upgrades) |
| `spec.parameters.postgresql.parameters` | `UpdateParameters` | `parameters`, `resetParameters` | None beyond the allow-list |

Every operation type must also be in the plan's `operations.allowed`. This
example scales `orders-db` to five instances:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-scale-5
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: Scale
  idempotencyKey: orders-db-scale-5
  parameters:
    instances: 5
  execution:
    window: Immediate
```

```bash
kubectl apply -f orders-db-scale-5.yaml
kubectl get serviceoperation orders-db-scale-5 -n my-project -w
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
`status.effectiveConfiguration` and `status.resolvedSpec`, not in the manifest
you applied. Do not copy it back: the manifest keeps stating what you created,
and the operations record what changed.

## Read the status

<span id="phases" />
<span id="conditions" />
<span id="is-the-result-current" />
<span id="other-status-fields" />

Confirm that the service reports `Ready=True` for its current generation before connecting applications.
See [Check service status](managed-services-status.md) for phases, conditions, effective configuration, and observation freshness.
After a change, use [Is the result current?](managed-services-status.md#is-the-result-current) to verify that it was applied.

## Common refusals

See [Common refusals](managed-services-status.md#common-refusals) for admission errors, plan restrictions, capacity failures, and operation conflicts.
After the service is ready, [connect your application](postgresql-connect.md).
