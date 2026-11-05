# Publish the catalog

As a platform operator, publish the classes, plans, and connectivity options that tenants can select.
A family can have multiple classes, such as single-node and replicated variants.
Each class can have multiple plans. It lives in the fleet repository under
`platform/kube-dc-services-catalog/` and reaches a cluster through the Flux
Kustomization `services-catalog`, which the cluster opts into with
`clusters/<name>/services-catalog.yaml`. Nothing in the catalog is edited by
hand on a cluster.

## Layout

| Path | Content |
|------|---------|
| `tenant-native.yaml`, `postgresql.yaml` | The base: the connectivity class and the PostgreSQL bundle, class and plans |
| `components/<family>/` | One component per additional family: `mysql`, `mariadb`, `clickhouse`, `valkey`, `kafka`, `rabbitmq`, `opensearch` and `forgejo`. Each carries the `ServiceFamilyBundle`, the `ManagedServiceClass` and the family's plans |
| `components/shared-service-plans/` | The Development and Production plans for PostgreSQL, Kafka and Valkey, with the sizing table in its README. The MySQL, MariaDB, ClickHouse, RabbitMQ and OpenSearch components define their own Development and Production pairs |
| `components/rabbitmq-durable-storage/`, `components/opensearch-durable-storage/` | Move the RabbitMQ Production plan and both OpenSearch plans from the `database-ha` role to the `database` role with volume expansion |
| `components/local-ha-storage/` | Moves the Production plans of PostgreSQL, MySQL, MariaDB, ClickHouse, Valkey and Kafka to the `database-ha` role on node-local storage. See [Local HA storage](managed-services-local-ha.md) |
| `components/data-plane/` | Registers the cluster's own data plane |
| `components/postgresql-gateway/`, `components/postgresql-public-loadbalancer/` | Exposure entitlements, where the cluster has a shared TLS listener or a public address pool |
| `components/console*/` | Console publication of plans on clusters that predate the annotation being the default |
| `revisions.lock` | One row per cluster, kind and name with the revision and the digest of the rendered content |

A cluster selects the components it offers:

```yaml
  components:
    - components/data-plane
    - components/mysql
    - components/mariadb
    - components/clickhouse
    - components/valkey
    - components/kafka
    - components/shared-service-plans
```

Keep `prune: false` on the Kustomization. Removing a file must never delete a
class or plan that a running instance still references.

## What a plan says

A plan is the product decision for one class. The fields an operator sets:

