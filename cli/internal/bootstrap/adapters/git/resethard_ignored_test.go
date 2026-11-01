package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A hard reset restores tracked files only: ignored local state such as an
// age key or an installer input must survive a rollback.
func TestResetHardKeepsIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("age.key\n/install/site.env\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "one")
	pre := run("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "age.key"), []byte("SECRET\n"), 0o600)
	os.MkdirAll(filepath.Join(dir, "install"), 0o755)
	os.WriteFile(filepath.Join(dir, "install", "site.env"), []byte("A=1\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2\n"), 0o644)
	if err := New().ResetHard(context.Background(), dir, pre); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"age.key", "install/site.env"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("ignored %s removed by ResetHard: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tracked.txt")); string(b) != "v1\n" {
		t.Errorf("tracked file not restored: %q", b)
	}
}
