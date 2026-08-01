# KMS (Key Management Service)

Kube-DC KMS lets you create non-exportable symmetric encryption keys in a
Project. The key material is held by OpenBao Transit and is never returned to
you. Use a `KMSKey` to:

- encrypt short payloads, up to 64 KiB;
- wrap data-encryption keys (DEKs) for larger payloads;
- rotate a key without immediately invalidating older ciphertext; and
- protect a Managed Cluster's etcd data and encrypted backups.

Key material never leaves OpenBao. **Caller plaintext is different:** direct
encrypt requests pass plaintext through the Kube-DC backend to OpenBao, and
decrypt requests return plaintext through the backend. Kube-DC audit events do
not record plaintext or ciphertext. For large or especially sensitive payloads,
encrypt the payload locally and send only its DEK to KMS for wrapping.

## How keys are represented

A `KMSKey` is a custom resource in the backing namespace for your Project. It
declares the key's purpose and lifecycle:

- `purpose`: `application`, `etcd`, or `backup`. This classifies the key and
  enables purpose-specific lifecycle checks.
- `algorithm`: `aes256-gcm96` (default) or `chacha20-poly1305`.
- `rotation`: optional scheduled creation of a new key version. Older versions
  remain available for decryption.
- `deletionPolicy`: `retain` (default) preserves key material when the
  `KMSKey` is deleted; `schedule` starts a cancellable 30-day deletion window.

The controller creates a corresponding Transit key in the Organization's
OpenBao namespace. Its internal name follows
`<organization>-<project>-<kmskey>` and appears in `status.keyId`.

## Access and security boundary

Human access from the dashboard and CLI uses the signed-in user's OIDC identity.
An `OrganizationGroup` maps that identity to one or more standard Project roles,
and Kube-DC creates the corresponding OpenBao policies.

| Role | `KMSKey` resource | Encrypt | Decrypt | Rotate or set minimum version | Explicit schedule/cancel action |
|---|---|---|---|---|---|
| `admin` | Full lifecycle | Yes | Yes | Yes | Yes |
| `project-manager` | Create, read, update | Yes | Yes | Yes | No |
| `developer` | Read | Yes | Yes | No | No |
| `user` | Read | Yes | No | No | No |

**Current enforcement limitation:** the explicit `schedule-delete` and
`cancel-delete` actions require a Project `admin`, but the current release does
not enforce that role at every path that can set `spec.deletionPolicy`.

A `project-manager` can submit `deletionPolicy: schedule` while creating a key
from the dashboard or CLI because the create endpoint relies on object-level
`create` permission. The advanced YAML editor likewise relies on `update`, and a
direct Kubernetes API client can set the field with `CREATE`, `UPDATE`, or
`PATCH`. Kubernetes RBAC authorizes those object-level verbs rather than one
field, and no admission policy currently adds the missing field-level check.

Until that guard is deployed, treat `project-manager` as able to schedule KMS
key deletion. Do not rely on the `admin` / `project-manager` distinction as a
key-lifecycle security boundary. Check the effective write permissions before
assigning the role:

```bash
kubectl auth can-i create kmskeys.security.kube-dc.com -n acme-production
kubectl auth can-i update kmskeys.security.kube-dc.com -n acme-production
```

### Current Organization-wide Transit boundary

`KMSKey` resources and key names are Project-scoped, but the current standard
human OpenBao policies are not. They grant role-level access with paths such as
`transit/encrypt/+` and `transit/decrypt/+` inside one Organization-wide Transit
mount.

As a result, a user with decrypt permission in one Project could operate on
another Project's Transit key in the same Organization if they learn its exact
internal key name. The dashboard and CLI still resolve a `KMSKey` through the
selected Project, but that does not change the underlying OpenBao authorization
boundary.

Treat the **Organization as the current KMS trust boundary**. Put mutually
untrusted teams in separate Organizations. Per-Project Transit authorization is
planned for Phase 3 or later and is not part of the current release.

### Workload identities

Do not copy a Pod ServiceAccount token into an OpenBao SDK example and assume a
Project role will accept it. Kube-DC does not provision a general-purpose
Kubernetes-auth role for application workloads.

For tenant-facing Project access, the only Kubernetes-auth binding is for the
dedicated `openbao-external-secrets` ServiceAccount. Its
`project-<project>-external-secrets` policy can read that Project's Secrets
Manager KV mount; it does not grant Transit/KMS access. Human KMS access uses
OIDC and `OrganizationGroup` roles instead.

Managed Clusters are a separate platform integration. Kube-DC gives each
Managed Cluster KMS plugin a dedicated, tightly bound ServiceAccount and an
OpenBao policy for exactly one Transit key. Application workloads must not reuse
either platform ServiceAccount.

## Create an application key

### CLI

```bash
# Default: purpose=application, algorithm=aes256-gcm96, no rotation
kube-dc kms keys create app-secrets

# Enable 90-day rotation
kube-dc kms keys create app-secrets --rotation=90d
```

### Kubernetes API

