package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	kversion "k8s.io/apimachinery/pkg/util/version"
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
	// ClientVersion is the actual running installer version, including prerelease metadata.
	ClientVersion string
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
	// OperatorRepository is set for --fleet-mode=existing-repo: RepoPath is
	// the operator's own committed checkout, whose content is kept.
	OperatorRepository bool
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
func EnsureStarter(ctx context.Context, opts EnsureStarterOptions) (result EnsureStarterResult, err error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.RepoPath == "" {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: RepoPath is required")
	}
	// A different starter requested for a checkout that nothing has been
	// installed from yet: remove the old one and import the requested one
	// below, as on a first run (starter_replace.go).
	replaced := false
	pulled := ""
	var moved []string // top-level entries this run renamed into RepoPath
	installed, changed := StarterChangeRequested(opts.RepoPath, opts.Ref)
	if changed && StarterShapePresent(opts.RepoPath) {
		// An installation keeps its starter (a plan saved by an older CLI may
		// still name another one); init does not upgrade it.
		if overlays, oerr := StarterOverlays(opts.RepoPath); oerr != nil || len(overlays) > 0 {
			fmt.Fprintf(out, "WARNING: %s was installed from %s; keeping it although the plan selects %s "+
				"(changing the starter of an installation is an upgrade, which init does not do)\n",
				opts.RepoPath, installed.Ref, opts.Ref)
			changed = false
		}
	}
	if StarterShapePresent(opts.RepoPath) {
		if changed {
			if err := CheckStarterReplacement(opts.RepoPath, installed, opts.Ref); err != nil {
				return EnsureStarterResult{}, err
			}
			if opts.Flux == nil || opts.Git == nil {
				return EnsureStarterResult{}, fmt.Errorf("init: starter: Flux and Git clients are required")
			}
			if err := CheckReplacementCheckout(opts.RepoPath); err != nil {
				return EnsureStarterResult{}, err
			}
			// head and tmp are assigned with "=", so the deferred restore below
			// reads the function's returned err, not a shadowing one.
			var head, tmp string
			if head, err = gitCLI(ctx, opts.RepoPath, "rev-parse", "--verify", "HEAD"); err != nil {
				return EnsureStarterResult{}, err
			}
			// Fetch and verify the requested starter before anything is
			// removed: a failed pull leaves the checkout as it was.
			if tmp, err = pullStarter(ctx, opts, out); err != nil {
				return EnsureStarterResult{}, err
			}
			defer os.RemoveAll(tmp)
			// Any failure from here on puts the old starter back, so the next
			// run starts from the same checkout. (A run killed between the two
			// commits leaves the removal commit; the next run imports as on a
			// first run, which accepts the .sops.yaml and age key it finds.)
			defer func() {
				if err == nil {
					return
				}
				if rerr := restoreStarter(context.WithoutCancel(ctx), opts.RepoPath, head, moved); rerr != nil {
					err = fmt.Errorf("%w; restoring %s also failed (%v): run `git -C %s reset --hard %s`",
						err, installed.Ref, rerr, opts.RepoPath, head)
					return
				}
				fmt.Fprintf(out, "replacement failed; fleet starter %s restored\n", installed.Ref)
			}()
			fmt.Fprintf(out, "replacing fleet starter %s with %s (no cluster overlay exists yet)\n", installed.Ref, opts.Ref)
			if err := removeStarterForReplacement(ctx, opts.RepoPath,
				fmt.Sprintf("chore: remove fleet starter %s before importing %s", installed.Ref, opts.Ref)); err != nil {
				return EnsureStarterResult{}, err
			}
			replaced, pulled = true, tmp
		}
	}
	if !replaced && StarterShapePresent(opts.RepoPath) {
		if err := ValidateStarterCLI(opts.RepoPath, opts.ClientVersion); err != nil {
			return EnsureStarterResult{}, err
		}
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
				if err := requireAgeKeyIgnored(opts.RepoPath); err != nil {
					return EnsureStarterResult{}, err
				}
				if _, err := opts.Git.Commit(ctx, opts.RepoPath, "chore: fleet starter (repaired commit)"); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: repair commit: %w", err)
				}
				if err := refuseTrackedAgeKey(opts.RepoPath); err != nil {
					return EnsureStarterResult{}, err
				}
				fmt.Fprintf(out, "repaired: starter tree was present but not committed — git-inited on main\n")
			case !gitHasCommit(opts.RepoPath):
				// .git exists but HEAD is unborn: a prior run's Init
				// succeeded and its Commit failed (review P1 — the
				// engine's later steps need a real commit).
				if err := requireAgeKeyIgnored(opts.RepoPath); err != nil {
					return EnsureStarterResult{}, err
				}
				if _, err := opts.Git.Commit(ctx, opts.RepoPath, "chore: fleet starter (repaired commit)"); err != nil {
					return EnsureStarterResult{}, fmt.Errorf("init: starter: repair unborn-HEAD commit: %w", err)
				}
				if err := refuseTrackedAgeKey(opts.RepoPath); err != nil {
					return EnsureStarterResult{}, err
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

	// A checkout with history of its own gets an import commit of named paths
	// (commitStarterPaths); a new repository is committed whole by the Git
	// adapter. A directory inside another repository's working tree is not a
	// checkout with history.
	hadCommit := false
	if gitDir, gerr := exec.Command("git", "-C", opts.RepoPath, "rev-parse", "--git-dir").Output(); gerr == nil &&
		strings.TrimSpace(string(gitDir)) == ".git" {
		hadCommit = exec.Command("git", "-C", opts.RepoPath, "rev-parse", "--verify", "-q", "HEAD").Run() == nil
	}
	operatorRepoMode := false // set by the populated-dir guard

	// Populated-dir guard. A missing dir is fine (MkdirAll below).
	// Starter-OWNED leftovers (an interrupted promotion: some trees
	// moved, shape incomplete) are cleaned and re-pulled — retry is
	// self-healing instead of wedged (review P1 2026-07-20). Anything
	// else non-benign refuses.
	if entries, err := os.ReadDir(opts.RepoPath); err == nil {
		// An operator's committed Git checkout (existing-repo) keeps its own
		// top-level content (for example ansible/ or install/): extraction only
		// adds starter-owned entries, and the commit below stages them alone
		// because Apply requires a clean tree. A starter-owned name that Git
		// already tracks is the operator's, so it is refused, never removed.
		// An empty (or .git-only) directory takes the fresh-extraction path in
		// every mode.
		hasContent := false
		for _, e := range entries {
			hasContent = hasContent || e.Name() != ".git"
		}
		// .git is a file in a worktree or submodule checkout.
		_, gitErr := os.Stat(filepath.Join(opts.RepoPath, ".git"))
		hasGit := gitErr == nil
		// A replaced starter leaves a committed checkout behind (with the
		// operator's .sops.yaml and age key): keep its content as for an
		// operator repository.
		operatorRepo := (opts.OperatorRepository || replaced) && hasContent
		operatorRepoMode = operatorRepo
		if operatorRepo {
			if !hasGit || !gitHasCommit(opts.RepoPath) {
				return EnsureStarterResult{}, fmt.Errorf("%w: --fleet-mode=existing-repo needs %s to be a Git checkout with at least one commit",
					ErrStarterDirNotEmpty, opts.RepoPath)
			}
			// The starter commit stages the whole tree, so operator changes
			// must be committed (or ignored) first. Only the .sops.yaml an
			// interrupted key generation left behind may remain: it holds no
			// secret, goes into the import commit, and the age-key step after
			// this one enrolls the key in it and commits that.
			dirty, err := gitDirtyPaths(opts.RepoPath)
			if err != nil {
				return EnsureStarterResult{}, fmt.Errorf("init: starter: read the Git status of %s: %w", opts.RepoPath, err)
			}
			if dirty = withoutInterruptedReplacement(dirty); dirty != "" {
				return EnsureStarterResult{}, fmt.Errorf("%w: %s has uncommitted changes; commit them, remove them or list them "+
					"in .gitignore before importing the starter:\n%s",
					ErrStarterDirNotEmpty, opts.RepoPath, dirty)
			}
		}
		var offenders, leftovers []string
		for _, e := range entries {
			switch {
			case benignPreseedEntries[e.Name()]:
			case e.Name() == ".sops.yaml":
				// Written by key generation (an interrupted run, or one whose
				// starter was being replaced); the age-key step completes it.
			case e.Name() == "age.key" && !(hasGit && gitTracksPath(opts.RepoPath, e.Name())):
				// The key an earlier run generated: kept, and never committed
				// (the .gitignore check below).
			case starterOwnedEntries[e.Name()]:
				// In an operator repository nothing is ever removed: a
				// starter-named entry there is the operator's.
				// A starter-named entry that Git tracks is the operator's in any
				// mode, too.
				if operatorRepo || (hasGit && (gitTracksPath(opts.RepoPath, e.Name()) || gitIgnoresPath(opts.RepoPath, e.Name()))) {
					offenders = append(offenders, e.Name())
				} else {
					leftovers = append(leftovers, e.Name())
				}
			case operatorRepo:
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

	tmp := pulled
	if tmp == "" {
		var err error
		if tmp, err = pullStarter(ctx, opts, out); err != nil {
			return EnsureStarterResult{}, err
		}
		defer os.RemoveAll(tmp)
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
	gitignoreBefore, gitignoreErr := os.ReadFile(filepath.Join(opts.RepoPath, ".gitignore"))
	_, versionErr := os.Lstat(filepath.Join(opts.RepoPath, ".starter-version"))
	promoted := make([]string, 0, len(entries))
	for _, e := range entries {
		promoted = append(promoted, e.Name())
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
		moved = append(moved, e.Name())
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
	// An age key already in the directory (from an earlier run) must be
	// ignored by Git itself, not only named in .gitignore: a later rule such
	// as `!*.key` re-includes it.
	if err := requireAgeKeyIgnored(opts.RepoPath); err != nil {
		// Take the starter out again, so the next run imports it afresh
		// (and checks again) instead of committing what is there.
		for _, name := range moved {
			os.RemoveAll(filepath.Join(opts.RepoPath, name))
		}
		if gitignoreErr == nil {
			os.WriteFile(filepath.Join(opts.RepoPath, ".gitignore"), gitignoreBefore, 0o644)
		}
		if versionErr != nil {
			os.Remove(filepath.Join(opts.RepoPath, ".starter-version"))
		}
		return EnsureStarterResult{}, err
	}
	message := fmt.Sprintf("chore: fleet starter from %s", opts.Ref)
	if hadCommit {
		// .starter-version also when the artifact did not ship it:
		// appendInstalledRef wrote it above.
		paths := append(append([]string{}, promoted...), ".starter-version")
		if operatorRepoMode {
			// The clean-tree check ran: commit what the starter added.
			for i, name := range paths {
				if name == "clusters" {
					paths[i] = "clusters/.gitkeep"
				}
			}
		} else {
			// new-repo: everything the guard let through, as a new
			// repository's commit takes it.
			for name := range benignPreseedEntries {
				if name != ".git" {
					paths = append(paths, name)
				}
			}
		}
		err = commitStarterPaths(ctx, opts.RepoPath, paths, message)
	} else {
		_, err = opts.Git.Commit(ctx, opts.RepoPath, message)
	}
	if err != nil {
		return EnsureStarterResult{}, fmt.Errorf("init: starter: initial commit: %w", err)
	}
	if err := refuseTrackedAgeKey(opts.RepoPath); err != nil {
		return EnsureStarterResult{}, err
	}
	fmt.Fprintf(out, "fleet starter extracted + committed (branch main)\n")
	return EnsureStarterResult{Extracted: true, Ref: opts.Ref}, nil
}

// pullStarter pulls opts.Ref into a scratch directory beside RepoPath and
// verifies it. The caller removes the returned directory.
//
// ATOMIC extract (review P1 2026-07-20): pull into a scratch sibling
// dir, validate the shape THERE, then promote entry-by-entry. A
// partial/failed pull never dirties RepoPath — the next run's
// populated-dir guard would otherwise refuse the leftovers and the
// operator would be wedged.
func pullStarter(ctx context.Context, opts EnsureStarterOptions, out io.Writer) (dir string, err error) {
	tmp, err := os.MkdirTemp(filepath.Dir(opts.RepoPath), ".starter-pull-*")
	if err != nil {
		return "", fmt.Errorf("init: starter: scratch dir: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()

	fmt.Fprintf(out, "pulling fleet starter %s\n", opts.Ref)
	if err := opts.Flux.PullArtifact(ctx, opts.Ref, tmp); err != nil {
		return "", fmt.Errorf("init: starter: %w", err)
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
		return "", fmt.Errorf(
			"%w: %s did not deliver %s — wrong ref or truncated artifact",
			ErrStarterArtifactInvalid, opts.Ref, strings.Join(missing, ", "))
	}
	// .gitignore must ship — it carries the age.key exclusion that keeps
	// the soon-to-be-generated private key out of the commit surface.
	if _, err := os.Stat(filepath.Join(tmp, ".gitignore")); err != nil {
		return "", fmt.Errorf(
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
		return "", fmt.Errorf(
			"%w: %s failed manifest verification: %v — wrong ref or corrupted artifact",
			ErrStarterArtifactInvalid, opts.Ref, err)
	}
	if err := ValidateStarterCLI(tmp, opts.ClientVersion); err != nil {
		return "", err
	}
	return tmp, nil
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

// starterMetadata reads compatibility independently of the mutable shared trees.
func starterMetadata(dir string) (schema int, minimum string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".starter-version"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var meta struct {
		Schema  int    `yaml:"schemaVersion"`
		Minimum string `yaml:"minCLIVersion"`
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return 0, "", fmt.Errorf("invalid starter compatibility metadata: %w", err)
	}
	if meta.Schema > 3 {
		return 0, "", fmt.Errorf("starter schema %d requires a newer kube-dc CLI", meta.Schema)
	}
	if meta.Schema >= 3 && meta.Minimum == "" {
		return 0, "", fmt.Errorf("starter schema %d requires minCLIVersion", meta.Schema)
	}
	if meta.Minimum != "" {
		if _, err := kversion.ParseSemantic(meta.Minimum); err != nil {
			return 0, "", fmt.Errorf("invalid starter minCLIVersion %q: %w", meta.Minimum, err)
		}
	}
	return meta.Schema, meta.Minimum, nil
}

// ValidateStarterCLI refuses incompatible installers before copying or committing
// starter content. Check this for existing checkouts as well as new OCI pulls.
func ValidateStarterCLI(dir, clientVersion string) error {
	_, minimum, err := starterMetadata(dir)
	if err != nil {
		return err
	}
	if minimum == "" {
		return nil
	} // historical operator checkout
	required, _ := kversion.ParseSemantic(minimum)
	actual, err := kversion.ParseSemantic(clientVersion)
	if err != nil || !actual.AtLeast(required) {
		return fmt.Errorf("fleet starter requires kube-dc CLI %s or newer; running %q — upgrade the CLI before installation", minimum, clientVersion)
	}
	return nil
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
	return verifyStarterManifestExcept(dir, nil)
}

func verifyStarterManifestExcept(dir string, generated map[string]bool) error {
	schema, wantManifestSha := starterSchemaVersion(dir)
	_, minimum, metaErr := starterMetadata(dir)
	if metaErr != nil {
		return metaErr
	}
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
	requirements := 0
	for i, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "requires-cli ") {
			requirements++
			if schema != 3 || requirements != 1 || t != "requires-cli "+minimum {
				return fmt.Errorf(".starter-manifest CLI requirement differs from schema/minCLIVersion")
			}
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
	if schema == 3 && requirements != 1 {
		return fmt.Errorf("schema-v3 manifest requires exactly one requires-cli directive")
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
			if _, ok := want[filepath.ToSlash(rel)]; !ok && !generated[filepath.ToSlash(rel)] {
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

// VerifyReviewedStarter requires the baseline contract for guided review.
// Legacy starter acquisition retains its compatibility path.
func VerifyReviewedStarter(dir string) error {
	schema, _ := starterSchemaVersion(dir)
	if schema < 2 || !StarterShapePresent(dir) {
		return fmt.Errorf("review requires a complete schema-v2 starter with a manifest")
	}
	generated := map[string]bool{}
	const credential = "platform/registry-depot/secret.enc.yaml"
	if _, err := os.Lstat(filepath.Join(dir, credential)); err == nil {
		if err := verifyRegistryDepotCredential(dir); err != nil {
			return err
		}
		generated[credential] = true
	} else if !os.IsNotExist(err) {
		return err
	}
	return verifyStarterManifestExcept(dir, generated)
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

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// gitDirtyPaths lists modified and untracked (not ignored) paths.
func gitDirtyPaths(repoPath string) (string, error) {
	// No global excludes file: the Git adapter's commit (go-git) does not
	// read it, so a file ignored only there would be committed unseen.
	out, err := exec.Command("git", "-C", repoPath, "-c", "core.excludesFile=/dev/null",
		"status", "--porcelain", "--untracked-files=all").Output()
	// Only the trailing newline: the first line's status column may start
	// with a space (" M path").
	return strings.TrimRight(string(out), "\n"), err
}

// gitTracksPath reports whether Git tracks anything at name in repoPath.
// An error counts as tracked, so an unreadable state never leads to removal.
func gitTracksPath(repoPath, name string) bool {
	out, err := exec.Command("git", "-C", repoPath, "ls-files", "--", name).Output()
	return err != nil || strings.TrimSpace(string(out)) != ""
}

// requireAgeKeyIgnored refuses an age.key in repoPath that Git itself does not
// ignore. A .gitignore line is not enough: a later rule such as `!*.key`
// re-includes the file.
func requireAgeKeyIgnored(repoPath string) error {
	if _, err := os.Lstat(filepath.Join(repoPath, "age.key")); err != nil {
		return nil
	}
	if !gitIgnoresPath(repoPath, "age.key") {
		return fmt.Errorf("%w: Git does not ignore %s; fix .gitignore before importing the starter",
			ErrStarterDirNotEmpty, filepath.Join(repoPath, "age.key"))
	}
	return nil
}

// refuseTrackedAgeKey fails when a commit took age.key, before init pushes.
func refuseTrackedAgeKey(repoPath string) error {
	if _, err := os.Lstat(filepath.Join(repoPath, "age.key")); err != nil || !gitTracksAgeKey(repoPath) {
		return nil
	}
	return fmt.Errorf("init: starter: a commit in %s includes age.key; remove it with "+
		"`git rm --cached age.key && git commit --amend` before anything is pushed", repoPath)
}

// gitTracksAgeKey reports whether Git tracks age.key in repoPath. Unlike
// gitTracksPath, an unreadable state reports false: it only confirms a commit
// that must not have happened.
func gitTracksAgeKey(repoPath string) bool {
	out, err := exec.Command("git", "-C", repoPath, "ls-files", "--", "age.key").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// gitIgnoresPath reports whether name is ignored: ignored content is the
// operator's local state, never an interrupted run's leftover.
func gitIgnoresPath(repoPath, name string) bool {
	return exec.Command("git", "-C", repoPath, "check-ignore", "-q", "--", name).Run() == nil
}
