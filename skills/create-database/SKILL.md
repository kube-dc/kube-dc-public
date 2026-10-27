---
name: create-database
description: Create a managed database (PostgreSQL, MySQL, MariaDB, ClickHouse) or Valkey cache in a Kube-DC Project as a ManagedService, deliver its credential to workloads with a ServiceBinding Secret, and prepare backup and restore workflows.
---

## Prerequisites

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check storage, CPU, memory and pod quota with the `check-quota` skill.
- Know which plan to use. Discover it from the Project's ServiceOfferings
  when the installation serves them (step 1); otherwise use the names below.

## 1. Discover the Plan

Tenants cannot read the cluster-scoped `ManagedServicePlan` objects. Where the
provider has enabled offerings, each plan the Project may use has a read-only
`ServiceOffering` copy (short name `so`) in the backing namespace. Check
whether the API is served, then list:

```bash
kubectl api-resources --api-group=services.kube-dc.com | grep -i serviceofferings
kubectl get serviceofferings -n {backing-namespace}
```

The offering name is the plan name (`spec.planRef.name`); everything is in
`status`. Use an offering only when it is **usable**:

- `status.projectionFormat` is `1` (the only format this skill knows);
- its `Ready` condition is `True`;
- the required fields for the next action are present: `class.family` and
  `class.name` to select the engine, `defaults` and `constraints` for sizing,
  `placementModes`, `connectivityClasses` and `deletionPolicies` for those
  choices, `createExample` for a manifest.

An offering without status, with another format, without `Ready=True`, or
missing a required field is not usable: skip it and treat what it says as
unknown.

```bash
kubectl get serviceofferings -n {backing-namespace} -o custom-columns='PLAN:.metadata.name,FORMAT:.status.projectionFormat,READY:.status.conditions[?(@.type=="Ready")].status,FAMILY:.status.class.family,CLASS:.status.class.name,PLACEMENT:.status.placementModes,CONNECTIVITY:.status.connectivityClasses,HA:.status.capabilities.ha,PER_PROJECT:.status.limits.servicesPerProject'
kubectl get serviceoffering {plan} -n {backing-namespace} -o jsonpath='{.status.defaults}{"\n"}{.status.constraints}{"\n"}'
kubectl get serviceoffering {plan} -n {backing-namespace} -o jsonpath='{.status.createExample}' > service.yaml
test -s service.yaml || echo "createExample missing: do not use this offering"
```

Match the user's engine against `class.family` (for example `postgresql`),
then use that offering's `class.name` in `spec.classRef.name`; the two can
differ. Pick a usable offering whose `placementModes` and
`connectivityClasses` include what the user needs. Default to
`ProviderShared` and `tenant-native` when the offering lists them; otherwise
use only values the offering lists.

Read a usable offering by its contract:

- `defaults` and `constraints` give per-instance CPU, memory and storage and
  the topology range (`instances`, or `shards` + `replicasPerShard`, or
  `dataReplicas`). Equal `min` and `max` mean fixed. Only the per-instance
  CPU, memory and storage ranges may omit `min` or `max`; an absent end there
  means the plan sets no bound at that end (storage often has only `max`).
  Topology ranges and `backup.retentionDays` always carry both `min` and
  `max` when present; if one is missing, do not use the offering for that
  step. An absent default storage means the plan has no persistent storage.
  Stay inside the bounds that are present; the service validator still checks
  at creation.
- `placementModes`, `connectivityClasses` and `deletionPolicies` are the only
  values the plan accepts. Choose `deletionPolicy` from `deletionPolicies`.
- `operations[].requiresApproval` previews which operations a new service
  on this plan may request and which wait for the provider; `backup` gives the
  retention range and schedule (absent means the plan takes no backups).
- `limits.servicesPerProject` caps services on this plan per Project. Absent
  `limits` means no per-Project limit beyond quota. Quota is a separate check
  (`check-quota`).
- `createExample` uses the defaults, the first placement mode and the first
  connectivity class. Rename `metadata.name` from `example` and add the
  family's `parameters`. If it says `TenantCluster`, either name the cluster
  in `placement.clusterRef` or, when `placementModes` also lists
  `ProviderShared`, switch to that for an in-Project service.
- An offering shows the plan's last projected revision
  (`plan.lastProjectedRevision`). A service that names the plan resolves it
  again when it is accepted, so the accepted revision can be newer. The
  offering is not a reservation and says nothing about existing services.

A plan missing from the usable offerings means discovery cannot confirm it is
available; it does not prove the plan is denied (projection can lag). Do not
guess another name. Confirm with the user or provider, or let the server dry
run and the service's `Accepted` condition decide.

