package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T12 — apply.
//
// **What this slice does**: composes T10 Scaffold + T11
// customInterfaces (already chained from T10) with the atomic Git
// transaction + flux-install.sh script so a single `Apply(ctx,
// opts)` call drives the new cluster overlay from "nothing on disk"
// to "Flux reconciling against the cluster".
//
// **Apply-plan verbatim contract** (T04+T09+T13 review-pass): the
// cobra layer is responsible for calling Validate +
// VerifyApplyPlanInput before invoking Apply. Apply itself trusts
// its `ApplyOptions` — passing an un-verified plan + mismatched
// `Sets`/`NodeNICs` is a programmer error, not a runtime one.
// Cobra tests cover the gate ordering.
//
// **Atomic Git transaction** (memory entry "Push failure rolls back
// the local commit. Never use HEAD~1; use pre-commit SHA captured
// via Head()"): the 5-phase flow is:
//
//   1. **Capture pre-commit SHA** via `git.Head` — this is the
//      rollback target. Captured BEFORE Scaffold so even if
//      Scaffold partially writes and then fails, the commit step
//      sees nothing to commit (Scaffold's own writes failed
//      atomically; the working tree only carries successful
//      writes).
//   2. **Clean-tree check** via `git.Diff` — refuse if the operator
//      has uncommitted changes elsewhere in the fleet repo
//      (typed `ErrDirtyWorkingTree`). Without this gate the
//      subsequent `wt.Add(".")` in CommitAndPush would silently
//      slurp the operator's unrelated work into the cluster's
//      bootstrap commit.
//   3. **Scaffold** runs (T10 script + T11 customInterfaces patch);
//      this is the only step that writes files.
//   4. **Commit + push** via `git.CommitAndPush`. On push failure,
//      rollback to the captured pre-commit SHA via
//      `git.ResetHard(preSHA)` — restores the working tree + HEAD
//      so the operator's repo looks like Apply was never called.
//      Remote is untouched (push didn't succeed).
//   5. **flux-install.sh** via ScriptRunner. Failure here is NOT
//      auto-rolled-back — the remote already has the commit so
//      `git reset --hard` locally wouldn't undo the published
//      state. The operator fixes forward (or manually reverts).

// ApplyOptions is the parameter bundle for Apply. The cobra layer
// builds it from validated InitOptions + a verified Plan +
// adapter handles via bootstrap.Session.
type ApplyOptions struct {
	// Plan is the previously-validated init plan. Apply trusts
	// this — see "verbatim contract" in the package doc.
	Plan *Plan

	// FleetRepo is the absolute path of the fleet repo on disk.
	FleetRepo string

	// NodeExternalIP is forwarded to Scaffold for the
	// add-cluster.sh positional arg.
	NodeExternalIP string

	// Sets + NodeNICs are the operator's resolved --set / --node-nic
	// maps. They must match what was in the plan when it was built
	// (VerifyApplyPlanInput enforces this for --apply-plan; the
	// default-flow path builds Plan + opts from the SAME InitOptions
	// in one go, so they're guaranteed-coherent there).
	Sets     map[string]string
	NodeNICs map[string]string

	// Runner executes scripts (add-cluster.sh, flux-install.sh).
	// Real flow uses the script adapter; tests use a fake.
	Runner ports.ScriptRunner

	// Git is the GitClient for the atomic commit + push transaction.
	Git ports.GitClient

	// GitHubToken is the PAT used by CommitAndPush. Empty for
	// --no-push paths or genuinely-public repos (rare in this
	// product). Never logged.
	GitHubToken string

	// NoPush skips the push step. The local commit still lands;
	// flux-install is also skipped because Flux can't reconcile
	// from a non-pushed commit.
	NoPush bool

	// Out is the operator-facing log writer. nil = ioutil.Discard.
	Out io.Writer
}

// --- Errors ---

// ErrDirtyWorkingTree is returned when the fleet repo has
// uncommitted changes at Apply time. The clean-tree gate prevents
// CommitAndPush's `wt.Add(".")` from accidentally including
// operator work-in-progress in the cluster's bootstrap commit.
var ErrDirtyWorkingTree = errors.New("init: fleet repo has uncommitted changes; commit or stash before --apply")

