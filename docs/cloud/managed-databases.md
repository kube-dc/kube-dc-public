# Managed Databases

Kube-DC provides fully managed **PostgreSQL** and **MariaDB** databases as a built-in service. Each database runs as a high-availability cluster with automated backups, connection management, and live configuration — all managed through the dashboard or via kubectl.

## Databases Overview

<img src={require('./images/db-manager-overview.png').default} alt="Managed Databases overview" style={{maxWidth: '900px', width: '100%'}} />

The Databases view shows all databases in your project. The left sidebar organizes databases by engine type (**PostgreSQL** and **MariaDB**). Each database shows its name, engine, version, status, replica count, and age.

Click on any database to expand its details:

- **General Information** — Name, engine, version, namespace, CPU/memory, storage, status, and creation date
- **Connection** — Internal endpoint, port, database name, replica count, and a quick-connect command
- **Credentials** — Username with copy button and password with reveal/rotate options
- **Actions** — View Details or Delete the database

## Supported Engines

| Engine | Supported Versions | Default Port | Replication Model |
|--------|-------------------|-------------|-------------------|
| **PostgreSQL** | 14, 15, 16, 17 | 5432 | Streaming replication (HA) |
| **MariaDB** | 10.11, 11.4 | 3306 | Primary-replica replication |

## Create a Database

### Via Dashboard

1. Navigate to your project → **Databases** → **Overview**
2. Click **+ Create Database**
3. Fill in the creation wizard:
   - **Name** — A unique name for your database (lowercase, alphanumeric, hyphens)
   - **Engine** — PostgreSQL or MariaDB
   - **Version** — Select the engine version
   - **Database Name** — Name of the default database to create
   - **Username** — Database user (defaults to `app`)
   - **CPU / Memory** — Resource allocation per instance
   - **Storage** — Persistent storage size per instance
   - **Replicas** — Number of instances (1 = standalone, 2+ = high availability)
4. Review the summary and click **Create**

The database will transition through `Pending` → `Provisioning` → `Ready` status. This typically takes 1–2 minutes.

### Via kubectl

Create a `KdcDatabase` resource in your project namespace:

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: my-postgres
  namespace: my-project
spec:
  engine: postgresql
  version: "16"
  databaseName: myapp
  username: app
  cpu: "1"
  memory: 2Gi
  storage: 20Gi
  replicas: 2
```

```bash
kubectl apply -f my-postgres.yaml
kubectl get kdcdatabases -n my-project
```

## Database Detail View

Click **View Details** on any database to access the full management interface with five tabs: **Summary**, **Connection**, **Backups**, **Configure**, and **YAML**.

### Summary

<img src={require('./images/postgres-view.png').default} alt="Database summary tab" style={{maxWidth: '800px', width: '100%'}} />

The Summary tab provides a quick overview of your database:

- **Database Status** — Current phase (Ready, Provisioning, Failed), instance count, internal endpoint, and age
- **Resources** — CPU, memory, and storage allocated per instance
- **Configuration** — Engine type, version, database name, replica count, and exposure mode

### Connection

<img src={require('./images/postgres-connections.png').default} alt="Database connection tab" style={{maxWidth: '800px', width: '100%'}} />

The Connection tab shows everything you need to connect to your database:

- **Endpoint** — Internal cluster endpoint (ClusterIP). Use this from pods in the same namespace or via port-forward
- **Admin Credentials** — Username, password (click to reveal or rotate), database name, and port

**Connecting from a pod in the same namespace:**

```bash
# PostgreSQL
psql -h my-postgres-rw.my-project.svc -p 5432 -U app -d myapp

# MariaDB
mysql -h my-mariadb.my-project.svc -P 3306 -u app -p myapp
```

**Connecting via port-forward:**

```bash
# Forward PostgreSQL port to localhost
kubectl port-forward svc/my-postgres-rw 5432:5432 -n my-project

# Then connect locally
psql -h localhost -p 5432 -U app -d myapp
```

:::tip
The password is auto-generated and stored in a Kubernetes Secret. You can view it on the **Connection** tab or retrieve it via kubectl:

```bash
# PostgreSQL
kubectl get secret my-postgres-app -n my-project -o jsonpath='{.data.password}' | base64 -d

