package git

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// No hooks for the test's own commands: a hook marker then proves that an
	// adapter command ran one.
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Env = gitEnv() // a test may set GIT_DIR and friends for the adapter
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

// at is the current branch with sha as the expected commit.
func at(t *testing.T, dir, sha string) ports.Checkout {
	t.Helper()
	co, err := New().Checkout(context.Background(), dir)
	if err != nil || co.Branch == "" {
		t.Fatalf("checkout: %+v %v", co, err)
	}
	co.SHA = sha
	return co
}

// hooks installs hooks that leave a marker when they run.
func hooks(t *testing.T, dir string) (marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "hook-ran")
	for _, h := range []string{"pre-commit", "post-commit", "post-checkout", "post-index-change", "reference-transaction"} {
		writeFile(t, dir, ".git/hooks/"+h, "#!/bin/sh\necho "+h+" >> "+marker+"\n", 0o755)
	}
	return marker
}

func isolate(t *testing.T, dir, sha string) string {
	t.Helper()
	work, discard, err := New().Isolate(context.Background(), dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = discard() })
	return work
}

// abandonRun leaves what a killed run leaves: a private checkout with its
// registration, and its lock file with nobody holding the lock.
func abandonRun(t *testing.T, dir, sha string) string {
	t.Helper()
	base := filepath.Join(gitOut(t, dir, "rev-parse", "--absolute-git-dir"), "kube-dc-init")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "run-killed")
	gitOut(t, dir, "worktree", "add", "--detach", "--quiet", work, sha)
	writeFile(t, work, "clusters/x/secrets.enc.yaml", "plaintext left by a killed run\n", 0o600)
	if err := os.WriteFile(work+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return work
}

func TestIsolate_PrivateCheckoutAndDiscard(t *testing.T) {
	dir, pre := initLocalRepo(t)
	writeFile(t, dir, "untracked-in-the-fleet.txt", "mine\n", 0o644)
	marker := hooks(t, dir)
	work, discard, err := New().Isolate(context.Background(), dir, pre)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, work, "README.md") != "seed\n" {
		t.Fatal("the private checkout does not hold the commit's files")
	}
	if readFile(t, work, "untracked-in-the-fleet.txt") != "<absent>" {
		t.Fatal("the private checkout holds an untracked file of the fleet checkout")
	}
	if fi, err := os.Stat(filepath.Dir(work)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the private checkout's directory must be 0700: %v %v", fi.Mode(), err)
	}
	if !strings.HasPrefix(work, filepath.Join(dir, ".git", "kube-dc-init")+string(filepath.Separator)) {
		t.Fatalf("the private checkout %s must be inside the repository's git directory", work)
	}
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != pre {
		t.Fatalf("private checkout at %s, want %s", got, pre)
	}
	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("the private checkout must be detached, got %q", got)
	}
	if err := discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "kube-dc-init")); !os.IsNotExist(err) {
		t.Fatalf("the private checkout was not removed: %v", err)
	}
	if n := len(strings.Split(gitOut(t, dir, "worktree", "list"), "\n")); n != 1 {
		t.Fatalf("worktree metadata left behind (%d entries)", n)
	}
	if err := discard(); err != nil {
		t.Fatalf("a second discard must be harmless: %v", err)
	}
	if b, _ := os.ReadFile(marker); len(b) != 0 {
		t.Fatalf("repository hooks ran: %s", b)
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "?? untracked-in-the-fleet.txt" {
		t.Fatalf("the fleet checkout changed:\n%s", st)
	}
}

func TestCommitIsolated(t *testing.T) {
	dir, _ := initLocalRepo(t)
	writeFile(t, dir, ".gitignore", "*.dec.yaml\nage.key\n", 0o644)
	gitOut(t, dir, "add", ".gitignore")
	gitOut(t, dir, "commit", "-q", "-m", "ignore rules")
	pre := gitOut(t, dir, "rev-parse", "HEAD")
	// Someone's work in the fleet checkout: it must play no part.
	writeFile(t, dir, "addons/local-notes.txt", "notes\n", 0o644)
	writeFile(t, dir, "staged-draft.txt", "draft\n", 0o644)
	gitOut(t, dir, "add", "staged-draft.txt")
	before := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all")
	marker := hooks(t, dir)
	work := isolate(t, dir, pre)
	ctx := context.Background()

	if _, err := New().CommitIsolated(ctx, dir, work, "msg", pre, nil); !errors.Is(err, ports.ErrNothingToCommit) {
		t.Fatalf("want ErrNothingToCommit, got %v", err)
	}
	writeFile(t, work, "c/out.yaml", "run\n", 0o644)
	writeFile(t, work, "c/secret.dec.yaml", "plaintext\n", 0o644)
	writeFile(t, work, "age.key", "AGE-SECRET-KEY\n", 0o600)
	writeFile(t, work, "bin/run.sh", "#!/bin/sh\n", 0o755)
	if err := os.Remove(filepath.Join(work, "README.md")); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("refused")
	if _, err := New().CommitIsolated(ctx, dir, work, "msg", pre, func(ports.CommitTrees) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("want the check's error, got %v", err)
	}
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != pre {
		t.Fatal("a refused commit moved the private checkout")
	}
	if _, err := New().CommitIsolated(ctx, dir, work, "msg", strings.Repeat("0", 39)+"1", nil); err == nil {
		t.Fatal("a commit on a parent that is not the private checkout's commit must be refused")
	}

	var checked bool
	sha, err := New().CommitIsolated(ctx, dir, work, "first line\n\nbody", pre, func(tr ports.CommitTrees) error {
		checked = true
		if data, present, err := tr.Candidate("c/out.yaml"); err != nil || !present || string(data) != "run\n" {
			t.Errorf("candidate c/out.yaml: %q %v %v", data, present, err)
		}
		if data, present, err := tr.Candidate("bin/run.sh"); err != nil || !present || string(data) != "#!/bin/sh\n" {
			t.Errorf("an executable file must read as a regular file: %q %v %v", data, present, err)
		}
		for _, ignored := range []string{"c/secret.dec.yaml", "age.key"} {
			if _, present, _ := tr.Candidate(ignored); present {
				t.Errorf("ignored %s is in the candidate tree", ignored)
			}
		}
		if _, present, _ := tr.Candidate("README.md"); present {
			t.Error("a deleted file is still in the candidate tree")
		}
		if data, present, err := tr.Parent("README.md"); err != nil || !present || string(data) != "seed\n" {
			t.Errorf("parent README.md: %q %v %v", data, present, err)
		}
		if _, present, err := tr.Candidate("c"); !present || err == nil {
			t.Errorf("a directory must be reported, not read as a file: %v %v", present, err)
		}
		return nil
	})
	if err != nil || !checked {
		t.Fatalf("CommitIsolated: %v (checked=%v)", err, checked)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != ".gitignore\nbin/run.sh\nc/out.yaml" {
		t.Fatalf("tree:\n%s", got)
	}
	if got := gitOut(t, dir, "ls-tree", sha, "--", "bin/run.sh"); !strings.HasPrefix(got, "100755 blob") {
		t.Fatalf("mode: %s", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%P|%an|%ae|%s", sha); got != pre+"|kube-dc bootstrap|bootstrap@kube-dc.invalid|first line" {
		t.Fatalf("commit: %s", got)
	}
	// A second commit stacks on the first.
	writeFile(t, work, "c/second.yaml", "run\n", 0o644)
	sha2, err := New().CommitIsolated(ctx, dir, work, "second", sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", sha2+"^"); got != sha {
		t.Fatalf("second commit's parent is %s, want %s", got, sha)
	}
	// The fleet checkout: same branch tip, same index, same worktree.
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != pre {
		t.Fatalf("the fleet branch moved to %s", got)
	}
	if after := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Fatalf("the fleet checkout changed:\n%s\nwas:\n%s", after, before)
	}
	if b, _ := os.ReadFile(marker); len(b) != 0 {
		t.Fatalf("repository hooks ran: %s", b)
	}
	gitOut(t, dir, "fsck", "--strict", "--no-dangling")
}

func bareRemote(t *testing.T, dir string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, dir, "remote", "add", "origin", bare)
	return bare
}

