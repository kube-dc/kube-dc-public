// Package openbao owns the engine for `kube-dc bootstrap openbao …`
// subcommands per installer-prd §12 and installer-ux §1.1. Sub-slices:
//
//   - Init (M5-T01): captures the 5 Shamir shares + root token from
//     `bootstrap/openbao-init.sh` and SOPS-encrypts them into
//     `clusters/<name>/secrets.enc.yaml` under
//     `OPENBAO_UNSEAL_KEY_{1..5}`. The only plaintext it writes are
//     0600 files outside any git tree: the operator-requested
//     `--openbao-shares-out` copy, and a write-ahead custody copy that
//     exists only between vault initialization and a successful commit
//     (removed on success, kept and reported on failure). The root
//     token is never written to either.
//   - Unseal (M5-T02): decrypts the first 3 shares from
//     `secrets.enc.yaml` and feeds them to every sealed OpenBao pod.
//
// **Share-custody contract** (installer-prd §12.3, agent-rule 7):
//   - Plaintext shares + root token live ONLY in a `secrets.Buffer`
//     for the duration of Init; `defer Buffer.Scrub()` zeroes them
//     before returns.
//   - The ScriptRunner's sentinel callback captures the JSON
//     payload in-memory — the regular `<-chan Line` stream sees
//     only the placeholder line `[ScriptOpenBaoInit payload
//     captured — N bytes]`.
//   - `SOPSClient.SetStringData` performs atomic per-key updates
//     (no full-file re-encrypt; matches the fleet's existing
//     pattern for OpenBao shares).
//   - Round-trip verify: every key written gets re-decrypted and
//     byte-compared; a mismatch returns a hard error AFTER preserving
//     the shares off-git. There is NO git rollback once the vault has
//     been initialized — the shares are the only recovery material,
//     so every failure path preserves them (see Init's Phase B note).
//   - Preservation is write-ahead, not best-effort-at-the-end: the
//     shares hit disk in the sentinel callback itself, so no error
//     between initialization and commit can exit with them held only
//     in a buffer that `defer Scrub()` is about to zero. Emergency
//     copies never overwrite an existing one — an older dump may
//     belong to a still-sealed vault (see emergencyDumpShares).
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/secrets"
)

// InitOptions is the parameter bundle for Init. Built by the cobra
// layer from validated InitOptions + a `bootstrap.Session`.
type InitOptions struct {
	// ClusterName is the fleet cluster name (used to locate
	// secrets.enc.yaml at clusters/<name>/secrets.enc.yaml).
	ClusterName string

	// FleetRepo is the absolute path of the fleet repo on disk.
	FleetRepo string

	// Runner executes openbao-init.sh; the engine attaches a
	// sentinel callback that diverts the share JSON into a
	// secrets.Buffer.
	Runner ports.ScriptRunner

	// SOPS performs per-key atomic encryption into secrets.enc.yaml.
	SOPS ports.SOPSClient

	// Git performs the atomic commit + push transaction (with
	// pre-commit-SHA rollback on push failure).
	Git ports.GitClient

	// OpenBao performs the post-commit irreversible operations
	// via stdin-transport adapters (review-pass P1): unseal pods,
	// run controller-auth setup, revoke the root token, annotate
	// the openbao Service. Never argv/env — shares + root token
	// only reach `bao` via the adapter's PodExec stdin channel.
	OpenBao ports.OpenBaoClient

	// K8s reads the in-cluster kube-apiserver CA bundle
	// (kube-root-ca.crt ConfigMap) for the M5-T08 controller-auth
	// setup that runs as part of Phase C. May be nil only when
	// SkipControllerAuth is true.
	K8s ports.K8sClient

	// SkipControllerAuth bypasses postInitSetupControllerAuth in
	// Phase C. Emergency / debug only — leaves the cluster in a
	// state where the manager cannot login until
	// `bootstrap openbao setup-controller-auth` is run manually.
	// Not documented prominently; intended for the rare case where
	// init must complete share custody before OpenBao auth-mount
	// writes are possible (e.g. wedged OpenBao state under
	// investigation).
	SkipControllerAuth bool

	// GitHubToken authenticates the push. Empty for --no-push or
	// genuinely-public repos.
	GitHubToken string

	// NoPush commits locally without pushing — for testing the
	// share-encryption path without involving the remote.
	NoPush bool

	// SharesOutPath is the optional `--openbao-shares-out=<file>`
	// destination for an off-fleet plaintext copy. The file is
	// written with mode 0600. The engine refuses paths that look
	// like they're inside a git tree (per installer-prd §12.3 — the
	// fleet's encrypted secrets.enc.yaml is the canonical share
	// store; an additional plaintext copy is an explicit operator
	// choice, and putting it in version control would defeat the
	// whole point).
	SharesOutPath string

	// Out is the operator-facing log writer. nil = io.Discard.
	// Sentinel-captured payloads + share values NEVER reach this
	// writer.
	Out io.Writer
}

// --- Errors ---

// ErrAlreadyInitialised is returned when the openbao Service already
// carries the `kube-dc.com/openbao-bootstrap-finalized` annotation —
// running Init again would corrupt the live shares. Wired via the
// OpenBaoClient.GetAnnotation pre-flight check (review-pass P2).
var ErrAlreadyInitialised = errors.New("openbao: cluster already initialised (openbao-bootstrap-finalized annotation present)")

// ErrDirtyWorkingTree is returned when the fleet repo has
// uncommitted changes at Init time (review-pass P1/P2). Without
// this gate, the share-encryption commit would slurp the operator's
// work-in-progress into the cluster's openbao-init commit.
var ErrDirtyWorkingTree = errors.New("openbao: fleet repo has uncommitted changes; commit or stash before openbao init")

