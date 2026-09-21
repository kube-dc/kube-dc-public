# Migrating from db-manager Databases

`KdcDatabase` (`db.kube-dc.com/v1alpha1`), operated by db-manager, was
Kube-DC's first database product. It is **deprecated**. Managed Services is
the database offer for every engine, and this page is for teams that still
run a `KdcDatabase`.

## What deprecated means for you

- No new `KdcDatabase` can be created from the console, and the agent skills
  and documentation describe managed services only.
- Your existing databases keep running. db-manager keeps reconciling them,
  their scheduled backups keep running, `DatabaseCredentialPolicy` keeps
  rotating passwords, and `kube-dc db credentials` keeps working.
- The **Databases** area of the console is shown only for the organizations
  and Projects your provider has listed as still running these databases,
  with a deprecation notice. Everyone else sees Managed services alone; a
  bookmarked link to Databases explains where to go.
- Your provider will retire db-manager once the last `KdcDatabase` on the
  installation is gone. Ask them for the date. Until then, this page is the
  reference for the resources you still have.

Kube-DC does not convert a `KdcDatabase` into a `ManagedService`, and a
`ManagedService` cannot be restored from a `KdcDatabase` backup. You create the
managed service, copy the data with the engine's own tools from inside your
Project, repoint the application, and delete the old database.

## Migration procedure

1. **Create the managed service** with the same engine, a plan whose capacity
   covers the old database, and the same database name. PostgreSQL:
   [Create a PostgreSQL Service](postgresql-create.md); MariaDB:
   [MySQL and MariaDB](managed-services-mysql-mariadb.md). Wait for `Ready`.
2. **Bind a credential** for the migration: an `owner` binding whose Secret
   the copy Job reads. See [Connect Applications](postgresql-connect.md).
3. **Freeze writes** to the old database (stop the application or put it in
   read-only mode) and **copy the data** with one of the Jobs below. The Job
   runs in your Project, reads the old engine Secret and the new binding
   Secret, and never prints a password.
