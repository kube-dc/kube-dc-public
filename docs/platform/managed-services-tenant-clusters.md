# Managed services on tenant clusters

As a platform operator, let tenants run managed services inside their own Managed Clusters.
This is `TenantCluster` placement, also called remote placement.
The hub, catalog and family adapters are the same as for in-Project services; the engine runs on a tenant's Managed Cluster that the hub registers as a data plane.
Tenant procedures are in [Run a service on your own cluster](/cloud/managed-services-tenant-cluster).

## How it works

| Component | Role |
|---|---|
| `ServiceDataPlaneGrant` | A Project admin's consent, in the Project namespace, to register one `KdcCluster` incarnation (name and UID), with a family ceiling and the disclosure |
| Opt-in annotations | `services.kube-dc.com/data-plane: enabled` and `services.kube-dc.com/families` on the `KdcCluster`. The registrar reads them and never writes the cluster |
| Registrar | A hub controller. It binds the cluster by UID (the CAPI Cluster, and the Kamaji `TenantControlPlane` that owns the admin kubeconfig Secret), creates the data plane and drives its registration phases |
| Generated `ServiceDataPlane` | Named `dp-<KdcCluster UID>`, with `spec.source`, `providerProfileRef`, isolation `TenantCluster`, connectivity `tenant-native` and `allowedProjects` set to the source Project only |
| Family delivery | The registrar stamps family labels on the CAPI Cluster; the family ClusterProfile installs the engine operator and the runner on the tenant cluster |
| `ServiceDisposition` | One record per placement on a tenant plane, in the hub namespace. It records what became of the instance and holds its name and capacity until the outcome is final |
| `ProviderProfile` | The qualified capabilities of the tenant clusters' infrastructure provider: storage budgets, egress probes, worker minimum and control-plane location |

The hub signs every revision, operation and deletion grant for the plane's audience `dp-<uid>/g<generation>`.
The runner pins the first generation it enrolls under and refuses work signed for an earlier one.

## Before you begin

Check these prerequisites:

- Managed services are enabled with a services release that includes the registrar. See [Enable managed services](managed-services-enable.md).
- Tenant Managed Clusters use Cluster API workers and a Kamaji control plane hosted on the management cluster.
- Each tenant cluster that will be registered has a `default/kubernetes` EndpointSlice. A data plane without one stays withheld from placement.
- The engine operator can run on tenant clusters. The tenant-cluster catalog component carries the RBAC it needs there.
- The backup object store has one HTTPS endpoint with a publicly trusted certificate, reachable from tenant workers.
- The hub's credential for the tenant cluster can impersonate the runner ServiceAccount. Restore-to-new depends on it; the Kamaji admin credential can.

## Enable remote placement

### 1. Publish a provider profile

The registrar sets a generated plane's `providerProfileRef` from the CAPI Cluster label `kube-dc.com/infrastructure-provider`.
A plane whose profile does not exist stays `Bootstrapping` with reason `ProviderProfileMissing`.

:::warning Run one ProviderProfile per installation
Project admins can write labels on their CAPI Clusters, including `kube-dc.com/infrastructure-provider`.
With a second, less restrictive profile on the same hub, a tenant could select that profile for their cluster.
Keep exactly one `ProviderProfile` for tenant clusters on each installation.
:::

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ProviderProfile
metadata:
  name: <provider>
spec:
  provider: <provider>
  supportState: Current
  kubernetesAccess:
    type: KamajiAdminSvc
    capiProvider: <provider>
  storageClasses:
  - name: <storage-class>
    snapshot: false
    expansion: true
    topologyAware: false
    performanceClass: Standard
    planeStorageBudget: 256Gi
  loadBalancer:
    type: None
    qualified: false
  defaultEgressProbeURLs:
  - https://<image-registry>/v2/
  controlPlane: Hosted
  qualifiedFamilies: [postgresql]
  minWorkerCPU: "3"
  minWorkerMemory: 6Gi
