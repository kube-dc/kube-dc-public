# Enable managed services on a Kube-DC installation

As a platform operator, use this procedure to install the managed services control plane and publish selected service plans.
New Kube-DC installations enable managed services when the installer has verified
expandable storage and an explicit capacity budget. Existing installations
enable them through their per-cluster Fleet overlay. The Fleet starter carries the services hub, the
catalog for PostgreSQL, Kafka, Valkey, MySQL, MariaDB, ClickHouse and Forgejo,
and their operator trees. Enable only the families that you qualify on the
target cluster.

## Before you begin

Check these prerequisites:

- A working Kube-DC installation with a Fleet repository and Flux.
- A compatible services release with reviewed hub, runner, and catalog pins.
- Storage roles, capacity budgets, and connectivity for the selected plans.
- The native operators required by the selected families.
- A reachable HTTPS backup endpoint for plans that require object storage.

## What an installation offers

The catalog defines the available services.
A family component supplies its class, bundle, and plans.
The provider selects components for each installation and publishes only qualified plans.
With all six database and messaging families selected, the catalog offers 12
Development and Production plans; Forgejo has its own plan.
See [Publish the catalog](managed-services-catalog.md).

Operations differ by family.
Every published Development and Production plan supports compute resize and
storage expansion within its bounds. Read the plan's `spec.operations.allowed`
and `status.tunables` for the supported inputs.
Valkey supports archive recovery on backup-enabled plans. Kafka exports metadata without message data.
Do not infer operation support from a plan's name or tier.

## Turn it on

### 1. Pins

In `clusters/<name>/cluster-config.env`. The release values come from the
starter's `bootstrap/release-pins.env`; the rest describe this cluster.

```text
SERVICES_CHART_VERSION=<services-release>
SERVICES_HUB_TAG=<hub-tag>
SERVICES_RUNNER_TAG=<runner-tag>
SERVICES_RUNNER_IMAGE=<runner-image>

# This cluster's identity as a services site.
SERVICES_CELL_ID=cell-<name>
SERVICES_RUNNER_SELF=true
SERVICES_DATAPLANE_NAME=<name>-platform

# What this cluster offers and what it may consume. There are no defaults:
# a cluster that has not decided should fail the render, not inherit
# somebody else's decision.
SERVICES_FAMILIES=[postgresql, valkey]
SERVICES_UNMANAGED_OPERATORS=[postgresql]
SERVICES_STORAGE_BUDGETS={rbd-vm: 100Gi}
SERVICES_STORAGE_ROLES={database: rbd-vm}
SERVICES_EGRESS_PROBE_URLS=[https://ghcr.io/v2/]

# PostgreSQL specifics, if you offer it.
SERVICES_PG_OPERATOR_VERSION=1.29.2
SERVICES_PG_STORAGE_CLASSES=[rbd-vm]
```

Replace the release and image placeholders with the reviewed release pins.
Replace `<name>` with the installation identifier. These are Fleet values, not shell commands.

`SERVICES_STORAGE_ROLES` maps logical storage roles to local classes. A published plan asks for the
`database` *role*, not a class name, because class names differ between
installations. Map the role to a class that supports volume expansion.

### 2. The hub

`clusters/<name>/services.yaml`, path `./platform/kube-dc-services`, with
`components: [components/self-data-plane]` when the cluster hosts its own data
plane, which it does when `SERVICES_RUNNER_SELF=true`. That component creates
the ServiceAccount the hub acts as on this cluster, its token, the scoped
read-only ClusterRole and the binding, and tells the hub that this plane is
itself. Nothing has to be applied by hand.

Use the Fleet services Kustomization as a template.
Review its namespace, substitutions, dependencies, and selected components for the target installation.

### 3. The catalog

`clusters/<name>/services-catalog.yaml`, path
`./platform/kube-dc-services-catalog`, `dependsOn: services`. Select
`components/data-plane` plus one component per family you offer, and
`components/shared-service-plans` for the published Development/Production
pairs:

```yaml
  components:
    - components/data-plane
    - components/valkey
    - components/shared-service-plans
```

`prune: false` on both Kustomizations, always: removing a file must never
delete a class or plan that a running instance still references.

### 4. Family operators

A family whose engine needs a cluster-wide operator gets its own opt-in Flux
Kustomization, and the family must be listed in `SERVICES_UNMANAGED_OPERATORS`
so the bundle attests the operator's version and never installs a second copy.

