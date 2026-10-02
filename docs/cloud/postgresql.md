# PostgreSQL

As a Project administrator or developer, use these guides to create and operate a managed PostgreSQL database.
Select a published plan for the version, capacity, availability, and recovery support your application needs.

## Create and connect

Start with the creation and connection procedures:

1. [Create a PostgreSQL service](postgresql-create.md) with a manifest, or [use the console](managed-services-console.md).
2. [Connect applications](postgresql-connect.md) through a binding with verified TLS.
3. [Configure credentials and rotation](postgresql-credentials.md) for declared roles or supported existing SQL logins.

PostgreSQL provides a primary endpoint, replica endpoints, and optional PgBouncer pooling.
Endpoint selection and credential roles are independent.
See the connection guide for availability checks and client examples.

## Operate and recover

Choose the guide for your task:

| Task | Guide |
|---|---|
| Scale replicas, resize compute, expand storage, change settings, or upgrade | [PostgreSQL operations](postgresql-operations.md) |
| Schedule backups, restore into another service, restore in place, or choose a recovery time | [PostgreSQL backups and recovery](postgresql-backup-restore.md) |
| Connect from outside the Project through a supported Gateway | [PostgreSQL external access](postgresql-external-access.md) |
| Remove a database or inspect retained resources | [PostgreSQL deletion](postgresql-deletion.md) |

Plan entitlement and service state determine which actions you can request.
Test recovery before you rely on backups, and check the documented limits for each operation.

## Shared managed service tasks

The same platform rules apply to PostgreSQL and other service families:

- [Classes and plans](managed-services-plans.md) define available capacity and operations.
- [Request an operation](managed-services-operations.md) explains approval, execution windows, and results.
- [Check service status](managed-services-status.md) explains readiness, stale observations, and refused changes.
- [Delete a managed service](managed-services-status-deletion.md) explains protection and Project deletion.
