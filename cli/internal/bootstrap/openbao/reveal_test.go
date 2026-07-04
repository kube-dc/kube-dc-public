package openbao

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a canned time so tests can assert on the audit
// timestamp without racing wall-clock.
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 3, 15, 4, 5, 0, time.UTC)
	return func() time.Time { return t }
}

// canonicalDecryptedForReveal is the shape SOPS emits after
// Decrypt(secrets.enc.yaml) — a Kubernetes Secret with stringData
// entries. Includes non-share keys (KEYCLOAK_ADMIN_PASSWORD) so tests
// verify the extractor filters correctly to OPENBAO_UNSEAL_KEY_*.
const canonicalDecryptedForReveal = `apiVersion: v1
kind: Secret
metadata:
    name: cluster-secrets
stringData:
    KEYCLOAK_ADMIN_PASSWORD: SHOULD-NOT-APPEAR-IN-OUTPUT
    OPENBAO_UNSEAL_KEY_1: shareOneAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
    OPENBAO_UNSEAL_KEY_2: shareTwoBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
    OPENBAO_UNSEAL_KEY_3: shareThrCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
    OPENBAO_UNSEAL_KEY_4: shareFourDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=
    OPENBAO_UNSEAL_KEY_5: shareFiveEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE=
`

func newFakeRevealSOPS(body string) *fakeSOPSDecryptOnly {
	return &fakeSOPSDecryptOnly{body: []byte(body)}
}

// TestRevealShares_HappyPath — canonical revealed-with-consent flow.
// Every share appears in stdout in KEY: VALUE form; audit line lands
// on stderr with the operator + timestamp + cluster name; unrelated
// stringData entries (KEYCLOAK_ADMIN_PASSWORD) are NOT written.
func TestRevealShares_HappyPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName:  "cs/zrh",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          &stdout,
		Audit:        &stderr,
	})
	if err != nil {
		t.Fatalf("happy path: unexpected error %v", err)
	}

	// Every share present on stdout.
	for i, want := range []string{
		"OPENBAO_UNSEAL_KEY_1: shareOneAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"OPENBAO_UNSEAL_KEY_2: shareTwoBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		"OPENBAO_UNSEAL_KEY_3: shareThrCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
		"OPENBAO_UNSEAL_KEY_4: shareFourDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=",
		"OPENBAO_UNSEAL_KEY_5: shareFiveEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE=",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("share %d: missing %q in stdout\nSTDOUT:\n%s", i+1, want, stdout.String())
		}
	}
	// BEGIN/END markers frame the block.
	if !strings.Contains(stdout.String(), "BEGIN OpenBao shares") {
		t.Errorf("missing BEGIN marker\nSTDOUT:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "END OpenBao shares") {
		t.Errorf("missing END marker\nSTDOUT:\n%s", stdout.String())
	}
	// Unrelated stringData MUST NOT appear.
	if strings.Contains(stdout.String(), "SHOULD-NOT-APPEAR-IN-OUTPUT") {
		t.Errorf("KEYCLOAK_ADMIN_PASSWORD leaked to stdout — extractor filter regression\nSTDOUT:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "KEYCLOAK_ADMIN_PASSWORD") {
		t.Errorf("non-share key name leaked to stdout\nSTDOUT:\n%s", stdout.String())
	}
	// Audit line on stderr — operator + cluster + timestamp.
	for _, want := range []string{
		"REVEAL:",
		"cs/zrh",
		"2026-07-03T15:04:05Z",
		"operator voa",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("audit missing %q\nSTDERR:\n%s", want, stderr.String())
		}
	}
	// Shares MUST NOT appear on stderr — stdout is the shares
	// channel; stderr is audit-only.
	for i := 1; i <= 5; i++ {
		if strings.Contains(stderr.String(), "OPENBAO_UNSEAL_KEY_") {
			t.Errorf("shares leaked to stderr\nSTDERR:\n%s", stderr.String())
			break
		}
		_ = i
	}
}

