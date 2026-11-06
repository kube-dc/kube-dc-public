package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/git"
)

// The real Git adapter (go-git): its commit stages the whole tree, which is
// what the replacement's checks must account for.
func realGit() *gitadapter.Client { return gitadapter.New() }

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

const (
	oldStarterRef = "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16@sha256:" +
		"e61c7f9d00000000000000000000000000000000000000000000000000000000"
	newStarterRef = "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16@sha256:" +
		"691a397400000000000000000000000000000000000000000000000000000000"
)

// versionedStarter writes the starter shape with every file holding body.
func versionedStarter(body string) *fakePuller {
	files := testStarterFiles("clusters/.gitkeep", ".gitignore", "scripts/tool.py")
	contents := map[string]string{}
	for _, f := range files {
		if f != ".gitignore" {
			contents[f] = body
		}
	}
	contents[".gitignore"] = "age.key\n"
	return &fakePuller{files: files, contents: contents}
}

// importedOperatorCheckout is an operator repository after a first run that
// imported the old starter and stopped in key generation: age.key written,
// .sops.yaml created with an empty recipient and not committed.
func importedOperatorCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "master")
	for _, p := range []string{"README.md", "ansible/site.yml", "install/site.env.example"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("site\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "site")
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: oldStarterRef, Flux: versionedStarter("old\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), []byte("creation_rules:\n  - path_regex: x\n    age: ''\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStarterChangeRequested(t *testing.T) {
	cases := []struct {
		name, recorded, requested string
		want                      bool
	}{
		{"no record", "", oldStarterRef, false},
		{"same digest", "installedRef: " + oldStarterRef + "\ninstalledDigest: sha256:e61c7f9d00000000000000000000000000000000000000000000000000000000\n", oldStarterRef, false},
		{"different digest, same tag", "installedRef: " + oldStarterRef + "\ninstalledDigest: sha256:e61c7f9d00000000000000000000000000000000000000000000000000000000\n", newStarterRef, true},
		{"tag request after a pinned import", "installedRef: " + oldStarterRef + "\n", "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16", false},
		{"different tag", "installedRef: oci://ghcr.io/kube-dc/fleet-starter:v0.9.15\n", "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16", true},
		{"last record wins", "installedRef: oci://ghcr.io/kube-dc/fleet-starter:v0.9.15\ninstalledRef: " + oldStarterRef + "\n", oldStarterRef, false},
		{"empty request", "installedRef: " + oldStarterRef + "\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.recorded != "" {
				if err := os.WriteFile(filepath.Join(dir, ".starter-version"), []byte("version: v0.9.16\n"+tc.recorded), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, got := StarterChangeRequested(dir, tc.requested); got != tc.want {
				t.Fatalf("StarterChangeRequested = %v, want %v", got, tc.want)
			}
		})
	}
}

// A failed first run, then a rerun with a corrected starter ref replaces the old
// starter, keeps operator content and the key, and commits only the removal.
func TestEnsureStarter_ReplacesStarterBeforeAnyOverlay(t *testing.T) {
	dir := importedOperatorCheckout(t)
	before := strings.TrimSpace(gitT(t, dir, "rev-parse", "HEAD"))

	var out strings.Builder
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true, Out: &out,
	})
	if err != nil || !res.Extracted {
		t.Fatalf("replacement: extracted=%v err=%v\n%s", res.Extracted, err, out.String())
	}
	if !strings.Contains(out.String(), "replacing fleet starter "+oldStarterRef+" with "+newStarterRef) {
		t.Errorf("output does not name both starters:\n%s", out.String())
	}
	if got := readFile(t, filepath.Join(dir, "scripts", "tool.py")); got != "new\n" {
		t.Errorf("scripts/tool.py = %q, want the new starter's", got)
	}
	if installed, _ := ReadInstalledStarter(dir); installed.Ref != newStarterRef {
		t.Errorf(".starter-version records %q, want %q", installed.Ref, newStarterRef)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, ".starter-version")), oldStarterRef) {
		t.Errorf(".starter-version still carries the old starter's record")
	}
	for _, p := range []string{"README.md", "ansible/site.yml", "install/site.env.example", "age.key", ".sops.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	// The removal commit holds deletions of starter-owned paths only.
	removal := strings.TrimSpace(gitT(t, dir, "show", "--name-status", "--format=", "HEAD~1"))
	for _, line := range strings.Split(removal, "\n") {
		status, path, _ := strings.Cut(line, "\t")
		top := strings.SplitN(path, "/", 2)[0]
		if status != "D" || !starterOwnedEntries[top] {
			t.Errorf("removal commit touches %q", line)
		}
	}
	if parent := strings.TrimSpace(gitT(t, dir, "rev-parse", "HEAD~2")); parent != before {
		t.Errorf("expected exactly a removal and an import commit after %s, HEAD~2 is %s", before, parent)
	}
	if tracked := gitT(t, dir, "ls-files", "age.key"); tracked != "" {
		t.Errorf("age.key was committed")
	}
	// The leftover .sops.yaml stays out of the starter's commits; the
	// age-key step completes and commits it.
	if dirty := gitT(t, dir, "status", "--porcelain"); dirty != "?? .sops.yaml\n" {
		t.Errorf("status after the replacement:\n%s", dirty)
	}
}

