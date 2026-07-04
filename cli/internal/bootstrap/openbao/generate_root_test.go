package openbao

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeGRBao is a minimal ports.OpenBaoClient for the generate-root
// engine. Records GenerateRoot + RevokeSelf calls with the input
// share count / token bytes so tests can assert on the ceremony
// wiring without a live cluster.
type fakeGRBao struct {
	tokenToReturn  []byte
	generateErr    error
	revokeErr      error
	generateCalls  int
	generateShares int
	revokeCalls    int
	revokedTokens  [][]byte
}

func (f *fakeGRBao) GenerateRoot(_ context.Context, shares [][]byte) ([]byte, error) {
	f.generateCalls++
	f.generateShares = len(shares)
	if f.generateErr != nil {
		return nil, f.generateErr
	}
	out := make([]byte, len(f.tokenToReturn))
	copy(out, f.tokenToReturn)
	return out, nil
}

func (f *fakeGRBao) RevokeSelf(_ context.Context, token []byte) error {
	f.revokeCalls++
	// Defensive copy so the caller's post-revoke scrub doesn't zero
	// our record.
	cp := make([]byte, len(token))
	copy(cp, token)
	f.revokedTokens = append(f.revokedTokens, cp)
	return f.revokeErr
}

// Interface stubs — none of these should be called by GenerateRoot.
func (f *fakeGRBao) PodList(_ context.Context) ([]string, error)                     { return nil, nil }
func (f *fakeGRBao) Status(_ context.Context, _ string) (ports.BaoStatus, error)     { return ports.BaoStatus{}, nil }
func (f *fakeGRBao) Unseal(_ context.Context, _ string, _ []byte) error              { return nil }
func (f *fakeGRBao) RaftJoin(_ context.Context, _, _ string) error                   { return nil }
func (f *fakeGRBao) ApplyPolicy(_ context.Context, _ []byte, _, _ string) error       { return nil }
func (f *fakeGRBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error    { return nil }
func (f *fakeGRBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	return nil
}
func (f *fakeGRBao) WriteAuthRole(_ context.Context, _ []byte, _, _ string, _ map[string]string) error {
	return nil
}
func (f *fakeGRBao) GetAnnotation(_ context.Context, _, _ string) (string, error) { return "", nil }
func (f *fakeGRBao) SetAnnotation(_ context.Context, _, _, _ string) error        { return nil }
func (f *fakeGRBao) SetAnnotations(_ context.Context, _ string, _ map[string]string) error {
	return nil
}

// TestGenerateRoot_HappyPath_NoRevoke — default operator flow.
// Token lands on stdout with the OPENBAO_ROOT_TOKEN: prefix; audit
// includes the RFC3339 timestamp + operator; NO RevokeSelf call
// (operator opted out of auto-revoke by not passing the flag).
func TestGenerateRoot_HappyPath_NoRevoke(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.CAESIP-CANARY-TOKEN-VALUE-01234")}
	var stdout, stderr bytes.Buffer
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:  "eu/dc1",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:      bao,
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          &stdout,
		Audit:        &stderr,
	})
	if err != nil {
		t.Fatalf("happy path: unexpected error %v", err)
	}
	// Token line on stdout.
	if !strings.Contains(stdout.String(), "OPENBAO_ROOT_TOKEN: hvs.CAESIP-CANARY-TOKEN-VALUE-01234") {
		t.Errorf("token missing from stdout\nSTDOUT:\n%s", stdout.String())
	}
	// Audit + LIVE-token hint on stderr.
	for _, want := range []string{
		"GENERATE-ROOT:",
		"eu/dc1",
		"2026-07-03T15:04:05Z",
		"operator voa",
		"token is LIVE",
		"bao token revoke",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("audit/stderr missing %q\nSTDERR:\n%s", want, stderr.String())
		}
	}
	// Ceremony wiring — 3 shares handed to bao (threshold, not all 5).
	if bao.generateCalls != 1 {
		t.Errorf("GenerateRoot should be called once, got %d", bao.generateCalls)
	}
	if bao.generateShares != 3 {
		t.Errorf("expected 3 threshold shares to GenerateRoot, got %d", bao.generateShares)
	}
	// Critical: RevokeSelf MUST NOT be called on the default path.
	if bao.revokeCalls != 0 {
		t.Errorf("default path must not auto-revoke; got %d revoke calls", bao.revokeCalls)
	}
	// Token MUST NOT leak to stderr.
	if strings.Contains(stderr.String(), "CANARY") {
		t.Errorf("token leaked to stderr — audit-channel discipline broken\nSTDERR:\n%s", stderr.String())
	}
}

