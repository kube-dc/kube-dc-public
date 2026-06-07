# Secrets Manager

Kube-DC's Secrets Manager gives every project a built-in, audit-grade place to keep sensitive values — API tokens, database credentials, TLS keys — without scattering them across raw Kubernetes `Secret` objects or stuffing them into git. Values live in the platform's encrypted store (OpenBao) and can be projected into a Kubernetes `Secret` automatically when your workloads need them.

You can manage secrets through the **dashboard**, the **`kube-dc` CLI**, or the **HTTP API**. All three surfaces talk to the same backend, so changes made in one show up immediately in the others.

## Concepts

A **ManagedSecret** is a CRD in your project that describes *intent*:

- **what** the secret is — name, type (`opaque`, `password`, `api-key`, `tls`, `db-static`), optional description
- **how** it should be projected — sync on or off; target `Secret` name; ESO refresh interval; optional key allowlist
- **whether** to rotate — opt-in scheduled rotation for `type=password`

Values themselves never live in the CRD. They're written through the platform API and stored in OpenBao under a per-project KV mount (`kv-<project>`). When sync is enabled, the Kube-DC controller wires an External Secrets Operator `ExternalSecret` that materializes the values into a regular Kubernetes `Secret` your pods can mount via `envFrom`, `env.valueFrom`, or a Secret volume — exactly like they would with any other Secret.

Every read, write, sync change, import, and destroy emits a structured audit event you can query with `kube-dc audit list`.

## Secrets Manager view

In your project's sidebar, click the **key icon** to open the Secrets Manager. You'll see one row per secret with the name, type, sync state, target Kubernetes `Secret` name, and a **Reveal** action.

The sidebar tree under **Secrets** lists every secret in the project. The status dot next to each name tells you at a glance:

- 🟢 **green** — sync is enabled and the Kubernetes `Secret` has been projected
- 🟡 **amber** — sync is enabled but the projection isn't ready yet (initial reconcile, or ESO is retrying)
- ⚪ **grey** — sync is disabled; values are platform-store only

Click a secret in the tree (or click its name in the table) to open the detail view with full metadata, a **Reveal values** button, and a **Used by** panel listing every Deployment, StatefulSet, DaemonSet, Job, CronJob, and Pod in the project that references the synced `Secret`.

## Create a secret

### Via the dashboard

1. Open the **Secrets Manager** view in your project.
2. Click **Create secret**.
3. Fill in the form:
   - **Name** — a valid Kubernetes name (lowercase letters, digits, hyphens, dots; up to 253 chars).
   - **Type** — pick `opaque` unless you're storing one of the higher-shape types.
   - **Description** — optional, surfaced in the UI for context.
   - **Sync to Kubernetes Secret** — leave on (default) to project the values into a regular `Secret`; turn off to keep values platform-only.
   - **Target Secret name** + **Refresh interval** — optional overrides; defaults to the secret name and 1 hour.
   - **Seed initial values** — toggle on and add `KEY=value` rows to write the first version atomically with the create.
4. Click **Create**.

If you provided initial values, the secret is created and its first version is written in one round trip. Otherwise the secret starts empty and you can populate it later with the CLI (`kube-dc secrets put …`).

### Via the CLI

```bash
# Empty secret, sync enabled, target defaults to the secret name:
kube-dc secrets create db-creds

# Seed the first version inline:
kube-dc secrets create app-config \
  --from-literal=DATABASE_URL=postgres://... \
  --from-file=tls.crt=./tls.crt

# Seed from a .env file:
kube-dc secrets create app-env --from-env-file=./app.env

# No sync — only readable via "kube-dc secrets get --value":
kube-dc secrets create api-keys --sync-disabled
```

## Import an existing Kubernetes Secret

Already have a raw `Secret` in your project namespace? Import it so the platform takes over its lifecycle.

### Via the dashboard

1. Open the **Secrets Manager** view.
2. Click **Import existing Secret**.
3. Enter the **source Kubernetes Secret name**. Same-namespace by default; tick **Cross-namespace import** and enter the source namespace if it lives elsewhere (audit-visible).
4. Optionally rename the managed secret (defaults to the source name) and pick a type.
5. Click **Import**.

The import reads every key from the source `Secret`, writes them to the platform store, creates the matching `ManagedSecret` CR, and turns on sync so the original `Secret` keeps existing (now owned by the platform). Failures roll back cleanly — no orphan KV paths are left behind.

### Via the CLI

```bash
kube-dc secrets import app-config --from legacy-app-credentials
```

## Read values

Values are hidden by default everywhere. To see them:

- **Dashboard** — click **Reveal** on a row, or **Reveal values** in the detail view. Each value renders with a one-click copy button. Values are re-hidden automatically if the page reloads or the data refreshes.
- **CLI** — `kube-dc secrets get <name> --value` prints the values as a key/value list. Without `--value` you get the metadata only.
- **API** — `GET /api/secrets/:project/:name?includeValue=true` returns `value.data` as a `{key: value}` map.

