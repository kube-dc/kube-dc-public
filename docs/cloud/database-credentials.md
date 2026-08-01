# Database Credentials

Kube-DC's Database Credentials feature rotates a database user's password and,
when Secret sync is enabled, projects the current value into a Kubernetes
`Secret`. The password does not
appear in the `DatabaseCredentialPolicy` manifest.

Static rotated is the only active mode. The username stays the same and OpenBao
changes its password on the configured interval. The API also accepts `dynamic`,
but the controller reports `Ready=False` with `DynamicModeDeferred` and the
`issue` command does not mint credentials yet.

The Kubernetes resources for Database Credentials are scoped to a
`KdcDatabase` in your Project.
Bring a `KdcDatabase` first (see [Managed Databases](managed-databases.md));
once you have one, attach as many `DatabaseCredentialPolicy` CRs to
it as you need.

## Concepts

A **DatabaseCredentialPolicy** is a CRD in your project:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: api-app
  namespace: acme-production
spec:
  databaseRef:
    name: api-db                   # KdcDatabase in the same Project
  mode: static-rotated
  username: app
  rotation:
    interval: 30d
  sync:
    enabled: true
    targetSecretName: api-app-creds
```

Key fields:

- **databaseRef** — `KdcDatabase` in the same Project.
  Cross-Project references cannot be expressed by this namespaced API; an
  admission webhook verifies the referenced database actually exists.
- **mode** — use `static-rotated`. The reserved `dynamic` value is
  admitted but remains deferred.
- **username** — the database username to manage. Must already exist
  in the database (Kube-DC does NOT create users in static-rotated
  mode — that prevents the platform from owning user identities and
  keeps the DBA in control). Defaults to `app`.
- **rotation.interval** — how often the password rotates (e.g. `30d`,
  `7d`).
- **rotation.strategy** — retained in the API for compatibility, but the current
  controller does not implement dual-password overlap. Treat either value as a
  password cutover and make clients reload credentials before reconnecting.
- **sync.enabled** — projects the username + password into a
  Kubernetes `Secret` your pods can mount. The Secret is rewritten
  in place on every rotation.

## Permissions

| Role | `DatabaseCredentialPolicy` resource | Read current password through API | Rotate through API |
|---|---|---|---|
| `admin` | Full lifecycle | Yes | Yes |
| `developer` | Create, read, update, delete | No | No |
| `project-manager` | Read and update existing resources | Yes | Yes |
| `user` | Read | No | No |

The OpenBao data plane grants static-password read and rotation to
`project-manager` and `admin`. The `developer` tier can manage the policy
resource but does not receive those OpenBao capabilities.

The `admin`, `developer`, and `project-manager` roles can read Kubernetes
`Secret` objects in the Project's backing namespace. When sync is enabled, all
three roles can therefore read the projected password. For Kubernetes Secret
access, treat the Project as the credential boundary.

### Current Organization-wide OpenBao boundary

`DatabaseCredentialPolicy` resources, database references, and projected Secrets
are Project-scoped. The underlying OpenBao Database engine is mounted once per
Organization, however, and the current human policies grant capabilities on a
complete role-name path segment. They cannot safely distinguish Project names
such as `prod` and `prod-west` with a prefix glob.

As a result, an `admin` or `project-manager` who has database credential access
in one Project can read or rotate another Project's static database role in the
same Organization if they learn its internal role name. The dashboard and CLI
first resolve a policy in the selected Project, but that supported workflow does
not narrow the underlying OpenBao authorization.

Treat the **Organization as the current trust boundary for direct OpenBao
database operations**. Put mutually untrusted teams in separate Organizations.
True per-Project isolation requires a future database-mount or role-name
migration; it is not part of this release.

### Trust model (read this if you share a Project)

> **On Kube-DC, anyone with `admin`, `developer`, or `project-manager` in a
> Project has effective superuser access to databases owned by that Project.**
> Tenant separation is enforced at the **Project boundary**, not between users
> inside one Project. Grant these roles only where that database access is
> acceptable.

This applies to every database in the Project, PostgreSQL and MariaDB alike.
The underlying mechanism is Kubernetes RBAC: all three roles can `get` and
`list` Secrets in the Project's backing namespace. Kube-DC's database engines
keep privileged credentials there in CNPG's `<db>-rotator` Secret for
PostgreSQL and mariadb-operator's `<db>-root` Secret for MariaDB. Anyone who can
read those Secrets can connect as a database superuser.

If separation of duties requires Secret readers not to be database administrators, place those workloads in separate Projects and grant `developer` or `project-manager` only where that access is acceptable. This release does not provide credential isolation within one Project.

## How rotation actually works (mental model)

You don't need to know any of this to use the feature — but it
helps when something goes wrong.

Every PostgreSQL `KdcDatabase` automatically gets a dedicated
**`kdc_rotator`** PostgreSQL role at creation time. This is the
identity OpenBao logs in as to run the `ALTER USER ... PASSWORD`
that rotates your application user's password. Its own credential
lives in a `<db>-rotator` Kubernetes Secret in the Project's backing
namespace. The normal DBCP lifecycle does not rotate it; that invariant
keeps point-in-time recovery safe (if we rotated the rotator,
a PITR back to before the rotation would lock OpenBao out of the
database it just restored).

The `kdc_rotator` role is reserved by the platform — DBCP creation
that targets `username: kdc_rotator` (or `postgres`, or MariaDB
`root`) is rejected by an admission webhook. Use any other
username; the platform manages rotation for it.

For MariaDB, the same shape exists by default via the
operator-provisioned `<db>-root` Secret. There's no separate rotator
role to worry about.

## Prerequisites

Before creating a policy, the underlying database user must exist:

```bash
# Connect through the internal Project Service from an application or migration
# workload, or through a configured LoadBalancer endpoint, then run:
psql -c "CREATE USER app WITH LOGIN PASSWORD 'temporary-bootstrap';"
psql -c "GRANT ALL PRIVILEGES ON DATABASE mydb TO app;"
# (Or the MariaDB equivalent.)
```

Once the DatabaseCredentialPolicy reconciles, OpenBao replaces the bootstrap
password and becomes the source of truth. Authorized users can retrieve the
current password with `kube-dc db credentials get --show-password` or from the
synced Secret; the value is not stored in the CRD or audit records.

## Create a policy

### Via the CLI

```bash
# Static-rotated, 30-day rotation, projected into a K8s Secret
kube-dc db credentials create api-app \
  --database=api-db \
  --mode=static-rotated \
  --username=app \
  --rotate=30d

