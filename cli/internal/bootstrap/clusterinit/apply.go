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
	// add-cluster.sh positional arg. When SingleIPNAT is true this
	// already carries the ARRIVING (internal) IP resolved by
	// DetectArrivingIP, not the operator-declared public IP.
	NodeExternalIP string

	// NodeCIDR is the node's LAN prefix, resolved over SSH before Apply and
	// forwarded to Scaffold so INFRA_ATTACHMENT_ROUTES can be written. Empty
	// disables dual-homing for this cluster rather than guessing a prefix.
	NodeCIDR string

	// SingleIPNAT forwards the findings-17/17b topology detection to
	// Scaffold (step 8: 6443-listener removal patch).
	SingleIPNAT bool

	// Sets + NodeNICs are the operator's resolved --set / --node-nic
	// maps. They must match what was in the plan when it was built
	// (VerifyApplyPlanInput enforces this for --apply-plan; the
	// default-flow path builds Plan + opts from the SAME InitOptions
	// in one go, so they're guaranteed-coherent there).
	Sets     map[string]string
	NodeNICs map[string]string

	// ObjectStorage carries the OS-1 mode + companions for the OS-2
	// scaffold writer (Scaffold step 7).
	ObjectStorage ObjectStorageSpec

	// VMStorage carries the VM root-disk mode + golden subset for the
	// scaffold writer (Scaffold step 9). See vmstorage.go.
	VMStorage VMStorageSpec

	// GPU carries the validated, non-secret accelerator fleet contract.
	GPU GPUConfig

	// Runner executes scripts (add-cluster.sh, flux-install.sh).
	// Real flow uses the script adapter; tests use a fake.
	Runner ports.ScriptRunner

	// Git is the GitClient for the atomic commit + push transaction.
	Git ports.GitClient

	// GitHubToken is the PAT used by CommitAndPush + flux-install.sh.
	// Empty for --no-push paths or genuinely-public repos (rare in
	// this product). Never logged. Despite the name, carries the
	// token for the SELECTED Provider — see Provider field below.
	GitHubToken string

	// Provider selects the remote-repo hosting service for the
	// flux-install.sh dispatch. Empty defaults to GitHub. Feeds the
	// `KUBE_DC_PROVIDER` env var passed to the fleet script (which
	// dispatches `flux bootstrap github` vs `flux bootstrap gitlab`
	// accordingly).
	Provider Provider

	// GitHubOwner / GitHubRepo are the fleet-repo coordinates on
	// the selected provider — piped through as `GITHUB_OWNER` /
	// `GITHUB_REPO` env vars to `flux-install.sh` (env var names
	// kept for backward compat; the fleet script reads them
	// regardless of provider).
	GitHubOwner string
	GitHubRepo  string

	// NoPush skips the push step. The local commit still lands;
	// flux-install is also skipped because Flux can't reconcile
	// from a non-pushed commit.
	NoPush bool

	// Out is the operator-facing log writer. nil = ioutil.Discard.
	Out io.Writer

	// Reporter observes milestone lifecycle (Scaffold / commit-push /
	// flux-install). nil ⇒ NopReporter, in which case behavior is
	// identical to the pre-reporter engine (detail still goes to Out).
	// The install-run TUI supplies one to drive its live checklist.
	Reporter StepReporter
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
	rep := reporterOrNop(opts.Reporter)

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
	// EXCEPTION (atlantis bug 2026-05-26): if Scaffold's own
	// preflight refused (ErrScaffoldTargetExists), the cluster dir
	// pre-existed this Apply call — operators sometimes pre-place a
	// `docs/` README there. Removing it would destroy the operator's
	// work. Skip cleanup in that case.
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName)
	resuming := false
	rep.Start(StepScaffold)
	if err := Scaffold(ctx, ScaffoldOptions{
		Plan:           opts.Plan,
		FleetRepo:      opts.FleetRepo,
		NodeExternalIP: opts.NodeExternalIP,
		NodeCIDR:       opts.NodeCIDR,
		Sets:           opts.Sets,
		NodeNICs:       opts.NodeNICs,
		ObjectStorage:  opts.ObjectStorage,
		VMStorage:      opts.VMStorage,
		GPU:            opts.GPU,
		SingleIPNAT:    opts.SingleIPNAT,
		Runner:         opts.Runner,
		Out:            out,
	}); err != nil {
		if errors.Is(err, ErrScaffoldTargetExists) {
			// RESUME (retry/restart): the overlay already exists from a
			// prior apply. Rather than fail, skip re-scaffolding and
			// continue — the Phase-2 clean-tree gate already confirmed
			// nothing is uncommitted, so there's nothing new to commit
			// and flux-install is idempotent. This is what makes a
			// re-run of `init` resume a partially-completed install
			// instead of erroring. To force a fresh scaffold, edit or
			// remove clusters/<name>/ first.
			fmt.Fprintf(out, "[apply] cluster overlay clusters/%s already exists — resuming (skipping scaffold + commit; flux-install is idempotent)\n", opts.Plan.ClusterName)
			rep.Done(StepScaffold, nil)
			resuming = true
		} else {
			rep.Done(StepScaffold, err)
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
	} else {
		rep.Done(StepScaffold, nil)
	}

	// Phase 4: commit + push (atomic with rollback on push failure).
	// On resume the overlay is already COMMITTED (the clean-tree gate
	// proved nothing is uncommitted, and an untracked overlay would have
	// tripped that gate as dirty), so there's nothing new to commit —
	// but a prior --no-push run or a failed push can leave HEAD ahead of
	// the branch Flux reconciles. A clean tree does NOT prove the commit
	// is on the remote, so we PUSH HEAD (idempotent) rather than skip:
	// otherwise flux-install would point Flux at a remote missing the
	// overlay. Under --no-push the overlay is local-only by contract, so
	// there's nothing to push and flux-install is itself skipped below.
	if resuming {
		if opts.NoPush {
			rep.Skip(StepCommitPush, "overlay already committed; --no-push (local-only)")
			fmt.Fprintln(out, "[apply] resuming: overlay already committed (--no-push, local-only)")
		} else {
			rep.Start(StepCommitPush)
			if perr := opts.Git.Push(ctx, opts.FleetRepo, opts.GitHubToken); perr != nil {
				rep.Done(StepCommitPush, perr)
				return fmt.Errorf("apply: resume push (overlay committed locally may be unpushed — cannot confirm the remote Flux reconciles has it): %w", perr)
			}
			rep.Done(StepCommitPush, nil)
			fmt.Fprintln(out, "[apply] resuming: overlay already committed — pushed HEAD to upstream, proceeding to flux-install")
		}
	} else {
		rep.Start(StepCommitPush)
		msg := fmt.Sprintf("feat(%s): initial bootstrap via kube-dc CLI", opts.Plan.ClusterName)
		commitSHA, cerr := commitAndMaybePush(ctx, opts, msg)
		if cerr != nil {
			rep.Done(StepCommitPush, cerr)
			// Push failed — roll back the local commit so the operator's
			// repo looks like Apply was never called. The pre-commit SHA
			// captured in Phase 1 is the canonical rollback target (per
			// agent-rule 8: never HEAD~1; use captured SHA).
			fmt.Fprintf(out, "[apply] commit-push failed; rolling back local commit to %s\n", preSHA)
			if rerr := opts.Git.ResetHard(ctx, opts.FleetRepo, preSHA); rerr != nil {
				return fmt.Errorf("apply: commit-push failed AND rollback failed (manual recovery needed): commit-push=%w; rollback=%v",
					cerr, rerr)
			}
			return fmt.Errorf("apply: commit-push: %w", cerr)
		}
		rep.Done(StepCommitPush, nil)
		fmt.Fprintf(out, "[apply] commit=%s%s\n", commitSHA, pushedSuffix(opts.NoPush))
	}

	// Phase 5: flux-install.sh (skipped when NoPush — Flux needs a
	// pushed commit to reconcile from).
	if opts.NoPush {
		rep.Skip(StepFluxInstall, "--no-push: cluster overlay is local-only")
		fmt.Fprintln(out, "[apply] --no-push set; skipping flux-install (cluster overlay exists locally only)")
		return nil
	}
	rep.Start(StepFluxInstall)
	if err := runFluxInstall(ctx, opts, out); err != nil {
		rep.Done(StepFluxInstall, err)
		return fmt.Errorf("%w: %v", ErrApplyFluxInstallFailed, err)
	}
	rep.Done(StepFluxInstall, nil)
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
// supplied ScriptRunner. Args: `<cluster-name> --new-cluster`.
// Passes provider + owner/repo/token via env — the fleet script
// dispatches `flux bootstrap github` vs `flux bootstrap gitlab`
// based on `KUBE_DC_PROVIDER`. No secret-redaction step
// (flux-install doesn't echo passwords; add-cluster.sh's redactor
// is specific to that script's output).
func runFluxInstall(ctx context.Context, opts ApplyOptions, out io.Writer) error {
	fmt.Fprintln(out, "[apply] running flux-install.sh")

	// Provider dispatch — default (empty) → github for backward
	// compat with pre-multi-provider callers. The env var name is
	// `KUBE_DC_PROVIDER`; matches the fleet script's expectation.
	provider := opts.Provider
	if provider == "" {
		provider = ProviderGitHub
	}
	env := map[string]string{
		"KUBE_DC_PROVIDER": string(provider),
	}
	if opts.GitHubOwner != "" {
		env["GITHUB_OWNER"] = opts.GitHubOwner
	}
	if opts.GitHubRepo != "" {
		env["GITHUB_REPO"] = opts.GitHubRepo
	}
	// Token env var name differs per provider. Set the appropriate
	// one so the fleet script's auth branch reads it directly; the
	// script also has a CLI-auth fallback (`gh`/`glab`) but env is
	// authoritative when we resolved a token upstream.
	if opts.GitHubToken != "" {
		if provider == ProviderGitLab {
			env["GITLAB_TOKEN"] = opts.GitHubToken
		} else {
			env["GITHUB_TOKEN"] = opts.GitHubToken
		}
	}

	lines, err := opts.Runner.Run(ctx, ports.ScriptFluxInstall, env, opts.Plan.ClusterName, "--new-cluster")
	if err != nil {
		return fmt.Errorf("start flux-install.sh: %w", err)
	}
	// ports.Drain enforces the ScriptRunner contract: a stream that ends
	// without a terminal exit record is a failure, not a silent success.
	// This loop previously defaulted exitCode to 0, so a killed
	// flux-install left the CLI reporting Flux as installed.
	exitCode, err := ports.Drain(lines, func(ln ports.Line) {
		streamTag := "stdout"
		if ln.Stream == ports.StreamStderr {
			streamTag = "stderr"
		}
		fmt.Fprintf(out, "[flux-install %s] %s\n", streamTag, ln.Text)
	})
	if err != nil {
		return fmt.Errorf("flux-install: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("flux-install.sh exit=%d", exitCode)
	}
	return nil
}
