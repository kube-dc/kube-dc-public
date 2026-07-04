// Typed direct-K8s wrappers for security.kube-dc.com/v1alpha1
// ManagedSecret. Backs the kubectl-style verbs in the CLI
// (list / get-metadata / sync-patch / soft-delete) — the value
// plane goes via the backend, not here.

package k8sapi

import (
	"context"
	"fmt"
	"net/url"
)

const (
	secGroup    = "security.kube-dc.com"
	secVersion  = "v1alpha1"
	secResource = "managedsecrets"
)

// ManagedSecret is the partial shape the CLI cares about. We
// preserve the raw JSON so `-o yaml` and `-o json` outputs round-trip
// the full object without us needing to model every field.
type ManagedSecret struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   ObjectMeta             `json:"metadata"`
	Spec       ManagedSecretSpec      `json:"spec"`
	Status     ManagedSecretStatus    `json:"status,omitempty"`
}

type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

type ManagedSecretSpec struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Rotation    map[string]any         `json:"rotation,omitempty"`
	Sync        ManagedSecretSyncSpec  `json:"sync,omitempty"`
}

type ManagedSecretSyncSpec struct {
	// Enabled must serialize even when false so a `create --sync-disabled`
	// request faithfully expresses tenant intent on the wire. The earlier
	// `omitempty` here was the same class of bug as the M1-T07 sync-patch
	// P1 (`buildSyncPatch` now uses map[string]any for the same reason).
	Enabled          bool     `json:"enabled"`
	TargetSecretName string   `json:"targetSecretName,omitempty"`
	RefreshInterval  string   `json:"refreshInterval,omitempty"`
	Keys             []string `json:"keys,omitempty"`
}

type ManagedSecretStatus struct {
	Conditions       []map[string]any `json:"conditions,omitempty"`
	SyncedSecretName string           `json:"syncedSecretName,omitempty"`
}

type ManagedSecretList struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Items      []ManagedSecret `json:"items"`
}

func crBasePath(namespace string) string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s",
		secGroup, secVersion, url.PathEscape(namespace), secResource)
}

func crItemPath(namespace, name string) string {
	return crBasePath(namespace) + "/" + url.PathEscape(name)
}

// ListManagedSecrets lists CRs in the project namespace. RBAC is
// enforced natively by the kube-apiserver — the user's JWT must
// satisfy the `list managedsecrets.security.kube-dc.com` verb in
// the namespace.
func (c *Client) ListManagedSecrets(ctx context.Context, namespace string) (*ManagedSecretList, error) {
	var out ManagedSecretList
	if err := c.do(ctx, "GET", crBasePath(namespace), nil, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetManagedSecret(ctx context.Context, namespace, name string) (*ManagedSecret, error) {
	var out ManagedSecret
	if err := c.do(ctx, "GET", crItemPath(namespace, name), nil, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateManagedSecret POSTs a new ManagedSecret CR. The caller fills
// in metadata.name/namespace + spec; APIVersion/Kind are stamped here
// so callers don't have to. RBAC: `create managedsecrets...` in the
// target namespace (developers and above).
func (c *Client) CreateManagedSecret(ctx context.Context, ms *ManagedSecret) (*ManagedSecret, error) {
	if ms == nil || ms.Metadata.Name == "" || ms.Metadata.Namespace == "" {
		return nil, fmt.Errorf("CreateManagedSecret: metadata.name and metadata.namespace are required")
	}
	ms.APIVersion = secGroup + "/" + secVersion
	ms.Kind = "ManagedSecret"
	var out ManagedSecret
	if err := c.do(ctx, "POST", crBasePath(ms.Metadata.Namespace), ms, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchManagedSecretSync applies a strategic-merge-style spec.sync
// patch. Takes a sparse map[string]any (not a struct) because the
// caller must be able to send {"enabled":false} without it being
// dropped by json.Marshal — the prior typed-struct version used
// `omitempty` on every field and silently swallowed --enabled=false
// (M1-T07 first review pass P1).
//
// The backend POST /sync endpoint takes the same shape; the CLI's
// hybrid design uses this direct path because there's no
// orchestration to delegate. Audit emission for this op is captured
// by OpenBao native audit only (not the Loki audit stream the
// backend emits); UI-driven syncs continue to land in Loki.
func (c *Client) PatchManagedSecretSync(ctx context.Context, namespace, name string, syncPatch map[string]any) (*ManagedSecret, error) {
	if len(syncPatch) == 0 {
		return nil, fmt.Errorf("PatchManagedSecretSync: at least one field is required")
	}
	patch := map[string]any{
		"spec": map[string]any{"sync": syncPatch},
	}
	var out ManagedSecret
	if err := c.do(ctx, "PATCH", crItemPath(namespace, name), patch, &out, "application/merge-patch+json"); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteManagedSecret(ctx context.Context, namespace, name string) error {
	return c.do(ctx, "DELETE", crItemPath(namespace, name), nil, nil, "")
}