// ErrPostCommitIrreversible signals failure of an irreversible
// step (unseal, revoke, annotate) that runs AFTER the shares are
// safely committed + pushed. The encrypted shares are durable in
// the fleet repo at this point — Init returns the error so the
// operator sees it, but does NOT roll back the commit because
// rolling back would discard the only durable copy of the recovery
// material. The operator's remediation is `bootstrap openbao
// unseal <name>` (auto-resumes from where init left off).
var ErrPostCommitIrreversible = errors.New("openbao: post-commit step failed (shares are safely encrypted + committed; run `bootstrap openbao unseal <name>` to resume)")

// ErrShareCustodyFailed is returned when the round-trip verify
// after SetStringData detects a mismatch — the share that ended up
// encrypted on disk differs from the captured plaintext. Nothing is
// rolled back (the vault is already initialized), and the captured
// shares are already on disk from the write-ahead custody dump taken
// in the sentinel callback; the returned error names that path.
var ErrShareCustodyFailed = errors.New("openbao: share-custody round-trip verify failed")

// ErrScriptStreamTruncated is returned when the script's Line channel
// closed without ever carrying a StreamExit record. The drainer used to
// report exit 0 in that case, so a killed or disconnected run looked
// like a clean success. Any shares captured before the truncation are
// preserved and the custody path is named in the returned error.
//
// Aliased to the shared ports sentinel: the contract is enforced once in
// ports.Drain, and errors.Is matches through either name.
var ErrScriptStreamTruncated = ports.ErrStreamTruncated

// ErrShareCaptureMissing is returned when the script exited 0 but
// the sentinel callback never fired (no payload captured) or the
// payload didn't parse into 5 shares + 1 root token. Indicates a
// fleet-script regression — refuse to silently succeed with no
// captured shares.
var ErrShareCaptureMissing = errors.New("openbao: script exited cleanly but no shares captured")

// ErrSharesOutInsideGit is returned when --openbao-shares-out points
// at a path under any git working tree. The cobra layer surfaces
// this with the offending path so the operator picks an
// off-version-control destination.
var ErrSharesOutInsideGit = errors.New("openbao: --openbao-shares-out path is inside a git working tree; pick a path outside any repo")

// ErrInitMissingDependency surfaces when Init is called with a nil
// adapter or empty cluster name. Cobra wires every required
// adapter; seeing this in production is a programmer error.
var ErrInitMissingDependency = errors.New("openbao: init missing required dependency")

// --- Engine ---

