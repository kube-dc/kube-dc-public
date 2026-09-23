# Classes and plans

Every `ManagedService` names three catalog entries that the provider defines:
a class, a plan and a connectivity class. They decide what kind of service you
get, what it may do, and how your workloads reach it.

| Catalog entry | Field on `ManagedService` | What it decides |
|---------------|---------------------------|-----------------|
| Class (`ManagedServiceClass`) | `spec.classRef.name` | The service family and its contract: the parameter schema, which parameters can change after creation, the operations the family implements, its credential roles and its endpoint names |
| Plan (`ManagedServicePlan`) | `spec.planRef.name` | The provider's versioned offering for a class: engine version, instance, storage and compute bounds, allowed operations and approvals, backups, and entitlements |
| Connectivity class (`ConnectivityClass`) | `spec.connectivity.classRef.name` | How workloads reach the service |

The class, plan, placement and connectivity of a service cannot change after
creation.

## Names to use

Class and plan names come from the provider catalog.
The following names are examples from the standard catalog; installations that publish the standard shared plans use
`<family>-development` and `<family>-production`, which the console shows as
the Dev and Production tiers.

| Family | Class | Standard plans |
|--------|-------|----------------|
| PostgreSQL | `postgresql` | `postgresql-development`, `postgresql-production` |
| MySQL | `mysql` | `mysql-development`, `mysql-production` |
| MariaDB | `mariadb` | `mariadb-development`, `mariadb-production` |
| ClickHouse | `clickhouse` | `clickhouse-development`, `clickhouse-production` |
| Valkey | `valkey` (single node), `valkey-ha` (Sentinel) | `valkey-development`, `valkey-production` |
| Kafka | `kafka` | `kafka-development`, `kafka-production` |

The standard shared plans use `tenant-native`. It publishes the service
as internal Services inside your Project's network. Workloads in the same
Project connect to them directly. It allocates no public address.

The console's creation sheet lists the published plans of a class under
**Advanced · pick a published plan**, and its **or get the YAML for GitOps**
link shows the names it selected.

:::info You cannot list the catalog
Classes, plans and connectivity classes are cluster-scoped, and no Project role
grants access to them. `kubectl get managedserviceplans` is refused for tenant
identities. Ask your provider for the names to use. Plan names and plan
contents can differ between Kube-DC installations.
:::

## What a plan decides

A plan is revisioned, and its values differ between installations. The following tables list the plan fields that decide what your service may do. Where a
feature depends on a field, your plan must allow it before you can use it. Ask
your provider for the current values of your plan.

### Engine version

| Plan field | Effect |
|------------|--------|
| `engineVersion` | The engine version of the plan (a PostgreSQL major, or a release line such as MySQL `8.4`). If you set `spec.engineVersion`, it must equal this value. Omit it to take the plan's version |
| `allowedImages` | The exact engine images that `MinorUpgrade` and `MajorUpgrade` operations may move to |

### Instances

| Plan field | Effect |
|------------|--------|
| `topology.minInstances`, `topology.maxInstances` | The range allowed for `spec.topology.instances` and for `Scale` operations |
| `topology.defaultInstances` | The instance count when you omit `spec.topology.instances` |
| `topology.minShards`, `topology.maxShards`, `topology.defaultShards`, `topology.minReplicasPerShard`, `topology.maxReplicasPerShard`, `topology.defaultReplicasPerShard` | The shard shape of a sharded family (ClickHouse), used with `spec.topology.shards` and `spec.topology.replicasPerShard` instead of `instances`. A plan declares exactly one of the two shapes |
| `topology.ha` | Declares a high-availability topology. The family defines member, quorum, and failover requirements |
| `maxInstancesPerProject` | How many services on this plan one Project may have. `0` means no limit |

### Storage

| Plan field | Effect |
|------------|--------|
| `capacity.storage` | The data volume size per instance when you omit `spec.storage.size` |
| `capacity.maxStorage` | The largest size allowed at creation and for `ExpandStorage` |
| `capacity.storageClass` | The storage class used when you omit `spec.storage.class` |
| `capacity.allowedStorageClasses` | Other storage classes you may select at creation. Empty means only the default |

### Compute

