---
name: create-database
description: Create a managed database (PostgreSQL, MySQL, MariaDB, ClickHouse) or Valkey cache in a Kube-DC Project as a ManagedService, deliver its credential to workloads with a ServiceBinding Secret, and prepare backup and restore workflows. KdcDatabase is deprecated; never create one.
---

## Prerequisites

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check storage, CPU, memory and pod quota with the `check-quota` skill.
- Know the plan names of the installation. Tenants cannot list the
  cluster-scoped catalog; standard installations publish
  `{family}-development` and `{family}-production`, and the console's
  "or get the YAML for GitOps" link shows the exact names it uses. When the
  user does not know, ask before applying.

## 1. Choose the Engine and Plan

| Need | Class | Standard plans | Notes |
|---|---|---|---|
| General SQL, PITR, replicas, pooler | `postgresql` | `postgresql-development`, `postgresql-production` | Production: 3 members, automatic failover |
| MySQL 8.4 | `mysql` | `mysql-development`, `mysql-production` | Sized once at creation; Production is Group Replication (every table needs a primary key) |
| MariaDB 11.8 | `mariadb` | `mariadb-development`, `mariadb-production` | Production is a Galera cluster; Resize and ExpandStorage available |
| Analytics SQL | `clickhouse` | `clickhouse-development`, `clickhouse-production` | Sized by shards × replicas, fixed at creation |
| Cache, sessions, queues | `valkey` (one node), `valkey-ha` (Sentinel) | `valkey-development`, `valkey-production` | Data you can rebuild |

Start with the Development plan unless the user asks for failover or backups
with 14-day retention.

## 2. Create the Service

Use the template for the engine: [postgresql-template.yaml](postgresql-template.yaml),
[mysql-mariadb-template.yaml](mysql-mariadb-template.yaml),
[clickhouse-template.yaml](clickhouse-template.yaml) or
[valkey-template.yaml](valkey-template.yaml). The PostgreSQL shape:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: "{service-name}"
  namespace: "{backing-namespace}"
spec:
  classRef:
    name: postgresql
  planRef:
    name: postgresql-development
  placement:
    mode: ProviderShared          # always; the API default is refused by the plans
  connectivity:
    classRef:
      name: tenant-native
  topology:
    instances: 1                  # within the plan's bounds
  compute:
    cpu: 500m
    memory: 1Gi
  storage:
    size: 10Gi
  parameters:
    database: "{application-database}"
    owner: app
    readonlyRole: true
    backup:
      enabled: true
  deletionPolicy: Retain
  deletionProtection: true
```

Rules the API enforces:

- Sizing is typed. Never put `cpu`, `memory`, `instances`, `storage` or
  `version` under `spec.parameters`; admission refuses them and names the
  typed field to use.
- `classRef`, `planRef`, `placement`, `connectivity` and `storage.class` are
  immutable. Sizing changes go through `ServiceOperation` objects (Scale,
  Resize, ExpandStorage) where the family allows them; MySQL and ClickHouse
  are sized once.
- ClickHouse uses `topology.shards` and `topology.replicasPerShard`, never
  `instances`.

Apply with a server-side dry run first, then wait for readiness and record
the UID, which every binding, operation and policy must pin:

```bash
kubectl apply --dry-run=server -f service.yaml
kubectl apply -f service.yaml
kubectl get managedservice {service-name} -n {backing-namespace} -w
kubectl get managedservice {service-name} -n {backing-namespace} -o jsonpath='{.metadata.uid}{"\n"}'
```

`PHASE: Ready` with `READY: True` is the readiness signal. A refusal shows as
`Accepted=False` with a reason (`PlanNotEntitled`, `ParameterSchemaRejected`,
`PlanQuotaExceeded`); fix the manifest rather than retrying.

## 3. Deliver a Credential to the Application

Create a `ServiceBinding` from [binding-template.yaml](binding-template.yaml)
with the service UID. The platform delivers a Secret; nothing is copied into
manifests.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: "{service-name}-owner"
  namespace: "{backing-namespace}"
spec:
  serviceRef:
    name: "{service-name}"
  serviceUID: "{service-uid}"
  role: owner                     # readonly | default (Valkey) | client, admin (Kafka)
  consumer:
    kind: ServiceAccount
    name: "{workload-service-account}"
    namespace: "{backing-namespace}"
  delivery:
    secretName: "{service-name}-owner"
```

Secret keys by family:

| Family | Keys |
|---|---|
| PostgreSQL | `host`, `port`, `dbname`, `username`, `password`, `sslmode` (`verify-full`), `ca.crt`, `uri` |
| MySQL, MariaDB | `host`, `port`, `database`, `username`, `password`, `tls` (`verify-full`), `ca.crt`, `uri`, `jdbcUrl` |
| ClickHouse | `host`, `port` (9440), `httpsPort` (8443), `database`, `username`, `password`, `tls`, `ca.crt`, `uri`, `httpsUrl`, `jdbcUrl` |
| Valkey | `host`, `port` (6379), `username`, `password`, `uri`, `ca.crt` |

Every endpoint is TLS-only with the platform's own CA: mount `ca.crt` and
verify the server (PostgreSQL `PGSSLROOTCERT` + `sslmode=verify-full`, MySQL
`--ssl-ca --ssl-verify-server-cert`, Valkey `--tls --cacert`). See
[db-connection-patterns.md](db-connection-patterns.md).

Some Helm charts expect a different password key. Prefer a chart setting such
as `existingSecretPasswordKey`; otherwise create a small bridge Secret from
the binding Secret and recreate it after every rotation unless automation
keeps it synchronized.

## 4. Do Not Expose Externally by Default

Services are reachable inside the Project through `tenant-native`. PostgreSQL
can be exposed through the direct-TLS Gateway (`parameters.expose.type:
gateway`, plan entitlement, PostgreSQL 17+ clients) or a public LoadBalancer
(`loadbalancer`, plan entitlement plus IPv4 quota). The other families have no
external exposure. Do not build host names; use the binding Secret's `host`.

## 5. Back Up and Restore

Scheduled backups run when the plan enables them (daily on the standard
plans). An on-demand backup is a `ServiceOperation` of type `Backup`; history
is the read-only `ServiceBackup` list. Restore is into a new service for every
family (`RestoreToNew` operation or `spec.restoreFrom` on a new
`ManagedService`); PostgreSQL also restores in place and offers point-in-time
recovery. Kafka backs up metadata only. See
[backup-restore-patterns.md](backup-restore-patterns.md).

## Verification

```bash
kubectl get managedservice,servicebinding -n {backing-namespace}
kubectl get managedservice {service-name} -n {backing-namespace} \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
kubectl describe secret {service-name}-owner -n {backing-namespace}   # keys only, never values
```

Report the service name, plan, phase, the binding Secret name and its keys.
Never print `password` or `uri`.

## Deprecated: KdcDatabase

`KdcDatabase` and `DatabaseCredentialPolicy` are deprecated. Never create
them. If the Project has one, point the user to the migration guide
(`docs/cloud/managed-services-migration.md`): create the managed service,
copy the data with the engine's dump tool from a Job in the Project, repoint
the application at the binding Secret, delete the `KdcDatabase`.
