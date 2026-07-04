package openbao

// M5-T05 — `kube-dc bootstrap openbao reveal-shares`.
//
// Emergency-only subcommand that decrypts every Shamir share from a
// cluster's secrets.enc.yaml and prints them to stdout. The intended
// operator flow is:
//
//   - Break-glass moment (e.g. share-holder unreachable + need to
//     unseal OR need shares to run generate-root outside the CLI).
//   - Operator explicitly opts in via `--i-understand-the-risk` or
//     `REVEAL=true` env-var — the double gate is deliberate friction.
//   - Shares stream to stdout so `> shares.txt` cleanly captures
//     them without the audit log interleaved.
//   - Audit line goes to stderr with $USER + timestamp + cluster.
//     Even if the operator redirects stdout, the audit is
//     terminal-visible.
//   - Buffer scrubbed on every return path via defer.
//
// Reveals ALL FIVE shares (not just the 3-of-5 threshold that
// `unseal` uses). The whole point of this subcommand is worst-case
// recovery — if a share holder is unreachable, the operator may
// need any of the 5 to reconstruct threshold access via a different
// share-holder subset.
//
// Root token is NOT revealed here — the root token was scrubbed
// after init's Phase C annotate. Operators who need a fresh root
// token use `openbao generate-root` (M5-T06), which runs the
// generate-root ceremony against the current OpenBao pods with the
// existing shares.
//
// Never runs against the apiserver. Never mutates cluster state.
// The only I/O is SOPS.Decrypt on a local file + writes to Out/Err.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"
)

// RevealOptions is the parameter bundle for RevealShares.
type RevealOptions struct {
	ClusterName string
	FleetRepo   string
	SOPS        SOPSDecrypter
	// Consent is true when the operator has explicitly opted in via
	// --i-understand-the-risk OR REVEAL=true env-var. The engine
	// refuses to proceed without it — the cobra layer is where the
	// two gates are OR'd; the engine sees the resolved boolean.
	Consent bool
	// OperatorName is the actor's identity used in the audit line.
	// Empty falls back to "<unknown>" so the audit line still shows
	// SOMETHING even in non-shell contexts (CI where $USER is unset).
	OperatorName string
	// Now is a test seam for the audit timestamp. Nil defaults to
	// time.Now(); tests pin it for deterministic stderr assertions.
	Now func() time.Time
	// Out receives the share block (stdout). Nil is safe (io.Discard).
	Out io.Writer
	// Audit receives the audit line (stderr). Nil is safe (io.Discard).
	Audit io.Writer
}

// SOPSDecrypter is the minimal SOPS surface RevealShares needs. Kept
// separate from ports.SOPSClient so tests can wire a 5-line fake
// instead of the full client surface — mirrors NodeLabelsProvider's
// pattern in M6-T05.
type SOPSDecrypter interface {
	Decrypt(ctx context.Context, path string) ([]byte, error)
}

// --- Errors ---

// ErrRevealConsentRequired is returned when the operator did not
// opt in via --i-understand-the-risk OR REVEAL=true. Deliberately
// distinct from a "missing shares" error so the cobra layer can
// map it to a specific exit code + remediation message.
var ErrRevealConsentRequired = errors.New("openbao reveal-shares: consent required (pass --i-understand-the-risk or set REVEAL=true)")

// ErrRevealMissingDependency surfaces on nil SOPS / empty
// FleetRepo / empty ClusterName. Caller wiring bug.
var ErrRevealMissingDependency = errors.New("openbao reveal-shares: missing required dependency")

// ErrRevealMissingShares fires when secrets.enc.yaml decrypted OK
// but doesn't carry all 5 OPENBAO_UNSEAL_KEY_{1..5} entries. Either
// the cluster wasn't put through `bootstrap openbao init` or the
// encrypted file is corrupt.
var ErrRevealMissingShares = errors.New("openbao reveal-shares: secrets.enc.yaml is missing one or more OPENBAO_UNSEAL_KEY_{1..5} entries — run `bootstrap openbao init` first")

// ErrRevealAuditFailed surfaces when writing the audit line to the
// Audit writer errors. Reveal REFUSES to emit shares in this state —
// the contract is "shares written + audit written" as an atomic
// pair, and a silent reveal (audit lost, shares still on stdout)
// would violate the whole purpose of the audit channel.
var ErrRevealAuditFailed = errors.New("openbao reveal-shares: audit write failed — refusing to emit shares without a durable audit record")

// ErrRevealOutputFailed surfaces when a share write to stdout errors
// mid-block (broken pipe, closed file). Distinct from audit failure
// so operators reading the exit + tag can tell which channel broke.
var ErrRevealOutputFailed = errors.New("openbao reveal-shares: stdout write failed — partial reveal (audit line already written)")

// --- Engine ---