func TestEnsureStarter_ReplacementRefusals(t *testing.T) {
	t.Run("cluster overlay exists: kept", func(t *testing.T) {
		dir := importedOperatorCheckout(t)
		if err := os.MkdirAll(filepath.Join(dir, "clusters", "site"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "clusters", "site", "cluster-config.env"), []byte("X=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, dir, "add", "clusters")
		gitT(t, dir, "commit", "-q", "-m", "overlay")
		puller := versionedStarter("new\n")
		var out strings.Builder
		// A plan saved by an older CLI may name another starter: the
		// installation keeps its own.
		res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: puller, Git: realGit(), OperatorRepository: true, Out: &out,
		})
		if err != nil || res.Extracted || !strings.Contains(out.String(), "keeping it although the plan selects "+newStarterRef) {
			t.Fatalf("installation with another starter in the plan: extracted=%v err=%v\n%s", res.Extracted, err, out.String())
		}
		if puller.called != 0 || readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "old\n" {
			t.Fatalf("an installation's starter was pulled or changed")
		}
		if !errors.Is(CheckStarterReplacement(dir, InstalledStarter{Ref: oldStarterRef}, newStarterRef), ErrStarterChangeRefused) {
			t.Fatalf("CheckStarterReplacement accepted an overlay")
		}
	})
	t.Run("uncommitted operator change", func(t *testing.T) {
		dir := importedOperatorCheckout(t)
		if err := os.WriteFile(filepath.Join(dir, "install", "draft.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		puller := versionedStarter("new\n")
		_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: puller, Git: realGit(), OperatorRepository: true,
		})
		if !errors.Is(err, ErrStarterChangeRefused) || !strings.Contains(err.Error(), "draft.txt") || puller.called != 0 {
			t.Fatalf("dirty operator tree: called=%d err=%v", puller.called, err)
		}
	})
	t.Run("starter file edited", func(t *testing.T) {
		dir := importedOperatorCheckout(t)
		writeOldStarterManifest(t, dir)
		// An edit committed in the checkout: git status is clean.
		if err := os.WriteFile(filepath.Join(dir, "scripts", "tool.py"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, dir, "commit", "-q", "-am", "local edit")
		_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
		})
		if !errors.Is(err, ErrStarterChangeRefused) || !strings.Contains(err.Error(), "changed locally") {
			t.Fatalf("edited starter file: %v", err)
		}
	})
	t.Run("failed pull keeps the old starter", func(t *testing.T) {
		dir := importedOperatorCheckout(t)
		head := gitT(t, dir, "rev-parse", "HEAD")
		puller := versionedStarter("new\n")
		puller.err = errors.New("registry unreachable")
		_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: puller, Git: realGit(), OperatorRepository: true,
		})
		if err == nil || !strings.Contains(err.Error(), "registry unreachable") {
			t.Fatalf("failed pull: %v", err)
		}
		if gitT(t, dir, "rev-parse", "HEAD") != head || readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "old\n" {
			t.Fatalf("a failed pull changed the checkout")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".starter-pull-*")); len(leftovers) != 0 {
			t.Fatalf("scratch directories left behind: %v", leftovers)
		}
	})
}

func TestWithoutSOPSLeftover(t *testing.T) {
	in := " M .sops.yaml\n?? .sops.yaml\nM  .sops.yaml\n?? install/draft.txt\n M scripts/x"
	want := "M  .sops.yaml\n?? install/draft.txt\n M scripts/x"
	if got := withoutSOPSLeftover(in); got != want {
		t.Fatalf("withoutSOPSLeftover = %q, want %q", got, want)
	}
	if got := withoutSOPSLeftover("?? .sops.yaml"); got != "" {
		t.Fatalf("only the leftover: got %q", got)
	}
}

