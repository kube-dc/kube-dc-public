# Managed services: how it is built

Managed services give a tenant a database, cache or message broker they
describe as a Kubernetes object and never operate. This page explains the
parts of the system an operator will meet, what each owns, and where the
tenant's view ends. The other pages of this chapter cover
[enabling it](managed-services-enable.md),
[publishing the catalog](managed-services-catalog.md),
[operating it](managed-services-operations.md) and
[retiring db-manager](managed-services-retire-db-manager.md).

## Components

| Component | Runs where | Owns |
|-----------|-----------|------|
| **Hub** (commercial plane and site cell, one binary) | The management cluster, chart `kube-dc-services` | Admission of tenant objects, placement, signing of desired revisions, operation scheduling and approval, evidence, metering |
| **Runner** | Every data plane, in `kube-dc-service-runner` | Applies signed revisions and operations with the family adapter, observes the engine, reports status and usage |
| **Family bundle** (`ServiceFamilyBundle`) | Cluster-scoped | The runner image and, where the platform installs it, the engine operator of one family |
| **Data plane** (`ServiceDataPlane`) | Cluster-scoped | A cluster that hosts instances: its connector, families, storage roles and budgets, connectivity classes, isolation, lifecycle |
| **Catalog** (`ManagedServiceClass`, `ManagedServicePlan`, `ConnectivityClass`) | Cluster-scoped | What tenants may ask for. Published through the fleet, never edited by hand |
| **Engine operators** | Each data plane | CloudNativePG, MySQL Operator, MariaDB Operator, Altinity ClickHouse Operator, Strimzi, the Valkey operator for the HA class |

On a Kube-DC installation the management cluster is also the data plane
(`SERVICES_RUNNER_SELF=true`): the hub, the runner and the engines share the
cluster, and instances run inside the tenant's Project namespace
(`placement.mode: ProviderShared`, isolation `NamespaceSharedNodes`).

## Families

| Family | Class | Operator | Engine line | Topology | Backup format |
|--------|-------|----------|-------------|----------|---------------|
| PostgreSQL | `postgresql` | CloudNativePG | 14 to 18 | instances, synchronous standbys | `barman-cloud`, PITR |
| MySQL | `mysql` | MySQL Operator | 8.4 | instances (Group Replication) | `mysql-logical-v1` |
| MariaDB | `mariadb` | MariaDB Operator | 11.8 | instances (Galera) | `mariadb-logical-v1` |
| ClickHouse | `clickhouse` | Altinity operator | 25.8 | shards × replicas, Keeper quorum | `clickhouse-native-v1` |
| Valkey | `valkey`, `valkey-ha` | none / Valkey operator | 8 | one node, or three members with Sentinels | `valkey-rdb` |
| Kafka | `kafka` | Strimzi (KRaft) | 4.2, 4.3 | brokers; platform-owned controllers | metadata only |
| Forgejo | `forgejo` | none | 11 to 13 | one instance | local volume |

A family is code (an adapter compiled into the runner) plus a blueprint that
declares its parameter schema, mutation classes, credential roles, endpoint
roles, capabilities, operations and meters. A class points at a blueprint by
digest and fails closed when the compiled blueprint differs, so a catalog
cannot outrun the runner it describes.

## What a tenant object becomes

1. A tenant applies a `ManagedService`. Admission policies check immutability
   and the mutation fences; the hub validates the parameters against the class
   schema and the sizing against the plan, records `Accepted`, and looks for a
   data plane that offers the family, the connectivity class and the isolation
   the plan requires.
2. The hub reserves capacity on the cell (`CellReservation`), commits a
   `ServicePlacement` and signs a **desired revision**: the resolved spec with
   plan defaults filled in, the pinned class and plan revisions, the adapter
   version, the backup retention policy and the meters. Tenants read the
   resolution in `status.resolvedSpec` with a provenance map that says which
   value came from them, from the plan or from an operation.
3. The runner verifies the signature, renders the engine objects with the
   adapter and applies them in the instance namespace. It probes the endpoints
   from inside the data plane, publishes credentials as `ServiceBinding`
   Secrets, and reports status, topology and usage back through the hub.
4. Day-2 requests are `ServiceOperation` objects: immutable, idempotent,
   scheduled by concurrency class, optionally approval-gated by the plan, and
   executed one at a time per instance under a lease. The receipt of a
   completed operation lands in `status.effectiveConfiguration`; the
   manifest a tenant applied never changes.
5. Evidence (`ServiceEvidence`) is written for every applied revision and
   operation and kept for 90 days, 365 for deletions. `ServiceBackup` records
   are written into the tenant's Project without an owner, so the backup
   catalog survives the deletion of its source.

## Where the tenant's view ends

- Tenants read and write `ManagedService`, `ServiceOperation`,
  `ServiceBinding` and `ServiceCredentialPolicy` in their Project, and read
  `ServiceBackup`, `ServicePlacement` and `TopologyChange`. No Project role
  reads the cluster-scoped catalog: the console shows a redacted projection
  of it, and manifest writers get the names from you.
- Tenants cannot approve operations, adopt catalog revisions or accept
  parameter snapshots. Those annotations are restricted to the operator
  groups by admission policy.
- Engine objects (clusters, pods, volumes, engine Secrets) in the Project
  namespace are protected from Project identities by the platform's marker
  policies; a tenant changes a service only through the four resources above
  or the console.
- Credentials never appear in any status. A binding Secret is the value
  boundary, and Project Secret RBAC decides who reads it.

## Revisions and upgrades

Every instance pins the class revision, plan revision and adapter version it
was placed with, and keeps running on them until an operator adopts a newer
catalog for it. A catalog change therefore never rebuilds an instance on its
own; the runner refuses revisions it cannot execute; and the hub, runner and
catalog move as one release. The upgrade order and the adoption token are on
[Operating the service](managed-services-operations.md).

## Metering

The runner reports usage in sequenced batches per instance; the hub counts
gaps rather than guessing. The meters a plan declares are signed into every
revision: `instance-hours` and `storage-gib-hours` for the databases and
Valkey (PostgreSQL also `backup-bytes`; storage keeps metering while an
instance is hibernated), `broker-hours`, `controller-hours` and
`storage-gib-hours` for Kafka. ClickHouse Keeper members are not metered.
`kubectl -n <project> get msvc <name> -o jsonpath='{.status.usage}'` shows an
instance's totals.

## Related

- [Enabling managed services](managed-services-enable.md)
- [Publishing the catalog](managed-services-catalog.md)
- [Operating the service](managed-services-operations.md)
- [Security model](security-model.md)