// Init runs the M5-T01 share-custody ceremony. The pipeline is
// reorganised around custody-first ordering (review-pass P1):
//
// **Phase A — pre-flight + capture share JSON**
// (reversible ONLY until step 6: running the script initializes the
// vault and is the true point of no return):
//
//  1. Preflight: `<repo>/clusters/<name>/secrets.enc.yaml` exists.
//  2. `--openbao-shares-out` path safety: refuse git-tree paths.
//  3. Dirty-tree gate via Git.Diff — refuse uncommitted changes.
//  4. Already-initialised check via OpenBao.GetAnnotation — refuse
//     if `kube-dc.com/openbao-bootstrap-finalized` is set.
//  5. Resolve fleet HEAD via Git.Head as a pre-init sanity gate
//     (NOT a rollback target — nothing is rolled back post-init).
//  6. Run `openbao-init.sh` with the sentinel callback.
//     The script emits 5 shares + 1 root token JSON between
//     sentinels; the callback parses it into a secrets.Buffer.
//     The script no longer performs any post-init action — unseal,
//     revoke, annotate all moved to Phase C below.
//     6a. WRITE-AHEAD CUSTODY: the same callback immediately persists
//     whatever shares it parsed to an off-git 0600 file, fsynced.
//     This closes the window between "the vault is initialized" and
//     "the shares are encrypted in the fleet" — a window in which
//     `defer buf.Scrub()` would otherwise destroy the only copy of
//     the recovery material on ANY error return (drain failure,
//     nonzero exit, unparseable payload, missing root token).
//     The file is removed once step 9 succeeds.
//
// **Phase B — durable custody (NO ROLLBACK — see below)**:
//
//  7. For each share, SOPS.SetStringData + round-trip verify.
//     Failure → point the operator at the 6a file, then return.
//  8. Optional `--openbao-shares-out` plaintext copy (mode 0600,
//     outside any git tree). Failure → WARN only; the encrypted
//     copy in the fleet is the authoritative custody.
//  9. Commit + push. Failure → keep the encrypted shares in the
//     working tree, keep the 6a off-git copy, and return the
//     manual git command that actually completes from HERE (push
//     only if the commit already landed — see manualFinishHint).
//     Success → remove the 6a file; the encrypted committed copy
//     is now the custody of record.
//
// **Phase C — irreversible post-commit operations (no rollback)**:
//
//  10. Unseal openbao-0 with 3 shares via OpenBaoClient.Unseal
//     (stdin transport — never argv).
//  11. Revoke the root token via OpenBaoClient.RevokeSelf (stdin).
//  12. Annotate svc/openbao with
//     `kube-dc.com/openbao-bootstrap-finalized=<rfc3339>`.
//
// Phase C failures return ErrPostCommitIrreversible. The encrypted
// shares are durable on the remote at this point; `bootstrap
// openbao unseal <name>` can resume from where Init left off.
// Rolling back the commit would discard the only durable copy of
// the recovery material — explicitly not done (review-pass P1).
//
// `defer Buffer.Scrub()` zeroes shares + root token regardless of
// outcome.
func Init(ctx context.Context, opts InitOptions) error {
	if err := validateInitOptions(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	// --- Phase A: pre-flight ---

	secretsPath := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName, "secrets.enc.yaml")
	if _, err := os.Stat(secretsPath); err != nil {
		return fmt.Errorf("openbao: secrets.enc.yaml not found at %s — run `bootstrap init` first: %w",
			secretsPath, err)
	}

	if opts.SharesOutPath != "" {
		if err := assertNotInsideAnyGitTree(opts.SharesOutPath); err != nil {
			return err
		}
	}

	// Dirty-tree gate (review-pass P1/P2). Same shape as M4-T12's
	// Apply: refuse if Git.Diff reports any files so the openbao
	// commit doesn't silently include operator work-in-progress.
	diff, err := opts.Git.Diff(ctx, opts.FleetRepo)
	if err != nil {
		return fmt.Errorf("openbao: diff %s: %w", opts.FleetRepo, err)
	}
	if len(diff.Files) > 0 {
		paths := make([]string, 0, len(diff.Files))
		for _, f := range diff.Files {
			paths = append(paths, f.Path)
		}
		return fmt.Errorf("%w (dirty paths: %v)", ErrDirtyWorkingTree, paths)
	}

	// Already-initialised check (review-pass P2). The fleet script
	// is intentionally non-idempotent — running it on a finalised
	// cluster would `bao operator init` fail. We catch it here so
	// the operator gets the typed error before the script runs.
	if anno, err := opts.OpenBao.GetAnnotation(ctx, "openbao", "kube-dc.com/openbao-bootstrap-finalized"); err == nil && anno != "" {
		return fmt.Errorf("%w (annotation set %s)", ErrAlreadyInitialised, anno)
	}

	// Pre-init gate: the fleet repo must have a resolvable HEAD before
	// we touch the vault, so a broken repo fails fast while failure is
	// still free. The SHA is deliberately NOT retained as a rollback
	// target — see the no-rollback note in Phase B.
	if _, err := opts.Git.Head(ctx, opts.FleetRepo); err != nil {
		return fmt.Errorf("openbao: resolve fleet HEAD: %w", err)
	}

	// Buffer + sentinel callback. The callback runs on the
	// ScriptRunner's drainer goroutine; values are read on the
	// main goroutine after the channel closes.
	buf := secrets.NewBuffer()
	defer buf.Scrub()

	var (
		captureMu  sync.Mutex
		captureErr error
		captured   bool
		wahPath    string // write-ahead custody file, "" if none/failed
		wahErr     error
	)
	captureCB := func(_ ports.ScriptKind, _ string, payload []byte) error {
		captureMu.Lock()
		defer captureMu.Unlock()
		captured = true
		if perr := loadBufferFromJSON(buf, payload); perr != nil {
			captureErr = perr
			// Fall through: a partial parse may still have left real
			// shares in the buffer, and they are worth persisting.
		}

		// WRITE-AHEAD CUSTODY (P0, review-pass 3). The instant this
		// callback holds the payload, the shares are the sole copy of
		// the vault's recovery material — `defer buf.Scrub()` destroys
		// them on ANY return below, including returns that happen
		// before Phase B is ever reached (drain error, nonzero exit,
		// parse error, missing root token, wrong share count).
		// Persisting here, fsynced, means no post-capture error path
		// can exit with the shares only in memory.
		//
		// Deliberately silent: this runs on the ScriptRunner's drainer
		// goroutine, concurrently with drainOpenBaoInit's writes to
		// `out` on the main goroutine. The path is reported after the
		// channel closes.
		wahPath, wahErr = emergencyDumpShares(opts, buf)
		if captureErr != nil {
			return captureErr
		}
		return nil
	}

	runner := opts.Runner.WithSentinelCallback(captureCB)

	fmt.Fprintf(out, "[openbao] running bootstrap/openbao-init.sh for %s\n", opts.ClusterName)
	lines, err := runner.Run(ctx, ports.ScriptOpenBaoInit, nil)
	if err != nil {
		return fmt.Errorf("openbao: start script: %w", err)
	}
	exitCode, drainErr := drainOpenBaoInit(lines, out)

	// Read the capture state BEFORE acting on any script-level error.
	// Whether the shares were captured decides whether a failure is a
	// free retry or a custody event, so it has to be known first.
	captureMu.Lock()
	cerr := captureErr
	cok := captured
	custodyPath, custodyErr := wahPath, wahErr
	captureMu.Unlock()

	// custody annotates a post-capture error with where the shares were
	// preserved. Every return between here and a successful commit goes
	// through it — that is the whole point of the write-ahead dump.
	//
	// `cause` is wrapped with %w, not formatted with %v: callers and
	// tests match the underlying sentinels (ErrShareCustodyFailed,
	// ErrShareCaptureMissing, the adapter's push error) with errors.Is,
	// and flattening them to text would silently break that.
	custody := func(cause error, format string, args ...any) error {
		base := fmt.Sprintf(format, args...)
		if cause == nil {
			cause = errors.New("no further detail")
		}
		if custodyErr != nil {
			return fmt.Errorf("openbao: CRITICAL — %s. The vault IS initialized but its %d "+
				"captured share(s) could NOT be preserved (%v), and this process is about "+
				"to discard them. The vault is unrecoverable and must be re-created from "+
				"scratch. cause: %w",
				base, buf.ShareCount(), custodyErr, cause)
		}
		return fmt.Errorf("openbao: %s. The vault IS initialized; its %d captured share(s) "+
			"were preserved at %s (mode 0600) — move them somewhere safe. Nothing was "+
			"rolled back. cause: %w",
			base, buf.ShareCount(), custodyPath, cause)
	}
	if cok {
		if custodyErr != nil {
			fmt.Fprintf(out, "[openbao] CRITICAL: shares were captured but could not be "+
				"written to disk: %v\n", custodyErr)
		} else {
			fmt.Fprintf(out, "[openbao] write-ahead custody: shares preserved at %s "+
				"(removed automatically once they are encrypted + committed)\n", custodyPath)
		}
	}

	// From here on, `cok` splits the two worlds: !cok means the vault was
	// never initialized (or we never saw its keys) and failing is free;
	// cok means failing costs recovery material.
	// Report the parse failure ahead of the exit code: when the payload
	// is malformed the runner's own callback error is usually what made
	// the script exit non-zero, so "root_token absent" tells the
	// operator far more than "exit=1".
	if cerr != nil {
		return custody(cerr, "captured payload did not parse")
	}
	if drainErr != nil {
		if cok {
			return custody(drainErr, "script output drain failed")
		}
		return fmt.Errorf("openbao: drain: %w", drainErr)
	}
	if exitCode != 0 {
		if cok {
			return custody(nil, "openbao-init.sh exit=%d AFTER emitting the share payload", exitCode)
		}
		return fmt.Errorf("openbao: openbao-init.sh exit=%d", exitCode)
	}
	if !cok {
		return ErrShareCaptureMissing
	}
	if n := buf.ShareCount(); n != 5 {
		return custody(ErrShareCaptureMissing, "got %d shares, want 5", n)
	}
	if tok, _ := buf.RootToken(); len(tok) == 0 {
		return custody(ErrShareCaptureMissing, "root token absent from payload")
	}

	// --- Phase B: durable custody ---

	// NOTE ON ROLLBACK (P0, review-pass): there is deliberately NO
	// Git.ResetHard anywhere below.
	//
	// The point of no return is the SCRIPT RUN in Phase A, not the
	// commit. The moment openbao-init.sh returns, the vault holds a
	// master key that can ONLY be reconstructed from the 5 shares in
	// `buf` — they are irreplaceable. `preSHA` rollback does not
	// un-initialize the vault; it just deletes the recovery material,
	// turning a recoverable failure into a permanently sealed vault.
	//
	// That is exactly what happened on cs/next (2026-07): the push
	// failed on an SSH remote, the old code rolled back, and the fleet
	// lost the encrypted shares while OpenBao stayed Initialized. It
	// was recoverable only because the operator happened to pass the
	// OPTIONAL --openbao-shares-out; without it the vault would have
	// been unrecoverable. Fixing the push auth removed one trigger —
	// this removes the data-loss invariant itself.
	//
	// Failure policy from here on: PRESERVE, never destroy. Every path
	// leaves the shares somewhere durable and tells the operator how to
	// finish by hand.

	if err := writeAndVerifyShares(ctx, opts.SOPS, secretsPath, buf, out); err != nil {
		// The shares are already on disk from the write-ahead dump, so
		// this is a "finish by hand" error, not a data-loss one.
		return custody(err, "share encryption into %s failed (add them by hand "+
			"once recovered)", secretsPath)
	}

	if opts.SharesOutPath != "" {
		if err := writeSharesOutFile(opts.SharesOutPath, buf); err != nil {
			// Non-fatal: the shares are already encrypted + verified
			// in the working tree, which is the authoritative custody.
			// This file is an operator convenience copy, so its failure
			// must not abandon the ceremony (and must never roll back).
			fmt.Fprintf(out, "[openbao] WARNING: shares-out write to %s failed: %v\n",
				opts.SharesOutPath, err)
			fmt.Fprintf(out, "[openbao] continuing — shares are encrypted in %s; "+
				"re-create the off-git copy from there if you need one\n", secretsPath)
		} else {
			fmt.Fprintf(out, "[openbao] shares written to %s (mode 0600)\n", opts.SharesOutPath)
		}
	}

	msg := fmt.Sprintf("feat(%s): encrypt OpenBao Shamir shares into secrets.enc.yaml", opts.ClusterName)
	commitSHA, err := commitAndMaybePush(ctx, opts, msg)
	if err != nil {
		// The cs/next case. The encrypted shares are in the working
		// tree (and possibly committed but unpushed). Keep them and
		// hand the operator the exact remaining steps.
		fmt.Fprintf(out, "[openbao] commit/push failed — NOT rolling back; "+
			"the encrypted shares in %s are recovery material\n", secretsPath)
		return custody(err, "commit/push failed after the vault was initialized. %s",
			manualFinishHint(opts, secretsPath, msg, commitSHA))
	}
	fmt.Fprintf(out, "[openbao] commit=%s%s — shares durable\n", commitSHA, pushedSuffix(opts.NoPush))

	// The shares are now encrypted, verified and committed (and pushed
	// unless --no-push). The write-ahead plaintext copy has served its
	// purpose; leaving it behind would quietly scatter unencrypted
	// recovery material across operator laptops. An operator who wants a
	// lasting off-git copy asks for one with --openbao-shares-out.
	if custodyPath != "" {
		if rmErr := os.Remove(custodyPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			fmt.Fprintf(out, "[openbao] WARNING: could not remove the write-ahead copy at %s: %v — "+
				"delete it by hand; it holds the shares in plaintext\n", custodyPath, rmErr)
		} else {
			fmt.Fprintf(out, "[openbao] write-ahead copy at %s removed (shares are encrypted + committed)\n", custodyPath)
		}
	}

	// --- Phase C: irreversible post-commit operations ---
	//
	// Failures here do NOT trigger rollback. The shares are
	// already encrypted + (push case) on the remote. Rolling back
	// would lose the only durable copy of the recovery material.
	// The operator's remediation is `bootstrap openbao
	// setup-controller-auth <name>` (or `unseal <name>` for an
	// unseal failure) — both idempotent and resume from where
	// init's Phase C left off.

	if err := postInitUnseal(ctx, opts, buf, out); err != nil {
		fmt.Fprintf(out, "[openbao] post-commit unseal failed (commit %s already %s); run `kube-dc bootstrap openbao unseal %s` to resume\n",
			commitSHA, pushedStatusForRecovery(opts.NoPush), opts.ClusterName)
		return fmt.Errorf("%w: %v", ErrPostCommitIrreversible, err)
	}

	// Capture the root token ONCE here. defer RevokeSelf IMMEDIATELY
	// so any subsequent failure (controller-auth setup, annotate, or
	// a panic) still revokes. The token bytes stay in buf — outer
	// Scrub() wipes them on every return path.
	rootToken, err := buf.RootToken()
	if err != nil || len(rootToken) == 0 {
		fmt.Fprintf(out, "[openbao] root-token capture failed (commit %s already %s); manual remediation required\n",
			commitSHA, pushedStatusForRecovery(opts.NoPush))
		return fmt.Errorf("%w: retrieve root token: %v", ErrPostCommitIrreversible, err)
	}
	defer func() {
		if rerr := opts.OpenBao.RevokeSelf(ctx, rootToken); rerr != nil {
			fmt.Fprintf(out, "[openbao] WARNING root-token revoke failed (commit %s already %s): %v — manual remediation: `bao token revoke -self` with the captured token\n",
				commitSHA, pushedStatusForRecovery(opts.NoPush), rerr)
		}
	}()

	if !opts.SkipControllerAuth {
		if err := postInitSetupControllerAuth(ctx, opts, rootToken, out); err != nil {
			fmt.Fprintf(out, "[openbao] post-commit controller-auth setup failed (commit %s already %s); run `kube-dc bootstrap openbao setup-controller-auth %s --repo <fleet>` to resume\n",
				commitSHA, pushedStatusForRecovery(opts.NoPush), opts.ClusterName)
			return fmt.Errorf("%w: %v", ErrPostCommitIrreversible, err)
		}
	} else {
		fmt.Fprintln(out, "[openbao] --skip-controller-auth — leaving auth/k8s-host + controller policies unset; run `bootstrap openbao setup-controller-auth` manually before the manager can login")
	}

	if err := postInitAnnotateBoth(ctx, opts, out); err != nil {
		fmt.Fprintf(out, "[openbao] service-annotation failed (commit %s already %s); manual remediation: `kubectl -n openbao annotate svc/openbao %s=<rfc3339> %s=<rfc3339> --overwrite`\n",
			commitSHA, pushedStatusForRecovery(opts.NoPush),
			AnnotationBootstrapFinalized,
			AnnotationControllerAuthInstalled)
		return fmt.Errorf("%w: %v", ErrPostCommitIrreversible, err)
	}

	if opts.SkipControllerAuth {
		fmt.Fprintln(out, "[openbao] init complete (--skip-controller-auth): shares encrypted, openbao-0 unsealed, finalized stamp set. Controller-auth NOT installed — run setup-controller-auth before the manager can login.")
	} else {
		fmt.Fprintln(out, "[openbao] init complete — shares encrypted, openbao-0 unsealed, controller-auth installed, finalized + controller-auth-installed stamps set")
	}
	return nil
}

