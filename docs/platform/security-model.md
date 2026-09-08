# Security Model

Kube-DC combines identity, Kubernetes authorization, admission policy, and
Kube-OVN routing controls. No single layer is a complete tenant boundary, and
platform administrators remain privileged across Organizations.

This page describes the controls applied to **Projects** on the management
cluster. A **Managed Cluster** has its own Kubernetes API, RBAC, and workload
security configuration.

GPU workloads add privileged node components and deployment-specific isolation
assumptions. See the [GPU security threat model](gpu-threat-model.md) before
enabling a GPU profile.

## Control layers

| Layer | Protects | Scope |
|---|---|---|
| OIDC and Keycloak | User identity and group claims | Platform and Organization realms |
| Kubernetes RBAC | Which resources a user can read or change | Organization namespaces and Project backing namespaces |
| ValidatingAdmissionPolicy | Dangerous Pod fields, exec/attach, protected annotations, quota, and selected accelerator rules | Resources selected by each policy or binding |
| Kube-OVN VPCs | Primary Project network separation | One VPC and workload subnet per Project |
| Router-policy isolation | Traffic on shared cloud/public external networks | Project VPCs when ingress or egress isolation is enabled |
| Workload policy | Application-specific traffic rules | Optional operator-provided Kubernetes NetworkPolicy; default Project Roles cannot author it |

## Pod admission

Project backing namespaces carry the `kube-dc.com/project` label. The
`restrict-pod-security-in-projects` policy denies user-created or user-updated
Pods that request:

- `hostPath` volumes;
- privileged containers or init containers;
- `hostNetwork`;
- `hostPID`;
- `hostIPC`.

Trusted platform service-account namespaces, node identities, and
`system:masters` are excluded because controllers such as KubeVirt and Kamaji
must create infrastructure Pods in Project backing namespaces. Those exclusions are
part of the platform trust boundary; they are not a user-configurable bypass.

Users can still run ordinary Pods, Deployments, StatefulSets, Jobs, and
DaemonSets, mount supported volume sources, and expose workloads through
Services or Gateway routes.

## Exec and attach

Pod exec and attach are unsupported in Projects. Current standard Role
templates omit `pods/exec` and `pods/attach`, and
`restrict-pod-exec-in-projects` denies CONNECT requests even if a custom or
stale Role grants the subresource.

Run administrative container tasks as a purpose-built Job that mounts the
required volume. Use the VM console or VNC for virtual machines. Platform
service accounts, nodes, and `system:masters` are exempt and must be protected
accordingly.

## Protected annotations

The `protect-kube-dc-resource-annotations` policy prevents non-platform users
from changing annotations on `Organization`, `Project`, and
`OrganizationGroup` resources during UPDATE. Users can still perform the
specification changes allowed by their RBAC.

Cluster administrators can set controller-consumed annotations. For example:

```bash
kubectl -n <organization> annotate project <project> \
  network.kube-dc.com/egress-allowlist="10.8.0.0/24" \
  --overwrite
```

Treat annotations as privileged configuration: admission protects changes, but
does not validate the business reason for an allowlist entry.

## External-network isolation

Project VPC separation is the primary east-west boundary. Optional ingress and
egress router policies add protection when multiple tenants share an external
cloud or public subnet.

### Egress isolation

When `egress_network_isolation` is enabled, Kube-DC drops Project traffic whose
**destination** is another address on a configured external subnet, except for:

- the external subnet gateway, which is needed for SNAT and internet access;
- external addresses owned by the Project;
- the global egress allowlist;
- the Project's `network.kube-dc.com/egress-allowlist` entries.

This control does **not** block all outbound traffic. Internet destinations do
not match the external-subnet drop rule and continue through the Project's SNAT
path.

### Ingress isolation

When `ingress_network_isolation` is enabled, Kube-DC drops traffic whose
**source** is a configured external subnet, except for:

- the external subnet gateway, needed for return traffic;
- the global ingress allowlist;
- the Project's `network.kube-dc.com/ingress-allowlist` entries.

Ingress allowlists describe trusted source addresses. Unlike egress rules, they
are not populated from Project EIPs automatically.

### Platform configuration

The master configuration holds cluster-wide switches and allowlists:

```json
{
  "egress_network_isolation": true,
  "egress_global_allowlist": ["10.8.0.0/24"],
  "ingress_network_isolation": true,
  "ingress_global_allowlist": ["192.0.2.10"]
}
```