// ErrApplyFluxInstallFailed is returned when bootstrap/flux-install.sh
// exits non-zero. The commit has already been pushed at that point
// — operator fixes forward rather than auto-rolling-back the
// published state.
var ErrApplyFluxInstallFailed = errors.New("init: flux-install.sh failed AFTER successful commit+push")

// ErrApplyMissingDependency surfaces when Apply is called with a
// nil Plan / Runner / Git / empty FleetRepo / NodeExternalIP.
// Cobra wires every required adapter, so seeing this in production
// is a programmer error.
var ErrApplyMissingDependency = errors.New("init: apply missing required dependency")

// --- Engine ---

// Apply orchestrates the full bootstrap apply: scaffold + commit +
// push + flux-install. See the package doc for the canonical
// 5-phase flow + rollback semantics.
func Apply(ctx context.Context, opts ApplyOptions) error {
	if err := validateApplyOptions(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	// Phase 1: capture pre-commit SHA (rollback target).
	preSHA, err := opts.Git.Head(ctx, opts.FleetRepo)
	if err != nil {
		return fmt.Errorf("apply: capture pre-commit SHA: %w", err)
	}

	// Phase 2: clean-tree gate.
	diff, err := opts.Git.Diff(ctx, opts.FleetRepo)
	if err != nil {
		return fmt.Errorf("apply: diff %s: %w", opts.FleetRepo, err)
	}
	if len(diff.Files) > 0 {
		// Surface the dirty file paths so the operator knows what
		// to stash/commit before re-running. ports.Diff.Files
		// includes untracked files (per port godoc) so this also
		// catches stray .swp / editor backup files.
		paths := make([]string, 0, len(diff.Files))
		for _, f := range diff.Files {
			paths = append(paths, f.Path)
		}
		return fmt.Errorf("%w (dirty paths: %v)", ErrDirtyWorkingTree, paths)
	}

	// Phase 3: scaffold (T10 script + T11 customInterfaces).
	//
	// **Cleanup on Scaffold failure** (M4-T12 review-pass — P2):
	// Scaffold writes into `clusters/<name>/` before the commit
	// gate. If it fails partway, the operator's working tree
	// carries those files — and crucially, the SOPS-fallback path
	// (which Scaffold's step 4 specifically refuses) could have
	// left a *plaintext* `secrets.enc.yaml` on disk. Leaving that
	// for the operator to `git clean -fd` is a footgun. Instead
	// we remove the cluster dir wholesale on Scaffold failure.
	//
	// EXCEPTION (cloudacropolis bug 2026-05-26): if Scaffold's own
	// preflight refused (ErrScaffoldTargetExists), the cluster dir
	// pre-existed this Apply call — operators sometimes pre-place a
	// `docs/` README there. Removing it would destroy the operator's
	// work. Skip cleanup in that case.
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName)
	if err := Scaffold(ctx, ScaffoldOptions{
		Plan:           opts.Plan,
		FleetRepo:      opts.FleetRepo,
		NodeExternalIP: opts.NodeExternalIP,
		Sets:           opts.Sets,
		NodeNICs:       opts.NodeNICs,
		Runner:         opts.Runner,
		Out:            out,
	}); err != nil {
		if errors.Is(err, ErrScaffoldTargetExists) {
			// Pre-existing dir — leave it alone.
			return fmt.Errorf("apply: scaffold: %w", err)
		}
		// Best-effort cleanup of the partial cluster dir. Failures
		// in cleanup don't mask the original Scaffold error — we
		// log them so the operator knows manual cleanup is needed,
		// then return the Scaffold error.
		if rmErr := os.RemoveAll(clusterDir); rmErr != nil {
			fmt.Fprintf(out, "[apply] WARNING: cleanup of partial scaffold at %s failed: %v\n",
				clusterDir, rmErr)
			fmt.Fprintf(out, "[apply] Manually inspect + remove that directory before re-running (may contain plaintext secrets if SOPS fallback fired).\n")
		} else {
			fmt.Fprintf(out, "[apply] cleaned up partial scaffold at %s\n", clusterDir)
		}
		return fmt.Errorf("apply: scaffold: %w", err)
	}

	// Phase 4: commit + push (atomic with rollback on push failure).
	msg := fmt.Sprintf("feat(%s): initial bootstrap via kube-dc CLI", opts.Plan.ClusterName)
	commitSHA, err := commitAndMaybePush(ctx, opts, msg)
	if err != nil {
		// Push failed — roll back the local commit so the operator's
		// repo looks like Apply was never called. The pre-commit SHA
		// captured in Phase 1 is the canonical rollback target (per
		// agent-rule 8: never HEAD~1; use captured SHA).
		fmt.Fprintf(out, "[apply] commit-push failed; rolling back local commit to %s\n", preSHA)
		if rerr := opts.Git.ResetHard(ctx, opts.FleetRepo, preSHA); rerr != nil {
			return fmt.Errorf("apply: commit-push failed AND rollback failed (manual recovery needed): commit-push=%w; rollback=%v",
				err, rerr)
		}
		return fmt.Errorf("apply: commit-push: %w", err)
	}
	fmt.Fprintf(out, "[apply] commit=%s%s\n", commitSHA, pushedSuffix(opts.NoPush))

	// Phase 5: flux-install.sh (skipped when NoPush — Flux needs a
	// pushed commit to reconcile from).
	if opts.NoPush {
		fmt.Fprintln(out, "[apply] --no-push set; skipping flux-install (cluster overlay exists locally only)")
		return nil
	}
	if err := runFluxInstall(ctx, opts, out); err != nil {
		return fmt.Errorf("%w: %v", ErrApplyFluxInstallFailed, err)
	}
	fmt.Fprintf(out, "[apply] complete — Flux is reconciling cluster %q\n", opts.Plan.ClusterName)
	return nil
}