If the resource is not served, the read fails or is `Forbidden`, or no
offering is usable for the request, fall back: get the plan name **and** its
supported placement and connectivity from the console (its "or get the YAML
for GitOps" link shows the exact values), the installation's documentation or
the provider, or ask the user. Standard installations publish
`{family}-development` and `{family}-production` with `ProviderShared` and
`tenant-native`; use those only when the installation confirms them. Do not
guess names, placement, connectivity, sizes, HA or backup retention. Never
export offerings into Git; GitOps manages the `ManagedService`.

## 2. Choose the Engine and Plan

These examples cover selected catalog families. Other integrations can use the
same managed service model. Usable offerings describe the plans and settings
last projected for the Project; the table shows the standard names and their
usual shape only.

| Need | Class | Standard plans | Notes |
|---|---|---|---|
| General SQL, PITR, replicas, pooler | `postgresql` | `postgresql-development`, `postgresql-production` | Production: 3 members, automatic failover |
| MySQL 8.4 | `mysql` | `mysql-development`, `mysql-production` | Sized once at creation; Production is Group Replication (every table needs a primary key) |
| MariaDB 11.8 | `mariadb` | `mariadb-development`, `mariadb-production` | Production is a Galera cluster; Resize and ExpandStorage available |
| Analytics SQL | `clickhouse` | `clickhouse-development`, `clickhouse-production` | Sized by shards × replicas, fixed at creation |
| Cache, sessions, queues | `valkey` (one node), `valkey-ha` (Sentinel) | `valkey-development`, `valkey-production` | Data you can rebuild |

Use the plan names the usable offerings list. Without offerings, use the
installation's confirmed names, placement and connectivity. Choose any
confirmed plan that satisfies the requested capabilities, sizing, engine
version, placement and connectivity. Use `{family}-development` (one
instance) unless the user asks for production or high availability; then use
`{family}-production` (three instances, which triples the quota draw). Tell
the user which plan you chose. Confirm HA (`capabilities.ha`), retention
(`backup.retentionDays`), sizing and engine version from the offering or from
the provider; do not assume them from the plan name.

## 3. Create the Service

Start from the edited `createExample` when step 1 produced one; otherwise use
the template for the engine: [postgresql-template.yaml](postgresql-template.yaml),
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
    mode: ProviderShared          # set explicitly; a value the offering or the provider confirms
  connectivity:
    classRef:
      name: tenant-native         # a value the offering or the provider confirms
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
  deletionPolicy: Retain          # must be one of the offering's deletionPolicies
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
the UID. Operations require it. Pin it in bindings and policies so that they
cannot attach to a replacement service with the same name:

```bash
kubectl apply --dry-run=server -f service.yaml
kubectl apply -f service.yaml
kubectl get managedservice {service-name} -n {backing-namespace} -w
kubectl get managedservice {service-name} -n {backing-namespace} -o jsonpath='{.metadata.uid}{"\n"}'
```

`PHASE: Ready` with `READY: True` is the readiness signal. A refusal shows as
`Accepted=False` with a reason (`PlanNotEntitled`, `ParameterSchemaRejected`,
`PlanQuotaExceeded`); fix the manifest rather than retrying.

## 4. Deliver a Credential to the Application

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
keeps it synchronized. Creating Secrets needs the Project `admin` role; the
`developer` role cannot.

## 5. Do Not Expose Externally by Default

Services are reachable inside the Project through `tenant-native`. PostgreSQL
can be exposed through the direct-TLS Gateway (`parameters.expose.type:
gateway`, plan entitlement, PostgreSQL 17+ clients) or a public LoadBalancer
(`loadbalancer`, plan entitlement plus IPv4 quota). The other families have no
external exposure. Do not build host names; use the binding Secret's `host`.

## 6. Back Up and Restore

Scheduled backups run when the plan enables them (daily on the standard
plans). An on-demand backup is a `ServiceOperation` of type `Backup`; history
is the read-only `ServiceBackup` list. Where the family and plan support
recovery, submit a `RestoreToNew` operation with a typed `spec.restore` block.
Do not create `ManagedService.spec.restoreFrom` yourself. PostgreSQL also
supports in-place restore and point-in-time recovery when the plan permits
them. Kafka backs up metadata only. See
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

## Optional PostgreSQL Client CIDRs

For an operator-qualified PostgreSQL public LoadBalancer or managed Gateway
plan, `spec.parameters.expose.allowedCIDRs` is an optional JSON/YAML array of
at most 32 IPv4 CIDRs. Use `/32` for one client and account for client NAT.
Omission on a new endpoint means unrestricted. On an existing signed
restriction, omission preserves the list, including exposure switches;
`allowedCIDRs: []` explicitly requests open access. Keep the rest of the
ManagedService parameters intact.

Do not invent plan flags or apply this policy to another database family.
Managed Gateway requires the provider's qualified managed-route plan and core
route ownership/admission/source-preservation contracts. Wait for the current
signed revision and binding, then use its CA, TLS server name and selected
external address for a real authenticated SQL query from allowed and denied
clients. Internal engine health is not independent public-reachability proof.
