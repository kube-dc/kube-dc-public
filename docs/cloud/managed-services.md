# Managed Services

Managed Services runs a provider-operated data service inside your Project:
a PostgreSQL, MySQL, MariaDB or ClickHouse database, a Valkey cache or a Kafka
cluster. You choose an engine and a plan; the platform provisions the engine,
issues its certificates, holds its credentials, backs it up, runs your
day-2 operations and reports what it actually applied. You never operate the
engine yourself.

You can work from the [console](managed-services-console.md), or describe the
service with Kubernetes resources in the `services.kube-dc.com/v1alpha1` API
and apply them with `kubectl` or your GitOps pipeline. Both paths create the
same objects, and the console offers the exact YAML for every request it
makes.

:::warning db-manager databases are deprecated
Managed Services replaces the earlier `KdcDatabase` product operated by
db-manager. Its console area remains only for the organizations and Projects
your provider has listed as still running such databases, and new databases
cannot be created that way. See
[Migrating from db-manager databases](managed-services-migration.md).
:::

## Families

| Family | Class | What the plans offer | Backups |
|--------|-------|----------------------|---------|
| [PostgreSQL](postgresql-create.md) | `postgresql` | One instance, or a replicated cluster with automatic failover, a connection pooler and read-only credentials | Scheduled backups to object storage with continuous archiving, point-in-time recovery, restore into a new service or in place |
| [MySQL](managed-services-mysql-mariadb.md) | `mysql` | One server with a Router, or a Group Replication cluster behind Routers | Scheduled verified logical archives, restore into a new service |
| [MariaDB](managed-services-mysql-mariadb.md) | `mariadb` | One server, or a Galera cluster where published | Scheduled verified logical archives, restore into a new service |
| [ClickHouse](managed-services-clickhouse.md) | `clickhouse` | One server with its own Keeper, or two replicas of one shard behind one address | Scheduled verified native archives, restore into a new service |
| [Valkey](managed-services-valkey.md) | `valkey` | One node, or a Sentinel-managed set where published | On-demand snapshots only; not a recovery facility |
| [Kafka](managed-services-kafka.md) | `kafka` | Controllers and brokers sized by the plan | None; durability is replication |

Which families and plans your Project can use is decided by your provider.
The console shows only what is published for your installation; a manifest
that names an unpublished plan is refused.

## Resource model

| Resource | Scope | What it is |
|----------|-------|------------|
| `ManagedService` | Project | One service instance: its class, plan, placement, connectivity and parameters |
| `ServiceBinding` | Project | A request to deliver one credential role of a service as a Kubernetes `Secret` in the Project |
| `ServiceOperation` | Project | One immutable day-2 action on a service, such as `Scale`, `Backup` or `RotateCredentials` |
| `ServiceCredentialPolicy` | Project | A rotation schedule for one credential role of a service |
| `ServiceBackup` | Project | Read-only backup history records |
| `ManagedServiceClass`, `ManagedServicePlan`, `ConnectivityClass` | Cluster | The provider's catalog. Tenants cannot list or read it; see [Classes and plans](managed-services-plans.md) |

A typical workflow has four steps:

1. Create a `ManagedService` and wait until it is ready.
2. Read the service's UID from `metadata.uid`.
3. Create a `ServiceBinding` that names the service and pins that UID. The
   platform delivers a `Secret` with connection details and credentials.
4. Point your application at that `Secret`.

In the console, choosing **Kubernetes Secret** on the last step of creation
does steps 2 and 3 for you and names the Secret `<service>-owner`.

The PostgreSQL pages of this chapter walk through the full procedure with
manifests: [Create a PostgreSQL service](postgresql-create.md) and
[Connect applications](postgresql-connect.md). The other family pages show
what differs for their engine.

### The service UID

Resource names can be reused. If you delete `orders-db` and create a new
`orders-db`, the new service has a different `metadata.uid` and different data.
Bindings, operations and credential policies carry `spec.serviceUID` so that
they act on the exact service you meant:

- A binding or operation whose `serviceUID` does not match the current service
  of that name is refused with reason `ServiceIdentityChanged`. Create a new
  one for the new service.
- A `ServiceCredentialPolicy` requires `serviceUID`.
- An operation without `serviceUID` is accepted by the API and then acts on
  whichever service currently holds the name. Always set `serviceUID`.

Because the UID exists only after the service is created, create the service
first and the objects that reference it second. A GitOps pipeline must read
the UID and pin it rather than drop the field.

### Day-2 changes

Each class parameter belongs to a mutation class that decides how it can
change after creation:

| Mutation class | How to change it |
|----------------|------------------|
| `CreateOnly` | It cannot change. Create a new service instead |
| `OnlineDesired` | Edit the `ManagedService` and apply the complete manifest, or change it in the console's **Settings** tab |
| `OperationOnly` | Create a `ServiceOperation`, or use the matching action in the console. Editing the value on the `ManagedService` is refused |

Each family page lists its parameters and their mutation classes.

