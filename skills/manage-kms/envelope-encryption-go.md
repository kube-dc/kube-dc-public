# Envelope Encryption — Go helper

Self-contained Go helper that uses a Kube-DC `KMSKey` to envelope-encrypt
arbitrary-sized payloads. Mirrors the wire format Kube-DC's own
managed-K8s etcd backup uses:

```
ciphertext = NONCE (12B) || CIPHERTEXT || GCM_TAG (16B)
+ wrappedDek = "vault:vN:..." (small; store next to the ciphertext)
```

## Pod requirements

The workload pod authenticates against OpenBao via a projected SA token
(audience=openbao). Add the volume:

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

The pod's `ServiceAccount` needs the developer-tier OrganizationGroup
binding for the project — that ships with the right OpenBao transit
encrypt/decrypt grants. No extra policy work.

## Code

`go.mod`:

```text
require github.com/openbao/openbao/api/v2 v2.5.1
```

`envelope.go`:

```go
package envelope

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "io"
    "os"

    bao "github.com/openbao/openbao/api/v2"
)

// Wiring — fill these in from your config. The Transit mount lives at
// the root of the Org's OpenBao namespace; only the key name varies.
// KeyName follows the Kube-DC convention "<org>-<project>-<KMSKey>".
type KMSConfig struct {
    Addr      string   // e.g. https://bao.kube-dc.cloud
    Namespace string   // your Org name (matches the kube-dc.com/project annotation's first half)
    KeyName   string   // "<org>-<project>-<KMSKey>", e.g. "shalb-docs-app-secrets"
    Role      string   // OpenBao K8s-auth role, e.g. "developer-shalb"
    TokenFile string   // default "/var/run/secrets/openbao/token"
}

func defaultTokenFile(c KMSConfig) string {
    if c.TokenFile == "" {
        return "/var/run/secrets/openbao/token"
    }
    return c.TokenFile
}

func login(c KMSConfig) (*bao.Client, error) {
    cfg := bao.DefaultConfig()
    cfg.Address = c.Addr
    cli, err := bao.NewClient(cfg)
    if err != nil { return nil, err }
    cli.SetNamespace(c.Namespace)

    jwt, err := os.ReadFile(defaultTokenFile(c))
    if err != nil { return nil, fmt.Errorf("read SA token: %w", err) }

    sec, err := cli.Logical().Write("auth/k8s-host/login", map[string]interface{}{
        "role": c.Role,
        "jwt":  string(jwt),
    })
    if err != nil { return nil, fmt.Errorf("openbao login: %w", err) }
    cli.SetToken(sec.Auth.ClientToken)
    return cli, nil
}

// EncryptEnvelope encrypts plaintext with a fresh DEK, wraps the DEK
// via the configured KMSKey, and returns (NONCE||CIPHERTEXT||TAG,
// wrappedDek). Store both next to each other.
func EncryptEnvelope(c KMSConfig, plaintext []byte) (ciphertext []byte, wrappedDek string, err error) {
    cli, err := login(c)
    if err != nil { return }

    // 1. Generate a fresh 32-byte DEK
    dek := make([]byte, 32)
    if _, err = rand.Read(dek); err != nil { return }

    // 2. Wrap the DEK with the KMSKey via Transit
    resp, err := cli.Logical().Write(fmt.Sprintf("transit/encrypt/%s", c.KeyName), map[string]interface{}{
        "plaintext": base64.StdEncoding.EncodeToString(dek),
    })
    if err != nil { return }
    wrappedDek = resp.Data["ciphertext"].(string)

    // 3. Encrypt the plaintext locally with AES-256-GCM
    block, err := aes.NewCipher(dek); if err != nil { return }
    gcm, err := cipher.NewGCM(block); if err != nil { return }
    nonce := make([]byte, gcm.NonceSize())
    if _, err = io.ReadFull(rand.Reader, nonce); err != nil { return }
    // gcm.Seal(dst, nonce, plaintext, additionalData) returns
    // ciphertext||tag. We prepend the nonce so the whole envelope is
    // a single self-contained blob.
    ciphertext = gcm.Seal(nonce, nonce, plaintext, nil)
    return
}

// DecryptEnvelope reverses EncryptEnvelope.
func DecryptEnvelope(c KMSConfig, ciphertext []byte, wrappedDek string) ([]byte, error) {
    cli, err := login(c)
    if err != nil { return nil, err }

    // 1. Unwrap the DEK via Transit
    resp, err := cli.Logical().Write(fmt.Sprintf("transit/decrypt/%s", c.KeyName), map[string]interface{}{
        "ciphertext": wrappedDek,
    })
    if err != nil { return nil, fmt.Errorf("transit decrypt: %w", err) }
    dek, err := base64.StdEncoding.DecodeString(resp.Data["plaintext"].(string))
    if err != nil { return nil, err }

    // 2. Decrypt locally
    block, err := aes.NewCipher(dek); if err != nil { return nil, err }
    gcm, err := cipher.NewGCM(block); if err != nil { return nil, err }
    n := gcm.NonceSize()
    if len(ciphertext) < n {
        return nil, fmt.Errorf("ciphertext too short")
    }
    return gcm.Open(nil, ciphertext[:n], ciphertext[n:], nil)
}
```

## Smoke test

```go
package main

import (
    "fmt"
    "log"

    "your/module/envelope"
)

func main() {
    cfg := envelope.KMSConfig{
        Addr:      "https://bao.kube-dc.cloud",
        Namespace: "my-org",
        KeyName:   "my-org-my-project-app-secrets",
        Role:      "developer-my-org",
    }

    payload := []byte("this is a multi-megabyte file in real life")
    ct, wrapped, err := envelope.EncryptEnvelope(cfg, payload)
    if err != nil { log.Fatal(err) }

    fmt.Println("wrappedDek:", wrapped)
    fmt.Println("ciphertext bytes:", len(ct))

    pt, err := envelope.DecryptEnvelope(cfg, ct, wrapped)
    if err != nil { log.Fatal(err) }
    fmt.Println("plaintext:", string(pt))
}
```

## Notes

- The DEK is generated **locally** by `crypto/rand` — it never leaves
  your process unencrypted (after wrapping, only OpenBao can unwrap).
- AES-256-GCM is authenticated — tampering with the ciphertext or
  truncating it causes `gcm.Open` to fail with `cipher: message
  authentication failed`. Don't catch and ignore that error.
- The `wrappedDek` is small (~150 bytes for AES-256). Stash it
  anywhere the ciphertext lives — a database column, an S3 object
  tag, a sidecar object.
- After a KEK rotation, new envelopes use the new key version
  automatically (the `vault:vN:` prefix increments). Old envelopes
  keep decrypting via their captured version until
  `min_decryption_version` is manually advanced.
