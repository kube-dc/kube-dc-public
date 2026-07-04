package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeDNS is a hermetic ports.DNSClient. Tests wire the answers map
// directly against the probe's expected query names (WildcardDNSProbe
// uses `kdc-dns-test-<subLabel>.<domain>`, ExplicitFQDNDNSProbe uses
// `<sub>.<domain>` for kube-api/console/login/grafana/s3). SubLabel
// is pinned via DNSGateOptions.SubLabel so the wildcard query is
// deterministic.
type fakeDNS struct {
	answers map[string][]string
	err     error
	calls   int
}

func (f *fakeDNS) Resolve(_ context.Context, name, recordType string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if recordType != ports.DNSRecordTypeA {
		return nil, errors.New("fakeDNS: only A supported")
	}
	return f.answers[name], nil
}

// Reusable inputs — 42 characters keeps output columns aligned in
// eyeball inspection; nothing magical about the domain.
const (
	testDomain = "acme.example.com"
	testNodeIP = "213.111.154.233"
	testSub    = "abcd1234"
	testWildQ  = "kdc-dns-test-abcd1234.acme.example.com"
)

// TestCheckDNSReady_WildcardMatches — canonical happy path. Random
// sub-label pinned via SubLabel; the fake resolver answers the
// wildcard query with the expected node IP; gate returns nil + the
// operator log shows PASS.
func TestCheckDNSReady_WildcardMatches(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{
		testWildQ: {testNodeIP},
	}}
	var out bytes.Buffer
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:         testDomain,
		NodeExternalIP: testNodeIP,
		DNS:            dns,
		Out:            &out,
		SubLabel:       testSub,
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !strings.Contains(out.String(), "DNS gate: PASS (wildcard)") {
		t.Errorf("expected wildcard PASS banner, got:\n%s", out.String())
	}
	// Explicit probe must NOT have been called on the wildcard-pass
	// path — one Resolve() call, not six (wildcard + 5 explicit).
	if dns.calls != 1 {
		t.Errorf("wanted 1 Resolve call, got %d (explicit probe fired unnecessarily)", dns.calls)
	}
}

// TestCheckDNSReady_ExplicitFQDNsMatch — operator wired per-hostname
// records instead of a wildcard. Wildcard probe fails (NXDOMAIN);
// explicit-FQDN probe finds all 5 canonical subdomains resolving to
// the node IP; gate passes.
func TestCheckDNSReady_ExplicitFQDNsMatch(t *testing.T) {
	answers := map[string][]string{}
	// intentionally NO wildcard-probe answer → NXDOMAIN
	for _, sub := range []string{"kube-api", "console", "login", "grafana", "s3"} {
		answers[sub+"."+testDomain] = []string{testNodeIP}
	}
	dns := &fakeDNS{answers: answers}
	var out bytes.Buffer
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:         testDomain,
		NodeExternalIP: testNodeIP,
		DNS:            dns,
		Out:            &out,
		SubLabel:       testSub,
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !strings.Contains(out.String(), "DNS gate: PASS (explicit FQDNs)") {
		t.Errorf("expected explicit-FQDN PASS banner, got:\n%s", out.String())
	}
}

