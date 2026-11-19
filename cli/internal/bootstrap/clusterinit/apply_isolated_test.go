package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/mock"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// These tests run Apply against real Git repositories with the real adapter:
// the fleet checkout, a local bare origin, and the private checkout Apply
// generates in.

// plainGit hides the adapter's provider binding: these checkouts have no
// provider remote, and the tests want the real isolate, commit, push and
// fast-forward.
type plainGit struct{ ports.GitClient }

func (g plainGit) isolated() ports.GitIsolatedClient { return g.GitClient.(ports.GitIsolatedClient) }

func (g plainGit) Checkout(ctx context.Context, dir string) (ports.Checkout, error) {
	return g.isolated().Checkout(ctx, dir)
}

func (g plainGit) Isolate(ctx context.Context, dir, sha string) (string, func() error, error) {
	return g.isolated().Isolate(ctx, dir, sha)
}

func (g plainGit) CommitIsolated(ctx context.Context, dir, work, msg, parent string, check func(ports.CommitTrees) error) (string, error) {
	return g.isolated().CommitIsolated(ctx, dir, work, msg, parent, check)
}

func (g plainGit) TreeOf(ctx context.Context, dir, sha string) (ports.TreeFileReader, error) {
	return g.isolated().TreeOf(ctx, dir, sha)
}

func (g plainGit) ResumeAdvance(ctx context.Context, dir string) (bool, error) {
	return g.isolated().ResumeAdvance(ctx, dir)
}

func (g plainGit) PushCommit(ctx context.Context, dir, token string, at ports.Checkout, check *ports.PublishCheck) error {
	return g.isolated().PushCommit(ctx, dir, token, at, check)
}

func (g plainGit) Advance(ctx context.Context, dir string, from ports.Checkout, to string) error {
	return g.isolated().Advance(ctx, dir, from, to)
}

// duringPushGit runs fn as the push call starts: before the destination is
// asked what it has and before the last check of the fleet checkout.
type duringPushGit struct {
	plainGit
	fn func()
}

func (g duringPushGit) PushCommit(ctx context.Context, dir, token string, at ports.Checkout, check *ports.PublishCheck) error {
	g.fn()
	return g.plainGit.PushCommit(ctx, dir, token, at, check)
}

// afterPushGit runs fn right after the push went out, before the fleet
// checkout is fast-forwarded.
type afterPushGit struct {
	plainGit
	fn func()
}

func (g afterPushGit) PushCommit(ctx context.Context, dir, token string, at ports.Checkout, check *ports.PublishCheck) error {
	err := g.plainGit.PushCommit(ctx, dir, token, at, check)
	if err == nil {
		g.fn()
	}
	return err
}

// stepHook runs fn when a step finishes successfully.
type stepHook struct {
	NopReporter
	after StepID
	fn    func()
}

