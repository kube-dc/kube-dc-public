package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// interruptedKeyCheckout is a committed starter checkout after a run that
// generated age.key and stopped before enrolling it in .sops.yaml.
func interruptedKeyCheckout(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, marker := range []string{
		"bootstrap/add-cluster.sh",
		"infrastructure/kube-ovn-network-public/kustomization.yaml",
		"infrastructure/ext-net-bridge-tag/kustomization.yaml",
		"platform/kustomization.yaml",
		"addons/metallb/kustomization.yaml",
		"addons/metallb-config/kustomization.yaml",
		"addons/metallb-config-bgp/kustomization.yaml",
		"scripts/install-prerequisites.sh",
	} {
		path := filepath.Join(repo, marker)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("age.key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "age.key"), []byte(fixtureAgeKeyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	sops := "creation_rules:\n  - path_regex: '\\.enc\\.yaml$'\n    encrypted_regex: '^(data|stringData)$'\n    age: ''\n"
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(sops), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestValidateAgeKeyEnrollment_RepairsInterruptedEnrollment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	repo := interruptedKeyCheckout(t)
	o := &clusterinit.InitOptions{Repo: repo, FleetMode: clusterinit.FleetExistingRepo}

	// Dry run: reported, nothing written.
	var dry bytes.Buffer
	if err := validateAgeKeyEnrollment(context.Background(), &dry, o, false); err != nil ||
		!strings.Contains(dry.String(), "the install enrolls "+fixtureAgePubkey) {
		t.Fatalf("dry run: err=%v\n%s", err, dry.String())
	}
	if os.Getenv("SOPS_AGE_KEY_FILE") != "" {
		t.Fatalf("dry run selected a key for later sops calls")
	}
	if body, _ := os.ReadFile(filepath.Join(repo, ".sops.yaml")); strings.Contains(string(body), fixtureAgePubkey) {
		t.Fatalf("dry run wrote .sops.yaml")
	}

	var out bytes.Buffer
	if err := validateAgeKeyEnrollment(context.Background(), &out, o, true); err != nil {
		t.Fatalf("repair: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "[sops] repaired .sops.yaml") || !strings.Contains(out.String(), "[sops] enrolled") {
		t.Errorf("output:\n%s", out.String())
	}
	body, _ := os.ReadFile(filepath.Join(repo, ".sops.yaml"))
	if !strings.Contains(string(body), "age: '"+fixtureAgePubkey+"'") {
		t.Fatalf(".sops.yaml not repaired:\n%s", body)
	}
	if st := gitStatus(t, repo); strings.TrimSpace(st) != "" {
		t.Errorf("repair not committed:\n%s", st)
	}
	if subject := gitRun(t, repo, "log", "-1", "--format=%s"); !strings.Contains(subject, "enroll operator age key") {
		t.Errorf("last commit: %s", subject)
	}
	keys, err := os.ReadFile(filepath.Join(home, ".config", "sops", "age", "keys.txt"))
	if err != nil || !strings.Contains(string(keys), fixtureAgePubkey) {
		t.Errorf("key not added to the sops default keys file: %v", err)
	}
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != filepath.Join(repo, "age.key") {
		t.Errorf("SOPS_AGE_KEY_FILE = %q, want the verified fleet key", got)
	}

	// A rerun is a plain enrolled pass.
	out.Reset()
	if err := validateAgeKeyEnrollment(context.Background(), &out, o, true); err != nil || strings.Contains(out.String(), "repaired") {
		t.Fatalf("rerun: err=%v\n%s", err, out.String())
	}
}

// Once anything is encrypted, the empty recipient is not repaired: the files
// were encrypted for some other key.
func TestValidateAgeKeyEnrollment_NoRepairOnceEncrypted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	repo := interruptedKeyCheckout(t)
	enc := filepath.Join(repo, "clusters", "site", "secrets.enc.yaml")
	if err := os.MkdirAll(filepath.Dir(enc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("data:\n  x: ENC[x]\nsops:\n  age: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &clusterinit.InitOptions{Repo: repo, FleetMode: clusterinit.FleetExistingRepo}
	err := validateAgeKeyEnrollment(context.Background(), &bytes.Buffer{}, o, true)
	if err == nil || !strings.Contains(err.Error(), "lists no age recipients") {
		t.Fatalf("want refusal, got %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(repo, ".sops.yaml")); strings.Contains(string(body), fixtureAgePubkey) {
		t.Fatalf(".sops.yaml was changed")
	}
}

// The script stopped after it wrote age.key, before it created .sops.yaml;
// the checkout is used as `--repo .`; something else is staged.
func TestValidateAgeKeyEnrollment_CreatesMissingConfigFromRelativeRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	repo := interruptedKeyCheckout(t)
	if err := os.Remove(filepath.Join(repo, ".sops.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "site.env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "site.env")
	t.Chdir(repo)

	o := &clusterinit.InitOptions{Repo: ".", FleetMode: clusterinit.FleetNewRepo}
	var out bytes.Buffer
	if err := validateAgeKeyEnrollment(context.Background(), &out, o, true); err != nil {
		t.Fatalf("repair: %v\n%s", err, out.String())
	}
	body, err := os.ReadFile(".sops.yaml")
	if err != nil || !strings.Contains(string(body), "age: '"+fixtureAgePubkey+"'") ||
		!strings.Contains(string(body), "encrypted_regex: '^(data|stringData)$'") {
		t.Fatalf(".sops.yaml not created: %v\n%s", err, body)
	}
	if files := gitRun(t, repo, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(files) != ".sops.yaml" {
		t.Errorf("repair commit holds %q, want only .sops.yaml", files)
	}
	if st := gitStatus(t, repo); !strings.Contains(st, "A  site.env") {
		t.Errorf("the staged file was not left staged:\n%s", st)
	}
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != filepath.Join(repo, "age.key") {
		t.Errorf("SOPS_AGE_KEY_FILE = %q, want the absolute key path", got)
	}
}
