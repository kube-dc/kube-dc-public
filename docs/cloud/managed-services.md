# Managed services

import {ManagedServicesModelDiagram} from '@site/src/components/Diagram/ManagedServicesDiagrams';

As a Project member, use managed services to run software that your provider operates for you.
Select a service and plan from the catalog. Then connect your application through a service binding.
Use the console, Kubernetes manifests, or GitOps to manage the same resources.

The catalog can contain databases, caches, message brokers, analytics systems, and applications.
Each installation publishes its own selection. The catalog is extensible and is not limited to the examples in this guide.
A service appears only after the provider installs its integration and publishes a plan.

<ManagedServicesModelDiagram />

## Choose a service

The following families have guides. These examples do not define the complete catalog:

| Family | Purpose | Data protection |
|---|---|---|
| [PostgreSQL](postgresql-create.md) | Relational database | Base backups and continuous log archives. Point-in-time recovery requires plan support |
| [MySQL and MariaDB](managed-services-mysql-mariadb.md) | Relational databases | Verified logical archives. Restore creates a new service |
| [ClickHouse](managed-services-clickhouse.md) | Analytical database | Verified native archives. Restore creates a new service |
| [Valkey](managed-services-valkey.md) | Cache and key-value store | RDB archives on backup-enabled plans. Restore creates a new service |
| [Kafka](managed-services-kafka.md) | Event streaming | Metadata export only. Message protection depends on replication and external copies |

Read the published plan before you select a service.
Check its capacity, topology, operations, backup policy, and support responsibilities.
A Production or HA label does not, by itself, promise a recovery time or protection from site failure.
See [Classes and plans](managed-services-plans.md).

## Resource model

The managed services API uses `services.kube-dc.com/v1alpha1`.
These resources have the same purpose across service families:

| Resource | Scope | Purpose |
|---|---|---|
| `ManagedService` | Project | Selects the class, plan, placement, connectivity, and service settings |
| `ServiceBinding` | Project | Delivers one credential role as a Kubernetes Secret |
| `ServiceOperation` | Project | Records one immutable action and its result |
| `ServiceCredentialPolicy` | Project | Sets a credential rotation schedule |
| `ServiceBackup` | Project | Records backup history and available recovery information |
| `ManagedServiceClass` | Cluster | Defines a service family, parameters, endpoints, credential roles, and operations |
| `ManagedServicePlan` | Cluster | Defines versions, capacity limits, allowed operations, and provider policy |
| `ConnectivityClass` | Cluster | Defines how applications reach a service |

Project roles cannot read the cluster catalog directly.
The console shows the published catalog through a filtered API response. Ask your provider for catalog names when you write manifests.

A normal connection procedure has four steps:

1. Create a `ManagedService` from a published plan.
2. Wait for its `Ready` condition.
3. Create a `ServiceBinding` with the service name and UID.
4. Configure your application to read the delivered Secret.

The console can create the binding after it creates the service.
See [Use the console](managed-services-console.md) or [Create a PostgreSQL service](postgresql-create.md) for a complete procedure.

### The service UID

The UID identifies one service throughout its life. Kubernetes assigns it when it creates the resource.
Deleting and recreating a service with the same name produces a different UID.

Operations and credential policies require `spec.serviceUID`.
Set it on bindings too, so each request identifies the intended service.
A mismatched UID causes `ServiceIdentityChanged`.
Create the service first, then read its UID for resources that refer to it.
For a restore from a deleted source, use the source UID in the selected `ServiceBackup` record.

### Changes after creation

Each class assigns a mutation rule to its parameters:

| Rule | How to change the value |
|---|---|
| `CreateOnly` | Create another service with the required value |
| `OnlineDesired` | Edit the service manifest or use **Settings** in the console |
| `OperationOnly` | Submit the matching `ServiceOperation` |

Operation support depends on both the service integration and the plan.
See [Managed service operations](managed-services-operations.md) for actions, approval, execution windows, and results.
An accepted request is not proof of completion. Read the service conditions and operation status.

## Responsibilities

The provider and the Project team have different responsibilities:

| Provider | Project team |
|---|---|
| Publish qualified classes, plans, and connectivity options | Select a plan that meets application needs |
| Install and operate service engines and their controllers | Manage application data, queries, and client behavior |
| Provide capacity and execute supported operations | Request changes and check their results |
| Execute backups where the plan enables them | Select retention within plan limits and test restores |
| Deliver credentials and execute supported rotation | Control Secret access and refresh application credentials |
| Approve operations that require approval | Allow time for approval and maintenance |

The platform protects service-owned workloads, volumes, and credentials from direct tenant modification.
Use managed services resources or the console to change a service.

## Project roles

Standard Project roles grant the following access:

| Action | `admin` | `developer` | `project-manager` | `user` |
|---|---|---|---|---|
| Read services, bindings, operations, policies, and backup history | Yes | Yes | Yes | Yes |
| Create, edit, or delete services | Yes | Yes | No | No |
| Create or delete bindings and rotation policies | Yes | Yes | No | No |
| Create operations or request cancellation before execution | Yes | Yes | No | No |
| Change an existing rotation policy | Yes | Yes | Yes | No |
| Read delivered credential Secrets | Yes | Yes | Yes | No |
| Approve operations or write resource status | No | No | No | No |
| Read cluster catalog resources directly | No | No | No | No |

See [User and group management](team-management.md) for role assignment.
Ask your provider when an operation waits in `AwaitingApproval`.

## The Project is the credential boundary

For in-Project placement, a binding delivers credentials into the service's Project.
A binding to another namespace is refused with `ConsumerNamespaceRejected`.

The consumer on a binding does not restrict who can read its Secret.
Project identities with Secret read permission can read delivered credentials.
Use separate Projects for teams or workloads that must not share credentials.

## Delete a service or a Project

`spec.deletionPolicy` selects `Retain`, `SnapshotAndDelete`, or `Delete`.
The default is `Retain`. Available policies depend on the plan and service family.
`spec.deletionProtection: true` blocks deletion of the service resource until you disable protection in a separate update.

:::warning Project deletion removes service data
For in-Project placement, deleting a Project removes its services and data volumes without a final backup.
Service deletion policies and deletion protection do not prevent Project deletion.
Copy required data outside the Project before you delete it.
:::

See [Status and deletion](managed-services-status-deletion.md) for retained resources, backup records, and deletion checks.

## Next steps

Use these guides for your next task:

- [Use the console](managed-services-console.md).
- [Select a class and plan](managed-services-plans.md).
- [Run a managed service operation](managed-services-operations.md).
- [Connect an application](postgresql-connect.md).
- [Protect and recover data](backups-snapshots.md).