# Custom username, interval, and projected Secret name
kube-dc db credentials create batch-app \
  --database=api-db \
  --username=batch \
  --rotate=7d \
  --sync-secret=batch-db-creds
```

### Via kubectl

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: api-app
  namespace: acme-production
spec:
  databaseRef:
    name: api-db
  mode: static-rotated
  username: app
  rotation:
    interval: 30d
  sync:
    enabled: true
    targetSecretName: api-app-creds
```

```bash
kubectl apply -f dbcp.yaml
```

Watch the policy reach Ready:

```bash
kubectl get dbcp api-app -w
# NAME      DATABASE   MODE             USERNAME   AGE   READY
# api-app   api-db     static-rotated   app        15s   True
```

## Use the credentials in a workload

When `sync.enabled: true`, the platform projects the credentials into
a regular Kubernetes `Secret` (`type: Opaque`):

```bash
kubectl get secret api-app-creds -o yaml
# type: Opaque
# data:
#   username: <base64 "app">
#   password: <base64 "rotated password">
#   host:     <base64 "api-db-rw.acme-production.svc.cluster.local">
#   port:     <base64 "5432">
#   database: <base64 "mydb">
#   engine:   <base64 "postgresql">     # postgresql | mariadb
#   dsn:      <base64 "postgres://app:...@host:5432/mydb?sslmode=require">
```

Mount it like any Secret. The platform rewrites the Secret on each rotation,
but a running container does not automatically reload environment variables.
The example below is convenient for startup; roll out the workload after a
rotation so new Pods receive the new values:

