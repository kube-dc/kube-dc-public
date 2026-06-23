# Database Credentials

Kube-DC's Database Credentials feature gives every project a way to
issue, rotate, and revoke database passwords without ever putting one
in a YAML file. The platform owns the password lifecycle — your
workloads consume credentials through a regular Kubernetes `Secret`,
and the values rotate underneath them on a schedule you choose.

Two modes are supported:

- **Static rotated** — long-lived credentials whose password rotates
  on schedule. The username stays the same; only the password changes.
  Safe for legacy apps that can't handle short-lived leases. **Phase 1
  ships static-rotated.**
- **Dynamic** — short-lived credentials minted on demand for each
  client lease. Each lease has its own unique username + password,
  expires after a TTL, and is revoked on lease close. **Phase 2 — the
  field exists in the spec, but `kube-dc db credentials issue`
  returns a deferred-state notice for now.**

Database Credentials are scoped to a `KdcDatabase` in your project.
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
  namespace: my-project
spec:
  databaseRef:
    name: api-db                   # KdcDatabase in the same namespace
  mode: static-rotated
  username: app
  rotation:
    interval: 30d
    strategy: rolling              # rolling | immediate
  sync:
    enabled: true
    targetSecretName: api-app-creds
```

Key fields:

- **databaseRef** — `KdcDatabase` in the same project namespace.
  Cross-namespace references are impossible by API shape; an
  admission webhook verifies the referenced database actually exists.
- **mode** — `static-rotated` (Phase 1) or `dynamic` (Phase 2).
- **username** — the database username to manage. Must already exist
  in the database (Kube-DC does NOT create users in static-rotated
  mode — that prevents the platform from owning user identities and
  keeps the DBA in control). Defaults to `app`.
- **rotation.interval** — how often the password rotates (e.g. `30d`,
  `7d`).
- **rotation.strategy** — `rolling` (write the new password, the
  underlying OpenBao role keeps the previous one accepted briefly so
  in-flight deploys overlap) or `immediate` (atomic swap; clients
  must reconnect right away).
- **sync.enabled** — projects the username + password into a
  Kubernetes `Secret` your pods can mount. The Secret is rewritten
  in place on every rotation.

## Permissions

| Role | DatabaseCredentialPolicy CRD | Read current password | Rotate now | Delete |
|---|---|---|---|---|
| Project Manager | full | ✅ | ✅ | ✅ |
| Developer | read | ✅ | ✅ | ✅ |
| Viewer | read | — | — | — |

The platform-side OpenBao policy that backs each project grants
`database/static-creds/*` (read) + `database/rotate-role/*` (update)
to the developer tier, so application code can read and force a
rotation without project-manager credentials.

### Trust model (read this if you share a project)

> **On Kube-DC, anyone with `developer` or `manager` role on a
> project namespace has effective superuser access to databases
> owned by that project.** Tenant separation is enforced at the
> **project boundary** — not within the project. To restrict who can
> read database credentials, restrict who is granted `developer` or
> `manager` on the project.

This applies to every database in your project (PostgreSQL and
MariaDB alike). The underlying mechanism is K8s default RBAC: both
roles include `get, list` on Secrets in the project namespace, and
Kube-DC's database engines (CNPG's `<db>-rotator` for PostgreSQL,
mariadb-operator's `<db>-root` for MariaDB) live as Secrets in that
namespace. Anyone who can read those Secrets can connect as a
database superuser.

If you need a SOC2 / compliance posture where Secret readers ≠ DB
superusers, today's answer is "don't give them developer/manager."
A future release will offer a sub-superuser rotator option for
compliance environments; until it lands, the project boundary IS
the trust boundary.

## How rotation actually works (mental model)

You don't need to know any of this to use the feature — but it
helps when something goes wrong.

Every PostgreSQL `KdcDatabase` automatically gets a dedicated
**`kdc_rotator`** PostgreSQL role at creation time. This is the
identity OpenBao logs in as to run the `ALTER USER ... PASSWORD`
that rotates your application user's password. Its own credential
lives in a `<db>-rotator` Kubernetes Secret in your project
namespace and **is never rotated** — that's a deliberate invariant
that keeps point-in-time recovery safe (if we rotated the rotator,
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
# Connect to your managed database however you normally would
# (port-forward, kubectl exec, your IDE, etc.) and run:
psql -c "CREATE USER app WITH LOGIN PASSWORD 'temporary-bootstrap';"
psql -c "GRANT ALL PRIVILEGES ON DATABASE mydb TO app;"
# (Or the MariaDB equivalent.)
```

The bootstrap password gets immediately rotated to something only
OpenBao knows once the DatabaseCredentialPolicy reconciles. From that
point on, you never see the password in plaintext anywhere except in
the synced Secret.

## Create a policy

### Via the CLI

```bash
# Static-rotated, 30-day rotation, projected into a K8s Secret
kube-dc db credentials create api-app \
  --database=api-db \
  --mode=static-rotated \
  --username=app \
  --rotate=30d

# With rolling strategy (overlapping passwords during deploy)
kube-dc db credentials create batch-app \
  --database=api-db \
  --username=batch \
  --rotate=7d \
  --rotate-strategy=rolling
```

### Via kubectl

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: api-app
  namespace: my-project
spec:
  databaseRef:
    name: api-db
  mode: static-rotated
  username: app
  rotation:
    interval: 30d
    strategy: rolling                # rolling | immediate
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
#   host:     <base64 "api-db-rw.my-project.svc.cluster.local">
#   port:     <base64 "5432">
#   database: <base64 "mydb">
#   engine:   <base64 "postgresql">     # postgresql | mariadb
#   dsn:      <base64 "postgres://app:...@host:5432/mydb?sslmode=require">
```

Mount it like any Secret — the platform rewrites it in place on each
rotation, so a pod that fetches the value on each connection picks
up the new password automatically:

```yaml
spec:
  containers:
  - name: app
    image: my-app
    envFrom:
    - secretRef:
        name: api-app-creds
```

For workloads that hold connections open across rotations, you have
two strategies:

1. **Reconnect on auth error** — your client code catches an auth
   failure, re-reads the Secret, reconnects. Works with rotation
   strategy `immediate`.
2. **Use `rolling` strategy** — OpenBao briefly accepts both the old
   and new passwords during the changeover, so a standard rolling
   Deployment can churn pods in that window without anyone seeing an
   auth failure. Long-running pods reload the Secret via the kubelet
   inotify path and pick up the new password naturally.

> **How long the Secret can be stale (two hops, not one).** When
> OpenBao rotates the database role, the new password lands in the
> projected Kubernetes Secret on the next DBCP reconcile, then in
> mounted pod volumes on the next kubelet inotify pass.
>
> - **OpenBao → K8s Secret**: ~15s for short rotation intervals
>   (rotation-aware requeue), up to 5 min for long intervals
>   (steady-state reconcile ceiling). The platform also re-reads
>   if anything edits the projected Secret directly.
> - **K8s Secret → pod volume**: kubelet propagates within ~60s
>   for mounted files; `envFrom` env vars require a pod restart.
>
> For 30s-1m rotation intervals the total round-trip is typically
> under 30s. For 30d intervals it can be up to 5min + kubelet lag.

## Read the current credentials

```bash
kube-dc db credentials get api-app
# Username: app
# Password: ******** (use --show-password to print)
# Host:     api-db-rw.my-project.svc.cluster.local
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

This calls OpenBao's `database/rotate-role/<role>` endpoint, which
generates a new password, updates the database, then writes it back
to the synced Secret. With `strategy: rolling`, OpenBao briefly keeps
the previous password accepted alongside the new one so in-flight
clients can finish reconnecting.

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

Every `Create`, `Rotate`, `RotateFailed`, and `Delete` emits an audit
event:

```bash
kube-dc audit list --resource=DatabaseCredentialPolicy --since=24h
```

Logs the calling identity, the database, the username, and the policy
operation. Rotation events also log the OpenBao lease ID for
correlation with platform-side audit.

## Dynamic mode (Phase 2 preview)

Dynamic mode mints a short-lived lease for every consumer:

```yaml
spec:
  databaseRef:
    name: api-db
  mode: dynamic
  role: my-app-readonly             # OpenBao Database role name
  ttl: 1h
  maxTtl: 24h
```

When this lands, your code will fetch a fresh lease per invocation
(or per connection pool initialization) via:

```bash
kube-dc db credentials issue api-app
# username: v-token-my-app-r-xxxx
# password: ...
# lease_id: database/creds/my-app-readonly/abcd
# lease_duration: 3600
```

Each lease has a unique username, so revocation is per-client.
Workloads use the OpenBao SDK directly (similar to the
[KMS](kms.md) examples) to issue leases on demand. The role itself
must be declared by the project-manager via `spec.role` in the DBCP.

Phase-2 ships the lease-issue path; the policy lifecycle is the same
as static-rotated today, with `kube-dc db credentials issue`
returning a `DeferredFeature` notice until then. Phase-1 production
clusters are all static-rotated.

## Break-glass: direct superuser access (PostgreSQL)

Sometimes you need direct DBA access to a tenant database — running
a schema migration tool from your laptop, debugging an
unrescheduable production query, or recovering after OpenBao is
temporarily unreachable. The `KdcDatabase.spec.breakGlass`
opt-in surfaces a stable `<db>-superuser` Kubernetes Secret with the
`postgres` superuser identity for exactly that:

```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: api-db
  namespace: my-project
spec:
  engine: postgresql
  # ... usual fields ...
  breakGlass:
    enableSuperuserAccess: true       # default false; opt in only when needed
```

When enabled, CNPG provisions a `<db>-superuser` Secret containing
the `postgres` user's password. Anyone with `get, list` on Secrets
in the project namespace (developer/manager) can read it and run
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
  namespace: my-project
spec:
  engine: postgresql
  version: "16"
  replicas: 1
  databaseName: app
  username: app
  storage: 20Gi
  restoreFrom:
    backupName: api-db-scheduled-20260623100000   # Backup CR in same namespace
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
kubectl get backups.postgresql.cnpg.io -n my-project
# (or: kubectl get physicalbackups -n my-project for MariaDB)

# 2. Trigger the in-place restore
kubectl annotate kdcdatabase api-db \
  kube-dc.com/restore-from=api-db-scheduled-20260623100000 \
  --overwrite

# Optional PITR (PostgreSQL only):
# kubectl annotate kdcdatabase api-db \
#   kube-dc.com/restore-target-time=2026-06-23T10:30:00Z \
#   --overwrite
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
- **Synced Secret is at most 1 minute stale.** Kubelet refreshes
  Secret mounts on inotify; long-running pods see the rotation
  within ~60s. Use `rolling` strategy or app-level reconnect to
  bridge the gap.

## Reference

- [Managed Databases](managed-databases.md) — the `KdcDatabase` you
  attach DBCPs to
- [Secrets Manager](secrets-manager.md) — for static secrets that
  aren't database credentials
- [KMS](kms.md) — application-level encryption keys
- OpenBao Database engine reference:
  [openbao.org/docs/secrets/databases/](https://openbao.org/docs/secrets/databases/)