// PushCommit pushes the named commit to the named branch, whatever HEAD is,
// and by ID: a commit landing on the local branch meanwhile is not pushed.
func TestPushCommit_ExplicitBranchAndCommit(t *testing.T) {
	dir, pre := initLocalRepo(t)
	bare := bareRemote(t, dir)
	base := at(t, dir, pre)
	work := isolate(t, dir, pre)
	writeFile(t, work, "f", "x\n", 0o644)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "checkout", "-q", "-b", "other")
	c := New()
	c.afterRemoteSelection = func() { gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "later, on other") }
	if err := c.PushCommit(context.Background(), dir, "", ports.Checkout{Branch: base.Branch, SHA: sha}, nil); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, bare, "rev-parse", base.Branch); got != sha {
		t.Fatalf("remote %s=%s want %s", base.Branch, got, sha)
	}
	if out, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "-q", "other").Output(); err == nil {
		t.Fatalf("other was pushed: %s", out)
	}
}

func TestCheckout_BranchAndDetached(t *testing.T) {
	dir, pre := initLocalRepo(t)
	co, err := New().Checkout(context.Background(), dir)
	if err != nil || co.SHA != pre || !strings.HasPrefix(co.Branch, "refs/heads/") {
		t.Fatalf("on a branch: %+v %v", co, err)
	}
	gitOut(t, dir, "checkout", "-q", "--detach")
	co, err = New().Checkout(context.Background(), dir)
	if err != nil || co.SHA != pre || co.Branch != "" {
		t.Fatalf("detached: %+v %v", co, err)
	}
}

func TestAdvance(t *testing.T) {
	ctx := context.Background()
	// setup: a fleet checkout at pre, and a commit `to` made in a private
	// checkout that adds c/a.yaml and c/b.yaml and edits README.md.
	setup := func(t *testing.T) (dir, pre, to string) {
		dir, pre = initLocalRepo(t)
		work := isolate(t, dir, pre)
		writeFile(t, work, "c/a.yaml", "run\n", 0o644)
		writeFile(t, work, "c/b.yaml", "run\n", 0o644)
		writeFile(t, work, "README.md", "run edit\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "run", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		return dir, pre, to
	}
	t.Run("fast-forwards branch, index and worktree; unrelated work is left", func(t *testing.T) {
		dir, pre, to := setup(t)
		marker := hooks(t, dir)
		writeFile(t, dir, "notes.txt", "mine\n", 0o644)
		writeFile(t, dir, "c/operator-notes.txt", "mine, next to the new files\n", 0o644)
		writeFile(t, dir, "draft.txt", "staged\n", 0o644)
		gitOut(t, dir, "add", "draft.txt")
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err != nil {
			t.Fatal(err)
		}
		if got := gitOut(t, dir, "rev-parse", "HEAD"); got != to {
			t.Fatalf("HEAD=%s want %s", got, to)
		}
		if st := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all"); st != "A  draft.txt\n?? c/operator-notes.txt\n?? notes.txt" {
			t.Fatalf("status:\n%s", st)
		}
		if readFile(t, dir, "c/a.yaml") != "run\n" || readFile(t, dir, "README.md") != "run edit\n" {
			t.Fatal("the new commit's files are not in the worktree")
		}
		if b, _ := os.ReadFile(marker); len(b) != 0 {
			t.Fatalf("repository hooks ran: %s", b)
		}
	})
	refused := func(name string, prepare func(t *testing.T, dir string), intact func(t *testing.T, dir string)) {
		t.Run("refuses, changing nothing: "+name, func(t *testing.T) {
			dir, pre, to := setup(t)
			base := at(t, dir, pre)
			prepare(t, dir)
			before := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all", "--ignored")
			head := gitOut(t, dir, "rev-parse", "HEAD")
			if err := New().Advance(ctx, dir, base, to); err == nil {
				t.Fatal("want a refusal")
			}
			if got := gitOut(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD moved to %s", got)
			}
			if got := gitOut(t, dir, "rev-parse", base.Branch); got != pre && name != "the branch moved" {
				t.Fatalf("%s moved to %s", base.Branch, got)
			}
			if after := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all", "--ignored"); after != before {
				t.Fatalf("the checkout changed:\n%s\nwas:\n%s", after, before)
			}
			intact(t, dir)
		})
	}
	refused("an untracked file where the commit adds one",
		func(t *testing.T, dir string) { writeFile(t, dir, "c/a.yaml", "mine\n", 0o644) },
		func(t *testing.T, dir string) {
			if readFile(t, dir, "c/a.yaml") != "mine\n" {
				t.Fatal("the untracked file was overwritten")
			}
		})
	refused("an IGNORED file where the commit adds one",
		func(t *testing.T, dir string) {
			writeFile(t, dir, ".git/info/exclude", "c/a.yaml\n", 0o644)
			writeFile(t, dir, "c/a.yaml", "mine, ignored\n", 0o644)
		},
		func(t *testing.T, dir string) {
			if readFile(t, dir, "c/a.yaml") != "mine, ignored\n" {
				t.Fatal("the ignored file was overwritten")
			}
		})
	refused("a file where the commit adds a directory",
		func(t *testing.T, dir string) { writeFile(t, dir, "c", "a file named c\n", 0o644) },
		func(t *testing.T, dir string) {
			if readFile(t, dir, "c") != "a file named c\n" {
				t.Fatal("the file was replaced by a directory")
			}
		})
	refused("a local edit of a file the commit changes",
		func(t *testing.T, dir string) { writeFile(t, dir, "README.md", "my edit\n", 0o644) },
		func(t *testing.T, dir string) {
			if readFile(t, dir, "README.md") != "my edit\n" {
				t.Fatal("the local edit was overwritten")
			}
		})
	refused("a staged draft of a file the commit adds",
		func(t *testing.T, dir string) {
			writeFile(t, dir, "c/b.yaml", "draft\n", 0o644)
			gitOut(t, dir, "add", "c/b.yaml")
		},
		func(t *testing.T, dir string) {
			if gitOut(t, dir, "show", ":c/b.yaml") != "draft" {
				t.Fatal("the staged draft was replaced")
			}
		})
	refused("the branch moved",
		func(t *testing.T, dir string) { gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "operator") },
		func(t *testing.T, dir string) {})
	refused("the checkout switched to another branch at the same commit",
		func(t *testing.T, dir string) { gitOut(t, dir, "checkout", "-q", "-b", "other") },
		func(t *testing.T, dir string) {
			if readFile(t, dir, "c/a.yaml") != "<absent>" {
				t.Fatal("the other branch's worktree received the files")
			}
		})
	t.Run("refuses a commit that does not descend from the checkout's", func(t *testing.T) {
		dir, pre, _ := setup(t)
		gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "next")
		next := gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		writeFile(t, work, "side.yaml", "x\n", 0o644)
		side, err := New().CommitIsolated(ctx, dir, work, "side", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := New().Advance(ctx, dir, at(t, dir, next), side); err == nil {
			t.Fatal("want a refusal")
		}
		if gitOut(t, dir, "rev-parse", "HEAD") != next || readFile(t, dir, "side.yaml") != "<absent>" {
			t.Fatal("the checkout changed")
		}
	})
}

// GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE inherited from the operator's
// shell must not send the private checkout's staging to the fleet checkout.
func TestIsolated_IgnoresInheritedGitEnvironment(t *testing.T) {
	dir, pre := initLocalRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	t.Setenv("GIT_WORK_TREE", dir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(dir, ".git", "index"))
	work := isolate(t, dir, pre)
	writeFile(t, dir, "draft.txt", "the operator's unstaged draft\n", 0o644)
	writeFile(t, work, "out.yaml", "run\n", 0o644)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != "README.md\nout.yaml" {
		t.Fatalf("the commit was built from the wrong worktree:\n%s", got)
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "?? draft.txt" {
		t.Fatalf("the fleet checkout's index or worktree was used:\n%s", st)
	}
}

// The ignore policy is the starting checkout's: a global excludes file that
// applies only on its branch (includeIf onbranch) still applies to the
// detached private checkout.
func TestCommitIsolated_UsesTheStartingCheckoutsIgnorePolicy(t *testing.T) {
	dir, pre := initLocalRepo(t)
	branch := strings.TrimPrefix(at(t, dir, pre).Branch, "refs/heads/")
	conf := t.TempDir()
	writeFile(t, conf, "excludes", "*.dec.yaml\n", 0o644)
	writeFile(t, conf, "onbranch.inc", "[core]\n\texcludesFile = "+filepath.Join(conf, "excludes")+"\n", 0o644)
	gitOut(t, dir, "config", "includeIf.onbranch:"+branch+".path", filepath.Join(conf, "onbranch.inc"))
	work := isolate(t, dir, pre)
	writeFile(t, work, "c/out.yaml", "run\n", 0o644)
	writeFile(t, work, "c/sample.dec.yaml", "decrypted\n", 0o644)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != "README.md\nc/out.yaml" {
		t.Fatalf("a file the starting branch ignores was committed:\n%s", got)
	}
}

// Discarding a private checkout removes its own registration only: another
// worktree whose directory is absent right now (renamed, unmounted) keeps its
// registration and its index.
func TestIsolate_DiscardLeavesOtherWorktreesAlone(t *testing.T) {
	dir, pre := initLocalRepo(t)
	other := filepath.Join(t.TempDir(), "other")
	gitOut(t, dir, "worktree", "add", "-q", "-b", "side", other, pre)
	writeFile(t, other, "draft.txt", "staged in the other worktree\n", 0o644)
	gitOut(t, other, "add", "draft.txt")
	if err := os.Rename(other, other+".away"); err != nil {
		t.Fatal(err)
	}
	work, discard, err := New().Isolate(context.Background(), dir, pre)
	if err != nil {
		t.Fatal(err)
	}
	_ = work
	if err := discard(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other+".away", other); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, other, "status", "--porcelain"); got != "A  draft.txt" {
		t.Fatalf("the other worktree lost its registration or index:\n%s", got)
	}
}

// A run that was killed could not discard its checkout: the next run removes
// it (it may hold a plaintext secret), with its registration.
func TestIsolate_SweepsAbandonedRuns(t *testing.T) {
	dir, pre := initLocalRepo(t)
	abandoned := abandonRun(t, dir, pre)
	// Not discarded while the test runs: this process holds its lock. The
	// discard function is kept (and called at the end): it is what holds the
	// lock file open, and one the test dropped could be collected, which
	// releases the lock and makes this run look abandoned too.
	live, discardLive, err := New().Isolate(context.Background(), dir, pre)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = discardLive() })
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("the abandoned checkout survived: %v", err)
	}
	if _, err := os.Stat(abandoned + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("the abandoned run's lock file survived: %v", err)
	}
	next, discard, err := New().Isolate(context.Background(), dir, pre)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("a live run's checkout was swept: %v", err)
	}
	list := gitOut(t, dir, "worktree", "list")
	if strings.Contains(list, "run-killed") || !strings.Contains(list, filepath.Base(next)) || !strings.Contains(list, filepath.Base(live)) {
		t.Fatalf("worktree registrations:\n%s", list)
	}
	if err := discard(); err != nil {
		t.Fatal(err)
	}
}

// A fetch refspec that writes into refs/heads/ lets a push move a local
// branch without updating its checkout.
func TestPushCommit_RefusesFetchMappingIntoLocalBranches(t *testing.T) {
	dir, pre := initLocalRepo(t)
	bare := bareRemote(t, dir)
	base := at(t, dir, pre)
	gitOut(t, dir, "config", "remote.origin.fetch", "+"+base.Branch+":"+base.Branch)
	err := New().PushCommit(context.Background(), dir, "", base, nil)
	if err == nil || !strings.Contains(err.Error(), "writes into local branches") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if out, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "-q", base.Branch).Output(); err == nil {
		t.Fatalf("something was pushed: %s", out)
	}
}