```yaml
spec:
  containers:
  - name: app
    image: my-app
    envFrom:
    - secretRef:
        name: api-app-creds
```

After a rotation:

- Secret volume files eventually refresh, but the application must reread them.
- Values injected through `env` or `envFrom` never change in a running
  container; restart or roll out the workload.
- New database connections must use the new password. Existing sessions depend
  on the engine and client and should not be used as a recovery mechanism.

The current controller performs a single-password cutover; `rolling` does not
keep two passwords valid. Choose an interval that leaves time to reload or roll
out clients, then verify a fresh connection with the new credential.

For long intervals, the DBCP reconciler can take up to five minutes to copy a
rotation from OpenBao into the Project Secret. Kubelet propagation adds delay
for mounted volumes. Design for this lag rather than assuming instant rotation.

## Read the current credentials

```bash
kube-dc db credentials get api-app
# Username: app
# Password: ******** (use --show-password to print)
# Host:     api-db-rw.acme-production.svc.cluster.local
# Port:     5432
# DB:       mydb

# Print the password (useful for ad-hoc psql)
kube-dc db credentials get api-app --show-password

# Or as shell-eval-able env vars (requires --show-password)
eval "$(kube-dc db credentials get api-app -o env --show-password)"
```

The `get` reads from OpenBao directly, not from the synced Kubernetes
Secret — so you can verify the source of truth even if the Secret
projection is lagging or sync is disabled.

## Rotate on demand

```bash
kube-dc db credentials rotate api-app
```

This calls OpenBao's `database/rotate-role/<role>` endpoint and creates one new
password. The backend then prompts the controller to refresh the synced Secret.
Reload or roll out clients before they open new connections; there is no
dual-password overlap.

## Inspect

```bash
# List all policies in the project
kube-dc db credentials list

# Detail with status conditions + last/next rotation times
kube-dc db credentials describe api-app
```

Or via kubectl (note the short name `dbcp`):

```bash
kubectl get dbcp
kubectl describe dbcp api-app
```

## Delete

```bash
kube-dc db credentials delete api-app --yes
```

(`--yes` is required — the CLI refuses to delete without explicit
confirmation.) Deleting the DBCP:

- Stops further rotation.
- Removes the projected Kubernetes Secret.
- Does **NOT** drop the database user. That's intentional — the
  platform doesn't own the user identity, so it doesn't have the
  right to delete it. The DBA still controls the user; you control
  the policy.

If you genuinely want the user gone, drop it via your normal DB
admin path after deleting the DBCP.

## Audit

Calls made through the dashboard, CLI, or backend API emit structured audit
events. Automatic controller work is reported through resource status,
conditions, Kubernetes Events, and platform logs rather than the caller audit
stream.

```bash
kube-dc audit list --service db-credentials
```

Audit records include the caller and operation metadata, never the password.

## Dynamic mode is deferred

The `dynamic` mode and lease fields are reserved for API compatibility. A
dynamic policy remains `Ready=False` with reason `DynamicModeDeferred`, and
`kube-dc db credentials issue` does not mint a lease. Use `static-rotated` until
the capability is listed as available in the release notes for your installation.

## Break-glass: direct superuser access (PostgreSQL)

Sometimes you need direct DBA access to a Project database — running
an approved schema migration, investigating a production incident, or
recovering after OpenBao is temporarily unreachable. The `KdcDatabase.spec.breakGlass`
opt-in surfaces a stable `<db>-superuser` Kubernetes Secret with the
`postgres` superuser identity for exactly that:

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: api-db
  namespace: acme-production
spec:
  engine: postgresql
  # ... usual fields ...
  breakGlass:
    enableSuperuserAccess: true       # default false; opt in only when needed
