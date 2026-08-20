# Database Connection Patterns

Use these examples from workloads that can reach the Project network. Replace
`{backing-namespace}` with the selected Project's Kubernetes backing
namespace.

## PostgreSQL

```text
postgresql://app:{password}@{database-name}-rw.{backing-namespace}.svc:5432/{application-database}
```

The bootstrap password is in Secret `{database-name}-app`, key `password`.
If a DatabaseCredentialPolicy manages `app`, use that policy's projected
Secret instead.

```yaml
env:
- name: DB_HOST
  value: "{database-name}-rw.{backing-namespace}.svc"
- name: DB_PORT
  value: "5432"
- name: DB_NAME
  value: "{application-database}"
- name: DB_USER
  value: "app"
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: "{database-name}-app"
      key: password
```

## MariaDB

The write endpoint depends on replica count:

| Shape | Host |
|---|---|
| One replica | `{database-name}.{backing-namespace}.svc` |
| Two or more replicas | `{database-name}-primary.{backing-namespace}.svc` |

The bootstrap password is in Secret `{database-name}-password`, key
`password`. Use the policy-projected Secret after a DatabaseCredentialPolicy
starts managing the user.

```yaml
env:
- name: DB_HOST
  value: "{mariadb-write-host}"
- name: DB_PORT
  value: "3306"
- name: DB_NAME
  value: "{application-database}"
- name: DB_USER
  value: "app"
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: "{database-name}-password"
      key: password
```

## External Clients

Use `spec.expose.type: loadbalancer` for the supported workstation path, then
read `.status.externalEndpoint`. The platform allocates a dedicated public EIp;
disable exposure from the Connection tab (or set the type back to `internal`)
to release it without interrupting the internal Service. Re-enabling assigns a
new public address and migrates legacy cloud-network endpoints to public.

Gateway is a narrow, manifest-only option for PostgreSQL 17 direct TLS:

```bash
psql "host={database-name}-db-{backing-namespace}.{platform-domain} port=443 dbname={application-database} user=app sslmode=require sslnegotiation=direct"
```

Do not use this Gateway path for PostgreSQL 14-16 or MariaDB. Standard Project
roles do not grant pod port-forward.