// RevealShares decrypts every OPENBAO_UNSEAL_KEY_{1..5} entry from
// clusters/<name>/secrets.enc.yaml and writes them to Out (stdout in
// the cobra layer). Writes a single-line audit record to Audit
// (stderr in the cobra layer). Refuses without Consent.
//
// Every share slice is scrubbed on return (success + error paths
// alike). The decrypted secrets.enc.yaml blob is scrubbed inline
// after the regex extract.
func RevealShares(ctx context.Context, opts RevealOptions) error {
	if err := validateRevealOptions(opts); err != nil {
		return err
	}
	if !opts.Consent {
		return ErrRevealConsentRequired
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

	// (1) Decrypt secrets.enc.yaml in memory.
	secretsPath := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName, "secrets.enc.yaml")
	decrypted, err := opts.SOPS.Decrypt(ctx, secretsPath)
	if err != nil {
		return fmt.Errorf("openbao reveal-shares: decrypt %s: %w", secretsPath, err)
	}
	defer func() {
		for i := range decrypted {
			decrypted[i] = 0
		}
	}()

	// (2) Extract all 5 shares. Distinct from unseal's threshold-3
	// path — for recovery we hand out the whole set.
	shares, err := extractAllShares(decrypted)
	if err != nil {
		return err
	}
	// Scrub every share on return regardless of the write outcome.
	defer func() {
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
	}()

	// (3) Audit line to stderr FIRST. Reviewer's P2 correctness
	// tightening: the contract is exit-0 iff (audit written AND
	// shares written); dropping the audit write's error would let a
	// broken audit writer + successful stdout produce a silent
	// reveal, which defeats the whole audit channel. So we check
	// the write here and REFUSE to touch stdout if audit failed —
	// no shares emitted without a durable audit record.
	stamp := now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(audit,
		"[openbao] REVEAL: 5 shares decrypted for cluster %s at %s by operator %s\n",
		opts.ClusterName, stamp, operator); err != nil {
		return fmt.Errorf("%w: %v", ErrRevealAuditFailed, err)
	}

	// (4) Share block to stdout. Header + KEY: VALUE lines + footer.
	// Header/footer bracket the shares so an operator eyeballing the
	// output sees where the sensitive block starts and ends. Every
	// write is error-checked — a mid-block broken pipe (redirected
	// to a full disk, closed reader) surfaces as a structural
	// failure rather than silently returning success with a partial
	// share block.
	if _, err := fmt.Fprintln(out, "# BEGIN OpenBao shares — 3 of 5 required to unseal or generate-root"); err != nil {
		return fmt.Errorf("%w: header: %v", ErrRevealOutputFailed, err)
	}
	for i, share := range shares {
		if _, err := fmt.Fprintf(out, "OPENBAO_UNSEAL_KEY_%d: %s\n", i+1, share); err != nil {
			return fmt.Errorf("%w: share %d: %v", ErrRevealOutputFailed, i+1, err)
		}
	}
	if _, err := fmt.Fprintln(out, "# END OpenBao shares — scrub your terminal (clear + scrollback) after use"); err != nil {
		return fmt.Errorf("%w: footer: %v", ErrRevealOutputFailed, err)
	}

	return nil
}

func validateRevealOptions(opts RevealOptions) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("%w: ClusterName", ErrRevealMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrRevealMissingDependency)
	}
	if opts.SOPS == nil {
		return fmt.Errorf("%w: SOPS", ErrRevealMissingDependency)
	}
	return nil
}

// allSharesRegex mirrors shareValueRegex from unseal.go but captures
// keys 1-5 rather than 1-3. Kept as a separate regex + extractor so
// the unseal path's narrow-window discipline (only 3 shares in
// memory at any moment) isn't compromised by refactoring reveal
// into it.
var allSharesRegex = regexp.MustCompile(`(?m)^\s*OPENBAO_UNSEAL_KEY_(\d+):\s*([A-Za-z0-9+/=_-]+)\s*$`)

// extractAllShares pulls OPENBAO_UNSEAL_KEY_{1..5} out of the
// decrypted YAML. Returns 5 freshly-allocated byte slices the caller
// scrubs. Missing shares fail loudly rather than partial reveal —
// operators asking to see the whole set expect the whole set.
func extractAllShares(decrypted []byte) ([][]byte, error) {
	matches := allSharesRegex.FindAllSubmatchIndex(decrypted, -1)
	found := make(map[int][]byte, 5)
	for _, m := range matches {
		numBytes := decrypted[m[2]:m[3]]
		valBytes := decrypted[m[4]:m[5]]
		n := 0
		for _, c := range numBytes {
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 5 {
			continue
		}
		dup := make([]byte, len(valBytes))
		copy(dup, valBytes)
		found[n] = dup
	}

	shares := make([][]byte, 0, 5)
	missing := []int{}
	for i := 1; i <= 5; i++ {
		v, ok := found[i]
		if !ok || len(v) == 0 {
			missing = append(missing, i)
			continue
		}
		shares = append(shares, v)
	}
	if len(missing) > 0 {
		// Best-effort scrub of any shares we managed to collect
		// before returning the error.
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
		return nil, fmt.Errorf("%w (missing keys: %v)", ErrRevealMissingShares, missing)
	}
	return shares, nil
}