// postInitSetupControllerAuth thinly wraps SetupControllerAuth so
// init Phase C and the standalone setup-controller-auth subcommand
// share a single engine. The token here is the SAME root token init
// captured from its own generate-root ceremony — the caller (Init)
// owns the defer RevokeSelf.
func postInitSetupControllerAuth(ctx context.Context, opts InitOptions, token []byte, out io.Writer) error {
	if opts.K8s == nil {
		return fmt.Errorf("postInitSetupControllerAuth: K8s client required (set InitOptions.K8s)")
	}
	return SetupControllerAuth(ctx, SetupControllerAuthOptions{
		Token:       token,
		RefreshMode: RefreshFull,
		OpenBao:     opts.OpenBao,
		K8s:         opts.K8s,
		Out:         out,
	})
}

// postInitAnnotateBoth stamps both Phase C markers atomically via a
// single SetAnnotations call. On SkipControllerAuth the
// controller-auth-installed marker is omitted (since that step was
// skipped); the bootstrap-finalized marker is always stamped to gate
// init from being re-run.
func postInitAnnotateBoth(ctx context.Context, opts InitOptions, out io.Writer) error {
	stamp := time.Now().UTC().Format(time.RFC3339)
	kv := map[string]string{
		AnnotationBootstrapFinalized: stamp,
	}
	if !opts.SkipControllerAuth {
		kv[AnnotationControllerAuthInstalled] = stamp
	}
	fmt.Fprintf(out, "[openbao] annotating svc/openbao with %d marker(s) (atomic patch)\n", len(kv))
	return opts.OpenBao.SetAnnotations(ctx, "openbao", kv)
}

