---
name: manage-database-credentials
description: Create and manage Kube-DC DatabaseCredentialPolicy (DBCP) resources — project-scoped CRDs that rotate database passwords on a tenant-chosen schedule and project them into a Kubernetes Secret. Works against KdcDatabase (PostgreSQL via CNPG / MariaDB via mariadb-operator). Use this for application credentials on managed databases. For the database itself use create-database. For TLS use manage-certificates. For application secrets unrelated to databases use manage-secrets.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- **A KdcDatabase exists and is Ready in the same namespace.** Create it via the `create-database` skill first if not.
- **The database user the policy will rotate must already exist** — the platform does NOT create users in static-rotated mode. Connect to the database (e.g. via the project's `<db>-rotator` Secret if you have developer/manager access, or via your DBA path) and `CREATE USER` before the DBCP can reach Ready.

## Key Concepts

- **DatabaseCredentialPolicy (`dbcp`)** — project-scoped CRD in `security.kube-dc.com/v1alpha1`. Names a target `KdcDatabase` + username, declares a rotation schedule, optionally projects the credentials into a Kubernetes Secret for workloads to mount.
- **mode** — `static-rotated` (long-lived credentials, password rotates on schedule, username stays the same) — Phase 1, the supported mode. `dynamic` (short-lived per-lease) — Phase 2 preview; field present, controller defers issuance.
- **rotation.strategy** — `rolling` (briefly accepts both old and new passwords during changeover, safe for rolling Deployments) or `immediate` (atomic swap, clients must reconnect).
- **sync.targetSecretName** — when sync is enabled, the platform writes a regular Kubernetes Secret with username/password/host/port/dbname/uri keys. Rewritten in place on each rotation.
- **`kdc_rotator` / `postgres` / `root` are RESERVED** — the admission webhook rejects DBCPs targeting these usernames. They are platform-internal (rotator) or break-glass (postgres/root) identities; rotate a dedicated application user instead.

## Trust model (read before you grant project access)

On Kube-DC, **anyone with `developer` or `manager` role on a project namespace has effective superuser access to databases owned by that project.** Tenant separation is enforced at the project boundary, not within. To restrict who can read database credentials, restrict who is granted developer/manager on the project. This applies to PostgreSQL and MariaDB equally; both engines surface their high-privilege credential as a Secret in the project namespace (`<db>-rotator` / `<db>-root`) and default RBAC grants `get, list` on Secrets to the dev/manager tier.

## Create a DBCP

### 1. Prepare the user

```bash
# Connect to the DB and create the user the policy will rotate.
# (One-time bootstrap — the platform rotates the password from here onward.)
psql -h {db}-rw.{project-namespace}.svc -U postgres -c \
  "CREATE USER app WITH LOGIN PASSWORD 'temporary-bootstrap';"
psql -h {db}-rw.{project-namespace}.svc -U postgres -c \
  "GRANT ALL PRIVILEGES ON DATABASE app TO app;"
```

(For MariaDB use `mysql -u root -p ... CREATE USER ... ; GRANT ...`.)

### 2. Apply the DBCP

See @dbcp-template.yaml for a fully-annotated template.

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: {dbcp-name}
  namespace: {project-namespace}
spec:
  databaseRef:
    name: {kdcdatabase-name}             # KdcDatabase in same namespace
  mode: static-rotated
  username: app                          # NOT kdc_rotator/postgres/root
  rotation:
    interval: 30d                        # KubeDC duration; 1m..730d
    strategy: rolling                    # rolling | immediate
  sync:
    enabled: true
    targetSecretName: {target-secret}    # K8s Secret the platform projects credentials into
```

```bash
kubectl apply -f dbcp.yaml
```

### 3. Wait for Ready

```bash
kubectl get dbcp {dbcp-name} -n {project-namespace} -w
# NAME     DATABASE   MODE             USERNAME   AGE   READY
# {name}   {db}       static-rotated   app        15s   True
```

## Read / Use Credentials

### Via the synced Kubernetes Secret (preferred — workloads consume this)

```yaml
spec:
  containers:
  - name: app
    image: my-app
    envFrom:
    - secretRef:
        name: {target-secret}            # the targetSecretName from the DBCP
```

The Secret is `type: Opaque` and carries these keys (verbatim from the controller's `secretData()`):

| Key | Value |
|---|---|
| `username` | the username `spec.username` from the DBCP |
| `password` | the current rotated password |
| `host` | engine's RW Service hostname (e.g. `api-db-rw.my-project.svc`) |
| `port` | engine port (`5432` PG, `3306` MariaDB) |
| `database` | the database name from the parent `KdcDatabase.spec.databaseName` |
| `engine` | `postgresql` or `mariadb` (handy for engine-aware connection clients) |
| `dsn` | ready-to-use connection string (e.g. `postgres://user:pass@host:5432/db?sslmode=require`) |

Mount as env vars, file volumes, whatever your app expects. The Secret is rewritten in place on each rotation.

**Staleness has two hops, not one**:

1. **OpenBao → Kubernetes Secret** — the DBCP controller reconciles on a tightened schedule when rotation is imminent (rotation-aware requeue, ~15s for short intervals) and otherwise on the 5-minute ceiling. So a server-side rotation typically lands in the K8s Secret within 15s on short intervals, or up to 5m on long ones.
2. **Kubernetes Secret → pod volume** — kubelet propagates Secret-mount changes within ~60s via inotify (env vars require a Pod restart).

For workloads holding connections open across rotations, use the `rolling` strategy or reconnect on auth-error.

### Via the CLI (for ad-hoc operator use)

```bash
# Hidden password (default)
kube-dc db credentials get {dbcp-name}

# Reveal password
kube-dc db credentials get {dbcp-name} --show-password

# Shell-eval as env vars (e.g. for ad-hoc psql)
eval "$(kube-dc db credentials get {dbcp-name} -o env --show-password)"
```

## Rotate on Demand

```bash
# Force a rotation NOW (does not change the schedule)
kube-dc db credentials rotate {dbcp-name}
```

This calls OpenBao's `database/rotate-role/<role>`, which generates a new password, updates the database via the platform-managed rotator identity, and writes the new password back to the synced Secret. With `strategy: rolling`, OpenBao briefly accepts both the old and the new password so in-flight clients can finish reconnecting.

## List / Describe

```bash
kube-dc db credentials list
kube-dc db credentials describe {dbcp-name}    # status conditions + last/next rotation
kubectl get dbcp -n {project-namespace}        # short name 'dbcp' works in kubectl
```

## Delete

```bash
# Requires --yes; refuses without explicit confirmation.
kube-dc db credentials delete {dbcp-name} --yes
```

Deleting the DBCP stops rotation and removes the synced Kubernetes Secret. It does **NOT** drop the database user — the platform doesn't own user identities, so it doesn't have the right to delete them. If you want the user gone, drop it via your DBA path after deleting the DBCP.

## Restore semantics

If the parent KdcDatabase is restored, DBCPs pointing at it stay valid as CRDs — but **the projected Secret can hold the wrong password for a window after restore**, and there's a manual step to converge it. Read this carefully.

### What survives the destroy + rebuild cycle

- **`<db>-rotator` Secret** (PG): bit-identical (PITR-safety invariant). OpenBao reconnects with the same management identity it had before.
- **`DatabaseCredentialPolicy` CRs**: untouched. They still reference the same DB.
- **OpenBao static-creds entry**: still there.

### What goes out of sync (and why you need a manual step)

After an **in-place restore**, the underlying database role's password is whatever the backup captured — but **OpenBao's static-creds entry may still hold the post-backup, pre-restore password** because OpenBao didn't witness the restore. The DBCP controller does NOT watch `KdcDatabase` restore completion; it only re-reads OpenBao on its periodic reconcile (rotation-aware tick or 5-minute ceiling). So:

- The projected K8s Secret will keep holding the pre-restore password until either (a) the next rotation tick fires and forces a re-sync via `rotate-role`, or (b) you force one manually.
- During that window, application auth against the restored DB will fail with `password authentication failed` because the DB has the backup's password and the Secret has the pre-restore one.

**The reliable post-restore step** is to manually rotate every affected DBCP once the KdcDatabase reaches Ready:

```bash
# For each DBCP pointing at the restored DB
kube-dc db credentials rotate {dbcp-name}

# Then verify app auth using the projected Secret
PASS=$(kubectl -n {ns} get secret {target-secret} -o jsonpath='{.data.password}' | base64 -d)
kubectl -n {ns} run probe-{rand} --rm -i --restart=Never \
  --image=postgres:16-alpine --env=PGPASSWORD="$PASS" --command -- \
  psql -h {db}-rw.{ns}.svc -U {dbcp-username} -d {database} -tAc "SELECT 'ok'"
```

`kube-dc db credentials rotate` calls OpenBao's `database/rotate-role/<role>`. OpenBao generates a fresh password, writes it to the database role via the rotator identity (which is now the restored DB's rotator, same password as before the restore — that's the PITR-safety invariant in action), and persists the new password. The DBCP controller then projects it into the K8s Secret on the next reconcile.

### New-name restore (`spec.restoreFrom` on a fresh KdcDatabase)

The original DBCPs continue pointing at the original KdcDatabase, which keeps running. If you want DBCPs against the restored copy, create new DBCPs referencing the new database name. No manual rotate needed on the original side; the source DB is untouched.

See the [Database Credentials docs](https://docs.kube-dc.com/cloud/database-credentials/) for the full restore + break-glass story.

## Verification

After creating a DBCP:

```bash
# 1. DBCP is Ready
kubectl get dbcp {dbcp-name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# Expected: True

# 2. Projected Kubernetes Secret exists
kubectl get secret {target-secret} -n {project-namespace}
# Expected: type=Opaque, with username/password/host/port/database/engine/dsn keys

# 3. Database user authenticates with the projected password
PASS=$(kubectl -n {project-namespace} get secret {target-secret} \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl -n {project-namespace} run probe-{rand} --rm -i --restart=Never \
  --image=postgres:16-alpine --env=PGPASSWORD="$PASS" --command -- \
  psql -h {db}-rw.{project-namespace}.svc -U app -d app -tAc "SELECT 'ok'"
# Expected: ok
```

**Success**: DBCP Ready, Secret present with valid credentials, PG/MariaDB authenticates as the target user with the projected password.

**Common failure modes** (reasons match controller constants in `security.kube-dc.com/v1alpha1`):

- **`Ready=False / Reason=DatabaseEngineUnconfigured`** — `db-manager` hasn't yet stamped `KdcDatabase.status.openBaoEngineConfigured=true`. Wait for the parent `KdcDatabase` to reach Ready; the engine config is written during that bootstrap. If the database is Ready but the condition stays false, the `db-manager` controller is wedged — check `kubectl -n kube-dc logs deploy/kube-dc-db-manager` for the reconcile error.
- **`Ready=False / Reason=DatabaseNotFound`** — `spec.databaseRef.name` doesn't resolve in the project namespace. The admission webhook normally rejects this on create; this reason covers the rare race where the `KdcDatabase` is deleted while a DBCP still references it. Fix the ref or delete the DBCP.
- **Admission rejection at create — `spec.username: <reserved>` is reserved** — webhook rejects DBCPs targeting `kdc_rotator` (PG, platform rotator), `postgres` (PG break-glass), or `root` (MariaDB break-glass). Change to a dedicated application user.
- **`Ready=False / Reason=RoleProvisioning`** — OpenBao's `database/static-role` write succeeded but the first read of credentials hasn't returned a password yet. Usually transient; clears within a reconcile or two. If it persists for more than a minute, the OpenBao rotator identity probably can't reach the database — check `kubectl -n openbao logs <leader-pod>` for `ALTER USER` errors and the `connection_url` config.
- **`Ready=False / Reason=TargetSecretConflict`** — the `sync.targetSecretName` already exists and is owned by another controller. Pick a different name or delete the conflicting Secret.
- **`status.lastRotatedTime` never advances AND OpenBao logs show `SQLSTATE 28P01 password authentication failed`** — pre-v0.1.5 self-rotation deadlock. Verify `db-manager` image is `v0.1.5` or later; older releases are known broken and the fix landed 2026-06-22.

## Audit

Every Create / Rotate / RotateFailed / Delete emits an audit event:

```bash
kube-dc audit list --resource=DatabaseCredentialPolicy --since=24h
```

Records the calling identity, the database, the username, the operation, and (for rotation) the OpenBao lease ID for correlation with platform audit logs.

## Safety

- **Don't target reserved usernames** — `kdc_rotator` (platform rotator), `postgres` (PG break-glass), `root` (MariaDB break-glass). The admission webhook rejects them, but knowing why saves a confusing error.
- **Don't manually edit the synced Kubernetes Secret.** The next rotation overwrites it.
- **`/rotate?root=true` was retired.** Earlier releases exposed a `database/rotate-root` path. It was incompatible with PITR and is now 410-Gone at the backend. If you need to invalidate a leaked bootstrap password, drop and recreate the user via the DBA path, not via root rotation.
- **One DBCP per `(databaseRef, username)` pair.** Two DBCPs racing on the same user is an admission error.
- **Short rotation intervals tax OpenBao.** `interval: 30s` works but pounds the database with `ALTER USER` calls. Use minutes for testing, days for production.
- **Rotation events show in `kube-dc audit`** — useful for compliance attestations. Cross-reference with the OpenBao platform audit log via the lease ID.
