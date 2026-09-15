package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

func TestLoginKeepsCAFileAndTLSName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "config"))
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer api.Close()
	server := strings.Replace(api.URL, "127.0.0.1", "localhost", 1)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	path := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"tenant", "admin"} {
		t.Run(identity, func(t *testing.T) {
			if err := mgr.Save(&kubeconfig.Config{APIVersion: "v1", Kind: "Config", Clusters: []kubeconfig.NamedCluster{{Name: identity, Cluster: kubeconfig.Cluster{Server: server, CertificateAuthority: "ca.crt", TLSServerName: "example.com"}}}}); err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveLoginAPICA(context.Background(), "unused.invalid", identity, server, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.AddKubeDCContext(kubeconfig.AddContextParams{Server: server, ClusterName: identity, UserName: identity, ContextName: identity, CACert: resolved.CACert, UseSystemCA: resolved.UseSystemCA}); err != nil {
				t.Fatal(err)
			}
			cfg, err := mgr.Load()
			if err != nil {
				t.Fatal(err)
			}
			cluster := cfg.Clusters[0].Cluster
			if cluster.CertificateAuthority != "ca.crt" || cluster.CertificateAuthorityData != "" || cluster.TLSServerName != "example.com" || cluster.InsecureSkipTLSVerify {
				t.Fatalf("existing connection changed: %+v", cluster)
			}
			// A later file rotation must remain visible through the same reference.
			rotated := append(ca, []byte("\n# rotated bundle\n")...)
			if err := os.WriteFile(path, rotated, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := mgr.ClusterCA(identity, server)
			if err != nil || got != string(rotated) {
				t.Fatalf("CA file rotation lost: %v", err)
			}
		})
	}
}

func TestLoginPreservesConnectionErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := "https://" + listener.Addr().String()
	listener.Close()
	_, err = resolveAPICA(context.Background(), server, "", "", auth.APIConnection{}, func(context.Context) (string, error) {
		t.Fatal("CA discovery cannot repair an unreachable API")
		return "", nil
	})
	if err == nil || strings.Contains(err.Error(), "--ca-cert") {
		t.Fatalf("misleading error: %v", err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("original connection error lost: %v", err)
	}
}

func TestReadLoginCANormalizesAndRejectsKeys(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer api.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, []byte("subject=Operator CA\n"+ca), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readLoginCA(path)
	if err != nil || got != ca {
		t.Fatalf("operator CA was not canonicalized: %v", err)
	}
	if err := os.WriteFile(path, []byte(ca+"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLoginCA(path); err == nil {
		t.Fatal("private key accepted")
	}
}

func TestLoginAndBootstrapMigrateToSystemCA(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux SSL_CERT_FILE trust configuration")
	}
	if server := os.Getenv("KUBEDC_TEST_MIGRATION_API"); server != "" {
		dir := t.TempDir()
		t.Setenv("KUBECONFIG", filepath.Join(dir, "config"))
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		oldCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		mgr, err := kubeconfig.NewManager()
		if err != nil {
			t.Fatal(err)
		}
		params := kubeconfig.AddContextParams{Server: server, ClusterName: "kube-dc-example-admin", ContextName: "admin", UserName: "admin", CACert: oldCA}
		if err := mgr.AddKubeDCContext(params); err != nil {
			t.Fatal(err)
		}
		resolved, err := resolveLoginAPICA(context.Background(), "unused.invalid", params.ClusterName, server, "", false)
		if err != nil || !resolved.UseSystemCA || resolved.CACert != "" {
			t.Fatalf("login did not select system trust: %+v %v", resolved, err)
		}
		updated := params
		updated.CACert, updated.UseSystemCA = resolved.CACert, resolved.UseSystemCA
		if err := mgr.AddKubeDCContext(updated); err != nil {
			t.Fatal(err)
		}
		assertSystemCA := func() {
			t.Helper()
			cluster, err := mgr.ClusterConnection(params.ClusterName, server)
			if err != nil || cluster.Server != server || cluster.CertificateAuthorityData != "" || cluster.CertificateAuthority != "" || cluster.InsecureSkipTLSVerify {
				t.Fatalf("stale trust retained: %+v %v", cluster, err)
			}
			if err := auth.VerifyAPIServer(context.Background(), server, ""); err != nil {
				t.Fatal(err)
			}
		}
		assertSystemCA()
		// Exercise the actual bootstrap command, including FetchCA's system-root result.
		if err := mgr.AddKubeDCContext(params); err != nil {
			t.Fatal(err)
		}
		clusterDir := filepath.Join(dir, "fleet", "clusters", "example")
		if err := os.MkdirAll(clusterDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte("KUBE_API_EXTERNAL_URL="+server+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runBootstrapKubeconfig(context.Background(), runKubeconfigOpts{RepoRoot: filepath.Join(dir, "fleet"), ClusterName: "example", Realm: "master"}); err != nil {
			t.Fatal(err)
		}
		assertSystemCA()
		return
	}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer api.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "system-ca.crt")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	if err := os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoginAndBootstrapMigrateToSystemCA$")
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+path, "SSL_CERT_DIR="+dir, "KUBEDC_TEST_MIGRATION_API="+api.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("system CA migration failed: %v\n%s", err, output)
	}
}

func TestBootstrapKeepsTrustWhenCAProbeFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "config"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := "https://" + listener.Addr().String()
	listener.Close()
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	previous := kubeconfig.Cluster{Server: server, CertificateAuthority: "ca.crt", CertificateAuthorityData: "b2xkLXRydXN0"}
	if err := mgr.Save(&kubeconfig.Config{APIVersion: "v1", Kind: "Config", Clusters: []kubeconfig.NamedCluster{{Name: "kube-dc-example-admin", Cluster: previous}}}); err != nil {
		t.Fatal(err)
	}
	clusterDir := filepath.Join(dir, "fleet", "clusters", "example")
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte("KUBE_API_EXTERNAL_URL="+server+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runBootstrapKubeconfig(context.Background(), runKubeconfigOpts{RepoRoot: filepath.Join(dir, "fleet"), ClusterName: "example", Realm: "master"}); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.ClusterConnection("kube-dc-example-admin", server)
	if err != nil || got.CertificateAuthority != previous.CertificateAuthority || got.CertificateAuthorityData != previous.CertificateAuthorityData || got.InsecureSkipTLSVerify {
		t.Fatalf("failed probe changed trust: %+v %v", got, err)
	}
}

func TestLoginDoesNotDiscoverCAForUntrustedHTTPSProxy(t *testing.T) {
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted proxy was used") }))
	defer proxy.Close()
	_, err := resolveAPICA(context.Background(), "https://127.0.0.1:1", "", "", auth.APIConnection{ProxyURL: proxy.URL}, func(context.Context) (string, error) {
		t.Fatal("API CA discovery cannot repair untrusted proxy TLS")
		return "", nil
	})
	var proxyError *net.OpError
	if !errors.As(err, &proxyError) || proxyError.Op != "proxyconnect" || strings.Contains(err.Error(), "--ca-cert") {
		t.Fatalf("proxy failure was misclassified: %v", err)
	}
}