Applying a manifest or creating an operation only records your request.
Completion comes from status: the service's `Accepted` and `Ready` conditions,
and the operation's `status.phase`. Your plan decides which operations are
allowed at all and which ones wait for provider approval.

## What the provider owns and what you own

| The provider | You |
|--------------|-----|
| The catalog: classes, plans and connectivity classes | The `ManagedService` manifest and the choices you make within your plan |
| Provisioning and running the engine, including automatic failover when your plan enables `topology.ha` and your service has two or more instances | Which workloads receive credentials, through `ServiceBinding` resources |
| Executing operations, and scheduled backups when your plan sets `backup.enabled: true` | When to request operations, and verifying their results |
| Capacity, placement, backup storage and engine upgrades available in the plan | Your data model, schema, SQL grants, and application connection handling and retries |
| Approving operations that your plan marks as approval-gated | Testing that you can restore your data |

The platform protects the engine's own objects (database cluster, volumes,
engine Secrets and ServiceAccounts) from Project identities. Change a service
only through its `ManagedService`, `ServiceOperation` and `ServiceBinding`
resources, or through the console, which does the same.

## Project roles

The standard Project roles (see [User and group management](team-management.md))
grant the following access to Managed Services resources:

| Action | `admin` | `developer` | `project-manager` | `user` |
|--------|---------|-------------|-------------------|--------|
| View services, bindings, operations, credential policies and their status | ✅ | ✅ | ✅ | ✅ |
| View `ServiceBackup` history | ✅ | ✅ | ✅ | ✅ |
| Create, edit and delete `ManagedService` resources | ✅ | ✅ | ❌ | ❌ |
| Create and delete `ServiceBinding` resources | ✅ | ✅ | ❌ | ❌ |
| Create a `ServiceOperation`, or cancel one before it runs | ✅ | ✅ | ❌ | ❌ |
| Create and delete `ServiceCredentialPolicy` resources | ✅ | ✅ | ❌ | ❌ |
| Change an existing `ServiceCredentialPolicy` (interval, pause) | ✅ | ✅ | ✅ | ❌ |
| Read delivered credential `Secret` objects | ✅ | ✅ | ✅ | ❌ |
| Approve an approval-gated operation | ❌ | ❌ | ❌ | ❌ |
| Write the status of any Managed Services resource | ❌ | ❌ | ❌ | ❌ |
| Read the catalog (classes, plans, connectivity classes) | ❌ | ❌ | ❌ | ❌ |

`project-manager` is read-mostly by design. It cannot create or cancel
`ServiceOperation` resources, and it cannot create or delete bindings or
policies. Approval of approval-gated operations belongs to the provider; ask
your provider when an operation waits in `AwaitingApproval`.

## The Project is the credential boundary

A `ServiceBinding` delivers its `Secret` into the same Project as the service.
For the in-Project placement this chapter covers, a binding whose consumer is
in another namespace is refused with reason `ConsumerNamespaceRejected`.

Inside a Project, the consumer named on a binding does not restrict who can
read the delivered `Secret`. Every identity that can read Secrets in the
Project, which includes `admin`, `developer` and `project-manager`, can read
every delivered credential. This also applies to the engine's own credential
Secrets that `ManagedService.status.credentials` refers to. Place workloads
and teams that must not share database credentials in separate Projects.

## Delete a service or a Project

A `ManagedService` has two deletion settings:

- `spec.deletionPolicy` decides what happens to the engine and its data when
  you delete the `ManagedService`: `Retain` (the default), `SnapshotAndDelete`
  or `Delete`.
- `spec.deletionProtection: true` refuses deletion of the `ManagedService`
  until you set it to `false` in a separate update.

See [Create a PostgreSQL service](postgresql-create.md#service-fields) and
[Status and deletion](managed-services-status-deletion.md) for details.

:::warning Deleting a Project deletes its services without a final backup
For the in-Project placement covered by this chapter, a service runs in the
Project's own namespace: its `status.instanceNamespace` is the Project
namespace. When the Project is deleted, every such service is deleted together
with its data volumes. This happens whatever `deletionPolicy` says and
regardless of `deletionProtection` or any delete confirmation. No final backup
is taken. Those settings protect a service from being deleted on its own, not
from deletion of its Project. Before you delete a Project, copy out any data
you need to keep.
:::

## Next steps

- [Use the console](managed-services-console.md): the catalog, the creation
  sheet and the service page.
- [Classes and plans](managed-services-plans.md): the class, plan and
  connectivity names to use, and the plan fields that decide what a service
  may do.
- [Create a PostgreSQL service](postgresql-create.md) and
  [Connect applications](postgresql-connect.md): the full manifest procedure.
- [MySQL and MariaDB](managed-services-mysql-mariadb.md),
  [ClickHouse](managed-services-clickhouse.md),
  [Valkey](managed-services-valkey.md), [Kafka](managed-services-kafka.md).
- [Status and deletion](managed-services-status-deletion.md)
- [Migrating from db-manager databases](managed-services-migration.md)