Each Project is backed by a namespace. For Organization `acme` and Project
`production`, that namespace is `acme-production`.

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: KMSKey
metadata:
  name: app-secrets
  namespace: acme-production
spec:
  purpose: application
  algorithm: aes256-gcm96
  rotation:
    enabled: true
    interval: 90d
  deletionPolicy: retain
```

```bash
kubectl apply -f kmskey.yaml
```

The controller provisions the Transit key and sets the `Ready` condition. Check
status before using it:

```bash
kube-dc kms keys describe app-secrets
kubectl get kmskey app-secrets -n acme-production -o yaml
```

A ready key reports an identifier similar to:

```text
acme/transit/keys/acme-production-app-secrets
```

## Encrypt and decrypt short payloads

Direct mode accepts at most 64 KiB. Prefer files or standard input for sensitive
values because `--plaintext` can be preserved in shell history.

```bash
# Encrypt bytes from a file and write an opaque Transit ciphertext.
kube-dc kms encrypt app-secrets \
  --plaintext-file=./token.txt \
  --out=./token.enc

# Decrypt the ciphertext back to a file.
kube-dc kms decrypt app-secrets \
  --ciphertext-file=./token.enc \
  --out=./token.dec
```

Ciphertext has the form `vault:vN:...`, where `N` identifies the key version.
Store the complete value. Decryption requires the same key and, when used, the
same encryption context.

## Encrypt larger payloads

For payloads larger than 64 KiB, use envelope encryption in your application:

1. Generate a random DEK locally.
2. Encrypt the payload locally with an authenticated cipher such as AES-256-GCM.
3. Wrap only the DEK with the `KMSKey`.
4. Store the encrypted payload, nonce, authentication tag, and wrapped DEK.
5. To decrypt, unwrap the DEK through KMS and decrypt the payload locally.

This keeps the large plaintext out of Kube-DC and OpenBao. The application still
needs an approved authentication path for wrapping and unwrapping; arbitrary
workload ServiceAccount authentication is not a self-service KMS feature in the
current release.

## Managed Cluster etcd encryption

Do not manually create a KEK for the standard Managed Cluster flow. Enable etcd
encryption on the `KdcCluster`:

```yaml
spec:
  encryption:
    etcd:
      enabled: true
      kekRotation:
        enabled: true
        interval: 90d
```

When `keyRef` is omitted, the Managed Cluster controller automatically creates
and manages a `<cluster>-etcd` `KMSKey` in the same Project. It sets
`purpose: etcd`, `algorithm: aes256-gcm96`, and `deletionPolicy: retain`, then
records the name in `status.encryption.resolvedKeyRef`. It also reconciles the
key's rotation policy from `spec.encryption.etcd.kekRotation`.

Treat that key as Managed Cluster-owned and change its rotation through the
`KdcCluster` spec. When an explicit `keyRef` selects an operator-managed
`KMSKey`, Kube-DC does not overwrite that key's rotation policy. See
[Managed Cluster encryption at rest](provisioning-cluster.md#encryption-at-rest).

## Rotate a key

For an application-managed key, create a new version immediately:

```bash
kube-dc kms keys rotate app-secrets
```

Or enable scheduled rotation:

```bash
kubectl patch kmskey app-secrets -n acme-production --type=merge -p '
spec:
  rotation:
    enabled: true
    interval: 90d
'
```

New encrypt operations use the latest version. Existing ciphertext remains
decryptable because its `vault:vN:` prefix identifies the version used. Kube-DC
does not automatically re-encrypt application ciphertext after rotation.

For an auto-managed Managed Cluster key, change
`spec.encryption.etcd.kekRotation` on the `KdcCluster` instead.

## Set the minimum decryption version

Rotation does not disable older versions. To block ciphertext created with
versions below a chosen floor:

```bash
kube-dc kms keys set-min-decryption-version app-secrets 2
```

This operation takes effect immediately and has no interactive prompt. Verify
that required data has been re-encrypted before raising the floor. OpenBao
allows the floor to be lowered while the older key versions still exist, but
applications should treat any change as a potentially disruptive operation.

## Delete a key

The supported deletion flow requires the `admin` role. With the default
`deletionPolicy: retain`, deleting the `KMSKey` resource leaves the Transit key
material in place. To schedule destruction:

```bash
# Start the 30-day countdown.
kube-dc kms keys schedule-delete app-secrets

# Cancel before the deadline.
kube-dc kms keys cancel-delete app-secrets
```

Transit keys are created with deletion disabled. When the 30-day window expires,
the controller deliberately enables deletion and removes the key. Ciphertext
that depends on the deleted key cannot be recovered.

## Audit KMS operations

Kube-DC emits structured audit events for KMS lifecycle and data operations.
Filter the current Project's audit stream by the `kms` service:

```bash
kube-dc audit list --service kms
```

Events include the actor, action, result, and key resource plus selected
operation metadata. They never include plaintext, ciphertext, or key material.

## Related guides

- [Managed Cluster encryption at rest](provisioning-cluster.md#encryption-at-rest)
- [Secrets Manager](secrets-manager.md)
- [OpenBao Transit documentation](https://openbao.org/docs/secrets/transit/)
