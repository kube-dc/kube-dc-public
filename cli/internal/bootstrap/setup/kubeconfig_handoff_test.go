package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func childConfig(t *testing.T, c Compiled) *clientcmdapi.Config {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	client := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "admin"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, err := x509.CreateCertificate(rand.Reader, client, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	name := c.Spec.Name
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = name
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.Clusters[name] = &clientcmdapi.Cluster{
		Server: "https://kube-api." + c.Init.Domain + ":6443", CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
	}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{ClientCertificateData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		ClientKeyData: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})}
	return cfg
}

func TestExclusiveKubeconfigHandoffWritesPrivateReviewedConfig(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	path := filepath.Join(t.TempDir(), "guided", "child.yaml")
	handoff, err := NewExclusiveKubeconfigHandoff(c, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff(childConfig(t, c)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("child kubeconfig is not private: %v, %v", info, err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("child kubeconfig directory is not private: %v, %v", dir, err)
	}
	loaded, err := clientcmd.LoadFromFile(path)
	if err != nil || loaded.CurrentContext != c.Spec.Name || loaded.Clusters[c.Spec.Name].Server != "https://kube-api.example.test:6443" {
		t.Fatalf("child kubeconfig changed during handoff: %v, %v", loaded, err)
	}
	if err := handoff(childConfig(t, c)); err == nil {
		t.Fatal("handoff replaced an existing child kubeconfig")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "child.yaml" {
		t.Fatalf("temporary secret file remained: %v, %v", entries, err)
	}
}

func TestExclusiveKubeconfigHandoffRejectsExistingAndUnsafePaths(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	root := t.TempDir()
	path := filepath.Join(root, "existing.yaml")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff, err := NewExclusiveKubeconfigHandoff(c, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff(childConfig(t, c)); err == nil {
		t.Fatal("existing kubeconfig was accepted")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "keep" {
		t.Fatalf("existing kubeconfig was changed: %q, %v", body, err)
	}
	for _, bad := range []string{"relative.yaml", filepath.Join(root, "new", "..", "child.yaml") + "/"} {
		if _, err := NewExclusiveKubeconfigHandoff(c, bad); err == nil {
			t.Fatalf("unsafe path %q was accepted", bad)
		}
	}
	wide := filepath.Join(root, "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff, err = NewExclusiveKubeconfigHandoff(c, filepath.Join(wide, "child.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff(childConfig(t, c)); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("group-readable directory was accepted: %v", err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(wide, linked); err != nil {
		t.Fatal(err)
	}
	handoff, err = NewExclusiveKubeconfigHandoff(c, filepath.Join(linked, "child.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff(childConfig(t, c)); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("linked directory was accepted: %v", err)
	}
}

func TestExclusiveKubeconfigHandoffRejectsWrongClusterAndExternalSecrets(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	path := filepath.Join(t.TempDir(), "guided", "child.yaml")
	handoff, err := NewExclusiveKubeconfigHandoff(c, path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*clientcmdapi.Config)
	}{
		{"wrong server", func(cfg *clientcmdapi.Config) { cfg.Clusters[c.Spec.Name].Server = "https://other:6443" }},
		{"skip TLS", func(cfg *clientcmdapi.Config) { cfg.Clusters[c.Spec.Name].InsecureSkipTLSVerify = true }},
		{"external CA", func(cfg *clientcmdapi.Config) {
			cfg.Clusters[c.Spec.Name].CertificateAuthorityData = nil
			cfg.Clusters[c.Spec.Name].CertificateAuthority = "/tmp/ca"
		}},
		{"malformed CA", func(cfg *clientcmdapi.Config) { cfg.Clusters[c.Spec.Name].CertificateAuthorityData = []byte("not PEM") }},
		{"external key", func(cfg *clientcmdapi.Config) { cfg.AuthInfos[c.Spec.Name].ClientKey = "/tmp/key" }},
		{"malformed client cert", func(cfg *clientcmdapi.Config) { cfg.AuthInfos[c.Spec.Name].ClientCertificateData = []byte("not PEM") }},
		{"mismatched client key", func(cfg *clientcmdapi.Config) {
			cfg.AuthInfos[c.Spec.Name].ClientKeyData = childConfig(t, c).AuthInfos[c.Spec.Name].ClientKeyData
		}},
		{"foreign context", func(cfg *clientcmdapi.Config) { cfg.Contexts["other"] = &clientcmdapi.Context{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := childConfig(t, c)
			tc.edit(cfg)
			if err := handoff(cfg); err == nil {
				t.Fatal("unsafe child kubeconfig was accepted")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("invalid config created a kubeconfig directory: %v", err)
			}
		})
	}
}