func (h stepHook) Done(id StepID, err error) {
	if id == h.after && err == nil {
		h.fn()
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitOutput runs git and returns trimmed stdout ("" on failure).
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func revParse(t *testing.T, repo, rev string) string {
	t.Helper()
	return gitOutput(t, repo, "rev-parse", "--verify", "-q", rev)
}

func status(t *testing.T, repo string) string {
	t.Helper()
	return gitOutput(t, repo, "status", "--porcelain", "--untracked-files=all")
}

// bareOrigin adds a local bare repository as origin and returns its path.
func bareOrigin(t *testing.T, repo string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	return bare
}

// committedFleet: a starter checkout committed on main, with the starter's
// ignore rules.
func committedFleet(t *testing.T) string {
	t.Helper()
	repo := applyFleet(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("age.key\n*.dec.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "starter")
	return repo
}

// privateTemp points the private checkouts at a directory the test can
// inspect: it must be empty again after every run.
func privateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func wantNoPrivateCheckout(t *testing.T, repo, tmp string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(tmp, "kube-dc-init-*")); len(left) != 0 {
		t.Errorf("a private checkout was left in the temp directory: %v", left)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "kube-dc-init")); !os.IsNotExist(err) {
		t.Errorf("a private checkout was left in the git directory (%v)", err)
	}
	if list := gitOutput(t, repo, "worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Errorf("worktree metadata was left behind:\n%s", list)
	}
}

func ranFluxInstall(r *fakeScriptRunner) bool {
	for _, c := range r.calls {
		if c.Kind == ports.ScriptFluxInstall {
			return true
		}
	}
	return false
}

// A fresh install publishes its own output only and fast-forwards the fleet
// checkout; someone's unrelated files and staged draft are neither published
// nor disturbed, and no repository hook runs.
func TestApply_Isolated_FreshInstall(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	tmp := privateTemp(t)
	t.Setenv("SOPS_AGE_KEY_FILE", "") // Apply exports the fleet's key; restored after the test
	t.Setenv("SOPS_AGE_KEY", "")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, h := range []string{"pre-commit", "post-commit", "post-checkout", "post-index-change", "pre-push"} {
		p := filepath.Join(repo, ".git", "hooks", h)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho "+h+" >> "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "age.key"), []byte("AGE-SECRET-KEY-TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Someone's work, present before the run would be refused by the
	// clean-tree gate, so it appears while the run is under way.
	notes := filepath.Join(repo, "addons", "local-notes.txt")
	overlayNotes := filepath.Join(repo, "clusters", "east1", "operator-notes.txt")
	runner := applyRunner(t, repo)
	inner := runner.onRun
	var keySeen string
	scaffolded := false
	runner.onRun = func(clusterDir string) error {
		if scaffolded {
			return nil // flux-install: runs in the fleet checkout, writes nothing here
		}
		scaffolded = true
		root := filepath.Dir(filepath.Dir(clusterDir))
		// The key reaches scripts by absolute path, not as a file in the
		// private checkout.
		if b, err := os.ReadFile(os.Getenv("SOPS_AGE_KEY_FILE")); err == nil {
			keySeen = string(b)
		}
		if _, err := os.Lstat(filepath.Join(root, "age.key")); err == nil {
			t.Error("the fleet's age key was linked or copied into the private checkout")
		}
		if root == repo {
			t.Error("the scaffold script ran in the fleet checkout, not in a private checkout")
		}
		if err := inner(clusterDir); err != nil {
			return err
		}
		// A decrypted file a script leaves behind: ignored, never published.
		return os.WriteFile(filepath.Join(clusterDir, "secrets.dec.yaml"), []byte("plaintext\n"), 0o600)
	}
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.Reporter = stepHook{after: StepScaffold, fn: func() {
		_ = os.MkdirAll(filepath.Dir(notes), 0o755)
		_ = os.WriteFile(notes, []byte("unrelated\n"), 0o644)
		_ = os.MkdirAll(filepath.Dir(overlayNotes), 0o755)
		_ = os.WriteFile(overlayNotes, []byte("my notes\n"), 0o644)
		_ = os.WriteFile(filepath.Join(repo, "draft.txt"), []byte("draft\n"), 0o644)
		runGit(t, repo, "add", "draft.txt")
	}}
	var out bytes.Buffer
	opts.Out = &out
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	local, remote := revParse(t, repo, "main"), revParse(t, bare, "main")
	if local == "" || local != remote {
		t.Fatalf("local main=%q, origin main=%q", local, remote)
	}
	published := gitOutput(t, bare, "ls-tree", "-r", "--name-only", "main")
	for _, private := range []string{"local-notes", "operator-notes", "draft.txt", "age.key", "secrets.dec.yaml"} {
		if strings.Contains(published, private) {
			t.Errorf("%s was published:\n%s", private, published)
		}
	}
	if !strings.Contains(published, "clusters/east1/cluster-config.env") {
		t.Fatalf("the overlay was not published:\n%s", published)
	}
	if st := status(t, repo); st != "A  draft.txt\n?? addons/local-notes.txt\n?? clusters/east1/operator-notes.txt" {
		t.Fatalf("the fleet checkout after a successful run:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", "east1", "secrets.dec.yaml")); !os.IsNotExist(err) {
		t.Error("an ignored file from the private checkout reached the fleet checkout")
	}
	if keySeen != "AGE-SECRET-KEY-TEST\n" {
		t.Errorf("scripts in the private checkout could not read the fleet's age key through SOPS_AGE_KEY_FILE (%q)", keySeen)
	}
	if b, _ := os.ReadFile(marker); len(b) != 0 {
		t.Errorf("repository hooks ran: %s", b)
	}
	if !ranFluxInstall(runner) {
		t.Error("flux-install did not run")
	}
	wantNoPrivateCheckout(t, repo, tmp)
}

// The reason this exists: an init that fails must leave the checkout as it
// was, so the retry in the same checkout just works.
func TestApply_Isolated_FailedInitThenRetry(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	key := filepath.Join(repo, "age.key")
	pub := newAgeKey(t, key)
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(sopsConfig(sopsRule(`\.enc\.yaml$`, pub))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "platform", "registry-depot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "platform", "registry-depot", "kustomization.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "sops rules, registry-depot")
	pre := revParse(t, repo, "HEAD")
	bare := bareOrigin(t, repo)
	tmp := privateTemp(t)

	// Run 1 mints the registry credential, then fails (plaintext secrets),
	// as the 2026-11-18 install failed after minting.
	failing := &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if err := ensureRegistryDepotSecret(filepath.Dir(filepath.Dir(clusterDir)), io.Discard); err != nil {
				return err
			}
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"),
				[]byte("stringData:\n    KEYCLOAK_ADMIN_PASSWORD: plaintext\n"), 0o644)
		},
		lines: []ports.Line{{Stream: ports.StreamExit, Text: "0", Time: time.Now()}},
	}
	if err := Apply(context.Background(), registryOpts(t, repo, failing, plainGit{realGit()})); !errors.Is(err, ErrScaffoldSecretsNotEncrypted) {
		t.Fatalf("run 1: want ErrScaffoldSecretsNotEncrypted, got %v", err)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("run 1 left the checkout dirty:\n%s", st)
	}
	if revParse(t, repo, "HEAD") != pre || revParse(t, bare, "main") != "" {
		t.Fatal("run 1 moved a branch or pushed")
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(registryDepotCredentialPath))); !os.IsNotExist(err) {
		t.Fatal("run 1 left its credential in the checkout")
	}
	wantNoPrivateCheckout(t, repo, tmp)

	// Run 2, same checkout, nothing cleaned up by hand.
	runner := registryRunner(t, repo)
	if err := Apply(context.Background(), registryOpts(t, repo, runner, plainGit{realGit()})); err != nil {
		t.Fatalf("run 2 (retry): %v", err)
	}
	if revParse(t, repo, "main") != revParse(t, bare, "main") || revParse(t, bare, "main") == pre {
		t.Fatal("the retry did not publish")
	}
	credential := gitOutput(t, bare, "show", "main:"+registryDepotCredentialPath)
	if !strings.Contains(credential, "ENC[") || !strings.Contains(credential, pub) {
		t.Fatalf("the published credential is not encrypted for the fleet's recipient:\n%s", credential)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the retry left the checkout dirty:\n%s", st)
	}
	if !ranFluxInstall(runner) {
		t.Error("flux-install did not run")
	}
	wantNoPrivateCheckout(t, repo, tmp)
}

// A failed push changes nothing locally; the re-run publishes.
func TestApply_Isolated_PushFailureThenRetry(t *testing.T) {
	repo := committedFleet(t)
	missing := filepath.Join(t.TempDir(), "missing.git")
	runGit(t, repo, "remote", "add", "origin", "file://"+missing)
	pre := revParse(t, repo, "HEAD")
	tmp := privateTemp(t)
	runner := applyRunner(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "commit-push") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("want a commit-push failure that says nothing changed, got %v", err)
	}
	if revParse(t, repo, "HEAD") != pre || status(t, repo) != "" {
		t.Fatalf("a failed push changed the checkout: HEAD=%s status=%q", revParse(t, repo, "HEAD"), status(t, repo))
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran after a failed push")
	}
	wantNoPrivateCheckout(t, repo, tmp)

	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", missing).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()})); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := revParse(t, missing, "main"); got == "" || got != revParse(t, repo, "main") {
		t.Fatal("the retry did not publish")
	}
}

// The checkout leaves the starting branch or commit while init generates:
// nothing is pushed and the checkout is left exactly as its owner put it.
func TestApply_Isolated_CheckoutMovedBeforePublish(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(t *testing.T, repo string)
	}{
		{"someone commits", func(t *testing.T, repo string) { runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "operator") }},
		{"someone switches branch at the same commit", func(t *testing.T, repo string) { runGit(t, repo, "checkout", "-q", "-b", "other") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := committedFleet(t)
			bare := bareOrigin(t, repo)
			tmp := privateTemp(t)
			var head string
			opts := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
			opts.Reporter = stepHook{after: StepScaffold, fn: func() {
				tc.move(t, repo)
				head = revParse(t, repo, "HEAD")
			}}
			err := Apply(context.Background(), opts)
			if !errors.Is(err, ErrCheckoutMoved) || !strings.Contains(err.Error(), "nothing was pushed") {
				t.Fatalf("want ErrCheckoutMoved before any push, got %v", err)
			}
			if revParse(t, bare, "main") != "" || revParse(t, bare, "other") != "" {
				t.Fatal("something was pushed")
			}
			if revParse(t, repo, "HEAD") != head || status(t, repo) != "" {
				t.Fatalf("the checkout was changed: HEAD=%s status=%q", revParse(t, repo, "HEAD"), status(t, repo))
			}
			wantNoPrivateCheckout(t, repo, tmp)
		})
	}
}

