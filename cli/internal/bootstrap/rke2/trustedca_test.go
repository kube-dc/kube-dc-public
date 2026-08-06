package rke2

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
)

// Node-layer trust. Whatever this accepts becomes a trust anchor on EVERY node
// of the cluster — trusted by containerd, kubelet and every process on the host.
// The tests below are about what must never get that far.

func writePEM(t *testing.T, dir, name string, blocks ...[]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var body []byte
	for _, b := range blocks {
		body = append(body, b...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mkCert builds a certificate PEM with the properties a test needs.
func mkCert(t *testing.T, cn string, isCA bool, notAfter time.Time, usage x509.KeyUsage) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		KeyUsage:              usage,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func goodCA(t *testing.T, cn string) []byte {
	return mkCert(t, cn, true, time.Now().AddDate(10, 0, 0), x509.KeyUsageCertSign)
}

func TestAcceptsARealCAChain(t *testing.T) {
	dir := t.TempDir()
	path := writePEM(t, dir, "ca.pem", goodCA(t, "Root"), goodCA(t, "Intermediate"))

	m, err := LoadTrustedCAForNodes(path)
	if err != nil {
		t.Fatalf("a valid CA chain must be accepted: %v", err)
	}
	if len(m.Subjects) != 2 {
		t.Fatalf("expected 2 CAs, got %d", len(m.Subjects))
	}
	if m.Fingerprint == "" {
		t.Fatal("a fingerprint is what lets an operator confirm what was installed")
	}
}

// No path = no private CA. That is the correct default for a connected,
// ACME-issued cluster, and it must not be an error.
func TestNoBundleIsNotAnError(t *testing.T) {
	m, err := LoadTrustedCAForNodes("")
	if err != nil || m != nil {
		t.Fatalf("an empty path must mean 'public CAs only', got (%v, %v)", m, err)
	}
	if m.Summary() != "none (public CAs only)" {
		t.Fatalf("a nil bundle must describe itself safely, got %q", m.Summary())
	}
}

// The one that matters most: a private key must never be pushed to a fleet of
// hosts. Finding one means the operator handed us the wrong file.
func TestRefusesAPrivateKey(t *testing.T) {
	dir := t.TempDir()
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("not-really")})
	path := writePEM(t, dir, "mixed.pem", goodCA(t, "Root"), key)

	_, err := LoadTrustedCAForNodes(path)
	if err == nil {
		t.Fatal("a PRIVATE KEY block must be refused, never distributed to nodes")
	}
	if !strings.Contains(err.Error(), "certificates only") {
		t.Fatalf("the error must name the problem, got: %v", err)
	}
}

// Installing a leaf as an anchor silently widens node trust to anything that
// leaf's key can sign — not what an operator passing "our CA" expects.
func TestRefusesALeafCertificate(t *testing.T) {
	dir := t.TempDir()
	leaf := mkCert(t, "web.example.invalid", false, time.Now().AddDate(1, 0, 0), x509.KeyUsageDigitalSignature)
	path := writePEM(t, dir, "leaf.pem", leaf)

	_, err := LoadTrustedCAForNodes(path)
	if err == nil {
		t.Fatal("a leaf certificate must not be installed as a node trust anchor")
	}
	if !strings.Contains(err.Error(), "not a CA") {
		t.Fatalf("the error must say why, got: %v", err)
	}
}

func TestRefusesACAThatCannotSign(t *testing.T) {
	dir := t.TempDir()
	bad := mkCert(t, "NoSign", true, time.Now().AddDate(1, 0, 0), x509.KeyUsageDigitalSignature)
	path := writePEM(t, dir, "nosign.pem", bad)

	if _, err := LoadTrustedCAForNodes(path); err == nil {
		t.Fatal("a CA without cert-sign usage cannot serve as an anchor")
	}
}

func TestRefusesGarbageAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{"empty.pem", ""},
		{"text.pem", "this is not a certificate\n"},
		{"truncated.pem", "-----BEGIN CERTIFICATE-----\nnope\n"},
	} {
		path := writePEM(t, dir, tc.name, []byte(tc.body))
		if _, err := LoadTrustedCAForNodes(path); err == nil {
			t.Fatalf("%s must be refused", tc.name)
		}
	}
	if _, err := LoadTrustedCAForNodes(filepath.Join(dir, "absent.pem")); err == nil {
		t.Fatal("a missing file must be refused, not silently ignored")
	}
}

