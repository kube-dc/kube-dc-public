package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePuller simulates `flux pull artifact` by writing a configurable
// file set into the output dir.
type fakePuller struct {
	files    []string          // relative paths to create on Pull; nil = starter shape
	contents map[string]string // optional per-path content override
	err      error
	called   int
	gotURL   string
}

func (f *fakePuller) PullArtifact(_ context.Context, url, dir string) error {
	f.called++
	f.gotURL = url
	// Writes BEFORE returning err — a real interrupted `flux pull`
	// leaves partial content in the output dir, and the atomicity test
	// depends on that being simulated (review P2 2026-07-20: the old
	// err-first ordering made the partial-pull test vacuous).
	files := f.files
	if files == nil {
		files = testStarterFiles("clusters/.gitkeep", ".gitignore")
	}
	for _, rel := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		content := "x\n"
		if rel == ".gitignore" {
			content = "age.key\n" // the real artifact's P0-3 invariant
		}
		if c, ok := f.contents[rel]; ok {
			content = c
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return f.err
}

func testStarterFiles(extra ...string) []string {
	files := append([]string{}, starterShapeMarkers...)
	return append(files, extra...)
}

// fakeStarterGit records Init/Commit calls.
type fakeStarterGit struct {
	initDir, initBranch string
	commitMsg           string
	initErr, commitErr  error
}

func (g *fakeStarterGit) Init(_ context.Context, dir, branch string) error {
	g.initDir, g.initBranch = dir, branch
	return g.initErr
}
func (g *fakeStarterGit) Commit(_ context.Context, _, msg string) (string, error) {
	g.commitMsg = msg
	return "deadbeef", g.commitErr
}

const testStarterRef = "oci://ghcr.io/kube-dc/fleet-starter:v0.5.0"

func TestEnsureStarter_ShapePresentSkips(t *testing.T) {
	// An operator-managed checkout (the legacy rsync flow) must pass
	// through untouched — no pull, no git mutation. The checkout has a
	// .git (a real checkout always does); a shape WITHOUT .git takes
	// the repair path — separate test below.
	dir := t.TempDir()
	for _, p := range testStarterFiles(".git/HEAD") {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	puller := &fakePuller{}
	git := &fakeStarterGit{}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: git,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Extracted {
		t.Errorf("expected no-op for present shape")
	}
	if puller.called != 0 {
		t.Errorf("expected no PullArtifact call, got %d", puller.called)
	}
	if git.initDir != "" || git.commitMsg != "" {
		t.Errorf("expected no git mutation on skip")
	}
}

func TestEnsureStarter_ExtractsIntoMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-fleet")
	puller := &fakePuller{}
	git := &fakeStarterGit{}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: git,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Extracted || res.Ref != testStarterRef {
		t.Errorf("result = %+v, want Extracted with ref", res)
	}
	if puller.gotURL != testStarterRef {
		t.Errorf("pulled %q, want %q", puller.gotURL, testStarterRef)
	}
	if git.initBranch != "main" {
		t.Errorf("git init branch = %q, want main (B5: deterministic branch)", git.initBranch)
	}
	if !strings.Contains(git.commitMsg, testStarterRef) {
		t.Errorf("initial commit %q should record the starter ref", git.commitMsg)
	}
	if _, err := os.Stat(filepath.Join(dir, "clusters")); err != nil {
		t.Errorf("clusters/ must exist post-extract: %v", err)
	}
}

func TestEnsureStarter_BenignPreseedAllowed(t *testing.T) {
	// A fresh `git init` + pre-placed README/docs is the documented
	// pre-seed shape — extraction proceeds.
	dir := t.TempDir()
	for _, p := range []string{".git/HEAD", "README.md", "docs/topology.md"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: &fakePuller{}, Git: &fakeStarterGit{},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Extracted {
		t.Errorf("expected extraction over benign pre-seed")
	}
}

func TestEnsureStarter_RefusesPopulatedDir(t *testing.T) {
	// --repo pointing at a random populated dir ($HOME typo) must
	// refuse loudly, naming the offenders, and never pull.
	dir := t.TempDir()
	for _, p := range []string{"Downloads/x", ".bashrc"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	puller := &fakePuller{}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	})
	if !errors.Is(err, ErrStarterDirNotEmpty) {
		t.Fatalf("expected ErrStarterDirNotEmpty, got %v", err)
	}
	for _, name := range []string{".bashrc", "Downloads"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should name offender %q: %v", name, err)
		}
	}
	if puller.called != 0 {
		t.Errorf("must not pull into a refused dir")
	}
}

