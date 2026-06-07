---
name: manage-kms
description: Create and operate per-project encryption keys (KMSKey) backed by OpenBao Transit. Use for encrypting opaque payloads ≤ 64 KiB directly, or for envelope encryption of larger blobs (files, database fields, message-queue messages). Plaintext NEVER leaves OpenBao — workloads call encrypt/decrypt by referencing the key, not by holding key material. The same KMSKey type powers managed-K8s etcd encryption-at-rest. For storing whole secrets use manage-secrets. For TLS certificates use manage-certificates.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- OpenBao must be enabled on the cluster (check master-config `enable_openbao=true`)

## Key Concepts

- **KMSKey** — Project-scoped CRD bound to a unique OpenBao Transit key. Stays alive (`deletionPolicy: retain`) even when the CRD is deleted, so backup envelopes wrapping old DEKs remain decryptable.
- **purpose** — `application` (your code; the default), `etcd` (managed-K8s etcd encryption), or `backup` (snapshot wrapping). Filters in the UI + drives lifecycle guards.
- **algorithm** — `aes256-gcm96` (default) or `chacha20-poly1305`. Both are symmetric AEAD; pick the default unless told otherwise.
- **rotation** — Opt-in scheduled rotation. New encrypt calls use the latest version automatically. Old versions stay alive for decryption until `min_decryption_version` is manually advanced (irreversible operator action — never auto).
- **deletionPolicy** — `retain` (default; survives CRD delete) or `schedule` (30-day countdown; cancellable until expiry).

## When to use KMS vs other features

| Need | Use |
|------|-----|
| Encrypt an API token to put in a database field | **KMS direct encrypt** (≤ 64 KiB) |
| Encrypt a large file or message-queue payload before storing | **KMS envelope encryption** (Go / Python helpers below) |
| Store a credential the platform syncs into a K8s Secret | `manage-secrets` |
| TLS server / mTLS / code-signing certs | `manage-certificates` |
| Database username + password with rotation | `create-database` |

## Create a Key

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: KMSKey
metadata:
  name: {key-name}
  namespace: {project-namespace}      # {org}-{project}
spec:
  purpose: application                # application | etcd | backup
  algorithm: aes256-gcm96             # aes256-gcm96 | chacha20-poly1305
  rotation:
    enabled: true                     # opt-in
    interval: 90d                     # KubeDC duration; d|h|m|s only (no w)
  deletionPolicy: retain              # retain | schedule
```

See @kmskey-template.yaml for a fully-annotated template.

### CLI shortcuts

```bash
# Default (purpose=application, no rotation)
kube-dc kms keys create {key-name}

# With scheduled rotation
kube-dc kms keys create {key-name} --rotation=90d

# Backup-purpose with schedule-delete enabled
kube-dc kms keys create archive-2026 \
  --purpose=backup --deletion-policy=schedule
```

## Inspect

```bash
kube-dc kms keys list
kube-dc kms keys describe {key-name}
# NAME       PURPOSE      ALGORITHM      VERSION   ROTATION
# {name}     application  aes256-gcm96   3         enabled (90d)

# Or via kubectl
kubectl get kmskey -n {project-namespace}
kubectl describe kmskey {key-name} -n {project-namespace}
```

## Direct encrypt / decrypt (≤ 64 KiB)

For short payloads — API tokens, configuration secrets, single database fields:

```bash
# Inline plaintext
kube-dc kms encrypt {key-name} --plaintext="hunter2"
# vault:v3:hQH7+t9xZ...

# From a file
kube-dc kms encrypt {key-name} --plaintext-file=./token.txt > token.enc

