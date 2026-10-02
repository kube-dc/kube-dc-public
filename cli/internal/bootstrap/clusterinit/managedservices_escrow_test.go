package clusterinit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/sops"
)

func testCellKey(seedByte byte, cell string) map[string][]byte {
	seed := bytes.Repeat([]byte{seedByte}, ed25519.SeedSize)
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	digest := sha256.Sum256(public)
	return map[string][]byte{
		"seed":   []byte(base64.StdEncoding.EncodeToString(seed)),
		"public": []byte(base64.StdEncoding.EncodeToString(public)),
		"keyID":  []byte("ed25519-" + hex.EncodeToString(digest[:6])),
		"cellID": []byte(cell),
	}
}

func TestCellKeyIdentityValidation(t *testing.T) {
	key := testCellKey(7, "cell-example")
	if err := validateCellKeyData("cell-example", key); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name string
		key  string
		data []byte
	}{
		{"wrong cell", "cellID", []byte("cell-other")},
		{"bad seed", "seed", []byte("not-base64")},
		{"wrong public", "public", []byte("not-the-public-key")},
		{"wrong key ID", "keyID", []byte("ed25519-wrong")},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := make(map[string][]byte, len(key))
			for name, value := range key {
				changed[name] = value
			}
			changed[mutation.key] = mutation.data
			if err := validateCellKeyData("cell-example", changed); err == nil {
				t.Fatal("invalid cell key accepted")
			}
		})
	}
}

func TestCellEscrowEncryptsAndPreservesIdentity(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed")
	}
	if _, err := exec.LookPath("age-keygen"); err != nil {
		t.Skip("age-keygen is not installed")
	}
	repo := t.TempDir()
	keyPath := filepath.Join(repo, "age.key")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("generate disposable age key: %v: %s", err, out)
	}
	public, err := exec.Command("age-keygen", "-y", keyPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)
	config := "creation_rules:\n  - path_regex: '\\.enc\\.yaml$'\n    encrypted_regex: '^(data|stringData)$'\n    age: '" + strings.TrimSpace(string(public)) + "'\n"
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "clusters", "example"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := testCellKey(7, "cell-example")
	path, err := EscrowManagedServicesCellKey(ctx, repo, "example", "cell-example", key, sops.New())
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("sops:")) || !bytes.Contains(first, []byte("ENC[")) ||
		bytes.Contains(first, key["seed"]) || bytes.Contains(first, []byte(base64.StdEncoding.EncodeToString(key["seed"]))) {
		t.Fatal("cell escrow did not encrypt the private seed")
	}
	if _, err := EscrowManagedServicesCellKey(ctx, repo, "example", "cell-example", key, sops.New()); err != nil {
		t.Fatal("repeat escrow must be idempotent:", err)
	}
	restored, err := LoadManagedServicesCellKeyEscrow(ctx, repo, "example", "cell-example", sops.New())
	if err != nil {
		t.Fatal("restore escrow:", err)
	}
	for field, value := range key {
		if !bytes.Equal(restored[field], value) {
			t.Fatalf("restored cell key differs in %s", field)
		}
	}
	clearCellKeyData(restored)
	if _, err := LoadManagedServicesCellKeyEscrow(ctx, repo, "example", "other-cell", sops.New()); err == nil {
		t.Fatal("restore accepted another cell identity")
	}
	second, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("repeat escrow changed the ciphertext", err)
	}
	if _, err := EscrowManagedServicesCellKey(ctx, repo, "example", "cell-example", testCellKey(8, "cell-example"), sops.New()); err == nil {
		t.Fatal("a different cell key replaced the escrow")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, unchanged) {
		t.Fatal("identity refusal changed the escrow", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(strings.Replace(config, strings.TrimSpace(string(public)), "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EscrowManagedServicesCellKey(ctx, repo, "example", "cell-example", key, sops.New()); err == nil {
		t.Fatal("escrow accepted a SOPS rule without age recipients")
	}
}
