package openbao

// M5-T06 — `kube-dc bootstrap openbao generate-root`.
//
// One-shot diagnostic. Decrypts the 3-of-5 Shamir threshold from
// clusters/<name>/secrets.enc.yaml, runs the OpenBao
// `bao operator generate-root` ceremony against the live pods, and
// prints the resulting short-lived root token to stdout. Used
// operationally for:
//
//   - Emergency `bao` commands the CLI doesn't wrap (dev auth
//     methods, secret backends outside the M5-T08 policy set, etc.).
//   - Verifying that generate-root itself still works after a chart
//     upgrade or an OpenBao version bump.
//
// Distinct from M5-T05 reveal-shares:
//
//   - Reveal-shares hands out share MATERIAL (persistent secret
//     that unseals the cluster). This subcommand hands out a
//     short-lived ROOT TOKEN (revocable + time-bound by OpenBao's
//     defaults).
//   - Reveal-shares works on SOPS material alone (no cluster
//     contact). This subcommand needs a live cluster + reachable
//     OpenBao pods — the ceremony runs via `kubectl exec` inside
//     the active pod.
//
// Consent: same two-gate as reveal-shares (--i-understand-the-risk
// OR REVEAL=true). Root tokens are cluster-admin credentials; the
// friction is deliberate.
//
// Revoke policy:
//
//   - Default: print the token; DO NOT auto-revoke. Operator uses
//     the token in a separate `bao` session, then revokes manually
//     via `bao token revoke <token>`. This is the useful-default
//     case — an operator asking for a token typically wants one
//     they can actually use.
//   - `--revoke-immediately`: run the full ceremony, print, then
//     RevokeSelf before returning. Health-check mode — proves
//     generate-root works end-to-end without leaving a live token.
//     Also the shape a CI job can use as a smoke.
//
// Buffers scrubbed: shares (post-ceremony) + the local token copy
// (post-write + post-revoke). Even under --no-auto-revoke the local
// slice is zeroed; the operator holds the STRING form off the
// terminal thereafter.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// GenerateRootOptions is the parameter bundle for GenerateRoot.
type GenerateRootOptions struct {
	ClusterName string
	FleetRepo   string
	SOPS        SOPSDecrypter
	OpenBao     ports.OpenBaoClient

	// Consent — flipped by the cobra layer after resolving
	// --i-understand-the-risk / REVEAL=true. Engine refuses when
	// false so tests + library callers must go through the same
	// friction as the CLI.
	Consent bool

	// OperatorName lands in the audit line. Empty resolves to
	// "<unknown>" (same convention as RevealShares).
	OperatorName string

	// RevokeImmediately runs RevokeSelf after the token write. Off
	// by default — operators typically want a usable token; the
	// health-check-only flow opts in via this flag.
	RevokeImmediately bool

	// Now is a test seam for the audit timestamp.
	Now func() time.Time

	// Out is stdout in the cobra layer; carries the token line.
	// Nil is safe (io.Discard).
	Out io.Writer

	// Audit is stderr in the cobra layer; carries the audit +
	// revoke-status lines. Nil is safe (io.Discard).
	Audit io.Writer
}

// --- Errors ---

// ErrGenerateRootConsentRequired mirrors reveal's consent sentinel.
var ErrGenerateRootConsentRequired = errors.New("openbao generate-root: consent required (pass --i-understand-the-risk or set REVEAL=true)")

// ErrGenerateRootMissingDependency surfaces on nil SOPS / OpenBao /
// empty FleetRepo / empty ClusterName.
var ErrGenerateRootMissingDependency = errors.New("openbao generate-root: missing required dependency")

// ErrGenerateRootAuditFailed — the PRE-EMIT audit write failed
// (audit header line before the token reaches stdout). Engine
// refuses to emit the token in this state — the fail-closed
// invariant means no token ever lands on stdout without a durable
// audit record fronting it. Distinct sentinel from
// ErrGenerateRootStatusAuditFailed which fires AFTER the token has
// already been written; the two are not interchangeable.
var ErrGenerateRootAuditFailed = errors.New("openbao generate-root: pre-emit audit write failed — refusing to emit token without a durable audit record")

// ErrGenerateRootStatusAuditFailed — a POST-EMIT audit trailer
// write failed (the LIVE-token hint / revoke-status line). Token
// IS already on stdout; the audit trailer that would tell the
// operator what to do next is broken. Operator needs to know so
// they don't assume the LIVE-token hint (or revoke confirmation)
// landed. Distinct from ErrGenerateRootAuditFailed because the
// exit-code + recovery advice differ.
var ErrGenerateRootStatusAuditFailed = errors.New("openbao generate-root: post-emit audit trailer failed — token IS on stdout, revoke-status line was NOT written")

// ErrGenerateRootOutputFailed — token write to stdout failed.
var ErrGenerateRootOutputFailed = errors.New("openbao generate-root: stdout write failed — the token may have been emitted partially")

// --- Engine ---