# MariaDB
kubectl get secret my-mariadb-password -n my-project -o jsonpath='{.data.password}' | base64 -d
```
:::

### Connecting from Application Workloads

The most common pattern is mounting the database password from the auto-created Kubernetes Secret and setting connection details as environment variables in your Deployment:

**PostgreSQL:**

```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: my-postgres-app      # Secret: {db-name}-app
        key: password
  - name: DB_HOST
    value: "my-postgres-rw.my-project.svc"
  - name: DB_PORT
    value: "5432"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "myapp"
  - name: DATABASE_URL             # Connection string (many frameworks use this)
    value: "postgresql://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_NAME)"
```

**MariaDB:**

```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: my-mariadb-password   # Secret: {db-name}-password
        key: password
  - name: DB_HOST
    value: "my-mariadb.my-project.svc"
  - name: DB_PORT
    value: "3306"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "myapp"
```

:::info Secret Naming Convention
- **PostgreSQL**: `{db-name}-app` (key: `password`)
- **MariaDB**: `{db-name}-password` (key: `password`)

These secrets are auto-created by the database controller and updated if the password is rotated.
:::

### External Access

By default, databases are only accessible within the cluster (ClusterIP). There are three ways to access a database externally:

#### Option 1: Gateway (TLS Passthrough) — Recommended for Production

Expose the database via Envoy Gateway with TLS passthrough. This provides a public hostname with TLS encryption handled by the database engine (PostgreSQL/MariaDB native TLS).

Set `spec.expose.type: gateway` when creating the database:

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: my-postgres
  namespace: my-project
spec:
  engine: postgresql
  version: "16"
  databaseName: myapp
  username: app
  cpu: "1"
  memory: 2Gi
  storage: 20Gi
  replicas: 2
  expose:
    type: gateway
```

Once ready, the external endpoint appears in the status and on the **Connection** tab:

```
my-postgres-db-my-project.kube-dc.cloud:5432
```

Connect using TLS:

```bash
psql "host=my-postgres-db-my-project.kube-dc.cloud port=5432 dbname=myapp user=app sslmode=require"
```

You can also select **Gateway (TLS Passthrough)** in the creation wizard UI.

#### Option 2: LoadBalancer + EIP — Direct IP Access

Expose the database via a dedicated LoadBalancer service with an External IP. This gives you a direct IP:port for the database.

Set `spec.expose.type: loadbalancer`:

```yaml
spec:
  expose:
    type: loadbalancer
```

Once an EIP is allocated, the external endpoint appears in the status:

```bash
# Check the external endpoint
kubectl get kdcdatabase my-postgres -n my-project -o jsonpath='{.status.externalEndpoint}'
# Example: 100.65.0.42:5432
```

#### Option 3: Port-Forward — Development / Ad-Hoc Access

For quick local access without exposing the database externally:

```bash
# PostgreSQL
kubectl port-forward svc/my-postgres-rw 5432:5432 -n my-project
PGPASSWORD=$(kubectl get secret my-postgres-app -n my-project -o jsonpath='{.data.password}' | base64 -d) \
  psql -h 127.0.0.1 -p 5432 -U app -d myapp

# MariaDB
kubectl port-forward svc/my-mariadb 3306:3306 -n my-project
mysql -h 127.0.0.1 -P 3306 -u app -p myapp
```

This is ideal for connecting from a local IDE, database GUI tool, or one-off queries.

| Method | Use Case | Requires | Persistent |
|--------|----------|----------|------------|
| **Gateway** | Production external access | `spec.expose.type: gateway` | Yes |
| **LoadBalancer** | Direct IP access | `spec.expose.type: loadbalancer` | Yes |
| **Port-forward** | Dev / ad-hoc queries | `kubectl` access | No (session-only) |

### Backups

<img src={require('./images/db-backups-schedule.png').default} alt="Database backups tab" style={{maxWidth: '800px', width: '100%'}} />

The Backups tab manages both scheduled and on-demand backups:

- **Schedule (cron)** — Set a cron expression for automatic backups (e.g., `0 2 * * *` for daily at 2 AM)
- **Retention (days)** — How many days to keep backups before automatic cleanup
- **Destination** — S3 bucket path where backups are stored, with a direct link to browse in S3
- **Last Completed** — Timestamp and name of the most recent successful backup

**Backup History** shows all backups with their name, type (Backup or Scheduled), status (Completed, Active, Failed), creation time, completion time, and schedule expression.

