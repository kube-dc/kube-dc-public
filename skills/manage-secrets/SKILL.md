---
name: manage-secrets
description: Create and manage Kube-DC ManagedSecrets — project-scoped secrets backed by OpenBao, optionally projected into a Kubernetes Secret via External Secrets Operator. Use this for storing API tokens, OAuth client secrets, signing keys, third-party credentials. For database passwords use create-database. For encryption keys use manage-kms. For TLS certificates use manage-certificates.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- OpenBao must be enabled on the cluster (check with `kubectl -n kube-dc get secret master-config -o jsonpath='{.data.enable_openbao}' | base64 -d` → expect `true`)

## Key Concepts

- **ManagedSecret** — Project-scoped CRD describing *intent*: name, type, optional sync to a Kubernetes Secret. Values are stored in OpenBao under `<org>/kv-<project>/<name>` and never live in the CRD.
- **Sync** — When enabled (default), the platform projects the values into a regular Kubernetes Secret your workloads mount via `envFrom` / `volumeMounts`. The Secret is rewritten in place on every value update.
- **Types** — `opaque` (default), `password`, `api-key`, `tls`, `db-static`. The shape drives UI rendering and permission policy.

## Create a Secret

### Empty secret with default sync (target Secret name = ManagedSecret name)

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedSecret
metadata:
  name: {secret-name}
  namespace: {project-namespace}      # {org}-{project}
spec:
  type: opaque                        # opaque | password | api-key | tls | db-static
  description: "What this secret is for"
  sync:
    enabled: true                     # project into a Kubernetes Secret
    refreshInterval: 1h               # ESO poll interval; 1h is fine for most uses
```

See @managed-secret-template.yaml for a fully-annotated template.

### Seed initial values (CLI is the only way; CRD never holds values)

```bash
# Inline literals
kube-dc secrets create {secret-name} \
  --from-literal=API_KEY={value} \
  --from-literal=API_SECRET={value}

# From a .env file
kube-dc secrets create {secret-name} --from-env-file=./app.env

# No sync — values readable only via `kube-dc secrets get --reveal`
kube-dc secrets create {secret-name} --sync-disabled
```

## Read / Update Values

```bash
# List secrets in the project
kube-dc secrets list

# Reveal current values
kube-dc secrets get {secret-name} --reveal

# Update a key (writes a new version atomically)
kube-dc secrets put {secret-name} --from-literal=API_KEY={new-value}

# Delete a specific key
kube-dc secrets unset {secret-name} --key=OLD_KEY
```

Updates trigger an ESO refresh; the synced Kubernetes Secret reflects the change within ~`refreshInterval`. For instant rollout, kick the workload (`kubectl rollout restart deploy/{name}`).

## Use in a Workload

The synced Secret name defaults to the ManagedSecret name. Mount it like any Secret:

```yaml
spec:
  containers:
  - name: app
    image: my-app
    envFrom:
    - secretRef:
        name: {secret-name}
```

Or selectively:

```yaml
env:
- name: API_KEY
  valueFrom:
    secretKeyRef:
      name: {secret-name}
      key: API_KEY
```

## Import an Existing Kubernetes Secret

```bash
kube-dc secrets import {secret-name} --from-secret={existing-k8s-secret}
```

The platform takes over lifecycle. The original Secret is rewritten with the synced values; existing references continue to work.

## Verification

After creating a ManagedSecret:

```bash
# 1. ManagedSecret is Ready
kubectl get managedsecret {name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# Expected: True

# 2. Synced Kubernetes Secret exists
kubectl get secret {name} -n {project-namespace}
# Expected: type=Opaque (or kubernetes.io/tls for type=tls), with the keys

# 3. ExternalSecret is in sync
kubectl get externalsecret -n {project-namespace} | grep {name}
# Expected: SyncedToTarget=True
```

**Success**: ManagedSecret Ready, Kubernetes Secret present with values, ExternalSecret reports SyncedToTarget.
**Failure**:
- Stuck `Ready=False / OpenBaoUnavailable`: cluster doesn't have OpenBao enabled or the platform reconciler can't reach it. Check `kubectl -n kube-dc get deploy kube-dc-manager` is Running.
- Synced Secret never appears: External Secrets Operator may be down; check `kubectl get pods -n external-secrets-system`.

## Delete

```bash
# Keep stored values in OpenBao but remove the CRD + synced Secret
kube-dc secrets delete {name}

# Destroy: drop the CRD, the synced Secret, AND the OpenBao values
kube-dc secrets delete {name} --destroy
```

`--destroy` is irreversible — there is no platform recovery path for destroyed values. Confirm with the user before invoking.

## Safety

- ManagedSecret values NEVER live in the CRD spec — only intent does. Don't put a `data` field in there; it's not a Secret.
- Don't `kubectl edit` the synced Kubernetes Secret directly. The next ESO reconcile (~`refreshInterval`) will overwrite your edits.
- Before deleting with `--destroy`, run `kube-dc secrets consumers {name}` to list every workload mounting the synced Secret. Their pods will fail to restart after destruction.
- For TLS certificates use the `manage-certificates` skill instead — it owns renewal lifecycle.
- For database passwords use the `create-database` skill — it sets up rotation tied to the actual DB user.
- For encryption keys (encrypt/decrypt opaque payloads, envelope encryption) use the `manage-kms` skill — those are NOT secrets-to-store.