// TestCheckDNSReady_Blocked — both probes fail (no wildcard, some
// explicit FQDNs missing). Gate returns ErrDNSGateBlocked and the
// operator sees the canonical single wildcard record recommendation
// + a diagnostics line.
func TestCheckDNSReady_Blocked(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{}}
	var out bytes.Buffer
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:         testDomain,
		NodeExternalIP: testNodeIP,
		DNS:            dns,
		Out:            &out,
		SubLabel:       testSub,
	})
	if err == nil {
		t.Fatal("want ErrDNSGateBlocked, got nil")
	}
	if !errors.Is(err, ErrDNSGateBlocked) {
		t.Errorf("want ErrDNSGateBlocked, got: %v", err)
	}
	// Actionable remediation hint on the error text
	if !strings.Contains(err.Error(), "--allow-dns-not-ready") {
		t.Errorf("error should point at the escape hatch: %v", err)
	}
	// Record-set output shape
	got := out.String()
	if !strings.Contains(got, "TYPE") || !strings.Contains(got, "NAME") ||
		!strings.Contains(got, "VALUE") || !strings.Contains(got, "TTL") {
		t.Errorf("expected column header in output, got:\n%s", got)
	}
	wantRow := "*." + testDomain
	if !strings.Contains(got, wantRow) {
		t.Errorf("expected wildcard record row (%q), got:\n%s", wantRow, got)
	}
	if !strings.Contains(got, testNodeIP) {
		t.Errorf("expected node-IP in record row, got:\n%s", got)
	}
	if !strings.Contains(got, "Diagnostics:") {
		t.Errorf("expected diagnostics tail, got:\n%s", got)
	}
}

// TestCheckDNSReady_BlockedButAllowFlag — the same failing DNS
// state, but the operator opted in via --allow-dns-not-ready.
// Gate returns nil, still prints the record-set (so the operator can
// wire it later), and adds an explicit "TLS certs will land Pending"
// warning so the escape hatch isn't silent.
func TestCheckDNSReady_BlockedButAllowFlag(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{}}
	var out bytes.Buffer
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:           testDomain,
		NodeExternalIP:   testNodeIP,
		AllowDNSNotReady: true,
		DNS:              dns,
		Out:              &out,
		SubLabel:         testSub,
	})
	if err != nil {
		t.Fatalf("--allow-dns-not-ready should not block, got %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "--allow-dns-not-ready set; proceeding") {
		t.Errorf("expected the 'proceeding' banner, got:\n%s", got)
	}
	if !strings.Contains(got, "TLS certs") {
		t.Errorf("expected the 'certs will land Pending' warning to keep the flag from being silent, got:\n%s", got)
	}
	// Record-set is still printed so operators can still wire it.
	if !strings.Contains(got, "*."+testDomain) {
		t.Errorf("expected the record-set even on --allow path, got:\n%s", got)
	}
}

// TestCheckDNSReady_WildcardMismatch — wildcard resolves but to the
// wrong IP (operator's registrar has a stale record from a previous
// cluster). Wildcard probe returns Blocker; explicit-FQDN probe
// finds nothing; gate blocks. Regression test for "silent-fail on
// stale wildcard" — the previous non-gated apply would have gone
// through and then failed with cert-issuance errors on a domain
// pointing elsewhere.
func TestCheckDNSReady_WildcardMismatch(t *testing.T) {
	stale := "8.8.8.8" // resolves, but not to our node
	dns := &fakeDNS{answers: map[string][]string{
		testWildQ: {stale},
	}}
	var out bytes.Buffer
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:         testDomain,
		NodeExternalIP: testNodeIP,
		DNS:            dns,
		Out:            &out,
		SubLabel:       testSub,
	})
	if err == nil || !errors.Is(err, ErrDNSGateBlocked) {
		t.Fatalf("want ErrDNSGateBlocked, got %v", err)
	}
	// Diagnostics line should mention that wildcard was partial
	// (resolves-but-mismatch) not simply missing — makes the
	// stale-record cause discoverable.
	if !strings.Contains(out.String(), "partial") {
		t.Errorf("expected 'partial' in diagnostics for stale-wildcard case:\n%s", out.String())
	}
}

// TestCheckDNSReady_NilDNSClient — defensive guard for programmatic
// callers or test paths that forget to wire the adapter.
func TestCheckDNSReady_NilDNSClient(t *testing.T) {
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain:         testDomain,
		NodeExternalIP: testNodeIP,
	})
	if err == nil || !errors.Is(err, ErrDNSGateBlocked) {
		t.Errorf("want ErrDNSGateBlocked for nil DNSClient, got: %v", err)
	}
	if !strings.Contains(err.Error(), "internal wiring bug") {
		t.Errorf("expected 'internal wiring bug' hint: %v", err)
	}
}

