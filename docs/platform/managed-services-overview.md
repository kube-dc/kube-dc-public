# Managed services architecture

import {ManagedServicesArchitectureDiagram} from '@site/src/components/Diagram/ManagedServicesDiagrams';

As a platform operator, use managed services to publish software that your team operates for tenants.
This page describes the control model, execution components, and tenant boundaries.
Use [Enable managed services](managed-services-enable.md) for installation steps.

## Design

Managed services separate the product contract from the service implementation.
A class defines a family. A plan defines the provider's offering for that class.
Tenants create instances and request operations through a common API.

The catalog can grow across databases, caches, message brokers, analytics systems, and applications.
Each family adapter defines its parameters, credentials, endpoints, operations, and recovery format.
An adapter can manage workloads directly or use a native operator.
The presence of an adapter does not publish a tenant offering.

<ManagedServicesArchitectureDiagram />

## Components

The following components implement the service contract:

| Component | Location | Responsibility |
|---|---|---|
| Hub | Management cluster | Validates requests, selects placement, signs revisions, schedules operations, and records results |
| Runner | Data plane | Verifies signed instructions, runs family adapters, and reports observed state and usage |
| Family bundle (`ServiceFamilyBundle`) | Cluster resource | Pins the runner and declares the family's operator requirements |
| Data plane (`ServiceDataPlane`) | Cluster resource | Declares connectivity, families, storage roles, capacity, isolation, and lifecycle state |
| Catalog | Cluster resources | Defines classes, plans, and connectivity options |
| Native operator | Data plane, where required | Reconciles engine resources under the family integration |

A data plane is the cluster where service instances run.
For a self-hosted data plane, `SERVICES_RUNNER_SELF=true` places the hub, runner, and services in the management cluster.
Standard shared plans use `ProviderShared` placement with `NamespaceSharedNodes` isolation.
Their instances run in the tenant's Project namespace.
Other placements require a qualified provider configuration and compatible plans.

## Request flow

The platform processes an instance through these stages:

1. The tenant creates a `ManagedService` in a Project.
2. The hub checks its class, plan, parameters, placement, connectivity, and capacity.
3. The hub pins catalog revisions and signs the resolved configuration.
4. The runner verifies the signature and applies the family resources.
5. The runner reports engine state, connectivity, topology, and usage.
6. The hub publishes the observed service status.

A `ServiceBinding` requests a credential role for an application.
The platform delivers the credential through a controlled Secret publication path.
Credentials do not appear as values in service status.

The tenant requests later actions with `ServiceOperation` resources.
The plan controls entitlement, approval, and maintenance windows.
The family adapter checks engine-specific conditions and executes the action.
See [Operate managed services](managed-services-operations.md) and the [tenant operation guide](/cloud/managed-services-operations).

## Tenant boundaries

The following boundaries apply to the shared Project model:

- Tenants manage instances, bindings, operations, and credential policies within their Project roles.
- Tenants read backup history and status. They cannot write controller status.
- Tenants cannot read the cluster catalog directly. The console receives a filtered publication.
- Tenants cannot approve operations, adopt catalog revisions, or accept parameter snapshots.
- Platform policies protect service-owned workloads, volumes, ServiceAccounts, and engine credentials from direct tenant modification.
- Project Secret permissions control access to delivered application credentials.

A binding consumer does not provide separate Secret read isolation within a Project.
Use separate Projects when teams must not share credentials.

## Catalog revisions and upgrades

An instance pins its class revision, plan revision, and adapter version.
Catalog changes do not automatically update placed instances.
The operator explicitly adopts a compatible revision for each instance.

Release the hub, runner, and catalog as a compatible set.
Verify family bundles and class blueprint digests before you publish plans.
See [Publish the catalog](managed-services-catalog.md) for revision controls and qualification steps.

## Recovery and usage

Recovery is a family capability, not a universal platform promise.
Backup-enabled services publish `ServiceBackup` records into the Project.
Those records can survive source service deletion. Project deletion removes the records and in-Project service data.
A completed history record does not prove that its archive remains available.

The runner sends sequenced usage batches.
The plan selects meter definitions. The hub records gaps instead of estimating missing measurements.
Read an instance's usage totals with this command:

```bash
kubectl get managedservice <service> -n <project> \
  -o jsonpath='{.status.usage}{"\n"}'
```

Replace `<service>` with the instance name and `<project>` with its Project namespace.
Metered usage is not a fixed performance or billing guarantee. The provider's plan defines commercial terms.

## Next steps

Use these guides to publish and operate a service offering:

- [Enable managed services](managed-services-enable.md).
- [Publish the catalog](managed-services-catalog.md).
- [Operate managed services](managed-services-operations.md).
- [Review the security model](security-model.md).
