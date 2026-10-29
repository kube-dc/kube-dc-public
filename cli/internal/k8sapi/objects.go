package k8sapi

import (
	"context"
	"net/http"
	"net/url"
)

// SecurityGroupVersion serves KMSKey, ManagedCertificate,
// DatabaseCredentialPolicy and ManagedSecret.
const SecurityGroupVersion = "security.kube-dc.com/v1alpha1"

// PlatformGroupVersion serves the typed platform operations (the
// platform.kube-dc.com extension API).
const PlatformGroupVersion = "platform.kube-dc.com/v1alpha1"

// Object is a namespaced custom resource as the API server stores it. Spec
// and status stay maps: the CLI shows them, the controller owns them.
type Object struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   ObjectMeta     `json:"metadata"`
	Spec       map[string]any `json:"spec,omitempty"`
	Status     map[string]any `json:"status,omitempty"`
}

// ObjectList is a list of Objects.
type ObjectList struct {
	Items []Object `json:"items"`
}

// Resource addresses one namespaced resource of a group version.
type Resource struct {
	GroupVersion string // e.g. security.kube-dc.com/v1alpha1
	Plural       string // e.g. kmskeys
	Kind         string // e.g. KMSKey
}

func (r Resource) path(namespace string) string {
	return "/apis/" + r.GroupVersion + "/namespaces/" + url.PathEscape(namespace) + "/" + r.Plural
}

// List lists r in namespace with the caller's token.
func (c *Client) List(ctx context.Context, r Resource, namespace string) (*ObjectList, error) {
	var out ObjectList
	return &out, c.do(ctx, http.MethodGet, r.path(namespace), nil, &out, "")
}

// Get reads one object.
func (c *Client) Get(ctx context.Context, r Resource, namespace, name string) (*Object, error) {
	var out Object
	return &out, c.do(ctx, http.MethodGet, r.path(namespace)+"/"+url.PathEscape(name), nil, &out, "")
}

// Create creates an object of r named name in namespace with spec.
func (c *Client) Create(ctx context.Context, r Resource, namespace, name string, spec map[string]any) (*Object, error) {
	var out Object
	in := &Object{APIVersion: r.GroupVersion, Kind: r.Kind, Metadata: ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
	return &out, c.do(ctx, http.MethodPost, r.path(namespace), in, &out, "")
}

// Delete deletes one object.
func (c *Client) Delete(ctx context.Context, r Resource, namespace, name string) error {
	return c.do(ctx, http.MethodDelete, r.path(namespace)+"/"+url.PathEscape(name), nil, nil, "")
}

// Patch merge-patches r/name in namespace with the caller's token.
func (c *Client) Patch(ctx context.Context, r Resource, namespace, name string, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, r.path(namespace)+"/"+url.PathEscape(name), patch, nil, "application/merge-patch+json")
}

// SecretData reads a core Secret with the caller's token and returns its
// data decoded.
func (c *Client) SecretData(ctx context.Context, namespace, name string) (map[string]string, error) {
	var out struct {
		Data map[string][]byte `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(namespace)+"/secrets/"+url.PathEscape(name), nil, &out, ""); err != nil {
		return nil, err
	}
	data := make(map[string]string, len(out.Data))
	for k, v := range out.Data {
		data[k] = string(v)
	}
	return data, nil
}

// MachineIdentities live in the organization namespace.
var MachineIdentities = Resource{"kube-dc.com/v1", "machineidentities", "MachineIdentity"}

// The security resources the CLI manages.
var (
	KMSKeys                    = Resource{SecurityGroupVersion, "kmskeys", "KMSKey"}
	ManagedCertificates        = Resource{SecurityGroupVersion, "managedcertificates", "ManagedCertificate"}
	DatabaseCredentialPolicies = Resource{SecurityGroupVersion, "databasecredentialpolicies", "DatabaseCredentialPolicy"}
	// Create-only request objects: the result comes back in status.
	KMSEncryptions = Resource{PlatformGroupVersion, "kmsencryptions", "KMSEncryption"}
	KMSDecryptions = Resource{PlatformGroupVersion, "kmsdecryptions", "KMSDecryption"}
	// A ManagedSecret's current value: get, update.
	ManagedSecretValues = Resource{PlatformGroupVersion, "managedsecretvalues", "ManagedSecretValue"}
	// Rotations, database credentials and version destruction.
	KMSKeyRotations             = Resource{PlatformGroupVersion, "kmskeyrotations", "KMSKeyRotation"}
	DatabaseCredentials         = Resource{PlatformGroupVersion, "databasecredentials", "DatabaseCredential"}
	DatabaseCredentialIssues    = Resource{PlatformGroupVersion, "databasecredentialissues", "DatabaseCredentialIssue"}
	DatabaseCredentialRotations = Resource{PlatformGroupVersion, "databasecredentialrotations", "DatabaseCredentialRotation"}
	SecretVersionDestructions   = Resource{PlatformGroupVersion, "managedsecretversiondestructions", "ManagedSecretVersionDestruction"}
)

// SecretValue is a ManagedSecret's current value as the typed operation
// returns it.
type SecretValue struct {
	Data   map[string]string `json:"data"`
	Status struct {
		Version      int    `json:"version"`
		CreatedTime  string `json:"createdTime,omitempty"`
		ValueMissing bool   `json:"valueMissing,omitempty"`
		Storage      *struct {
			CurrentVersion int               `json:"currentVersion"`
			CreatedTime    string            `json:"createdTime,omitempty"`
			UpdatedTime    string            `json:"updatedTime,omitempty"`
			MaxVersions    int               `json:"maxVersions"`
			CasRequired    bool              `json:"casRequired"`
			CustomMetadata map[string]string `json:"customMetadata,omitempty"`
		} `json:"storage,omitempty"`
	} `json:"status"`
}

// GetSecretValue reads a ManagedSecret's current value through the typed
// operation.
func (c *Client) GetSecretValue(ctx context.Context, namespace, name string) (*SecretValue, error) {
	var out SecretValue
	return &out, c.do(ctx, http.MethodGet, ManagedSecretValues.path(namespace)+"/"+url.PathEscape(name), nil, &out, "")
}

// ListRaw lists any namespaced collection at apiPath (e.g. /apis/apps/v1)
// and decodes its items into out.
func (c *Client) ListRaw(ctx context.Context, apiPath, namespace, resource string, out any) error {
	return c.do(ctx, http.MethodGet, apiPath+"/namespaces/"+url.PathEscape(namespace)+"/"+resource, nil, out, "")
}

// PutSecretValue writes a ManagedSecret's value through the typed operation
// and returns the stored version.
func (c *Client) PutSecretValue(ctx context.Context, namespace, name string, data map[string]string) (int, error) {
	body := map[string]any{
		"apiVersion": ManagedSecretValues.GroupVersion, "kind": ManagedSecretValues.Kind,
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"data":     data,
	}
	var out struct {
		Status struct {
			Version int `json:"version"`
		} `json:"status"`
	}
	err := c.do(ctx, http.MethodPut, ManagedSecretValues.path(namespace)+"/"+url.PathEscape(name), body, &out, "")
	return out.Status.Version, err
}