// pushedStatusForRecovery is the noun the recovery hints use to
// describe the commit state — "pushed" or "committed locally".
// Mirrors pushedSuffix but reads as an adjective inside a longer
// sentence.
func pushedStatusForRecovery(noPush bool) string {
	if noPush {
		return "committed locally (not yet pushed; remember to push manually)"
	}
	return "pushed to remote"
}

// postInitUnseal feeds 3 shares via stdin to openbao-0's `bao
// operator unseal -` (review-pass P1: never argv). Pod name is
// the script's default; if a future deployment changes the
// canonical bootstrap pod, the engine reads PodList[0] which is
// always the lexicographically-first pod (openbao-0 in the Helm
// chart's default Statefulset).
func postInitUnseal(ctx context.Context, opts InitOptions, buf *secrets.Buffer, out io.Writer) error {
	pods, err := opts.OpenBao.PodList(ctx)
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no openbao pods")
	}
	pod := pods[0]
	fmt.Fprintf(out, "[openbao] unsealing %s (3 shares via stdin)\n", pod)
	for i := 0; i < 3; i++ {
		share, err := buf.Share(i)
		if err != nil {
			return fmt.Errorf("retrieve share %d: %w", i+1, err)
		}
		if len(share) == 0 {
			return fmt.Errorf("share %d empty", i+1)
		}
		if err := opts.OpenBao.Unseal(ctx, pod, share); err != nil {
			for j := range share {
				share[j] = 0
			}
			return fmt.Errorf("unseal %s with share %d: %w", pod, i+1, err)
		}
		for j := range share {
			share[j] = 0
		}
	}
	return nil
}