// The checkout switches branch while the push call is checking what it would
// publish: the last check before sending catches it and nothing is pushed.
func TestApply_Isolated_BranchSwitchBeforeSending(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	pre := revParse(t, repo, "HEAD")
	git := duringPushGit{plainGit{realGit()}, func() { runGit(t, repo, "checkout", "-q", "-b", "other") }}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, applyRunner(t, repo), git))
	if !errors.Is(err, ErrCheckoutMoved) || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want ErrCheckoutMoved before anything is sent, got %v", err)
	}
	if revParse(t, bare, "main") != "" || revParse(t, bare, "other") != "" {
		t.Fatal("something was pushed")
	}
	if revParse(t, repo, "main") != pre || revParse(t, repo, "other") != pre || status(t, repo) != "" {
		t.Fatal("a local branch or the checkout changed")
	}
}

// The checkout switches branch right after the push went out: the commit is
// on the starting branch, the other branch's checkout is not touched, and the
// error says the push happened.
func TestApply_Isolated_BranchSwitchDuringPush(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	pre := revParse(t, repo, "HEAD")
	runner := applyRunner(t, repo)
	git := afterPushGit{plainGit{realGit()}, func() { runGit(t, repo, "checkout", "-q", "-b", "other") }}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, git))
	if !errors.Is(err, ErrCheckoutNotAdvanced) {
		t.Fatalf("want ErrCheckoutNotAdvanced, got %v", err)
	}
	pushed := revParse(t, bare, "main")
	if pushed == "" || pushed == pre || revParse(t, bare, "other") != "" {
		t.Fatalf("the commit must be on origin/main only (main=%q other=%q)", pushed, revParse(t, bare, "other"))
	}
	if revParse(t, repo, "main") != pre || revParse(t, repo, "other") != pre || status(t, repo) != "" {
		t.Fatalf("a local branch or the other branch's checkout changed: main=%s other=%s status=%q",
			revParse(t, repo, "main"), revParse(t, repo, "other"), status(t, repo))
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran against a checkout without the overlay")
	}
}

// A file appears where the new commit adds one: it is not overwritten, the
// error names it, and after the operator moves it away and pulls, the re-run
// resumes and completes.
func TestApply_Isolated_OverlapThenRecover(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	pre := revParse(t, repo, "HEAD")
	mine := filepath.Join(repo, "clusters", "east1", "cluster-config.env")
	runner := applyRunner(t, repo)
	git := afterPushGit{plainGit{realGit()}, func() {
		_ = os.MkdirAll(filepath.Dir(mine), 0o755)
		_ = os.WriteFile(mine, []byte("# mine\n"), 0o644)
	}}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, git))
	if !errors.Is(err, ErrCheckoutNotAdvanced) || !strings.Contains(err.Error(), "clusters/east1/cluster-config.env") {
		t.Fatalf("want ErrCheckoutNotAdvanced naming the file, got %v", err)
	}
	if b, _ := os.ReadFile(mine); string(b) != "# mine\n" {
		t.Fatalf("the operator's file was overwritten: %q", b)
	}
	if revParse(t, repo, "main") != pre || status(t, repo) != "?? clusters/east1/cluster-config.env" {
		t.Fatalf("the checkout changed: status=%q", status(t, repo))
	}
	pushed := revParse(t, bare, "main")
	if pushed == "" || ranFluxInstall(runner) {
		t.Fatalf("want the commit pushed and flux-install not run (pushed=%q)", pushed)
	}

	// What the error tells the operator to do.
	if err := os.Remove(mine); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "pull", "-q", "--ff-only", "origin", "main")
	if err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()})); err != nil {
		t.Fatalf("re-run after the pull: %v", err)
	}
	if !ranFluxInstall(runner) {
		t.Fatal("the re-run did not continue to flux-install")
	}
	if revParse(t, bare, "main") != pushed {
		t.Fatal("the re-run created another commit")
	}
}

// A resume pushes the commit it resumed to the branch it started on, even
// when the checkout switches branches as the push starts; and flux-install,
// which works in this checkout and reads its branch, does not run from the
// branch that was switched to.
func TestApply_Isolated_ResumePushGoesToStartingBranch(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	overlay := revParse(t, repo, "HEAD")
	if revParse(t, bare, "main") != "" || status(t, repo) != "" {
		t.Fatalf("--no-push must commit locally only and leave a clean checkout (status=%q)", status(t, repo))
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", "east1", "cluster-config.env")); err != nil {
		t.Fatalf("--no-push did not fast-forward the checkout: %v", err)
	}

	runner := applyRunner(t, repo)
	git := afterPushGit{plainGit{realGit()}, func() { runGit(t, repo, "checkout", "-q", "-b", "other") }}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, git))
	if !errors.Is(err, ErrCheckoutMoved) {
		t.Fatalf("run 2 (resume, switched right after the push): want ErrCheckoutMoved, got %v", err)
	}
	if got := revParse(t, bare, "main"); got != overlay {
		t.Fatalf("origin/main=%q, want the resumed overlay commit %s", got, overlay)
	}
	if revParse(t, bare, "other") != "" {
		t.Fatal("the resume pushed the switched-to branch")
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran from the branch that was switched to")
	}

	// Back on the starting branch, the re-run continues.
	runGit(t, repo, "checkout", "-q", "main")
	if err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()})); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if !ranFluxInstall(runner) {
		t.Fatal("flux-install did not run on the starting branch")
	}
}

