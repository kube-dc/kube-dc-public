# Envelope Encryption — Python helper

Self-contained Python helper that uses a Kube-DC `KMSKey` to envelope-
encrypt arbitrary-sized payloads. Mirrors the wire format Kube-DC's own
managed-K8s etcd backup uses:

```
ciphertext = NONCE (12B) || CIPHERTEXT || GCM_TAG (16B)
+ wrappedDek = "vault:vN:..." (small; store next to the ciphertext)
```

Uses `hvac` (wire-compatible with OpenBao) + `cryptography` for AES-GCM.

## Pod requirements

Same as the [Go helper](./envelope-encryption-go.md) — mount a
projected SA token at `/var/run/secrets/openbao/token` with
`audience: openbao`. The pod's `ServiceAccount` needs the developer
OrganizationGroup binding so OpenBao accepts the login.

## Code

`requirements.txt`:

```text
hvac>=2.1.0
cryptography>=42.0.0
```

`envelope.py`:

```python
"""
Envelope encryption helpers for Kube-DC KMS.

Wire format matches the platform managed-K8s etcd backup envelope:
    nonce (12B) || ciphertext || tag (16B)
+ a sidecar wrappedDek "vault:vN:..." stored next to the ciphertext.

The DEK is generated locally; only the wrapped DEK is round-tripped
through OpenBao. Plaintext key material never leaves the process.
"""
from __future__ import annotations

import base64
import os
from dataclasses import dataclass

import hvac
from cryptography.hazmat.primitives.ciphers.aead import AESGCM


@dataclass
class KMSConfig:
    addr: str                # https://bao.kube-dc.cloud
    namespace: str           # your Org name
    transit: str             # "<project-namespace>/transit"
    key_name: str            # "<org>-<project>-<keyref>"
    role: str                # OpenBao K8s-auth role, e.g. "developer-<org>"
    token_file: str = "/var/run/secrets/openbao/token"


def _client(cfg: KMSConfig) -> hvac.Client:
    """Login to OpenBao via the projected SA token. Returns an
    authenticated hvac.Client scoped to the Org namespace."""
    cli = hvac.Client(url=cfg.addr, namespace=cfg.namespace)
    with open(cfg.token_file) as f:
        cli.auth.kubernetes.login(role=cfg.role, jwt=f.read(), mount_point="k8s-host")
    return cli


def encrypt_envelope(cfg: KMSConfig, plaintext: bytes) -> tuple[bytes, str]:
    """Generate a fresh DEK, wrap it via the KMSKey, encrypt the
    plaintext locally with AES-256-GCM. Returns
    (nonce||ciphertext||tag, wrappedDek)."""
    cli = _client(cfg)

    # 1. Fresh 32-byte DEK
    dek = os.urandom(32)

    # 2. Wrap via Transit
    wrap_resp = cli.write(
        f"{cfg.transit}/encrypt/{cfg.key_name}",
        plaintext=base64.b64encode(dek).decode(),
    )
    wrapped = wrap_resp["data"]["ciphertext"]

    # 3. AES-256-GCM encrypt
    nonce = os.urandom(12)
    aead = AESGCM(dek)
    ct = aead.encrypt(nonce, plaintext, None)   # ct = ciphertext || tag
    return nonce + ct, wrapped


def decrypt_envelope(cfg: KMSConfig, blob: bytes, wrapped_dek: str) -> bytes:
    """Reverse of encrypt_envelope."""
    cli = _client(cfg)

    # 1. Unwrap the DEK
    unwrap_resp = cli.write(
        f"{cfg.transit}/decrypt/{cfg.key_name}",
        ciphertext=wrapped_dek,
    )
    dek = base64.b64decode(unwrap_resp["data"]["plaintext"])

    # 2. AES-256-GCM decrypt
    nonce, ct = blob[:12], blob[12:]
    return AESGCM(dek).decrypt(nonce, ct, None)
```

## Smoke test

```python
from envelope import KMSConfig, encrypt_envelope, decrypt_envelope

cfg = KMSConfig(
    addr="https://bao.kube-dc.cloud",
    namespace="my-org",
    transit="kv-my-project/transit",
    key_name="my-org-my-project-app-secrets",
    role="developer-my-org",
)

payload = b"this is a multi-megabyte file in real life"
ct, wrapped = encrypt_envelope(cfg, payload)
print("wrappedDek:", wrapped)
print("ciphertext bytes:", len(ct))

pt = decrypt_envelope(cfg, ct, wrapped)
print("plaintext:", pt.decode())
```

## Notes

- AES-256-GCM via `cryptography.hazmat...AESGCM` is authenticated —
  tampering with the ciphertext or wrappedDek causes
  `InvalidTag` (a subclass of `Exception`). Don't blanket-catch.
- For very large payloads (multi-GB), use the `Cipher(algorithms.AES,
  modes.GCM)` builder with chunked `update()` calls rather than
  loading the whole plaintext into memory. The wire format stays the
  same — Kube-DC's own etcd-backup helper does exactly this in
  [envelope.py](https://github.com/shalb/kube-dc/blob/main/images/etcd-backup/envelope.py)
  inside the etcd-backup image.
- The `wrappedDek` is small (~150 bytes for AES-256). Store it
  anywhere the ciphertext lives.
- After a KEK rotation, new envelopes use the new key version
  automatically. Old envelopes keep decrypting via their captured
  version until `min_decryption_version` is manually advanced.