# Decrypt back
kube-dc kms decrypt {key-name} --ciphertext-file=./token.enc
# hunter2
```

The ciphertext is an opaque `vault:vN:...` string — store it anywhere; only the project's KMS can decrypt it.

## Envelope encryption (> 64 KiB or for in-app use)

For files, database fields, or message-queue payloads, generate a per-payload data key, encrypt locally with the data key, store the wrapped data key next to the ciphertext.

The wire format Kube-DC uses (matching the managed-K8s etcd backup envelope):

```
NONCE (12B) || CIPHERTEXT || GCM_TAG (16B)
+ wrappedDek = "vault:vN:..." (stored next to the ciphertext)
```

See @envelope-encryption-go.md for the Go helper and @envelope-encryption-py.md for the Python helper. Both authenticate against OpenBao via the projected SA token (audience=openbao), so a workload pod needs:

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

## Rotate

```bash
# Add a new key version. Old versions remain alive for decryption.
kube-dc kms keys rotate {key-name}
```

Or schedule via spec:

```bash
kubectl patch kmskey {key-name} -n {project-namespace} \
  --type=merge -p '{"spec":{"rotation":{"enabled":true,"interval":"90d"}}}'
```

After rotation:
- Any new `encrypt` call uses the new version (the `vault:vN:` prefix increments).
- Existing ciphertexts decrypt fine — the wrapped DEK carries its own version stamp.
- Forcing a re-wrap is the application's responsibility: read each ciphertext, decrypt, re-encrypt with the current version, write back.

## Advance min_decryption_version (IRREVERSIBLE)

To force old ciphertexts to become unreadable (e.g. a compromised version):

```bash
kube-dc kms keys set-min-decryption-version {key-name} {version}
```

**This is irreversible.** Anything still wrapped with a sub-`{version}` DEK stops decrypting forever. The CLI walks through a confirmation prompt; confirm with the user first that everything important has been re-wrapped or aged out.

Automation NEVER does this — it's always a human action. Workloads using the KMSKey have `encrypt` + `decrypt` permissions but NOT `set-min-decryption-version`.

## Delete

Default `deletionPolicy: retain` keeps the underlying Transit key even after the CRD is deleted. To actually destroy key material:

```bash
# Step 1 — flip the policy to schedule (30-day countdown, cancellable)
kube-dc kms keys schedule-delete {key-name}

# Step 2 — cancel at any time before expiry
kube-dc kms keys cancel-delete {key-name}

# Step 3 — the controller hard-deletes after 30 days
```

OpenBao's own `deletion_allowed=false` flag prevents accidental destruction even by platform admins.

## Verification

After creating a KMSKey:

```bash
# 1. KMSKey is Ready
kubectl get kmskey {key-name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# Expected: True

# 2. status.keyId points at the OpenBao Transit path
kubectl get kmskey {key-name} -n {project-namespace} \
  -o jsonpath='{.status.keyId}'
# Expected: <org>/transit/keys/<org>-<project>-<key-name>

# 3. status.currentVersion is at least 1
kubectl get kmskey {key-name} -n {project-namespace} \
  -o jsonpath='{.status.currentVersion}'
# Expected: 1 (or higher after rotations)

# 4. Round-trip test
echo -n "test" | kube-dc kms encrypt {key-name} --plaintext-file=/dev/stdin \
  | kube-dc kms decrypt {key-name} --ciphertext-file=/dev/stdin
# Expected: test
```

## Safety

- Plaintext NEVER leaves OpenBao. Workloads call encrypt/decrypt by key reference; key material is non-exportable.
- Rotation is the standard mitigation; min_decryption_version advancement is the **nuclear** mitigation. Never advance casually — anything below the new floor becomes unrecoverable.
- `deletionPolicy: retain` is the default for a reason: a deleted Transit key would make every existing ciphertext (and every backup envelope wrapping a DEK with that key) unrecoverable. Don't `schedule` deletion without confirming nothing references the key.
- Workloads authenticate via projected SA tokens (audience=openbao). NEVER hardcode an OpenBao token in a Secret — they expire and the platform doesn't manage their lifecycle. Use the projected token volume pattern.
- The same KMSKey CRD powers managed-K8s etcd encryption-at-rest. If a tenant cluster opted into encryption, the controller auto-created a `<cluster>-etcd` KMSKey with purpose=etcd. Do NOT delete that key, schedule it for deletion, or advance its min_decryption_version — every etcd row + every encrypted backup depends on it. See the `manage-cluster` skill for KEK rotation on those keys.