// The registry-depot overlay must not be published without its credential: a
// credential Git ignores is not in the commit.
func TestApply_Isolated_IgnoredCredentialRefused(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	key := filepath.Join(repo, "age.key")
	pub := newAgeKey(t, key)
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	for path, body := range map[string]string{
		".sops.yaml": sopsConfig(sopsRule(`\.enc\.yaml$`, pub)),
		".gitignore": "age.key\n" + registryDepotCredentialPath + "\n",
		"platform/registry-depot/kustomization.yaml": "resources: []\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "ignore the credential")
	bare := bareOrigin(t, repo)
	err := Apply(context.Background(), registryOpts(t, repo, registryRunner(t, repo), plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "does not contain "+registryDepotCredentialPath) {
		t.Fatalf("want a missing-credential refusal, got %v", err)
	}
	if revParse(t, bare, "main") != "" || status(t, repo) != "" {
		t.Fatal("an overlay without its credential was published, or the checkout changed")
	}
}

// SOPS must choose the creation rule of the file's final path, not of the
// temp file the plaintext is encrypted in.
func TestSopsEncryptToFile_UsesTheFinalPathsRule(t *testing.T) {
	requireSOPS(t)
	repo := t.TempDir()
	narrow := newAgeKey(t, filepath.Join(repo, "narrow.key"))
	fallback := newAgeKey(t, filepath.Join(repo, "fallback.key"))
	rules := sopsConfig(
		sopsRule(`^platform/registry-depot/secret\.enc\.yaml$`, narrow),
		sopsRule(`\.enc\.yaml$`, fallback),
	)
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "platform", "registry-depot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRegistryDepotSecret(repo, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(registryDepotCredentialPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), narrow) || strings.Contains(string(b), fallback) {
		t.Fatalf("the credential must be encrypted for the rule of its own path (narrow), not the fallback:\n%s", b)
	}
}

// The adapter behind KUBE_DC_MOCK runs Apply (it implements the
// private-checkout client), commit included.
func TestApply_MockGitClient(t *testing.T) {
	repo := applyFleet(t)
	git := mock.NewGitClient(&mock.Scenario{Fleet: &mock.FleetFixture{Cloned: true, Path: repo}})
	before, err := git.Head(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, noPush := range []bool{true, false} {
		opts := east1ApplyOpts(t, repo, applyRunner(t, repo), git)
		opts.NoPush = noPush
		if err := Apply(context.Background(), opts); err != nil {
			t.Fatalf("apply with the mock git client (no-push=%v): %v", noPush, err)
		}
	}
	after, _ := git.Head(context.Background(), repo)
	if after == before {
		t.Fatal("the mock recorded no commit")
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", "east1", "cluster-config.env")); err != nil {
		t.Fatalf("no scaffold: %v", err)
	}
}

func requireSOPS(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
}

func newAgeKey(t *testing.T, path string) string {
	t.Helper()
	if out, err := exec.Command("age-keygen", "-o", path).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pub))
}

func sopsConfig(rules ...string) string {
	return "# kube-dc fleet SOPS rules\ncreation_rules:\n" + strings.Join(rules, "")
}

func sopsRule(pathRegex string, recipients ...string) string {
	return "  - path_regex: '" + pathRegex + "'\n    encrypted_regex: '^(data|stringData)$'\n    age: '" + strings.Join(recipients, ",") + "'\n"
}

// registryRunner is applyRunner plus the platform.yaml the registry listener patches.
func registryRunner(t *testing.T, repo string) *fakeScriptRunner {
	r := applyRunner(t, repo)
	inner := r.onRun
	r.onRun = func(clusterDir string) error {
		existed := true
		if _, err := os.Stat(clusterDir); err != nil {
			existed = false
		}
		if err := inner(clusterDir); err != nil || existed {
			return err
		}
		return os.WriteFile(filepath.Join(clusterDir, "platform.yaml"), []byte(genPlatformYAML), 0o644)
	}
	return r
}

func registryOpts(t *testing.T, repo string, runner ports.ScriptRunner, git ports.GitClient) ApplyOptions {
	opts := east1ApplyOpts(t, repo, runner, git)
	opts.ImageAccel = ImageAccelSpec{Enabled: true, S3: true}
	return opts
}

// A tracked directory symlink that leads out of the repository (here: back
// into the operator's own checkout) would carry a writer out of the private
// checkout. Init refuses before anything runs.
func TestApply_Isolated_RefusesSymlinkOutOfTheRepository(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	key := filepath.Join(repo, "age.key")
	pub := newAgeKey(t, key)
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	depot := filepath.Join(repo, "shared", "depot")
	for path, body := range map[string]string{
		".sops.yaml":                      sopsConfig(sopsRule(`\.enc\.yaml$`, pub)),
		"shared/depot/kustomization.yaml": "resources: []\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "platform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(depot, filepath.Join(repo, "platform", "registry-depot")); err != nil { // absolute target
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "registry-depot is a link to shared/depot")
	runner := registryRunner(t, repo)
	err := Apply(context.Background(), registryOpts(t, repo, runner, plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "platform/registry-depot") || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("want a refusal naming the symlink, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("a script ran")
	}
	if _, serr := os.Stat(filepath.Join(depot, "secret.enc.yaml")); !os.IsNotExist(serr) {
		t.Fatal("a writer followed the symlink into the operator's checkout")
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the checkout changed:\n%s", st)
	}
}

// A symlink that stays inside the repository is fine.
func TestApply_Isolated_AllowsSymlinkInsideTheRepository(t *testing.T) {
	repo := committedFleet(t)
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../clusters", filepath.Join(repo, "docs", "clusters")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "a relative link")
	opts := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	opts.NoPush = true
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// An age.key that Git does not ignore must not even be staged: staging stores
// its content in the repository's object store.
func TestApply_Isolated_UnignoredAgeKeyNeverReachesTheObjectStore(t *testing.T) {
	repo := applyFleet(t)
	runGit(t, repo, "init", "-q", "-b", "main") // no ignore rule for age.key
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "starter without ignore rules")
	const keyBody = "AGE-SECRET-KEY-LEAK-CANARY\n"
	runner := applyRunner(t, repo)
	inner := runner.onRun
	runner.onRun = func(clusterDir string) error {
		if err := inner(clusterDir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(clusterDir)), "age.key"), []byte(keyBody), 0o600)
	}
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "age.key") {
		t.Fatalf("want a refusal about age.key, got %v", err)
	}
	hash := exec.Command("git", "-C", repo, "hash-object", "--stdin")
	hash.Stdin = strings.NewReader(keyBody)
	blob, herr := hash.Output()
	if herr != nil {
		t.Fatal(herr)
	}
	if exec.Command("git", "-C", repo, "cat-file", "-e", strings.TrimSpace(string(blob))).Run() == nil {
		t.Fatal("the key's content is in the repository's object store")
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the checkout changed:\n%s", st)
	}
}

