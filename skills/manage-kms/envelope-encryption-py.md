# Envelope Encryption Helper for Python

This sample encrypts payloads locally with AES-256-GCM and accepts explicit
wrap/unwrap callbacks. It does not authenticate to OpenBao.

Kube-DC does not provision a general-purpose Transit role for application
ServiceAccounts. Implement the callbacks only after an operator provides an
approved workload authentication path or supported service integration.

## Dependency

```text
cryptography>=42.0.0
```

## Code

```python
from __future__ import annotations

import os
from collections.abc import Callable
from dataclasses import dataclass

from cryptography.hazmat.primitives.ciphers.aead import AESGCM


WrapDEK = Callable[[bytes], str]
UnwrapDEK = Callable[[str], bytes]


@dataclass(frozen=True)
class Envelope:
    ciphertext: bytes  # nonce || ciphertext || GCM tag
    wrapped_dek: str   # opaque Transit ciphertext, for example vault:v2:...


def encrypt(plaintext: bytes, wrap_dek: WrapDEK) -> Envelope:
    dek = bytearray(os.urandom(32))
    try:
        wrapped = wrap_dek(bytes(dek))
        nonce = os.urandom(12)
        ciphertext = AESGCM(bytes(dek)).encrypt(nonce, plaintext, None)
        return Envelope(nonce + ciphertext, wrapped)
    finally:
        for index in range(len(dek)):
            dek[index] = 0


def decrypt(envelope: Envelope, unwrap_dek: UnwrapDEK) -> bytes:
    dek = bytearray(unwrap_dek(envelope.wrapped_dek))
    try:
        if len(dek) != 32:
            raise ValueError("unwrapped DEK must be 32 bytes")
        if len(envelope.ciphertext) < 12 + 16:
            raise ValueError("ciphertext is too short")
        nonce = envelope.ciphertext[:12]
        ciphertext = envelope.ciphertext[12:]
        return AESGCM(bytes(dek)).decrypt(nonce, ciphertext, None)
    finally:
        for index in range(len(dek)):
            dek[index] = 0
```

## Integration Contract

- `wrap_dek` sends only the 32-byte DEK to the approved KMS encrypt operation
  and returns the complete `vault:vN:...` value.
- `unwrap_dek` sends the opaque value to the matching decrypt operation and
  returns exactly 32 bytes.
- Store any encryption context alongside the envelope and require it on unwrap.
- Do not catch and ignore `InvalidTag`; it means authentication failed.
- Python cannot guarantee that every immutable copy is erased from memory. The
  bytearray cleanup is best effort, not a formal memory-erasure guarantee.