// postInitRevokeRoot + postInitAnnotate were removed in the M5-T08
// rework. Their behavior is now baked into Init's Phase C as:
//   - root-token revoke = a `defer RevokeSelf` registered the moment
//     buf.RootToken() succeeds (BEFORE controller-auth setup runs),
//     so any setup failure still revokes;
//   - annotation = postInitAnnotateBoth using the atomic
//     SetAnnotations port method (stamps both bootstrap-finalized
//     AND controller-auth-installed in one patch).

func validateInitOptions(opts InitOptions) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("%w: ClusterName", ErrInitMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrInitMissingDependency)
	}
	if opts.Runner == nil {
		return fmt.Errorf("%w: Runner", ErrInitMissingDependency)
	}
	if opts.SOPS == nil {
		return fmt.Errorf("%w: SOPS", ErrInitMissingDependency)
	}
	if opts.Git == nil {
		return fmt.Errorf("%w: Git", ErrInitMissingDependency)
	}
	if opts.OpenBao == nil {
		return fmt.Errorf("%w: OpenBao", ErrInitMissingDependency)
	}
	return nil
}

// --- Payload parser ---

// baoInitJSON is the on-the-wire shape of `bao operator init
// -format=json` output (the subset we consume). The full payload
// also has `keys` (hex-encoded duplicates), `unseal_threshold`,
// `unseal_shares`, and `recovery_*` fields — all ignored.
type baoInitJSON struct {
	UnsealKeysB64 []string `json:"unseal_keys_b64"`
	RootToken     string   `json:"root_token"`
}

// loadBufferFromJSON parses `payload` as bao-init JSON and populates
// the buffer. Returns a typed error when fields are missing or
// counts wrong — keeps the sentinel-callback error surface narrow
// (parse errors propagate to ErrShareCaptureMissing wrappers at
// the caller).
func loadBufferFromJSON(buf *secrets.Buffer, payload []byte) error {
	// Trim leading/trailing whitespace; some `bao` versions emit a
	// trailing newline.
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return errors.New("empty payload")
	}

	var parsed baoInitJSON
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}

	// STORE FIRST, VALIDATE SECOND (P0, review-pass 3). This used to
	// validate the share count and the root token before storing
	// anything, which meant a payload that was merely *incomplete* —
	// 4 shares, or 5 shares with no root token — left the buffer empty
	// and gave the caller's write-ahead custody nothing to preserve.
	// The vault is initialized by then either way, so shares present in
	// the payload are real recovery material and must be kept even when
	// the payload as a whole is rejected. Anything stored here is
	// visible to emergencyDumpShares on the error path.
	for i, share := range parsed.UnsealKeysB64 {
		if share == "" {
			continue // record the hole; reported below
		}
		buf.SetShare(i, []byte(share))
	}
	if parsed.RootToken != "" {
		buf.SetRootToken([]byte(parsed.RootToken))
	}

	if len(parsed.UnsealKeysB64) != 5 {
		return fmt.Errorf("expected 5 unseal_keys_b64, got %d", len(parsed.UnsealKeysB64))
	}
	for i, share := range parsed.UnsealKeysB64 {
		if share == "" {
			return fmt.Errorf("unseal_keys_b64[%d] empty", i)
		}
	}
	if parsed.RootToken == "" {
		return errors.New("root_token absent")
	}
	return nil
}

// --- Drain helper ---

// drainOpenBaoInit reads the script's Line channel + writes
// non-sentinel lines through to `out` (the sentinel-captured payload
// itself never reaches this drainer — the runner emits a
// placeholder in its place). Returns the integer exit code.
func drainOpenBaoInit(lines <-chan ports.Line, out io.Writer) (int, error) {
	return ports.Drain(lines, func(ln ports.Line) {
		tag := "stdout"
		if ln.Stream == ports.StreamStderr {
			tag = "stderr"
		}
		fmt.Fprintf(out, "[openbao-init %s] %s\n", tag, ln.Text)
	})
}

// --- Share encryption + round-trip verify ---

// shareKey returns the cluster-config key name for the i-th share
// (1-indexed per the fleet convention: OPENBAO_UNSEAL_KEY_1 ..
// OPENBAO_UNSEAL_KEY_5).
func shareKey(i int) string {
	return fmt.Sprintf("OPENBAO_UNSEAL_KEY_%d", i+1)
}