| Family | Tree | Pin |
|---|---|---|
| PostgreSQL | `infrastructure/cnpg` | `CNPG_VERSION` |
| MySQL | `platform/mysql-operator` | `MYSQL_OPERATOR_CHART_VERSION` |
| MariaDB | `platform/mariadb-operator` | `MARIADB_OPERATOR_VERSION` |
| ClickHouse | `platform/clickhouse-operator` | `CLICKHOUSE_OPERATOR_CHART_VERSION` |
| Valkey | `platform/valkey-operator` | `VALKEY_OPERATOR_CHART_VERSION` |
| Kafka | `platform/strimzi-operator` | `STRIMZI_OPERATOR_CHART_VERSION` |

Selecting a family component without its operator gives you a bundle that
never becomes ready and placements that are refused with "family bundle not
ready".

### 5. Label older Rook bucket Secrets

The PostgreSQL startup guard accepts a backup bucket Secret only if it carries
`services.kube-dc.com/provenance: rook-ceph`. The platform adds this label to
bucket Secrets that Rook creates in Project namespaces, but Secrets created
before that labelling existed don't have it. A new PostgreSQL instance in such a
Project then fails with `foreign PostgreSQL Secret API input db-backups`.

On an installation that already has Project buckets, run the backfill before
enabling the guard, then check that nothing is left:

```sh
hack/backfill-rook-bucket-provenance.py --kubeconfig <kubeconfig>          # report only
hack/backfill-rook-bucket-provenance.py --kubeconfig <kubeconfig> --apply
```

The script labels a Secret only if Rook evidently created it. It must be
controlled by its own Bound bucket claim, hold only the S3 key pair, have no
writer other than Rook, and predate automatic labelling. It reports everything
else and leaves it alone.

### 6. Verify the installation

```sh
kubectl get servicedataplane <name>-platform \
  -o jsonpath='{.status.ready}{"\n"}{range .status.bundles[*]}{.family}{"\t"}{.ready}{"\t"}{.message}{"\n"}{end}'
```

Every bundle must be ready. `does not pin a runner version` means
`SERVICES_RUNNER_TAG` rendered empty. `rollout pending` is the ordinary window
between the hub rolling out and the catalog applying, and clears by itself.

Then create a test service and connect an application through a binding.
Test the operations and recovery paths that the plan offers before publication.
Verify deletion and retained resources after the test.

## What tenants see in the console

The console shows managed services to every organization of the installation
by default. The following variables in `cluster-config.env` control visibility.
The chart renders them into the console runtime configuration:

```sh
# Managed services in the tenant console. The default is every organization;
# list organizations instead to run a pilot. The runtime configuration controls visibility.
KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true
KUBE_DC_UI_MANAGED_SERVICES_ORGANIZATIONS=[]
```

A tenant's console lists exactly the classes whose plans are published for
the cluster and not annotated `services.kube-dc.com/console: disabled`; the
creation sheet groups the plans of a class into Dev, Production and HA tiers.
See [Publish the catalog](managed-services-catalog.md).

## Check a deployment without rebuilding the cluster

Use the same read-only release gate after installation and after a services
upgrade:

```sh
kube-dc bootstrap --repo FLEET_REPO services verify CLUSTER \
  --kubeconfig ADMIN_KUBECONFIG
```

Replace `FLEET_REPO`, `CLUSTER`, and `ADMIN_KUBECONFIG` with the repository,
cluster overlay name, and administrator kubeconfig. The command pins that
kubeconfig's context and checks cluster/cell identity, the Ready services
chart and hub digest, the completed runner image rollout, current-generation
classes and plans, six family bundles, egress, placements, and console
publication. Add `--greenfield` for a new installation; it also requires the
starter's legacy plans to be disabled. It does not read signing-key Secrets.

For deployment testing, reuse the previously qualified family lifecycle
matrix when adapter code and plan inputs are unchanged. Render the starter
and target overlay, run this gate, then perform one disposable backup/restore
and deletion smoke. Test changed adapters or plan inputs separately. This
checks an existing cluster; the fresh-install harness still qualifies RKE2,
Flux ordering and first-install prerequisites on a clean host.

## Upgrade a family operator on one cluster

Operator, chart and catalog bundle pins must describe the same version.
For ClickHouse, select all three values in that cluster's
`cluster-config.env`:

