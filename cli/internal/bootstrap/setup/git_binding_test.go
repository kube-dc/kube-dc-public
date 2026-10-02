package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	body, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Git fixture failed: %v %s", err, body)
	}
	return strings.TrimSpace(string(body))
}
func gitBindingFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	gitTest(t, base, "init", "--bare", bare)
	checkout := filepath.Join(base, "fleet")
	gitTest(t, base, "init", "-b", "main", checkout)
	gitTest(t, checkout, "config", "user.name", "Test")
	gitTest(t, checkout, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, checkout, "add", ".")
	gitTest(t, checkout, "commit", "-m", "initial")
	gitTest(t, checkout, "remote", "add", "origin", bare)
	gitTest(t, checkout, "push", "-u", "origin", "main")
	return checkout, bare
}

func TestGitBindingRejectsLocalAndRemoteDrift(t *testing.T) {
	for _, variant := range []string{"clean", "untracked", "tracked", "branch", "head", "push-url", "remote-head", "credentials"} {
		t.Run(variant, func(t *testing.T) {
			checkout, bare := gitBindingFixture(t)
			binding, err := InspectGitBinding(context.Background(), checkout)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "untracked":
				_ = os.WriteFile(filepath.Join(checkout, "foreign.txt"), []byte("operator work\n"), 0600)
			case "tracked":
				_ = os.WriteFile(filepath.Join(checkout, "README.md"), []byte("changed\n"), 0600)
			case "branch":
				gitTest(t, checkout, "switch", "-c", "other")
			case "head":
				gitTest(t, checkout, "commit", "--allow-empty", "-m", "another")
			case "push-url":
				gitTest(t, checkout, "remote", "set-url", "--push", "origin", bare+"-other")
			case "remote-head":
				gitTest(t, checkout, "commit", "--allow-empty", "-m", "upstream")
				gitTest(t, checkout, "push")
				gitTest(t, checkout, "reset", "--hard", binding.Head)
			case "credentials":
				gitTest(t, checkout, "remote", "set-url", "origin", "https://user:DO-NOT-PRINT@example.test/fleet.git")
			}
			err = RecheckGitBinding(context.Background(), binding)
			if variant == "clean" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("Git drift passed review")
			}
			if err != nil && strings.Contains(err.Error(), "DO-NOT-PRINT") {
				t.Fatal("remote credential leaked")
			}
		})
	}
}
