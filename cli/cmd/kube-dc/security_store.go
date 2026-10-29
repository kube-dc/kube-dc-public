package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
)

// Certificate and database-credential CRUD, served like kmsKeys: by the
// Kubernetes API where the cluster serves the resource and the installation
// audits Kubernetes calls on its group, by the backend routes otherwise. Both
// paths answer the backend's summary shapes (certificatesModule.js,
// databaseCredentialsModule.js). Renew, rotate, issue and credential reads
// stay on the backend: they are OpenBao/cert-manager operations.

func field(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func conditions(m map[string]any) []map[string]any {
	out := []map[string]any{}
	if conds, ok := m["conditions"].([]any); ok {
		for _, c := range conds {
			if cm, ok := c.(map[string]any); ok {
				out = append(out, cm)
			}
		}
	}
	return out
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// dns1123Subdomain is the backend's name check for both kinds.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// ---- certificates -------------------------------------------------------

type certificates interface {
	List(ctx context.Context, namespace string) (*backend.CertificateList, error)
	Get(ctx context.Context, namespace, name string) (*backend.CertificateSummary, error)
	Create(ctx context.Context, namespace, name string, opts backend.CreateCertificateOptions) (*backend.CertificateSummary, error)
	Delete(ctx context.Context, namespace, name string) (*backend.DeleteCertificateResult, error)
}

func (s *secretsScope) certificates(ctx context.Context) (certificates, error) {
	if s.native(ctx, k8sapi.SecurityGroupVersion, k8sapi.ManagedCertificates.Plural) {
		k8s, err := s.k8s()
		if err != nil {
			return nil, err
		}
		return nativeCertificates{k8s}, nil
	}
	bc, err := s.backend()
	if err != nil {
		return nil, err
	}
	return legacyCertificates{bc}, nil
}

type legacyCertificates struct{ c *backend.Client }

func (l legacyCertificates) List(ctx context.Context, ns string) (*backend.CertificateList, error) {
	return l.c.ListCertificates(ctx, ns)
}
func (l legacyCertificates) Get(ctx context.Context, ns, name string) (*backend.CertificateSummary, error) {
	return l.c.GetCertificate(ctx, ns, name)
}
func (l legacyCertificates) Create(ctx context.Context, ns, name string, o backend.CreateCertificateOptions) (*backend.CertificateSummary, error) {
	return l.c.CreateCertificate(ctx, ns, name, o)
}
func (l legacyCertificates) Delete(ctx context.Context, ns, name string) (*backend.DeleteCertificateResult, error) {
	return l.c.DeleteCertificate(ctx, ns, name)
}

type nativeCertificates struct{ c *k8sapi.Client }

func (n nativeCertificates) List(ctx context.Context, ns string) (*backend.CertificateList, error) {
	list, err := n.c.List(ctx, k8sapi.ManagedCertificates, ns)
	if err != nil {
		return nil, err
	}
	out := &backend.CertificateList{Items: make([]backend.CertificateSummary, 0, len(list.Items))}
	for i := range list.Items {
		out.Items = append(out.Items, summariseCertificate(&list.Items[i]))
	}
	return out, nil
}

func (n nativeCertificates) Get(ctx context.Context, ns, name string) (*backend.CertificateSummary, error) {
	o, err := n.c.Get(ctx, k8sapi.ManagedCertificates, ns, name)
	if err != nil {
		return nil, err
	}
	s := summariseCertificate(o)
	return &s, nil
}

func (n nativeCertificates) Create(ctx context.Context, ns, name string, o backend.CreateCertificateOptions) (*backend.CertificateSummary, error) {
	spec, err := certificateSpec(name, o)
	if err != nil {
		return nil, err
	}
	created, err := n.c.Create(ctx, k8sapi.ManagedCertificates, ns, name, spec)
	if err != nil {
		return nil, err
	}
	s := summariseCertificate(created)
	return &s, nil
}

func (n nativeCertificates) Delete(ctx context.Context, ns, name string) (*backend.DeleteCertificateResult, error) {
	if err := n.c.Delete(ctx, k8sapi.ManagedCertificates, ns, name); err != nil {
		return nil, err
	}
	return &backend.DeleteCertificateResult{Name: name, Namespace: ns, Deleted: true}, nil
}

// certificateSpec applies the backend's checks and defaults (certificatesModule.js create).
func certificateSpec(name string, o backend.CreateCertificateOptions) (map[string]any, error) {
	if !dns1123Subdomain.MatchString(name) || len(name) > 253 {
		return nil, fmt.Errorf("name must be a DNS-1123 subdomain")
	}
	typ := field(map[string]any{"t": o.Type}, "t", "private")
	if typ != "private" && typ != "public" {
		return nil, fmt.Errorf("type must be one of private, public")
	}
	purpose := field(map[string]any{"p": o.Purpose}, "p", "server")
	switch purpose {
	case "server", "client", "mtls", "code-signing":
	default:
		return nil, fmt.Errorf("purpose must be one of server, client, mtls, code-signing")
	}
	dns := []any{}
	for _, d := range o.DnsNames {
		if d != "" {
			dns = append(dns, d)
		}
	}
	if len(dns) == 0 {
		return nil, fmt.Errorf("dnsNames must contain at least one entry")
	}
	spec := map[string]any{"type": typ, "purpose": purpose, "dnsNames": dns,
		"targetSecretName": field(map[string]any{"s": o.TargetSecretName}, "s", name+"-tls")}
	if o.Duration != "" {
		spec["duration"] = o.Duration
	}
	if o.RenewBefore != "" {
		spec["renewBefore"] = o.RenewBefore
	}
	return spec, nil
}

// summariseCertificate is the backend's summariseManagedCertificate.
func summariseCertificate(o *k8sapi.Object) backend.CertificateSummary {
	spec, status := orEmpty(o.Spec), orEmpty(o.Status)
	dns := []string{}
	if list, ok := spec["dnsNames"].([]any); ok {
		for _, d := range list {
			if s, ok := d.(string); ok {
				dns = append(dns, s)
			}
		}
	}
	return backend.CertificateSummary{
		Name: o.Metadata.Name, Namespace: o.Metadata.Namespace, CreationTimestamp: o.Metadata.CreationTimestamp,
		Type: field(spec, "type", "private"), Purpose: field(spec, "purpose", "server"), DnsNames: dns,
		Duration: field(spec, "duration", ""), RenewBefore: field(spec, "renewBefore", ""), TargetSecretName: field(spec, "targetSecretName", ""),
		Status: backend.CertificateStatus{
			Issuer: field(status, "issuer", ""), CertificateSecretName: field(status, "certificateSecretName", ""),
			NotBefore: field(status, "notBefore", ""), NotAfter: field(status, "notAfter", ""), RenewalTime: field(status, "renewalTime", ""),
			Conditions: conditions(status),
		},
	}
}

// ---- database credential policies ----------------------------------------

type credentialPolicies interface {
	List(ctx context.Context, namespace string) (*backend.DBCredentialPolicyList, error)
	Get(ctx context.Context, namespace, name string) (*backend.DBCredentialPolicySummary, error)
	Create(ctx context.Context, namespace, name string, opts backend.CreateDBCredentialPolicyOptions) error
	Delete(ctx context.Context, namespace, name string) error
}

func (s *secretsScope) credentialPolicies(ctx context.Context) (credentialPolicies, error) {
	if s.native(ctx, k8sapi.SecurityGroupVersion, k8sapi.DatabaseCredentialPolicies.Plural) {
		k8s, err := s.k8s()
		if err != nil {
			return nil, err
		}
		return nativeCredentialPolicies{k8s}, nil
	}
	bc, err := s.backend()
	if err != nil {
		return nil, err
	}
	return legacyCredentialPolicies{bc}, nil
}

type legacyCredentialPolicies struct{ c *backend.Client }

func (l legacyCredentialPolicies) List(ctx context.Context, ns string) (*backend.DBCredentialPolicyList, error) {
	return l.c.ListDBCredentialPolicies(ctx, ns)
}
func (l legacyCredentialPolicies) Get(ctx context.Context, ns, name string) (*backend.DBCredentialPolicySummary, error) {
	return l.c.GetDBCredentialPolicy(ctx, ns, name)
}
func (l legacyCredentialPolicies) Create(ctx context.Context, ns, name string, o backend.CreateDBCredentialPolicyOptions) error {
	return l.c.CreateDBCredentialPolicy(ctx, ns, name, o)
}
func (l legacyCredentialPolicies) Delete(ctx context.Context, ns, name string) error {
	return l.c.DeleteDBCredentialPolicy(ctx, ns, name)
}

type nativeCredentialPolicies struct{ c *k8sapi.Client }

func (n nativeCredentialPolicies) List(ctx context.Context, ns string) (*backend.DBCredentialPolicyList, error) {
	list, err := n.c.List(ctx, k8sapi.DatabaseCredentialPolicies, ns)
	if err != nil {
		return nil, err
	}
	out := &backend.DBCredentialPolicyList{Items: make([]backend.DBCredentialPolicySummary, 0, len(list.Items))}
	for i := range list.Items {
		out.Items = append(out.Items, summariseCredentialPolicy(&list.Items[i]))
	}
	return out, nil
}

func (n nativeCredentialPolicies) Get(ctx context.Context, ns, name string) (*backend.DBCredentialPolicySummary, error) {
	o, err := n.c.Get(ctx, k8sapi.DatabaseCredentialPolicies, ns, name)
	if err != nil {
		return nil, err
	}
	s := summariseCredentialPolicy(o)
	return &s, nil
}

func (n nativeCredentialPolicies) Create(ctx context.Context, ns, name string, o backend.CreateDBCredentialPolicyOptions) error {
	spec, err := credentialPolicySpec(name, o)
	if err != nil {
		return err
	}
	_, err = n.c.Create(ctx, k8sapi.DatabaseCredentialPolicies, ns, name, spec)
	return err
}

func (n nativeCredentialPolicies) Delete(ctx context.Context, ns, name string) error {
	return n.c.Delete(ctx, k8sapi.DatabaseCredentialPolicies, ns, name)
}

// credentialPolicySpec applies the backend's validateCreatePayload and sends
// the spec as given, as the backend route does.
func credentialPolicySpec(name string, o backend.CreateDBCredentialPolicyOptions) (map[string]any, error) {
	if !dns1123Subdomain.MatchString(name) {
		return nil, fmt.Errorf("name must be a DNS-1123 subdomain")
	}
	s := o.Spec
	if s.DatabaseRef.Name == "" {
		return nil, fmt.Errorf("spec.databaseRef.name is required")
	}
	if s.Mode != "static-rotated" && s.Mode != "dynamic" {
		return nil, fmt.Errorf("spec.mode must be one of static-rotated | dynamic")
	}
	if st := s.Rotation.Strategy; st != "" && st != "rolling" && st != "immediate" {
		return nil, fmt.Errorf("spec.rotation.strategy must be one of rolling | immediate")
	}
	return toMap(s)
}

// summariseCredentialPolicy is the backend's summariseDBCP.
func summariseCredentialPolicy(o *k8sapi.Object) backend.DBCredentialPolicySummary {
	spec, status := orEmpty(o.Spec), orEmpty(o.Status)
	out := backend.DBCredentialPolicySummary{
		Name: o.Metadata.Name, Namespace: o.Metadata.Namespace, CreationTimestamp: o.Metadata.CreationTimestamp,
		Mode: field(spec, "mode", "static-rotated"), Username: field(spec, "username", "app"),
		Role: field(spec, "role", ""), TTL: field(spec, "ttl", ""), MaxTTL: field(spec, "maxTtl", ""),
		Status: backend.DBCredentialPolicyStatus{
			Endpoint: field(status, "endpoint", ""), Username: field(status, "username", ""),
			TargetSecretName: field(status, "targetSecretName", ""), LastRotatedTime: field(status, "lastRotatedTime", ""),
			NextRotationTime: field(status, "nextRotationTime", ""), LeaseSupported: status["leaseSupported"] == true,
			Conditions: conditions(status),
		},
	}
	if ref, ok := spec["databaseRef"].(map[string]any); ok {
		out.DatabaseRef = field(ref, "name", "")
	}
	if r, ok := spec["rotation"].(map[string]any); ok {
		out.Rotation = backend.DBCredentialRotation{Interval: field(r, "interval", ""), Strategy: field(r, "strategy", "")}
	}
	if sy, ok := spec["sync"].(map[string]any); ok {
		if e, ok := sy["enabled"].(bool); ok {
			out.Sync.Enabled = &e
		}
		out.Sync.TargetSecretName = field(sy, "targetSecretName", "")
	}
	return out
}

// toMap turns a typed spec into the JSON object the API server receives,
// with the same field names and omissions as the backend route's body.
func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}