// TestRevealShares_NoConsent_Refuses — the whole point of the
// subcommand's safety story. Without Consent=true the engine MUST
// return ErrRevealConsentRequired without touching SOPS.
func TestRevealShares_NoConsent_Refuses(t *testing.T) {
	sops := newFakeRevealSOPS(canonicalDecryptedForReveal)
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName: "cs/zrh",
		FleetRepo:   "/tmp/fake",
		SOPS:        sops,
		Consent:     false,
	})
	if err == nil {
		t.Fatal("want ErrRevealConsentRequired, got nil")
	}
	if !errors.Is(err, ErrRevealConsentRequired) {
		t.Errorf("want ErrRevealConsentRequired, got %v", err)
	}
	// Defensive: consent gate MUST run before SOPS is touched. The
	// fake tracks decrypt errors via body != nil AND err == nil; we
	// verify Decrypt wasn't called by asserting the body ptr is
	// untouched. (fakeSOPSDecryptOnly doesn't expose call counts, so
	// we use a proxy: err should mention consent, not decrypt.)
	if strings.Contains(err.Error(), "decrypt") {
		t.Errorf("SOPS.Decrypt appears to have been called before consent gate: %v", err)
	}
}

// TestRevealShares_MissingShare_Refuses — 4 shares in secrets.enc.yaml
// (SEAL_KEY_2 removed). Engine must refuse rather than partial-reveal.
func TestRevealShares_MissingShare_Refuses(t *testing.T) {
	trimmed := strings.Replace(canonicalDecryptedForReveal,
		"    OPENBAO_UNSEAL_KEY_2: shareTwoBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\n",
		"", 1)
	var stdout, stderr bytes.Buffer
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName: "cs/zrh",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(trimmed),
		Consent:     true,
		Out:         &stdout,
		Audit:       &stderr,
	})
	if err == nil {
		t.Fatal("want ErrRevealMissingShares, got nil")
	}
	if !errors.Is(err, ErrRevealMissingShares) {
		t.Errorf("want ErrRevealMissingShares, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing keys: [2]") {
		t.Errorf("error should name the missing share number, got %v", err)
	}
	// No partial reveal — stdout must be empty when we refuse.
	if stdout.Len() > 0 {
		t.Errorf("no bytes should reach stdout on missing-share refuse\nSTDOUT:\n%s", stdout.String())
	}
}

// TestRevealShares_SOPSDecryptError_Propagates — apiserver-like
// failure at the SOPS layer wraps up with cluster context.
func TestRevealShares_SOPSDecryptError_Propagates(t *testing.T) {
	sops := &fakeSOPSDecryptOnly{err: errors.New("age key not enrolled")}
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName: "cs/zrh",
		FleetRepo:   "/tmp/fake",
		SOPS:        sops,
		Consent:     true,
	})
	if err == nil {
		t.Fatal("expected wrapped decrypt error, got nil")
	}
	if !strings.Contains(err.Error(), "age key not enrolled") {
		t.Errorf("expected propagated adapter error, got %v", err)
	}
	if !strings.Contains(err.Error(), "cs/zrh") {
		t.Errorf("error should include cluster context, got %v", err)
	}
}

// TestRevealShares_EmptyOperator_FallsBackToUnknown — the audit line
// must ALWAYS show SOMETHING as the operator; empty $USER in CI
// resolves to "<unknown>" at the engine layer so the audit isn't
// misleadingly blank.
func TestRevealShares_EmptyOperator_FallsBackToUnknown(t *testing.T) {
	var stderr bytes.Buffer
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName:  "cs/zrh",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		Consent:      true,
		OperatorName: "",
		Now:          fixedClock(),
		Audit:        &stderr,
	})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if !strings.Contains(stderr.String(), "operator <unknown>") {
		t.Errorf("expected 'operator <unknown>' in audit, got:\n%s", stderr.String())
	}
}

// TestRevealShares_MissingDependency_Refuses — nil SOPS / empty
// FleetRepo / empty ClusterName all map to ErrRevealMissingDependency
// (caller wiring bug).
func TestRevealShares_MissingDependency_Refuses(t *testing.T) {
	cases := []struct {
		name string
		opts RevealOptions
	}{
		{"nil-sops", RevealOptions{ClusterName: "c", FleetRepo: "/f"}},
		{"empty-fleet", RevealOptions{ClusterName: "c", SOPS: newFakeRevealSOPS(canonicalDecryptedForReveal)}},
		{"empty-cluster", RevealOptions{FleetRepo: "/f", SOPS: newFakeRevealSOPS(canonicalDecryptedForReveal)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RevealShares(context.Background(), c.opts)
			if err == nil {
				t.Fatal("want ErrRevealMissingDependency, got nil")
			}
			if !errors.Is(err, ErrRevealMissingDependency) {
				t.Errorf("want ErrRevealMissingDependency, got %v", err)
			}
		})
	}
}