func TestEnsureStarter_InvalidArtifactFailsWithRef(t *testing.T) {
	// A wrong/truncated artifact must fail at the starter step, naming
	// the ref and what's missing — not later inside add-cluster.sh.
	dir := filepath.Join(t.TempDir(), "fleet")
	puller := &fakePuller{files: []string{"README.md"}} // no shape
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	})
	if !errors.Is(err, ErrStarterArtifactInvalid) {
		t.Fatalf("expected ErrStarterArtifactInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), testStarterRef) {
		t.Errorf("error should carry the ref: %v", err)
	}
	if !strings.Contains(err.Error(), "bootstrap/add-cluster.sh") {
		t.Errorf("error should name the missing marker: %v", err)
	}
}

func TestEnsureStarter_PullErrorPropagates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	sentinel := errors.New("boom: registry unreachable")
	git := &fakeStarterGit{}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: &fakePuller{err: sentinel}, Git: git,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected pull error to propagate, got %v", err)
	}
	if git.commitMsg != "" {
		t.Errorf("must not commit after a failed pull")
	}
}

func TestEnsureStarter_ShapeWithoutGitRepaired(t *testing.T) {
	// A prior run that extracted but died before git-init leaves the
	// shape present with NO .git. Skipping acquisition is right, but
	// the repo must still get init+commit (review P1: the engine's
	// later steps need a usable repository).
	dir := t.TempDir()
	for _, p := range starterShapeMarkers {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	puller := &fakePuller{}
	git := &fakeStarterGit{}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: git,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Extracted {
		t.Errorf("repair path must not re-pull")
	}
	if puller.called != 0 {
		t.Errorf("expected no PullArtifact call on repair")
	}
	if git.initBranch != "main" {
		t.Errorf("repair must git-init on main, got %q", git.initBranch)
	}
	if !strings.Contains(git.commitMsg, "repaired") {
		t.Errorf("repair must commit, got %q", git.commitMsg)
	}
}

func TestEnsureStarter_PartialPullLeavesRepoClean(t *testing.T) {
	// Atomicity (review P1): a pull that dies mid-extraction must leave
	// RepoPath untouched so the NEXT run isn't refused by the
	// populated-dir guard. The fake writes a partial tree into the
	// scratch dir and then errors.
	parent := t.TempDir()
	dir := filepath.Join(parent, "fleet")
	puller := &fakePuller{files: []string{"bootstrap/half-written.sh"}, err: errors.New("boom: interrupted")}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	})
	if err == nil {
		t.Fatalf("expected pull error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("RepoPath dirtied by failed pull: %v", entries)
	}
	// And the immediate re-run must be able to proceed (not refused).
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: &fakePuller{}, Git: &fakeStarterGit{},
	})
	if err != nil || !res.Extracted {
		t.Fatalf("re-run after failed pull should extract cleanly, got res=%+v err=%v", res, err)
	}
}

func TestEnsureStarter_PartialLeftoversCleanedAndResumed(t *testing.T) {
	// An interrupted PROMOTION leaves some starter-owned trees moved
	// but the shape incomplete. The next run must clean them and
	// re-pull — not refuse (review P1: retry was wedged).
	dir := t.TempDir()
	for _, p := range []string{"bootstrap/add-cluster.sh", "addons/x", ".starter-version"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: &fakePuller{}, Git: &fakeStarterGit{},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Extracted {
		t.Errorf("expected clean+re-pull after partial promotion")
	}
	b, err := os.ReadFile(filepath.Join(dir, "bootstrap", "add-cluster.sh"))
	if err != nil || string(b) == "stale" {
		t.Errorf("stale leftover survived the resume: %q err=%v", b, err)
	}
}

func TestEnsureStarter_UnbornHeadRepaired(t *testing.T) {
	// Shape present, .git present, but HEAD unborn (prior run's Init
	// succeeded, Commit failed). Must commit — not silently skip.
	dir := t.TempDir()
	for _, p := range starterShapeMarkers {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// unborn: HEAD points at refs/heads/main; no loose ref, no packed-refs
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := &fakeStarterGit{}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: &fakePuller{}, Git: git,
	}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(git.commitMsg, "repaired") {
		t.Errorf("unborn HEAD must trigger a repair commit, got %q", git.commitMsg)
	}
}

func TestEnsureStarter_PreseedReadmeAndGitignorePreserved(t *testing.T) {
	// Operator preseed: README.md must WIN; .gitignore must be MERGED
	// (operator lines first, starter's age.key appended).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("MY TOPOLOGY NOTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("my-scratch/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	puller := &fakePuller{files: testStarterFiles("clusters/.gitkeep", ".gitignore", "README.md")}
	if _, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if string(readme) != "MY TOPOLOGY NOTES" {
		t.Errorf("preseeded README clobbered: %q", readme)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasPrefix(string(gi), "my-scratch/") {
		t.Errorf("operator gitignore lines must stay first: %q", gi)
	}
	if !strings.Contains(string(gi), "age.key") {
		t.Errorf("merged gitignore must carry age.key: %q", gi)
	}
}

// --- artifact format v2: .starter-manifest verification ---

// manifestFor renders a valid manifest for the fake puller's default
// content ("x\n") over the given paths.
func manifestFor(paths ...string) string {
	sum := sha256.Sum256([]byte("x\n"))
	h := hex.EncodeToString(sum[:])
	var b strings.Builder
	b.WriteString("# kube-dc fleet-starter manifest (format 1)\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "644 %s %s\n", h, p)
	}
	return b.String()
}

func TestEnsureStarter_ValidManifestAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	puller := &fakePuller{
		files:    testStarterFiles("clusters/.gitkeep", ".gitignore", ".starter-manifest"),
		contents: map[string]string{".starter-manifest": manifestFor(starterShapeMarkers...)},
	}
	res, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	})
	if err != nil {
		t.Fatalf("valid manifest must extract: %v", err)
	}
	if !res.Extracted {
		t.Fatal("expected extraction")
	}
	// The baseline must land in the repo (committed with the tree).
	if _, err := os.Stat(filepath.Join(dir, ".starter-manifest")); err != nil {
		t.Errorf("manifest must be promoted into the repo: %v", err)
	}
}