Every value-read attempt emits an audit event tagged with your identity, the secret name, and (if applicable) the elevation_id of the active org-admin elevation window.

## Use a synced value in a workload

When sync is enabled, a regular Kubernetes `Secret` is created and kept in sync with the platform store by the External Secrets Operator. Reference it the same way you reference any other `Secret`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-app:latest
        envFrom:
        - secretRef:
            name: db-creds          # the ManagedSecret's target name
```

The **Used by** panel on the secret's detail view lists every workload in the project that references the synced `Secret` so you can see the blast radius before rotating or destroying it.

## Rotate a password automatically (preview)

For `type=password` secrets you can ask the platform to generate a new value on a schedule:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedSecret
metadata:
  name: app-password
spec:
  type: password
  rotation:
    enabled: true
    interval: 90d
    generator:
      length: 32
      charset: alnum-symbol           # alnum | alnum-symbol | hex
  sync:
    enabled: true
    targetSecretName: app-password
```

The first version is written when the resource is created. Subsequent versions appear on the schedule. Workloads pick up the new value on the next ESO refresh (default 1 hour; configurable via `spec.sync.refreshInterval`).

The Kube-DC CLI's `kube-dc secrets get app-password --value` always shows the current version. Older versions remain readable via the API for the secret's KV history window.

## Delete a secret

Two flavours:

- **Soft delete** — removes the `ManagedSecret` and the projected Kubernetes `Secret`, but keeps the value history in the platform store so an admin can restore it.
- **Destroy** — `kube-dc secrets delete <name> --destroy` (admin-only) wipes both the resource and every version of the stored values irreversibly.

The dashboard's delete action soft-deletes by default; destroy must be done via the CLI or HTTP API with `?destroy=true` so the irreversible step is explicit.

## Permissions

Secret operations follow your project role:

| Role | List | Read metadata | Reveal values | Write | Delete | Destroy |
|---|---|---|---|---|---|---|
| `user` | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| `developer` | ✅ | ✅ | ✅ (own project) | ✅ | ✅ (soft) | ❌ |
| `project-manager` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| `admin` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ (destroy) |

**Organization admins** see metadata across every project in the org by default but can't reveal values without first calling:

```bash
kube-dc orgs elevate <project> --reason "incident IR-2026-05-12"
```

This opens a 15-minute audit-tagged window where the org-admin can read values. The reason is stored on every value-read audit event during the window so compliance review can correlate. Close the window early with `kube-dc orgs release <project>`.

## Audit

The audit stream captures every operation:

```bash
# All secret events in this project in the last hour:
kube-dc audit list --service secrets --since 1h

# Org-wide view (org-admin only):
kube-dc audit list --org --service secrets

# CSV export for compliance review:
kube-dc audit list --csv --output-file incident-2026-05-12.csv
```

Every event includes `actor`, `actor_email`, `action`, `result`, `resource`, `request_id`, `source_ip`, and (for value reads inside an elevation) `elevation_id`. **No secret values ever appear in the audit log.**

## Tips

- **Cross-project copies** — to move a secret between projects, `kube-dc secrets get --value -o yaml` in the source project, then `kube-dc secrets create … --from-literal=…` in the target. The platform deliberately doesn't expose a one-step cross-project copy to keep the audit trail unambiguous.
- **Diff before destroy** — `kube-dc secrets consumers <name>` lists every workload referencing the synced `Secret`. Always check this before `--destroy`.
- **Org-admin reading own projects** — if you're org-admin and also a member of a project's developer/admin group, you can read values directly without elevation. Elevation is only required when accessing projects you don't otherwise have project-level access to.

## When to use Secrets Manager vs. other features

The Secrets Manager is for **values you store** — API tokens, signing
keys, OAuth client secrets. Three sibling features cover related but
distinct needs:

| Feature | Use when |
|---|---|
| [KMS](kms.md) | You want to encrypt opaque payloads or wrap your own data keys on the fly (not store them). The platform never sees plaintext. |
| [Certificate Manager](certificate-manager.md) | You need x509 certs (TLS server, mTLS, code signing). Cert renewal is automatic. |
| [Database Credentials](database-credentials.md) | The "secret" is a database password whose lifecycle is tied to an actual DB user. The platform rotates the password on schedule. |

Use Secrets Manager when none of those fit — short-lived OAuth tokens
from your IdP, third-party API keys, SSH host keys, GPG signing keys,
etc. Anything you'd otherwise jam into a YAML file or git-crypt.

## Reference

- **CLI** — `kube-dc secrets --help`
- **CRD** — `ManagedSecret` in API group `security.kube-dc.com/v1alpha1`
- **HTTP API** — see your cluster's backend at `https://backend.<your-domain>/api/secrets/*`
- **Related** — [KMS](kms.md) · [Certificate Manager](certificate-manager.md) · [Database Credentials](database-credentials.md)