// new-repo mode: the checkout init created itself, with nothing of the
// operator's in it.
func TestEnsureStarter_ReplacesStarterInNewRepoMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: oldStarterRef, Flux: versionedStarter("old\n"), Git: realGit(),
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), []byte("creation_rules:\n  - path_regex: x\n    age: ''\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	})
	if err != nil || !res.Extracted || readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "new\n" {
		t.Fatalf("new-repo replacement: extracted=%v err=%v", res.Extracted, err)
	}
	if tracked := gitT(t, dir, "ls-files", "age.key"); tracked != "" {
		t.Errorf("age.key was committed")
	}
}

// A failed import commit puts the old starter back: the next run starts from
// the checkout it had. An edit to a tracked .sops.yaml survives the restore.
func TestEnsureStarter_FailedReplacementRestoresStarter(t *testing.T) {
	dir := importedOperatorCheckout(t)
	gitT(t, dir, "add", ".sops.yaml")
	gitT(t, dir, "commit", "-q", "-m", "sops")
	edited := "creation_rules:\n  - path_regex: x\n    age: ''\n# edited\n"
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitT(t, dir, "rev-parse", "HEAD")
	beforeStarterCommit = func(string) error { return errors.New("commit refused") }
	t.Cleanup(func() { beforeStarterCommit = func(string) error { return nil } })
	var out strings.Builder
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true, Out: &out,
	})
	if err == nil || !strings.Contains(err.Error(), "commit refused") {
		t.Fatalf("want the commit error, got %v", err)
	}
	if !strings.Contains(out.String(), "fleet starter "+oldStarterRef+" restored") {
		t.Errorf("output:\n%s", out.String())
	}
	if gitT(t, dir, "rev-parse", "HEAD") != head || readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "old\n" {
		t.Fatalf("old starter not restored")
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != " M .sops.yaml\n" {
		t.Fatalf("status after restore: %q", st)
	}
	if readFile(t, filepath.Join(dir, ".sops.yaml")) != edited {
		t.Fatalf("the .sops.yaml edit was lost")
	}
	for _, p := range []string{"age.key", "ansible/site.yml"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s lost: %v", p, err)
		}
	}
	// The rerun succeeds.
	beforeStarterCommit = func(string) error { return nil }
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatalf("rerun after a failed replacement: %v", err)
	}
}

// A run killed between the removal and the import commit leaves the removal
// commit: the next run imports as on a first run, in either mode.
func TestEnsureStarter_ResumesAfterKilledReplacement(t *testing.T) {
	for _, operatorRepo := range []bool{true, false} {
		dir := importedOperatorCheckout(t)
		if !operatorRepo {
			// new-repo mode: only what init itself put there.
			dir = filepath.Join(t.TempDir(), "fleet")
			if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
				RepoPath: dir, Ref: oldStarterRef, Flux: versionedStarter("old\n"), Git: realGit(),
			}); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"age.key": "AGE-SECRET-KEY-1TEST\n", ".sops.yaml": "creation_rules:\n  - age: ''\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := removeStarterForReplacement(context.Background(), dir, "chore: remove fleet starter"); err != nil {
			t.Fatal(err)
		}
		_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: operatorRepo,
		})
		if err != nil || readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "new\n" {
			t.Fatalf("operatorRepo=%v: resume after a killed replacement: %v", operatorRepo, err)
		}
		if tracked := gitT(t, dir, "ls-files", "age.key"); tracked != "" {
			t.Errorf("operatorRepo=%v: age.key was committed", operatorRepo)
		}
	}
}

// A tracked age.key in a new-repo directory is still refused, never committed
// onwards.
func TestEnsureStarter_NewRepoRefusesTrackedAgeKey(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "age.key")
	gitT(t, dir, "commit", "-q", "-m", "key")
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	})
	if !errors.Is(err, ErrStarterDirNotEmpty) || !strings.Contains(err.Error(), "age.key") {
		t.Fatalf("tracked age.key: %v", err)
	}
}