Project-specific values are comma-separated IP addresses or CIDRs:

```bash
kubectl -n <organization> annotate project <project> \
  network.kube-dc.com/ingress-allowlist="192.0.2.10,198.51.100.0/28" \
  network.kube-dc.com/egress-allowlist="10.8.0.0/24" \
  --overwrite
```

Changes apply when the Project reconciles. Validate the resulting behavior from
both directions; an allowlist does not replace application authentication,
TLS, or a workload NetworkPolicy.

## Project RBAC

Kube-DC creates four standard Roles in every Project backing namespace:

| Role | Intended access |
|---|---|
| `admin` | Broad lifecycle access to supported Project resources plus namespaced Roles and RoleBindings; quota is read-only |
| `project-manager` | Read and monitor Project resources and use the VM console; update or patch existing managed secrets, certificates, and database credential policies; create, update, or patch KMS keys; no Project, membership, or quota administration |
| `developer` | Manage supported workloads, Services, VMs, and managed services; raw Kubernetes Secrets are get/list only |
| `user` | Read-only Project visibility without raw Secret or VM-console access |

The exact rules come from the platform's default Role templates. Review live
rules rather than inferring privileges from the role name:

```bash
kubectl -n <backing-namespace> get role \
  admin developer project-manager user -o yaml
```

Namespaced RBAC does not grant access to cluster-scoped resources. Organization
Groups create RoleBindings only for the selected Projects. See
[Multi-tenancy and access control](architecture-multi-tenancy.md).

## Operating admission policy

Admission policy and bindings are installed through the Fleet GitOps path.
Inspect them with:

```bash
kubectl get validatingadmissionpolicy
kubectl get validatingadmissionpolicybinding
kubectl describe validatingadmissionpolicy restrict-pod-security-in-projects
```

Change exemptions or enforcement in the Fleet source, review the impact, and
let Flux reconcile it. Deleting a live binding creates an immediate enforcement
gap and Flux may recreate it; it is not a normal debugging procedure.

Policy denials appear in API responses and, when audit logging is enabled, in
the API-server audit log. Capture the denied request, user, policy name, and
message before changing policy.

## Managed services placed inside a Project

A managed database or message broker can run **in the tenant's own Project
namespace**. A project admin holds broad rights there, so the platform draws
the boundary at admission rather than at RBAC: every object the platform
manages carries `services.kube-dc.com/managed-by`, admission policies keep
tenant identities off marked objects, and the manager's webhooks cover what a
policy cannot see — a tenant object *aimed at* a managed engine.

What that means in practice for an operator:

- **The engine's own operation APIs are the platform's.** A CNPG `Backup` or
  `ScheduledBackup`, or a MariaDB backup/restore/SQL job, naming a managed
  engine is refused — created, changed or **deleted**: those run through the
  managed service's own operations, which carry approval, concurrency control
  and evidence, and deleting the schedule of a managed database would stop its
  backups silently. A tenant's own, unmanaged engine stays fully controllable.
- **A tenant database may not read a managed one.** A CNPG `Cluster` that
  bootstraps from a managed engine's backup, streams from it with
  `pg_basebackup`, replicates it, or subscribes to it is refused, as is a
  `Publication` or `Subscription` naming it: each is a way to act on the
  platform's engine without an operation. Restoring a managed database is an
  operation of that managed service.

  **What this check is worth, precisely.** It resolves the forms people write
  — the engine's Service names, a Service in the namespace by name or cluster
  IP, `ExternalName` aliases — and refuses those. It does **not** stop someone
  who is trying: a selectorless Service with a hand-written EndpointSlice
  pointing at the engine's pods, an `ExternalName` that leaves the cluster and
  resolves back, or any proxy the tenant runs, all reach the engine and are
  admitted. That is inherent once a tenant can choose arbitrary addresses in
  its own namespace, and it is accepted rather than papered over: what bounds
  the damage is the engine's own authorization and the platform's disk
  alerting. Treat the rule as protection against the wrong manifest, not
  against a determined tenant — the same-namespace trust model already says a
  project admin can reach its project's databases.