func TestAdvance_OperatorStateSurvivesARefusal(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, files map[string]string) (dir, pre, to string) {
		dir, pre = initLocalRepo(t)
		writeFile(t, dir, "z/keep", "tracked\n", 0o644)
		gitOut(t, dir, "add", "z/keep")
		gitOut(t, dir, "commit", "-q", "-m", "z")
		pre = gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		for rel, body := range files {
			writeFile(t, work, rel, body, 0o644)
		}
		to, err := New().CommitIsolated(ctx, dir, work, "run", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		return dir, pre, to
	}
	t.Run("an edit staged with the incoming content is refused, not consumed", func(t *testing.T) {
		dir, pre, to := setup(t, map[string]string{"README.md": "run edit\n", "c/b.yaml": "run\n"})
		base := at(t, dir, pre)
		// The same content the commit brings: Git itself would accept it, and
		// the staged change would vanish from `git diff --cached`.
		writeFile(t, dir, "README.md", "run edit\n", 0o644)
		gitOut(t, dir, "add", "README.md")
		err := New().Advance(ctx, dir, base, to)
		if err == nil || !strings.Contains(err.Error(), "README.md has a staged change") {
			t.Fatalf("want a refusal naming the staged path, got %v", err)
		}
		if gitOut(t, dir, "diff", "--cached", "--name-only") != "README.md" {
			t.Fatal("the staged change is gone")
		}
		if got := gitOut(t, dir, "rev-parse", base.Branch); got != pre {
			t.Fatalf("the branch was not put back: %s", got)
		}
		if got := gitOut(t, dir, "show", ":README.md"); got != "run edit" {
			t.Fatalf("the operator's staged edit was rewound: %q", got)
		}
		if readFile(t, dir, "README.md") != "run edit\n" || readFile(t, dir, "c/b.yaml") != "<absent>" {
			t.Fatal("the operator's worktree changed")
		}
	})
	t.Run("a tracked file deleted locally is not brought back", func(t *testing.T) {
		dir, pre, to := setup(t, map[string]string{"README.md": "run edit\n"})
		base := at(t, dir, pre)
		if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
			t.Fatal(err)
		}
		err := New().Advance(ctx, dir, base, to)
		if err == nil || !strings.Contains(err.Error(), "README.md is deleted locally") {
			t.Fatalf("want a refusal naming the file, got %v", err)
		}
		if readFile(t, dir, "README.md") != "<absent>" || gitOut(t, dir, "rev-parse", "HEAD") != pre {
			t.Fatal("the deletion was reversed or the branch moved")
		}
	})
	t.Run("a checkout that fails part-way says what it wrote", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir, pre, to := setup(t, map[string]string{"a/out": "run\n", "z/out": "run\n"})
		base := at(t, dir, pre)
		if err := os.Chmod(filepath.Join(dir, "z"), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "z"), 0o755) })
		err := New().Advance(ctx, dir, base, to)
		if err == nil || !strings.Contains(err.Error(), "part-way") || !strings.Contains(err.Error(), "a/out") {
			t.Fatalf("want a part-way failure naming a/out, got %v", err)
		}
		if got := gitOut(t, dir, "rev-parse", base.Branch); got != pre {
			t.Fatalf("the branch was not put back: %s", got)
		}
	})
}

// An ignore policy supplied through Git's configuration environment
// (GIT_CONFIG_COUNT/KEY/VALUE) is the starting checkout's policy too.
func TestCommitIsolated_HonoursIgnorePolicyFromTheConfigEnvironment(t *testing.T) {
	dir, pre := initLocalRepo(t)
	excludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(excludes, []byte("*.canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.excludesFile")
	t.Setenv("GIT_CONFIG_VALUE_0", excludes)
	work := isolate(t, dir, pre)
	writeFile(t, work, "c/out.yaml", "run\n", 0o644)
	writeFile(t, work, "c/local.canary", "must stay local\n", 0o644)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != "README.md\nc/out.yaml" {
		t.Fatalf("a file the operator's configuration ignores was committed:\n%s", got)
	}
}

// A filesystem-monitor program is a hook by another name: index-reading
// commands would run it.
func TestIsolated_DoesNotRunTheFsmonitorProgram(t *testing.T) {
	dir, pre := initLocalRepo(t)
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ran >> "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "config", "core.fsmonitor", hook)
	work := isolate(t, dir, pre)
	writeFile(t, work, "c/a.yaml", "run\n", 0o644)
	to, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := New().Advance(context.Background(), dir, at(t, dir, pre), to); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(marker); len(b) != 0 {
		t.Fatalf("the fsmonitor program ran: %s", b)
	}
}

// The operator's checkout may itself be a linked worktree: its branch lives
// in the common Git directory.
func TestIsolated_LinkedWorktreeAsTheFleetCheckout(t *testing.T) {
	main, pre := initLocalRepo(t)
	dir := filepath.Join(t.TempDir(), "linked")
	gitOut(t, main, "worktree", "add", "-q", "-b", "fleet", dir, pre)
	ctx := context.Background()
	co, err := New().Checkout(ctx, dir)
	if err != nil || co.Branch != "refs/heads/fleet" || co.SHA != pre {
		t.Fatalf("checkout of a linked worktree: %+v %v", co, err)
	}
	work, discard, err := New().Isolate(ctx, dir, pre)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "c/a.yaml", "run\n", 0o644)
	to, err := New().CommitIsolated(ctx, dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := New().Advance(ctx, dir, co, to); err != nil {
		t.Fatal(err)
	}
	if err := discard(); err != nil {
		t.Fatal(err)
	}
	if gitOut(t, dir, "rev-parse", "HEAD") != to || readFile(t, dir, "c/a.yaml") != "run\n" || gitOut(t, dir, "status", "--porcelain") != "" {
		t.Fatal("the linked worktree was not fast-forwarded cleanly")
	}
	if gitOut(t, main, "rev-parse", "HEAD") != pre || readFile(t, main, "c/a.yaml") != "<absent>" {
		t.Fatal("the main worktree was touched")
	}
}

func TestAdvance_NamesAndPartialWrites(t *testing.T) {
	ctx := context.Background()
	t.Run("a locally deleted file whose name starts with a space", func(t *testing.T) {
		dir, _ := initLocalRepo(t)
		writeFile(t, dir, " note", "tracked\n", 0o644)
		gitOut(t, dir, "add", "--", " note")
		gitOut(t, dir, "commit", "-q", "-m", "a name with a leading space")
		pre := gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		writeFile(t, work, " note", "run edit\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "run", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, " note")); err != nil {
			t.Fatal(err)
		}
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err == nil || !strings.Contains(err.Error(), "deleted locally") {
			t.Fatalf("want a refusal, got %v", err)
		}
		if readFile(t, dir, " note") != "<absent>" {
			t.Fatal("the deleted file was brought back")
		}
	})
	t.Run("a part-way failure names a MODIFIED file it wrote", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir, _ := initLocalRepo(t)
		writeFile(t, dir, "a/file", "old\n", 0o644)
		writeFile(t, dir, "z/keep", "tracked\n", 0o644)
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "commit", "-q", "-m", "base")
		pre := gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		writeFile(t, work, "a/file", "new\n", 0o644)
		writeFile(t, work, "z/out", "run\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "run", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		base := at(t, dir, pre)
		if err := os.Chmod(filepath.Join(dir, "z"), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "z"), 0o755) })
		err = New().Advance(ctx, dir, base, to)
		if !errors.Is(err, ports.ErrPartialCheckout) || !strings.Contains(err.Error(), "a/file") {
			t.Fatalf("want ErrPartialCheckout naming a/file, got %v", err)
		}
		if got := gitOut(t, dir, "rev-parse", base.Branch); got != pre {
			t.Fatalf("the branch was not put back: %s", got)
		}
	})
}

