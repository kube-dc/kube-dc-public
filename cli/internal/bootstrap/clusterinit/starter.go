package clusterinit

import (
	"context"
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
	"infrastructure",
	"platform",
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
	"bootstrap":        true,
	"infrastructure":   true,
	"platform":         true,
	"addons":           true,
	"scripts":          true,
	".starter-version": true,
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
