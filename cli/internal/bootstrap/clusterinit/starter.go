package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fleet-starter acquisition (installer-ux-prd "Fleet starter template",
// makes the M4-T10 plan line "(in-process) extract fleet-starter OCI
// bundle" real).
//
// `bootstrap init` greenfield modes (new-repo / existing-repo) need the
// SHARED fleet trees — bootstrap/ scripts, infrastructure/, platform/,
// addons/, scripts/ — present in --repo before anything else runs:
// add-cluster.sh lives in bootstrap/, install-prerequisites.sh in
// scripts/, and the scaffolded clusters/<name>/ Flux Kustomizations
// reference ../infrastructure + ../platform by path. Historically that
// shape arrived via a MANUAL rsync from the private kube-dc-fleet
// checkout (e2e runbook §4.2) — impossible for a customer. EnsureStarter
// replaces the rsync: when the shape is absent, it pulls the published
// fleet-starter OCI artifact (oci://ghcr.io/kube-dc/fleet-starter:<ver>,
// built by cicd/release/publish-starter) via `flux pull artifact` and
// git-inits the result so the engine's later commit+push/flux-bootstrap
// steps see a normal repo on a deterministic branch (B5).
//
// Contract:
//   - Shape already present → no-op (an operator-managed checkout —
//     including the legacy rsync flow — keeps working unchanged).
//   - Target dir must be empty or contain only benign pre-seeded
//     entries (.git, README*, docs/, clusters/, LICENSE, .gitignore) —
//     extracting into an arbitrary populated dir (--repo=$HOME typo)
//     is refused loudly rather than splatting files.
//   - Post-extract the shape is validated; a truncated/wrong artifact
//     fails here, not three steps later inside add-cluster.sh.

// ErrStarterDirNotEmpty is returned when --repo points at a populated
// directory that is neither a fleet checkout nor a benign pre-seed.
var ErrStarterDirNotEmpty = errors.New("init: starter target dir has unexpected content")

// ErrStarterArtifactInvalid is returned when the pulled artifact does
// not contain the fleet-starter shape.
var ErrStarterArtifactInvalid = errors.New("init: fleet-starter artifact missing expected content")

// starterShapeMarkers are the paths whose joint presence means "this
// dir already carries the shared fleet trees". add-cluster.sh is the
// first script the engine executes; infrastructure/ + platform/ are
// what the scaffolded Flux Kustomizations reference.
var starterShapeMarkers = []string{
	"bootstrap/add-cluster.sh",
	"infrastructure/kube-ovn-network-public/kustomization.yaml",
	"infrastructure/ext-net-bridge-tag/kustomization.yaml",
	"platform/kustomization.yaml",
	"addons/metallb/kustomization.yaml",
	"addons/metallb-config/kustomization.yaml",
	"addons/metallb-config-bgp/kustomization.yaml",
	// Parity with the publisher's pre-push assertion (review P2
	// 2026-07-20): both ends validate the SAME set, so a bad override
	// or replaced tag fails at extraction, not one step later inside
	// install-prereqs.
	"scripts/install-prerequisites.sh",
}

// starterOwnedEntries are the top-level names the starter artifact owns.
// In a dir WITHOUT the full shape they can only be leftovers of an
// interrupted promotion — safe to clean and re-pull (resumable retry,
// review P1 2026-07-20).
var starterOwnedEntries = map[string]bool{
	"bootstrap":         true,
	"infrastructure":    true,
	"platform":          true,
	"addons":            true,
	"scripts":           true,
	".starter-version":  true,
	".starter-manifest": true,
}

// benignPreseedEntries are directory entries allowed to pre-exist in a
// non-fleet --repo dir: what a fresh `git init` + a topology-notes
// pre-seed legitimately contain (mirrors Scaffold's marker-file
// preflight posture — operators pre-place docs/READMEs).
var benignPreseedEntries = map[string]bool{
	".git":       true,
	".gitignore": true,
	"README.md":  true,
	"README":     true,
	"LICENSE":    true,
	"docs":       true,
	"clusters":   true,
}

// starterPuller is the narrow slice of ports.FluxClient EnsureStarter
// needs — hermetic tests supply a 3-line fake.
type starterPuller interface {
	PullArtifact(ctx context.Context, url, dir string) error
}

// starterGit is the narrow slice of ports.GitClient EnsureStarter needs.
type starterGit interface {
	Init(ctx context.Context, dir, branch string) error
	Commit(ctx context.Context, dir, msg string) (string, error)
}

// EnsureStarterOptions is the parameter bundle for EnsureStarter.
type EnsureStarterOptions struct {
	// RepoPath is the fleet repo root (--repo, already resolved).
	RepoPath string
	// Ref is the full OCI ref to pull, e.g.
	// oci://ghcr.io/kube-dc/fleet-starter:v0.5.0.
	Ref string
	// Flux pulls the artifact; Git init+commits the extracted tree.
	Flux starterPuller
	Git  starterGit
	// Out receives one-line progress notes. Nil-safe.
	Out io.Writer
}

// EnsureStarterResult reports what happened.
type EnsureStarterResult struct {
	// Extracted is false when the shape was already present (no-op).
	Extracted bool
	// Ref echoes the pulled ref when Extracted.
	Ref string
}

// StarterShapePresent reports whether dir already carries the shared
// fleet trees. Exported for the cobra layer's step-skip decision.
func StarterShapePresent(dir string) bool {
	for _, m := range starterShapeMarkers {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	return true
}

// EnsureStarter makes RepoPath a usable fleet-starter checkout.
func EnsureStarter(ctx context.Context, opts EnsureStarterOptions) (EnsureStarterResult, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.RepoPath == "" {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: RepoPath is required")
	}
	if StarterShapePresent(opts.RepoPath) {
		fmt.Fprintf(out, "fleet starter already present in %s — skipping OCI pull\n", opts.RepoPath)
		// Repair half-done runs (review P1 2026-07-20): a prior run that
		// extracted but died before git-init/commit leaves the shape
		// present — skipping acquisition is right, but the repo must
		// still end up committed or the engine's later steps hit an
		// unusable tree. Init is idempotent; commit only when .git was
		// genuinely absent (an operator checkout is never touched).
		if opts.Git != nil {
			switch _, statErr := os.Stat(filepath.Join(opts.RepoPath, ".git")); {
			case errors.Is(statErr, os.ErrNotExist):
				if err := opts.Git.Init(ctx, opts.RepoPath, "main"); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: repair git-init: %w", err)
				}
				if _, err := opts.Git.Commit(ctx, opts.RepoPath, "chore: fleet starter (repaired commit)"); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: repair commit: %w", err)
				}
				fmt.Fprintf(out, "repaired: starter tree was present but not committed — git-inited on main\n")
			case !gitHasCommit(opts.RepoPath):
				// .git exists but HEAD is unborn: a prior run's Init
				// succeeded and its Commit failed (review P1 — the
				// engine's later steps need a real commit).
				if _, err := opts.Git.Commit(ctx, opts.RepoPath, "chore: fleet starter (repaired commit)"); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: repair unborn-HEAD commit: %w", err)
				}
				fmt.Fprintf(out, "repaired: git HEAD was unborn — committed the starter tree\n")
			default:
				warnNonMainBranch(out, opts.RepoPath)
			}
		}
		return EnsureStarterResult{Extracted: false}, nil
	}
	if opts.Ref == "" {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: Ref is required")
	}
	if opts.Flux == nil || opts.Git == nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: Flux and Git clients are required")
	}

	// Populated-dir guard. A missing dir is fine (MkdirAll below).
	// Starter-OWNED leftovers (an interrupted promotion: some trees
	// moved, shape incomplete) are cleaned and re-pulled — retry is
	// self-healing instead of wedged (review P1 2026-07-20). Anything
	// else non-benign refuses.
	if entries, err := os.ReadDir(opts.RepoPath); err == nil {
		var offenders, leftovers []string
		for _, e := range entries {
			switch {
			case benignPreseedEntries[e.Name()]:
			case starterOwnedEntries[e.Name()]:
				leftovers = append(leftovers, e.Name())
			default:
				offenders = append(offenders, e.Name())
			}
		}
		if len(offenders) > 0 {
			sort.Strings(offenders)
			return EnsureStarterResult{}, fmt.Errorf(
				"%w: %s contains %s — refusing to extract the fleet starter over it. "+
					"Point --repo at an empty (or fresh) directory, or at an existing fleet checkout",
				ErrStarterDirNotEmpty, opts.RepoPath, strings.Join(offenders, ", "))
		}
		if len(leftovers) > 0 {
			sort.Strings(leftovers)
			fmt.Fprintf(out, "cleaning partial starter from an interrupted run: %s\n", strings.Join(leftovers, ", "))
			for _, name := range leftovers {
				if err := os.RemoveAll(filepath.Join(opts.RepoPath, name)); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: clean leftover %s: %w", name, err)
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: read %s: %w", opts.RepoPath, err)
	}
	if err := os.MkdirAll(opts.RepoPath, 0o755); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: mkdir %s: %w", opts.RepoPath, err)
	}

	// ATOMIC extract (review P1 2026-07-20): pull into a scratch sibling
	// dir, validate the shape THERE, then promote entry-by-entry. A
	// partial/failed pull never dirties RepoPath — the next run's
	// populated-dir guard would otherwise refuse the leftovers and the
	// operator would be wedged.
	tmp, err := os.MkdirTemp(filepath.Dir(opts.RepoPath), ".starter-pull-*")
	if err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: scratch dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	fmt.Fprintf(out, "pulling fleet starter %s\n", opts.Ref)
	if err := opts.Flux.PullArtifact(ctx, opts.Ref, tmp); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: %w", err)
	}

	// Validate the artifact actually delivered the shape — fail HERE
	// with the ref in hand, not later inside a script with a confusing
	// "add-cluster.sh: not found".
	if !StarterShapePresent(tmp) {
		var missing []string
		for _, m := range starterShapeMarkers {
			if _, err := os.Stat(filepath.Join(tmp, m)); err != nil {
				missing = append(missing, m)
			}
		}
		return EnsureStarterResult{}, fmt.Errorf(
			"%w: %s did not deliver %s — wrong ref or truncated artifact",
			ErrStarterArtifactInvalid, opts.Ref, strings.Join(missing, ", "))
	}
	// .gitignore must ship — it carries the age.key exclusion that keeps
	// the soon-to-be-generated private key out of the commit surface.
	if _, err := os.Stat(filepath.Join(tmp, ".gitignore")); err != nil {
		return EnsureStarterResult{}, fmt.Errorf(
			"%w: %s did not deliver .gitignore (carries the age.key exclusion) — republish the starter",
			ErrStarterArtifactInvalid, opts.Ref)
	}
	// Artifact format v2 ships .starter-manifest (mode+sha256 per shared-tree
	// file). Verify the pull against it: shape markers prove presence, the
	// manifest proves INTEGRITY (a truncated layer or corrupted blob fails
	// here with the ref in hand). v1 artifacts have no manifest — tolerated,
	// they predate the contract. The manifest is then committed with the
	// tree, so the repo records its vendor-clean baseline for the upgrade
	// path (PRD fleet-starter-lifecycle, Phase 0).
	if err := verifyStarterManifest(tmp); err != nil {
		return EnsureStarterResult{}, fmt.Errorf(
			"%w: %s failed manifest verification: %v — wrong ref or corrupted artifact",
			ErrStarterArtifactInvalid, opts.Ref, err)
	}

	// Promote: move validated top-level entries into RepoPath. Same
	// filesystem (sibling scratch dir), so each move is a rename.
	// Collision policy for the benign preseed set (review P1 — a bare
	// loop EEXIST-failed on a preseeded clusters/ mid-promotion and
	// silently clobbered a preseeded README):
	//   clusters/   → merged (starter ships only .gitkeep; preserve any
	//                 operator-preseeded content)
	//   README.md   → operator's preseed WINS (their topology notes)
	//   .gitignore  → merged: the starter's entries append AFTER the
	//                 operator's (the age.key exclusion must survive)
	// Everything else cannot collide: the benign set and the starter's
	// top level only intersect on those three (guard above enforces it).
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: read scratch: %w", err)
	}
	for _, e := range entries {
		src := filepath.Join(tmp, e.Name())
		dst := filepath.Join(opts.RepoPath, e.Name())
		if _, err := os.Stat(dst); err == nil {
			switch e.Name() {
			case "clusters":
				continue // merge: keep operator content; .gitkeep is moot
			case "README.md":
				continue // operator preseed wins
			case ".gitignore":
				if err := mergeGitignore(src, dst); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: merge .gitignore: %w", err)
				}
				continue
			default:
				return EnsureStarterResult{}, fmt.Errorf(
					"init: starter: promote %s: unexpected collision (guard should have refused)", e.Name())
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return EnsureStarterResult{}, fmt.Errorf("init: starter: promote %s: %w", e.Name(), err)
		}
	}
	// P0-3 invariant, whichever path the .gitignore took: age.key must
	// be excluded before the engine generates the key.
	if !gitignoreExcludesAgeKey(filepath.Join(opts.RepoPath, ".gitignore")) {
		return EnsureStarterResult{}, fmt.Errorf(
			"%w: resulting .gitignore does not exclude age.key — refusing to continue into key generation",
			ErrStarterArtifactInvalid)
	}

	// clusters/ must exist for add-cluster.sh; the artifact carries a
	// .gitkeep but belt-and-suspenders here is one MkdirAll.
	if err := os.MkdirAll(filepath.Join(opts.RepoPath, "clusters"), 0o755); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: mkdir clusters/: %w", err)
	}

	// Record the exact ref this repo was materialized from. The publisher
	// cannot embed the artifact's own digest (self-reference), so THIS is
	// where immutable identity lands: when the ref is digest-pinned (the
	// init default resolves tag@digest), the upgrade path can later prove
	// which artifact supplied the baseline even if tags move (codex review).
	if err := appendInstalledRef(opts.RepoPath, opts.Ref); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: record installed ref: %w", err)
	}

	// Deterministic branch BEFORE flux bootstrap's branch detection
	// (B5: stock git init defaults to master and breaks --branch=main
	// assumptions unless detected; starting on main removes the trap).
	// A PRESEEDED .git is left on its branch (Init is a no-op there) —
	// flux-install.sh detects the actual branch (the B5 fix), so a
	// master repo still works; we warn so the operator isn't surprised.
	if err := opts.Git.Init(ctx, opts.RepoPath, "main"); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: %w", err)
	}
	warnNonMainBranch(out, opts.RepoPath)
	if _, err := opts.Git.Commit(ctx, opts.RepoPath,
		fmt.Sprintf("chore: fleet starter from %s", opts.Ref)); err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: initial commit: %w", err)
	}
	fmt.Fprintf(out, "fleet starter extracted + committed (branch main)\n")
	return EnsureStarterResult{Extracted: true, Ref: opts.Ref}, nil
}

