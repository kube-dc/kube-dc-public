# Operating managed services

Day-2 for the operator: what tenants wait on you for, what to check after a
release, and the procedures that change a running installation safely. Tenant
day-2 is in the cloud documentation, starting with
[Day-2 Operations](/cloud/postgresql-operations).

## Approvals

Operation types a plan lists in `operations.allowed` but not in
`operations.autoApprove` wait in phase `AwaitingApproval`. Only a member of
the operator groups may approve:

```sh
kubectl -n <project> get serviceoperations
kubectl -n <project> annotate serviceoperation <name> services.kube-dc.com/approved=true
```

Admission refuses the annotation from anyone else, including the tenant. The
platform admin console lists waiting operations and approves them with the
same effect. Operations a `ServiceCredentialPolicy` generates carry the
platform marker and can be cancelled or approved only by `system:masters`.

Other operator-only annotations on a `ManagedService`:

| Annotation | Effect |
|------------|--------|
| `services.kube-dc.com/adopt-catalog=<token>` | Move the instance to the current catalog revisions; see below |
| `services.kube-dc.com/accept-parameters=<generation>` | Accept a parameter snapshot the hub refused as drift; honoured for the current generation only, then removed |

Tenants may set `services.kube-dc.com/cancel: "true"` on an operation that
has not started, and `services.kube-dc.com/confirm-delete=<name>` on a
service with `deletionPolicy: Delete`.

## Reading status honestly

- `Ready` needs `Accepted`, `Placed`, `Reconciled`, `ConnectivityReady` (an
  active probe from the promised location) and `BackupReady` when the plan
  enables backups. A green pod is not Ready.
- `Stale=True` means the hub has not observed the data plane recently; the
  instance keeps running under its local operator.
- `Frozen=True` means dangerous mutation is blocked because of ownership
  drift; an operator repair or accept decision is required.
- `CatalogPinned=False` means the catalog changed after placement; the
  instance keeps its pinned revisions until you adopt the new catalog for it.
- `status.acceptedRevision` and `status.appliedRevision` differ while a change
  is in flight. Accepted is not applied.

## After a release

The hub, the runner and the catalog are one release. After moving the pins:

```sh
kubectl get servicedataplane <plane> \
  -o jsonpath='{.status.ready}{"\n"}{range .status.bundles[*]}{.family}{"\t"}{.ready}{"\t"}{.message}{"\n"}{end}'
```

Bundle readiness is an AND across families: one bad pin refuses placements for
every family. `rollout pending` is the ordinary window between the runner
rolling out and the catalog applying; `does not pin a runner version` means
`SERVICES_RUNNER_TAG` rendered empty. When moving a runner by hand, change the
bundle's `spec.runner.image` and `spec.runner.version` together; the readiness
gate compares the version label the runner reports with the bundle.

Then create one service from a Development plan, bind it, and delete it.

## Catalog adoption

A changed class, plan or adapter never rebuilds a placed instance on its own.
You move it explicitly, in the tenant's maintenance window:

```sh
kubectl -n <project> get msvc <name> -o jsonpath='{range .status.conditions[?(@.type=="CatalogPinned")]}{.message}{"\n"}{end}'
kubectl -n <project> annotate msvc <name> services.kube-dc.com/adopt-catalog=<token>
```

The token is `<classRevision>/<planRevision>/<8-hex digest of the pin set>`,
printed in the `CatalogPinned` condition. It names exactly one target, is
consumed once, and any later pin change needs a new token. The hub signs a
new revision from the current catalog, the runner applies it, and
`CatalogPinned` returns to `True`. A change of the connectivity class cannot
be adopted; it needs a new placement.

The order for an adapter (family code) upgrade that never breaks a running
instance: roll the runner bundle, bump the class's adapter version, blueprint
digest and revision, wait for `Verified`, then adopt instance by instance.
Instances not adopted keep working on their pins indefinitely.

## Backups and restore

Backups land in the Project's `db-backups` bucket (or the plane store the plan
names). `ServiceBackup` records are the tenant's catalog and survive source
deletion; `status.recovery.retainUntil` bounds how long the archive is kept.
Restore into a new service reads only backups taken on the target's own data
plane: **cross-plane restore and instance migration are not supported in this
release.** A drain therefore ends with the tenant recreating the service
elsewhere from an export they hold, or with the plane staying up.

PostgreSQL restore in place preserves the service identity and replaces the
engine; it needs the tenant's explicit `engineUID`, data-loss
acknowledgement and `deletionProtection: false`. Deleting a Project deletes
its in-Project instances with their data and takes no final backup, whatever
the deletion policy says; a `ProjectDeleted` event records it.

## Data planes

```sh
kubectl get sdp                       # READY, runner version, egress self-check
kubectl get sdp <name> -o yaml        # capabilities, bundles, inventory
```

`spec.lifecycle` moves a plane through `Paused`, `Draining` (refuses new
placements) and `Decommissioning` (detaches every deployment with `Retain`;
data stays). Delete or retain the instances first and wait for their deletion
receipts: a destructive deletion policy is not released while the
`ServiceDataPlane` object is missing.

Retiring a family from a plane removes nothing by itself. Inventory every
instance of the family and decide its fate, remove the family from
`spec.families`, remove the runner and the operator only where this cell
installed them and no retained engine still depends on them, and delete the
`ServiceFamilyBundle` last; its finalizer holds the deletion while anything
still references it. Never delete a generated `kube-dc-service-family-*`
ClusterProfile directly.

## Evidence, usage and audit

```sh
kubectl -n kube-dc-services get serviceevidences        # immutable, by digest
kubectl -n <project> get msvc <name> -o jsonpath='{.status.usage}'
```

Evidence is removed after its retention (90 days; 365 for deletions): export
what must outlive that. Usage totals come from sequenced runner batches; gaps
are counted, never guessed. The API server audit policy records every Secret
request and every write to the managed-services resources at metadata level;
never pass a credential as an exec argument, since request URIs are recorded.

## The tenant console

Tenants see managed services for every organization unless the cluster sets
`KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false` and lists
organizations, and they see the deprecated db-manager Databases area only
where the cluster lists them (`KUBE_DC_UI_LEGACY_DATABASES_*`). The console's
metrics route reads the platform Prometheus for the instance namespace;
`PROM_URL` on the backend must point at it for the Overview tiles to fill.

## Related

- [Managed services overview](managed-services-overview.md)
- [Publishing the catalog](managed-services-catalog.md)
- [Retiring db-manager](managed-services-retire-db-manager.md)