| Field | Decides |
|-------|---------|
| `classRef`, `revision` | The class, and the plan's own revision string |
| `engineVersion`, `allowedImages` | The engine line the plan runs, and the exact images upgrades may move to |
| `capacity.cpu`, `capacity.memory`, `capacity.storage` | The per-member defaults |
| `capacity.computeBounds`, `capacity.maxStorage` | What tenants may choose at creation and through `Resize` and `ExpandStorage`; a plan without bounds is fixed-size |
| `capacity.storageRole` | The logical storage role, `database` or `database-ha`, that the data plane maps to a class. Mutually exclusive with `capacity.storageClass` and `capacity.allowedStorageClasses`. Tenants omit `spec.storage.class` on a role-based plan; a set value is refused |
| `capacity.storageClass`, `capacity.allowedStorageClasses` | On a plan without a storage role: the default class and the other classes tenants may pick at creation |
| `capacity.storageAllocationMode` | `Volume` (the default) resizes the PVC on `ExpandStorage`. `UnrestrictedLocal` records a larger allocation on node-local storage and does not resize the PVC |
| `capacity.allocationProtocol: v1` | Durable capacity accounting; required for `Resize` and `RestoreInPlace` |
| `topology` | Either `min/max/defaultInstances`, or `min/max/defaultShards` with `min/max/defaultReplicasPerShard` for sharded families; plus `ha`, `synchronousStandbys`, `durability` |
| `operations.allowed`, `operations.autoApprove` | Which operation types tenants may request and which run without an operator's approval |
| `maintenance` | The recurring window (`day`, `startHour`, `durationMinutes`, UTC) for `NextPlanWindow` operations and deferred declarative changes |
| `backup` | `enabled`, `schedule`, `retentionDays`, the retention and interval bounds, `pitr`, and the object store. See [Storage roles and backups](#storage-roles-and-backups) |
| `exposure` | `allowPublicLoadBalancer`, and the shared `gateway` listener for PostgreSQL direct TLS |
| `credentials` | `allowExistingUsers`, `allowBreakGlass`, and the OpenBao delivery switches |
| `allowedPlacementModes`, `allowedConnectivityClasses`, `allowedDeletionPolicies`, `minIsolation` | The placement contract; every published shared plan allows only `ProviderShared`, `tenant-native` and `NamespaceSharedNodes` |
| `maxInstancesPerProject` | A per-Project quota; `0` means unlimited. The shared plans use 5, and 3 for the HA MariaDB and ClickHouse plans |
| `support.tier`, `support.responsibilityText` | What the console shows as the plan's responsibilities and limits |
| `meters` | The meters billed for the plan |
| `disabled` | Accept no new services; existing ones keep running |

Defaults are starting sizes, not performance guarantees. All shared plans
expose `computeBounds` and `maxStorage`; which of those a tenant can change
later depends on the family (MySQL and ClickHouse are sized once).

### Console publication

A plan appears in the tenant console when it carries the annotation
`services.kube-dc.com/console: enabled`. This is publication, not
entitlement: an unannotated plan still accepts manifests that name it. Hide a
plan you have not qualified on your installation with `disabled`, and retire
an old plan by setting the annotation to `disabled`, never by deleting the
object while an instance references it.

The console groups a class's published plans into tiers by name and shape:
`*-development` as Dev, `*-production` as Production, and a plan whose
topology is highly available with more than one member as HA.

## Storage roles and backups

A plan asks for a storage **role**, not a class name, because class names
differ between installations. The standard catalog uses two roles:

| Role | Used by | Typical class |
|------|---------|---------------|
| `database` | Every standard plan that the `database-ha` row does not name | An expandable network block class, such as a Ceph RBD class |
| `database-ha` | Both OpenSearch plans, the RabbitMQ Production plan, and the six Production plans that `components/local-ha-storage` patches | A node-local class, such as `local-path`, for families that replicate their own data across hosts |

The Forgejo plan sets `capacity.storageClass` instead of a role.

The data plane maps each role to a physical class in
`ServiceDataPlane.spec.storageRoles`. The fleet writes the mapping and the
per-class budgets from `cluster-config.env`:

```text
SERVICES_STORAGE_ROLES={database: rbd-vm, database-ha: local-path}
SERVICES_STORAGE_BUDGETS={rbd-vm: 500Gi, local-path: 256Gi}
```

Map every role that a selected plan uses. A data plane without a mapping for
the plan's role is not a placement candidate. The service stays `Pending`
with `Placed=False`, reason `NoQualifiedDataPlane`, and a message that names
the role: `storage role "database-ha" has no mapping on data plane <plane>`.
To offer OpenSearch and RabbitMQ Production without node-local storage, select
`components/opensearch-durable-storage` and
`components/rabbitmq-durable-storage`. They move those plans to `database`.

The `storage.kube-dc.com/roles` annotation on a `StorageClass`, described in
[Storage classes](storage-classes.md), does not affect managed services. Only
`spec.storageRoles` on the data plane does.

`ExpandStorage` depends on the plan's `capacity.storageAllocationMode`:

- **`Volume`**: the platform resizes the PVC. Map the role to a class that
  supports volume expansion.
- **`UnrestrictedLocal`**: on node-local storage, the platform records the
  larger allocation against the logical budget. It does not resize the PVC or
  cap the directory, and the host filesystem must have the space.

Backups go to the Project's own bucket by default: `backup.objectStoreSource:
ProjectBucketClaim` with `objectBucketClaimName: db-backups`. Each Project has its own backup bucket.
`objectStoreEndpoint` must be the endpoint the pods reach, `https://<S3
hostname>`, not the in-cluster service. `Plane` instead uses the plan's own
endpoint and bucket with the cell's credential. The shared plans back up daily
at 02:00 UTC and keep 7 days (Development) or 14 days (Production), with
tenant-selectable retention of 1 to 35 days and schedules no more frequent
than hourly.

## Revisions

An instance pins the class and plan revision strings it was created with, and
nothing fences plan content under an unchanged revision. Every change to a
cluster's rendered catalog therefore needs a new revision, either by bumping
the literal in the manifest or by overriding one cluster's `/spec/revision`
with a JSON patch in its `services-catalog.yaml`. `revisions.lock` records the
digest of each rendered object per cluster; the pre-push gate refuses content
that changed under an unchanged revision. After editing:

```sh
scripts/services-catalog-render-test.sh --update   # refreshes revisions.lock
scripts/services-catalog-render-test.sh            # the gate
```

A class carries `spec.blueprint.expectedDigest`. When the runner's compiled
blueprint of that family does not match, the class stops being `Verified` and
new placements are refused while running instances keep their pins.
Regenerate the digest in the services repository with
`go run ./hack/blueprint-digest <family>` when the family code changes, and
move `SERVICES_RUNNER_TAG`, the bundle's `revision` and the class in one
commit.

## Extend the catalog

The catalog is not a fixed list of engines.
An additional service requires a family adapter, parameter schema, credential and endpoint contracts, and declared operation support.
Its bundle must pin compatible runner and operator versions.
Define capacity, isolation, connectivity, usage meters, and recovery responsibilities before you publish a plan.

Catalog entries alone do not implement a service.
For any additional database, workflow system, or application, qualify its integration on the target infrastructure first.
Publish only implemented operations. Describe unsupported recovery or scaling paths in the plan's responsibility statement.

## Add a family to a cluster

1. Install or attest the engine operator (see the operator table on
   [Enabling managed services](managed-services-enable.md#4-family-operators))
   and list the family in `SERVICES_FAMILIES`; add it to
   `SERVICES_UNMANAGED_OPERATORS` when Flux, not the bundle, owns the
   operator.
2. Select `components/<family>` in the cluster's `services-catalog.yaml`.
3. Wait until `kubectl get servicedataplane <plane>` reports the bundle ready
   and the class `Verified`.
4. Create a test service and verify a binding with an application client.
5. Test each operation that the plan offers, including recovery where supported.
6. Test access restrictions, quotas, deletion, and retained resources.
7. Publish the console annotation only after the checks pass.

## Related

- [Managed services overview](managed-services-overview.md)
- [Enabling managed services](managed-services-enable.md)
- [Operating the service](managed-services-operations.md)