// warnNonMainBranch reads .git/HEAD directly (no go-git dependency in
// this package) and warns when the repo's initial branch is not main.
// Not an error: flux-install.sh detects the real branch (the B5 fix),
// so a master-initialized preseed still bootstraps — but the operator
// should know their fleet will live on a non-default branch name.
func warnNonMainBranch(out io.Writer, repoPath string) {
	head, err := os.ReadFile(filepath.Join(repoPath, ".git", "HEAD"))
	if err != nil {
		return
	}
	ref := strings.TrimSpace(strings.TrimPrefix(string(head), "ref: "))
	const prefix = "refs/heads/"
	if !strings.HasPrefix(ref, prefix) {
		return // detached or unusual HEAD — nothing useful to say
	}
	if branch := strings.TrimPrefix(ref, prefix); branch != "main" {
		fmt.Fprintf(out, "WARNING: fleet repo is on branch %q (not main) — flux bootstrap will follow it; rename with `git branch -m main` if unintended\n", branch)
	}
}

// gitHasCommit reports whether the repo at repoPath has at least one
// commit (HEAD born). Reads .git directly — no go-git dependency here.
// Unreadable/odd layouts return true (assume born) so we never
// "repair"-commit into a repository we don't understand.
func gitHasCommit(repoPath string) bool {
	head, err := os.ReadFile(filepath.Join(repoPath, ".git", "HEAD"))
	if err != nil {
		return true
	}
	ref := strings.TrimSpace(strings.TrimPrefix(string(head), "ref: "))
	if !strings.HasPrefix(ref, "refs/heads/") {
		return true // detached HEAD = there is a commit
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git", filepath.FromSlash(ref))); err == nil {
		return true
	}
	packed, err := os.ReadFile(filepath.Join(repoPath, ".git", "packed-refs"))
	if err != nil {
		return false // loose ref absent + no packed-refs → unborn
	}
	return strings.Contains(string(packed), ref)
}