```

When enabled, CNPG provisions a `<db>-superuser` Secret containing
the `postgres` user's password. Anyone with `get, list` on Secrets
in the Project's backing namespace (`admin`, `developer`, or `project-manager`) can read it and run
`psql -U postgres ...`.

Notes:

- **Default is off.** Tenants opt in explicitly per database.
- **The `<db>-rotator` Secret has been there all along** (see "How
  rotation actually works" above), already grants superuser-
  equivalent access via the `kdc_rotator` identity. The
  `breakGlass.enableSuperuserAccess` toggle simply adds a SECOND
  Secret bound to the `postgres` user — useful when you want PG
  audit logs to cleanly distinguish "platform rotation"
  (`kdc_rotator`) from "human ops" (`postgres`). It does not change
  what's *possible* to access; it changes what shows up in audit
  trails.
- **Cannot be changed after creation** for PostgreSQL — CNPG's
  `enableSuperuserAccess` is provisioning-time. Set it at
  `KdcDatabase` creation if you want it on; recreate the DB if you
  change your mind later.
- **MariaDB tenants don't need this knob** — mariadb-operator
  unconditionally provides `<db>-root`, which is already this kind
  of break-glass path.

Do NOT target `kdc_rotator` or `postgres` (or `root` on MariaDB)
from a `DatabaseCredentialPolicy` — those usernames are
admission-blocked. Use this break-glass Secret directly for the
infrequent operator-grade tasks it's there for; let the platform
manage rotation on a dedicated application user.

## Restore from a backup

Two restore paths are supported. **New-name** is the recommended
default; **in-place** is destructive and powerful.

### New-name restore (recommended — safe by construction)

Create a NEW `KdcDatabase` whose `spec.restoreFrom` points at a
completed `Backup` CR (PostgreSQL) or `PhysicalBackup` (MariaDB).
The original database keeps running; the new one bootstraps from
the chosen backup's state.

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: api-db-restored               # NEW name — not the same as source
  namespace: acme-production
spec:
  engine: postgresql
  version: "16"
  replicas: 1
  databaseName: app
  username: app
  storage: 20Gi
  restoreFrom:
    backupName: api-db-scheduled-20260623100000   # Backup CR in the same Project
    sourceDatabaseName: api-db                    # original KdcDatabase
    # Optional PITR (PostgreSQL only):
    # targetTime: "2026-06-23T10:30:00Z"
```

The restored database comes up Ready with the backup's data in
place. Your DBCPs pointing at the original DB keep working
against the original; if you want them against the restored copy,
create new DBCPs referencing the new name.

### In-place restore (destructive — same name, original data gone)

Annotate the existing `KdcDatabase` to overwrite it from a backup.
The platform deletes the underlying database cluster + PVCs,
re-bootstraps from the chosen backup, and keeps the same Service
endpoints + Secret names. **Existing data after the backup is
lost.**

```bash
# 1. Pick a completed Backup CR
kubectl get backups.postgresql.cnpg.io -n acme-production
# (or: kubectl get physicalbackups -n acme-production for MariaDB)

# 2a. Restore exactly to the completed backup
kubectl annotate kdcdatabase api-db \
  kube-dc.com/restore-from=api-db-scheduled-20260623100000 \
  --overwrite

# 2b. Or choose PostgreSQL PITR. Send both annotations in one request;
#     restore-from starts destructive reconciliation immediately.
kubectl annotate kdcdatabase api-db \
  kube-dc.com/restore-from=api-db-scheduled-20260623100000 \
  kube-dc.com/restore-target-time=2026-06-23T10:30:00Z \
  --overwrite
```

What happens:

1. The platform sets `status.restore.phase=InProgress` on the
   `KdcDatabase`.
2. The existing CNPG `Cluster` / MariaDB CR + its PVCs are deleted.
3. A new cluster is recreated, bootstrapping from the named backup.
4. When the cluster reaches Ready, the platform stamps a
   `db.kube-dc.com/restored-at=<RFC3339>` annotation and clears the
   `kube-dc.com/restore-from` trigger.
5. Existing `DatabaseCredentialPolicy` CRs pointing at this database
   stay valid — the platform's stable `<db>-rotator` Secret survives
   the restore cycle unchanged (PITR-safety invariant), so OpenBao
   reconnects with the same management identity it had before.