- **A tenant PodDisruptionBudget cannot cover managed pods.** Admission appends
  one `DoesNotExist` requirement per ownership marker to every tenant budget's
  selector, so it applies to that tenant's own pods and never to a platform
  engine's — `minAvailable: 100%` over engine pods would otherwise block the
  node drains maintenance needs. Selecting explicitly on the platform's or an
  engine operator's keys is refused outright; a null selector is left alone
  (in `policy/v1` it matches no pods).

  Two consequences worth knowing. A budget written **before** this shipped is
  not changed until something updates it — `hack/audit-managed-service-boundary.sh`
  lists those and `--apply` narrows them (a compare-and-swap patch; it exits
  non-zero if any budget could not be narrowed, and 2 if it could not audit at
  all). And a GitOps tool that compares live objects against its source sees
  the appended requirements as drift: the tenant-facing guidance is to write
  the two requirements into their manifests, with `ignoreDifferences` /
  `driftDetection.ignore` (or Argo's
  `ServerSideDiff=true,IncludeMutationWebhook=true` — both options, since
  server-side diff alone does not account for mutating webhooks) as the
  alternative. See [Scaling and performance](../cloud/scaling-performance.md).

  **Decided 2026-09-05: narrowing stays.** The alternative — REFUSING a budget
  that lacks the requirements — ends the *mutation* drift, because admission
  stops adding anything the source did not declare. It does not make desired
  and live state agree by itself: a rejected apply leaves the old object live
  and the new one unapplied, which is a different divergence and a louder one.
  Drift is noise; a broken apply is an incident. Revisit at a major version,
  and only when both are true: `hack/audit-managed-service-boundary.sh --apply`
  has been run fleet-wide (live objects), **and** tenant sources have been
  checked to carry the two requirements (a repaired live object does not fix
  the manifest that will overwrite it on the next sync).
- **Protection needs its enforcement.** `projectPolicies.protectManagedServices`
  requires `manager.webhook.enabled` and `Deny` in `validationActions`; the
  chart refuses the contradictory combinations rather than rendering an
  installation that looks protected.
- **Fail-closed admission requires two manager replicas.** With one replica,
  every rollout, drain or eviction is a window in which tenant pods, claims and
  Secrets cannot be created. Set `manager.replicaCount: 2` on two nodes, or
  state the trade-off explicitly with
  `manager.singleReplicaAdmissionAccepted: true` on a development or
  single-node installation.

### Which managed-service families are supported here

Protection is generic: the platform marks what it manages and the policies key
on that marker, whatever the engine. Four families ship today — `postgresql`,
`kafka`, `valkey`, `forgejo` — and all four are supported. Three things are
**not** generic, and they are what a new family has to be checked against:

- **The management-API route.** Where Tenant Networking v2 (`infraAttachment`)
  is enabled, a pod reaches the Kubernetes API only when the platform can prove
  what it is, and that proof walks a compiled list of engine roots — CNPG
  `Cluster` and `MariaDB` today. Everything else gets the plain lock.

  This is measured, not theoretical. On the production cluster (2026-09-05, all
  46 project namespaces opted in): an ordinary tenant pod lands in security
  group `infra-lock-<project>` and **cannot reach `kubernetes.default` at all**
  (connection times out; the internet is reachable), while the managed CNPG
  engine's pod lands in `infra-lock-<project>-api-client` with an extra route.

  So a family whose pods consume the Kubernetes API — Strimzi's user operator,
  which the Kafka family deploys; a Spark driver creating executors; MySQL under
  Oracle's operator — **will not work on such a cluster** until its engine root
  is in that list.
- **Operation CRs and reserved selector keys.** Each engine's own operation
  API is named explicitly in the reference gate (CNPG's `Backup`,
  `ScheduledBackup`, `Publication`, `Subscription`; MariaDB's backup, restore
  and SQL jobs), as are the selector keys reserved against tenant identities
  (`cnpg.io/*`, `strimzi.io/*`). A family with its own operation CRs or labels
  needs them added, or those objects are ungated.
- **Quota attribution.** The same classifier splits a Project's CPU into
  workload and platform overhead; an unclassified engine's pods count as tenant
  workload.

- **Retained-engine identity.** When an instance is retained, its ManagedService
  is gone but its pods must still be classifiable — the runner writes an
  attestation the platform reads from a fixed namespace, and the same engine
  roots decide what it proves. A family that is retained on a
  Tenant-Networking-v2 cluster needs the same entry as above.