```

| Field | Effect |
|---|---|
| `provider` | Must equal the value of `kube-dc.com/infrastructure-provider` on the tenant clusters' CAPI Clusters |
| `storageClasses[].planeStorageBudget` | Copied into every generated plane's `spec.storageBudgets`. Without any budget the capacity ledger treats the plane's storage as unknown and refuses every revision that grows. Once one class has a budget, a class without one has a budget of zero |
| `defaultEgressProbeURLs` | Egress every instance needs, such as image pulls. Do not list the object store here: it has its own probe, and a failure here takes the whole plane out of service |
| `controlPlane: Hosted` | The API servers run outside the data plane, so the hub attests backup admission instead of the runner. Required for destructive deletion and `RestoreToNew` on Kamaji-hosted planes |
| `qualifiedFamilies` | Records the families with completed drills on this provider. It is qualification metadata: the controllers do not enforce it. A plane's families are the cluster's annotated families, intersected with the grant's families and the cell's family bundles, so publish tenant plans only for qualified families |
| `minWorkerCPU`, `minWorkerMemory` | Minimum average allocatable per worker, checked by `WorkerShapeQualified`. Allocatable sits below the VM size |

Profile edits reach existing planes on the registrar's next periodic pass, within about five minutes.

### 2. Publish tenant-cluster plans

Tenant-cluster plans are separate from in-Project plans. They need:

- `minIsolation: TenantCluster`, `allowedPlacementModes: [TenantCluster]` and `allowedConnectivityClasses: [tenant-native]`;
- `backup.enabled: true` with `objectStoreSource: ProjectBucketClaim` and an HTTPS `objectStoreEndpoint`. The catalog refuses a non-HTTPS endpoint on such a plan;
- `requiredCapabilities: [projectObjectStore]`. A plane advertises it only while its object-store probe passes;
- for PostgreSQL, the annotation `services.kube-dc.com/postgresql-monitoring: none`, because tenant clusters carry no Prometheus Operator. These plans promise no platform metrics or alerts;
- no public load balancer or Gateway entitlement and no `ManagedSecret` delivery entitlement;
- a `support.responsibilityText` that states the tenant's responsibility for node capacity, cluster health and the cluster's network.

Use one object-store endpoint for all tenant-cluster plans of a cell. A plane whose families resolve to tenant-cluster plans with more than one endpoint withholds `projectObjectStore` with reason `ObjectStoreEndpointsConflict`.
List `RestoreToNew`, `SnapshotAndDelete` and `Delete` only after the plane reports `BackupAdmissionReady=True`. Offer `ExpandStorage` only if the provider's storage class expands mounted volumes online.
See [Publish the catalog](managed-services-catalog.md) for revisions and adoption.

### 3. Select the catalog components

In the Fleet catalog Kustomization, add the tenant-cluster component for the provider, for example `components/tenant-cluster-cloudsigma`. It carries the provider profile, the tenant plans and the engine operator pins for tenant clusters.
On an installation without its own data plane, also select `components/legacy-plans-disabled` and set `SERVICES_RUNNER_SELF=false`.

### 4. Turn on the registrar

The registrar starts work only when its preflight holds: every family-selecting ClusterProfile uses `LeavePolicies`, and the `reserve-service-selection-labels` and `family-clusterprofile-deletion-protection` policies are bound with Deny.

1. Check the policies exist:

   ```bash
   kubectl get validatingadmissionpolicybindings | grep -E 'reserve-service-selection-labels|family-clusterprofile-deletion-protection'
   ```

2. Set `SERVICES_REGISTRAR=true` in `clusters/<name>/cluster-config.env` and push. Fleet renders it into the chart's `registrar.enabled`.
3. After the first opt-in, confirm the hub metric `kdc_services_registrar_preflight_ok` is `1`.
   When the preflight fails, the hub logs `registrar preflight does not hold` and the `KdcCluster` gets a `PreflightFailed` Event.

### 5. Qualify with a test cluster

Opt in and grant a disposable cluster as described in the tenant guide, then check its plane:

```bash
kubectl get servicedataplane -l services.kube-dc.com/source-namespace=<project>
kubectl get servicedataplane dp-<uid> \
  -o jsonpath='{.status.registration.phase}{"\n"}{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\n"}{end}'