// mergeGitignore appends the starter's .gitignore entries that the
// operator's preseeded file doesn't already carry (line-exact match).
// Operator content stays first and untouched.
func mergeGitignore(starterPath, operatorPath string) error {
	starter, err := os.ReadFile(starterPath)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(operatorPath)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, l := range strings.Split(string(starter), "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") && !have[t] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	f, err := os.OpenFile(operatorPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# appended by kube-dc fleet-starter (entries your preseed lacked)\n%s\n",
		strings.Join(missing, "\n"))
	return err
}

// starterManifestTrees is the FIXED set of shared trees the manifest
// covers. Deliberately not derived from the manifest's own entries: a
// truncated manifest would then shrink its own coverage and pass (codex
// review). Coverage growth is a schema bump, not an inference.
var starterManifestTrees = []string{"bootstrap", "infrastructure", "platform", "addons", "scripts"}

// starterSchemaVersion parses `schemaVersion:` and `manifestSha256:` out
// of .starter-version. Absent file or absent schemaVersion key → 0 (a v1
// artifact — predates the metadata contract).
func starterSchemaVersion(dir string) (version int, manifestSha string) {
	raw, err := os.ReadFile(filepath.Join(dir, ".starter-version"))
	if err != nil {
		return 0, ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "schemaVersion:"); ok {
			fmt.Sscanf(strings.TrimSpace(v), "%d", &version)
		}
		if v, ok := strings.CutPrefix(line, "manifestSha256:"); ok {
			manifestSha = strings.TrimSpace(v)
		}
	}
	return version, manifestSha
}

