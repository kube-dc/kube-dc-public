package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// configEnvFixture is a cluster-config.env with an inline comment on one
// key (to prove get/list strip it).
const configEnvFixture = "KUBE_DC_MANAGER_TAG=v0.3.88   # pinned for the billing fix\n" +
	"PROM_RETENTION=15d\n"

func runConfig(t *testing.T, repo string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapConfigCmd(&fleetRepo)
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestBootstrapConfig_ListAndGet(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})

	out, err := runConfig(t, repo, "list", "cloud")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Inline comment stripped in the listing.
	if !strings.Contains(out, "KUBE_DC_MANAGER_TAG=v0.3.88\n") {
		t.Errorf("list should strip inline comment:\n%s", out)
	}
	if !strings.Contains(out, "PROM_RETENTION=15d") {
		t.Errorf("list missing PROM_RETENTION:\n%s", out)
	}

	out, err = runConfig(t, repo, "get", "cloud", "KUBE_DC_MANAGER_TAG")
	if err != nil || strings.TrimSpace(out) != "v0.3.88" {
		t.Errorf("get = %q, err %v; want v0.3.88", strings.TrimSpace(out), err)
	}

	if _, err := runConfig(t, repo, "get", "cloud", "NOPE"); err == nil {
		t.Error("get of a missing key should error")
	}
	if _, err := runConfig(t, repo, "get", "ghost-cluster", "X"); err == nil {
		t.Error("get on an unknown cluster should error")
	}
}

func TestBootstrapConfig_SetPreviewDoesNotWrite(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	envPath := filepath.Join(repo, "clusters", "cloud", "cluster-config.env")
	before, _ := os.ReadFile(envPath)

	// No --yes → preview only.
	out, err := runConfig(t, repo, "set", "cloud", "KUBE_DC_MANAGER_TAG=v0.3.90")
	if err != nil {
		t.Fatalf("set preview: %v", err)
	}
	if !strings.Contains(out, "~ KUBE_DC_MANAGER_TAG: v0.3.88 → v0.3.90") {
		t.Errorf("preview diff wrong:\n%s", out)
	}
	if !strings.Contains(out, "re-run with --yes") {
		t.Errorf("preview should tell the operator to use --yes:\n%s", out)
	}
	after, _ := os.ReadFile(envPath)
	if !bytes.Equal(before, after) {
		t.Error("preview must not modify cluster-config.env")
	}
}

func TestBootstrapConfig_SetValidation(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})

	// Unknown key without --add.
	if _, err := runConfig(t, repo, "set", "cloud", "NEW_KEY=x"); err == nil {
		t.Error("unknown key without --add should error")
	}
	// Bad assignment shape.
	if _, err := runConfig(t, repo, "set", "cloud", "noequals"); err == nil {
		t.Error("assignment without '=' should error")
	}
	// No-op set → "nothing to change", no error.
	out, err := runConfig(t, repo, "set", "cloud", "PROM_RETENTION=15d")
	if err != nil {
		t.Fatalf("no-op set: %v", err)
	}
	if !strings.Contains(out, "nothing to change") {
		t.Errorf("no-op should say nothing to change:\n%s", out)
	}
}

// TestBootstrapConfig_SetYesNoPush_CommitsLocally drives the full write +
// git transaction against a real git-initialised fleet fixture, with
// --no-push so no remote is needed.
func TestBootstrapConfig_SetYesNoPush_CommitsLocally(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	gitInit(t, repo)
	headBefore := gitHead(t, repo)

	out, err := runConfig(t, repo, "set", "cloud", "KUBE_DC_MANAGER_TAG=v0.3.90", "--yes", "--no-push")
	if err != nil {
		t.Fatalf("set --yes --no-push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "committed") {
		t.Errorf("expected a commit confirmation:\n%s", out)
	}
	// A new commit landed.
	if gitHead(t, repo) == headBefore {
		t.Error("HEAD did not advance — no commit made")
	}
	// The file actually changed.
	body, _ := os.ReadFile(filepath.Join(repo, "clusters", "cloud", "cluster-config.env"))
	if !strings.Contains(string(body), "KUBE_DC_MANAGER_TAG=v0.3.90") {
		t.Errorf("env not updated:\n%s", body)
	}
	// Working tree is clean again (the change was committed).
	if st := gitStatus(t, repo); strings.TrimSpace(st) != "" {
		t.Errorf("tree should be clean after commit, got:\n%s", st)
	}
}

// Regression for the review P2: `config set --yes` needs ONLY Git, so a
// missing/invalid kubeconfig must NOT block it (NewGitOnly bypasses k8s).
func TestBootstrapConfig_SetYes_NoKubeconfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Point KUBECONFIG at a nonexistent file — the old NewSession path
	// failed here with "git adapter unavailable".
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	gitInit(t, repo)

	out, err := runConfig(t, repo, "set", "cloud", "PROM_RETENTION=30d", "--yes", "--no-push")
	if err != nil {
		t.Fatalf("set --yes with no kubeconfig should still commit (git-only): %v\n%s", err, out)
	}
	if !strings.Contains(out, "committed") {
		t.Errorf("expected a commit even without a kubeconfig:\n%s", out)
	}
}

func TestBootstrapConfig_SetRejectsBadProvider(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	out, err := runConfig(t, repo, "set", "cloud", "PROM_RETENTION=30d", "--yes", "--provider", "bitbucket")
	if err == nil || !strings.Contains(err.Error(), "provider must be github or gitlab") {
		t.Errorf("bad --provider should be rejected, got out=%q err=%v", out, err)
	}
}

func TestBootstrapConfig_SetRejectsBadKey(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	// Even with --add, a non-SCREAMING_SNAKE key is refused (P3).
	if _, err := runConfig(t, repo, "set", "cloud", "lower_case=1", "--add"); err == nil {
		t.Error("--add with a lowercase key should be rejected")
	}
}

func TestBootstrapConfig_SetYes_DirtyTreeRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := writeFleetFixture(t, map[string]string{"cloud": configEnvFixture})
	gitInit(t, repo)
	// Dirty the tree with an unrelated change.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runConfig(t, repo, "set", "cloud", "KUBE_DC_MANAGER_TAG=v0.3.90", "--yes", "--no-push")
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Errorf("dirty tree should be refused, got out=%q err=%v", out, err)
	}
}

// ---- git test helpers ----

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "fixture")
}

func gitHead(t *testing.T, dir string) string {
	return strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
}

func gitStatus(t *testing.T, dir string) string {
	return gitRun(t, dir, "status", "--porcelain")
}