// TestGenerateRoot_HappyPath_RevokeImmediately — --revoke-immediately
// flag: token still lands on stdout (for the health-check smoke to
// grep exit-code = 0), then RevokeSelf runs BEFORE the engine
// returns. Audit's second line confirms the revoke.
func TestGenerateRoot_HappyPath_RevokeImmediately(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.HEALTHCHECK-TOKEN")}
	var stdout, stderr bytes.Buffer
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:       "eu/dc1",
		FleetRepo:         "/tmp/fake",
		SOPS:              newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:           bao,
		Consent:           true,
		OperatorName:      "voa",
		Now:               fixedClock(),
		RevokeImmediately: true,
		Out:               &stdout,
		Audit:             &stderr,
	})
	if err != nil {
		t.Fatalf("revoke-immediately: unexpected error %v", err)
	}
	if !strings.Contains(stdout.String(), "OPENBAO_ROOT_TOKEN: hvs.HEALTHCHECK-TOKEN") {
		t.Errorf("token missing from stdout\nSTDOUT:\n%s", stdout.String())
	}
	if bao.revokeCalls != 1 {
		t.Errorf("--revoke-immediately should revoke exactly once, got %d", bao.revokeCalls)
	}
	if len(bao.revokedTokens) != 1 || string(bao.revokedTokens[0]) != "hvs.HEALTHCHECK-TOKEN" {
		t.Errorf("wrong token revoked: %+v", bao.revokedTokens)
	}
	if !strings.Contains(stderr.String(), "token revoked") {
		t.Errorf("stderr should confirm revoke\nSTDERR:\n%s", stderr.String())
	}
	// LIVE hint MUST be absent when we revoke.
	if strings.Contains(stderr.String(), "token is LIVE") {
		t.Errorf("LIVE hint must NOT appear when auto-revoking\nSTDERR:\n%s", stderr.String())
	}
}

// TestGenerateRoot_NoConsent_Refuses — mirror of reveal-shares'
// consent gate. Engine MUST refuse before touching SOPS/OpenBao.
func TestGenerateRoot_NoConsent_Refuses(t *testing.T) {
	bao := &fakeGRBao{}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName: "eu/dc1",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:     bao,
	})
	if !errors.Is(err, ErrGenerateRootConsentRequired) {
		t.Errorf("want ErrGenerateRootConsentRequired, got %v", err)
	}
	// Consent gate MUST run before any ceremony.
	if bao.generateCalls != 0 {
		t.Errorf("ceremony ran despite consent refusal")
	}
}

// TestGenerateRoot_MissingShares_Refuses — 2 shares in
// secrets.enc.yaml (below threshold). extractThresholdShares refuses;
// engine surfaces ErrUnsealMissingShares (same sentinel unseal uses).
func TestGenerateRoot_MissingShares_Refuses(t *testing.T) {
	trimmed := strings.Replace(canonicalDecryptedForReveal,
		"    OPENBAO_UNSEAL_KEY_3: shareThrCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=\n",
		"", 1)
	bao := &fakeGRBao{}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName: "eu/dc1",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(trimmed),
		OpenBao:     bao,
		Consent:     true,
	})
	if !errors.Is(err, ErrUnsealMissingShares) {
		t.Errorf("want ErrUnsealMissingShares, got %v", err)
	}
	if bao.generateCalls != 0 {
		t.Errorf("ceremony ran despite missing shares")
	}
}