// A mirror fetch refspec maps the pushed branch onto the local branch too.
func TestPushCommit_RefusesMirrorFetchRefspec(t *testing.T) {
	dir, pre := initLocalRepo(t)
	bareRemote(t, dir)
	gitOut(t, dir, "config", "remote.origin.fetch", "+refs/*:refs/*")
	if err := New().PushCommit(context.Background(), dir, "", at(t, dir, pre), nil); err == nil || !strings.Contains(err.Error(), "writes into local branches") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// A process killed after the branch moved and before the files did: the next
// run finishes the fast-forward, and refuses where local work overlaps.
func TestResumeAdvance(t *testing.T) {
	ctx := context.Background()
	marker := func(dir string) string { return filepath.Join(dir, ".git", "kube-dc-init-advance") }
	// interrupted: Advance was killed right after it moved the branch.
	interrupted := func(t *testing.T) (dir, pre, to string, base ports.Checkout) {
		dir, pre = initLocalRepo(t)
		work := isolate(t, dir, pre)
		writeFile(t, work, "c/a.yaml", "run\n", 0o644)
		writeFile(t, work, "README.md", "run edit\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "run", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		base = at(t, dir, pre)
		writeFile(t, dir, ".git/kube-dc-init-advance", base.Branch+"\n"+pre+"\n"+to+"\n"+indexChecksum(ctx, dir)+"\n", 0o600)
		gitOut(t, dir, "update-ref", base.Branch, to, pre)
		return dir, pre, to, base
	}
	t.Run("finishes it while the index is exactly as it was", func(t *testing.T) {
		dir, _, to, _ := interrupted(t)
		writeFile(t, dir, "notes.txt", "unrelated\n", 0o644)
		resumed, err := New().ResumeAdvance(ctx, dir)
		if err != nil || !resumed {
			t.Fatalf("resumed=%v err=%v", resumed, err)
		}
		if gitOut(t, dir, "rev-parse", "HEAD") != to || readFile(t, dir, "c/a.yaml") != "run\n" || gitOut(t, dir, "status", "--porcelain") != "?? notes.txt" {
			t.Fatalf("the checkout was not brought to the branch: %q", gitOut(t, dir, "status", "--porcelain"))
		}
		if resumed, err := New().ResumeAdvance(ctx, dir); resumed || err != nil {
			t.Fatalf("a second call must find nothing: %v %v", resumed, err)
		}
	})
	t.Run("refuses over local work, and can be retried", func(t *testing.T) {
		dir, _, _, _ := interrupted(t)
		writeFile(t, dir, "c/a.yaml", "mine\n", 0o644)
		resumed, err := New().ResumeAdvance(ctx, dir)
		if err == nil || !resumed || !strings.Contains(err.Error(), "c/a.yaml") {
			t.Fatalf("want a refusal naming the file, got resumed=%v err=%v", resumed, err)
		}
		if readFile(t, dir, "c/a.yaml") != "mine\n" {
			t.Fatal("local work was overwritten")
		}
		if err := os.Remove(filepath.Join(dir, "c", "a.yaml")); err != nil {
			t.Fatal(err)
		}
		if resumed, err := New().ResumeAdvance(ctx, dir); err != nil || !resumed {
			t.Fatalf("retry: resumed=%v err=%v", resumed, err)
		}
		if readFile(t, dir, "c/a.yaml") != "run\n" {
			t.Fatal("the retry did not finish the fast-forward")
		}
	})
	t.Run("leaves a checkout whose index changed since to its owner", func(t *testing.T) {
		dir, _, _, _ := interrupted(t)
		writeFile(t, dir, "mine.txt", "staged after the interruption\n", 0o644)
		gitOut(t, dir, "add", "mine.txt")
		resumed, err := New().ResumeAdvance(ctx, dir)
		if err == nil || !resumed || !strings.Contains(err.Error(), "git read-tree -m -u") {
			t.Fatalf("want an error that hands the checkout to its owner, got resumed=%v err=%v", resumed, err)
		}
		if readFile(t, dir, "c/a.yaml") != "<absent>" || readFile(t, dir, "README.md") != "seed\n" || gitOut(t, dir, "diff", "--cached", "--name-only", "HEAD~1") != "mine.txt" {
			t.Fatal("the checkout was changed")
		}
		if _, serr := os.Stat(marker(dir)); !os.IsNotExist(serr) {
			t.Fatal("the marker must go, so the next run reports the state through its normal checks")
		}
	})
	// The fast-forward had completed; only the marker was left. Later the
	// operator stages a revert of a file the commit changed.
	t.Run("does not undo a revert staged after a completed fast-forward", func(t *testing.T) {
		dir, pre, to, base := interrupted(t)
		gitOut(t, dir, "read-tree", "-m", "-u", pre, to)   // the merge the killed process did complete
		gitOut(t, dir, "checkout", pre, "--", "README.md") // the operator's staged revert
		resumed, err := New().ResumeAdvance(ctx, dir)
		if resumed && err == nil {
			t.Fatal("the merge was replayed over the operator's staged revert")
		}
		if readFile(t, dir, "README.md") != "seed\n" || gitOut(t, dir, "show", ":README.md") != "seed" {
			t.Fatal("the staged revert was overwritten")
		}
		if gitOut(t, dir, "rev-parse", base.Branch) != to {
			t.Fatal("the branch moved")
		}
	})
	t.Run("recognises a fast-forward that had completed", func(t *testing.T) {
		dir, pre, to, _ := interrupted(t)
		gitOut(t, dir, "read-tree", "-m", "-u", pre, to)
		resumed, err := New().ResumeAdvance(ctx, dir)
		if resumed || err != nil {
			t.Fatalf("a completed fast-forward needs nothing: resumed=%v err=%v", resumed, err)
		}
		if _, serr := os.Stat(marker(dir)); !os.IsNotExist(serr) || gitOut(t, dir, "status", "--porcelain") != "" {
			t.Fatal("the marker was kept, or the checkout is not clean")
		}
	})
	t.Run("a marker for a branch that never moved is dropped", func(t *testing.T) {
		dir, pre := initLocalRepo(t)
		base := at(t, dir, pre)
		writeFile(t, dir, ".git/kube-dc-init-advance", base.Branch+"\n"+pre+"\n"+strings.Repeat("1", 40)+"\n"+indexChecksum(ctx, dir)+"\n", 0o600)
		if resumed, err := New().ResumeAdvance(ctx, dir); resumed || err != nil {
			t.Fatalf("%v %v", resumed, err)
		}
		if _, err := os.Stat(marker(dir)); !os.IsNotExist(err) {
			t.Fatal("the stale marker was kept")
		}
	})
}

// A writer may leave a directory it made read-only; the checkout (it may hold
// generated secrets) must still go. And a repository path with glob
// characters must not hide abandoned runs.
func TestIsolate_RemovesReadOnlyDirectoriesAndSweepsOddPaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "fleet[1]")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, parent, "init", "-q")
	writeFile(t, parent, "README.md", "seed\n", 0o644)
	gitOut(t, parent, "add", "-A")
	gitOut(t, parent, "commit", "-q", "-m", "seed")
	pre := gitOut(t, parent, "rev-parse", "HEAD")
	ctx := context.Background()

	work, discard, err := New().Isolate(ctx, parent, pre)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "locked/secret", "canary\n", 0o600)
	if err := os.Chmod(filepath.Join(work, "locked"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := discard(); err != nil {
		t.Fatalf("discard with a read-only directory: %v", err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("the private checkout survived")
	}

	abandoned := abandonRun(t, parent, pre)
	_, discard2, err := New().Isolate(ctx, parent, pre)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatal("an abandoned run under a path with glob characters was not swept")
	}
	_ = discard2()
}

// The starting checkout's ignore rules decide BEFORE anything is staged, with
// the operator's whole configuration: case-insensitive matching supplied
// through the environment, and an excludes file given relative to that
// checkout and present only there. A file they exclude is neither committed
// nor written to the shared object store.
func TestCommitIsolated_StartingPolicyDecidesBeforeStaging(t *testing.T) {
	dir, _ := initLocalRepo(t)
	writeFile(t, dir, ".gitignore", "*.dec.yaml\n", 0o644)
	gitOut(t, dir, "add", ".gitignore")
	gitOut(t, dir, "commit", "-q", "-m", "ignore rules")
	pre := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, ".git/info/local-excludes", "*.canary\n", 0o644) // operator-local, not in the private checkout
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "core.ignoreCase")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_CONFIG_KEY_1", "core.excludesFile")
	t.Setenv("GIT_CONFIG_VALUE_1", ".git/info/local-excludes") // relative to the starting checkout
	work := isolate(t, dir, pre)
	const canary = "must never reach the object store\n"
	writeFile(t, work, "c/out.yaml", "run\n", 0o644)
	writeFile(t, work, "c/sample.DEC.YAML", "decrypted, upper-case name\n", 0o644)
	writeFile(t, work, "c/local.canary", canary, 0o644)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != ".gitignore\nREADME.md\nc/out.yaml" {
		t.Fatalf("a file the starting checkout ignores was committed:\n%s", got)
	}
	hash := exec.Command("git", "-C", dir, "hash-object", "--stdin")
	hash.Env = gitEnv()
	hash.Stdin = strings.NewReader(canary)
	blob, herr := hash.Output()
	if herr != nil {
		t.Fatal(herr)
	}
	check := exec.Command("git", "-C", dir, "cat-file", "-e", strings.TrimSpace(string(blob)))
	check.Env = gitEnv()
	if check.Run() == nil {
		t.Fatal("an excluded file's content was written to the repository's object store")
	}
}