// writeAndVerifyShares encrypts each share via SOPS.SetStringData
// + verifies round-trip via SOPS.Decrypt + bytes.Contains. Mismatch
// returns ErrShareCustodyFailed for the caller to roll back.
func writeAndVerifyShares(ctx context.Context, sops ports.SOPSClient, path string, buf *secrets.Buffer, out io.Writer) error {
	for i := 0; i < 5; i++ {
		share, err := buf.Share(i)
		if err != nil {
			return fmt.Errorf("retrieve share %d from buffer: %w", i+1, err)
		}
		if len(share) == 0 {
			return fmt.Errorf("share %d empty in buffer", i+1)
		}

		key := shareKey(i)
		if err := sops.SetStringData(ctx, path, key, share); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}

		// Round-trip verify. Decrypt returns the full file; we
		// bytes.Contains the share's plaintext (the value will
		// appear once as a YAML stringData entry).
		decrypted, err := sops.Decrypt(ctx, path)
		if err != nil {
			return fmt.Errorf("verify-decrypt %s: %w", path, err)
		}
		// Scrub the decrypted blob ASAP — it contains every share
		// written so far. The bytes.Contains read is the only thing
		// we need from it.
		match := bytes.Contains(decrypted, share)
		for j := range decrypted {
			decrypted[j] = 0
		}
		// And scrub our local share copy. The buffer still holds
		// the canonical copy for any subsequent iteration that
		// needs it (Buffer.Share returns a fresh copy each call).
		for j := range share {
			share[j] = 0
		}
		if !match {
			return fmt.Errorf("%w: %s value didn't survive round-trip", ErrShareCustodyFailed, key)
		}
		fmt.Fprintf(out, "[openbao] encrypted %s (verified round-trip)\n", key)
	}
	return nil
}

// --- shares-out file writer ---

// writeSharesOutFile emits the operator's optional off-fleet plaintext
// copy of the 5 Shamir shares. The root token is NOT included —
// the CLI's Init engine revokes it via RevokeSelf in Phase C, so
// recording it here would be both stale and a needless secret-
// surface increase. The shares ARE what the operator needs for
// recovery (unseal, generate-root, rekey). Refuses paths inside
// any git working tree (caller already checked; re-assert as a
// belt-and-braces defence). Mode 0600.
func writeSharesOutFile(path string, buf *secrets.Buffer) error {
	body, err := renderSharesYAML(buf, "Generated by `kube-dc bootstrap openbao init --openbao-shares-out`.")
	if err != nil {
		return err
	}
	defer scrub(body)
	return writeSharesFile(path, body, false /* allow replace */)
}

// renderSharesYAML pretty-prints the captured shares as operator-readable
// YAML.
//
// It renders however many shares the buffer actually holds rather than
// insisting on 5. That matters only on the emergency path: a partial
// capture (say 3 of 5 shares) is exactly when we most need to persist
// what we have — refusing to write because the payload was incomplete
// would throw away recoverable material. A count other than 5 is called
// out in the header so the operator is never misled into thinking a
// partial dump is a full custody copy.
//
// NEVER use fmt.Sprintf with %v / %q on a share — those formatters can
// be subverted by an unusual character, and the operator's eye expects
// clean output.
func renderSharesYAML(buf *secrets.Buffer, provenance string) ([]byte, error) {
	n := buf.ShareCount()
	var b bytes.Buffer
	b.WriteString("# kube-dc OpenBao Shamir shares (5/3) — KEEP THIS FILE SAFE.\n")
	b.WriteString("# " + provenance + "\n")
	b.WriteString("# Mode 0600 + outside any git tree.\n")
	b.WriteString("# The root token is NOT recorded — Init revokes it after capture.\n")
	if n != 5 {
		b.WriteString(fmt.Sprintf("# WARNING: PARTIAL CAPTURE — %d of 5 shares. Unsealing needs 3.\n", n))
	}
	b.WriteString("openbao:\n")
	b.WriteString("  shares:\n")
	written := 0
	for i := 0; i < n; i++ {
		share, err := buf.Share(i)
		if err != nil {
			return nil, fmt.Errorf("retrieve share %d: %w", i+1, err)
		}
		if len(share) == 0 {
			continue // hole in a partial capture; skip rather than emit a blank item
		}
		b.WriteString("    - ")
		b.Write(share)
		b.WriteString("\n")
		scrub(share)
		written++
	}
	if written == 0 {
		return nil, fmt.Errorf("no shares to write (buffer holds %d)", n)
	}
	return b.Bytes(), nil
}

// writeSharesFile durably persists rendered share YAML at 0600.
//
// Durability matters more here than anywhere else in the CLI: this file
// may be the only copy of the vault's recovery material, and the process
// is frequently about to exit with an error. So we fsync the file AND its
// parent directory before returning — without the directory fsync a crash
// or power loss can lose the rename itself even though the data landed.
//
// noClobber selects the publish primitive: os.Link fails with EEXIST
// rather than replacing, so emergency dumps can never destroy an earlier
// dump. The operator-supplied --openbao-shares-out path keeps rename
// semantics (the operator named that exact file and expects it updated).
func writeSharesFile(path string, body []byte, noClobber bool) error {
	if err := assertNotInsideAnyGitTree(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Atomic write at 0600. CreateTemp default mode is 0600 already
	// but we Chmod explicitly to be unambiguous.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if _, werr := tmp.Write(body); werr != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, werr)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	// fsync BEFORE publishing: a rename/link that beats its own data to
	// disk yields a present-but-empty shares file, which reads as custody
	// while holding nothing.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}

	if noClobber {
		// Link is atomic and refuses to replace an existing name.
		if err := os.Link(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("link %s: %w", path, err)
		}
		_ = os.Remove(tmpPath)
	} else if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", tmpPath, err)
	}

	return fsyncDir(dir)
}

// fsyncDir flushes a directory entry so the just-published filename
// survives a crash. Best-effort on platforms that reject directory opens.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil //nolint:nilerr // data is already fsynced; entry durability is best-effort
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return nil //nolint:nilerr // EINVAL on some filesystems; not worth failing custody over
	}
	return nil
}

