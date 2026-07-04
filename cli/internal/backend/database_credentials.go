// Typed wrappers for the /api/database-credentials/* surface (M4-T04).
//
// Same shape as the kms / certificates / secrets backend clients —
// all CLI `db credentials` verbs talk to
// `backend.<domain>/api/database-credentials/...` so audit emission,
// admission, and per-role policy gates are uniform with the UI.
// The CLI never reaches the kube-apiserver directly for DBCPs.

package backend

import (
	"context"
	"fmt"
)

// DBCredentialRotation mirrors DatabaseCredentialPolicy.spec.rotation.
type DBCredentialRotation struct {
	Interval string `json:"interval,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// DBCredentialSync mirrors DatabaseCredentialPolicy.spec.sync.
// Enabled is *bool to match the v1alpha1 API — nil means "unset, let
// the webhook default it" (defaults to true for static-rotated).
type DBCredentialSync struct {
	Enabled          *bool  `json:"enabled,omitempty"`
	TargetSecretName string `json:"targetSecretName,omitempty"`
}

// DBCredentialPolicyStatus mirrors the backend's summarised status block.
type DBCredentialPolicyStatus struct {
	Endpoint         string           `json:"endpoint,omitempty"`
	Username         string           `json:"username,omitempty"`
	TargetSecretName string           `json:"targetSecretName,omitempty"`
	LastRotatedTime  string           `json:"lastRotatedTime,omitempty"`
	NextRotationTime string           `json:"nextRotationTime,omitempty"`
	LeaseSupported   bool             `json:"leaseSupported,omitempty"`
	Conditions       []map[string]any `json:"conditions,omitempty"`
}

// DBCredentialPolicySummary mirrors the backend's summariseDBCP shape.
type DBCredentialPolicySummary struct {
	Name              string                   `json:"name"`
	Namespace         string                   `json:"namespace"`
	CreationTimestamp string                   `json:"creationTimestamp,omitempty"`
	DatabaseRef       string                   `json:"databaseRef"`
	Mode              string                   `json:"mode"`
	Username          string                   `json:"username,omitempty"`
	Rotation          DBCredentialRotation     `json:"rotation,omitempty"`
	Sync              DBCredentialSync         `json:"sync,omitempty"`
	Role              string                   `json:"role,omitempty"`
	TTL               string                   `json:"ttl,omitempty"`
	MaxTTL            string                   `json:"maxTtl,omitempty"`
	Status            DBCredentialPolicyStatus `json:"status"`
}

type DBCredentialPolicyList struct {
	Items []DBCredentialPolicySummary `json:"items"`
}

func (c *Client) ListDBCredentialPolicies(ctx context.Context, namespace string) (*DBCredentialPolicyList, error) {
	var out DBCredentialPolicyList
	if err := c.do(ctx, "GET", "/api/database-credentials/"+pathEscape(namespace), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetDBCredentialPolicy(ctx context.Context, namespace, name string) (*DBCredentialPolicySummary, error) {
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out DBCredentialPolicySummary
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDBCredentialPolicyOptions mirrors the POST body the backend
// accepts at /api/database-credentials/:ns/:name. The backend's
// validateCreatePayload requires spec.databaseRef.name + spec.mode;
// other fields are defaulted by the validating webhook.
type CreateDBCredentialPolicyOptions struct {
	Spec CreateDBCredentialPolicySpec `json:"spec"`
}

type CreateDBCredentialPolicySpec struct {
	DatabaseRef DatabaseRef          `json:"databaseRef"`
	Mode        string               `json:"mode"`
	Username    string               `json:"username,omitempty"`
	Rotation    DBCredentialRotation `json:"rotation,omitempty"`
	Sync        DBCredentialSync     `json:"sync,omitempty"`
	Role        string               `json:"role,omitempty"`
	TTL         string               `json:"ttl,omitempty"`
	MaxTTL      string               `json:"maxTtl,omitempty"`
}

type DatabaseRef struct {
	Name string `json:"name"`
}

func (c *Client) CreateDBCredentialPolicy(ctx context.Context, namespace, name string, opts CreateDBCredentialPolicyOptions) error {
	if opts.Spec.DatabaseRef.Name == "" {
		return fmt.Errorf("create: spec.databaseRef.name is required")
	}
	if opts.Spec.Mode == "" {
		return fmt.Errorf("create: spec.mode is required (static-rotated|dynamic)")
	}
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name)
	// Backend returns the raw K8s CR JSON on 201 — we don't need to
	// decode it; callers re-fetch via Get if they want the summarised
	// shape.
	return c.do(ctx, "POST", p, opts, nil)
}

func (c *Client) DeleteDBCredentialPolicy(ctx context.Context, namespace, name string) error {
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name)
	return c.do(ctx, "DELETE", p, nil, nil)
}

// RotateDBCredentialPolicy calls /rotate. With rotateRoot=true it sets
// ?root=true which switches the backend from rotateDatabaseStaticRole
// (project-manager+) to rotateDatabaseRoot (project-admin only).
type RotateDBCredentialPolicyResult struct {
	Rotated            bool   `json:"rotated"`
	Target             string `json:"target"`
	ReconcileTriggered bool   `json:"reconcileTriggered"`
}

func (c *Client) RotateDBCredentialPolicy(ctx context.Context, namespace, name string, rotateRoot bool) (*RotateDBCredentialPolicyResult, error) {
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name) + "/rotate"
	if rotateRoot {
		p += "?root=true"
	}
	var out RotateDBCredentialPolicyResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDBCredentials calls /credentials — returns the current rotated
// username + password from OpenBao static-creds. Authorization gated
// by OpenBao policy on the per-user OIDC token (developer/viewer
// tiers 403 here; project-manager+ allowed).
type DBCredentials struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	LastVaultRotation string `json:"lastVaultRotation,omitempty"`
	RotationPeriod    int    `json:"rotationPeriod,omitempty"`
	TTL               int    `json:"ttl,omitempty"`
}

func (c *Client) GetDBCredentials(ctx context.Context, namespace, name string) (*DBCredentials, error) {
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name) + "/credentials"
	var out DBCredentials
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IssueDBCredentials calls /issue — dynamic-mode credential issue.
// Phase-1 the controller returns 501 / DynamicModeDeferred because
// the dynamic-role plumbing isn't wired yet; the CLI surface is
// shipped so the surface doesn't shift when the controller catches up.
type DBLease struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	LeaseId       string `json:"leaseId,omitempty"`
	LeaseDuration int    `json:"leaseDuration,omitempty"`
}

func (c *Client) IssueDBCredentials(ctx context.Context, namespace, name string) (*DBLease, error) {
	p := "/api/database-credentials/" + pathEscape(namespace) + "/" + pathEscape(name) + "/issue"
	var out DBLease
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