// TestGenerateRoot_CeremonyError_Propagates — the bao ceremony fails
// (e.g. pod unreachable, invalid shares). Engine surfaces the wrapped
// error and DOES NOT audit — no token was ever produced.
func TestGenerateRoot_CeremonyError_Propagates(t *testing.T) {
	bao := &fakeGRBao{generateErr: errors.New("pod not ready")}
	var stderr bytes.Buffer
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName: "eu/dc1",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:     bao,
		Consent:     true,
		Audit:       &stderr,
	})
	if err == nil {
		t.Fatal("expected propagated ceremony error, got nil")
	}
	if !strings.Contains(err.Error(), "pod not ready") {
		t.Errorf("expected 'pod not ready' in wrapped error, got %v", err)
	}
	// Audit line should NOT be written when no token exists — the
	// audit is about a REVEAL that happened, not an attempt that
	// failed. (Attempts fail through the error return.)
	if strings.Contains(stderr.String(), "GENERATE-ROOT:") {
		t.Errorf("audit should not fire when ceremony failed\nSTDERR:\n%s", stderr.String())
	}
}

// TestGenerateRoot_RevokeFailure_Surfaces — the token IS out
// (already written); revoke failure is a WARNING + returned error
// so the operator knows to revoke manually. Reviewer P1: the
// recipe MUST be `bao token revoke <token>` (explicit-argument
// form) not `bao token revoke -self`. `-self` targets whatever's
// in $VAULT_TOKEN — either unset (command fails) or set to a
// DIFFERENT token (revokes the wrong one). We assert the exact
// recipe so a future refactor can't silently drop back to the
// dangerous `-self` form.
func TestGenerateRoot_RevokeFailure_Surfaces(t *testing.T) {
	bao := &fakeGRBao{
		tokenToReturn: []byte("hvs.WOULD-REVOKE"),
		revokeErr:     errors.New("revoke 500 timeout"),
	}
	var stdout, stderr bytes.Buffer
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:       "eu/dc1",
		FleetRepo:         "/tmp/fake",
		SOPS:              newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:           bao,
		Consent:           true,
		RevokeImmediately: true,
		Out:               &stdout,
		Audit:             &stderr,
	})
	if err == nil {
		t.Fatal("expected revoke error, got nil")
	}
	if !strings.Contains(err.Error(), "revoke 500 timeout") {
		t.Errorf("expected propagated revoke error, got %v", err)
	}
	// Token WAS emitted (contract: the write already happened).
	if !strings.Contains(stdout.String(), "OPENBAO_ROOT_TOKEN: hvs.WOULD-REVOKE") {
		t.Errorf("token should still be on stdout even when revoke fails\nSTDOUT:\n%s", stdout.String())
	}
	// Warning on stderr names the manual-revoke recipe.
	if !strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("stderr should carry a WARNING banner\nSTDERR:\n%s", stderr.String())
	}
	// P1 recipe regression guard: the exact command form.
	if !strings.Contains(stderr.String(), "bao token revoke <OPENBAO_ROOT_TOKEN>") {
		t.Errorf("stderr must use explicit-argument revoke form 'bao token revoke <OPENBAO_ROOT_TOKEN>' (not the dangerous -self form)\nSTDERR:\n%s", stderr.String())
	}
	// Explicit anti-regression: -self must NOT appear as a suggestion.
	// Preceded/followed by whitespace so we don't false-positive on
	// substrings like "yourself" in a future refactor.
	if strings.Contains(stderr.String(), "bao token revoke -self") {
		t.Errorf("stderr suggested the dangerous `bao token revoke -self` form — see M5-T06 P1 review\nSTDERR:\n%s", stderr.String())
	}
}

// TestGenerateRoot_PostEmitAuditFail_UsesStatusSentinel — reviewer's
// P2 (audit-fail granularity): the token IS on stdout, then the
// audit trailer write fails. Engine MUST surface
// ErrGenerateRootStatusAuditFailed (NOT ErrGenerateRootAuditFailed,
// whose sentinel promise is "no token was emitted"). Prevents an
// operator from misreading the error and assuming the token is
// still secret.
func TestGenerateRoot_PostEmitAuditFail_UsesStatusSentinel(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.POST-EMIT-CANARY")}
	var stdout bytes.Buffer
	// Let the pre-emit audit through (call 1), fail on the trailer
	// (call 2 — the LIVE-token hint line).
	audit := &failingWriter{successBeforeErr: 1, err: errors.New("audit trailer closed")}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:  "eu/dc1",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:      bao,
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          &stdout,
		Audit:        audit,
	})
	if err == nil {
		t.Fatal("expected post-emit audit failure, got nil")
	}
	// The critical distinction: this MUST be StatusAuditFailed, not
	// AuditFailed — the two sentinels have opposite guarantees about
	// stdout state.
	if !errors.Is(err, ErrGenerateRootStatusAuditFailed) {
		t.Errorf("want ErrGenerateRootStatusAuditFailed (post-emit), got %v", err)
	}
	if errors.Is(err, ErrGenerateRootAuditFailed) {
		t.Errorf("MUST NOT return pre-emit ErrGenerateRootAuditFailed after stdout has been written — sentinel semantics broken")
	}
	// Token IS on stdout — the pre-emit audit succeeded so we
	// proceeded to write it.
	if !strings.Contains(stdout.String(), "OPENBAO_ROOT_TOKEN: hvs.POST-EMIT-CANARY") {
		t.Errorf("token should be on stdout when only the trailer failed\nSTDOUT:\n%s", stdout.String())
	}
	// The pre-emit audit line DID land (call 1 succeeded).
	if !strings.Contains(audit.Buffer.String(), "GENERATE-ROOT:") {
		t.Errorf("pre-emit audit line should have landed on the audit buffer\nAUDIT:\n%s", audit.Buffer.String())
	}
}

