---
name: create-database
description: Create a managed PostgreSQL or MariaDB database in a Kube-DC Project, connect workloads with Kubernetes Secrets, configure supported external access, and prepare backup or restore workflows.
---

## Prerequisites

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check storage, CPU, memory, and pod quota with the `check-quota` skill.

## 1. Choose the Database Shape

| Engine | Supported versions | Internal write endpoint |
|---|---|---|
| PostgreSQL | 14, 15, 16, 17 | `{name}-rw.{backing-namespace}.svc:5432` |
| MariaDB, one replica | 10.11, 11.4 | `{name}.{backing-namespace}.svc:3306` |
| MariaDB, two or more replicas | 10.11, 11.4 | `{name}-primary.{backing-namespace}.svc:3306` |

Use two or more replicas when the engine and workload require failover. Start
with internal exposure unless the user explicitly needs workstation access.

## 2. Create the Database

Use [pg-template.yaml](pg-template.yaml) or
[mariadb-template.yaml](mariadb-template.yaml), or apply this PostgreSQL
example:

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: "{database-name}"
  namespace: "{backing-namespace}"
spec:
  engine: postgresql
  version: "16"
  databaseName: "{application-database}"
  username: app
  replicas: 2
  cpu: "1"
  memory: 2Gi
  storage: 20Gi
  expose:
    type: internal
```

Wait for the resource:

```bash
kubectl get kdcdb {database-name} -n {backing-namespace} -w
```

Treat `.status.phase=Ready` as the readiness signal. Provisioning time depends
on image availability, storage, placement, and quota.

## 3. Connect an Application

The engine creates a bootstrap credential Secret:

| Engine | Secret | Password key |
|---|---|---|
| PostgreSQL | `{name}-app` | `password` |
| MariaDB | `{name}-password` | `password` |

PostgreSQL example:

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

For MariaDB, use `{database-name}.{backing-namespace}.svc` with one replica
or `{database-name}-primary.{backing-namespace}.svc` with two or more, port
`3306`, and Secret `{database-name}-password`.

If a `DatabaseCredentialPolicy` manages this user, stop reading the engine
Secret. It remains at the provisioning-time value after the first rotation.
Use the policy's projected Secret or the authorized `kube-dc db credentials`
command instead.

Some Helm charts expect a different password key. Prefer a chart setting such
as `existingSecretPasswordKey`. If none exists, create a small bridge Secret
from the current source-of-truth Secret and point the chart at it. Recreate that
bridge after every credential rotation unless automation keeps it synchronized.

See [db-connection-patterns.md](db-connection-patterns.md) for connection
examples.

## 4. Configure External Access Only When Needed

### LoadBalancer: supported workstation path

The dashboard supports **Internal** and **LoadBalancer**. To request a
dedicated address in a manifest:

```yaml
spec:
  expose:
    type: loadbalancer
```

Read the allocated endpoint from status:

```bash
kubectl get kdcdb {database-name} -n {backing-namespace} \
  -o jsonpath='{.status.externalEndpoint}{"\n"}'
```

The wizard and Connection tab show Organization public-IPv4 usage as an
advisory; stale UI quota data never blocks the request, and the EIp controller
remains authoritative. The Connection tab can enable and disable this endpoint
after database creation. Enabling creates `{database-name}-external`, allocates
a dedicated public EIp, and waits for
`status.conditions[type=ExposureReady]` before presenting the endpoint as
ready. Disabling changes `spec.expose.type` to `internal`; db-manager deletes
the Service and the platform releases its EIp before clearing
`status.externalEndpoint`. The internal endpoint stays online.

Disable/re-enable recreates the Service with a new public address. It also
migrates a legacy endpoint that implicitly used the Project `cloud` network to
the supported public network, so update client allowlists and DNS.

Remove external exposure when it is no longer needed. Public IPv4 quota is
hard; if allocation is denied, inspect the `ExposureReady=False` reason/message
and the generated `slb-*` EIp condition.

### Gateway: PostgreSQL 17 direct TLS only

Gateway exposure is an advanced, manifest-only compatibility path. It works
only when PostgreSQL 17 and the client both start with a standard TLS
ClientHello and the client sets `sslnegotiation=direct`:

```yaml
spec:
  engine: postgresql
  version: "17"
  expose:
    type: gateway
```

Connect to public port `443`, not the engine port reported in status:

```bash
psql "host={database-name}-db-{backing-namespace}.{platform-domain} port=443 dbname={application-database} user=app sslmode=require sslnegotiation=direct"
```

This path is not compatible with PostgreSQL 14-16 protocol negotiation or the
MariaDB server-first handshake. It passes through the database certificate and
does not issue one for the public hostname. Use LoadBalancer for those engines
or when verified server identity is required.

Standard Project roles do not grant pod port-forward. Do not present
`kubectl port-forward` as a tenant database access method.

## 5. Back Up the Database

Scheduled backups are disabled unless configured:

```yaml
spec:
  backup:
    enabled: true
    schedule: "0 2 * * *"
    retentionDays: 7
```

Backups use the Project backup bucket configured by the platform. PostgreSQL
also uses continuous WAL archiving for point-in-time recovery when backup is
enabled. Follow [backup-restore-patterns.md](backup-restore-patterns.md) for
on-demand backups and both restore paths.

## Verification

```bash
# Database state and endpoints
kubectl get kdcdb {database-name} -n {backing-namespace} -o yaml

# Engine Service and ready endpoints
kubectl get service,endpointslice -n {backing-namespace}

# Bootstrap Secret, only when no DBCP manages the user
kubectl get secret {database-name}-app -n {backing-namespace} # PostgreSQL
```

Success means the `KdcDatabase` is Ready, the expected Service has endpoints,
and the correct source-of-truth credential Secret exists. On failure, inspect
conditions and events:

```bash
kubectl describe kdcdb {database-name} -n {backing-namespace}
```

## Safety

- Never print or log passwords.
- Default to internal exposure.
- Use the correct MariaDB endpoint for the replica count.
- Do not use an engine bootstrap Secret after a credential policy rotates that
  user.
- Prefer a new-name restore. In-place restore deletes engine resources and
  PVCs before rebuilding from the selected backup.
- MariaDB manual `PhysicalBackup` resources should use
  `target: PreferReplica`; strict `Replica` can wait indefinitely when no
  replica is available.