// TestCheckDNSReady_EmptyDomain / EmptyNodeExternalIP — these should
// have been caught by InitOptions.Validate upstream, but a defensive
// guard means callers who bypass Validate get a clear error rather
// than a panic downstream in the probe's own guards.
func TestCheckDNSReady_EmptyDomain(t *testing.T) {
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		NodeExternalIP: testNodeIP,
		DNS:            &fakeDNS{},
	})
	if err == nil || !errors.Is(err, ErrDNSGateBlocked) {
		t.Errorf("want ErrDNSGateBlocked for empty Domain, got: %v", err)
	}
}

func TestCheckDNSReady_EmptyNodeIP(t *testing.T) {
	err := CheckDNSReady(context.Background(), DNSGateOptions{
		Domain: testDomain,
		DNS:    &fakeDNS{},
	})
	if err == nil || !errors.Is(err, ErrDNSGateBlocked) {
		t.Errorf("want ErrDNSGateBlocked for empty NodeExternalIP, got: %v", err)
	}
}

// TestRenderDNSGateFailure_WildcardHintPreferred — when both probe
// results carry FixHint.Records, the render prefers the wildcard's
// (single row per B-003) over the explicit's (5 rows). Guards
// against a future probe refactor that populates the explicit
// hint's records first — the M4-T08 gate specifically wants B-003's
// single-record recommendation.
func TestRenderDNSGateFailure_WildcardHintPreferred(t *testing.T) {
	wildcardR := ports.Result{
		Status: ports.StatusMissing,
		FixHint: ports.FixHint{
			Records: []ports.DNSRecord{{
				Type: ports.DNSRecordTypeA, Name: "*." + testDomain,
				Value: testNodeIP, TTL: 300,
			}},
		},
	}
	explicitR := ports.Result{
		Status: ports.StatusMissing,
		FixHint: ports.FixHint{
			Records: []ports.DNSRecord{
				{Type: "A", Name: "kube-api." + testDomain, Value: testNodeIP, TTL: 300},
				{Type: "A", Name: "console." + testDomain, Value: testNodeIP, TTL: 300},
				{Type: "A", Name: "login." + testDomain, Value: testNodeIP, TTL: 300},
				{Type: "A", Name: "grafana." + testDomain, Value: testNodeIP, TTL: 300},
				{Type: "A", Name: "s3." + testDomain, Value: testNodeIP, TTL: 300},
			},
		},
	}
	got := renderDNSGateFailure(testDomain, testNodeIP, wildcardR, explicitR)
	if !strings.Contains(got, "*."+testDomain) {
		t.Errorf("expected wildcard record row, got:\n%s", got)
	}
	if strings.Contains(got, "kube-api."+testDomain) {
		t.Errorf("wildcard hint should have suppressed the 5-row explicit block, got:\n%s", got)
	}
}

// TestRenderDNSGateFailure_FallbackToExplicit — when the wildcard
// probe's FixHint has no Records (defensive edge — shouldn't happen
// once Domain + NodeIP are set, but we compose the fallback
// anyway), the render falls through to the explicit probe's records.
func TestRenderDNSGateFailure_FallbackToExplicit(t *testing.T) {
	wildcardR := ports.Result{Status: ports.StatusMissing, FixHint: ports.FixHint{}}
	explicitR := ports.Result{
		Status: ports.StatusPartial,
		FixHint: ports.FixHint{
			Records: []ports.DNSRecord{{
				Type: "A", Name: "console." + testDomain,
				Value: testNodeIP, TTL: 300,
			}},
		},
	}
	got := renderDNSGateFailure(testDomain, testNodeIP, wildcardR, explicitR)
	if !strings.Contains(got, "console."+testDomain) {
		t.Errorf("expected explicit fallback records, got:\n%s", got)
	}
}