4. **Repoint the application** at the new binding Secret, using the key names
   in the [mapping](#secret-keys). Start it, check it, and keep the old
   database untouched until you have verified the copy.
5. **Delete the old database** with `kubectl delete kdcdatabase <name>` and
   any `DatabaseCredentialPolicy` that referred to it. Its Secrets go with it.

Check that the old database's last backup is recent before step 3, in case
the migration has to be repeated.

### PostgreSQL copy Job

The `KdcDatabase` engine Secret is `<db>-app` (keys `username`, `password`,
and the service `<db>-rw.<namespace>.svc:5432`). Replace `legacy-db`,
`orders-db`, `orders-api-db` (the binding Secret) and `my-project`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-orders
  namespace: my-project
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: copy
          image: ghcr.io/cloudnative-pg/postgresql:17.11
          command: ["/bin/sh", "-ec"]
          args:
            - |
              export PGSSLROOTCERT=/etc/target/ca.crt
              pg_dump --no-owner --no-privileges \
                "postgresql://$SRC_USER:$SRC_PASSWORD@legacy-db-rw.my-project.svc:5432/orders?sslmode=require" \
              | psql --set ON_ERROR_STOP=1 \
                "postgresql://$DST_USER:$DST_PASSWORD@$DST_HOST:$DST_PORT/$DST_DB?sslmode=verify-full"
          env:
            - { name: SRC_USER, valueFrom: { secretKeyRef: { name: legacy-db-app, key: username } } }
            - { name: SRC_PASSWORD, valueFrom: { secretKeyRef: { name: legacy-db-app, key: password } } }
            - { name: DST_HOST, valueFrom: { secretKeyRef: { name: orders-api-db, key: host } } }
            - { name: DST_PORT, valueFrom: { secretKeyRef: { name: orders-api-db, key: port } } }
            - { name: DST_DB, valueFrom: { secretKeyRef: { name: orders-api-db, key: dbname } } }
            - { name: DST_USER, valueFrom: { secretKeyRef: { name: orders-api-db, key: username } } }
            - { name: DST_PASSWORD, valueFrom: { secretKeyRef: { name: orders-api-db, key: password } } }
          volumeMounts:
            - { name: target, mountPath: /etc/target, readOnly: true }
      volumes:
        - name: target
          secret:
            secretName: orders-api-db
            items: [{ key: ca.crt, path: ca.crt }]
```

```bash
kubectl apply -f migrate-orders.yaml
kubectl wait --for=condition=Complete --timeout=1h job/migrate-orders -n my-project
kubectl logs job/migrate-orders -n my-project
```

`--no-owner --no-privileges` drops the old login's ownership so the objects
belong to the new `owner` role. Extensions the old database used must be
available in the new service; check the dump's `CREATE EXTENSION` lines first.

### MariaDB copy Job

The `KdcDatabase` engine Secret is `<db>-password` (key `password`, user `app`),
reachable at `<db>.<namespace>.svc:3306` for one replica or
`<db>-primary.<namespace>.svc:3306` for several:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-shop
  namespace: my-project
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: copy
          image: docker.io/library/mariadb:11.8
          command: ["/bin/sh", "-ec"]
          args:
            - |
              mariadb-dump --host legacy-shop.my-project.svc --port 3306 \
                --user app --password="$SRC_PASSWORD" --single-transaction --routines --triggers shop \
              | mariadb --host "$DST_HOST" --port "$DST_PORT" --user "$DST_USER" --password="$DST_PASSWORD" \
                --ssl-ca /etc/target/ca.crt --ssl-verify-server-cert "$DST_DB"
          env:
            - { name: SRC_PASSWORD, valueFrom: { secretKeyRef: { name: legacy-shop-password, key: password } } }
            - { name: DST_HOST, valueFrom: { secretKeyRef: { name: shop-api-db, key: host } } }
            - { name: DST_PORT, valueFrom: { secretKeyRef: { name: shop-api-db, key: port } } }
            - { name: DST_DB, valueFrom: { secretKeyRef: { name: shop-api-db, key: database } } }
            - { name: DST_USER, valueFrom: { secretKeyRef: { name: shop-api-db, key: username } } }
            - { name: DST_PASSWORD, valueFrom: { secretKeyRef: { name: shop-api-db, key: password } } }
          volumeMounts:
            - { name: target, mountPath: /etc/target, readOnly: true }
      volumes:
        - name: target
          secret:
            secretName: shop-api-db
            items: [{ key: ca.crt, path: ca.crt }]
```

## Secret keys

An application that reads the old engine Secret, or the Secret a
`DatabaseCredentialPolicy` projected, changes to the binding Secret:

| Old key (`DatabaseCredentialPolicy` projection) | Binding Secret key | Change |
|---|---|---|
| `username`, `password`, `host`, `port` | Same names | Change the Secret name only |
| `database` | `dbname` (PostgreSQL), `database` (MySQL, MariaDB) | Key name, for PostgreSQL |
| `dsn` | `uri` | Key name. The PostgreSQL URI uses `sslmode=verify-full`, so the client also needs the CA |
| `engine` | Not delivered | Select the driver in the application configuration |
| None | `ca.crt`, `sslmode` or `tls` | Mount `ca.crt`; PostgreSQL clients set `PGSSLROOTCERT` and `PGSSLMODE=verify-full` |

The old PostgreSQL engine Secret `<db>-app` and MariaDB Secret `<db>-password`
use `password` too. The old `dsn` value used `sslmode=require`, which does not
check the server's name; binding Secrets always verify it.

## What replaces what

| You used | In Managed Services |
|----------|---------------------|
| `KdcDatabase` | `ManagedService` with the class `postgresql`, `mysql` or `mariadb`, a plan and the connectivity class `tenant-native` |
| The engine Secret, or the Secret a `DatabaseCredentialPolicy` projects | A `ServiceBinding`, which delivers a Secret with connection details, the credential and the CA |
| `DatabaseCredentialPolicy` | A `ServiceCredentialPolicy` for scheduled rotation; a `ServiceBinding` for delivery |
| Native backup objects you create and list | A `Backup` operation and read-only `ServiceBackup` records |
| Field edits, annotations and `kube-dc db` commands for day-2 actions | One `ServiceOperation` per action, or the console's actions |
| The Databases area of the console | Managed services |

### Fields

| `KdcDatabase` field | `ManagedService` equivalent | Difference |
|---------------------|-----------------------------|------------|
| `spec.engine` | `spec.classRef.name` | The class, plan, placement and connectivity cannot change after creation |
| `spec.version` | `spec.engineVersion`, or omit it to take the plan's version | You choose a version by choosing a plan. Later versions arrive through `MinorUpgrade` and `MajorUpgrade` operations |
| `spec.databaseName` | `spec.parameters.database` | `CreateOnly` |
| `spec.username` | `spec.parameters.owner` (PostgreSQL) | `CreateOnly`; MySQL and MariaDB name their owner login for you. The login is delivered through the `owner` role |
| `spec.cpu`, `spec.memory` | `spec.compute.cpu`, `spec.compute.memory` | Within the plan's `capacity.computeBounds`; changed by a `Resize` operation where the family allows it |
| `spec.storage` | `spec.storage.size` | At most the plan's `capacity.maxStorage`; grown by `ExpandStorage` where the family and the storage class allow it |
| `spec.storageClassName` | `spec.storage.class` | `CreateOnly`; the plan default or one of its `allowedStorageClasses` |
| `spec.replicas` | `spec.topology.instances` | Within the plan's topology bounds; changed by a `Scale` operation for PostgreSQL |
| `spec.parameters` | `spec.parameters.postgresql.parameters` or `parameters.settings` | Allow-listed settings with string values |
| `spec.backup.*` | `spec.parameters.backup.*` (PostgreSQL) or the plan's schedule | Within the plan's backup bounds |
| `spec.backup.s3Endpoint`, `spec.backup.s3CredentialSecret` | `spec.parameters.backup.store` (PostgreSQL) | Needs the plan's `backup.allowedCustomEndpoints`; `CreateOnly` |
| `spec.expose.type` | `spec.parameters.expose.type` (PostgreSQL) | `gateway` and `loadbalancer` need plan entitlements; see [External Access](postgresql-external-access.md) |
| `spec.breakGlass.enableSuperuserAccess` | `spec.parameters.breakGlass.enableSuperuserAccess` (PostgreSQL) | Needs the plan's `credentials.allowBreakGlass` |
| `spec.restoreFrom.backupName`, `.targetTime` | `spec.restoreFrom.serviceRef`, `.serviceUID`, `.backupRef` or `.targetTime` | The source is a `ManagedService` in the same Project and its `ServiceBackup` record |
| None | `spec.deletionPolicy`, `spec.deletionProtection` | New. The default policy is `Retain` |

### Day-2 actions

| With `KdcDatabase` | With Managed Services |
|--------------------|-----------------------|
| Change `spec.version` for a major upgrade | A `MajorUpgrade` operation (PostgreSQL) with an image from the plan's `allowedImages`; needs a completed backup |
| Create a native backup object | A `Backup` operation, or **Back up now** in the console |
| List native backup objects | `ServiceBackup` records, or the **Backups** tab |
| Annotate with `kube-dc.com/restore-from` | A `RestoreInPlace` operation (PostgreSQL) with `engineUID`, `acknowledgeDataLoss: true` and `backupID` or `targetTime`, or a restore into a new service for every family |
| `kube-dc db credentials rotate` | A `RotateCredentials` operation, or the action in **Users & access** |
| `kube-dc db credentials get --show-password` | Read the binding Secret, or reveal it in **Users & access** |

### Credential policies

| `DatabaseCredentialPolicy` field | `ServiceCredentialPolicy` equivalent | Difference |
|----------------------------------|--------------------------------------|------------|
| `spec.databaseRef.name` | `spec.serviceRef.name` and `spec.serviceUID` | `serviceUID` is required |
| `spec.username` of the application user | `spec.role: owner` | `spec.role: readonly` rotates the read-only login |
| `spec.username` of another existing login | `spec.existingUser.username` and `.database` | Needs the plan's `credentials.allowExistingUsers`; the login must already exist |
| `spec.mode: static-rotated` | None | A policy only rotates passwords. It never creates a login |
| `spec.rotation.interval`, such as `30d` | `spec.rotationIntervalSeconds`, 60 to 31536000 | Default 2592000 (30 days) |
| `spec.sync.enabled`, `spec.sync.targetSecretName` | A `ServiceBinding` with `delivery.secretName` | A policy never delivers a Secret itself |

Deleting a `DatabaseCredentialPolicy` removes the Secret it projected.
Deleting a `ServiceCredentialPolicy` of a declared role keeps its bindings and
their Secrets. See [Credentials and Rotation](postgresql-credentials.md).

## While you still run a KdcDatabase

The resource keeps its API: `kubectl get kdcdatabase <name> -n <namespace>`
shows its phase and conditions, `spec.backup` holds the schedule and
retention, and the console's Databases area (where your Project is listed)
shows connection details, credential policies, backups and the restore
actions. Confirm the `BackupReady` condition before you rely on a backup, and
plan the migration before your provider's retirement date.
