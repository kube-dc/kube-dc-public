---
name: manage-database-credentials
description: Manage static password rotation for an existing Kube-DC database with DatabaseCredentialPolicy, optionally projecting the current credential into a stable Kubernetes Secret.
---

## Prerequisites

- The target Project and `KdcDatabase` are Ready.
- Know the Project's backing namespace: `{organization}-{project}`.
- The database user already exists. The default `app` user is created with a
  managed database; create custom users through an approved DBA or migration
  Job before adding a policy.
- Never target the reserved `kdc_rotator`, `postgres`, or `root` users.

## Current Capability

`static-rotated` is the only supported mode. OpenBao changes one existing
user's password on the configured interval. The `rolling` and `immediate`
strategy values remain for API compatibility, but both currently perform a
single-password cutover. Applications must reload credentials before opening a
new connection.

`dynamic` is reserved but not implemented. A dynamic policy reports
`Ready=False/DynamicModeDeferred`, does not project a Secret, and the issue
API/CLI returns HTTP 501.

## Trust Boundary

DatabaseCredentialPolicy is scoped to a Project, but raw Kubernetes Secret
access is shared within that Project. The standard `admin`, `developer`, and
`project-manager` roles can read raw Secrets, including database management
credentials. The `user` role cannot.

If two teams must not share database credentials, place their workloads in
separate Projects. There is no per-Secret isolation inside one Project.

## Create a Policy

Use [dbcp-template.yaml](dbcp-template.yaml) or apply:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: api-app
  namespace: "{backing-namespace}"
spec:
  databaseRef:
    name: "{database-name}"
  mode: static-rotated
  username: app
  rotation:
    interval: 30d
    strategy: rolling
  sync:
    enabled: true
    targetSecretName: api-app-credentials
```

```bash
kubectl apply -f dbcp.yaml
kubectl get dbcp api-app -n {backing-namespace} -w
```

When sync is omitted for `static-rotated`, admission defaults it on. The
target Secret name defaults to the policy name.

## Consume the Projected Secret

A synced Secret contains `username`, `password`, `host`, `port`,
`database`, `engine`, and `dsn`.

```yaml
env:
- name: DB_USER
  valueFrom:
    secretKeyRef:
      name: api-app-credentials
      key: username
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: api-app-credentials
      key: password
- name: DB_HOST
  valueFrom:
    secretKeyRef:
      name: api-app-credentials
      key: host
- name: DB_PORT
  valueFrom:
    secretKeyRef:
      name: api-app-credentials
      key: port
```

The Secret is rewritten after rotation. Environment variables do not change in
a running container; restart the workload. Secret volume files refresh
eventually, but the application must reread them. Keep connection-pool reload or
authentication-error recovery in the application design.

Do not use the engine bootstrap Secret after a policy starts managing that
user. It is not updated on each policy rotation.

## CLI Operations

```bash
kube-dc db credentials list
kube-dc db credentials describe api-app

# Password is masked unless explicitly requested
kube-dc db credentials get api-app
kube-dc db credentials get api-app --show-password

# One immediate single-password cutover
kube-dc db credentials rotate api-app

# Explicit confirmation is required
kube-dc db credentials delete api-app --yes
```

Do not put `--show-password` output in logs or chat.

## Rotation Timing

OpenBao owns the rotation schedule. The controller tightens its requeue near a
rotation and otherwise uses a five-minute ceiling to refresh the projected
Secret. Kubelet propagation adds another delay for mounted files. Design
clients for eventual propagation; do not promise an exact cutover second.

## Restore and Delete Semantics

After an in-place database restore, rotate every affected policy once the
database is Ready. The restored database can contain an older password while
OpenBao and the projected Secret still hold the pre-restore value:

```bash
kube-dc db credentials rotate api-app
```

Deleting a policy removes its controller-owned projected Secret. For
PostgreSQL's default `app` user, the finalizer attempts to copy OpenBao's
current password back to `{database}-app`. MariaDB does not currently provide
the same engine-Secret resynchronization guarantee. Before deleting a MariaDB
policy, arrange a replacement credential or database-user reset with the
operator.

Deleting a policy does not drop the database user.

## Verification

```bash
# Ready condition
kubectl get dbcp api-app -n {backing-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'

# Projected Secret metadata and expected keys
kubectl get secret api-app-credentials -n {backing-namespace} \
  -o jsonpath='{.metadata.name}{"\t"}{.type}{"\n"}'
```

Use an application or migration Job on the Project network for an end-to-end
login check. Project pod exec, attach, and port-forward are not supported
administrative paths.

Common conditions:

- `DynamicModeDeferred`: dynamic mode is not implemented; use
  `static-rotated`.
- `DatabaseEngineUnconfigured`: wait for db-manager to register the Ready
  database with OpenBao.
- `DatabaseNotFound`: fix `spec.databaseRef.name`.
- `TargetSecretConflict`: choose an unused target Secret or remove the
  conflicting owner.
- `RoleProvisioning`: OpenBao accepted the role but credentials are not yet
  readable; retry and escalate if it persists.

## Safety

- One policy is allowed for each `(databaseRef, username)` pair.
- Both rotation strategies are currently single-password cutovers.
- Do not edit the projected Secret; reconciliation overwrites it.
- Root/superuser rotation endpoints are retired and return HTTP 410. Use the
  documented break-glass path with operator help.
- Short intervals create repeated `ALTER USER` load. Use short periods only
  in tests and days in production.
