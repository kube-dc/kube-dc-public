# Operate managed services

As a platform operator, use this guide to approve requests, inspect service state, and manage catalog changes.
Tenant procedures are in [Managed service operations](/cloud/managed-services-operations).
The class and plan determine available actions for each service.

## Approve an operation

An operation waits in `AwaitingApproval` when its type is allowed but absent from the plan's `operations.autoApprove` list.
Only authorized operator identities can approve it.

1. List the Project's operations:

   ```bash
   kubectl get serviceoperations -n <project>
   ```

2. Inspect the request and its target service before approval:

   ```bash
   kubectl get serviceoperation <operation> -n <project> -o yaml
   ```

3. Approve the selected operation:

   ```bash
   kubectl annotate serviceoperation <operation> -n <project> \
     services.kube-dc.com/approved=true
   ```

4. Check its final phase and the service's observed state.

Replace `<project>` with the Project namespace and `<operation>` with the request name.
The platform admin console uses the same approval contract.
Admission refuses approval annotations from tenant identities.
Platform-generated credential policy operations have additional controls and require `system:masters` for approval or cancellation.

## Read service status

These fields distinguish the requested configuration from observed results:

| Field or condition | Meaning |
|---|---|
| `Ready` | Required acceptance, placement, reconciliation, connectivity, and backup checks passed |
| `Stale=True` | The hub lacks recent data-plane observations. Local engine operation can continue |
| `Frozen=True` | Ownership drift blocks mutation until the operator resolves it |
| `CatalogPinned=False` | The published catalog differs from the instance's pinned revisions |
| `status.acceptedRevision` | Revision accepted by the control plane |
| `status.appliedRevision` | Revision that the runner reports as applied |
| `status.effectiveConfiguration` | Observed configuration, including completed operation changes |

A running Pod does not prove that the service is ready.
Check endpoint verification and backup readiness where the plan requires them.
After a change, compare accepted and applied revisions and inspect operation results.

## Verify a release

The hub, runner, and catalog must remain compatible.
After a release, inspect the target data plane:

```bash
kubectl get servicedataplane <plane> -o yaml
```

Replace `<plane>` with the registered data-plane name.
Check `status.ready`, runner version, family bundles, and reported capability checks.
A missing runner version or an unready bundle can block placement.

Use a disposable Project to verify the published service contract:

1. Create a service from each plan that changed.
2. Connect an application through a binding with certificate verification enabled.
3. Test the operations that changed, including approval and capacity refusals.
4. For backup-enabled plans, restore a backup and verify application data.
5. Verify tenant role restrictions and service-owned resource protection.
6. Delete test resources and inspect retained data and credentials.

Run these checks against the target installation.
Unit tests and console fixtures do not prove network access, storage behavior, or recovery on that installation.

## Adopt a catalog revision

A class or plan change does not automatically reconfigure placed services.
Read the `CatalogPinned` condition to obtain the exact adoption token:

```bash
kubectl get managedservice <service> -n <project> \
  -o jsonpath='{range .status.conditions[?(@.type=="CatalogPinned")]}{.message}{"\n"}{end}'
```

Replace `<service>` with the instance name.
The token identifies the class revision, plan revision, and a digest of the target pins.

After you review the change, apply that token:

```bash
kubectl annotate managedservice <service> -n <project> \
  services.kube-dc.com/adopt-catalog=<token>
```

Replace `<token>` with the token from the condition.
The hub consumes it once. A later catalog change requires another token.
Wait for the applied revision and `CatalogPinned=True`.
Changing the connectivity class requires a separate placement.

For an adapter upgrade, use this order:

1. Release the compatible runner bundle.
2. Publish the class adapter version, blueprint digest, and revision.
3. Wait for class verification.
4. Adopt the catalog for selected instances during approved maintenance.
5. Verify each instance before you continue.

The operator-only `services.kube-dc.com/accept-parameters` annotation accepts a refused parameter snapshot for one generation.
Use it only after you investigate the drift. It is not a routine tenant configuration path.

