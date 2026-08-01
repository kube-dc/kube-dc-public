# Envelope Encryption Helper for Go

This sample handles local AES-256-GCM encryption and accepts explicit
`WrapDEK` and `UnwrapDEK` callbacks for KMS operations. It deliberately does
not log in to OpenBao.

Kube-DC does not provision a general-purpose Transit role for application
ServiceAccounts. Supply these callbacks only after an operator provides an
approved workload authentication path or supported service integration. Do not
reuse the External Secrets or Managed Cluster KMS ServiceAccounts.

## Code

```go
package envelope

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

type WrapDEK func(context.Context, []byte) (string, error)
type UnwrapDEK func(context.Context, string) ([]byte, error)

type Envelope struct {
	Ciphertext []byte // nonce || ciphertext || GCM tag
	WrappedDEK string // opaque Transit ciphertext, for example vault:v2:...
}

func Encrypt(
	ctx context.Context,
	plaintext []byte,
	wrap WrapDEK,
) (Envelope, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return Envelope{}, fmt.Errorf("generate DEK: %w", err)
	}
	defer zero(dek)

	wrapped, err := wrap(ctx, dek)
	if err != nil {
		return Envelope{}, fmt.Errorf("wrap DEK: %w", err)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return Envelope{}, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate nonce: %w", err)
	}

	blob := aead.Seal(nonce, nonce, plaintext, nil)
	return Envelope{Ciphertext: blob, WrappedDEK: wrapped}, nil
}

func Decrypt(
	ctx context.Context,
	envelope Envelope,
	unwrap UnwrapDEK,
) ([]byte, error) {
	dek, err := unwrap(ctx, envelope.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap DEK: %w", err)
	}
	defer zero(dek)
	if len(dek) != 32 {
		return nil, fmt.Errorf("unwrapped DEK must be 32 bytes")
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	n := aead.NonceSize()
	if len(envelope.Ciphertext) < n+aead.Overhead() {
		return nil, fmt.Errorf("ciphertext is too short")
	}
	plaintext, err := aead.Open(
		nil,
		envelope.Ciphertext[:n],
		envelope.Ciphertext[n:],
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("authenticate and decrypt: %w", err)
	}
	return plaintext, nil
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
```

## Integration Contract

- `wrap` sends only the 32-byte DEK to the approved KMS encrypt operation and
  returns the complete `vault:vN:...` value.
- `unwrap` sends that opaque value to the matching decrypt operation and
  returns exactly 32 bytes.
- Bind the key identity and any encryption context in deployment configuration;
  store the context with the envelope when one is used.
- Store `Ciphertext` and `WrappedDEK` together. Neither is secret on its own,
  but integrity and availability still matter.
- Treat authentication errors and GCM authentication failures as hard failures.
