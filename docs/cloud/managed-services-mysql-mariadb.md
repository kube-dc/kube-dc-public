# MySQL and MariaDB

Kube-DC offers both MySQL-family engines as separate classes: `mysql` (MySQL
8.4 on the MySQL Operator) and `mariadb` (MariaDB 11.8 on the MariaDB
Operator). Their plans, bindings and backups look the same to you; the
differences are in what can change after creation, and they are listed below.

In the console, pick the MySQL or MariaDB tile and a tier. The manifests
below create the same services.

## Shapes

| | Dev tier | Production tier |
|---|---|---|
| MySQL | One server behind a MySQL Router | Three Group Replication members behind two Routers, one write endpoint |
| MariaDB | One server | A three-member Galera cluster on distinct nodes, one write endpoint on the primary and a read endpoint on a replica |

## Create a service

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: shop-db
  namespace: my-project
spec:
  classRef:
    name: mariadb          # or mysql
  planRef:
    name: mariadb-production
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  topology:
    instances: 3
  compute:
    cpu: "1"
    memory: 2Gi
  storage:
    size: 20Gi
  parameters:
    database: shop
    settings:
      max_connections: "300"
      character_set_server: utf8mb4
  deletionPolicy: Retain
  deletionProtection: true
```

```bash
kubectl apply --dry-run=server -f shop-db.yaml
kubectl apply -f shop-db.yaml
kubectl get managedservice shop-db -n my-project -w
kubectl get managedservice shop-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
```

### Parameters

| Parameter | Meaning | Changes after creation |
|-----------|---------|------------------------|
| `database` | The application database, matching `^[a-z][a-z0-9_]{0,62}$`. Default `app` | `CreateOnly` |
| `settings` | Allow-listed server settings as strings: `max_connections`, `character_set_server`, `collation_server`, `wait_timeout`, `interactive_timeout`, `max_allowed_packet`, `long_query_time`, `slow_query_log`, `sql_mode`, `transaction_isolation`, `innodb_lock_wait_timeout` | `CreateOnly` |

### What changes after creation

| Field | MySQL | MariaDB |
|-------|-------|---------|
| `spec.compute.cpu`, `spec.compute.memory` | Fixed at creation | `Resize` operation |
| `spec.storage.size` | Fixed at creation | `ExpandStorage` operation |
| `spec.topology.instances` | Fixed at creation | Fixed at creation |
| `spec.engineVersion` | The plan's release line; a new version means a new plan | Same |
| `parameters.settings` | Fixed at creation | Fixed at creation |

A MySQL service is therefore sized once. To move to a larger size, take a
backup and restore it into a new service on a larger plan. See
[Backups and restore](#backups-and-restore).

## Connect an application

Both classes have two credential roles, `owner` and `readonly`, and two
endpoints: `read-write` (the default) and `read-only`. On the Production plans
the `read-only` endpoint reaches a replica; on the Dev plans it reaches the
single server.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: shop-api-db
  namespace: my-project
spec:
  serviceRef:
    name: shop-db
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  consumer:
    kind: ServiceAccount
    name: shop-api
    namespace: my-project
  delivery:
    secretName: shop-api-db
```

The delivered `Secret` has these keys:

| Key | Value |
|-----|-------|
| `host`, `port` | The endpoint. Port `3306` for the write endpoint; MySQL's read-only Router port is `6447` |
| `database` | The application database |
| `username`, `password` | The login of the credential role |
| `tls` | `verify-full`: the server certificate must be verified against `ca.crt` |
| `ca.crt` | The CA that signs the server certificate |
| `uri` | `mysql://<username>:<password>@<host>:<port>/<database>` |
| `jdbcUrl` | `jdbc:mysql://…?sslMode=VERIFY_IDENTITY` for MySQL, `jdbc:mariadb://…?sslMode=verify-full` for MariaDB |

Every endpoint is TLS-only with a certificate from the platform's own
authority: pass `ca.crt` to your client and verify the server name. A check
from a pod in the Project, reading the Secret from environment variables:

```sh
mariadb --host "$DB_HOST" --port "$DB_PORT" --user "$DB_USER" --password="$DB_PASSWORD" \
  --ssl-ca /etc/db/ca.crt --ssl-verify-server-cert "$DB_DATABASE" -e 'SELECT VERSION()'
```

The console's **Connect** card shows the same connection for the `mysql` or
`mariadb` client, Node, Python, Go, JDBC and a `.env` file.

Group Replication (MySQL Production) requires a primary key on every table;
tables without one are refused by the server. Galera (MariaDB Production)
switches the write endpoint to another member after a failure; applications
must reconnect and retry.

## Day-2 operations

| Operation | MySQL | MariaDB | What it does |
|-----------|-------|---------|--------------|
| `Backup` | ✅ | ✅ | Takes a verified logical archive and records it as a `ServiceBackup` |
| `RestoreToNew` | ✅ | ✅ | Creates a new service from a completed backup |
| `RotateCredentials` | ✅ | ✅ | New password for `owner` or `readonly`, republished to every binding of that role |
| `Resize` | ❌ | ✅ | CPU and memory within the plan's bounds |
| `ExpandStorage` | ❌ | ✅ | Grows the data volume, never shrinks |

Every type must be in your plan's `operations.allowed`. Rotation is also
available on a schedule through a `ServiceCredentialPolicy`; see
[Credentials and rotation](postgresql-credentials.md), which applies to these
classes with the roles `owner` and `readonly`.

## Backups and restore

Scheduled backups run when the plan enables them; the standard Production
plans back up daily at 02:00 UTC and keep 14 days, Dev keeps 7. A backup is a
verified logical archive (`mysqldump`-style for MySQL, `mariadb-dump`-style
for MariaDB) of the application database in your Project's backup bucket.
There is no point-in-time recovery and no restore in place.

Restore into a new service with a `RestoreToNew` operation.
Select an exact backup record and a compatible target plan. See
[Restore into a new service](postgresql-backup-restore.md#restore-into-a-new-service).
The new service may use a different plan, which is also how you move a MySQL
service to a larger size.

## Deletion

`deletionPolicy` and `deletionProtection` behave as described in
[Status and deletion](managed-services-status-deletion.md). Deleting the
Project deletes the service and its data without a final backup.

## Limits

| | MySQL | MariaDB |
|---|---|---|
| Versions | 8.4 release line | 11.8 release line |
| High availability | Production plan: Group Replication, automatic primary election; every table needs a primary key | Production plan: Galera, automatic write endpoint move; reconnect after a primary change |
| Sizing after creation | None | CPU, memory and storage |
| Point-in-time recovery | No | No |
| Exposure | Inside the Project only | Inside the Project only |