```

Expect registration `Active`, `READY` true, the fitness conditions in [the condition table](#data-plane-conditions) `True`, and `projectObjectStore` among the capabilities.
Then create a service, bind an application in the cluster, take a backup, restore it, and delete services under each policy the plan offers.

## Revocation grace

Deleting the bound grant or removing the opt-in annotation moves an `Active` or `Suspended` plane to `Revoking` until a deadline.
A plane still in `Bootstrapping` has no grace: it retires at once.
New placements, dispatch and new operations stop; running instances keep running and backing up.
Before the deadline, the plane returns to `Active` at the same generation when the registrar sees, on uncached reads:

- the opt-in annotation on the same `KdcCluster` incarnation;
- exactly one grant that names that UID, is not terminating, has never served a generation, was created after the plane's `grantWatermark`, covers an annotated family and covers **every** family the plane currently serves;
- a binding equal to the recorded CAPI Cluster and `TenantControlPlane` UIDs;
- a passing registrar preflight.

Otherwise the deadline retires the plane even if the tenant created a grant.
At the deadline the plane retires and every instance is detached under `Retain`.

The grace is the chart value `registrar.revocationGrace` (a Go duration). Empty keeps the hub default of 72 hours, and `0s` retires at once.
Shorten it only on a qualification installation.

When a Project namespace starts terminating, the plane waits up to the hub's receipt window, 120 seconds by default, for deletion receipts from the cluster before it records unknown outcomes.

## Monitor registration

```bash
kubectl get servicedataplanegrants -A
kubectl get servicedataplane -l services.kube-dc.com/source-name \
  -o custom-columns='NAME:.metadata.name,PROJECT:.spec.source.namespace,CLUSTER:.spec.source.name,PHASE:.status.registration.phase,REASON:.status.registration.reason,GEN:.status.registration.generation'
