---
name: manage-kms
description: Create and operate Kube-DC KMSKey resources backed by non-exportable OpenBao Transit keys, use the authenticated CLI for short payloads, and design envelope encryption for larger data.
---

## What KMS Protects

A `KMSKey` belongs to a Project and references non-exportable symmetric key
material in OpenBao Transit. Use it to:

- encrypt short payloads up to 64 KiB;
- wrap locally generated data-encryption keys for larger payloads;
- rotate to a new key version while retaining older versions for decryption;
- protect a Managed Cluster's etcd data and encrypted backups.

Key material never leaves OpenBao. Caller plaintext is different: direct
encrypt plaintext passes through the Kube-DC backend to OpenBao, and decrypt
plaintext returns through the backend. Kube-DC audit events do not record
plaintext, ciphertext, or key material.

## Prerequisites and Trust Boundary

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- KMS/OpenBao is enabled for the installation.
- The signed-in identity has the required Project role.

| Role | Key resource | Encrypt | Decrypt | Rotate or set version floor | Explicit schedule/cancel action |
|---|---|---|---|---|---|
| `admin` | Full lifecycle | Yes | Yes | Yes | Yes |
| `project-manager` | Create/read/update | Yes | Yes | Yes | No |
| `developer` | Read | Yes | Yes | No | No |
| `user` | Read | Yes | No | No | No |

The explicit `schedule-delete` and `cancel-delete` actions require the Project
`admin` role. This is not a complete deletion-policy boundary in the current
release. A `project-manager` can set `deletionPolicy: schedule` during creation
through the dashboard or CLI, use the advanced YAML editor, or send a direct
Kubernetes API `CREATE`, `UPDATE`, or `PATCH` request. Those paths rely on the
object-level `create`, `update`, and `patch` permissions already granted to the
role; no admission policy currently adds a field-level check.

Until that guard is deployed, treat `project-manager` as able to schedule key
deletion and do not claim an `admin`-only lifecycle boundary.

Current human Transit policies are Organization-wide at the OpenBao layer.
Although KMSKey resources and names are Project-scoped, someone with Transit
permission in one Project may be able to operate on another Project's key in the
same Organization if they learn its internal name. Treat the Organization as
the current KMS trust boundary; use separate Organizations for mutually
untrusted teams.

## Workload Authentication Is Not Self-Service

Kube-DC does not provision a general-purpose OpenBao Kubernetes-auth role for
application ServiceAccounts. Do not mount an arbitrary projected ServiceAccount
token and call `auth/k8s-host/login`: standard Project roles apply to human
OIDC identities, not Pods.

The `openbao-external-secrets` ServiceAccount can read that Project's Secrets
Manager KV mount, but it has no Transit permission. Managed Cluster KMS plugins
use dedicated platform identities bound to one key; applications must not reuse
them.

For application-side envelope encryption, first obtain an operator-approved
Transit authentication path or a supported service integration. The helper
files show local cryptography with injected wrap/unwrap callbacks; they do not
invent an authentication mechanism.

## Create a Key

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: KMSKey
metadata:
  name: app-secrets
  namespace: "{backing-namespace}"
spec:
  purpose: application # application | etcd | backup
  algorithm: aes256-gcm96 # aes256-gcm96 | chacha20-poly1305
  rotation:
    enabled: true
    interval: 90d
  deletionPolicy: retain
```

Or use the CLI:

```bash
kube-dc kms keys create app-secrets
kube-dc kms keys create app-secrets --rotation=90d
```

Wait for Ready:

```bash
kube-dc kms keys describe app-secrets
kubectl get kmskey app-secrets -n {backing-namespace} -o yaml
```

Use [kmskey-template.yaml](kmskey-template.yaml) for an annotated manifest.

## Encrypt and Decrypt Short Payloads

Prefer files or standard input; inline plaintext can remain in shell history.

```bash
kube-dc kms encrypt app-secrets \
  --plaintext-file=./token.txt \
  --out=./token.enc

kube-dc kms decrypt app-secrets \
  --ciphertext-file=./token.enc \
  --out=./token.dec
```

Transit ciphertext has the form `vault:vN:...`. Store the entire value.
Decryption requires the same key and, if supplied, the same encryption context.

## Envelope Encryption

For payloads larger than 64 KiB, or when plaintext should not pass through the
Kube-DC backend:

1. Generate a random data-encryption key (DEK) locally.
2. Encrypt the payload locally with an authenticated cipher such as
   AES-256-GCM.
3. Send only the DEK through the approved KMS path for wrapping.
4. Store the nonce, encrypted payload, authentication tag, and wrapped DEK.
5. Unwrap the DEK through KMS, decrypt locally, then erase the in-memory DEK as
   far as the runtime permits.

See [envelope-encryption-go.md](envelope-encryption-go.md) and
[envelope-encryption-py.md](envelope-encryption-py.md). The examples accept
wrap/unwrap callbacks so authentication remains an explicit deployment
decision.

## Rotate and Set a Version Floor

```bash
# Create a new key version; old ciphertext remains decryptable.
kube-dc kms keys rotate app-secrets

# Reject ciphertext encrypted below version 2.
kube-dc kms keys set-min-decryption-version app-secrets 2
```

New encrypt operations use the latest version. Kube-DC does not re-encrypt
application data automatically.

Raising the minimum decryption version takes effect immediately and can make
older ciphertext or backups unreadable. Confirm that required data has been
re-encrypted before changing it. OpenBao may allow the floor to be lowered
while old versions still exist, but applications should not use that as a
recovery plan.

## Delete a Key

The supported schedule/cancel flow requires the Project `admin` role:

```bash
# Start the 30-day countdown.
kube-dc kms keys schedule-delete app-secrets

# Cancel before the deadline.
kube-dc kms keys cancel-delete app-secrets
```

With `deletionPolicy: retain`, deleting the Kubernetes resource preserves
Transit key material. Once scheduled destruction expires, ciphertext that
depends on the deleted key cannot be recovered. Verify consumers and backups
before scheduling it.

## Managed Cluster etcd Keys

For the standard Managed Cluster flow, enable
`KdcCluster.spec.encryption.etcd.enabled`. When no explicit key is selected,
the platform creates `{cluster}-etcd` in the same Project and reconciles its
rotation from `KdcCluster.spec.encryption.etcd.kekRotation`.

Do not directly change, delete, or schedule destruction of that auto-managed
key. Manage it through the `KdcCluster` spec.

## Verification and Audit

```bash
kubectl get kmskey app-secrets -n {backing-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\t"}{.status.currentVersion}{"\n"}'

printf test > /tmp/kms-input
kube-dc kms encrypt app-secrets --plaintext-file=/tmp/kms-input --out=/tmp/kms-cipher
kube-dc kms decrypt app-secrets --ciphertext-file=/tmp/kms-cipher --out=/tmp/kms-output
cmp /tmp/kms-input /tmp/kms-output

kube-dc audit list --service kms
```

Remove plaintext test files when finished. Never print sensitive plaintext,
ciphertext, or kubeconfig/token values into logs or chat.
