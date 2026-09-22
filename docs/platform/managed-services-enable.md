# Enable managed services on a Kube-DC installation

Managed services give a tenant a database or a message broker they ask for as a
Kubernetes object and never operate: Kube-DC places it, issues its
certificates, holds its credentials, backs it up, restores it and meters it.

This page is for the operator of a Kube-DC installation. It covers turning the
feature on, choosing which engines you offer, and upgrading it later.

**A cluster that does not want managed services does nothing.** Nothing on this
page is in the shared platform tree, so an installation that never creates the
two Flux Kustomizations below has no hub, no catalog and no tenant-visible
plans. The CloudSigma sites run this way deliberately.

## What an installation offers

| Family | Development plan | Production plan | Backups |
|---|---|---|---|
| PostgreSQL | 1 instance | 3 instances, replicated | object store, PITR |
| MySQL | 1 server + Router | 3 Group Replication members + 2 Routers | verified logical archive |
| MariaDB | 1 server | 3-member Galera *(unpublished)* | verified logical archive |
| ClickHouse | 1 server + Keeper | 2 replicas + 3 Keepers | verified native archive |
| Valkey | 1 node | 3 nodes, Sentinel | RDB snapshot |
| Kafka | 1 controller | 3 controllers, 3+ brokers | none; replication provides the durability |

Every family offers create, bind, credential rotation, storage expansion,
backup and restore-into-a-new-service. A plan marked *unpublished* ships
implemented but annotated `services.kube-dc.com/console: disabled`, so tenants
do not see it: its HA-specific checks have not been run. Publish one by setting
that annotation to `enabled` once you have run them on your own cluster — and
bump the plan's `revision`, or the catalog gate refuses content that changed
under an unchanged one.

MariaDB's Galera shape is the one still unpublished, and not for want of
testing: the operator's Galera `Init` step creates the engine's storage claim
itself, and `managed-references.kube-dc.com` refuses a reserved claim name from
any creator that does not carry the platform marker. The Standalone shape is
unaffected, because there the StatefulSet creates its claims from a template
that already carries it.

## Turn it on

### 1. Pins

In `clusters/<name>/cluster-config.env`. The release values come from the
starter's `bootstrap/release-pins.env`; the rest describe this cluster.

```sh
SERVICES_CHART_VERSION=v0.9.0-rc1
SERVICES_HUB_TAG=v0.9.0-rc1
SERVICES_RUNNER_TAG=v0.9.0-rc1
SERVICES_RUNNER_IMAGE=shalb/kube-dc-services-runner:v0.9.0-rc1

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

`SERVICES_STORAGE_ROLES` is the important one. A published plan asks for the
`database` *role*, not a class name, because class names differ between
installations. Map the role to a class that supports volume expansion.

### 2. The hub

`clusters/<name>/services.yaml`, path `./platform/kube-dc-services`, with
`components: [components/self-data-plane]` when the cluster hosts its own data
plane, which it does when `SERVICES_RUNNER_SELF=true`. That component creates
the ServiceAccount the hub acts as on this cluster, its token, the scoped
read-only ClusterRole and the binding, and tells the hub that this plane is
itself. Nothing has to be applied by hand.

Copy `clusters/stage/services.yaml` and change nothing but the components list.

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
| Kafka | installed by the family bundle | None |

Selecting a family component without its operator gives you a bundle that
never becomes ready and placements that are refused with "family bundle not
ready".

### 5. Check it

```sh
kubectl get servicedataplane <name>-platform \
  -o jsonpath='{.status.ready}{"\n"}{range .status.bundles[*]}{.family}{"\t"}{.ready}{"\t"}{.message}{"\n"}{end}'
```

Every bundle must be ready. `does not pin a runner version` means
`SERVICES_RUNNER_TAG` rendered empty. `rollout pending` is the ordinary window
between the hub rolling out and the catalog applying, and clears by itself.

Then create one service from a Development plan, bind it, and delete it.

## What tenants see in the console

The console shows managed services to every organization of the installation
by default. Two groups of variables in `cluster-config.env` tune that, and the
chart renders them into the console's runtime configuration:

```sh
# Managed services in the tenant console. The default is every organization;
# list organizations instead to run a pilot. CloudSigma installations never
# show the area.
KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true
KUBE_DC_UI_MANAGED_SERVICES_ORGANIZATIONS=[]

# The deprecated db-manager Databases area, only for tenants that still run
# KdcDatabase resources. Empty lists retire it for everyone.
KUBE_DC_UI_LEGACY_DATABASES_ORGANIZATIONS=[]
KUBE_DC_UI_LEGACY_DATABASES_PROJECTS=[]
```

A tenant's console lists exactly the classes whose plans are published for
the cluster and not annotated `services.kube-dc.com/console: disabled`; the
creation sheet groups the plans of a class into Dev, Production and HA tiers.
See [Publishing the catalog](managed-services-catalog.md) and
[Retiring db-manager](managed-services-retire-db-manager.md).

## Upgrade

The hub, the runner and the catalog are **one release**. The catalog in the
fleet tree describes the adapters the released runner compiles, and a class
whose blueprint digest does not match the running hub stops being `Verified`.
new placements are refused while everything already running keeps its pinned
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