// GenerateRoot decrypts the 3-of-5 threshold, runs OpenBao's
// generate-root ceremony, emits the token to Out, and audits the
// operation to Audit. When RevokeImmediately is true, revokes the
// token before returning. Every share slice and the local token
// copy are scrubbed on all return paths.
func GenerateRoot(ctx context.Context, opts GenerateRootOptions) error {
	if err := validateGenerateRootOptions(opts); err != nil {
		return err
	}
	if !opts.Consent {
		return ErrGenerateRootConsentRequired
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	audit := opts.Audit
	if audit == nil {
		audit = io.Discard
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	operator := opts.OperatorName
	if operator == "" {
		operator = "<unknown>"
	}

	// (1) Decrypt secrets.enc.yaml + extract the 3-of-5 threshold.
	// Reuses the unseal engine's extractor — same narrow-window
	// plaintext discipline (only 3 shares in memory at any moment),
	// which is the right posture even in reveal-adjacent code paths.
	secretsPath := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName, "secrets.enc.yaml")
	decrypted, err := opts.SOPS.Decrypt(ctx, secretsPath)
	if err != nil {
		return fmt.Errorf("openbao generate-root: decrypt %s: %w", secretsPath, err)
	}
	defer func() {
		for i := range decrypted {
			decrypted[i] = 0
		}
	}()

	shares, err := extractThresholdShares(decrypted)
	if err != nil {
		return err
	}
	defer func() {
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
	}()

	// (2) Run the ceremony against live OpenBao pods. This is the
	// step that distinguishes generate-root from reveal-shares —
	// generate-root NEEDS a reachable cluster; the token doesn't
	// exist until the pod computes it.
	token, err := opts.OpenBao.GenerateRoot(ctx, shares)
	if err != nil {
		return fmt.Errorf("openbao generate-root: ceremony: %w", err)
	}
	// Scrub the local token buffer on every return path. The
	// operator retains the token via the stdout write; our copy
	// dies here.
	defer func() {
		for i := range token {
			token[i] = 0
		}
	}()

	// (3) Audit line FIRST (fail-closed if audit write errors — same
	// invariant as reveal-shares).
	stamp := now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(audit,
		"[openbao] GENERATE-ROOT: single-use root token generated for cluster %s at %s by operator %s\n",
		opts.ClusterName, stamp, operator); err != nil {
		return fmt.Errorf("%w: %v", ErrGenerateRootAuditFailed, err)
	}

	// (4) Token on stdout as a single "TOKEN: <value>" line — same
	// shape as reveal's per-share lines so operators can grep the
	// same way.
	if _, err := fmt.Fprintf(out, "OPENBAO_ROOT_TOKEN: %s\n", token); err != nil {
		return fmt.Errorf("%w: %v", ErrGenerateRootOutputFailed, err)
	}

	// (5) Optional immediate revoke — health-check mode. Once we're
	// past step 4 the token is on stdout; every branch below is
	// POST-emit and any audit-write failure maps to
	// ErrGenerateRootStatusAuditFailed (NOT ErrGenerateRootAuditFailed)
	// so the sentinel doesn't lie about the token's state on disk.
	if opts.RevokeImmediately {
		if err := opts.OpenBao.RevokeSelf(ctx, token); err != nil {
			// Revoke failed. The token IS out on stdout; the
			// operator MUST see this so they can manually revoke.
			// Reviewer P1 fix: use `bao token revoke <token>` (the
			// explicit-argument form), NOT `bao token revoke -self`.
			// `-self` targets whatever token is in $VAULT_TOKEN in
			// the operator's shell — which is either unset (command
			// fails) or set to a DIFFERENT token (revokes the wrong
			// one). The explicit form takes the printed token
			// directly and is unambiguous.
			if _, werr := fmt.Fprintf(audit,
				"[openbao] WARNING: RevokeSelf failed (%v) — token may still be live; run: bao token revoke <OPENBAO_ROOT_TOKEN> (using the token printed on stdout above)\n",
				err); werr != nil {
				// Audit failed on top of revoke failure. Compose
				// BOTH sentinel-carrying errors via errors.Join so
				// sentinel-based recovery (errors.Is against either
				// the revoke error OR ErrGenerateRootStatusAuditFailed)
				// sees BOTH signals — reviewer P2: the prior single-
				// %w wrap lost the status-audit sentinel in exactly
				// this "token is on stdout, trailer was not written"
				// case. Revoke error is listed first so the operator
				// sees it as the primary signal (more actionable).
				return errors.Join(
					fmt.Errorf("openbao generate-root: revoke: %w", err),
					fmt.Errorf("%w: warning banner: %v", ErrGenerateRootStatusAuditFailed, werr),
				)
			}
			return fmt.Errorf("openbao generate-root: revoke: %w", err)
		}
		if _, err := fmt.Fprintln(audit, "[openbao] token revoked (--revoke-immediately)"); err != nil {
			return fmt.Errorf("%w: %v", ErrGenerateRootStatusAuditFailed, err)
		}
	} else {
		if _, err := fmt.Fprintln(audit, "[openbao] token is LIVE — revoke when done: bao token revoke <token> (or re-run with --revoke-immediately)"); err != nil {
			return fmt.Errorf("%w: %v", ErrGenerateRootStatusAuditFailed, err)
		}
	}

	return nil
}

func validateGenerateRootOptions(opts GenerateRootOptions) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("%w: ClusterName", ErrGenerateRootMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrGenerateRootMissingDependency)
	}
	if opts.SOPS == nil {
		return fmt.Errorf("%w: SOPS", ErrGenerateRootMissingDependency)
	}
	if opts.OpenBao == nil {
		return fmt.Errorf("%w: OpenBao", ErrGenerateRootMissingDependency)
	}
	return nil
}
