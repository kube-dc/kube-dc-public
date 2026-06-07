# KMS — Key Management Service

Kube-DC's KMS gives every project a per-project encryption key (or
several) backed by OpenBao Transit. Use it to:

- **Encrypt short payloads directly** — API tokens, configuration
  secrets, anything ≤ 64 KiB.
- **Wrap your own data keys** (envelope encryption) for larger
  payloads — files, database fields, message-queue messages.
- **Rotate keys on a schedule** without invalidating older ciphertext.

The plaintext never leaves OpenBao. Keys are NEVER exported — you
encrypt and decrypt by referencing the key, not by holding the key
material yourself.

KMS keys are also what powers
[managed Kubernetes etcd encryption-at-rest](provisioning-cluster.md#encryption-at-rest):
the same per-cluster key you create here wraps every DEK the apiserver
uses internally.

## Concepts

A **KMSKey** is a CRD in your project that names an encryption key and
its lifecycle:

- **purpose** — `application` (your code), `etcd` (managed K8s
  encryption), or `backup` (snapshot wrapping). Filters in the UI and
  drives some lifecycle guards.
- **algorithm** — `aes256-gcm96` (default) or `chacha20-poly1305`.
  Both are symmetric AEAD; pick the default unless you have a reason.
- **rotation** — opt-in scheduled rotation that adds a new key
  *version* every `interval`. Older versions remain alive for
  decryption.
- **deletionPolicy** — `retain` (default; the key survives even after
  the KMSKey CR is deleted) or `schedule` (30-day countdown to a
  hard delete; cancellable).

Each KMSKey is bound to a unique OpenBao Transit key under your
Org's Transit engine. Versions increment with each rotation. You
never see the key material — the only API is encrypt / decrypt /
generate-data-key.

## Permissions

| Role | KMSKey CRD | Encrypt | Decrypt | Rotate | Delete | min-decrypt-version |
|---|---|---|---|---|---|---|
| Project Manager | full | ✅ | ✅ | ✅ | ✅ | ✅ |
| Developer | read | ✅ | ✅ | — | — | — |
| Viewer | read | ✅ | — | — | — | — |

The developer role's policy ships with `transit/encrypt/+` +
`transit/decrypt/+` paths so application code can encrypt AND decrypt
without needing project-manager credentials.

Viewer is **encrypt-only** on purpose: a viewer can produce ciphertext
(e.g. a frontend that needs to seal a field before handing it off to
a developer-owned service) but cannot read plaintext back. If you need
decryption, the caller needs at least the developer role.

## Create a key

### Via the CLI

```bash
# Default: purpose=application, algorithm=aes256-gcm96, no rotation
kube-dc kms keys create app-secrets

# With scheduled rotation (--rotation takes the interval directly;
# setting it also enables rotation)
kube-dc kms keys create app-secrets --rotation=90d

# A backup-purpose key with the schedule deletion policy
kube-dc kms keys create archive-2026 \
  --purpose=backup \
  --deletion-policy=schedule
```

### Via kubectl

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: KMSKey
metadata:
  name: app-secrets
  namespace: my-project
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

The controller provisions the underlying OpenBao Transit key, stamps
`status.keyId` to the OpenBao path, and sets the `Ready` condition.

## Inspect

```bash
# List
kube-dc kms keys list

# Detail
kube-dc kms keys describe app-secrets
# NAME         PURPOSE      ALGORITHM      VERSION   ROTATION
# app-secrets  application  aes256-gcm96   3         enabled (90d)
#
# Status:
#   KeyId:                transit/keys/my-org-my-project-app-secrets
#   Current version:      3
#   Min decryption ver:   1
#   Last rotated:         2026-04-15T02:00:11Z
#   Next rotation:        2026-07-14T02:00:11Z
```

Or with kubectl:

```bash
kubectl get kmskey app-secrets -o yaml
```

## Encrypt / decrypt — direct mode (≤ 64 KiB)

For short payloads, encrypt and decrypt directly through KMS. The
ciphertext you get back is an opaque `vault:vN:...` string — that's
the OpenBao Transit ciphertext format. Store it anywhere; only your
project's KMS can decrypt it.

### Via the CLI

```bash
# Inline plaintext
kube-dc kms encrypt app-secrets --plaintext="hunter2"
# vault:v3:hQH7+t9xZ...

# From a file
kube-dc kms encrypt app-secrets --plaintext-file=./token.txt > token.enc

# Decrypt back
kube-dc kms decrypt app-secrets --ciphertext-file=./token.enc
# hunter2
```

### Via a workload (Go)

The kube-dc CLI's encrypt/decrypt is a thin wrapper over the OpenBao
Transit HTTP API. From inside the cluster, your pod's
`ServiceAccount` has a transit policy (developer role and above) and
can call OpenBao directly via the project's OIDC token or the
Kube-DC API proxy.

The cleanest pattern is to use the official OpenBao Go SDK with the
SA token injected by the External Secrets Operator binding. Here's
a self-contained example:

```go
// go.mod
// require github.com/openbao/openbao/api/v2 v2.5.1

package main

import (
    "encoding/base64"
    "fmt"
    "log"
    "os"

    bao "github.com/openbao/openbao/api/v2"
)

const (
    addr      = "https://bao.kube-dc.cloud"
    namespace = "my-org"            // your Org (matches the first
                                    // segment of kube-dc.com/project)
    // Transit mount is at the root of the Org namespace.
    // Key name follows the kube-dc kmskey_controller convention:
    //   "<org>-<project>-<KMSKey name>"
    keyName = "my-org-my-project-app-secrets"
    role    = "developer-my-org"    // OrganizationGroup developer
                                    // role bound to your SA
)

func main() {
    cfg := bao.DefaultConfig()
    cfg.Address = addr
    cli, err := bao.NewClient(cfg)
    if err != nil { log.Fatal(err) }
    cli.SetNamespace(namespace)

    // Authenticate via the projected SA token (audience=openbao).
    jwt, err := os.ReadFile("/var/run/secrets/openbao/token")
    if err != nil { log.Fatal(err) }
    secret, err := cli.Logical().Write("auth/k8s-host/login", map[string]interface{}{
        "role": role,
        "jwt":  string(jwt),
    })
    if err != nil { log.Fatal(err) }
    cli.SetToken(secret.Auth.ClientToken)

    // Encrypt — transit/encrypt/<key> under the Org namespace
    plain := []byte("hunter2")
    resp, err := cli.Logical().Write(fmt.Sprintf("transit/encrypt/%s", keyName), map[string]interface{}{
        "plaintext": base64.StdEncoding.EncodeToString(plain),
    })
    if err != nil { log.Fatal(err) }
    ct := resp.Data["ciphertext"].(string)
    fmt.Println("ciphertext:", ct)
    // → vault:v3:hQH7+t9xZ...

    // Decrypt — transit/decrypt/<key>
    resp, err = cli.Logical().Write(fmt.Sprintf("transit/decrypt/%s", keyName), map[string]interface{}{
        "ciphertext": ct,
    })
    if err != nil { log.Fatal(err) }
    pt, _ := base64.StdEncoding.DecodeString(resp.Data["plaintext"].(string))
    fmt.Println("plaintext:", string(pt))
    // → hunter2
}
```

For the SA token volume your Pod needs:

```yaml
spec:
  containers:
  - name: app
    volumeMounts:
    - name: bao-token
      mountPath: /var/run/secrets/openbao
      readOnly: true
  volumes:
  - name: bao-token
    projected:
      sources:
      - serviceAccountToken:
          audience: openbao
          path: token
          expirationSeconds: 3600
```

### Via a workload (Python)

Same pattern with the official `hvac` client (works against OpenBao
since the Vault API is wire-compatible):

```python
# requirements.txt
# hvac>=2.1.0

import base64
import hvac

ADDR      = "https://bao.kube-dc.cloud"
NAMESPACE = "my-org"
# Transit mount is at the root of the Org namespace.
# Key name follows "<org>-<project>-<KMSKey name>".
KEY_NAME  = "my-org-my-project-app-secrets"
ROLE      = "developer-my-org"

cli = hvac.Client(url=ADDR, namespace=NAMESPACE)

# Authenticate via the projected SA token
with open("/var/run/secrets/openbao/token") as f:
    jwt = f.read()

cli.auth.kubernetes.login(role=ROLE, jwt=jwt, mount_point="k8s-host")

# Encrypt
plain = b"hunter2"
resp = cli.write(
    f"transit/encrypt/{KEY_NAME}",
    plaintext=base64.b64encode(plain).decode(),
)
ct = resp["data"]["ciphertext"]
print("ciphertext:", ct)

# Decrypt
resp = cli.write(f"transit/decrypt/{KEY_NAME}", ciphertext=ct)
plain = base64.b64decode(resp["data"]["plaintext"])
print("plaintext:", plain.decode())
```

## Envelope encryption — encrypt large blobs

For files, database fields, or anything > 64 KiB, direct encryption is
inefficient (and Transit refuses payloads above the limit anyway).
Use **envelope encryption**: generate a random data key, encrypt the
big blob locally with the data key (cheap), and store the data key
wrapped by your KMSKey alongside the ciphertext.

The wire format is the same one Kube-DC uses for
[managed-K8s etcd backups](provisioning-cluster.md#encryption-at-rest):

```
NONCE (12B) || CIPHERTEXT || GCM_TAG (16B)
+ a sidecar wrappedDek = "vault:vN:..."
```

### Go envelope helper

```go
package main

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "io"
    "log"
    "os"

    bao "github.com/openbao/openbao/api/v2"
)

const (
    addr      = "https://bao.kube-dc.cloud"
    namespace = "my-org"
    // Transit mount is at the root of the Org namespace.
    // Key name follows "<org>-<project>-<KMSKey name>".
    keyName = "my-org-my-project-app-secrets"
    role    = "developer-my-org"
)

func auth(cli *bao.Client) error {
    jwt, err := os.ReadFile("/var/run/secrets/openbao/token")
    if err != nil { return err }
    sec, err := cli.Logical().Write("auth/k8s-host/login", map[string]interface{}{
        "role": role, "jwt": string(jwt),
    })
    if err != nil { return err }
    cli.SetToken(sec.Auth.ClientToken)
    return nil
}

func encryptEnvelope(plaintext []byte) (ciphertext []byte, wrappedDek string, err error) {
    cfg := bao.DefaultConfig(); cfg.Address = addr
    cli, _ := bao.NewClient(cfg); cli.SetNamespace(namespace)
    if err = auth(cli); err != nil { return }

    // 1. Generate a fresh 32-byte DEK
    dek := make([]byte, 32)
    if _, err = rand.Read(dek); err != nil { return }

    // 2. Wrap the DEK with the KMSKey via Transit
    resp, err := cli.Logical().Write(fmt.Sprintf("transit/encrypt/%s", keyName), map[string]interface{}{
        "plaintext": base64.StdEncoding.EncodeToString(dek),
    })
    if err != nil { return }
    wrappedDek = resp.Data["ciphertext"].(string)

    // 3. Encrypt the plaintext locally with AES-256-GCM
    block, err := aes.NewCipher(dek); if err != nil { return }
    gcm, err := cipher.NewGCM(block); if err != nil { return }
    nonce := make([]byte, gcm.NonceSize())
    if _, err = io.ReadFull(rand.Reader, nonce); err != nil { return }
    ciphertext = gcm.Seal(nonce, nonce, plaintext, nil)
    // wire format: nonce || ciphertext || tag (gcm.Seal returns
    // ciphertext || tag, we prepended nonce).
    return
}

func decryptEnvelope(ciphertext []byte, wrappedDek string) ([]byte, error) {
    cfg := bao.DefaultConfig(); cfg.Address = addr
    cli, _ := bao.NewClient(cfg); cli.SetNamespace(namespace)
    if err := auth(cli); err != nil { return nil, err }

    // 1. Unwrap the DEK via Transit
    resp, err := cli.Logical().Write(fmt.Sprintf("transit/decrypt/%s", keyName), map[string]interface{}{
        "ciphertext": wrappedDek,
    })
    if err != nil { return nil, err }
    dek, _ := base64.StdEncoding.DecodeString(resp.Data["plaintext"].(string))

    // 2. Decrypt locally
    block, err := aes.NewCipher(dek); if err != nil { return nil, err }
    gcm, err := cipher.NewGCM(block); if err != nil { return nil, err }
    n := gcm.NonceSize()
    if len(ciphertext) < n {
        return nil, fmt.Errorf("ciphertext too short")
    }
    return gcm.Open(nil, ciphertext[:n], ciphertext[n:], nil)
}

func main() {
    blob := []byte("the whole novel in one slice")
    ct, wrapped, err := encryptEnvelope(blob)
    if err != nil { log.Fatal(err) }
    pt, err := decryptEnvelope(ct, wrapped)
    if err != nil { log.Fatal(err) }
    fmt.Println(string(pt))
}
```

### Python envelope helper

```python
# requirements.txt
# hvac>=2.1.0
# cryptography>=42.0.0

import base64, os
import hvac
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

ADDR      = "https://bao.kube-dc.cloud"
NAMESPACE = "my-org"
# Transit mount is at the root of the Org namespace.
# Key name follows "<org>-<project>-<KMSKey name>".
KEY_NAME  = "my-org-my-project-app-secrets"
ROLE      = "developer-my-org"

def _client():
    cli = hvac.Client(url=ADDR, namespace=NAMESPACE)
    with open("/var/run/secrets/openbao/token") as f:
        cli.auth.kubernetes.login(role=ROLE, jwt=f.read(), mount_point="k8s-host")
    return cli

def encrypt_envelope(plaintext: bytes) -> tuple[bytes, str]:
    cli = _client()
    dek = os.urandom(32)
    wrapped = cli.write(
        f"transit/encrypt/{KEY_NAME}",
        plaintext=base64.b64encode(dek).decode(),
    )["data"]["ciphertext"]
    nonce = os.urandom(12)
    ct = AESGCM(dek).encrypt(nonce, plaintext, None)
    return nonce + ct, wrapped  # wire: nonce || ciphertext || tag

def decrypt_envelope(blob: bytes, wrapped: str) -> bytes:
    cli = _client()
    dek = base64.b64decode(
        cli.write(f"transit/decrypt/{KEY_NAME}", ciphertext=wrapped)["data"]["plaintext"]
    )
    return AESGCM(dek).decrypt(blob[:12], blob[12:], None)

if __name__ == "__main__":
    payload = b"the whole novel in one bytestring"
    ct, wrapped = encrypt_envelope(payload)
    print(decrypt_envelope(ct, wrapped))
```

Pattern: the wrapped DEK is small enough to store next to the ciphertext
(or in your database row, or in S3 object metadata). At rest, both
pieces are useless without OpenBao access — exactly the property
Kube-DC's own etcd-backup pipeline relies on.

## Rotate the key

```bash
# Add a new key version. Old versions stay alive for decryption.
kube-dc kms keys rotate app-secrets

# Or schedule it
kubectl patch kmskey app-secrets --type=merge -p '
spec:
  rotation:
    enabled: true
    interval: 90d
'
```

After rotation:

- Any **new** `encrypt` call uses the new version (the `vault:vN:` prefix
  bumps).
- Existing ciphertexts in your storage decrypt fine — the wrapped DEK
  carries its own version stamp.
- Forcing a re-wrap is your responsibility: read each ciphertext,
  decrypt with the old wrapped DEK, encrypt with the new one. Often
  worth doing only when you advance `min_decryption_version`.

## Advance `min_decryption_version`

Rotating the key doesn't disable old versions — they remain
decryptable indefinitely. To **force** older ciphertexts to become
unreadable (e.g. a compromised version), advance the floor:

```bash
kube-dc kms keys set-min-decryption-version app-secrets 2
```

This is **irreversible**. Anything still wrapped with version 1 stops
decrypting forever. The CLI runs the operation immediately — there's
no interactive prompt — so be certain you've re-wrapped or aged out
everything wrapped with the about-to-be-prohibited versions before
invoking. Project-manager role required.

## Delete a key

Default `deletionPolicy: retain` means the underlying Transit key
survives even when you delete the `KMSKey` CR. To actually destroy the
key material:

```bash
# Step 1 — flip the policy to schedule (30-day countdown, cancellable)
kube-dc kms keys schedule-delete app-secrets

# Step 2 — wait, or cancel
kube-dc kms keys cancel-delete app-secrets   # any time before expiry

# Step 3 — the controller hard-deletes after 30d
```

OpenBao's own `deletion_allowed=false` flag on the Transit key
prevents accidental destruction even by platform admins. The 30-day
window plus the explicit CRD spec change is the safety net.

## Audit

Every `encrypt`, `decrypt`, `rotate`, `set-min-decryption-version`,
and `delete` emits a structured audit event:

```bash
kube-dc audit list --resource=KMSKey --since=24h
```

Includes the calling identity (OIDC subject), the key name, the
operation, and the key version touched.

## Reference

- [KMSKey CRD spec — `purpose`, `algorithm`, `rotation`, `deletionPolicy`](provisioning-cluster.md#encryption-at-rest)
  (cross-link; same KMSKey is used by managed-K8s etcd encryption)
- [Secrets Manager](secrets-manager.md) — for storing whole secrets
  the platform projects into a Kubernetes `Secret` (not raw encrypt /
  decrypt of opaque payloads)
- OpenBao Transit reference: [openbao.org/docs/secrets/transit/](https://openbao.org/docs/secrets/transit/)
- OpenBao Kubernetes auth: [openbao.org/docs/auth/kubernetes/](https://openbao.org/docs/auth/kubernetes/)