// An overlay file Git ignores is not in the commit, and the generated copy
// would be discarded with the private checkout: refuse instead.
func TestApply_Isolated_RequiredOverlayFileIgnored(t *testing.T) {
	repo := committedFleet(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("age.key\nclusters/east1/secrets.enc.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "commit", "-q", "-am", "ignore the cluster secret")
	pre := revParse(t, repo, "HEAD")
	opts := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "does not contain clusters/east1/secrets.enc.yaml") {
		t.Fatalf("want a refusal naming the missing file, got %v", err)
	}
	if revParse(t, repo, "HEAD") != pre || status(t, repo) != "" {
		t.Fatal("the checkout changed")
	}
}

// With the mock adapter too, a run that fails leaves the fleet directory as
// it was: its private checkout is a real copy.
func TestApply_MockGitClient_FailureLeavesFleetUntouched(t *testing.T) {
	repo := applyFleet(t)
	git := mock.NewGitClient(&mock.Scenario{Fleet: &mock.FleetFixture{Cloned: true, Path: repo}})
	before, _ := git.Head(context.Background(), repo)
	runner := applyRunner(t, repo)
	opts := east1ApplyOpts(t, repo, runner, git)
	opts.NoPush = true
	opts.SiteComponents = []SiteComponent{{Layer: "no-such-layer", Path: "install/x"}}
	if err := Apply(context.Background(), opts); err == nil {
		t.Fatal("expected the site component to fail")
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", "east1")); !os.IsNotExist(err) {
		t.Fatalf("the mock let a failed run write into the fleet directory: %v", err)
	}
	if after, _ := git.Head(context.Background(), repo); after != before {
		t.Fatal("the mock recorded a commit for a failed run")
	}
	// And the next run is not confused by the failed one.
	opts.SiteComponents = nil
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// Symlinks are resolved the way the filesystem resolves them: a link whose
// target runs through another link and then ".." must not pass for a path
// inside the repository.
func TestRefuseEscapingSymlinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	outside := filepath.Join(base, "scratch")
	for _, d := range []string{filepath.Join(root, "deep", "er"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, at)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, at)); err != nil {
			t.Fatal(err)
		}
	}
	// Harmless: inside the repository, also when dangling.
	link("../deep", "ok/inside")
	link("../not-there-yet.yaml", "ok/dangling")
	if err := refuseEscapingSymlinks(root); err != nil {
		t.Fatalf("links inside the repository were refused: %v", err)
	}
	for name, make := range map[string]func(){
		"absolute": func() { link(outside, "bad/abs") },
		"relative": func() { link("../../scratch", "bad/rel") },
		// "deep/back" leads to the repository root (harmless). The target
		// "../deep/back/../scratch" cleans to "deep/scratch", inside; the
		// filesystem follows "back" first and ".." then leaves the repository.
		"through another link": func() {
			link("..", "deep/back")
			link("../deep/back/../scratch", "bad/chain")
		},
		"dangling, outside": func() { link("../../scratch/not-there", "bad/dangling") },
	} {
		t.Run(name, func(t *testing.T) {
			make()
			defer os.RemoveAll(filepath.Join(root, "bad"))
			defer os.Remove(filepath.Join(root, "deep", "back"))
			err := refuseEscapingSymlinks(root)
			if err == nil || !strings.Contains(err.Error(), "outside the repository") {
				t.Fatalf("want a refusal, got %v", err)
			}
		})
	}
}

// The age key is ignored in the detached private checkout but NOT on the
// starting branch (a branch-conditional excludes file): staging uses the
// branch's policy, so the key is refused before it is staged.
func TestApply_Isolated_AgeKeyIgnoredOnlyOffTheBranch(t *testing.T) {
	repo := applyFleet(t)
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "starter without ignore rules")
	conf := t.TempDir()
	for name, body := range map[string]string{
		"default":  "age.key\n",
		"on-main":  "# nothing ignored on main\n",
		"main.inc": "[core]\n\texcludesFile = " + filepath.Join(conf, "on-main") + "\n",
	} {
		if err := os.WriteFile(filepath.Join(conf, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "config", "core.excludesFile", filepath.Join(conf, "default"))
	runGit(t, repo, "config", "includeIf.onbranch:main.path", filepath.Join(conf, "main.inc"))
	const keyBody = "AGE-SECRET-KEY-ONBRANCH-CANARY\n"
	runner := applyRunner(t, repo)
	inner := runner.onRun
	runner.onRun = func(clusterDir string) error {
		if err := inner(clusterDir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(clusterDir)), "age.key"), []byte(keyBody), 0o600)
	}
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "age.key") {
		t.Fatalf("want a refusal about age.key, got %v", err)
	}
	hash := exec.Command("git", "-C", repo, "hash-object", "--stdin")
	hash.Stdin = strings.NewReader(keyBody)
	blob, _ := hash.Output()
	if exec.Command("git", "-C", repo, "cat-file", "-e", strings.TrimSpace(string(blob))).Run() == nil {
		t.Fatal("the key's content is in the repository's object store")
	}
}

func TestOverlayTreeCheck(t *testing.T) {
	tree := func(files map[string]string) ports.TreeFileReader {
		return func(path string) ([]byte, bool, error) {
			body, ok := files[path]
			return []byte(body), ok, nil
		}
	}
	complete := map[string]string{
		"clusters/c/cluster-config.env": "CLUSTER_NAME=c\n",
		"clusters/c/secrets.enc.yaml":   "stringData:\n  a: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]\nsops:\n  mac: m\n",
		"clusters/c/tenant-addons.yaml": "kind: Kustomization\n",
		"clusters/c/kustomization.yaml": "resources:\n  - flux-system\n  - ./tenant-addons.yaml\n  - secrets.enc.yaml\n  - https://example.invalid/remote.yaml\n  - ../shared/x.yaml\n",
	}
	if err := overlayTreeCheck("c", tree(complete)); err != nil {
		t.Fatalf("a complete overlay was refused: %v", err)
	}
	for _, missing := range []string{"clusters/c/cluster-config.env", "clusters/c/secrets.enc.yaml", "clusters/c/tenant-addons.yaml", "clusters/c/kustomization.yaml"} {
		files := map[string]string{}
		for k, v := range complete {
			if k != missing {
				files[k] = v
			}
		}
		if err := overlayTreeCheck("c", tree(files)); err == nil || !strings.Contains(err.Error(), filepath.Base(missing)) {
			t.Errorf("without %s: want a refusal naming it, got %v", missing, err)
		}
	}
}

