---
name: create-database
description: Create a managed PostgreSQL or MariaDB database in a Kube-DC project, configure access for workloads via environment variables and secrets, and optionally expose externally via Gateway or LoadBalancer.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`

## Steps

### 1. Create KdcDatabase

Apply the template with user's parameters:
- **Engine**: `postgresql` (default) or `mariadb`
- **Version**: PostgreSQL 14-17, MariaDB 10.11/11.4
- **Replicas**: 2+ for HA (recommended for production)
- **Expose**: `internal` (default), `gateway` (TLS passthrough), or `loadbalancer`

See @pg-template.yaml for PostgreSQL and @mariadb-template.yaml for MariaDB.

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: {db-name}
  namespace: {project-namespace}
spec:
  engine: postgresql
  version: "16"
  replicas: 2
  cpu: "1"
  memory: 2Gi
  storage: 10Gi
  databaseName: {database-name}
  username: app
```

### 2. Wait for Ready

```bash
kubectl get kdcdb {db-name} -n {project-namespace} -w
```

### 3. Configure Application Access

The database auto-creates a credential secret. Mount it in your workload:

**PostgreSQL** — secret: `{db-name}-app`, key: `password`
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-app
        key: password
  - name: DB_HOST
    value: "{db-name}-rw.{project-namespace}.svc"
  - name: DB_PORT
    value: "5432"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database-name}"
```

**MariaDB** — secret: `{db-name}-password`, key: `password`
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-password
        key: password
  - name: DB_HOST
    value: "{db-name}.{project-namespace}.svc"
  - name: DB_PORT
    value: "3306"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database-name}"
```

See @db-connection-patterns.md for full connection string examples.

### 4. External Access (Optional)

**Gateway** (recommended for production external access):
```yaml
spec:
  expose:
    type: gateway
```
→ Endpoint: `{db-name}-db-{project-namespace}.kube-dc.cloud:{port}`
→ Connect: `psql "host={db-name}-db-{project-namespace}.kube-dc.cloud port=5432 dbname={database-name} user=app sslmode=require"`

**Port-forward** (development/ad-hoc):
```bash
kubectl port-forward svc/{db-name}-rw {port}:{port} -n {project-namespace}
# Then connect to localhost:{port}
```

**LoadBalancer** (dedicated IP):
```yaml
spec:
  expose:
    type: loadbalancer
```
→ Check `status.externalEndpoint` for the allocated IP

### 5. Retrieve Password

```bash
# PostgreSQL
kubectl get secret {db-name}-app -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d

# MariaDB
kubectl get secret {db-name}-password -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d
```

## Verification

After creating the database, run these checks:

```bash
# 1. Check database phase (expect: Ready)
kubectl get kdcdb {db-name} -n {project-namespace} -o jsonpath='{.status.phase}'

# 2. Check endpoint is assigned
kubectl get kdcdb {db-name} -n {project-namespace} -o jsonpath='{.status.endpoint}'
# PostgreSQL: {db-name}-rw.{project-namespace}.svc:5432
# MariaDB: {db-name}.{project-namespace}.svc:3306

# 3. Verify credential secret exists
# PostgreSQL:
kubectl get secret {db-name}-app -n {project-namespace}
# MariaDB:
kubectl get secret {db-name}-password -n {project-namespace}

# 4. Test connectivity (from a temporary pod)
# PostgreSQL:
kubectl run pg-test --rm -it --restart=Never --image=postgres:16 -n {project-namespace} -- \
  pg_isready -h {db-name}-rw.{project-namespace}.svc -p 5432
# MariaDB:
kubectl run mysql-test --rm -it --restart=Never --image=mysql:8.0 -n {project-namespace} -- \
  mysqladmin ping -h {db-name}.{project-namespace}.svc --ssl-mode=DISABLED
```

**Success**: Phase is `Ready`, endpoint assigned, secret exists, connectivity test passes.
**Failure**: If phase is `Provisioning`, wait and recheck. If `Failed`:
- `kubectl describe kdcdb {db-name} -n {project-namespace}` — check conditions and events
- **Known issue**: MariaDB does not create the `databaseName` database or `username` user (db-manager bug). Only root access works for MariaDB currently.
## Safety
- Never log database passwords in chat output
- Default to `internal` exposure unless user explicitly requests external
- Recommend 2+ replicas for production workloads
- PostgreSQL endpoint uses `-rw` suffix; MariaDB does not