**Families are registered per cluster, not compiled in.** From chart v0.6.34
the manager loads a service-family registry rendered from
`serviceFamilies.extra` in the chart's values: which engine kinds a pod's owner
chain may lead to (and through which controller kinds), which operator selector
keys are reserved, and which operation CRs are gated by their target. CNPG and
MariaDB are the built-in defaults; adding a family is a values change that
rolls the manager, not a kube-dc release. Trust is unchanged — every hop is
still UID-verified and the root still has to carry the platform's marker with
a ManagedService (or a retained-engine attestation) behind it; the registry
only says what a chain may look like, and it says so per root: the hops a
chain walked must all be in the via list of the root it reaches. The
registry is validated at load (strict decoding, unique roots and operations,
every via kind guarded by the owner-forgery policy by its resource, label
references reserved), and a manager whose configured registry file is
missing or invalid does not start. An operator that creates the children of
a via kind itself (Strimzi's pod sets) is admitted under that kind by an
explicit flag, never under Deployments or ReplicaSets, whose children are
kube-system's. Every identifier and every semantic rule in a family entry
is validated by the chart before a policy renders and by the manager when
it loads the file; a family may not claim the platform's own kinds as roots
or hops. Install the operator before registering the family: at startup
the manager checks each declared plural against API discovery and refuses
to start on one the server does not serve as declared; the check repeats
every minute, and a hop whose mapping stops matching is walked by nobody
until it matches again. A family also declares its whole API surface: the
entire group/version of its operations is routed to the reference gate,
which admits the resources the family lists as passthrough and refuses the
rest to tenants — so an operation renamed by an operator upgrade is
unknown, not unguarded, and a new API version of it is refused until the
registry names it. The family's operator ServiceAccounts are trusted inside
those groups only at the reference gate; their namespace is, like
`cnpg-system`, an exempt platform identity for the marker policies and the
child mutator, derived from the registry so that registering a family is
the whole of the configuration. Two kinds of trusted owner edge: the
platform's own kinds and apps/batch workloads through *controller*
references only (a tenant's garbage-collection reference to their own
Deployment is theirs), the engine kinds — CNPG Cluster, MariaDB, every
registered family's roots and custom via kinds — through *any* reference,
because Strimzi owns its pod sets through a non-controller reference to the
node pool and a tenant has no business referencing a platform engine as an
owner at all; the classifier follows the same edges. A managed CNPG Cluster's *status* is written by its own
instance and nobody else: the managed-services policy excludes that
subresource and the reference gate judges it with UID-verified provenance
(the instance ServiceAccount marked and owned by the Cluster, the token
bound to a pod the Cluster owns), because a name alone is something a
tenant can pre-own. The judge answers on its own webhook path so that an
older manager pod, during a rolling upgrade, refuses rather than waves the
write through; the policy stays unchanged in the chart that introduces the
stanza and excludes the subresource only in the next one, once every
cluster runs the stanza — the two are separate objects the API server
caches independently, and a relaxation observed before the stanza would
admit the write unjudged. CNPG does not adopt a pre-existing ServiceAccount, so a
tenant may not create one under a name the platform has claimed for an
engine; an account created before the claim leaves that engine's instance
refused with the reason named, and it is the platform's job to detect the
collision before building the engine. The reference gate refuses a tenant
object that names no engine at all unless the family declared such an object
inert, and refuses a resource it has no rule for instead of guessing. The
one surface still written per family in CEL is the per-instance
ServiceAccount authorization, which only an operator that runs instances
under a per-instance account needs.

**The supported boundary is therefore a conjunction, not one test.** A family
ships from kube-dc-services alone only if **all** of these hold (clauses 1, 3
and 4 through a registry entry; clause 2 through a registry entry unless the
operator needs per-instance authorization):

1. its engine pods do not need the Kubernetes API *on clusters where
   `infraAttachment` is enabled* (they may anywhere else);
2. it brings no operation CRs of its own that act on the engine — or they are
   already named in the reference gate;
3. it introduces no selector keys that must be reserved against tenants;
4. it needs no retained-engine classification on a Tenant-Networking-v2
   cluster.

Of the four shipped families, **PostgreSQL is the one proven on a
Tenant-Networking-v2 cluster** — CNPG is named explicitly, so it satisfies all
four clauses. `kafka`, `valkey` and `forgejo` satisfy (2), (3) and (4) but are
**not** in the engine-root list, so clause (1) is open for any of their pods
that need the Kubernetes API: qualify a family on such a cluster before
offering it there. Anything that fails a clause is a joint kube-dc +
kube-dc-services release, not a kube-dc-services-only change. The design that
would remove the coupling is in
`docs/prd/service-family-integration-contract.md`; it is deliberately not built
until a family needs it.

**Placement:** a managed service runs either in the tenant's Project namespace
(shared, protected as described above) or on a **separate data plane**. Private
per-instance namespaces inside a Project are not implemented — a sibling
namespace does not inherit the Project's networking, quota accounting or
identity, and adding a label does not create them.

What it does **not** mean: the model protects integrity, not confidentiality.
A project admin can read and mount the engine's Secrets, including its CA
material, and the backup object store is the Project's own bucket. That is the
same-namespace trust model working as designed — a tenant who can create
workloads in a namespace can read what those workloads read. Use a separate
data plane when a tenant must not be able to read its engine's credentials.

**An operator that runs inside the Project.** Some operators (Strimzi's
entity operator) run in the project namespace under an account they create
per instance. The policies see that account as a tenant. A registered
family therefore declares the account's shape and the few writes it must
make — the status of the family's operation objects, the Secrets it
derives from them — and the manager admits exactly those, on a webhook
path of its own, with the same UID-verified provenance as a managed
PostgreSQL's status write: the account marked and owned by the marked
root, the token bound to a pod the classifier recognises as that
instance's, the object marked with the instance. The policies hand those
writes to the webhook only in the chart after the one that introduces the
stanza, once every cluster serves it. All of these proofs read the object
as it is now, so a marker is only ever stamped when the platform creates
an object and is never added to one that already exists: the policy
`protect-managed-service-marker-introduction-in-projects` refuses that
transition on UPDATE, on every resource and subresource, for every
identity except cluster admins — no service account is exempt, the
platform's own included. An operator whose reconcile meets a name a tenant
pre-created fails on it instead of adopting it, and the object's UID — and
any token minted against it — never becomes the platform's. Databases that
predate markers are migrated below that chart with
`hack/migrate-managed-db-markers`, which recreates every authority-bearing
child under new UIDs rather than labelling what exists.

### Continuous enforcement is the invariant

Every trust decision above that reads an owner reference or an ownership
marker — a pod's chain to a managed engine through controller references,
the marked root itself, a registered family's non-controller edge — rests
on one platform invariant: **the owner-forgery guard
(`reserve-platform-identities-in-projects`), the marker policy
(`protect-managed-service-markers-in-projects`) and their `Deny` bindings
are continuously enforced, for CREATE and UPDATE, on every write-serving
apiserver and in every project namespace.** A tenant object admitted while
that was not true can carry a marker or an owner reference the platform
never granted, and nothing in the classifier can tell it apart later. The
manager does not prove this invariant; it checks it. Every minute each
manager replica dry-run creates, as a fixed synthetic tenant
(`owner-guard-probe@kube-dc.internal`) in the platform-owned canary
namespace `kube-dc-owner-guard-canary`, a plain Deployment that must be
admitted, one with a non-controller owner reference to a managed engine
kind that the guard must refuse, one carrying a marker key that the marker
policy or the object-protection policy must refuse (both deny it, and the
apiserver names only the first binding that denied, in no stable order),
and a marker stamped onto the chart's unmarked zero-replica target
Deployment in the canary, refused the same way (the UPDATE path); the non-controller edge is climbed only for
objects created after the *owner-guard epoch*, the server-set creation time
of an immutable ConfigMap `owner-guard-epoch` in the manager's namespace
plus ten minutes, recorded the first time the probes pass. A lapse the
probes observe retires that epoch (a later one is recorded when enforcement
is observed again); an inconclusive tick withholds the edge. What the probes
cannot see: a lapse between two ticks, one overlapping an inconclusive tick,
or one while no manager runs.

Operate accordingly. Policy maintenance — rolling the chart back below the
guard's any-edge generation, editing either policy or its binding, or
disabling `projectPolicies.reservePlatformIdentities` — follows this order:

1. Set `manager.nonControllerOwnerEdges: off` and wait for
   `kubectl -n kube-dc rollout status deployment/kube-dc-manager` to report
   the Deployment rolled out. The same upgrade installs the *off fence*, a
   ValidatingAdmissionPolicy (`owner-guard-off-fence`) that refuses any
   manager pod created in the manager's namespace without the literal knob
   in its `manager` container, whoever creates it — leader election is
   unfenced, so an old controller-manager leader could otherwise still
   create an attested pod after every observation the off manager can
   make. An off manager retires the epoch record every minute (its first
   tick must succeed or it exits, so a completed rollout means retired).
   The maintenance boundary, the ConfigMap `owner-guard-boundary` in the
   manager's namespace, is a *lease*: each off replica acknowledges it
   (`ack.<pod UID>`, a timestamp) only on a tick that observed everything
   holding — the Deployment satisfying the controller's own
   DeploymentComplete condition (at least as strict on replica counts as
   `rollout status`), none of its ReplicaSets desiring, holding or still
   able to create a pod without the knob (their controllers have observed
   the generation that zeroed them), no manager pod without it listed (the
   knob read from the `manager` container alone), and the fence refusing an
   attested manager pod (a server-side dry run) — and only after all of
   that has held on that replica for a full ten-minute settle since the
   fence was first observed refusing (one apiserver's denial says nothing
   about another's policy cache). Any break, any inconclusive tick, a
   restart, all start that replica's window over and drop its
   acknowledgement; if the drop itself cannot be written, the
   acknowledgement expires. A replica that sees every listed manager pod
   acknowledged within the last three minutes marks the boundary
   `complete`.
2. **Before weakening either policy, run
   `hack/owner-guard-boundary-check.sh` and require READY.** It is one
   stateful operation with two samples ninety seconds apart (longer than a
   tick plus jitter): each sample is bracketed by two reads of the manager
   Deployment that must agree on UID, generation and revision, with the
   rollout status pinned to that revision, so every accepted sample held
   at a single point in time; in each sample it checks that the
   boundary's `stamp` equals that sample's manager Deployment UID and
   rollout revision, that `complete` and every manager pod's
   acknowledgement are within three minutes and every manager pod runs
   off; and — the part two separate checks cannot give — that between the
   samples the Deployment (UID, generation, revision), the boundary object,
   its stamp and the manager pod set are unchanged while `complete` (a new
   aggregate write) and every pod's acknowledgement are strictly newer:
   renewal, not survival (an acknowledgement a replica could no longer drop
   would otherwise pass twice). An off manager
   deletes a boundary of any other stamp (a previous maintenance's, or a
   previous incarnation of the Deployment). Expect READY no earlier than
   ten minutes after the rollout completed. Never force-delete a manager
   pod, and never delete a Node object whose death is unproven, during
   this procedure: both remove the Pod object before the process has
   stopped, and the quiescence check would then miss a live attested
   writer — if either happened, abort until the process or its node is
   proven stopped (fence the node first).
3. Do the maintenance, restore the policies, confirm they are applied.
4. Clear the knob. The same upgrade removes the fence; attested pods that
   Helm rolls a few seconds before that deletion lands are refused and
   then retried by their ReplicaSet. An attested manager that finds the
   boundary retires whatever record stands behind it, removes the boundary
   and only then records anew — on every tick, its first included — so no
   epoch from before or during the maintenance is ever adopted, not even
   one written by a replica that overlapped the rollout.

The ten-minute settle is the bound that must hold for policy-cache
convergence across apiservers plus clock skew; keep the control plane
NTP-synced. Two more invariants the reasoning uses: a namespace is a
project (carries `kube-dc.com/project`) for the whole tenant-writable life
of every object in it — a namespace must not become, or become again, a
project while it holds objects tenants created when the selector was
absent — and objects reach the store only through admission (a storage
restore that bypasses it is outside the claim). The synthetic probe user
`owner-guard-probe@kube-dc.internal` is issued by no authentication
provider and tenants cannot impersonate it (project roles carry no
`impersonate`); the canary admits nothing but dry runs from user
identities regardless. An unplanned lapse of either policy is an
incident, not a state the classifier detects: after it, run
`hack/audit-owner-edges.sh --apply` (registered custom-via objects with
non-controller owners are recreated by their operators) and
`hack/audit-managed-service-boundary.sh` (marked objects predating the
rules), and treat anything marked during the lapse as untrusted.

## Boundaries and residual risk

- Platform controllers and `system:masters` can cross Project boundaries.
- Compromise of a trusted service account can bypass the admission exclusions
  granted to that account.
- Underlay VLAN attachments inherit the physical network's isolation model.
- Network isolation does not replace encryption, application authorization, or
  backup.
- A Project is not a separate Kubernetes cluster. Use a Managed Cluster when a
  tenant needs its own cluster-scoped administration boundary.
- Managed services placed in a Project namespace are protected for integrity,
  not confidentiality; their credentials and backup bucket are readable by that
  Project's administrators.
