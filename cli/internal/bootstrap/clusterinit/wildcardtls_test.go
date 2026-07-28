package clusterinit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
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
)

// writePair generates a certificate for the given SANs and returns cert/key paths.
func writePair(t *testing.T, sans []string, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: sans[0]},
		DNSNames:              sans,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	kb := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestLoadWildcardTLS_AcceptsMatchingWildcard(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"*.example.com", "example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	m, err := LoadWildcardTLS(cert, key, "example.com")
	if err != nil {
		t.Fatalf("valid wildcard rejected: %v", err)
	}
	if m.NotAfter.Before(now) {
		t.Fatal("NotAfter not surfaced")
	}
}

// The failure this prevents is the nastiest: a certificate that loads fine but
// simply does not cover the platform hostnames, so only untested pages break.
func TestLoadWildcardTLS_RejectsWrongDomain(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"*.other.example"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	_, err := LoadWildcardTLS(cert, key, "example.com")
	if err == nil {
		t.Fatal("wrong-domain certificate accepted")
	}
	// Rejected for the right reason: the wildcard SAN is for another domain.
	if !strings.Contains(err.Error(), "wildcard SAN") && !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("rejected for an unexpected reason: %v", err)
	}
}

func TestLoadWildcardTLS_RejectsExpired(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"*.example.com"}, now.Add(-48*time.Hour), now.Add(-time.Hour))
	if _, err := LoadWildcardTLS(cert, key, "example.com"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired certificate accepted: %v", err)
	}
}

