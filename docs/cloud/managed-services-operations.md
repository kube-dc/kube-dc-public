# Managed service operations

import {ManagedServicesOperationDiagram} from '@site/src/components/Diagram/ManagedServicesDiagrams';

As a Project administrator or developer, use a `ServiceOperation` to request an action on a managed service.
This guide explains the common operation contract. Family guides describe operation parameters and service-specific limits.

The class defines operations that its integration implements. The plan selects which operations you can request and which require provider approval.
An operation name in the API does not mean that every service supports it.

<ManagedServicesOperationDiagram />

## Operation types

The API defines the following operation types. Check your service's plan before you use one:

| Type | Purpose | Main condition or effect |
|---|---|---|
| `Backup` | Capture a backup or export | The content depends on the family. Kafka exports metadata, not message data |
| `RestoreToNew` | Create another service from a retained backup | Select an exact `ServiceBackup` name and UID. The target uses fresh credentials |
| `RestoreInPlace` | Replace data in an existing service | Destructive. Requires explicit identity and data-loss confirmation where supported |
| `Scale` | Change the member count | Must stay within plan bounds. Does not change the service class or plan |
| `Resize` | Change CPU or memory | Must stay within plan bounds. Can restart service members |
| `ExpandStorage` | Increase data volume capacity | Requires an expandable storage class. Volumes cannot shrink |
| `Switchover` | Move the primary role to a healthy member | Requires a suitable replica and a supported topology |
| `Failover` | Promote a replica after primary failure | Requires service-specific health and data-loss checks |
| `RotateCredentials` | Replace a role's credentials | Updates bindings. Applications must load the replacement credential |
| `MinorUpgrade` | Change the engine within a supported release line | Requires a target version or image allowed by the plan |
| `MajorUpgrade` | Move to another supported release line | Can be irreversible. Read the family procedure first |
| `UpdateParameters` | Change supported engine settings | Only allowed parameters can change. Some changes require a restart |
| `Hibernate` | Stop service compute and retain data | Requires family support. Storage remains allocated |
| `Resume` | Start a hibernated service | Requires retained data and sufficient capacity |
| `Custom` | Run a family-specific action | The family defines the action name, parameters, and checks |

Creation, bindings, and deletion use their own resources or service fields.
They are not additional `ServiceOperation` types.

## Before you begin

Check these requirements:

- You have the Project `admin` or `developer` role.
- The plan allows the requested operation.
- You know the service name and UID.
- Your application can tolerate the documented restart, interruption, or credential change.
- For a destructive operation, you have a tested recovery procedure.

Read the service identity:

```bash
kubectl get managedservice <service> -n <project> \
  -o jsonpath='{.metadata.uid}{"\n"}'
```

Replace `<service>` with the service name and `<project>` with its Project namespace.

## Submit an operation

This example requests credential rotation. Use a role that your service class supports:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: rotate-credentials-1
  namespace: <project>
spec:
  serviceRef:
    name: <service>
  serviceUID: <service-uid>
  type: RotateCredentials
  idempotencyKey: rotate-credentials-1
  parameters:
    role: <role>
  execution:
    window: Immediate
```

Replace `<service-uid>` with the UID you read.
Replace `<role>` with a published credential role, such as PostgreSQL `owner` or Valkey `default`.
Replace `<project>` and `<service>` with the same values as the lookup command.

1. Save the manifest as `rotate-credentials.yaml`.
2. Submit the request:

   ```bash
   kubectl apply -f rotate-credentials.yaml
   ```

3. Watch its result:

   ```bash
   kubectl get serviceoperation rotate-credentials-1 -n <project> -w
   ```

The expected final phase is `Succeeded`.
If the request stops in another phase, read `status.reason` and `status.message` before you submit another request.

## Approval and execution windows

`operations.allowed` controls entitlement. `operations.autoApprove` controls whether the provider must approve the request.
Project roles cannot approve their own requests.

`execution.window: Immediate` requests execution after the required checks and approval.
`NextPlanWindow` waits for the recurring maintenance window in the plan.
It does not bypass approval, capacity checks, or operation conflicts.

Each operation has an immutable specification and an idempotency key.
To retry an uncertain submission, apply the same manifest with the same name and key.
Do not submit a replacement until you know whether the first operation changed the service.

## Read the result

The following phases describe progress:

| Phase | Meaning |
|---|---|
| `Pending` | The request waits for a prerequisite, maintenance window, or conflicting operation |
| `Preflight` | The platform checks state and capacity before execution |
| `AwaitingApproval` | The provider must approve the request |
| `Accepted` | Queue checks passed. Execution checks can still refuse the request |
| `Running` | Execution is in progress |
| `Succeeded` | The platform reports completion |
| `Rejected` | A check refused the request. Inspect timestamps and checkpoints before retrying |
| `Failed` | Execution did not complete successfully. Partial changes can remain |
| `Cancelled` | The platform accepted cancellation before execution |

Read the full record when you investigate a failure:

```bash
kubectl get serviceoperation <operation> -n <project> -o yaml
```

Replace `<operation>` with the operation name.
Check `status.reason`, `status.message`, timestamps, checkpoints, and result fields.
For capacity or parameter changes, also check the service's `status.effectiveConfiguration` and `Ready` condition.
Operation results describe applied changes without rewriting the tenant's manifest.

## Cancel a waiting operation

To request cancellation before execution, set the cancellation annotation:

```bash
kubectl annotate serviceoperation <operation> -n <project> \
  services.kube-dc.com/cancel=true
```

Wait for `Cancelled`. A cancellation request can lose a race with execution.
Deleting the operation is not an instruction to reverse changes.

## Service-specific procedures

Use the appropriate procedure and its supported operation list:

- [PostgreSQL operations](postgresql-operations.md) and [backup recovery](postgresql-backup-restore.md).
- [MySQL and MariaDB](managed-services-mysql-mariadb.md).
- [ClickHouse](managed-services-clickhouse.md).
- [Valkey](managed-services-valkey.md).
- [Kafka](managed-services-kafka.md).

For another catalog service, use the operation schema and responsibility statement published by your provider.