| Plan field | Effect |
|------------|--------|
| `capacity.cpu`, `capacity.memory` | The CPU and memory per instance when you omit `spec.compute.cpu` or `spec.compute.memory` |
| `capacity.computeBounds` | `minCPU`, `maxCPU`, `minMemory` and `maxMemory`. When absent, the plan is fixed-size and only the default values are accepted |
| `capacity.allocationProtocol` | Must be `v1` for `Resize` and `RestoreInPlace` operations |

### Operations and approval

| Plan field | Effect |
|------------|--------|
| `operations.allowed` | The `ServiceOperation` types you may request. Other types are refused with reason `PlanNotEntitled` |
| `operations.autoApprove` | Allowed types that run without approval. Any other allowed type waits in phase `AwaitingApproval` until the provider approves it |
| `maintenance` | The recurring maintenance window used by operations that set `execution.window: NextPlanWindow`. Without a window, such operations are refused |

You cannot approve your own operations. A `ServiceCredentialPolicy` does not
bypass approval either: its scheduled rotations wait for approval when the plan
does not auto-approve `RotateCredentials`.

### Credentials

| Plan field | Effect |
|------------|--------|
| `credentials.allowExistingUsers` | Allows a `ServiceCredentialPolicy` to manage the password of an existing SQL login that a database administrator created |
| `credentials.allowBreakGlass` | Allows `parameters.breakGlass.enableSuperuserAccess`, which provides emergency `postgres` superuser access |

### Exposure

Internal access through `tenant-native` needs no exposure entitlement.

| Plan field | Effect |
|------------|--------|
| `exposure.gateway` | Allows `parameters.expose.type: gateway`, the PostgreSQL direct-TLS Gateway. See [External access](postgresql-external-access.md) |

Ask your provider which other external access options, if any, your plan
offers.

### Backups

| Plan field | Effect |
|------------|--------|
| `backup.enabled` | Enables the family's backup mechanism. Continuous WAL archiving applies to PostgreSQL, not every family |
| `backup.schedule`, `backup.retentionDays` | The schedule and retention a service inherits when it does not set its own |
| `backup.minRetentionDays`, `backup.maxRetentionDays` | The range allowed for `parameters.backup.retentionDays`. The defaults are 1 and 365 days |
| `backup.minScheduleIntervalMinutes` | The minimum spacing allowed between scheduled backups in `parameters.backup.schedule`. The default is 60 minutes |
| `backup.pitr` | Whether the plan advertises point-in-time recovery within the retention window |
| `backup.objectStoreSource` | Where backups are written: a provider-managed object store (`Plane`), or your Project's own object storage bucket (`ProjectBucketClaim`) |
| `backup.allowedCustomEndpoints` | The object storage endpoints a service may use for its own backup store (`parameters.backup.store`). Empty means custom stores are not allowed |

### Other plan fields

| Plan field | Effect |
|------------|--------|
| `allowedPlacementModes` | The placement modes a service may request |
| `allowedConnectivityClasses` | The connectivity classes a service may request |
| `allowedDeletionPolicies` | The `deletionPolicy` values a service may set |
| `support.tier`, `support.responsibilityText` | The support tier and the provider's statement of responsibilities for the plan |
| `disabled` | A disabled plan accepts no new services |

## How a refusal appears

When your manifest asks for something the plan does not allow, the request is
recorded and then refused in status:

| Resource | Where to look | Typical reasons |
|----------|---------------|-----------------|
| `ManagedService` | `Accepted` condition is `False`; `status.phase` is `Rejected` if the service has not been placed yet | `PlanNotEntitled`, `PlanQuotaExceeded`, `ParameterSchemaRejected` |
| `ServiceOperation` | `status.phase` is `Rejected` | `PlanNotEntitled`, with a message such as `plan <plan> does not allow <operation type>` |
| `ServiceOperation` | `status.phase` is `AwaitingApproval` | `ApprovalRequired`: the plan requires provider approval |

See [Create a PostgreSQL service](postgresql-create.md#common-refusals) for
the full list.

## When the catalog changes

A service keeps the class and plan revisions it was created with. When the
provider changes the catalog, a running service does not move automatically:
its `CatalogPinned` condition turns `False` with reason
`CatalogRevisionChanged`, and it keeps running on its pinned revisions until
the provider moves it to the new ones. Ask your provider if you need a newer
plan revision for an existing service.