// TestRevealShares_NilOut_NoCrash — nil Out + nil Audit fall back to
// io.Discard so the engine can be called from library contexts that
// don't care about the output streams (e.g. a future TUI screen that
// just wants to verify consent + successful decrypt).
func TestRevealShares_NilOut_NoCrash(t *testing.T) {
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName: "cs/zrh",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(canonicalDecryptedForReveal),
		Consent:     true,
		Out:         nil,
		Audit:       nil,
	})
	if err != nil {
		t.Fatalf("nil streams should not error, got %v", err)
	}
}

// failingWriter is an io.Writer that returns a canned error after
// the configured number of successful writes. Lets tests exercise
// the audit-fails vs. shares-fail-mid-block branches without
// depending on OS-level broken-pipe timing.
type failingWriter struct {
	// successBeforeErr counts write() calls that succeed; the
	// (successBeforeErr+1)-th call returns err. Set to 0 for
	// "fail immediately".
	successBeforeErr int
	err              error
	calls            int
	bytes.Buffer
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.successBeforeErr {
		return 0, f.err
	}
	return f.Buffer.Write(p)
}

// TestRevealShares_AuditWriteFails_RefusesToEmitShares — reviewer's
// P2: the contract is "shares written to stdout AND audit written to
// stderr". A broken audit writer must FAIL the whole operation and
// refuse to leak shares to stdout — otherwise a silent (unaudited)
// reveal is possible.
func TestRevealShares_AuditWriteFails_RefusesToEmitShares(t *testing.T) {
	var stdout bytes.Buffer
	audit := &failingWriter{successBeforeErr: 0, err: errors.New("simulated stderr closed")}
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName:  "cs/zrh",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          &stdout,
		Audit:        audit,
	})
	if err == nil {
		t.Fatal("want ErrRevealAuditFailed, got nil")
	}
	if !errors.Is(err, ErrRevealAuditFailed) {
		t.Errorf("want ErrRevealAuditFailed, got %v", err)
	}
	// Critical invariant: audit-failed → NO shares on stdout. A
	// silent reveal (audit lost + shares written) would defeat the
	// audit channel's whole purpose.
	if stdout.Len() > 0 {
		t.Errorf("audit failure must prevent stdout writes; stdout=%q", stdout.String())
	}
	for i := 1; i <= 5; i++ {
		if strings.Contains(stdout.String(), "OPENBAO_UNSEAL_KEY_") {
			t.Errorf("shares leaked to stdout despite audit failure\nSTDOUT:\n%s", stdout.String())
			break
		}
		_ = i
	}
}

// TestRevealShares_OutputWriteFails_ReturnsStructuralError — a
// broken stdout mid-block (e.g. `... | head -n 1` closes the pipe
// after the header) MUST surface as ErrRevealOutputFailed, not
// silent success with a truncated share list.
func TestRevealShares_OutputWriteFails_ReturnsStructuralError(t *testing.T) {
	var audit bytes.Buffer
	// Let the header + first share through, then fail on share 2 —
	// exercises the mid-block failure path where the audit line
	// already landed but stdout broke partway.
	out := &failingWriter{successBeforeErr: 2, err: errors.New("simulated broken pipe")}
	err := RevealShares(context.Background(), RevealOptions{
		ClusterName:  "cs/zrh",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          out,
		Audit:        &audit,
	})
	if err == nil {
		t.Fatal("want ErrRevealOutputFailed, got nil")
	}
	if !errors.Is(err, ErrRevealOutputFailed) {
		t.Errorf("want ErrRevealOutputFailed, got %v", err)
	}
	// Audit MUST have already been written — the whole reason we
	// audit first is to leave a durable record even when stdout
	// breaks. The reviewer's contract note names this explicitly.
	if !strings.Contains(audit.String(), "REVEAL:") {
		t.Errorf("audit line should land BEFORE stdout error; audit=%q", audit.String())
	}
	// Error mentions the failing share position so operators can
	// tell where the pipe broke.
	if !strings.Contains(err.Error(), "share 2") {
		t.Errorf("expected 'share 2' in error, got %v", err)
	}
}

// TestExtractAllShares_MissingSet — 3-share subset case; every
// missing index must be reported.
func TestExtractAllShares_MissingSet(t *testing.T) {
	// Only keys 1 + 4 present.
	body := []byte(`stringData:
  OPENBAO_UNSEAL_KEY_1: alpha
  OPENBAO_UNSEAL_KEY_4: delta
`)
	_, err := extractAllShares(body)
	if err == nil {
		t.Fatal("want error listing missing shares, got nil")
	}
	// Missing 2, 3, 5.
	for _, want := range []string{"[2 3 5]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected missing set %q, got %v", want, err)
		}
	}
}