```

| Phase | Meaning | Leaves when |
|---|---|---|
| `Bootstrapping` | Binding, enrolment and family installation; no placement | Ready: `Active`. Withdrawal or a changed incarnation: `RetiringHeld` |
| `Active` | Placement, dispatch and operations | Credential failure: `Suspended`. Withdrawal: `Revoking`. Incarnation change: `RetiringHeld` |
| `Suspended` | The admin kubeconfig is missing, unreadable or unreachable; retries only | The credential returns: previous phase |
| `Revoking` | Grace period after a withdrawal; `status.registration.revocation` holds the cause and deadline | Reinstatement: `Active`. Deadline: `RetiringHeld` |
| `RetiringHeld` | Instances are detached under `Retain` | Every disposition leaves `Active`: `Retired`. Loss boundary: `PlaneLost` |
| `Retired` | No disposition is `Active`. The hub releases runner trust and the guard from the cluster asynchronously; see `remoteRelease` below | See [Register a cluster again](#register-a-cluster-again) |
| `PlaneLost` | The cluster was deleted, its Project ended without receipts, or an operator declared it lost | See [Register a cluster again](#register-a-cluster-again) |

Registration reasons include `RegistrationAwaitingGrant` (opted in without a grant), `BindingUnverifiable`, `ProviderProfileMissing`, `SourceDeleting`, `ProjectDeleting`, `Reinstated`, `DeclaredLost` and `RetirementStalled` (dispositions still `Active` 24 hours into retirement).

`status.registration.remoteRelease.state` tracks removal of the trust ConfigMap and runner-state guard from the cluster: `Pending` retries with back-off and ends `Released`, `Superseded`, `IncarnationGone` or `Abandoned`.
The grant carries a copy in `status.remoteRelease`, so the tenant sees which objects remain.

A retired or lost plane keeps its family selection, so the engine operator and an idle runner stay installed on the cluster. The tenant removes them if wanted.
A `Pending` release retries indefinitely until it succeeds or an operator abandons it; check it before telling a tenant that the cluster is clean.

### Register a cluster again

A `Retired` or `PlaneLost` plane starts its next registration generation in `Bootstrapping` when all of these hold:

- the `KdcCluster` is the plane's original incarnation (`spec.source.uid`), carries the opt-in annotation with at least one family, is not deleting and has Cluster API enabled;
- exactly one grant names that UID, is not terminating, has never served a generation, was created after the plane's `grantWatermark` and covers an annotated family;
- the annotated families, the grant's families and the cell's family bundles have at least one family in common;
- the registrar preflight holds;
- no `ServiceDeployment` of the plane remains;
- the plane is not being deleted, its Project is not terminating, and `spec.declareLost` is empty.

A `PlaneLost` plane additionally needs a terminal remote release (not `NotStarted` or `Pending`) and no unresolved disposition.
To register a cluster that you declared lost, clear `spec.declareLost` after its dispositions are resolved.
A `Retired` plane can advance while instances of the earlier generation are still retained; those instances are not taken back under management.
The new generation reuses the components already installed on the cluster, but it binds afresh and passes the readiness checks again.

### Data plane conditions

| Condition | What it reports |
|---|---|
| `ConnectorHealthy` | The hub reaches the cluster with the bound kubeconfig |
| `Enrolled`, `RunnerStateProtected` | Runner trust and the runner-state guard are installed. After retirement the guard reports `InstallGated` |
| `BundleReady`, `AdapterCoverage` | The runner version and its compiled adapters match the family bundle |
| `EgressReady`, `CapabilitiesDiscovered` | The egress probes passed and capabilities were discovered |
| `WorkerShapeQualified` | Workers meet the profile minimum |
| `InClusterAPIHealthy` | Every address of the `default/kubernetes` EndpointSlice answers TLS. A failure withholds placement |
| `RunnerHeartbeatFresh` | The runner reported recently. A failure withholds placement |
| `NodesReady` | Lists NotReady nodes. Reported only; capacity already counts only Ready nodes |
| `FamilyAdmissionReady` | A dry-run create of the engine resource reaches the operator's webhooks. A failure withholds that family |
| `ObjectStoreReachable` | The runner reaches the object-store endpoint. A failure withdraws `projectObjectStore` only |
| `BackupAdmissionReady` | The hub's backup-admission topology of the hosted control plane is active |
| `CapacityInventoryReady` | The capacity ledger has an inventory of the plane |
| `AcceptingPlacements` | `False` with `RegistrationNotActive` outside the `Active` phase |
| `RemoteReleasePending` | The release of runner state from the cluster is still retrying |
| `PlaneDeletion` | Reason `DeletionHeld` names what blocks deletion of the plane object |

Instances on a failing plane report `Ready=False` with `DataPlaneDegraded`; precedence is `DataPlaneRetired`, then `DataPlaneRevoking`, then `DataPlaneDegraded`.
Losing the object store gives `BackupReady=False` with `BackupStoreUnreachable`.

## Dispositions

Every placement on a tenant plane has a `ServiceDisposition` in the hub namespace (`kube-dc-services` by default), named `disp-<hash>` and labelled with the project, service and placement:

```bash
kubectl get servicedispositions -n kube-dc-services
```

| Outcome | Meaning | Name, plan slot and capacity |
|---|---|---|
| `Active` | Placed and running, or about to be dispatched | Held |
| `Withdrawn` | Never dispatched, or cancelled before any apply | Released |
| `Destroyed` | Verified destructive deletion, or the instance namespace is absent on the bound cluster | Released |
| `SourceDestroyed` | The bound CAPI Cluster and `TenantControlPlane` are both gone | Released |
| `Retained` | Detached under `Retain`; the instance may still run in the cluster | Held |
| `RemoteOutcomeUnknown` | The plane was lost before a deletion receipt | Held |

A `Retained` or `RemoteOutcomeUnknown` disposition without a resolution is *unresolved*.
While one exists, placement of a service with the same Project and name is refused with `PredecessorUnresolved`, a restore from that source is refused, the disposition counts against the plan's per-Project limit, the plane object cannot be deleted, and a `PlaneLost` plane cannot start a new generation.
`Retained` becomes `SourceDestroyed` when the cluster is deleted.
It becomes `Destroyed` only when the hub itself verifies that the instance namespace is gone, reading the cluster through a reachable connection whose binding matches the one the disposition was committed under.
A namespace the tenant deleted on an unreachable, recreated or rebound cluster does not count.

A disposition becomes eligible for collection 365 days after its terminal outcome (`status.outcomeAt`) or its resolution (`status.resolution.at`), and is kept while its placement still exists or its reservation is still charged.
A collectable plane is deleted 365 days after `status.registration.terminalAt`.

### Retained namespace release

Before a tenant can delete a retained instance namespace, the hub must release the namespace's backup guard.
After a `Retain` detach, it does so once it has read the deletion receipt and recorded the instance's closed backup publication.
While a receipt is unreadable or a step is still running, the release only waits and retries.

Some cases never release. The hub then records a `NamespaceReleaseSkipped` Warning Event on the disposition, and the namespace stays guarded:

- the deletion ran under `Delete` or `SnapshotAndDelete`, but the runner retained the instance instead; the release applies only to a `Retain` detach;
- the runner's fence for the `Retain` detach is gone from the cluster;
- the receipt names no backup publication that the plane holds closed;
- an unacknowledged final backup of the closed publication expired;
- a backup of the closed publication was never acknowledged and its source `ManagedService` is gone;
- a restore credential in the retained namespace is still `Active`.

```bash
kubectl get events -n kube-dc-services --field-selector reason=NamespaceReleaseSkipped
```

For these cases, agree the outcome with the tenant and record a resolution as described below.


## Resolve dispositions

Resolve a disposition only when the tenant's cluster cannot report the outcome itself, for example when it is unreadable but still exists.

| Resolution | Use when | Effect |
|---|---|---|
| `ConfirmedDestroyed` | You verified that the instance and its data no longer exist | Acts as `Destroyed` |
| `AcceptedUnresolved` | The instance may still exist, and you accept releasing the name and capacity anyway | Records a possibly live predecessor and releases the name and capacity |

Only identities in the chart's `admissionPolicy.operatorGroups` can write a resolution. It is set once and freezes the outcome:

```bash
kubectl patch servicedisposition <disposition> -n kube-dc-services --subresource=status --type merge -p \
  '{"status":{"resolution":{"kind":"ConfirmedDestroyed","actor":"<operator>","reason":"<evidence>","at":"<RFC3339 time>"}}}'
