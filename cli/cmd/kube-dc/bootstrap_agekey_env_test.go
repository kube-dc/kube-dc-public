package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUseFleetAgeKey(t *testing.T) {
	repo := t.TempDir()
	key := filepath.Join(repo, "age.key")
	if err := os.WriteFile(key, []byte(fixtureAgeKeyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	useFleetAgeKey(repo)
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != key {
		t.Fatalf("SOPS_AGE_KEY_FILE = %q, want %q", got, key)
	}
	// An operator's own selection wins.
	t.Setenv("SOPS_AGE_KEY_FILE", "/operator/keys.txt")
	useFleetAgeKey(repo)
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != "/operator/keys.txt" {
		t.Fatalf("operator selection replaced: %q", got)
	}
	// No key in the checkout: nothing is set.
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	useFleetAgeKey(t.TempDir())
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != "" {
		t.Fatalf("set without a key: %q", got)
	}
}

// A key file SOPS could not parse is not exported.
func TestUseFleetAgeKey_SkipsMalformedKey(t *testing.T) {
	t.Setenv("SOPS_AGE_KEY", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	for _, body := range []string{"", "# public key: " + fixtureAgePubkey + "\n"} {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "age.key"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		useFleetAgeKey(repo)
		if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != "" {
			t.Fatalf("malformed key %q exported: %q", body, got)
		}
	}
}