func TestEnsureStarter_ManifestHashMismatchRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	// Replace the FIRST entry's hash with zeros (line 0 is the header).
	lines := strings.Split(manifestFor(starterShapeMarkers...), "\n")
	lines[1] = "644 0000000000000000000000000000000000000000000000000000000000000000 " + starterShapeMarkers[0]
	bad := strings.Join(lines, "\n")
	puller := &fakePuller{
		files:    testStarterFiles("clusters/.gitkeep", ".gitignore", ".starter-manifest"),
		contents: map[string]string{".starter-manifest": bad},
	}
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: testStarterRef, Flux: puller, Git: &fakeStarterGit{},
	})
	if !errors.Is(err, ErrStarterArtifactInvalid) {
		t.Fatalf("want ErrStarterArtifactInvalid on hash mismatch, got %v", err)
	}
	// Atomicity: validation fails pre-promotion, so the repo dir stays clean.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("repo dir must stay clean on refused artifact, has %d entries", len(entries))
	}
}

func TestVerifyStarterManifest(t *testing.T) {
	write := func(t *testing.T, dir, rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("x\n"))
	xhash := hex.EncodeToString(sum[:])

	t.Run("absent manifest is tolerated (v1 artifact)", func(t *testing.T) {
		if err := verifyStarterManifest(t.TempDir()); err != nil {
			t.Fatalf("v1 artifact must pass: %v", err)
		}
	})
	t.Run("listed file missing", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest", "644 "+xhash+" platform/kustomization.yaml\n")
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("want missing-file error, got %v", err)
		}
	})
	t.Run("unlisted extra under a covered tree", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "platform/kustomization.yaml", "x\n")
		write(t, dir, "platform/rogue.yaml", "x\n")
		write(t, dir, ".starter-manifest", "644 "+xhash+" platform/kustomization.yaml\n")
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "unlisted") {
			t.Fatalf("want unlisted-file error, got %v", err)
		}
	})
	t.Run("unsafe path refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest", "644 "+xhash+" ../escape.yaml\n")
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("want unsafe-path error, got %v", err)
		}
	})
	t.Run("malformed line refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest", "not-a-manifest-line\n")
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("want malformed error, got %v", err)
		}
	})
	t.Run("empty manifest refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest", "# only comments\n")
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "no entries") {
			t.Fatalf("want no-entries error, got %v", err)
		}
	})
}

// TestVerifyStarterManifest_AgainstExternalDir round-trips the PUBLISHER's
// real output through the CLI verifier: run
//
//	KEEP_STAGE=1 cicd/release/publish-starter vX.Y.Z   # dry-run, prints "stage kept: <dir>"
//	KDC_STARTER_VERIFY_DIR=<dir> go test -run AgainstExternalDir ./internal/bootstrap/clusterinit/
//
// Skipped unless the env var points at a staged artifact — the hermetic
// format tests above cover both sides in CI; this catches format drift
// between the shell generator and this parser against real content.
func TestVerifyStarterManifest_AgainstExternalDir(t *testing.T) {
	dir := os.Getenv("KDC_STARTER_VERIFY_DIR")
	if dir == "" {
		t.Skip("set KDC_STARTER_VERIFY_DIR to a KEEP_STAGE'd publish-starter stage")
	}
	if err := verifyStarterManifest(dir); err != nil {
		t.Fatalf("real staged artifact failed CLI verification: %v", err)
	}
}

