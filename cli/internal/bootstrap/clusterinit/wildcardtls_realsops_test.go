package clusterinit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A resumed init must not re-encrypt unchanged material. The decrypt-compare
// needs the Fleet's own age.key; without it every resume committed a fake
// rotation that restarted all dependent Flux layers (found in a 2026-10-31 installation rehearsal).
func TestWriteWildcardTLS_RealSopsResumeKeepsCiphertext(t *testing.T) {
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	t.Setenv("SOPS_AGE_KEY", "")
	t.Setenv("HOME", t.TempDir()) // no ~/.config/sops/age/keys.txt fallback
	fleet := tlsFleet(t)
	keyPath := filepath.Join(fleet, "age.key")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("generate disposable age key: %v: %s", err, out)
	}
	public, err := exec.Command("age-keygen", "-y", keyPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	config := "creation_rules:\n  - path_regex: '\\.enc\\.yaml$'\n    encrypted_regex: '^(data|stringData)$'\n    age: '" +
		strings.TrimSpace(string(public)) + "'\n"
	if err := os.WriteFile(filepath.Join(fleet, ".sops.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	m := wildcardMaterial(t)
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fleet, "clusters", "c1", wildcardSecretsFileName)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("unchanged TLS material was re-encrypted on resume")
	}
}

// The decrypt-compare must find the Fleet's own key when none is selected.
func TestSopsDecryptEnvUsesFleetAgeKey(t *testing.T) {
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	t.Setenv("SOPS_AGE_KEY", "")
	fleet := t.TempDir()
	if env := sopsDecryptEnv(fleet); strings.Contains(strings.Join(env, "\n"), "SOPS_AGE_KEY_FILE="+fleet) {
		t.Fatal("selected a key that does not exist")
	}
	if err := os.WriteFile(filepath.Join(fleet, "age.key"), []byte("AGE-SECRET-KEY-TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "SOPS_AGE_KEY_FILE=" + filepath.Join(fleet, "age.key")
	found := false
	for _, e := range sopsDecryptEnv(fleet) {
		found = found || e == want
	}
	if !found {
		t.Fatal("the Fleet age key was not passed to sops")
	}
	t.Setenv("SOPS_AGE_KEY_FILE", "/operator/key")
	for _, e := range sopsDecryptEnv(fleet) {
		if e == want {
			t.Fatal("overrode the operator's key selection")
		}
	}
}
