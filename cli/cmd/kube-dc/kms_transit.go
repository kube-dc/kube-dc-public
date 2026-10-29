package main

import (
	"context"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
)

// kmsTransit encrypts and decrypts with a KMSKey: through the typed
// platform.kube-dc.com operations where the cluster serves them, else
// through the backend route. Both take and return base64 plaintext. The
// typed operations need no audit gate: the backend audits them itself.
type kmsTransit interface {
	Encrypt(ctx context.Context, ns, key, plaintextB64, encContext string) (ciphertext string, err error)
	Decrypt(ctx context.Context, ns, key, ciphertext, encContext string) (plaintextB64 string, err error)
}

func (s *secretsScope) kmsTransit(ctx context.Context) (kmsTransit, error) {
	if caps, err := s.capabilities(); err == nil && caps.Serves(ctx, k8sapi.PlatformGroupVersion, k8sapi.KMSEncryptions.Plural) {
		k8s, err := s.k8s()
		if err != nil {
			return nil, err
		}
		return typedKMSTransit{k8s}, nil
	}
	bc, err := s.backend()
	if err != nil {
		return nil, err
	}
	return legacyKMSTransit{bc}, nil
}

type typedKMSTransit struct{ c *k8sapi.Client }

func (t typedKMSTransit) Encrypt(ctx context.Context, ns, key, plaintextB64, encContext string) (string, error) {
	spec := map[string]any{"keyRef": map[string]any{"name": key}, "plaintext": plaintextB64}
	if encContext != "" {
		spec["context"] = encContext
	}
	out, err := t.c.Create(ctx, k8sapi.KMSEncryptions, ns, "", spec)
	if err != nil {
		return "", err
	}
	ct, _ := out.Status["ciphertext"].(string)
	if ct == "" {
		return "", fmt.Errorf("encrypt: the platform API returned no ciphertext")
	}
	return ct, nil
}

func (t typedKMSTransit) Decrypt(ctx context.Context, ns, key, ciphertext, encContext string) (string, error) {
	spec := map[string]any{"keyRef": map[string]any{"name": key}, "ciphertext": ciphertext}
	if encContext != "" {
		spec["context"] = encContext
	}
	out, err := t.c.Create(ctx, k8sapi.KMSDecryptions, ns, "", spec)
	if err != nil {
		return "", err
	}
	pt, ok := out.Status["plaintext"].(string)
	if !ok {
		return "", fmt.Errorf("decrypt: the platform API returned no plaintext")
	}
	return pt, nil
}

type legacyKMSTransit struct{ c *backend.Client }

func (l legacyKMSTransit) Encrypt(ctx context.Context, ns, key, plaintextB64, encContext string) (string, error) {
	res, err := l.c.EncryptKMS(ctx, ns, key, backend.EncryptKMSOptions{PlaintextB64: plaintextB64, Context: encContext})
	if err != nil {
		return "", err
	}
	return res.Ciphertext, nil
}

func (l legacyKMSTransit) Decrypt(ctx context.Context, ns, key, ciphertext, encContext string) (string, error) {
	res, err := l.c.DecryptKMS(ctx, ns, key, backend.DecryptKMSOptions{Ciphertext: ciphertext, Context: encContext})
	if err != nil {
		return "", err
	}
	return res.PlaintextB64, nil
}
