---
name: manage-secrets
description: Store Project application secrets in Kube-DC's managed secret service, optionally sync them into Kubernetes Secrets, and manage their lifecycle with the kube-dc CLI.
---

# Manage Project Secrets

A `ManagedSecret` carries metadata and sync intent in Kubernetes. Secret values
do not live in its spec; the Kube-DC backend stores them in the Project's
encrypted OpenBao KV space. When sync is enabled, External Secrets Operator
projects selected values into a regular Kubernetes Secret.

Use this feature for stored values such as API tokens and OAuth client secrets.
Use `manage-database-credentials` for rotated database users,
`manage-certificates` for x509 lifecycle, and `manage-kms` for Transit
encrypt/decrypt operations.

## Prerequisites

- Select the Project with `kube-dc use {domain}/{organization}/{project}`.
- The installation advertises Secrets Manager as available.
- The caller has the required Project role.
- Never pass real values in chat or commit them to a manifest.

## Permissions

| Role | ManagedSecret lifecycle | Read/write values | Change sync | Destroy history |
|---|---|---|---|---|
| `user` | Read metadata | No | No | No |
| `developer` | Create, read, update, delete | Yes | Yes | No |
| `project-manager` | Read and update existing resources | Yes | Yes, on existing resources | No |
| `admin` | Full lifecycle | Yes | Yes | Yes |

The Project is the access boundary. `developer` and `project-manager` can also
read Kubernetes Secrets in the backing namespace.

## 1. Create and seed a secret

The CLI enables sync by default and uses the ManagedSecret name as the target
Kubernetes Secret:

```bash
kube-dc secrets create {name} \
  --type opaque \
  --description "Credential used by {application}" \
  --from-env-file ./application.env
```

Other input forms are repeatable `--from-literal=KEY=VALUE` and
`--from-file=KEY=path`. Prefer files or an env file so sensitive values do not
remain in shell history.

Creating the CR and writing its first value are two sequential operations, not
one transaction. If the value write fails, the CLI leaves the empty
ManagedSecret in place and reports retry/cleanup commands.

Keep values only in OpenBao when no Kubernetes workload needs them:

```bash
kube-dc secrets create {name} --sync-disabled
```

A Git-safe intent manifest contains no data:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedSecret
metadata:
  name: "{name}"
  namespace: "{project-backing-namespace}"
spec:
  type: opaque
  description: "Credential used by {application}"
  sync:
    enabled: true
    targetSecretName: "{name}"
    refreshInterval: 1h
```

See [managed-secret-template.yaml](managed-secret-template.yaml).

## 2. Read metadata or values

```bash
kube-dc secrets list
kube-dc secrets get {name}
kube-dc secrets get {name} --value
```

`--value` prints the current plaintext values. Use it only in a trusted
terminal, never pipe it into logs, and do not include its output in chat.

## 3. Update values or sync

```bash
kube-dc secrets put {name} --from-env-file ./application.env
kube-dc secrets unset {name} --key OLD_KEY

kube-dc secrets sync {name} --enabled=true --target={target-secret} --refresh=1h
```

External Secrets Operator updates the target on its reconciliation schedule.
Values injected into container environment variables do not change in an
already-running Pod; perform an application-aware rollout after the synced
Secret changes. File-mounted Secret volumes update eventually, but the
application must reread them.

Do not edit the projected Kubernetes Secret directly. ESO will reconcile it
back to the managed value.

## 4. Use the synced Secret

```yaml
envFrom:
  - secretRef:
      name: "{target-secret}"
```

Before a destructive operation, inspect all known consumers:

```bash
kube-dc secrets consumers {name}
```

## 5. Import an existing Kubernetes Secret

```bash
kube-dc secrets import {managed-name} --from {source-secret}
```

The source defaults to the current Project's backing namespace. A
cross-namespace import requires `--from-namespace`, `--cross-namespace`, and
permission to read the source; the operation is audit-visible.

## Verify

```bash
kubectl get managedsecret {name} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
kubectl get managedsecret {name} -o yaml
kubectl get secret {target-secret}
kubectl get externalsecret
```

When sync is disabled, absence of a target Secret is expected. When it is
enabled, verify `status.syncedSecretName` and the ExternalSecret conditions.
Troubleshoot `Ready=False` through ManagedSecret conditions, Project security
status, and the platform operator; do not try to read internal OpenBao
credentials.

## Delete or destroy

Soft delete removes the ManagedSecret and projected Kubernetes Secret but
preserves stored value history:

```bash
kube-dc secrets delete {name}
```

Permanent destruction is admin-only and irreversible:

```bash
kube-dc secrets consumers {name}
kube-dc secrets delete {name} --destroy
```

Confirm with the user immediately before `--destroy`. Destroy ordering and
audit are owned by the backend; do not reproduce the operation with raw
OpenBao commands.

## Safety

- Values never belong in `ManagedSecret.spec`.
- Prefer file-based CLI inputs over literals for sensitive data.
- Do not reveal values unless the user explicitly needs them and the terminal
  is trusted.
- Treat Project membership and roles as access to all Secrets the role permits
  in that Project.
- Check consumers before delete, rotation, sync-target changes, or destroy.
- Use the purpose-built database, certificate, and KMS resources for those
  lifecycles.