// TestGenerateRoot_RevokeSuccessAuditFail_UsesStatusSentinel —
// same shape as above but on the --revoke-immediately trailer.
func TestGenerateRoot_RevokeSuccessAuditFail_UsesStatusSentinel(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.HEALTHCHECK-POST")}
	var stdout bytes.Buffer
	// Let the pre-emit audit through (call 1), fail on the "token
	// revoked" trailer (call 2).
	audit := &failingWriter{successBeforeErr: 1, err: errors.New("stderr closed after emit")}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:       "eu/dc1",
		FleetRepo:         "/tmp/fake",
		SOPS:              newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:           bao,
		Consent:           true,
		RevokeImmediately: true,
		Out:               &stdout,
		Audit:             audit,
	})
	if err == nil {
		t.Fatal("expected post-emit audit failure, got nil")
	}
	if !errors.Is(err, ErrGenerateRootStatusAuditFailed) {
		t.Errorf("want ErrGenerateRootStatusAuditFailed, got %v", err)
	}
	// RevokeSelf DID run before the audit trailer failed.
	if bao.revokeCalls != 1 {
		t.Errorf("RevokeSelf should have run once; got %d calls", bao.revokeCalls)
	}
	if !strings.Contains(stdout.String(), "hvs.HEALTHCHECK-POST") {
		t.Errorf("token should still be on stdout\nSTDOUT:\n%s", stdout.String())
	}
}

// TestGenerateRoot_RevokeFail_AuditFail_ComposesErrors — the worst
// case: --revoke-immediately fails AND the audit warning about it
// also fails. Engine wraps both errors so the operator sees both
// problems; the revoke error stays as the primary wrap target
// because it's the more actionable signal (they need to revoke).
func TestGenerateRoot_RevokeFail_AuditFail_ComposesErrors(t *testing.T) {
	bao := &fakeGRBao{
		tokenToReturn: []byte("hvs.BOTH-BROKEN"),
		revokeErr:     errors.New("revoke exec timeout"),
	}
	var stdout bytes.Buffer
	// Let the pre-emit audit through (call 1), fail on the WARNING
	// write that follows revoke failure (call 2).
	audit := &failingWriter{successBeforeErr: 1, err: errors.New("warning banner blocked")}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:       "eu/dc1",
		FleetRepo:         "/tmp/fake",
		SOPS:              newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:           bao,
		Consent:           true,
		RevokeImmediately: true,
		Out:               &stdout,
		Audit:             audit,
	})
	if err == nil {
		t.Fatal("expected wrapped error, got nil")
	}
	// Primary wrap target: the revoke error (more actionable).
	if !strings.Contains(err.Error(), "revoke exec timeout") {
		t.Errorf("expected revoke error as primary, got %v", err)
	}
	// Audit failure surfaced in the wrapped message so the operator
	// isn't blind to it.
	if !strings.Contains(err.Error(), "warning banner blocked") {
		t.Errorf("expected audit error surfaced in composed error, got %v", err)
	}
	// Reviewer P2: BOTH sentinels must survive errors.Is checks.
	// The prior single-%w wrap lost the status-audit sentinel in
	// this "token is on stdout, warning banner was not written"
	// case; recovery code doing `errors.Is(err,
	// ErrGenerateRootStatusAuditFailed)` returned false and the
	// operator couldn't dispatch on it.
	if !errors.Is(err, ErrGenerateRootStatusAuditFailed) {
		t.Errorf("composed error must carry ErrGenerateRootStatusAuditFailed sentinel (token is on stdout, trailer failed) — got %v", err)
	}
	// The revoke error itself doesn't have a public sentinel; assert
	// via .Error() substring instead (already covered above).
}