// verifyStarterManifest checks an extracted artifact against its own
// .starter-manifest (format: `<mode> <sha256> <path>` per line, `#`
// comments, mode normalized to 755/644). Failure directions covered:
//   - a schema-v2 artifact (per .starter-version) MISSING its manifest, or
//     whose manifest sha256 differs from the declared manifestSha256 —
//     stripping/tampering the manifest can't demote the artifact to v1;
//   - listed files missing, hash-mismatched, non-regular (symlink/FIFO —
//     os.Open would follow or block), or with a wrong executable bit;
//   - files present under the FIXED shared trees but not listed
//     (truncation), including walk errors — an unreadable dir is a
//     verification failure, not a pass;
//   - malformed lines, unsafe paths, duplicate or case-fold-duplicate
//     entries (ambiguous on case-insensitive checkouts).
//
// Only a fully metadata-less artifact (no .starter-version schemaVersion,
// no manifest) passes unverified — the v1 population. Closing that
// residual hole needs artifact signing (PRD Phase 1).
func verifyStarterManifest(dir string) error {
	schema, wantManifestSha := starterSchemaVersion(dir)
	manifestPath := filepath.Join(dir, ".starter-manifest")
	raw, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		if schema >= 2 {
			return fmt.Errorf("artifact declares schemaVersion %d but ships no .starter-manifest", schema)
		}
		return nil // v1 artifact — predates the baseline contract
	}
	if err != nil {
		return err
	}
	if schema >= 2 {
		if wantManifestSha == "" {
			return fmt.Errorf("artifact declares schemaVersion %d but .starter-version has no manifestSha256", schema)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != wantManifestSha {
			return fmt.Errorf(".starter-manifest sha256 %s does not match the declared manifestSha256 %s", got, wantManifestSha)
		}
	}

	type rec struct{ mode, hash string }
	want := map[string]rec{}      // rel path → manifest record
	folded := map[string]string{} // lowercased path → original
	for i, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		fields := strings.SplitN(t, " ", 3)
		if len(fields) != 3 || !isOctalMode(fields[0]) || !isHexSha256(fields[1]) {
			return fmt.Errorf(".starter-manifest line %d is malformed: %q", i+1, t)
		}
		p := fields[2]
		if filepath.IsAbs(p) || p != filepath.ToSlash(filepath.Clean(p)) || strings.HasPrefix(p, "..") {
			return fmt.Errorf(".starter-manifest line %d has an unsafe path: %q", i+1, p)
		}
		if _, dup := want[p]; dup {
			return fmt.Errorf(".starter-manifest lists %s twice", p)
		}
		if prev, dup := folded[strings.ToLower(p)]; dup {
			return fmt.Errorf(".starter-manifest paths %s and %s collide on case-insensitive checkouts", prev, p)
		}
		folded[strings.ToLower(p)] = p
		want[p] = rec{mode: fields[0], hash: fields[1]}
	}
	if len(want) == 0 {
		return fmt.Errorf(".starter-manifest carries no entries")
	}

	var problems []string
	note := func(format string, a ...any) {
		if len(problems) < 5 {
			problems = append(problems, fmt.Sprintf(format, a...))
		}
	}
	for p, w := range want {
		full := filepath.Join(dir, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		switch {
		case errors.Is(err, os.ErrNotExist):
			note("missing: %s", p)
			continue
		case err != nil:
			note("unreadable: %s (%v)", p, err)
			continue
		case !fi.Mode().IsRegular():
			// os.Open would FOLLOW a symlink (possibly out of the
			// extraction root) or BLOCK on a FIFO — refuse the type
			// before touching content.
			note("not a regular file: %s (%s)", p, fi.Mode().Type())
			continue
		}
		if wantExec := strings.HasPrefix(w.mode, "7"); wantExec != (fi.Mode().Perm()&0o100 != 0) {
			note("executable bit mismatch: %s (manifest %s)", p, w.mode)
			continue
		}
		got, err := sha256File(full)
		switch {
		case err != nil:
			note("unreadable: %s (%v)", p, err)
		case got != w.hash:
			note("hash mismatch: %s", p)
		}
	}
	for _, tree := range starterManifestTrees {
		root := filepath.Join(dir, tree)
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue // shape markers police tree presence separately
		}
		walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				note("walk error under %s: %v", tree, err)
				return nil //nolint:nilerr // recorded as a problem; keep scanning
			}
			if d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(dir, path)
			if rerr != nil {
				note("walk error under %s: %v", tree, rerr)
				return nil //nolint:nilerr // recorded as a problem; keep scanning
			}
			if _, ok := want[filepath.ToSlash(rel)]; !ok {
				note("unlisted file: %s", filepath.ToSlash(rel))
			}
			return nil
		})
		if walkErr != nil {
			note("walk error under %s: %v", tree, walkErr)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// isOctalMode accepts the manifest's normalized mode field (3-4 octal
// digits; the publisher emits exactly 644 or 755).
func isOctalMode(s string) bool {
	if len(s) < 3 || len(s) > 4 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

// isHexSha256 accepts a lowercase 64-char hex digest.
func isHexSha256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// appendInstalledRef appends the resolved pull ref (and its digest, when
// the ref is digest-pinned) to .starter-version so the repo permanently
// records which artifact it was materialized from. Creates the file when
// the artifact predates .starter-version entirely.
func appendInstalledRef(repoPath, ref string) error {
	f, err := os.OpenFile(filepath.Join(repoPath, ".starter-version"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "installedRef: %s\n", ref); err != nil {
		return err
	}
	if _, digest, ok := strings.Cut(ref, "@"); ok && strings.HasPrefix(digest, "sha256:") {
		if _, err := fmt.Fprintf(f, "installedDigest: %s\n", digest); err != nil {
			return err
		}
	}
	return nil
}

// sha256File returns the lowercase hex sha256 of the file's contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gitignoreExcludesAgeKey reports whether the file has a line that
// excludes age.key — the P0-3 invariant checked after promotion,
// whichever merge path the .gitignore took.
func gitignoreExcludesAgeKey(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "age.key" {
			return true
		}
	}
	return false
}