// --- codex-review hardening: schema awareness, types, duplicates ---

func TestVerifyStarterManifest_Hardening(t *testing.T) {
	write := func(t *testing.T, dir, rel, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("x\n"))
	xhash := hex.EncodeToString(sum[:])

	t.Run("v2 artifact missing its manifest is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-version", "schemaVersion: 2\ntag: v9.9.9\nmanifestSha256: "+xhash+"\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "ships no .starter-manifest") {
			t.Fatalf("stripping the manifest must not demote to v1: %v", err)
		}
	})
	t.Run("manifestSha256 mismatch is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "platform/kustomization.yaml", "x\n", 0o644)
		write(t, dir, ".starter-manifest", "644 "+xhash+" platform/kustomization.yaml\n", 0o644)
		write(t, dir, ".starter-version", "schemaVersion: 2\nmanifestSha256: "+strings.Repeat("0", 64)+"\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "does not match the declared") {
			t.Fatalf("tampered manifest must fail the declared sha: %v", err)
		}
	})
	t.Run("v2 with matching manifestSha256 passes", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "platform/kustomization.yaml", "x\n", 0o644)
		manifest := "644 " + xhash + " platform/kustomization.yaml\n"
		msum := sha256.Sum256([]byte(manifest))
		write(t, dir, ".starter-manifest", manifest, 0o644)
		write(t, dir, ".starter-version", "schemaVersion: 2\nmanifestSha256: "+hex.EncodeToString(msum[:])+"\n", 0o644)
		if err := verifyStarterManifest(dir); err != nil {
			t.Fatalf("authentic v2 artifact must pass: %v", err)
		}
	})
	t.Run("truncated manifest fails on fixed-tree coverage", func(t *testing.T) {
		// Coverage must come from the FIXED tree set, not the manifest's own
		// entries — a manifest listing only bootstrap/ must still flag
		// platform/ files as unlisted.
		dir := t.TempDir()
		write(t, dir, "bootstrap/add-cluster.sh", "x\n", 0o644)
		write(t, dir, "platform/kustomization.yaml", "x\n", 0o644)
		write(t, dir, ".starter-manifest", "644 "+xhash+" bootstrap/add-cluster.sh\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "unlisted") {
			t.Fatalf("truncated manifest must fail: %v", err)
		}
	})
	t.Run("listed symlink is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "platform/real.yaml", "x\n", 0o644)
		if err := os.Symlink("real.yaml", filepath.Join(dir, "platform", "kustomization.yaml")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		write(t, dir, ".starter-manifest",
			"644 "+xhash+" platform/kustomization.yaml\n644 "+xhash+" platform/real.yaml\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("symlink must be refused even with a matching target hash: %v", err)
		}
	})
	t.Run("duplicate entry is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest",
			"644 "+xhash+" platform/a.yaml\n644 "+xhash+" platform/a.yaml\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Fatalf("duplicate must be refused: %v", err)
		}
	})
	t.Run("case-fold duplicate is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest",
			"644 "+xhash+" platform/a.yaml\n644 "+xhash+" platform/A.yaml\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "case-insensitive") {
			t.Fatalf("case-fold duplicate must be refused: %v", err)
		}
	})
	t.Run("executable bit mismatch is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "bootstrap/add-cluster.sh", "x\n", 0o644) // manifest says 755
		write(t, dir, ".starter-manifest", "755 "+xhash+" bootstrap/add-cluster.sh\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "executable bit") {
			t.Fatalf("stripped exec bit must be refused: %v", err)
		}
	})
	t.Run("bad mode grammar is malformed", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".starter-manifest", "banana "+xhash+" platform/a.yaml\n", 0o644)
		if err := verifyStarterManifest(dir); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("bad mode must be malformed: %v", err)
		}
	})
}

func TestEnsureStarter_RecordsInstalledRef(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	ref := "oci://ghcr.io/kube-dc/fleet-starter:v0.5.0@sha256:" + strings.Repeat("ab", 32)
	_, err := EnsureStarter(context.Background(), EnsureStarterOptions{
		RepoPath: dir, Ref: ref, Flux: &fakePuller{}, Git: &fakeStarterGit{},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".starter-version"))
	if err != nil {
		t.Fatalf(".starter-version must exist post-extract: %v", err)
	}
	if !strings.Contains(string(b), "installedRef: "+ref) {
		t.Errorf(".starter-version must record the installed ref:\n%s", b)
	}
	if !strings.Contains(string(b), "installedDigest: sha256:"+strings.Repeat("ab", 32)) {
		t.Errorf(".starter-version must record the digest of a digest-pinned ref:\n%s", b)
	}
}