// TestGenerateRoot_AuditWriteFails_RefusesToEmitToken — same
// fail-closed invariant as reveal-shares' P2 fix. If audit is
// broken, we do NOT emit the token — an unaudited root-token
// generation defeats the whole audit channel's purpose.
func TestGenerateRoot_AuditWriteFails_RefusesToEmitToken(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.SHOULD-NOT-APPEAR")}
	var stdout bytes.Buffer
	audit := &failingWriter{successBeforeErr: 0, err: errors.New("stderr closed")}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName:  "eu/dc1",
		FleetRepo:    "/tmp/fake",
		SOPS:         newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:      bao,
		Consent:      true,
		OperatorName: "voa",
		Now:          fixedClock(),
		Out:          &stdout,
		Audit:        audit,
	})
	if !errors.Is(err, ErrGenerateRootAuditFailed) {
		t.Errorf("want ErrGenerateRootAuditFailed, got %v", err)
	}
	// The critical invariant: NO token on stdout when audit fails.
	if stdout.Len() > 0 {
		t.Errorf("audit failure must prevent token emit; stdout=%q", stdout.String())
	}
	if strings.Contains(stdout.String(), "SHOULD-NOT-APPEAR") {
		t.Errorf("token leaked to stdout despite audit failure\nSTDOUT:\n%s", stdout.String())
	}
}

// TestGenerateRoot_OutputWriteFails_ReturnsStructuralError — mid-emit
// stdout failure surfaces ErrGenerateRootOutputFailed and the audit
// line already landed.
func TestGenerateRoot_OutputWriteFails_ReturnsStructuralError(t *testing.T) {
	bao := &fakeGRBao{tokenToReturn: []byte("hvs.WOULD-FAIL-WRITE")}
	var audit bytes.Buffer
	out := &failingWriter{successBeforeErr: 0, err: errors.New("broken pipe")}
	err := GenerateRoot(context.Background(), GenerateRootOptions{
		ClusterName: "eu/dc1",
		FleetRepo:   "/tmp/fake",
		SOPS:        newFakeRevealSOPS(canonicalDecryptedForReveal),
		OpenBao:     bao,
		Consent:     true,
		Out:         out,
		Audit:       &audit,
	})
	if !errors.Is(err, ErrGenerateRootOutputFailed) {
		t.Errorf("want ErrGenerateRootOutputFailed, got %v", err)
	}
	if !strings.Contains(audit.String(), "GENERATE-ROOT:") {
		t.Errorf("audit should land BEFORE stdout write; audit=%q", audit.String())
	}
}

// TestGenerateRoot_MissingDependency_Refuses — nil SOPS / OpenBao /
// empty FleetRepo / empty ClusterName all map to
// ErrGenerateRootMissingDependency.
func TestGenerateRoot_MissingDependency_Refuses(t *testing.T) {
	baoOK := &fakeGRBao{}
	sopsOK := newFakeRevealSOPS(canonicalDecryptedForReveal)
	cases := []struct {
		name string
		opts GenerateRootOptions
	}{
		{"nil-sops", GenerateRootOptions{ClusterName: "c", FleetRepo: "/f", OpenBao: baoOK}},
		{"nil-openbao", GenerateRootOptions{ClusterName: "c", FleetRepo: "/f", SOPS: sopsOK}},
		{"empty-fleet", GenerateRootOptions{ClusterName: "c", SOPS: sopsOK, OpenBao: baoOK}},
		{"empty-cluster", GenerateRootOptions{FleetRepo: "/f", SOPS: sopsOK, OpenBao: baoOK}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := GenerateRoot(context.Background(), c.opts)
			if !errors.Is(err, ErrGenerateRootMissingDependency) {
				t.Errorf("want ErrGenerateRootMissingDependency, got %v", err)
			}
		})
	}
}