```text
CLICKHOUSE_OPERATOR_CHART_VERSION=CHART_VERSION
CLICKHOUSE_OPERATOR_IMAGE_TAG=OPERATOR_VERSION@sha256:OPERATOR_DIGEST
CLICKHOUSE_METRICS_IMAGE_TAG=OPERATOR_VERSION@sha256:METRICS_DIGEST
```

Replace the placeholders with a compatible chart and verified image digests.
The chart version also updates the ClickHouse catalog bundle's operator
attestation. Other clusters retain their own pins. This upgrades the operator;
engine and Keeper versions remain selected by service plans.

Operator 0.27.4 removes `k8s_secret_password_sha256_hex`. Services source
`f280b84` and the combined `v0.9.1-rc9-clickhouse.20261002.3` artifacts implement
namespace-local `valueFrom.secretKeyRef` and a credential marker that rolls
server Pods after Secret changes. Stage operator 0.27.4 passed both credential roles on every replica,
explicit previous-password refusal, compute resize, volume expansion,
RestoreToNew, and retained source data and PVC identities. Evidence is in the
[ClickHouse migration checkpoint](https://github.com/kube-dc/kube-dc-services/blob/main/docs/operations-checkpoint-2026-10-02-clickhouse-migration.md).
Stage, Cloud and the starter candidate now pin `.4`, which preserves that
migration and fixes the user-visible status during Keeper loss. The
[Cloud fault checkpoint](https://github.com/kube-dc/kube-dc-services/blob/main/docs/operations-checkpoint-2026-10-02-cloud-faults.md) records member replacement, Keeper unavailability,
automatic data-preserving recovery and confirmed cleanup. These candidate
pins do not promote the stable CLI/starter release.

For an existing installation, use this order:

1. Deploy the reviewed compatible hub and runner before changing the operator.
   Confirm the running digests and the completed rollout.
2. Confirm existing CHIs use exact-owned namespace-local Secret selectors.
   The resource guard converts those references on apply and retains the
   credential marker through signed revision replay.
3. Select the operator chart, operator image and metrics image together.
   Verify the operator and current-generation catalog bundle.
4. Qualify credential rotation on every replica: the new password must work
   and the previous password must receive an explicit authentication failure.
   Check data markers and PVC identities before publication.

The published plans continue to select ClickHouse and Keeper 25.8.2. An operator
upgrade does not upgrade database engines. Keep a cluster on its qualified
operator pins until its migration gate passes.

Before pushing, render and record the target catalog's changed bundle pin:

```sh
bash scripts/services-catalog-render-test.sh --update CLUSTER
```

Review and commit the overlay, shared-template changes if needed, and
`platform/kube-dc-services-catalog/revisions.lock` together. Wait for the
operator HelmRelease and services catalog to become Ready, run
`bootstrap services verify`, and qualify a disposable service. Existing
instances keep their pinned catalog revision; use the reviewed catalog
adoption procedure when their bundle inputs need to move.

The same principle applies to MySQL: change
`MYSQL_OPERATOR_CHART_VERSION`, `MYSQL_OPERATOR_VERSION`, and
`MYSQL_OPERATOR_IMAGE_DIGEST` together. A version must exist in the operator's
published chart repository before selecting it.

## Upgrade the services control plane

The hub, the runner and the catalog are **one release**. The catalog in the
fleet tree describes the adapters the released runner compiles, and a class
whose blueprint digest does not match the running hub stops being `Verified`.
New placements are refused while existing services keep their pinned
revision.

So move `SERVICES_CHART_VERSION`, `SERVICES_HUB_TAG`, `SERVICES_RUNNER_TAG` and
`SERVICES_RUNNER_IMAGE` in one commit, and let the catalog follow in the same
commit. `dependsOn: services` orders the two applies.

If you hold a cluster on an older services release while the fleet tree moves
on, set `suspend: true` on its `services-catalog` Kustomization and un-suspend
it in the same commit that moves its pins. Nothing running is affected while it
is suspended.

## Not offering managed services

Create neither `services.yaml` nor `services-catalog.yaml`. Neither
`platform/kube-dc-services` nor `platform/kube-dc-services-catalog` is listed
in `platform/kustomization.yaml`, so no cluster gets them by being a cluster.
The `SERVICES_*` pins a new cluster inherits from the starter stay inert.

## Related

- [Managed services overview](managed-services-overview.md)
- [Publishing the catalog](managed-services-catalog.md)
- [Operating the service](managed-services-operations.md)
- [Installation guide](installation-guide.md)
- [Security model](security-model.md)
- [Multi-tenancy architecture](architecture-multi-tenancy.md)