// A tracked file becomes a directory holding an ignored file: the directory
// is never named to `git add`, so the ignored file is not swept in with it.
func TestCommitIsolated_FileReplacedByDirectoryWithIgnoredFile(t *testing.T) {
	dir, _ := initLocalRepo(t)
	writeFile(t, dir, ".gitignore", "*.dec.yaml\n", 0o644)
	writeFile(t, dir, "rendered", "a file\n", 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "rendered is a file")
	pre := gitOut(t, dir, "rev-parse", "HEAD")
	work := isolate(t, dir, pre)
	if err := os.Remove(filepath.Join(work, "rendered")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "rendered/output.yaml", "run\n", 0o644)
	writeFile(t, work, "rendered/password.dec.yaml", "hunter2\n", 0o600)
	sha, err := New().CommitIsolated(context.Background(), dir, work, "msg", pre, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "ls-tree", "-r", "--name-only", sha); got != ".gitignore\nREADME.md\nrendered/output.yaml" {
		t.Fatalf("tree:\n%s", got)
	}
}

// What a push would newly publish: every commit the DESTINATION lacks, through
// both parents of a merge, whatever the worktree holds, and each file under
// its own path.
func TestUnpublishedFiles(t *testing.T) {
	dir, _ := initLocalRepo(t)
	bareRemote(t, dir)
	branch := gitOut(t, dir, "symbolic-ref", "--short", "HEAD")
	gitOut(t, dir, "push", "-q", "origin", branch)
	gitOut(t, dir, "fetch", "-q", "origin")
	ctx := context.Background()
	secret := func(path string) bool { return path == "age.key" || strings.HasSuffix(path, ".enc.yaml") }
	names := func(sha string) string {
		files, err := New().UnpublishedFiles(ctx, dir, "", sha, secret, "age.key", "c")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range files {
			got = append(got, f.Path+"="+strings.TrimSpace(string(f.Data)))
		}
		sort.Strings(got)
		return strings.Join(got, " ")
	}
	if got := names(gitOut(t, dir, "rev-parse", "HEAD")); got != "" {
		t.Fatalf("a published tip publishes nothing new, got %q", got)
	}
	// A side branch commits the key and a decrypted secret (the same content
	// also sits under an unmanaged name), removes the key and re-encrypts
	// the secret, and is merged: the tip is clean.
	gitOut(t, dir, "checkout", "-q", "-b", "topic")
	writeFile(t, dir, "age.key", "KEY", 0o600)
	writeFile(t, dir, "c/backup.yaml", "PLAINTEXT", 0o644) // same blob, listed first
	writeFile(t, dir, "c/tls.enc.yaml", "PLAINTEXT", 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "key and plaintext, by mistake")
	gitOut(t, dir, "rm", "-q", "age.key", "c/backup.yaml")
	writeFile(t, dir, "c/tls.enc.yaml", "CIPHERTEXT", 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "cleaned up")
	gitOut(t, dir, "checkout", "-q", branch)
	gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "main moves on")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge topic", "topic")
	tip := gitOut(t, dir, "rev-parse", "HEAD")
	if got := names(tip); got != "age.key=KEY c/tls.enc.yaml=CIPHERTEXT c/tls.enc.yaml=PLAINTEXT" {
		t.Fatalf("the merged side branch's history is part of the push, got %q", got)
	}
	// The worktree plays no part: remove the file from it.
	if err := os.Remove(filepath.Join(dir, "c", "tls.enc.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := names(tip); !strings.Contains(got, "c/tls.enc.yaml=PLAINTEXT") {
		t.Fatalf("the scan depends on the worktree: %q", got)
	}
	gitOut(t, dir, "push", "-q", "origin", branch)
	// Nothing is new after the push; the tip itself is inspected all the
	// same (a resume publishes a tip the remote may already have), and
	// nothing of the history below it.
	if got := names(tip); got != "c/tls.enc.yaml=CIPHERTEXT" {
		t.Fatalf("after the push to origin only the tip's own files are inspected, got %q", got)
	}
	// origin now points at another, empty repository. refs/remotes/origin/*
	// still describe the old one; the new destination has none of it.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", elsewhere).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, dir, "remote", "set-url", "origin", elsewhere)
	if got := names(tip); !strings.Contains(got, "age.key=KEY") {
		t.Fatalf("stale remote-tracking refs hid history the new destination lacks: %q", got)
	}
}

// `git replace --graft` makes git show a history the push does not publish:
// the scan must walk the parents the commits really record.
func TestUnpublishedFiles_IgnoresReplaceGrafts(t *testing.T) {
	dir, _ := initLocalRepo(t)
	bareRemote(t, dir)
	writeFile(t, dir, "age.key", "KEY", 0o600)
	gitOut(t, dir, "add", "age.key")
	gitOut(t, dir, "commit", "-q", "-m", "key in")
	gitOut(t, dir, "rm", "-q", "age.key")
	gitOut(t, dir, "commit", "-q", "-m", "key out")
	tip := gitOut(t, dir, "rev-parse", "HEAD")
	gitOut(t, dir, "replace", "--graft", tip) // tip now looks like a root commit
	files, err := New().UnpublishedFiles(context.Background(), dir, "", tip, func(p string) bool { return p == "age.key" }, "age.key")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || string(files[0].Data) != "KEY" {
		t.Fatalf("the grafted-away parent is still pushed, and must be scanned: %+v", files)
	}
}

