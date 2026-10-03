# Check managed service status

As a Project member, use this reference to check service readiness and diagnose a refused or stalled change.
Phases and conditions use a shared contract. Engine details and supported operations depend on the service family.
For operation progress, see [Request an operation](managed-services-operations.md#read-the-result).

## Read the status

Read the service phase and `Ready` condition:

```bash
kubectl get managedservice <service> -n <project>
```

Replace `<service>` with the service name and `<project>` with its Project namespace.

### Phases

The service reports one of these phases:

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

Read each condition with its reason, message, and observed generation:

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
kubectl get managedservice <service> -n <project> \
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
4. Check that `status.lastObservedAt` advances.
   If it is more than four minutes old, treat the status as last-known, even without `Stale=True`.
   Contact your provider if the timestamp does not advance.

Print the accepted and applied revisions with the observation time:

```bash
kubectl get managedservice <service> -n <project> \
  -o jsonpath='accepted={.status.acceptedRevision}/{.status.acceptedGeneration} applied={.status.appliedRevision}/{.status.appliedGeneration} observed={.status.lastObservedAt}{"\n"}'
```

An invalid edit can leave the phase and the applied revision unchanged while
`Accepted=False` reports why the new spec was refused.

### Other status fields

These fields describe the applied service and its recent activity:

| Field | Contents |
|-------|----------|
| `status.effectiveConfiguration` | The configuration in force, including values set by operations |
| `status.engineDetails` | Engine observations, whose keys depend on the service family |
| `status.endpoints` | Named endpoints with addresses, TLS details and probe results. See [Connect applications](managed-services-connect.md) |
| `status.credentials` | Credential roles with their Secret name, version and fingerprint. Never the values |
| `status.lastBackup`, `status.recentBackups` | The newest verified backup and a short recent window. This is not the full backup history |
| `status.operationSummary` | The active, last successful and last failed operation |

Events for the service are recorded in the Project:

```bash
kubectl get events -n <project> --field-selector involvedObject.name=<service>
```

## Common refusals

Use the reported reason to identify the next action:

| Where it shows | Reason or message | Meaning and fix |
|----------------|-------------------|-----------------|
| `kubectl apply` error | `parameters ... are CreateOnly, OperationOnly or ReplaceOrMigrate ...` | You edited a parameter that cannot change on the `ManagedService`. Revert it, or use the matching operation |
| `kubectl apply` error | `spec.parameters.cpu ... use spec.compute.cpu` (also `memory`, `instances`, `storage`, `version`) | The manifest uses a retired parameter path. Move the value to the typed field the message names |
| `kubectl apply` error | `classRef is immutable`, `planRef is immutable ...`, `placement is immutable ...`, `connectivity is immutable after creation ...`, `restoreFrom is create-only` | These fields are fixed at creation. Create a new service instead |
| `kubectl delete` error | `deletionProtection is enabled on this ManagedService; set spec.deletionProtection=false first` | Set `deletionProtection: false`, apply, then delete |
| `Forbidden` from the API | RBAC | Your Project role cannot write this resource. See [Project roles](managed-services.md#project-roles) |
| `Accepted=False`, phase `Rejected` | `PlanNotEntitled` | The plan was not found, is disabled or not verified, or does not allow the requested placement mode, connectivity class or deletion policy. For example, if `placement.mode` is omitted and the plan's `allowedPlacementModes` does not include `TenantCluster`, the message is `plan <plan> does not allow placement mode TenantCluster` |
| `Accepted=False`, phase `Rejected` | `ClassNotVerified` | The class name is wrong or the class is unavailable |
| `Accepted=False`, phase `Rejected` | `ConnectivityClassAbsent` | The connectivity class name does not exist |
| `Accepted=False`, phase `Rejected` | `ParameterSchemaRejected` | A parameter is invalid for the class or outside the plan's bounds. Examples: `spec.engineVersion: must equal the plan engine version "..."`, `spec.topology.instances: must be between <min> and <max> for plan <plan>`, `spec.storage.size: must not exceed the plan maximum ...`, or an unknown parameter reported as `additional properties ...` |
| `Accepted=False`, phase `Rejected` | `PlanQuotaExceeded` | The Project already has the plan's `maxInstancesPerProject` services: `plan <plan> allows <n> instances per project` |
| `Placed=False`, phase `Pending` | `NoQualifiedDataPlane` | The platform has nowhere to place a service of this plan at present. Ask your provider |
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
`PlanQuotaExceeded`, clear without an edit after their cause is gone. When the
fix needs a different value for a `CreateOnly` or `OperationOnly` parameter or
for a catalog field, which cannot be edited after the object exists, delete the
refused service and create a corrected one. Set `deletionProtection: false`
first if you enabled it.
