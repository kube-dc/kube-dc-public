package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testDiscoveryCA(t *testing.T, isCA bool) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: isCA, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestDiscoverAPICA(t *testing.T) {
	ca := testDiscoveryCA(t, true)
	const apiServer = "https://kube-api.example.com:6443"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/kube-dc-config" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"apiVersion": "v1", "apiServer": apiServer, "certificateAuthorityData": base64.StdEncoding.EncodeToString([]byte(ca))})
	}))
	defer srv.Close()
	got, err := discoverAPICA(context.Background(), srv.Client(), srv.URL+"/.well-known/kube-dc-config", apiServer)
	if err != nil || got != ca {
		t.Fatalf("discovery failed: %v", err)
	}
	// The public entrypoint uses system roots; metadata's own CA cannot be
	// used to bootstrap trust in an untrusted discovery server.
	if _, err := DiscoverAPICA(context.Background(), srv.URL, apiServer); err == nil {
		t.Fatal("untrusted discovery TLS was accepted")
	}
}

func TestDiscoveryRejectsRedirectsHTTPAndErrors(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			called := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
			defer target.Close()
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer srv.Close()
			_, err := discoverAPICA(context.Background(), srv.Client(), srv.URL, "https://api.example")
			if err == nil || called {
				t.Fatalf("status %d accepted or redirect followed: %v", status, err)
			}
			if _, err := DiscoverAPICA(context.Background(), target.URL, "https://api.example"); err == nil || called {
				t.Fatal("HTTP discovery accepted")
			}
		})
	}
}

func TestDiscoveryRejectsInvalidMetadata(t *testing.T) {
	ca := testDiscoveryCA(t, true)
	for _, tc := range []struct{ name, version, server, ca string }{
		{"wrong version", "v2", "https://api.example", ca},
		{"wrong API", "v1", "https://other.example", ca},
		{"empty CA", "v1", "https://api.example", ""},
		{"private key", "v1", "https://api.example", ca + "-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----"},
		{"non CA certificate", "v1", "https://api.example", testDiscoveryCA(t, false)},
		{"garbage before certificate", "v1", "https://api.example", "garbage\n" + ca},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"apiVersion": tc.version, "apiServer": tc.server, "certificateAuthorityData": base64.StdEncoding.EncodeToString([]byte(tc.ca))})
			if _, err := decodeAPICA(strings.NewReader(string(body)), "https://api.example"); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	for _, body := range []string{`{"apiVersion":"v1","apiServer":"https://api.example","certificateAuthorityData":"%%%"}`, `{}`, `null`, `not json`, strings.Repeat("x", 256*1024+1)} {
		if _, err := decodeAPICA(strings.NewReader(body), "https://api.example"); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}

func TestDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	_, err := discoverAPICA(ctx, srv.Client(), srv.URL, "https://api.example")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestVerifyAPIServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/version" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected verification request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized) // Anonymous access need not be enabled.
	}))
	defer srv.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if err := VerifyAPIServer(context.Background(), srv.URL, ca); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAPIServer(context.Background(), srv.URL, testDiscoveryCA(t, true)); err == nil {
		t.Fatal("wrong CA accepted")
	}
	if err := VerifyAPIServer(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("untrusted API accepted")
	}
	if err := VerifyAPIServer(context.Background(), strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), ca); err == nil {
		t.Fatal("wrong certificate hostname accepted")
	}
	for _, server := range []string{"http://api.example", "https://user:password@api.example", "https://api.example/path"} {
		if err := VerifyAPIServer(context.Background(), server, ca); err == nil {
			t.Fatal("invalid API URL accepted")
		}
	}
}

func TestSystemTrustIsScopedToDiscovery(t *testing.T) {
	// A subprocess gets a fresh system-cert pool. This verifies the public
	// bootstrap trust boundary with local TLS servers and no Internet access.
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux SSL_CERT_FILE trust configuration")
	}
	if server := os.Getenv("KUBEDC_TEST_TRUST_SERVER"); server != "" {
		if err := VerifyAPIServer(context.Background(), server, ""); err != nil {
			t.Fatal(err)
		}
		if err := VerifyAPIServer(context.Background(), server, testDiscoveryCA(t, true)); err == nil {
			t.Fatal("API accepted system trust despite an explicit, unrelated CA")
		}
		if _, err := DiscoverAPICA(context.Background(), server+"/.well-known/kube-dc-config", server); err != nil {
			t.Fatal(err)
		}
		return
	}
	var server string
	var ca string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"apiVersion": "v1", "apiServer": server, "certificateAuthorityData": base64.StdEncoding.EncodeToString([]byte(ca))})
	}))
	defer srv.Close()
	server = srv.URL
	ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	dir := t.TempDir()
	path := filepath.Join(dir, "system-ca.crt")
	if err := os.WriteFile(path, []byte(ca), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemTrustIsScopedToDiscovery$")
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+path, "SSL_CERT_DIR="+dir, "KUBEDC_TEST_TRUST_SERVER="+server)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("trust scope regression: %v\n%s", err, out)
	}
}
