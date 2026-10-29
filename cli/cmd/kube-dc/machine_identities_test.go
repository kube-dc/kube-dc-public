package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMachineIdentityTokenIsAnExecCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "machine-ci" || r.Form.Get("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error_description":"Invalid client credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":60}`))
	}))
	defer srv.Close()
	out, err := midToken(context.Background(), srv.URL, "machine-ci", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	var cred struct {
		Kind   string
		Status struct{ Token, ExpirationTimestamp string }
	}
	if err := json.Unmarshal(out, &cred); err != nil || cred.Kind != "ExecCredential" || cred.Status.Token != "tok" || cred.Status.ExpirationTimestamp == "" {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := midToken(context.Background(), srv.URL, "machine-ci", "old"); err == nil || !strings.Contains(err.Error(), "Invalid client credentials") {
		t.Fatalf("a refused secret must fail with the reason: %v", err)
	}
}

func TestMachineIdentityKubeconfigUsesTheTokenPlugin(t *testing.T) {
	data, err := midKubeconfig("https://api:6443", "PEM", "acme", "ci", map[string]string{"token-url": "https://kc/t", "client-id": "machine-ci", "client-secret": "s"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"machine-identities"`, `"token"`, `"KUBE_DC_CLIENT_SECRET"`, `"https://api:6443"`, `"namespace": "acme"`, `"interactiveMode": "Never"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("kubeconfig lacks %s:\n%s", want, s)
		}
	}
}
