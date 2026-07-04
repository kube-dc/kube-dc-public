// Typed wrappers for the /api/kms/* surface (M3-T03).
//
// Same shape as the certificates / secrets backend clients — all CLI
// kms verbs talk to `backend.<domain>/api/kms/...` so the audit + admission
// guards there are exercised for CLI users too. The CLI never reaches
// the kube-apiserver directly for KMSKey.

package backend

import (
	"context"
	"fmt"
)

// KMSKeyRotation mirrors the KMSKey.spec.rotation block.
type KMSKeyRotation struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Interval string `json:"interval,omitempty"`
}

// KMSKeyStatus is the status-mirror block the reconciler keeps in
// sync with the underlying OpenBao Transit key. All fields are
// optional — the reconciler only populates them once the key is
// observed via ReadKey.
type KMSKeyStatus struct {
	KeyId                string           `json:"keyId,omitempty"`
	CurrentVersion       int              `json:"currentVersion,omitempty"`
	MinDecryptionVersion int              `json:"minDecryptionVersion,omitempty"`
	LastRotatedTime      string           `json:"lastRotatedTime,omitempty"`
	NextRotationTime     string           `json:"nextRotationTime,omitempty"`
	ScheduledDeletion    string           `json:"scheduledDeletion,omitempty"`
	Conditions           []map[string]any `json:"conditions,omitempty"`
}

// KMSKeySummary mirrors the backend's summariseKMSKey shape.
type KMSKeySummary struct {
	Name              string         `json:"name"`
	Namespace         string         `json:"namespace"`
	CreationTimestamp string         `json:"creationTimestamp,omitempty"`
	Purpose           string         `json:"purpose"`
	Algorithm         string         `json:"algorithm"`
	DeletionPolicy    string         `json:"deletionPolicy"`
	Rotation          KMSKeyRotation `json:"rotation"`
	Exportable        bool           `json:"exportable"`
	Status            KMSKeyStatus   `json:"status"`
}

type KMSKeyList struct {
	Items []KMSKeySummary `json:"items"`
}

func (c *Client) ListKMSKeys(ctx context.Context, namespace string) (*KMSKeyList, error) {
	var out KMSKeyList
	if err := c.do(ctx, "GET", "/api/kms/"+pathEscape(namespace), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetKMSKey(ctx context.Context, namespace, name string) (*KMSKeySummary, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out KMSKeySummary
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateKMSKeyOptions mirrors the POST body the backend accepts.
type CreateKMSKeyOptions struct {
	Purpose        string         `json:"purpose,omitempty"`
	Algorithm      string         `json:"algorithm,omitempty"`
	DeletionPolicy string         `json:"deletionPolicy,omitempty"`
	Rotation       KMSKeyRotation `json:"rotation,omitempty"`
}

func (c *Client) CreateKMSKey(ctx context.Context, namespace, name string, opts CreateKMSKeyOptions) (*KMSKeySummary, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out KMSKeySummary
	if err := c.do(ctx, "POST", p, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DeleteKMSKeyResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Deleted   bool   `json:"deleted"`
}

func (c *Client) DeleteKMSKey(ctx context.Context, namespace, name string) (*DeleteKMSKeyResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out DeleteKMSKeyResult
	if err := c.do(ctx, "DELETE", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateKMSKey calls /api/kms/:ns/:name/rotate. Returns the assigned
// transit key name for surfacing in audit logs.
type RotateKMSKeyResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	KeyName   string `json:"keyName"`
	Rotated   bool   `json:"rotated"`
}

func (c *Client) RotateKMSKey(ctx context.Context, namespace, name string) (*RotateKMSKeyResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/rotate"
	var out RotateKMSKeyResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EncryptKMSOptions / DecryptKMSOptions split utf-8 vs base64 plaintext
// so callers don't have to pre-encode short strings. The backend
// enforces the 64 KiB cap; this wrapper just forwards.
type EncryptKMSOptions struct {
	Plaintext    string `json:"plaintext,omitempty"`
	PlaintextB64 string `json:"plaintextB64,omitempty"`
	Context      string `json:"context,omitempty"`
}

type EncryptKMSResult struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Ciphertext string `json:"ciphertext"`
	KeyName    string `json:"keyName"`
}

func (c *Client) EncryptKMS(ctx context.Context, namespace, name string, opts EncryptKMSOptions) (*EncryptKMSResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/encrypt"
	var out EncryptKMSResult
	if err := c.do(ctx, "POST", p, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DecryptKMSOptions struct {
	Ciphertext string `json:"ciphertext"`
	Context    string `json:"context,omitempty"`
}

type DecryptKMSResult struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Plaintext    string `json:"plaintext"`
	PlaintextB64 string `json:"plaintextB64"`
	KeyName      string `json:"keyName"`
}

func (c *Client) DecryptKMS(ctx context.Context, namespace, name string, opts DecryptKMSOptions) (*DecryptKMSResult, error) {
	if opts.Ciphertext == "" {
		return nil, fmt.Errorf("decrypt: ciphertext is required")
	}
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/decrypt"
	var out DecryptKMSResult
	if err := c.do(ctx, "POST", p, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type SetMinDecryptionVersionResult struct {
	Name                 string `json:"name"`
	Namespace            string `json:"namespace"`
	KeyName              string `json:"keyName"`
	MinDecryptionVersion int    `json:"minDecryptionVersion"`
}

func (c *Client) SetMinDecryptionVersion(ctx context.Context, namespace, name string, version int) (*SetMinDecryptionVersionResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/min-decryption-version"
	var out SetMinDecryptionVersionResult
	if err := c.do(ctx, "POST", p, map[string]int{"version": version}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DeletionPolicyResult struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	DeletionPolicy string `json:"deletionPolicy"`
}

func (c *Client) ScheduleDeleteKMSKey(ctx context.Context, namespace, name string) (*DeletionPolicyResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/schedule-delete"
	var out DeletionPolicyResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelDeleteKMSKey(ctx context.Context, namespace, name string) (*DeletionPolicyResult, error) {
	p := "/api/kms/" + pathEscape(namespace) + "/" + pathEscape(name) + "/cancel-delete"
	var out DeletionPolicyResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