// A mismatched key yields opaque TLS handshake errors at runtime, so it must be
// caught at init time.
func TestLoadWildcardTLS_RejectsMismatchedKey(t *testing.T) {
	now := time.Now()
	cert, _ := writePair(t, []string{"*.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	_, otherKey := writePair(t, []string{"*.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	if _, err := LoadWildcardTLS(cert, otherKey, "example.com"); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

// Every Gateway listener secret must be produced, including the domain-templated
// keycloak name — a missing one means that listener serves no certificate.
func TestRenderWildcardSecrets_CoversEveryPlatformSecret(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"*.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	m, err := LoadWildcardTLS(cert, key, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	out := RenderWildcardSecrets(m, "example.com")

	if n := strings.Count(out, "kind: Secret"); n != len(wildcardSecretTargets()) {
		t.Fatalf("rendered %d Secrets, want %d", n, len(wildcardSecretTargets()))
	}
	for _, want := range []string{
		"name: login.example.com-tls",
		"namespace: keycloak",
		"name: wildcard-tls",
		"name: s3-server-tls",
		"name: registry-server-tls",
		"kube-dc.com/byo-wildcard-tls: \"true\"",
		"type: kubernetes.io/tls",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered manifest missing %q", want)
		}
	}
	// The plaintext key must never be emitted un-encoded.
	if strings.Contains(out, "BEGIN RSA PRIVATE KEY") {
		t.Fatal("private key emitted unencoded into the manifest")
	}
}

// Suppression must be grouped by the Kustomization that DECLARES each
// Certificate — patching only ./platform leaves registry-tls and s3-tls alive
// and Flux recreates them (observed on a private-CA install).
func TestRenderCertificateSuppressionPatches_GroupedByDeclaringKustomization(t *testing.T) {
	got := RenderCertificateSuppressionPatches()
	for _, ks := range []string{"platform", "registry-depot", "infra-object-storage"} {
		if _, ok := got[ks]; !ok {
			t.Fatalf("no suppression emitted for Kustomization %q", ks)
		}
	}
	if !strings.Contains(got["registry-depot"], "name: registry-tls") {
		t.Error("registry-tls not suppressed in registry-depot")
	}
	if !strings.Contains(got["infra-object-storage"], "name: s3-tls") {
		t.Error("s3-tls not suppressed in infra-object-storage")
	}
	total := 0
	for _, v := range got {
		total += strings.Count(v, "$patch: delete")
	}
	if total != len(suppressedCertificates()) {
		t.Fatalf("emitted %d delete patches, want %d", total, len(suppressedCertificates()))
	}
}

// Every secret a Certificate issues into must be covered by the BYO set,
// otherwise suppressing that Certificate leaves a listener with no material.
func TestSuppressedCertificatesAreCoveredByWildcardSecrets(t *testing.T) {
	secrets := map[string]bool{}
	for _, s := range wildcardSecretTargets() {
		name := s.Name
		if s.DomainTemplated {
			name = strings.ReplaceAll(s.Name, "%s", "example.com")
		}
		secrets[s.Namespace+"/"+name] = true
	}
	// Certificate name -> the secret it issues into (from the fleet manifests).
	issuesInto := map[string]string{
		"envoy-gateway-system/wildcard-tls":  "envoy-gateway-system/wildcard-tls",
		"keycloak/login-tls":                 "keycloak/login.example.com-tls",
		"kube-dc/kube-dc-frontend-tls":       "kube-dc/kube-dc-frontend-tls",
		"kube-dc/kube-dc-backend-tls":        "kube-dc/kube-dc-backend-tls",
		"kube-dc/kube-dc-admin-frontend-tls": "kube-dc/kube-dc-admin-frontend-tls",
		"monitoring/tls-grafana":             "monitoring/tls-grafana",
		"monitoring/tls-mimir-query":         "monitoring/tls-mimir-query",
		"monitoring/tls-loki-query":          "monitoring/tls-loki-query",
		"monitoring/tls-mimir-ruler":         "monitoring/tls-mimir-ruler",
		"monitoring/tls-mimir-alertmanager":  "monitoring/tls-mimir-alertmanager",
		"flux-system/tls-flux":               "flux-system/tls-flux",
		"openbao/tls-openbao":                "openbao/tls-openbao",
		"kube-dc/registry-tls":               "kube-dc/registry-server-tls",
		"rook-ceph/s3-tls":                   "rook-ceph/s3-server-tls",
	}
	for _, c := range suppressedCertificates() {
		key := c.Namespace + "/" + c.Name
		sec, ok := issuesInto[key]
		if !ok {
			t.Fatalf("suppressed Certificate %s has no known target secret in this test's map", key)
		}
		if !secrets[sec] {
			t.Errorf("Certificate %s is suppressed but its secret %s is not in the BYO set — that listener would serve nothing", key, sec)
		}
	}
}

func TestValidateTLSMode(t *testing.T) {
	if err := ValidateTLSMode("", "", ""); err != nil {
		t.Errorf("empty mode must default-accept: %v", err)
	}
	if err := ValidateTLSMode(TLSModeACME, "c", ""); err == nil {
		t.Error("cert material with acme mode must be rejected")
	}
	if err := ValidateTLSMode(TLSModeBYOWildcard, "", ""); err == nil {
		t.Error("byo-wildcard without material must be rejected")
	}
	if err := ValidateTLSMode("selfsigned", "", ""); err == nil {
		t.Error("unknown mode must be rejected")
	}
	if err := ValidateTLSMode(TLSModeBYOWildcard, "c", "k"); err != nil {
		t.Errorf("valid byo-wildcard rejected: %v", err)
	}
}

// codex review 2026-07-26 (HIGH): verifying ONE hostname did not prove wildcard
// coverage. A certificate whose only SAN is console.<domain> passed, was
// replicated into all 14 platform Secrets, and then failed on every OTHER
// hostname — login, backend, grafana… i.e. the platform half-works.
func TestLoadWildcardTLS_RejectsSingleHostnameCert(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"console.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	_, err := LoadWildcardTLS(cert, key, "example.com")
	if err == nil {
		t.Fatal("a console-only certificate was accepted as a wildcard")
	}
	if !strings.Contains(err.Error(), "wildcard SAN") {
		t.Fatalf("error should name the missing wildcard SAN, got: %v", err)
	}
}

// An apex-only certificate is likewise not a wildcard.
func TestLoadWildcardTLS_RejectsApexOnlyCert(t *testing.T) {
	now := time.Now()
	cert, key := writePair(t, []string{"example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	if _, err := LoadWildcardTLS(cert, key, "example.com"); err == nil {
		t.Fatal("an apex-only certificate was accepted as a wildcard")
	}
}

// codex review (MEDIUM): X509KeyPair parses only Certificate[0], so a malformed
// second block used to pass and then break Envoy or client chain building.
func TestLoadWildcardTLS_RejectsMalformedIntermediate(t *testing.T) {
	now := time.Now()
	certPath, keyPath := writePair(t, []string{"*.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	good, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := append(append([]byte{}, good...),
		[]byte("-----BEGIN CERTIFICATE-----\nbm90LWEtY2VydA==\n-----END CERTIFICATE-----\n")...)
	mixed := filepath.Join(t.TempDir(), "chain.crt")
	if err := os.WriteFile(mixed, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWildcardTLS(mixed, keyPath, "example.com"); err == nil {
		t.Fatal("a malformed intermediate block was accepted")
	}
}

// ECDSA must be accepted — the validation must not be RSA-only.
func TestLoadWildcardTLS_AcceptsECDSA(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "*.example.com"},
		DNSNames:              []string{"*.example.com", "example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cp := filepath.Join(dir, "tls.crt")
	kp := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWildcardTLS(cp, kp, "example.com"); err != nil {
		t.Fatalf("ECDSA wildcard rejected: %v", err)
	}
}

// codex review (MEDIUM): an EKU-restricted certificate that cannot do server
// auth passed every other check and was then rejected by all clients.
func TestLoadWildcardTLS_RejectsClientAuthOnlyCert(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(9),
		Subject:               pkix.Name{CommonName: "*.example.com"},
		DNSNames:              []string{"*.example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cp := filepath.Join(dir, "tls.crt")
	kp := filepath.Join(dir, "tls.key")
	os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	if _, err := LoadWildcardTLS(cp, kp, "example.com"); err == nil {
		t.Fatal("a clientAuth-only certificate was accepted for TLS serving")
	}
}