#### On-Demand Backups

Click **Create Backup** to trigger an immediate backup. The backup will appear in the history with type `Backup` and update its status as it progresses.

#### Scheduled Backups

Configure automatic backups by setting a cron schedule and retention period, then click **Update**. Common schedules:

| Schedule | Description |
|----------|-------------|
| `0 2 * * *` | Daily at 2:00 AM |
| `0 0 * * 0` | Weekly on Sunday at midnight |
| `0 3 */6 * *` | Every 6 hours starting at 3 AM |

All backups are stored in S3-compatible object storage and can be browsed from the **View in S3** link.

### Configure

The Configure tab lets you adjust database resources, scaling, and engine parameters without downtime.

**PostgreSQL Configuration:**

<img src={require('./images/postgres-configuration.png').default} alt="PostgreSQL configuration" style={{maxWidth: '800px', width: '100%'}} />

**MariaDB Configuration:**

<img src={require('./images/mariadb-configuration.png').default} alt="MariaDB configuration" style={{maxWidth: '800px', width: '100%'}} />

#### Resources

- **CPU** — CPU allocation per instance (e.g., `600m`, `1`, `2`)
- **Memory** — Memory allocation per instance (e.g., `1Gi`, `2Gi`, `4Gi`)
- **Storage** — Persistent storage per instance. Storage can only be **increased**, not decreased

#### Scaling

- **Replicas** — Number of database instances
  - **1** = Standalone (single instance, no replication)
  - **2+** = High Availability with automatic failover
  - PostgreSQL uses streaming replication; MariaDB uses primary-replica replication

#### Engine & Version

- **Engine** — Read-only. The engine type (PostgreSQL or MariaDB) cannot be changed after creation
- **Version** — Select a newer version to upgrade. Version upgrades trigger a rolling restart of all instances

:::warning
**Version upgrades** will cause a rolling restart. If running with 2+ replicas (HA mode), the database remains available during the upgrade. With a single replica, expect brief downtime.
:::

#### Parameters

Add engine-specific configuration parameters as key-value pairs:

- **PostgreSQL** — `shared_buffers`, `max_connections`, `work_mem`, `effective_cache_size`, etc.
- **MariaDB** — `max_connections`, `innodb_buffer_pool_size`, `query_cache_size`, etc.

Click **+ Add parameter** to add entries, then **Update** to apply changes.

### YAML

The YAML tab shows the raw `KdcDatabase` resource definition. You can use this to inspect the full configuration or as a template for creating similar databases via kubectl.

## High Availability

When running with **2 or more replicas**, your database operates in high-availability mode:

- **PostgreSQL** — One primary + one or more streaming replicas. If the primary fails, a replica is automatically promoted. The read-write endpoint (`-rw` suffix) always points to the current primary
- **MariaDB** — One primary + one or more replicas with automatic failover. The primary service endpoint always routes to the active primary

**Recommended configuration for production:**

| Setting | Value | Reason |
|---------|-------|--------|
| Replicas | 2+ | Enables automatic failover |
| CPU | 500m+ | Avoids throttling under load |
| Memory | 1Gi+ | Prevents OOM kills |
| Storage | 10Gi+ | Room for data growth |

## Deleting a Database

### Via Dashboard

1. Navigate to your database in the sidebar
2. Click the database row to expand details
3. Click **Delete**
4. Confirm the deletion

### Via kubectl

```bash
kubectl delete kdcdatabase my-postgres -n my-project
```

:::danger
Deleting a database permanently removes all data, replicas, and associated resources. This action cannot be undone. Make sure you have a recent backup before deleting.
:::

## Troubleshooting

### Database stuck in Provisioning

The database may be waiting for storage provisioning or resource allocation. Check events:

```bash
kubectl describe kdcdatabase my-postgres -n my-project
kubectl get events -n my-project --field-selector involvedObject.name=my-postgres
```

### Cannot connect to database

1. Verify the database status is **Ready** with all replicas running
2. Ensure you are connecting from within the same namespace or using port-forward
3. Check that the endpoint, port, username, and password are correct (use the **Connection** tab)
4. For cross-namespace access, use the full service name: `<db-name>-rw.<namespace>.svc:5432`

### Backup failed

Check the backup history for error details. Common causes:
- S3 storage credentials are misconfigured
- Insufficient S3 storage quota
- Database is not in Ready state
