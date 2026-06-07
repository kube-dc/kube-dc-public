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

> **NOTE — rotating the engine-root password is project-admin only.**
> The CLI exposes `kube-dc db credentials rotate <name> --root`, which
> calls OpenBao's `database/rotate-root` against the configured
> connection. This is destructive: the platform retains the new root
> password but the human who set up the database no longer knows it.
> Use it only when you need to invalidate the original bootstrap
> password (e.g. it leaked into git). For day-to-day operations,
> manage a dedicated user (`username: app` or similar) instead — its
> rotation is non-destructive and the standard recipe below.

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
a regular Kubernetes `Secret`:

```bash
kubectl get secret api-app-creds -o yaml
# data:
#   username: <base64 "app">
#   password: <base64 "rotated password">
#   host:     <base64 "api-db-rw.my-project.svc.cluster.local">
#   port:     <base64 "5432">
#   dbname:   <base64 "mydb">
#   uri:      <base64 "postgresql://app:...@host:5432/mydb">
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
   inotify path (~1 min lag) and pick up the new password naturally.

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

## Limits

- **Phase-1 is static-rotated only.** Dynamic mode is field-present
  but not actively issued by the controller; see above.
- **Username must pre-exist.** The platform does NOT create database
  users — bring your own via the DBA / migration path.
- **`rotate-root` is forbidden.** Use a dedicated user, not the root.
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
