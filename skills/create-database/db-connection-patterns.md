# Database Connection Patterns

## PostgreSQL

### Internal Connection String
```
postgresql://app:{password}@{db-name}-rw.{namespace}.svc:5432/{database}
```

### Secret Name
`{db-name}-app` — key: `password`

### Environment Variables for Workloads
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-app
        key: password
  - name: DB_HOST
    value: "{db-name}-rw.{namespace}.svc"
  - name: DB_PORT
    value: "5432"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database}"
  - name: DATABASE_URL
    value: "postgresql://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_NAME)"
```

### Port-Forward
```bash
kubectl port-forward svc/{db-name}-rw 5432:5432 -n {namespace}
psql "host=localhost port=5432 dbname={database} user=app"
```

### Gateway Access
```bash
psql "host={db-name}-db-{namespace}.kube-dc.cloud port=5432 dbname={database} user=app sslmode=require"
```

---

## MariaDB

### Internal Connection String
```
mysql://app:{password}@{db-name}.{namespace}.svc:3306/{database}
```

### Secret Name
`{db-name}-password` — key: `password`

### Environment Variables for Workloads
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-password
        key: password
  - name: DB_HOST
    value: "{db-name}.{namespace}.svc"
  - name: DB_PORT
    value: "3306"
  - name: DB_USER
    value: "app"
  - name: DB_NAME
    value: "{database}"
```

### Port-Forward
```bash
kubectl port-forward svc/{db-name} 3306:3306 -n {namespace}
mysql -h localhost -P 3306 -u app -p {database}
```

### Gateway Access
```bash
mysql -h {db-name}-db-{namespace}.kube-dc.cloud -P 3306 -u app -p --ssl {database}
```

---

## Key Differences

| Aspect | PostgreSQL | MariaDB |
|--------|-----------|---------|
| Service name | `{db-name}-rw` | `{db-name}` |
| Secret name | `{db-name}-app` | `{db-name}-password` |
| Default port | 5432 | 3306 |
| Gateway port | 5432 | 3306 |