// validateApplyOptions is a defensive guard against programmatic
// callers (e.g. tests) constructing ApplyOptions with missing
// dependencies. Returns ErrApplyMissingDependency wrapping a
// specific cause.
func validateApplyOptions(opts ApplyOptions) error {
	if opts.Plan == nil {
		return fmt.Errorf("%w: Plan", ErrApplyMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrApplyMissingDependency)
	}
	if opts.NodeExternalIP == "" {
		return fmt.Errorf("%w: NodeExternalIP", ErrApplyMissingDependency)
	}
	if opts.Runner == nil {
		return fmt.Errorf("%w: Runner", ErrApplyMissingDependency)
	}
	if opts.Git == nil {
		return fmt.Errorf("%w: Git", ErrApplyMissingDependency)
	}
	return nil
}

// commitAndMaybePush picks between Git.Commit (NoPush) and
// Git.CommitAndPush. Centralised so the rollback path in Apply
// handles both shapes via the same error wrapper.
func commitAndMaybePush(ctx context.Context, opts ApplyOptions, msg string) (string, error) {
	if opts.NoPush {
		return opts.Git.Commit(ctx, opts.FleetRepo, msg)
	}
	return opts.Git.CommitAndPush(ctx, opts.FleetRepo, msg, opts.GitHubToken)
}

func pushedSuffix(noPush bool) string {
	if noPush {
		return " (--no-push set; not pushed to remote)"
	}
	return " pushed"
}

// runFluxInstall executes bootstrap/flux-install.sh via the
// supplied ScriptRunner. Args: `<cluster-name> --new-cluster`. No
// secret-redaction step (flux-install doesn't echo passwords;
// add-cluster.sh's redactor is specific to that script's output).
func runFluxInstall(ctx context.Context, opts ApplyOptions, out io.Writer) error {
	fmt.Fprintln(out, "[apply] running flux-install.sh")
	lines, err := opts.Runner.Run(ctx, ports.ScriptFluxInstall, nil, opts.Plan.ClusterName, "--new-cluster")
	if err != nil {
		return fmt.Errorf("start flux-install.sh: %w", err)
	}
	exitCode := 0
	for ln := range lines {
		if ln.Stream == ports.StreamExit {
			c, err := parseExitCode(ln.Text)
			if err != nil {
				return fmt.Errorf("flux-install exit code parse: %w", err)
			}
			exitCode = c
			continue
		}
		streamTag := "stdout"
		if ln.Stream == ports.StreamStderr {
			streamTag = "stderr"
		}
		fmt.Fprintf(out, "[flux-install %s] %s\n", streamTag, ln.Text)
	}
	if exitCode != 0 {
		return fmt.Errorf("flux-install.sh exit=%d", exitCode)
	}
	return nil
}