// A file the kustomization lists is ignored by Git: the commit would publish
// a reference to a file the repository does not have.
func TestApply_Isolated_ListedResourceIgnored(t *testing.T) {
	repo := committedFleet(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("age.key\nclusters/east1/platform.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "commit", "-q", "-am", "ignore a listed resource")
	opts := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "lists platform.yaml") {
		t.Fatalf("want a refusal naming the listed file, got %v", err)
	}
	if status(t, repo) != "" {
		t.Fatal("the checkout changed")
	}
}

// A resume publishes a commit it did not generate: an overlay committed
// without its secrets is not pushed.
func TestApply_Isolated_ResumeRefusesIncompleteOverlay(t *testing.T) {
	repo := committedFleet(t)
	overlay := filepath.Join(repo, "clusters", "east1")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"cluster-config.env": "CLUSTER_NAME=east1\n",
		"kustomization.yaml": "resources:\n  - secrets.enc.yaml\n",
	} {
		if err := os.WriteFile(filepath.Join(overlay, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "an overlay without its secrets")
	bare := bareOrigin(t, repo)
	runner := applyRunner(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "secrets.enc.yaml") || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want a refusal before the push, got %v", err)
	}
	if revParse(t, bare, "main") != "" || ranFluxInstall(runner) {
		t.Fatal("an incomplete overlay was pushed, or flux-install ran")
	}
}

// The private checkout could not be removed (it may hold generated secrets):
// the run stops before flux-install and says so.
func TestApply_DiscardFailureStopsBeforeFlux(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{preSHA: "pre", commitSHA: "post", discardErr: errors.New("the private checkout could not be removed")}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, git))
	if err == nil || !strings.Contains(err.Error(), "could not be removed") || !strings.Contains(err.Error(), "re-run init") {
		t.Fatalf("want the discard failure, got %v", err)
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran with the private checkout left behind")
	}
}

// The adapters a mock session pairs (mock Git + mock scripts) get through
// Apply's private-checkout setup.
func TestApply_MockSessionPairing(t *testing.T) {
	repo := applyFleet(t)
	scenario := &mock.Scenario{Fleet: &mock.FleetFixture{Cloned: true, Path: repo}}
	opts := east1ApplyOpts(t, repo, mock.NewScriptRunner(scenario, nil), mock.NewGitClient(scenario))
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err != nil && (strings.Contains(err.Error(), "private checkout") || strings.Contains(err.Error(), "cannot")) {
		t.Fatalf("the mock pairing does not get past the private-checkout setup: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(repo, "clusters", "east1")); err != nil && !os.IsNotExist(serr) {
		t.Fatalf("a failed mock run wrote into the fleet directory: %v", serr)
	}
}

// A dangling link whose target climbs back out of a directory that does not
// exist yet: once the scaffold creates that directory, the filesystem follows
// a link the lexical reading skipped.
func TestRefuseEscapingSymlinks_DanglingWithParentTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	if err := os.MkdirAll(filepath.Join(root, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(root, "deep", "back")); err != nil { // -> root: harmless
		t.Fatal(err)
	}
	// clusters/east1 does not exist yet. Lexically the target is deep/canary,
	// inside. After `mkdir -p clusters/east1`: east1/../.. is root, deep/back
	// is root, and ".." leaves it.
	if err := os.Symlink("clusters/east1/../../deep/back/../canary", filepath.Join(root, "probe")); err != nil {
		t.Fatal(err)
	}
	err := refuseEscapingSymlinks(root)
	if err == nil || !strings.Contains(err.Error(), "probe") {
		t.Fatalf("want a refusal naming the link, got %v", err)
	}
}

func TestSecretsEncrypted(t *testing.T) {
	const enc = "ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]"
	for name, tc := range map[string]struct {
		manifest string
		ok       bool
	}{
		"fully encrypted":             {"kind: Secret\nstringData:\n  a: " + enc + "\n  b: " + enc + "\nsops:\n  mac: " + enc + "\n", true},
		"data section":                {"kind: Secret\ndata:\n  a: " + enc + "\nsops:\n  mac: " + enc + "\n", true},
		"last-applied annotation":     {"kind: Secret\nmetadata:\n  name: s\n  annotations:\n    kubectl.kubernetes.io/last-applied-configuration: '{\"data\":{\"a\":\"aHVudGVyMg==\"}}'\ndata:\n  a: " + enc + "\nsops:\n  mac: " + enc + "\n", false},
		"other annotations":           {"kind: Secret\nmetadata:\n  name: s\n  annotations:\n    example.net/owner: ops\ndata:\n  a: " + enc + "\nsops:\n  mac: " + enc + "\n", true},
		"one value in clear":          {"kind: Secret\nstringData:\n  a: " + enc + "\n  KEYCLOAK_ADMIN_PASSWORD: hunter2\nsops:\n  mac: " + enc + "\n", false},
		"no sops metadata":            {"kind: Secret\nstringData:\n  a: " + enc + "\n", false},
		"plaintext":                   {"kind: Secret\nstringData:\n  KEYCLOAK_ADMIN_PASSWORD: hunter2\n", false},
		"second doc in clear":         {"kind: Secret\nstringData:\n  a: " + enc + "\nsops:\n  mac: " + enc + "\n---\nkind: Secret\nstringData:\n  b: clear\nsops:\n  mac: " + enc + "\n", false},
		"non-string value":            {"kind: Secret\nstringData:\n  a: 12345\nsops:\n  mac: " + enc + "\n", false},
		"empty":                       {"", false},
		"numeric key hides it":        {"kind: Secret\nstringData:\n  2026: example\n  PASSWORD: plaintext\nsops:\n  mac: " + enc + "\n", false},
		"cleartext after it":          {"kind: Secret\nstringData:\n  a: \"" + enc + " hunter2\"\nsops:\n  mac: " + enc + "\n", false},
		"section not a map":           {"kind: Secret\nstringData: plaintext\nsops:\n  mac: " + enc + "\n", false},
		"empty sops":                  {"kind: Secret\nstringData:\n  a: " + enc + "\nsops: {}\n", false},
		"no sections, sops":           {"kind: Config\nusers: " + enc + "\nsops:\n  mac: " + enc + "\n", true},
		"merge key hides it":          {"kind: Secret\n<<: {stringData: {PASSWORD: cleartext}}\nsops:\n  mac: " + enc + "\n", false},
		"alias":                       {"kind: Secret\nx: &v cleartext\nstringData:\n  a: *v\nsops:\n  mac: " + enc + "\n", false},
		"null section":                {"kind: Secret\nstringData:\nsops:\n  mac: " + enc + "\n", false},
		"kubeconfig, token":           {"kind: Config\nusers:\n  - name: a\n    user:\n      token: " + enc + "\nsops:\n  mac: " + enc + "\n", true},
		"kubeconfig, cleartext token": {"kind: Config\nusers:\n  - name: a\n    user:\n      token: eyJhbGciOi\nsops:\n  mac: " + enc + "\n", false},
		"kubeconfig, cleartext key":   {"kind: Config\nusers:\n  - name: a\n    user:\n      client-key-data: LS0tLS1CRUdJTg==\nsops:\n  mac: " + enc + "\n", false},
		"not yaml":                    {"{{{", false},
	} {
		if err := secretsEncrypted([]byte(tc.manifest)); (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v, got err=%v", name, tc.ok, err)
		}
	}
}

func TestOverlayTreeCheck_ListedFileWithDotsInItsName(t *testing.T) {
	const enc = "ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]"
	files := map[string]string{
		"clusters/c/cluster-config.env": "CLUSTER_NAME=c\n",
		"clusters/c/secrets.enc.yaml":   "stringData:\n  a: " + enc + "\nsops:\n  mac: " + enc + "\n",
		"clusters/c/kustomization.yaml": "resources:\n  - policy..v2.yaml\n",
	}
	tree := func(path string) ([]byte, bool, error) {
		body, ok := files[path]
		return []byte(body), ok, nil
	}
	if err := overlayTreeCheck("c", tree); err == nil || !strings.Contains(err.Error(), "policy..v2.yaml") {
		t.Fatalf("a listed file with '..' inside its name must still be required, got %v", err)
	}
}

// A plain resume publishes a commit this run did not make. One that tracks
// the private key, or whose secrets are in the clear, is not pushed.
func TestApply_Isolated_ResumeRefusesKeyOrPlaintextSecrets(t *testing.T) {
	for name, tc := range map[string]struct {
		commit func(t *testing.T, repo string)
		want   string
	}{
		"a tracked age.key": {func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, "age.key"), []byte("AGE-SECRET-KEY-TEST\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "-f", "age.key")
		}, "age.key"},
		"secrets in the clear": {func(t *testing.T, repo string) {
			p := filepath.Join(repo, "clusters", "east1", "secrets.enc.yaml")
			if err := os.WriteFile(p, []byte("kind: Secret\nstringData:\n    KEYCLOAK_ADMIN_PASSWORD: hunter2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "clusters/east1/secrets.enc.yaml")
		}, "secrets.enc.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := committedFleet(t)
			first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
			first.NoPush = true
			if err := Apply(context.Background(), first); err != nil {
				t.Fatalf("run 1 (--no-push): %v", err)
			}
			tc.commit(t, repo)
			runGit(t, repo, "commit", "-q", "-m", "an unpublished local commit")
			bare := bareOrigin(t, repo)
			runner := applyRunner(t, repo)
			err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()}))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was pushed") {
				t.Fatalf("want a refusal about %s before the push, got %v", tc.want, err)
			}
			if revParse(t, bare, "main") != "" || ranFluxInstall(runner) {
				t.Fatal("the commit was pushed, or flux-install ran")
			}
		})
	}
}

