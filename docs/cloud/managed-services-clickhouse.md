# ClickHouse

ClickHouse is a column-oriented SQL database for analytics over large event
and log tables. Kube-DC offers it as the class `clickhouse` (ClickHouse 25.8
LTS on the Altinity operator). Every service comes with its own ClickHouse
Keeper for coordination, run and paid for by the platform.

ClickHouse is the first family that is sized by its **shard shape** rather
than by an instance count: a service has `shards` and `replicasPerShard`. The
standard plans offer one shard with one replica (Dev) or one shard with two
replicas behind one address and a three-member Keeper quorum (Production). In
the console, the Size step shows *Replicas per shard* instead of *Instances*.

## Create a service

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: events
  namespace: my-project
spec:
  classRef:
    name: clickhouse
  planRef:
    name: clickhouse-production
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  topology:
    shards: 1
    replicasPerShard: 2
  compute:
    cpu: "2"
    memory: 8Gi
  storage:
    size: 50Gi
  parameters:
    database: analytics
    limits:
      max_execution_time: "300"
      max_result_rows: "1000000"
  deletionPolicy: Retain
  deletionProtection: true
```

Do not set `spec.topology.instances` on a sharded family; it is refused. The
console submits `shards` and `replicasPerShard` for you.

```bash
kubectl apply --dry-run=server -f events.yaml
kubectl apply -f events.yaml
kubectl get managedservice events -n my-project -w
```

`status.topology` reports the shape that runs, including
`coordinationMembers`, the Keeper members the platform operates for you.

### Parameters

| Parameter | Meaning | Changes after creation |
|-----------|---------|------------------------|
| `database` | The application database. Default `app`. Backups cover this database only | `CreateOnly` |
| `limits.max_execution_time`, `limits.max_result_rows`, `limits.max_concurrent_queries_for_user`, `limits.max_threads` | Per-query limits applied to the delivered logins' profiles, as strings | `OnlineDesired` |

Compute, storage size and class, and the shard shape are all fixed at
creation. To grow, take a backup and restore it into a new service on a larger
plan.

## Connect an application

Two credential roles, `owner` and `readonly`, on one endpoint, `read-write`:
ClickHouse has no separate read path, and the read-only role is what bounds a
consumer. The service publishes the native protocol on port `9440` and HTTPS
on `8443`, both TLS-only; the plaintext ports never leave the pod.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: events-loader
  namespace: my-project
spec:
  serviceRef:
    name: events
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  consumer:
    kind: ServiceAccount
    name: events-loader
    namespace: my-project
  delivery:
    secretName: events-loader
```

The delivered `Secret` has these keys:

| Key | Value |
|-----|-------|
| `host` | The service address |
| `port` | `9440`, the native protocol over TLS |
| `httpsPort` | `8443`, the HTTP interface over TLS |
| `database` | The application database |
| `username`, `password` | The login of the credential role |
| `tls`, `ca.crt` | `verify-full`, and the CA that signs the server certificate |
| `uri` | `clickhouse://<username>:<password>@<host>:9440/<database>?secure=true&skip_verify=false` |
| `httpsUrl` | `https://<host>:8443` |
| `jdbcUrl` | `jdbc:clickhouse://…?ssl=true&sslmode=STRICT` |

A check from a pod in the Project:

```sh
clickhouse-client --host "$CH_HOST" --port 9440 --secure \
  --user "$CH_USER" --password "$CH_PASSWORD" --database "$CH_DATABASE" \
  --query 'SELECT version()'
```

`clickhouse-client` reads the CA from its configuration
(`<openSSL><client><caConfig>`); HTTP clients pass `ca.crt` directly, for
example `curl --cacert ca.crt -u "$CH_USER:$CH_PASSWORD"
"https://$CH_HOST:8443/?database=$CH_DATABASE" --data-binary 'SELECT 1'`. The
console's **Connect** card shows these and the Python and Go equivalents.

Replication in the Production shape applies to tables you declare with a
`Replicated*` engine, not to every table. Create replicated tables for data
that must survive the loss of a replica.

## Day-2 operations

| Operation | What it does |
|-----------|--------------|
| `Backup` | Takes a verified native archive of the application database, records it as a `ServiceBackup` |
| `RotateCredentials` | New password for `owner` or `readonly`, republished to every binding of that role |
| `RestoreToNew` | Creates a new service, with its own Keeper, from a completed backup |

There is no resize, storage expansion, scaling, upgrade or hibernation for
this family in the current release. Rotation on a schedule uses a
`ServiceCredentialPolicy`; see
[Credentials and rotation](postgresql-credentials.md).

## Backups and restore

Scheduled backups run when the plan enables them (daily, 14 days on the
Production plan, 7 on Dev). A backup covers the application database only:
anything you create in another database is not archived and is deleted with
the service. There is no point-in-time recovery and no restore in place.

Restore into a new service with a `RestoreToNew` operation or a new
`ManagedService` with `spec.restoreFrom`; see
[Restore into a new service](postgresql-backup-restore.md#restore-into-a-new-service).
The new service owns its own Keeper, so its tables never rejoin the source's
replication group.

## Limits

The following table lists the limits of a managed ClickHouse service:

| Property | Limit |
|---|---|
| Versions | 25.8 release line |
| High availability | Production plan: two replicas of one shard behind one address, coordinated by a three-member Keeper quorum that survives losing one member. A lost Keeper leaves replicated tables readable and refusing writes until it returns |
| Fixed at creation | Compute, storage size and class, shards, replicas per shard, the application database |
| Point-in-time recovery | No |
| Shard rebalancing | No |
| Exposure | Inside the Project only |