func scrub(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// assertNotInsideAnyGitTree walks up from `path` looking for a
// `.git/` directory. If found at any ancestor level, returns
// ErrSharesOutInsideGit with the offending repo path. Symlinks
// are not resolved — the operator's path string is what we check.
func assertNotInsideAnyGitTree(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve abs path %s: %w", path, err)
	}
	cur := filepath.Dir(abs)
	for {
		if info, err := os.Stat(filepath.Join(cur, ".git")); err == nil && info.IsDir() {
			return fmt.Errorf("%w (repo at %s)", ErrSharesOutInsideGit, cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil // hit filesystem root without finding .git
		}
		cur = parent
	}
}

// --- Commit + push helpers (mirrors M4-T12 Apply pattern) ---

func commitAndMaybePush(ctx context.Context, opts InitOptions, msg string) (string, error) {
	if opts.NoPush {
		return opts.Git.Commit(ctx, opts.FleetRepo, msg)
	}
	return opts.Git.CommitAndPush(ctx, opts.FleetRepo, msg, opts.GitHubToken)
}

// manualFinishHint builds the exact commands an operator must run to
// finish a ceremony whose commit/push step failed.
//
// It branches on what actually happened, because a one-size hint does not
// merely read oddly — it fails to run. CommitAndPush returns a non-empty
// SHA when the commit succeeded and only the push failed; telling that
// operator to `git add && git commit && git push` stops at the commit,
// which exits non-zero with "nothing to commit", so the `&&` chain never
// reaches the push and the shares stay local.
func manualFinishHint(opts InitOptions, secretsPath, msg, commitSHA string) string {
	switch {
	case commitSHA != "" && opts.NoPush:
		// Commit landed and no push was ever wanted: nothing to do.
		return fmt.Sprintf("commit %s already holds the shares (--no-push set); no manual git step is needed",
			commitSHA)
	case commitSHA != "":
		// Commit landed, push failed — push only.
		return fmt.Sprintf("finish by hand:  cd %s && git push   (commit %s already holds the shares)",
			opts.FleetRepo, commitSHA)
	case opts.NoPush:
		// Commit itself failed and no push was wanted.
		return fmt.Sprintf("finish by hand:  cd %s && git add %s && git commit -m %q   (--no-push set; push when ready)",
			opts.FleetRepo, secretsPath, msg)
	default:
		return fmt.Sprintf("finish by hand:  cd %s && git add %s && git commit -m %q && git push",
			opts.FleetRepo, secretsPath, msg)
	}
}

func pushedSuffix(noPush bool) string {
	if noPush {
		return " (--no-push set; not pushed to remote)"
	}
	return " pushed"
}

// emergencyDumpShares writes an off-git, 0600 plaintext copy of the
// Shamir shares when the ceremony fails AFTER the vault was initialized.
//
// It exists because share loss is unrecoverable: once openbao-init.sh
// returns, the vault's master key can only be reconstructed from these
// shares. Any failure path that would otherwise end the process with the
// shares only in memory (or would leave them at the mercy of a later
// `git checkout`) calls this first.
//
// The path is derived, not operator-supplied, so it works even when
// --openbao-shares-out was never passed. It deliberately lands under the
// user's home (never the fleet tree) and reuses the same git-tree guard
// as the regular shares-out path.
//
// It never overwrites and never reuses a name. Two properties get us
// there:
//
//   - The cluster segment is percent-encoded, so nested names stay
//     distinct: "cs/next" -> "cs%2Fnext", which can no longer be confused
//     with a literal cluster named "cs-next" (the old "/"->"-" flattening
//     mapped both to the same file).
//   - Publication uses O_EXCL semantics (see writeSharesFile), and on
//     EEXIST we advance to the next free suffix instead of replacing.
//     A second failed ceremony therefore preserves the first one's shares
//     — those may belong to a still-sealed vault nobody has recovered yet.
//
// Callers are responsible for telling the operator where the file landed;
// this function is deliberately silent so it is safe to call from the
// sentinel-callback goroutine (see the write-ahead custody note in Init).
func emergencyDumpShares(opts InitOptions, buf *secrets.Buffer) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	body, err := renderSharesYAML(buf,
		fmt.Sprintf("EMERGENCY dump from `kube-dc bootstrap openbao init %s` — the ceremony did not complete.", opts.ClusterName))
	if err != nil {
		return "", err
	}
	defer scrub(body)

	dir := filepath.Join(home, ".kube-dc")
	base := "openbao-emergency-shares-" + url.PathEscape(opts.ClusterName)

	var lastErr error
	for i := 0; i < emergencyDumpMaxAttempts; i++ {
		name := base + ".yaml"
		if i > 0 {
			name = fmt.Sprintf("%s.%d.yaml", base, i)
		}
		path := filepath.Join(dir, name)
		err := writeSharesFile(path, body, true /* no clobber */)
		if err == nil {
			return path, nil
		}
		if errors.Is(err, os.ErrExist) {
			lastErr = err
			continue // an earlier dump owns this name; keep it, take the next
		}
		return "", err
	}
	return "", fmt.Errorf("all %d emergency dump filenames under %s are taken (refusing to overwrite): %w",
		emergencyDumpMaxAttempts, dir, lastErr)
}

// emergencyDumpMaxAttempts bounds the no-clobber filename search. Hitting
// it means ~100 un-cleaned-up failed ceremonies for one cluster, which is
// an operator problem to surface, not one to silently overwrite.
const emergencyDumpMaxAttempts = 100

// preserveShares dumps and announces. Used by every post-capture failure
// path so the message an operator sees is identical no matter which step
// failed.
func preserveShares(opts InitOptions, buf *secrets.Buffer, out io.Writer) (string, error) {
	path, err := emergencyDumpShares(opts, buf)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "[openbao] EMERGENCY: shares preserved at %s (mode 0600) — "+
		"move them somewhere safe\n", path)
	return path, nil
}