## Capacity changes

The family integration defines supported changes. The plan can restrict them further.
These documented integrations illustrate the differences:

| Family | CPU and memory | Storage | Members |
|---|---|---|---|
| PostgreSQL | `Resize` | `ExpandStorage` | `Scale` |
| Kafka | Fixed after creation | `ExpandStorage` | `Scale` for brokers |
| Valkey | `Resize` | `ExpandStorage` | Fixed by class |
| MariaDB | `Resize` | `ExpandStorage` | Fixed by class |
| MySQL | Fixed after creation | Fixed after creation | Fixed by plan |
| ClickHouse | Fixed after creation | Fixed after creation | Fixed by plan |

Creation-time bounds do not imply support for later resizing.
These examples are not a complete catalog or a contract for additional families.
Check the class operation declarations and the plan's `operations.allowed` field.

A service cannot change its class or plan reference in place.
For a compatible backup-enabled family, `RestoreToNew` can create a target with another plan.
Set the target plan in `spec.restore.target.planRef`.
It must accept the archive family, format, and engine major version.
The tenant verifies data and changes application connections after recovery.

Kafka metadata exports do not contain message data.
A move to another Kafka cluster requires a separate message transfer procedure.

## Backup and recovery

A plan selects Project or provider backup storage.
Project-backed plans commonly use the `db-backups` bucket claim.
Use a verified HTTPS endpoint reachable from all backup and recovery clients.

A `ServiceBackup` record can outlive its source service.
Inspect recovery metadata, the retention deadline, and archive availability before you approve a restore.
Do not infer a cross-plane migration capability from the presence of `RestoreToNew`.
Use only placement and recovery combinations qualified for that installation.

PostgreSQL restore in place requires the exact engine UID, data-loss confirmation, and disabled service deletion protection.
It replaces existing data. The tenant must verify recovery before application writes resume.
See [Backups and restore](/cloud/postgresql-backup-restore).

For in-Project placement, Project deletion removes services and data volumes without a final backup.
Service deletion policies do not prevent this.

## Data-plane lifecycle

Inspect registered planes before you change placement availability:

```bash
kubectl get servicedataplanes
kubectl get servicedataplane <plane> -o yaml
```

`spec.lifecycle` controls placement and retirement.
`Paused` and `Draining` restrict placement. `Decommissioning` withdraws management with data retention.
Resolve each instance and verify its deletion or retention result before you remove the data-plane registration.
Destructive cleanup requires a valid data-plane identity.

To retire a family, follow this sequence:

1. Inventory its managed and retained instances.
2. Agree a deletion, retention, or replacement procedure for each instance.
3. Remove the family from the plane's offered families after the instance work completes.
4. Remove its runner and operator only when no retained engine needs them.
5. Delete the family bundle last.

The bundle finalizer blocks deletion while references remain.
Do not delete generated family ClusterProfiles directly.

## Evidence and usage

Inspect evidence and service usage with these commands:

```bash
kubectl get serviceevidences -n kube-dc-services
kubectl get managedservice <service> -n <project> \
  -o jsonpath='{.status.usage}{"\n"}'
```

Export evidence that must outlive its configured retention.
The runner reports sequenced usage batches. Gaps remain visible instead of becoming estimated measurements.
Audit policy records managed services writes and Secret access as metadata.
Keep credential values out of command arguments that can appear in request records.

## Console configuration

Runtime configuration controls which organizations can use managed services.
Set `KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false` and list organizations to restrict visibility.
Visibility does not replace API authorization.

The backend's `PROM_URL` must point to a compatible metrics source for service metrics to appear.
A missing metrics view does not prove that the service is unhealthy.

## Next steps

See [Publish the catalog](managed-services-catalog.md) for plan revisions and family qualification.
See [Managed service operations](/cloud/managed-services-operations) for the tenant request and result contract.