// A commit that turns a tracked file into a directory fast-forwards cleanly;
// the same file with a local edit is still refused.
func TestAdvance_FileBecomesDirectory(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (dir, pre, to string) {
		dir, _ = initLocalRepo(t)
		writeFile(t, dir, "rendered", "a file\n", 0o644)
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "commit", "-q", "-m", "rendered is a file")
		pre = gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		if err := os.Remove(filepath.Join(work, "rendered")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, work, "rendered/output.yaml", "run\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "msg", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		return dir, pre, to
	}
	t.Run("clean", func(t *testing.T) {
		dir, pre, to := setup(t)
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err != nil {
			t.Fatal(err)
		}
		if readFile(t, dir, "rendered/output.yaml") != "run\n" || gitOut(t, dir, "status", "--porcelain") != "" {
			t.Fatal("the checkout was not fast-forwarded cleanly")
		}
	})
	t.Run("edited locally", func(t *testing.T) {
		dir, pre, to := setup(t)
		writeFile(t, dir, "rendered", "my edit\n", 0o644)
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err == nil {
			t.Fatal("want a refusal")
		}
		if readFile(t, dir, "rendered") != "my edit\n" || gitOut(t, dir, "rev-parse", "HEAD") != pre {
			t.Fatal("the local edit was lost or the branch moved")
		}
	})
}

// The reverse: a directory holding only files the commit removes becomes a
// file. Something else in that directory is still in the way.
func TestAdvance_DirectoryBecomesFile(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (dir, pre, to string) {
		dir, _ = initLocalRepo(t)
		writeFile(t, dir, "rendered/output.yaml", "old\n", 0o644)
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "commit", "-q", "-m", "rendered is a directory")
		pre = gitOut(t, dir, "rev-parse", "HEAD")
		work := isolate(t, dir, pre)
		if err := os.RemoveAll(filepath.Join(work, "rendered")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, work, "rendered", "a file now\n", 0o644)
		to, err := New().CommitIsolated(ctx, dir, work, "msg", pre, nil)
		if err != nil {
			t.Fatal(err)
		}
		return dir, pre, to
	}
	t.Run("clean", func(t *testing.T) {
		dir, pre, to := setup(t)
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err != nil {
			t.Fatal(err)
		}
		if readFile(t, dir, "rendered") != "a file now\n" || gitOut(t, dir, "status", "--porcelain") != "" {
			t.Fatal("the checkout was not fast-forwarded cleanly")
		}
	})
	t.Run("someone's file in that directory", func(t *testing.T) {
		dir, pre, to := setup(t)
		writeFile(t, dir, "rendered/notes.txt", "mine\n", 0o644)
		if err := New().Advance(ctx, dir, at(t, dir, pre), to); err == nil {
			t.Fatal("want a refusal")
		}
		if readFile(t, dir, "rendered/notes.txt") != "mine\n" || gitOut(t, dir, "rev-parse", "HEAD") != pre {
			t.Fatal("the file was lost or the branch moved")
		}
	})
}

// The destination the publication check asked is the destination the push
// sends to: a push URL removed while the check runs must not redirect the
// push to the fetch URL, and a check error sends nothing.
func TestPushCommit_ChecksAndPushesTheSameDestination(t *testing.T) {
	dir, _ := initLocalRepo(t)
	fetchSide := bareRemote(t, dir) // origin's (fetch) URL: lacks the history below
	pushSide := filepath.Join(t.TempDir(), "push.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", pushSide).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, dir, "config", "remote.origin.pushurl", pushSide)
	branch := gitOut(t, dir, "symbolic-ref", "HEAD")
	writeFile(t, dir, "age.key", "KEY", 0o600)
	gitOut(t, dir, "add", "age.key")
	gitOut(t, dir, "commit", "-q", "-m", "key in")
	gitOut(t, dir, "rm", "-q", "age.key")
	gitOut(t, dir, "commit", "-q", "-m", "key out")
	gitOut(t, dir, "push", "-q", pushSide, "HEAD:"+branch) // the push side already has it
	tip := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "f", "x\n", 0o644)
	gitOut(t, dir, "add", "f")
	gitOut(t, dir, "commit", "-q", "-m", "new work")
	next := gitOut(t, dir, "rev-parse", "HEAD")
	_ = tip
	want := func(p string) bool { return p == "age.key" }
	ctx := context.Background()

	// The check sees nothing new about the key (the push side has that
	// history). While it runs, the push URL is removed.
	err := New().PushCommit(ctx, dir, "", ports.Checkout{Branch: branch, SHA: next}, &ports.PublishCheck{
		Want: want, Roots: []string{"age.key"},
		Check: func(files []ports.UnpublishedFile) error {
			if len(files) != 0 {
				t.Errorf("the push side already has the key's history, got %+v", files)
			}
			gitOut(t, dir, "config", "--unset", "remote.origin.pushurl")
			return nil
		},
	})
	if !errors.Is(err, ports.ErrPublishRefused) {
		t.Fatalf("want a refusal when the destination changed during the check, got %v", err)
	}
	if out, err := exec.Command("git", "-C", fetchSide, "rev-parse", "--verify", "-q", branch).Output(); err == nil {
		t.Fatalf("the push went to the other URL: %s", out)
	}

	// A check error sends nothing.
	gitOut(t, dir, "config", "remote.origin.pushurl", pushSide)
	boom := errors.New("refused")
	err = New().PushCommit(ctx, dir, "", ports.Checkout{Branch: branch, SHA: next}, &ports.PublishCheck{Want: want, Check: func([]ports.UnpublishedFile) error { return boom }})
	if !errors.Is(err, ports.ErrPublishRefused) || !errors.Is(err, boom) {
		t.Fatalf("want the check's error wrapped in ErrPublishRefused, got %v", err)
	}
	if got := gitOut(t, pushSide, "rev-parse", branch); got != tip {
		t.Fatalf("a refused push moved the remote branch to %s", got)
	}
	// And without interference it publishes to the push side.
	if err := New().PushCommit(ctx, dir, "", ports.Checkout{Branch: branch, SHA: next}, &ports.PublishCheck{Want: want, Check: func([]ports.UnpublishedFile) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, pushSide, "rev-parse", branch); got != next {
		t.Fatalf("push side at %s, want %s", got, next)
	}
}

// The clean-tree check must not write to the checkout: a submodule that is
// configured but not cloned stays that way.
func TestDiff_DoesNotWriteToTheCheckout(t *testing.T) {
	sub, _ := initLocalRepo(t)
	dir, _ := initLocalRepo(t)
	gitOut(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "vendor/sub")
	gitOut(t, dir, "commit", "-q", "-m", "a submodule")
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "-q", dir, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, clone, "submodule", "init") // configured, not updated
	writeFile(t, clone, "note.txt", "unrelated\n", 0o644)
	diff, err := New().Diff(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "note.txt" || diff.Files[0].Status != "??" {
		t.Fatalf("diff: %+v", diff.Files)
	}
	if entries, _ := os.ReadDir(filepath.Join(clone, "vendor", "sub")); len(entries) != 0 {
		t.Fatalf("the status check wrote into the submodule directory: %v", entries)
	}
}

