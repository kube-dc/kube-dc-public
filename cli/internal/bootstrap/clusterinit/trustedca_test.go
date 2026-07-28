package clusterinit

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func testCAPEM(t *testing.T, isCA bool) ([]byte, []byte) {
	t.Helper()
	usage := x509.KeyUsageDigitalSignature
	if isCA {
		usage |= x509.KeyUsageCertSign
	}
	return testCertificatePEM(t, isCA, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), usage)
}

func testCertificatePEM(t *testing.T, isCA bool, notBefore, notAfter time.Time, usage x509.KeyUsage) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Installer Test CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return cert, priv
}

func TestLoadTrustedCABundleValidatesCertificateOnlyMaterial(t *testing.T) {
	cert, key := testCAPEM(t, true)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTrustedCABundle(path)
	if err != nil {
		t.Fatalf("LoadTrustedCABundle: %v", err)
	}
	if got.CertCount != 1 || len(got.Fingerprint) != 64 || !strings.Contains(string(got.PEM), "BEGIN CERTIFICATE") {
		t.Fatalf("unexpected material: %+v", got)
	}

	if err := os.WriteFile(path, append(cert, key...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedCABundle(path); err == nil || !strings.Contains(err.Error(), "forbidden PEM block") {
		t.Fatalf("private key must be rejected, got %v", err)
	}
	leaf, _ := testCAPEM(t, false)

	expired, _ := testCertificatePEM(t, true, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour), x509.KeyUsageCertSign)
	if err := os.WriteFile(path, expired, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedCABundle(path); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("expired CA must be rejected, got %v", err)
	}

	nonSigning, _ := testCertificatePEM(t, true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.KeyUsageDigitalSignature)
	if err := os.WriteFile(path, nonSigning, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedCABundle(path); err == nil || !strings.Contains(err.Error(), "cannot sign certificates") {
		t.Fatalf("non-signing CA must be rejected, got %v", err)
	}
	if err := os.WriteFile(path, leaf, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedCABundle(path); err == nil || !strings.Contains(err.Error(), "is not a CA") {
		t.Fatalf("leaf certificate must be rejected, got %v", err)
	}
}

func TestWriteTrustedCAWiresAllChartTrustConsumers(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	cert, _ := testCAPEM(t, true)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	material, err := LoadTrustedCABundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTrustedCA(repo, "atlantis", material, nil); err != nil {
		t.Fatalf("WriteTrustedCA: %v", err)
	}
	clusterDir := filepath.Join(repo, "clusters", "atlantis")
	manifest, err := os.ReadFile(filepath.Join(clusterDir, trustedCAFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "name: "+trustedCAConfigMap) || !strings.Contains(string(manifest), "BEGIN CERTIFICATE") {
		t.Fatalf("bad trusted CA manifest:\n%s", manifest)
	}
	root, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if strings.Count(string(root), trustedCAFileName) != 1 {
		t.Fatalf("trusted CA resource not wired exactly once:\n%s", root)
	}
	env, err := config.LoadEnv(filepath.Join(clusterDir, "cluster-config.env"))
	if err != nil {
		t.Fatal(err)
	}
	values := env.AsMap()
	if values["MANAGER_TRUSTED_CA_CONFIGMAP"] != trustedCAConfigMap ||
		values["BACKEND_TRUSTED_CA_CONFIGMAP"] != trustedCAConfigMap ||
		values["BACKEND_TRUSTED_CA_FILENAME"] != trustedCAKey {
		t.Fatalf("trust env not wired: %+v", values)
	}
	if err := WriteTrustedCA(repo, "atlantis", material, nil); err != nil {
		t.Fatalf("idempotent WriteTrustedCA: %v", err)
	}
}
