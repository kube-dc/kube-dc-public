// Typed wrappers for the /api/certificates/* surface (M2-T06).
//
// All routes go through `backend.<domain>/api/certificates/...` —
// same pattern as the secrets module. The CLI never talks to the
// kube-apiserver directly for ManagedCertificate because the backend
// already does the audit emission + cross-Org JWT guard we want.

package backend

import (
	"context"
	"fmt"
)

// CertificateSummary mirrors the summariseManagedCertificate shape
// emitted by the backend (ui/backend/controllers/certificatesModule.js).
type CertificateSummary struct {
	Name              string             `json:"name"`
	Namespace         string             `json:"namespace"`
	CreationTimestamp string             `json:"creationTimestamp,omitempty"`
	Type              string             `json:"type"`
	Purpose           string             `json:"purpose"`
	DnsNames          []string           `json:"dnsNames"`
	Duration          string             `json:"duration,omitempty"`
	RenewBefore       string             `json:"renewBefore,omitempty"`
	TargetSecretName  string             `json:"targetSecretName"`
	Status            CertificateStatus  `json:"status"`
}

// CertificateStatus is the status-mirror block the reconciler keeps
// in sync with the underlying cert-manager Certificate. Fields are
// all optional — the reconciler only populates them once the
// cert-manager Certificate becomes observable.
type CertificateStatus struct {
	Issuer                string            `json:"issuer,omitempty"`
	CertificateSecretName string            `json:"certificateSecretName,omitempty"`
	NotBefore             string            `json:"notBefore,omitempty"`
	NotAfter              string            `json:"notAfter,omitempty"`
	RenewalTime           string            `json:"renewalTime,omitempty"`
	Conditions            []map[string]any  `json:"conditions,omitempty"`
}

type CertificateList struct {
	Items []CertificateSummary `json:"items"`
}

func (c *Client) ListCertificates(ctx context.Context, namespace string) (*CertificateList, error) {
	var out CertificateList
	if err := c.do(ctx, "GET", "/api/certificates/"+pathEscape(namespace), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetCertificate(ctx context.Context, namespace, name string) (*CertificateSummary, error) {
	p := "/api/certificates/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out CertificateSummary
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateCertificateOptions mirrors the POST body the backend accepts.
// type defaults to "private", purpose to "server", targetSecretName
// to "<name>-tls" when left empty.
type CreateCertificateOptions struct {
	Type             string   `json:"type,omitempty"`
	Purpose          string   `json:"purpose,omitempty"`
	DnsNames         []string `json:"dnsNames"`
	Duration         string   `json:"duration,omitempty"`
	RenewBefore      string   `json:"renewBefore,omitempty"`
	TargetSecretName string   `json:"targetSecretName,omitempty"`
}

func (c *Client) CreateCertificate(ctx context.Context, namespace, name string, opts CreateCertificateOptions) (*CertificateSummary, error) {
	if len(opts.DnsNames) == 0 {
		return nil, fmt.Errorf("create: at least one --dns name is required")
	}
	p := "/api/certificates/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out CertificateSummary
	if err := c.do(ctx, "POST", p, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type RenewCertificateResult struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	RenewRequestedAt string `json:"renewRequestedAt"`
}

func (c *Client) RenewCertificate(ctx context.Context, namespace, name string) (*RenewCertificateResult, error) {
	p := "/api/certificates/" + pathEscape(namespace) + "/" + pathEscape(name) + "/renew"
	var out RenewCertificateResult
	if err := c.do(ctx, "POST", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DeleteCertificateResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Deleted   bool   `json:"deleted"`
}

func (c *Client) DeleteCertificate(ctx context.Context, namespace, name string) (*DeleteCertificateResult, error) {
	p := "/api/certificates/" + pathEscape(namespace) + "/" + pathEscape(name)
	var out DeleteCertificateResult
	if err := c.do(ctx, "DELETE", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