func TestDiff_StatusCodes(t *testing.T) {
	dir, _ := initLocalRepo(t)
	writeFile(t, dir, "tracked.txt", "v1\n", 0o644)
	writeFile(t, dir, "gone.txt", "v1\n", 0o644)
	writeFile(t, dir, "old name.txt", "renamed content, long enough to be detected as a rename\n", 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "tracked.txt", "v2\n", 0o644)
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "mv", "old name.txt", "new name.txt")
	writeFile(t, dir, "staged.txt", "new\n", 0o644)
	gitOut(t, dir, "add", "staged.txt")
	writeFile(t, dir, "sub/untracked.txt", "new\n", 0o644)
	diff, err := New().Diff(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range diff.Files {
		got[f.Path] = f.Status
	}
	want := map[string]string{"tracked.txt": "M", "gone.txt": "D", "new name.txt": "R", "staged.txt": "A", "sub/untracked.txt": "??"}
	if len(got) != len(want) {
		t.Fatalf("diff: %v", got)
	}
	for p, st := range want {
		if got[p] != st {
			t.Errorf("%s: got %q want %q (all: %v)", p, got[p], st, got)
		}
	}
}

// A pushInsteadOf rule redirects pushes to a fork. git itself would push
// there; so must init, and the upstream must not receive the commit.
func TestPushCommit_HonoursPushInsteadOf(t *testing.T) {
	dir, pre := initLocalRepo(t)
	upstream := bareRemote(t, dir)
	fork := filepath.Join(t.TempDir(), "fork.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", fork).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, dir, "config", "url."+fork+".pushInsteadOf", upstream)
	base := at(t, dir, pre)
	if err := New().PushCommit(context.Background(), dir, "", base, nil); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, fork, "rev-parse", base.Branch); got != pre {
		t.Fatalf("the fork (the effective push destination) has %s, want %s", got, pre)
	}
	if out, err := exec.Command("git", "-C", upstream, "rev-parse", "--verify", "-q", base.Branch).Output(); err == nil {
		t.Fatalf("the upstream received the push: %s", out)
	}
}

// In a `git clone --shared` the commit's objects are reached through
// alternates. go-git may not find them and then reports "already up to date"
// without sending anything: the push must either publish or fail, never claim
// success with the remote left behind.
func TestPushCommit_SharedCloneNeverClaimsAnUnsentPush(t *testing.T) {
	src, _ := initLocalRepo(t)
	writeFile(t, src, "f", "x\n", 0o644)
	gitOut(t, src, "add", "f")
	gitOut(t, src, "commit", "-q", "-m", "tip")
	tip := gitOut(t, src, "rev-parse", "HEAD")
	clone := filepath.Join(t.TempDir(), "shared")
	if out, err := exec.Command("git", "clone", "-q", "--shared", src, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, clone, "remote", "set-url", "origin", bare)
	base := at(t, clone, tip)
	err := New().PushCommit(context.Background(), clone, "", base, nil)
	remote, rerr := exec.Command("git", "-C", bare, "rev-parse", "--verify", "-q", base.Branch).Output()
	published := rerr == nil && strings.TrimSpace(string(remote)) == tip
	if err == nil && !published {
		t.Fatal("the push reported success, but the remote branch is not at the commit")
	}
	if err != nil && published {
		t.Fatalf("the push reported an error although the remote has the commit: %v", err)
	}
}

// The destination hook judges the URL the push will go to, the one git
// resolves, and its refusal sends nothing.
func TestPushCommit_DestinationHookSeesTheEffectiveURL(t *testing.T) {
	dir, pre := initLocalRepo(t)
	upstream := bareRemote(t, dir)
	fork := filepath.Join(t.TempDir(), "fork.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", fork).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitOut(t, dir, "config", "url."+fork+".pushInsteadOf", upstream)
	base := at(t, dir, pre)
	var seen string
	refuse := errors.New("not the repository Flux reconciles")
	err := New().PushCommit(context.Background(), dir, "", base, &ports.PublishCheck{
		Destination: func(url string) error { seen = url; return refuse },
		Want:        func(string) bool { return false },
		Check: func([]ports.UnpublishedFile) error {
			t.Error("the check ran after the destination was refused")
			return nil
		},
	})
	if seen != fork {
		t.Fatalf("the hook saw %q, want the effective push URL %q", seen, fork)
	}
	if !errors.Is(err, ports.ErrPublishRefused) || !errors.Is(err, refuse) {
		t.Fatalf("want the refusal, got %v", err)
	}
	for _, remote := range []string{fork, upstream} {
		if out, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", "-q", base.Branch).Output(); err == nil {
			t.Fatalf("something was pushed to %s: %s", remote, out)
		}
	}
}

// sshConfigStub is an SSH configuration that sends every host elsewhere.
type sshConfigStub map[string]string

func (s sshConfigStub) Get(_, key string) string { return s[key] }

// countingListener accepts connections on a local port and counts them. No SSH
// server is behind it: a connection is closed as soon as it is made.
func countingListener(t *testing.T) (port string, hits *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no local listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hits = new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = conn.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, hits
}

// The operator's SSH configuration has an entry for the host of the push URL
// that names another HostName and Port. The transport would follow it (its
// Port even overrides a port written in the URL), and the listing, the push
// and its confirmation would all go to an address nothing checked. PushCommit
// dials the address the URL names, and only that one.
func TestPushCommit_SSHDialsTheAddressInTheURL(t *testing.T) {
	dir, pre := initLocalRepo(t)
	t.Setenv("SSH_AUTH_SOCK", startEmptyAgent(t))
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.WriteFile(known, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SSH_KNOWN_HOSTS", known)

	written, hitsWritten := countingListener(t)
	redirected, hitsRedirected := countingListener(t)
	stub := sshConfigStub{"Hostname": "127.0.0.1", "Port": redirected}
	saved := gitssh.DefaultSSHConfig
	gitssh.DefaultSSHConfig = stub
	t.Cleanup(func() { gitssh.DefaultSSHConfig = saved })

	gitOut(t, dir, "remote", "add", "origin", "ssh://git@127.0.0.1:"+written+"/acme/fleet.git")
	var judged string
	err := New().PushCommit(context.Background(), dir, "", at(t, dir, pre), &ports.PublishCheck{
		Destination: func(url string) error { judged = url; return nil },
		Want:        func(string) bool { return false },
		Check:       func([]ports.UnpublishedFile) error { return nil },
	})
	if err == nil {
		t.Fatal("a push to a port with no SSH server succeeded")
	}
	if judged != "ssh://git@127.0.0.1:"+written+"/acme/fleet.git" {
		t.Fatalf("the destination judged was %q", judged)
	}
	if n := hitsRedirected.Load(); n != 0 {
		t.Fatalf("%d connection(s) went to the address in the SSH configuration, which nothing checked (%v)", n, err)
	}
	if hitsWritten.Load() == 0 {
		t.Fatalf("nothing connected to the address in the URL: %v", err)
	}
	if got, ok := gitssh.DefaultSSHConfig.(sshConfigStub); !ok || got["Port"] != redirected {
		t.Fatal("the transport's SSH configuration was not put back after the push")
	}
}