```

To end a live registration from the hub side, declare the plane lost.
The plane moves to `PlaneLost`, active dispositions become `RemoteOutcomeUnknown`, and the hub still releases its runner state if the cluster answers:

```bash
kubectl patch servicedataplane dp-<uid> --type merge -p '{"spec":{"declareLost":"<reason>"}}'
```

If the remote release can never complete, `spec.abandonRemoteRelease: <reason>` ends it as `Abandoned`. Both fields are reserved to the operator groups.
After `declareLost`, the instance namespace stays on a live cluster; resolve its disposition to free the name.

## Features not supported on tenant clusters

Credential inputs and runner execution profiles are not supported on tenant clusters.
Their admission checks run against the provider's own data plane, and placement does not stop such a service from landing on a tenant cluster on an installation that also has its own data plane.
Do not publish tenant-cluster plans for services that need them, and tell tenants not to use them there.

Credentials reach tenant planes only as Secrets in the tenant cluster: `OpenBao` delivery and dynamic database credentials are refused there, and tenant plans carry no `ManagedSecret` delivery entitlement.

## Security model

- **The tenant administers the data plane.** Cluster administrators can read every credential the hub delivers there, including the key of the Project's backup bucket, which can read, overwrite and delete every object in that bucket, including backups of other Project services that use it. They can stop backups and delete fences, guards and records. The grant's `disclosure` records the tenant's acknowledgement.
- **Consent and binding.** Only a Project admin can create a grant. The registrar binds a plane to the cluster's CAPI Cluster and `TenantControlPlane` UIDs, and the connector refuses a kubeconfig Secret not controlled by the bound control plane. A recreated cluster is never registered by an old grant.
- **Scoped instructions.** The hub signs work for the plane's audience and registration generation; the runner refuses other audiences, earlier generations and revisions for detached instances.
- **Reserved labels.** `reserve-service-selection-labels` keeps tenants from adding family selection labels to their clusters. `servicedataplane-registration-protection` reserves `spec.source`, `declareLost` and `abandonRemoteRelease`, and `servicedisposition-protection` keeps disposition specs immutable and resolutions operator-only. The CAPI label that selects the `ProviderProfile` is not reserved; see the warning under [Publish a provider profile](#1-publish-a-provider-profile).
- **Destructive effects need an active binding.** Deleting an instance requires an `Active` plane, a reachable cluster under a valid binding and a signed deletion grant. Every other phase only fences, holds and records.
- **What leaves the management cluster.** The Project bucket credential, the control-plane addresses in the backup-admission record and the runner trust bundle are copied into the tenant cluster.
- **Observed values come from the tenant's cluster.** The runner on a tenant plane runs under the tenant's control, so status values it reports, such as versions, endpoints and messages, are tenant-supplied. The managed-services API leaves condition messages out of its data-plane listing for this reason; treat other status text of these services the same way.

## Limits

- One object-store endpoint per cell for tenant planes, with a publicly trusted certificate.
- `tenant-native` connectivity only; no public exposure from tenant clusters.
- No migration of a running service between in-Project and tenant-cluster placement.
- A restore from a tenant-cluster backup reads the recovery credential that the source cluster's runner holds, so the source plane must still exist and its cluster must be reachable. After the source cluster is deleted, its backups cannot be restored.
- Retained instances need manual recovery by the tenant; there is no automatic re-adoption.
- Tenant clusters must be `KdcCluster` resources with Cluster API workers and a hosted Kamaji control plane. Bring-your-own clusters are not supported.

## Related

- [Managed services architecture](managed-services-overview.md)
- [Enable managed services](managed-services-enable.md)
- [Publish the catalog](managed-services-catalog.md)
- [Operate managed services](managed-services-operations.md)
- [Security model](security-model.md)
