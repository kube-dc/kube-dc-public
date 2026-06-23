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

The Secret carries `username / password / host / port / dbname / uri`. Mount it as env vars, file volumes, whatever your app expects. The Secret is rewritten in place on every rotation; long-running pods see the new password within ~60s via the kubelet inotify path.

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

If the parent KdcDatabase is restored (in-place via `kube-dc.com/restore-from` annotation, or new-name via a fresh KdcDatabase with `spec.restoreFrom`), DBCPs pointing at the database continue to work:

- **In-place restore**: the platform's `<db>-rotator` Secret survives the restore cycle bit-identical (PITR-safety invariant). OpenBao reconnects with the same management identity it had before; the DBCP's projected Secret re-syncs to the restored DB's current password automatically. Existing client connections must reconnect — the projected Secret may briefly hold a stale password until the next rotation tick (≤ `rotation.interval`).
- **New-name restore**: the original DBCPs point at the original KdcDatabase, which keeps running. If you want DBCPs against the restored copy, create new DBCPs referencing the new database name.

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
# Expected: type=kubernetes.io/basic-auth, with username/password/host/port/dbname/uri keys

# 3. Database user authenticates with the projected password
PASS=$(kubectl -n {project-namespace} get secret {target-secret} \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl -n {project-namespace} run probe-{rand} --rm -i --restart=Never \
  --image=postgres:16-alpine --env=PGPASSWORD="$PASS" --command -- \
  psql -h {db}-rw.{project-namespace}.svc -U app -d app -tAc "SELECT 'ok'"
# Expected: ok
```

**Success**: DBCP Ready, Secret present with valid credentials, PG/MariaDB authenticates as the target user with the projected password.

**Common failure modes**:

- **Stuck `Ready=False / Reason=EngineSecretNotReady`** — the platform's rotator Secret (`<db>-rotator` for PostgreSQL) hasn't been provisioned yet. Wait for the parent KdcDatabase to reach Ready; the rotator Secret is created during that bootstrap. If the database is Ready but the rotator Secret is missing, the db-manager controller is wedged; check `kubectl -n kube-dc logs deploy/kube-dc-db-manager` for the reconcile error.
- **Stuck `Ready=False / Reason=ReservedUser`** — admission webhook rejected the DBCP because `spec.username` is one of `kdc_rotator` / `postgres` / `root`. Change to a dedicated application user.
- **Stuck `Ready=False / Reason=UserNotFound`** — the username doesn't exist in the database yet. Create it via your DBA path (see "Prepare the user" above).
- **`status.lastRotatedTime` never advances and OpenBao logs show `SQLSTATE 28P01`** — pre-v0.1.5 self-rotation deadlock. Verify db-manager image is `v0.1.5` or later; older releases are known broken and the fix landed 2026-06-22.

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