> **Manual post-restore step required for DBCPs.** When the
> `KdcDatabase` reaches Ready after the restore, the database role's
> password is whatever the backup captured — but OpenBao's
> static-creds entry may still hold the post-backup, pre-restore
> password (OpenBao didn't witness the restore). The DBCP controller
> doesn't watch `KdcDatabase` events, so the projected Kubernetes
> Secret can hold the wrong password until the next reconcile tick
> forces a re-sync.
>
> The reliable path is to manually rotate every affected DBCP once
> the restore completes:
>
> ```bash
> # For each DBCP pointing at the restored DB
> kube-dc db credentials rotate <dbcp-name>
> ```
>
> That triggers OpenBao's `rotate-role`, which writes a fresh
> password to both the database and OpenBao's static-creds, and the
> projected Secret picks it up on the next reconcile (~15s for
> short rotation intervals, up to 5 min for long ones).

What's preserved across an in-place restore:

- `<db>-rotator` Secret (password, rv, annotations — bit-identical)
- `<db>-app` Secret (CNPG/mariadb-operator-managed; reused by
  Bootstrap)
- `<db>-superuser` Secret if break-glass was on (re-provisioned
  with the same password from the backup if encryption-at-rest
  was off; new password if it was on)
- `DatabaseCredentialPolicy` CRs in the project — they keep pointing
  at the restored DB (re-sync requires the manual rotate step above)

What's lost:

- Any data written to the DB *after* the chosen backup. The restore
  intentionally winds back to the backup's consistent point; WAL
  generated post-backup is discarded.
- Active client connections — clients reconnect and pick the new
  password up via the synced Secret (after the manual rotate above
  propagates).

If you want PITR semantics (roll forward to a specific timestamp
*after* the backup's start time), add the
`kube-dc.com/restore-target-time` annotation alongside
`restore-from`. The platform translates that to PostgreSQL's
`recovery_target_time`. PITR is currently PostgreSQL-only; MariaDB
restores always target the backup's consistent point.

### Choosing between the two paths

| Use case | Choose |
|---|---|
| Spin up a copy for analysis without disturbing prod | **new-name** |
| Validate a backup is restorable | **new-name** |
| Run a destructive schema migration with rollback | **new-name** restore the pre-migration backup if needed |
| Production rollback after a bad data event | **in-place** (after confirming the chosen backup is the right point) |
| Wind back time on a development database | **in-place** (cheaper than recreating dependents) |

In-place is the right tool when you *want* the same name and same
Service endpoints. Most other cases want new-name.

## Limits

- **Phase-1 is static-rotated only.** Dynamic mode is field-present
  but not actively issued by the controller; see above.
- **Username must pre-exist.** The platform does NOT create database
  users — bring your own via the DBA / migration path.
- **`kdc_rotator`, `postgres`, and MariaDB `root` are reserved.**
  Admission webhook rejects DBCPs targeting these. They're for
  platform-managed rotation (`kdc_rotator`) or break-glass
  (`postgres`/`root`); regular workloads use a dedicated user.
- **`/rotate?root=true` was retired.** Previous releases exposed a
  `database/rotate-root` path. It was incompatible with PITR (a
  rotated root locks the database out after a restore) and is now
  410-Gone at the backend. If you need to invalidate a leaked
  bootstrap password, the right path is to drop+recreate the user
  via the DBA path, not to rotate root.
- **One policy per database role.** Two DBCPs for the same
  `databaseRef + username` would race; the admission webhook rejects
  duplicates.
- **Credential propagation is not instantaneous.** For long intervals, the
  controller can take up to five minutes to refresh the Project Secret. Mounted
  files then follow kubelet's normal Secret propagation; environment variables
  require a Pod restart. There is no dual-password overlap during this window.

## Reference

- [Managed Databases](managed-databases.md) — the `KdcDatabase` you
  attach DBCPs to
- [Secrets Manager](secrets-manager.md) — for static secrets that
  aren't database credentials
- [KMS](kms.md) — application-level encryption keys
- OpenBao Database engine reference:
  [openbao.org/docs/secrets/databases/](https://openbao.org/docs/secrets/databases/)
