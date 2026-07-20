package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIdentityFileCandidatesConfiguredPrecedesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := identityFileCandidates("/keys/cluster")
	want := []string{
		"/keys/cluster",
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_ecdsa_sk"),
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_ed25519_sk"),
		filepath.Join(home, ".ssh", "id_dsa"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%v", got)
	}
}

func TestIdentityFileCandidatesDeduplicatesDefaultReportedBySSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultRSA := filepath.Join(home, ".ssh", "id_rsa")
	got := identityFileCandidates(defaultRSA)
	if len(got) != 6 || got[0] != defaultRSA {
		t.Fatalf("candidates=%v", got)
	}
}

func TestLoadAuthMethodsUsesOpenSSHDefaultIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "id_rsa"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	methods, err := loadAuthMethods("")
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 {
		t.Fatalf("methods=%d", len(methods))
	}
}