// Ignored junk is neither an overlay nor a local edit.
func TestEnsureStarter_ReplacementIgnoresJunk(t *testing.T) {
	dir := importedOperatorCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".DS_Store\n__pycache__/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"clusters/.DS_Store", "scripts/.DS_Store", "scripts/__pycache__/tool.cpython-312.pyc"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("junk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeOldStarterManifest(t, dir)
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatalf("junk blocked the replacement: %v", err)
	}
	if tracked := gitT(t, dir, "ls-files", "clusters/.DS_Store", "scripts/.DS_Store"); tracked != "" {
		t.Fatalf("junk was committed: %s", tracked)
	}
}

// A file ignored only by the global excludes file would be committed by the
// adapter, which does not read it: the replacement refuses it instead.
func TestEnsureStarter_ReplacementSeesGloballyIgnoredFiles(t *testing.T) {
	dir := importedOperatorCheckout(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	excludes := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(excludes, []byte(".envrc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[core]\n\texcludesFile = "+excludes+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("export TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	})
	if !errors.Is(err, ErrStarterChangeRefused) || !strings.Contains(err.Error(), ".envrc") {
		t.Fatalf("globally ignored file: %v", err)
	}
}

// writeOldStarterManifest commits a manifest of every starter file of the
// old starter ("old\n" each).
func writeOldStarterManifest(t *testing.T, dir string) {
	t.Helper()
	sum := sha256.Sum256([]byte("old\n"))
	var b strings.Builder
	b.WriteString("# kube-dc fleet-starter manifest (format 1)\n")
	for _, f := range strings.Fields(gitT(t, dir, append([]string{"ls-files", "--"}, starterManifestTrees...)...)) {
		b.WriteString("644 " + hex.EncodeToString(sum[:]) + " " + f + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, ".starter-manifest"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".starter-manifest")
	gitT(t, dir, "commit", "-q", "-m", "manifest")
}

// The import commit in a checkout with history takes the starter's paths
// only: a file listed in .git/info/exclude is not committed.
func TestEnsureStarter_ImportCommitTakesStarterPathsOnly(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("site\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "site")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("local.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: oldStarterRef, Flux: versionedStarter("old\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatal(err)
	}
	if tracked := gitT(t, dir, "ls-files", "local.env"); tracked != "" {
		t.Fatalf("an excluded file was committed")
	}
	if tracked := gitT(t, dir, "ls-files", "scripts/tool.py", ".starter-version"); !strings.Contains(tracked, "scripts/tool.py") ||
		!strings.Contains(tracked, ".starter-version") {
		t.Fatalf("starter files not committed: %q", tracked)
	}
}

// Ignored files in the starter trees other than litter are the operator's:
// refused, not deleted.
func TestEnsureStarter_ReplacementRefusesIgnoredOperatorFiles(t *testing.T) {
	dir := importedOperatorCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("*.dec.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "platform", "x.dec.yaml")
	if err := os.WriteFile(secret, []byte("password: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	})
	if !errors.Is(err, ErrStarterChangeRefused) || !strings.Contains(err.Error(), "platform/x.dec.yaml") {
		t.Fatalf("ignored operator file: %v", err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("the operator's file was deleted: %v", err)
	}
}

// A replacement stopped between `git rm` and its commit leaves staged
// deletions; the next run imports the requested starter.
func TestEnsureStarter_ResumesAfterInterruptedRemoval(t *testing.T) {
	dir := importedOperatorCheckout(t)
	gitT(t, dir, append([]string{"rm", "-r", "-q", "--ignore-unmatch", "--"}, starterOwnedNames()...)...)
	for _, name := range starterOwnedNames() {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatalf("resume after an interrupted removal: %v", err)
	}
	if readFile(t, filepath.Join(dir, "scripts", "tool.py")) != "new\n" {
		t.Fatalf("new starter not imported")
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "?? .sops.yaml\n" {
		t.Fatalf("status: %q", st)
	}
}

// new-repo: an age key that a later .gitignore rule re-includes is refused.
func TestEnsureStarter_NewRepoRefusesUnignoredAgeKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("age.key\n!*.key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
			RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
		})
		if !errors.Is(err, ErrStarterDirNotEmpty) || !strings.Contains(err.Error(), "does not ignore") {
			t.Fatalf("run %d, re-included age.key: %v", run, err)
		}
		if StarterShapePresent(dir) {
			t.Fatalf("run %d left the starter in place", run)
		}
		if gitTracksAgeKey(dir) {
			t.Fatalf("run %d committed age.key", run)
		}
	}
	// Fixed .gitignore: the import goes through and the key stays out.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("age.key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	}); err != nil || gitTracksAgeKey(dir) {
		t.Fatalf("after the fix: err=%v tracked=%v", err, gitTracksAgeKey(dir))
	}
}

// The repair commit for a starter left uncommitted checks the key too.
func TestEnsureStarter_RepairCommitRefusesUnignoredAgeKey(t *testing.T) {
	dir := t.TempDir()
	for _, f := range testStarterFiles(".gitignore") {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("age.key\n!*.key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{RepoPath: dir, Ref: newStarterRef, Git: realGit()})
	if !errors.Is(err, ErrStarterDirNotEmpty) || gitTracksAgeKey(dir) {
		t.Fatalf("repair commit with a re-included age.key: err=%v tracked=%v", err, gitTracksAgeKey(dir))
	}
}

// new-repo with history (a clone with one commit): preseeded files are
// committed with the starter, as a new repository's commit takes them.
func TestEnsureStarter_NewRepoWithHistoryCommitsPreseeds(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("site\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "init")
	for _, p := range []string{"docs/design.md", "LICENSE"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A directory that stages nothing (empty) must not fail the commit.
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	}); err != nil {
		t.Fatal(err)
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("left uncommitted:\n%s", st)
	}
}

// A directory inside another repository's working tree is a new repository,
// not the parent's history.
func TestEnsureStarter_DirectoryInsideAnotherRepository(t *testing.T) {
	parent := t.TempDir()
	gitT(t, parent, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(parent, "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "add", "-A")
	gitT(t, parent, "commit", "-q", "-m", "parent")
	dir := filepath.Join(parent, "fleet")
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	}); err != nil {
		t.Fatal(err)
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("left uncommitted:\n%s", st)
	}
	if out := gitT(t, parent, "log", "--oneline"); strings.Count(out, "\n") != 1 {
		t.Fatalf("the parent repository got a commit:\n%s", out)
	}
}

// An operator .gitignore that ignores named paths (.starter-version) does not
// fail the import: those paths are left out, as the adapter skipped them.
func TestEnsureStarter_ImportSkipsPathsTheOperatorIgnores(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".*\n!.gitignore\nage.key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "site")
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatalf("import with ignored named paths: %v", err)
	}
	if !strings.Contains(gitT(t, dir, "ls-files", "scripts/tool.py"), "scripts/tool.py") {
		t.Fatalf("starter not committed")
	}
}

// An interrupted removal whose new starter drops a top-level entry commits
// that deletion too.
func TestEnsureStarter_InterruptedRemovalOfADroppedEntry(t *testing.T) {
	dir := importedOperatorCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, ".starter-manifest"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".starter-manifest")
	gitT(t, dir, "commit", "-q", "-m", "manifest")
	gitT(t, dir, append([]string{"rm", "-r", "-q", "--ignore-unmatch", "--"}, starterOwnedNames()...)...)
	for _, name := range starterOwnedNames() {
		os.RemoveAll(filepath.Join(dir, name))
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(), OperatorRepository: true,
	}); err != nil {
		t.Fatal(err)
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "?? .sops.yaml\n" {
		t.Fatalf("status: %q", st)
	}
}

// A failed replacement also removes top-level entries outside the
// starter-owned set that the new starter brought.
func TestEnsureStarter_RestoreRemovesNewTopLevelEntries(t *testing.T) {
	dir := importedOperatorCheckout(t)
	puller := versionedStarter("new\n")
	puller.files = append(puller.files, "Makefile")
	beforeStarterCommit = func(string) error { return errors.New("commit refused") }
	t.Cleanup(func() { beforeStarterCommit = func(string) error { return nil } })
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: puller, Git: realGit(), OperatorRepository: true,
	}); err == nil {
		t.Fatal("want the commit error")
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
		t.Fatalf("the new starter's Makefile was left behind")
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "?? .sops.yaml\n" {
		t.Fatalf("status after restore: %q", st)
	}
}

// new-repo with history and an empty docs/: the import still commits.
func TestEnsureStarter_NewRepoWithHistoryAndEmptyDocs(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("site\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: newStarterRef, Flux: versionedStarter("new\n"), Git: realGit(),
	}); err != nil {
		t.Fatalf("empty docs/: %v", err)
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("left uncommitted:\n%s", st)
	}
}
