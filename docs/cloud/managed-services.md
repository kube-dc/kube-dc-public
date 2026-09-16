# Managed Services

Managed Services runs a provider-operated service, such as a PostgreSQL
database, inside your Project. You describe the service you want with
Kubernetes resources in the `services.kube-dc.com/v1alpha1` API, and the
platform provisions the engine, runs it and reports what it actually applied.

This chapter currently covers PostgreSQL.

:::info Managed Services and KdcDatabase
Managed Services is the replacement for new databases. Existing `KdcDatabase`
resources, described in [Managed Databases](managed-databases.md), remain
supported and will be deprecated. The two APIs are separate: a `KdcDatabase`
does not appear as a `ManagedService`, and the reverse is also true. See
[Coming from KdcDatabase](managed-services-from-kdcdatabase.md).
:::

## Resource model

| Resource | Scope | What it is |
|----------|-------|------------|
| `ManagedService` | Project | One service instance: its class, plan, placement, connectivity and parameters |
| `ServiceBinding` | Project | A request to deliver one credential role of a service as a Kubernetes `Secret` in the Project |
| `ServiceOperation` | Project | One immutable day-2 action on a service, such as `Scale`, `Backup` or `RotateCredentials` |
| `ServiceCredentialPolicy` | Project | A rotation schedule for one credential role of a service |
| `ServiceBackup` | Project | Read-only backup history records |
| `ManagedServiceClass`, `ManagedServicePlan`, `ConnectivityClass` | Cluster | The provider's catalog. Tenants cannot list or read it; see [Classes and Plans](managed-services-plans.md) |

A typical workflow has four steps:

1. Create a `ManagedService` and wait until it is ready.
2. Read the service's UID from `metadata.uid`.
3. Create a `ServiceBinding` that names the service and pins that UID. The
   platform delivers a `Secret` with connection details and credentials.
4. Point your application at that `Secret`.

See [Create a PostgreSQL Service](postgresql-create.md) and
[Connect Applications](postgresql-connect.md) for the full procedure.

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
| `OnlineDesired` | Edit the `ManagedService` and apply the complete manifest |
| `OperationOnly` | Create a `ServiceOperation`. Editing the value on the `ManagedService` is refused |

The PostgreSQL parameters and their mutation classes are listed in
[Create a PostgreSQL Service](postgresql-create.md#parameter-reference).

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
resources. See
[What the platform protects](managed-databases.md#what-the-platform-protects-and-what-that-means-for-your-workloads).

## Project roles

The standard Project roles (see [User and Group Management](team-management.md))
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

## Deleting services and Projects

A `ManagedService` has two deletion settings:

- `spec.deletionPolicy` decides what happens to the engine and its data when
  you delete the `ManagedService`: `Retain` (the default), `SnapshotAndDelete`
  or `Delete`.
- `spec.deletionProtection: true` refuses deletion of the `ManagedService`
  until you set it to `false` in a separate update.

See [Create a PostgreSQL Service](postgresql-create.md#service-fields) and
[Status and Deletion](managed-services-status-deletion.md) for details.

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

- [Classes and Plans](managed-services-plans.md): the class, plan and
  connectivity names to use, and the plan fields that decide what a service
  may do.
- [Create a PostgreSQL Service](postgresql-create.md)
- [Connect Applications](postgresql-connect.md)
- [Status and Deletion](managed-services-status-deletion.md)
- [Coming from KdcDatabase](managed-services-from-kdcdatabase.md)
