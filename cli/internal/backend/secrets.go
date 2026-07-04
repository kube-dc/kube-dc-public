// Typed wrappers for the /api/secrets/* surface (M1-T07).

package backend

import (
	"context"
	"fmt"
)

// SecretSummary is the shape returned by GET /api/secrets/:ns and
// the per-secret GET when ?includeValue is omitted.
type SecretSummary struct {
	Name        string                 `json:"name"`
	Namespace   string                 `json:"namespace"`
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Sync        SecretSync             `json:"sync"`
	Status      SecretStatus           `json:"status"`
	OpenBao     *SecretOpenBao         `json:"openbao,omitempty"`
	Value       *SecretValue           `json:"value,omitempty"`
	ValueMissing bool                  `json:"valueMissing,omitempty"`
}

type SecretSync struct {
	Enabled          bool     `json:"enabled"`
	TargetSecretName string   `json:"targetSecretName,omitempty"`
	RefreshInterval  string   `json:"refreshInterval,omitempty"`
	Keys             []string `json:"keys,omitempty"`
}

type SecretStatus struct {
	Conditions       []map[string]any `json:"conditions,omitempty"`
	SyncedSecretName string           `json:"syncedSecretName,omitempty"`
}

type SecretOpenBao struct {
	CurrentVersion int               `json:"currentVersion"`
	CreatedTime    string            `json:"createdTime,omitempty"`
	UpdatedTime    string            `json:"updatedTime,omitempty"`
	MaxVersions    int               `json:"maxVersions"`
	CasRequired    bool              `json:"casRequired"`
	CustomMetadata map[string]string `json:"customMetadata,omitempty"`
}

type SecretValue struct {
	Data     map[string]string `json:"data"`
	Metadata SecretValueMeta   `json:"metadata"`
}

type SecretValueMeta struct {
	Version        int               `json:"version"`
	CreatedTime    string            `json:"createdTime,omitempty"`
	CustomMetadata map[string]string `json:"customMetadata,omitempty"`
}

type SecretList struct {
	Items []SecretSummary `json:"items"`
}

func (c *Client) ListSecrets(ctx context.Context, namespace string) (*SecretList, error) {
	var out SecretList
	if err := c.do(ctx, "GET", "/api/secrets/"+pathEscape(namespace), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetSecret(ctx context.Context, namespace, name string, includeValue bool) (*SecretSummary, error) {
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name)
	if includeValue {
		p += "?includeValue=true"
	}
	var out SecretSummary
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type PutSecretValuesResult struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Mount       string `json:"mount"`
	Version     int    `json:"version"`
	CreatedTime string `json:"createdTime"`
}

func (c *Client) PutSecretValues(ctx context.Context, namespace, name string, data map[string]string) (*PutSecretValuesResult, error) {
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name) + "/values"
	var out PutSecretValuesResult
	if err := c.do(ctx, "PUT", p, map[string]any{"data": data}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SyncOptions carries the partial-update payload for POST /sync. Use
// pointer fields so the caller can distinguish "unset" from "set to
// zero value" — only set fields are forwarded to the backend.
type SyncOptions struct {
	Enabled          *bool
	TargetSecretName *string
	RefreshInterval  *string
	Keys             *[]string
}

func (c *Client) SyncSecret(ctx context.Context, namespace, name string, opts SyncOptions) (*SecretSummary, error) {
	body := map[string]any{}
	if opts.Enabled != nil {
		body["enabled"] = *opts.Enabled
	}
	if opts.TargetSecretName != nil {
		body["targetSecretName"] = *opts.TargetSecretName
	}
	if opts.RefreshInterval != nil {
		body["refreshInterval"] = *opts.RefreshInterval
	}
	if opts.Keys != nil {
		body["keys"] = *opts.Keys
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("sync: at least one of --enabled, --target, --refresh, --keys is required")
	}
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name) + "/sync"
	var out SecretSummary
	if err := c.do(ctx, "POST", p, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type ImportSecretOptions struct {
	SourceSecretName      string `json:"sourceSecretName"`
	SourceSecretNamespace string `json:"sourceSecretNamespace,omitempty"`
	Type                  string `json:"type,omitempty"`
	Description           string `json:"description,omitempty"`
	AllowCrossNamespace   bool   `json:"allowCrossNamespace,omitempty"`
}

type ImportSecretResult struct {
	SecretSummary
	KvVersion    int                    `json:"kvVersion"`
	ImportedFrom map[string]any         `json:"importedFrom"`
}

func (c *Client) ImportSecret(ctx context.Context, namespace, name string, opts ImportSecretOptions) (*ImportSecretResult, error) {
	if opts.SourceSecretName == "" {
		return nil, fmt.Errorf("import: --from is required")
	}
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name) + "/import"
	var out ImportSecretResult
	if err := c.do(ctx, "POST", p, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DeleteSecretResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Deleted   bool   `json:"deleted"`
	Destroyed bool   `json:"destroyed"`
}

func (c *Client) DeleteSecret(ctx context.Context, namespace, name string, destroy bool) (*DeleteSecretResult, error) {
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name)
	if destroy {
		p += "?destroy=true"
	}
	var out DeleteSecretResult
	if err := c.do(ctx, "DELETE", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type ConsumerItem struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace"`
	References []string `json:"references"`
}

type ConsumerList struct {
	Items          []ConsumerItem   `json:"items"`
	Secret         string           `json:"secret"`
	ScannerVersion string           `json:"scannerVersion"`
	Cached         bool             `json:"cached"`
	Errors         []map[string]any `json:"errors,omitempty"`
}

type DestroyVersionResult struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	DestroyedVersion int    `json:"destroyedVersion"`
}

func (c *Client) DestroyKVVersion(ctx context.Context, namespace, name string, version int) (*DestroyVersionResult, error) {
	if version <= 0 {
		return nil, fmt.Errorf("destroy version: version must be > 0")
	}
	p := fmt.Sprintf("/api/secrets/%s/%s/versions/%d/destroy",
		pathEscape(namespace), pathEscape(name), version)
	var out DestroyVersionResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListConsumers(ctx context.Context, namespace, name string) (*ConsumerList, error) {
	p := "/api/secrets/" + pathEscape(namespace) + "/" + pathEscape(name) + "/consumers"
	var out ConsumerList
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
