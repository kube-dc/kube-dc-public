// Package openbao owns the engine for `kube-dc bootstrap openbao …`
// subcommands per installer-prd §12 and installer-ux §1.1. Sub-slices:
//
//   - Init (M5-T01): captures the 5 Shamir shares + root token from
//     `bootstrap/openbao-init.sh` and SOPS-encrypts them into
//     `clusters/<name>/secrets.enc.yaml` under
//     `OPENBAO_UNSEAL_KEY_{1..5}`. Never writes plaintext to disk.
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
//     byte-compared; mismatch → rollback (git.ResetHard to
//     pre-commit SHA) + return a hard error.
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
// encrypted on disk differs from the captured plaintext. Triggers
// the git rollback path.
var ErrShareCustodyFailed = errors.New("openbao: share-custody round-trip verify failed")

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
// **Phase A — pre-flight + capture share JSON (reversible)**:
//
//  1. Preflight: `<repo>/clusters/<name>/secrets.enc.yaml` exists.
//  2. `--openbao-shares-out` path safety: refuse git-tree paths.
//  3. Dirty-tree gate via Git.Diff — refuse uncommitted changes.
//  4. Already-initialised check via OpenBao.GetAnnotation — refuse
//     if `kube-dc.com/openbao-bootstrap-finalized` is set.
//  5. Capture pre-commit SHA via Git.Head (rollback target — only
//     used for Phase B failures, before commit lands).
//  6. Run `openbao-init.sh` with the sentinel callback.
//     The script emits 5 shares + 1 root token JSON between
//     sentinels; the callback parses it into a secrets.Buffer.
//     The script no longer performs any post-init action — unseal,
//     revoke, annotate all moved to Phase C below.
//
// **Phase B — durable custody (still reversible up to commit)**:
//
//  7. For each share, SOPS.SetStringData + round-trip verify.
//     Mismatch → Git.ResetHard(preSHA) + return.
//  8. Optional `--openbao-shares-out` plaintext copy (mode 0600,
//     outside any git tree). Failure → rollback.
//  9. Commit + push. Push failure → rollback (same shape as
//     M4-T12: the encrypted file is on remote anyway only if push
//     succeeded; rollback before push is safe).
//
// **Phase C — irreversible post-commit operations (no rollback)**:
//
//  10. Unseal openbao-0 with 3 shares via OpenBaoClient.Unseal
//      (stdin transport — never argv).
//  11. Revoke the root token via OpenBaoClient.RevokeSelf (stdin).
//  12. Annotate svc/openbao with
//      `kube-dc.com/openbao-bootstrap-finalized=<rfc3339>`.
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

	preSHA, err := opts.Git.Head(ctx, opts.FleetRepo)
	if err != nil {
		return fmt.Errorf("openbao: capture pre-commit SHA: %w", err)
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
	)
	captureCB := func(_ ports.ScriptKind, _ string, payload []byte) error {
		captureMu.Lock()
		defer captureMu.Unlock()
		captured = true
		if perr := loadBufferFromJSON(buf, payload); perr != nil {
			captureErr = perr
			return perr
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
	if drainErr != nil {
		return fmt.Errorf("openbao: drain: %w", drainErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("openbao: openbao-init.sh exit=%d", exitCode)
	}

	captureMu.Lock()
	cerr := captureErr
	cok := captured
	captureMu.Unlock()
	if cerr != nil {
		return fmt.Errorf("openbao: parse captured payload: %w", cerr)
	}
	if !cok {
		return ErrShareCaptureMissing
	}
	if n := buf.ShareCount(); n != 5 {
		return fmt.Errorf("%w: got %d shares, want 5", ErrShareCaptureMissing, n)
	}
	if tok, _ := buf.RootToken(); len(tok) == 0 {
		return fmt.Errorf("%w: root token absent from payload", ErrShareCaptureMissing)
	}

	// --- Phase B: durable custody ---

	if err := writeAndVerifyShares(ctx, opts.SOPS, secretsPath, buf, out); err != nil {
		// secrets.enc.yaml may have been partially mutated; roll
		// back to clean state. No remote irreversible state has
		// happened yet (Phase C hasn't run), so this rollback is
		// safe.
		fmt.Fprintf(out, "[openbao] share-write failed; rolling back to %s\n", preSHA)
		if rerr := opts.Git.ResetHard(ctx, opts.FleetRepo, preSHA); rerr != nil {
			return fmt.Errorf("openbao: write failed AND rollback failed: write=%w; rollback=%v", err, rerr)
		}
		return err
	}

	if opts.SharesOutPath != "" {
		if err := writeSharesOutFile(opts.SharesOutPath, buf); err != nil {
			fmt.Fprintf(out, "[openbao] shares-out write failed; rolling back to %s\n", preSHA)
			if rerr := opts.Git.ResetHard(ctx, opts.FleetRepo, preSHA); rerr != nil {
				return fmt.Errorf("openbao: shares-out write failed AND rollback failed: write=%w; rollback=%v", err, rerr)
			}
			return fmt.Errorf("openbao: shares-out write: %w", err)
		}
		fmt.Fprintf(out, "[openbao] shares written to %s (mode 0600)\n", opts.SharesOutPath)
	}

	msg := fmt.Sprintf("feat(%s): encrypt OpenBao Shamir shares into secrets.enc.yaml", opts.ClusterName)
	commitSHA, err := commitAndMaybePush(ctx, opts, msg)
	if err != nil {
		fmt.Fprintf(out, "[openbao] commit-push failed; rolling back to %s\n", preSHA)
		if rerr := opts.Git.ResetHard(ctx, opts.FleetRepo, preSHA); rerr != nil {
			return fmt.Errorf("openbao: commit-push failed AND rollback failed: commit-push=%w; rollback=%v", err, rerr)
		}
		return fmt.Errorf("openbao: commit-push: %w", err)
	}
	fmt.Fprintf(out, "[openbao] commit=%s%s — shares durable\n", commitSHA, pushedSuffix(opts.NoPush))

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
	if len(parsed.UnsealKeysB64) != 5 {
		return fmt.Errorf("expected 5 unseal_keys_b64, got %d", len(parsed.UnsealKeysB64))
	}
	if parsed.RootToken == "" {
		return errors.New("root_token absent")
	}
	for i, share := range parsed.UnsealKeysB64 {
		if share == "" {
			return fmt.Errorf("unseal_keys_b64[%d] empty", i)
		}
		buf.SetShare(i, []byte(share))
	}
	buf.SetRootToken([]byte(parsed.RootToken))
	return nil
}

// --- Drain helper ---

// drainOpenBaoInit reads the script's Line channel + writes
// non-sentinel lines through to `out` (the sentinel-captured payload
// itself never reaches this drainer — the runner emits a
// placeholder in its place). Returns the integer exit code.
func drainOpenBaoInit(lines <-chan ports.Line, out io.Writer) (int, error) {
	exit := 0
	for ln := range lines {
		if ln.Stream == ports.StreamExit {
			n, err := parseExitCode(ln.Text)
			if err != nil {
				return 0, fmt.Errorf("parse exit code %q: %w", ln.Text, err)
			}
			exit = n
			continue
		}
		tag := "stdout"
		if ln.Stream == ports.StreamStderr {
			tag = "stderr"
		}
		fmt.Fprintf(out, "[openbao-init %s] %s\n", tag, ln.Text)
	}
	return exit, nil
}

func parseExitCode(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("empty exit code")
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
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
	if err := assertNotInsideAnyGitTree(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Pretty-print as YAML so the file is operator-readable on
	// recovery. NEVER use fmt.Sprintf with %v / %q on a share —
	// those formatters can be subverted by an unusual character +
	// the operator's eye expects clean output.
	var b bytes.Buffer
	b.WriteString("# kube-dc OpenBao Shamir shares (5/3) — KEEP THIS FILE SAFE.\n")
	b.WriteString("# Generated by `kube-dc bootstrap openbao init --openbao-shares-out`.\n")
	b.WriteString("# Mode 0600 + outside any git tree.\n")
	b.WriteString("# The root token is NOT recorded — Init revokes it after capture.\n")
	b.WriteString("openbao:\n")
	b.WriteString("  shares:\n")
	for i := 0; i < 5; i++ {
		share, err := buf.Share(i)
		if err != nil {
			return fmt.Errorf("retrieve share %d: %w", i+1, err)
		}
		b.WriteString("    - ")
		b.Write(share)
		b.WriteString("\n")
		for j := range share {
			share[j] = 0
		}
	}

	// Atomic write at 0600. CreateTemp default mode is 0600 already
	// but we Chmod explicitly to be unambiguous.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, werr := tmp.Write(b.Bytes()); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, werr)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	// Scrub the buffer used for the YAML build before rename — the
	// rename only moves bytes that are already on disk.
	bbytes := b.Bytes()
	for i := range bbytes {
		bbytes[i] = 0
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", tmpPath, err)
	}
	return nil
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

func pushedSuffix(noPush bool) string {
	if noPush {
		return " (--no-push set; not pushed to remote)"
	}
	return " pushed"
}