// Every SOPS-managed file of the overlay is checked, not only secrets.enc.yaml:
// a resume must not publish a decrypted TLS key that was committed locally.
func TestApply_Isolated_ResumeRefusesAnyDecryptedSecret(t *testing.T) {
	repo := committedFleet(t)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	p := filepath.Join(repo, "clusters", "east1", "wildcard-tls-secrets.enc.yaml")
	if err := os.WriteFile(p, []byte("kind: Secret\nstringData:\n  tls.key: -----BEGIN PRIVATE KEY-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "clusters/east1/wildcard-tls-secrets.enc.yaml")
	runGit(t, repo, "commit", "-q", "-m", "a decrypted TLS secret, committed by mistake")
	bare := bareOrigin(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "wildcard-tls-secrets.enc.yaml") || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want a refusal naming the file before the push, got %v", err)
	}
	if revParse(t, bare, "main") != "" {
		t.Fatal("the decrypted secret was pushed")
	}
}

// The private key was committed and then removed again, locally: the tip is
// clean, but the push would publish the commit that has it.
func TestApply_Isolated_ResumeRefusesKeyInUnpublishedHistory(t *testing.T) {
	repo := committedFleet(t)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "age.key"), []byte("AGE-SECRET-KEY-TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-f", "age.key")
	runGit(t, repo, "commit", "-q", "-m", "the key, by mistake")
	runGit(t, repo, "rm", "-q", "--cached", "age.key")
	runGit(t, repo, "commit", "-q", "-m", "remove it again")
	bare := bareOrigin(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "contains age.key") {
		t.Fatalf("want a refusal about the unpublished history, got %v", err)
	}
	if revParse(t, bare, "main") != "" {
		t.Fatal("history containing the key was pushed")
	}
}

// A decrypted credential was committed locally and re-encrypted in a later
// commit: the tip is fine, but the push would publish the earlier version.
func TestApply_Isolated_ResumeRefusesPlaintextInUnpublishedHistory(t *testing.T) {
	const enc = "ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]"
	repo := committedFleet(t)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	p := filepath.Join(repo, "clusters", "east1", "wildcard-tls-secrets.enc.yaml")
	for _, version := range []string{
		"kind: Secret\nstringData:\n  tls.key: -----BEGIN PRIVATE KEY-----\n",
		"kind: Secret\nstringData:\n  tls.key: " + enc + "\nsops:\n  mac: " + enc + "\n",
	} {
		if err := os.WriteFile(p, []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "clusters/east1/wildcard-tls-secrets.enc.yaml")
		runGit(t, repo, "commit", "-q", "-m", "tls secret")
	}
	bare := bareOrigin(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "wildcard-tls-secrets.enc.yaml") || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want a refusal naming the file before the push, got %v", err)
	}
	if revParse(t, bare, "main") != "" {
		t.Fatal("history containing the decrypted secret was pushed")
	}
}

