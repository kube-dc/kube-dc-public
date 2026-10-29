package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
	"github.com/shalb/kube-dc/cli/internal/platform"
)

func object(t *testing.T, raw string) *k8sapi.Object {
	t.Helper()
	var o k8sapi.Object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	return &o
}

// The native summaries equal what the backend answers for the same object
// (summariseManagedCertificate, summariseDBCP).
func TestNativeCertificateSummaryMatchesTheBackendShape(t *testing.T) {
	got := summariseCertificate(object(t, `{"metadata":{"name":"api","namespace":"acme-web","creationTimestamp":"2026-10-24T10:00:00Z"},
	  "spec":{"type":"public","purpose":"mtls","dnsNames":["api.acme.test"],"duration":"2160h","targetSecretName":"api-tls"},
	  "status":{"issuer":"letsencrypt","certificateSecretName":"api-tls","notAfter":"2027-01-01T00:00:00Z","conditions":[{"type":"Ready","status":"True"}]}}`))
	var want backend.CertificateSummary
	_ = json.Unmarshal([]byte(`{"name":"api","namespace":"acme-web","creationTimestamp":"2026-10-24T10:00:00Z","type":"public","purpose":"mtls",
	  "dnsNames":["api.acme.test"],"duration":"2160h","targetSecretName":"api-tls",
	  "status":{"issuer":"letsencrypt","certificateSecretName":"api-tls","notAfter":"2027-01-01T00:00:00Z","conditions":[{"type":"Ready","status":"True"}]}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native %+v\nbackend %+v", got, want)
	}
	bare := summariseCertificate(object(t, `{"metadata":{"name":"b"}}`))
	if bare.Type != "private" || bare.Purpose != "server" || bare.DnsNames == nil || bare.Status.Conditions == nil {
		t.Fatalf("defaults %+v", bare)
	}
}

func TestNativeCredentialPolicySummaryMatchesTheBackendShape(t *testing.T) {
	got := summariseCredentialPolicy(object(t, `{"metadata":{"name":"app","namespace":"acme-web"},
	  "spec":{"databaseRef":{"name":"pg"},"mode":"dynamic","role":"ro","ttl":"1h","rotation":{"interval":"24h","strategy":"rolling"},"sync":{"enabled":false,"targetSecretName":"app-creds"}},
	  "status":{"endpoint":"pg.acme-web.svc:5432","leaseSupported":true,"conditions":[{"type":"Ready","status":"True"}]}}`))
	var want backend.DBCredentialPolicySummary
	_ = json.Unmarshal([]byte(`{"name":"app","namespace":"acme-web","databaseRef":"pg","mode":"dynamic","username":"app","role":"ro","ttl":"1h",
	  "rotation":{"interval":"24h","strategy":"rolling"},"sync":{"enabled":false,"targetSecretName":"app-creds"},
	  "status":{"endpoint":"pg.acme-web.svc:5432","leaseSupported":true,"conditions":[{"type":"Ready","status":"True"}]}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native %+v\nbackend %+v", got, want)
	}
}

func TestNativeCreateKeepsTheBackendChecks(t *testing.T) {
	spec, err := certificateSpec("api", backend.CreateCertificateOptions{DnsNames: []string{"", "api.acme.test"}})
	if err != nil || spec["type"] != "private" || spec["purpose"] != "server" || spec["targetSecretName"] != "api-tls" || len(spec["dnsNames"].([]any)) != 1 {
		t.Fatalf("defaults %v %v", spec, err)
	}
	for _, c := range []struct {
		name string
		o    backend.CreateCertificateOptions
		want string
	}{
		{"Bad_Name", backend.CreateCertificateOptions{DnsNames: []string{"a"}}, "DNS-1123"},
		{"api", backend.CreateCertificateOptions{}, "dnsNames"},
		{"api", backend.CreateCertificateOptions{Type: "self", DnsNames: []string{"a"}}, "type"},
		{"api", backend.CreateCertificateOptions{Purpose: "email", DnsNames: []string{"a"}}, "purpose"},
	} {
		if _, err := certificateSpec(c.name, c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	ok := backend.CreateDBCredentialPolicyOptions{Spec: backend.CreateDBCredentialPolicySpec{DatabaseRef: backend.DatabaseRef{Name: "pg"}, Mode: "static-rotated"}}
	if spec, err := credentialPolicySpec("app", ok); err != nil || spec["mode"] != "static-rotated" || spec["databaseRef"].(map[string]any)["name"] != "pg" {
		t.Fatalf("%v %v", spec, err)
	}
	for _, o := range []backend.CreateDBCredentialPolicyOptions{
		{Spec: backend.CreateDBCredentialPolicySpec{Mode: "dynamic"}},
		{Spec: backend.CreateDBCredentialPolicySpec{DatabaseRef: backend.DatabaseRef{Name: "pg"}, Mode: "forever"}},
		{Spec: backend.CreateDBCredentialPolicySpec{DatabaseRef: backend.DatabaseRef{Name: "pg"}, Mode: "dynamic", Rotation: backend.DBCredentialRotation{Strategy: "never"}}},
	} {
		if _, err := credentialPolicySpec("app", o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

// Like KMSKeys: native only where the cluster serves the resource and the
// installation audits its group.
func TestCertificatesAndCredentialPoliciesUseTheNewestAuditedAPI(t *testing.T) {
	audited := &platform.Contract{}
	audited.Audit.KubernetesGroups = []string{"security.kube-dc.com"}
	for _, c := range []struct {
		contract *platform.Contract
		native   bool
	}{{audited, true}, {&platform.Contract{}, false}, {nil, false}} {
		t.Setenv("HOME", t.TempDir())
		api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"resources":[{"name":"managedcertificates"},{"name":"databasecredentialpolicies"}]}`))
		}))
		scope := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true, Contract: c.contract}
		certs, err1 := scope.certificates(context.Background())
		creds, err2 := scope.credentialPolicies(context.Background())
		api.Close()
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		_, nc := certs.(nativeCertificates)
		_, np := creds.(nativeCredentialPolicies)
		if nc != c.native || np != c.native {
			t.Errorf("contract %+v: certificates %T, credential policies %T", c.contract, certs, creds)
		}
	}
}