// A whole system bundle, a log, or a truncated binary passed by mistake. Pushing
// hundreds of KB of unknown anchors onto every node is not a silent mistake.
func TestRefusesAnImplausiblyLargeBundle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.pem")
	if err := os.WriteFile(path, make([]byte, maxTrustedCABytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedCAForNodes(path); err == nil {
		t.Fatal("an implausibly large bundle must be refused")
	}
}

// Expiry warns rather than refuses: an expired root in a rotation bundle is
// normal, and refusing it would break the overlap that makes rotation safe.
func TestExpiryWarnsButDoesNotRefuse(t *testing.T) {
	dir := t.TempDir()
	soon := mkCert(t, "ExpiringRoot", true, time.Now().AddDate(0, 0, 10), x509.KeyUsageCertSign)
	path := writePEM(t, dir, "soon.pem", soon)

	m, err := LoadTrustedCAForNodes(path)
	if err != nil {
		t.Fatalf("a soon-to-expire CA must still be accepted: %v", err)
	}
	warn := m.ExpiringSoon(90*24*time.Hour, time.Now())
	if len(warn) != 1 || !strings.Contains(warn[0], "ExpiringRoot") {
		t.Fatalf("expected a warning naming the CA, got %v", warn)
	}
	if len(m.ExpiringSoon(24*time.Hour, time.Now())) != 0 {
		t.Fatal("a CA outside the window must not warn")
	}
}

// The env var carries only the PATH; the bundle itself is pushed as a file. A
// multi-KB PEM on the command line would be fragile and would appear in process
// listings on the host.
func TestOnlyThePathRidesInTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := writePEM(t, dir, "ca.pem", goodCA(t, "Root"))
	m, err := LoadTrustedCAForNodes(path)
	if err != nil {
		t.Fatal(err)
	}

	env := buildInstallEnv(InstallOptions{
		NodeName: "n1", NodeIP: "192.0.2.10", Domain: "example.invalid",
		PodCIDR: "10.42.0.0/16", ServiceCIDR: "10.43.0.0/16", ClusterDNS: "10.43.0.10",
		TrustedCA: m,
	})
	if env[trustedCAEnv] != remoteTrustedCAPath {
		t.Fatalf("expected %s=%s, got %q", trustedCAEnv, remoteTrustedCAPath, env[trustedCAEnv])
	}
	for k, v := range env {
		if strings.Contains(v, "BEGIN CERTIFICATE") {
			t.Fatalf("env %s carries certificate material; only the path may ride in the environment", k)
		}
	}
}

// No bundle must leave the variable unset, so the installer script takes its
// "public CAs only" path rather than looking for a file that was never pushed.
func TestNoBundleLeavesTheEnvironmentUnset(t *testing.T) {
	env := buildInstallEnv(InstallOptions{
		NodeName: "n1", NodeIP: "192.0.2.10", Domain: "example.invalid",
		PodCIDR: "10.42.0.0/16", ServiceCIDR: "10.43.0.0/16", ClusterDNS: "10.43.0.10",
	})
	if _, ok := env[trustedCAEnv]; ok {
		t.Fatal("with no bundle the installer must not be told to look for one")
	}
}

// Release-gate finding. pem.Decode stops at the first byte it cannot read, so
// ignoring the tail silently accepts a SHORTER bundle than supplied. In a
// rotation bundle — outgoing CA followed by a truncated incoming one — that
// installs only the CA that is about to expire and strands every node from the
// registry when it does. The controller already enforced this; the installer
// did not.
func TestRefusesATruncatedTrailingCertificate(t *testing.T) {
	dir := t.TempDir()
	path := writePEM(t, dir, "rotation.pem",
		goodCA(t, "Outgoing Root"),
		[]byte("-----BEGIN CERTIFICATE-----\nMIIBhTCCASug\n"))

	_, err := LoadTrustedCAForNodes(path)
	if err == nil {
		t.Fatal("a truncated trailing certificate must be refused, not silently dropped — " +
			"installing only the outgoing CA strands nodes when it expires")
	}
	if !strings.Contains(err.Error(), "partial bundle") {
		t.Fatalf("the error must say a partial bundle was refused, got: %v", err)
	}
}