// A fresh install publishes fully encrypted secrets and passes the scan; the
// SOPS lock file is kept in Git's own directory, never in the checkout, even
// when TMPDIR and the cache directory point into the checkout.
func TestApply_Isolated_LockFilesStayOutOfTheCheckout(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	key := filepath.Join(repo, "age.key")
	pub := newAgeKey(t, key)
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	for path, body := range map[string]string{
		".sops.yaml": sopsConfig(sopsRule(`\.enc\.yaml$`, pub)),
		"platform/registry-depot/kustomization.yaml": "resources: []\n",
		"scratch/.gitkeep":                           "",
	} {
		p := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "sops rules, registry-depot, a scratch directory")
	t.Setenv("TMPDIR", filepath.Join(repo, "scratch"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(repo, "scratch"))
	runGit(t, repo, "remote", "add", "origin", "file://"+filepath.Join(t.TempDir(), "missing.git"))
	err := Apply(context.Background(), registryOpts(t, repo, registryRunner(t, repo), plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "commit-push") {
		t.Fatalf("want the push failure, got %v", err)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the failed run left files in the checkout:\n%s", st)
	}
}

// The lock directory inside Git's directory cannot be created (something else
// is at that path): the write fails; it does not fall back to a place that may
// be inside the checkout.
func TestSopsLock_NoFallbackInsideARepository(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	pub := newAgeKey(t, filepath.Join(repo, "age.key"))
	for path, body := range map[string]string{
		".sops.yaml": sopsConfig(sopsRule(`\.enc\.yaml$`, pub)),
		"platform/registry-depot/kustomization.yaml": "resources: []\n",
		"scratch/.gitkeep":                           "",
	} {
		p := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "setup")
	if err := os.WriteFile(filepath.Join(repo, ".git", "kube-dc-init-locks"), []byte("in the way\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(repo, "scratch"))
	t.Setenv("TMPDIR", filepath.Join(repo, "scratch"))
	if err := ensureRegistryDepotSecret(repo, io.Discard); err == nil {
		t.Fatal("want the write to fail when its lock cannot be taken in Git's directory")
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("a lock file or a secret was left in the checkout:\n%s", st)
	}
}

// While the push call asks the remote what it has, the operator amends the
// starting commit (to withdraw a draft). The check of the checkout repeated
// right before sending catches it: the original commit is not published.
func TestApply_Isolated_BranchAmendedWhileThePushIsChecked(t *testing.T) {
	repo := committedFleet(t)
	bare := bareOrigin(t, repo)
	git := duringPushGit{plainGit{realGit()}, func() {
		runGit(t, repo, "commit", "-q", "--amend", "--allow-empty", "-m", "starter, amended")
	}}
	err := Apply(context.Background(), east1ApplyOpts(t, repo, applyRunner(t, repo), git))
	if !errors.Is(err, ErrCheckoutMoved) || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want ErrCheckoutMoved before anything is sent, got %v", err)
	}
	if revParse(t, bare, "main") != "" {
		t.Fatal("a commit built on the withdrawn one was pushed")
	}
}

// While the private checkout exists, TMPDIR is the run's own scratch
// directory in Git's directory, wherever the operator's TMPDIR points; it is
// restored and removed afterwards, and running twice does not pile up lock
// files.
func TestApply_Isolated_RunOwnedTempDir(t *testing.T) {
	requireSOPS(t)
	repo := committedFleet(t)
	key := filepath.Join(repo, "age.key")
	pub := newAgeKey(t, key)
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	for path, body := range map[string]string{
		".sops.yaml": sopsConfig(sopsRule(`\.enc\.yaml$`, pub)),
		"platform/registry-depot/kustomization.yaml": "resources: []\n",
		"scratch/.gitkeep":                           "",
	} {
		p := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "setup")
	operatorTemp := filepath.Join(repo, "scratch") // inside the checkout
	t.Setenv("TMPDIR", operatorTemp)
	runGit(t, repo, "remote", "add", "origin", "file://"+filepath.Join(t.TempDir(), "missing.git"))

	var during []string
	for run := 0; run < 2; run++ {
		runner := registryRunner(t, repo)
		inner := runner.onRun
		runner.onRun = func(clusterDir string) error {
			tmp := os.Getenv("TMPDIR")
			during = append(during, tmp)
			// What a killed subprocess would leave behind.
			_ = os.WriteFile(filepath.Join(tmp, "left-by-a-killed-render"), []byte("x"), 0o600)
			return inner(clusterDir)
		}
		if err := Apply(context.Background(), registryOpts(t, repo, runner, plainGit{realGit()})); err == nil {
			t.Fatal("want the push failure")
		}
		// The real script runner passes only selected host variables on:
		// the scaffold must hand TMPDIR to the script explicitly.
		if got := runner.calls[0].Env["TMPDIR"]; got != during[len(during)-1] || got == "" {
			t.Fatalf("add-cluster.sh was not given the run's TMPDIR: env has %q", got)
		}
		if got := runner.calls[0].Env["KUBECACHEDIR"]; !strings.HasPrefix(got, during[len(during)-1]+string(filepath.Separator)) {
			t.Fatalf("kubectl's cache was not sent to the run's scratch directory: %q", got)
		}
	}
	for _, tmp := range during {
		if !strings.HasPrefix(tmp, filepath.Join(repo, ".git", "kube-dc-init")+string(filepath.Separator)) {
			t.Fatalf("subprocesses got TMPDIR=%s, not the run's own directory", tmp)
		}
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Fatalf("the run's scratch directory %s was not removed", tmp)
		}
	}
	if got := os.Getenv("TMPDIR"); got != operatorTemp {
		t.Fatalf("TMPDIR was not restored: %s", got)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the failed runs left files in the checkout:\n%s", st)
	}
	locks, _ := os.ReadDir(filepath.Join(repo, ".git", "kube-dc-init-locks"))
	if len(locks) != 1 {
		t.Fatalf("lock files must not pile up run after run: %d after two runs", len(locks))
	}
}

// The mismatch error names the repositories, never the URL: a push URL can
// carry a credential.
func TestPushDestinationMatches_DoesNotPrintTheURL(t *testing.T) {
	err := pushDestinationMatches("https://ghp_SECRETTOKEN@github.com/operator/corp-fleet.git", "acme", "corp-fleet")
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") || !strings.Contains(err.Error(), "operator/corp-fleet") {
		t.Fatalf("want a mismatch that names the repositories without the credential, got %v", err)
	}
}

func TestPushDestinationMatches(t *testing.T) {
	for _, tc := range []struct {
		url, owner, repo string
		ok               bool
	}{
		{"https://github.com/acme/corp-fleet.git", "acme", "corp-fleet", true},
		{"https://github.com/Acme/Corp-Fleet", "acme", "corp-fleet", true},
		{"git@github.com:acme/corp-fleet.git", "acme", "corp-fleet", true},
		{"ssh://git@github.com/acme/corp-fleet.git", "acme", "corp-fleet", true},
		{"https://gitlab.example.com/group/sub/fleet.git", "group/sub", "fleet", true},
		{"https://github.com/operator/corp-fleet.git", "acme", "corp-fleet", false}, // a fork
		{"git@github.com:operator/corp-fleet.git", "acme", "corp-fleet", false},
		{"https://github.com/acme/other.git", "acme", "corp-fleet", false},
		{"deploy@git.example.net:.git", "acme", "corp-fleet", false}, // SSH to a mirror in a home directory: no project path
		{"ssh://deploy@git.example.net/.git", "acme", "corp-fleet", false},
		{"https://git.example.net/.git", "acme", "corp-fleet", false},
		{"https://git.example.net/", "acme", "corp-fleet", false},
		{"/srv/git/fleet.git", "acme", "corp-fleet", true}, // a local path names no hosted repository
		{"file:///srv/git/fleet.git", "acme", "corp-fleet", true},
		{"https://github.com/operator/x.git", "", "", true}, // no coordinates requested
	} {
		if err := pushDestinationMatches(tc.url, tc.owner, tc.repo); (err == nil) != tc.ok {
			t.Errorf("%s vs %s/%s: ok=%v, got %v", tc.url, tc.owner, tc.repo, tc.ok, err)
		}
	}
}
