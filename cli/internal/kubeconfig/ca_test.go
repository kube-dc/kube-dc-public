package kubeconfig

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoginPreservesExistingCAForSameServer(t *testing.T) {
	const server = "https://kube-api.example.com:6443"
	const oldCA = "existing-ca"
	for _, tc := range []struct {
		name     string
		previous Cluster
		ca       string
		insecure bool
		systemCA bool
		wantData string
		wantFile string
	}{
		{
			name:     "retain embedded CA",
			previous: Cluster{Server: server, CertificateAuthorityData: oldCA},
			wantData: oldCA,
		},
		{
			name:     "retain CA file",
			previous: Cluster{Server: server, CertificateAuthority: "certs/api-ca.crt"},
			wantFile: "certs/api-ca.crt",
		},
		{
			name:     "explicit replacement clears old file",
			previous: Cluster{Server: server, CertificateAuthority: "old.crt", CertificateAuthorityData: oldCA},
			ca:       "replacement-ca", wantData: base64.StdEncoding.EncodeToString([]byte("replacement-ca")),
		},
		{
			name:     "explicit system trust clears both CA forms",
			previous: Cluster{Server: server, CertificateAuthority: "old.crt", CertificateAuthorityData: oldCA},
			systemCA: true,
		},
		{
			name:     "explicit insecure clears both CA forms",
			previous: Cluster{Server: server, CertificateAuthority: "old.crt", CertificateAuthorityData: oldCA},
			insecure: true,
		},
		{
			name:     "different server cannot inherit trust",
			previous: Cluster{Server: "https://old.example.com:6443", CertificateAuthorityData: oldCA, CertificateAuthority: "old.crt"},
		},
		{
			name:     "previous insecure is not implicit consent",
			previous: Cluster{Server: server, InsecureSkipTLSVerify: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, path := newTestManager(t)
			writeConfig(t, path, &Config{
				APIVersion: "v1", Kind: "Config",
				Clusters: []NamedCluster{{Name: "tenant", Cluster: tc.previous}},
			})
			// Multiple Projects share the tenant cluster entry, so verify a
			// second update preserves the result of the first as login does.
			for _, project := range []string{"docs", "demo"} {
				if err := mgr.AddKubeDCContext(AddContextParams{
					Server: server, ClusterName: "tenant", UserName: "user", ContextName: project,
					CACert: tc.ca, Insecure: tc.insecure, UseSystemCA: tc.systemCA,
				}); err != nil {
					t.Fatal(err)
				}
				cfg, err := mgr.Load()
				if err != nil {
					t.Fatal(err)
				}
				got := cfg.Clusters[0].Cluster
				if got.CertificateAuthorityData != tc.wantData || got.CertificateAuthority != tc.wantFile || got.InsecureSkipTLSVerify != tc.insecure {
					t.Fatalf("after %s: got %+v, want data=%q file=%q insecure=%v", project, got, tc.wantData, tc.wantFile, tc.insecure)
				}
			}
		})
	}
}

func TestLoginPreservesOtherKubeconfigFields(t *testing.T) {
	mgr, path := newTestManager(t)
	before := `apiVersion: v1
kind: Config
extensions:
  - name: custom
    extension: {value: keep}
clusters:
  - name: external
    cluster:
      server: https://external.example
      certificate-authority: ca.crt
      proxy-url: socks5://localhost:1080
      tls-server-name: api.internal
      disable-compression: true
users:
  - name: client-cert
    user:
      client-certificate-data: dGVzdC1jZXJ0
      client-key-data: dGVzdC1rZXk=
  - name: external-exec
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: external
        interactiveMode: Never
        env: [{name: PROFILE, value: test}]
        provideClusterInfo: true
        installHint: install the plugin
  - name: legacy
    user:
      token: test-only
      auth-provider:
        name: oidc
        config: {id-token: test-only}
contexts:
  - name: external
    context:
      cluster: external
      user: client-cert
      extensions: [{name: custom, extension: {value: keep}}]
current-context: external
`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKubeDCContext(AddContextParams{Server: "https://api.example", ClusterName: "kube-dc", UserName: "kube-dc", ContextName: "kube-dc"}); err != nil {
		t.Fatal(err)
	}
	var original, updated map[string]any
	if err := yaml.Unmarshal([]byte(before), &original); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &updated); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clusters", "users", "contexts"} {
		old := original[key].([]any)
		newEntries := updated[key].([]any)
		if !reflect.DeepEqual(old, newEntries[:len(old)]) {
			t.Errorf("existing %s changed", key)
		}
	}
	if !reflect.DeepEqual(original["extensions"], updated["extensions"]) {
		t.Fatal("top-level extensions lost")
	}
}

func TestClusterCALookup(t *testing.T) {
	mgr, path := newTestManager(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "api-ca.crt"), []byte("test-ca"), 0600); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, &Config{Clusters: []NamedCluster{{Name: "cluster", Cluster: Cluster{Server: "https://api.example", CertificateAuthority: "api-ca.crt"}}}})
	got, err := mgr.ClusterCA("cluster", "https://api.example")
	if err != nil || got != "test-ca" {
		t.Fatalf("relative CA lookup: ca=%q err=%v", got, err)
	}
	got, err = mgr.ClusterCA("cluster", "https://other.example")
	if err != nil || got != "" {
		t.Fatal("CA reused for a different endpoint")
	}
}

func TestManagerUsesFirstNonEmptyKubeconfigPath(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	t.Setenv("KUBECONFIG", string(os.PathListSeparator)+first+string(os.PathListSeparator)+second)
	mgr, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Path() != first {
		t.Fatalf("path=%q, want %q", mgr.Path(), first)
	}
}
