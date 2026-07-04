package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeDNS returns canned answers for ports.DNSClient.Resolve. The
// resolver matches by exact name (which means tests pin the
// wildcard probe's sub-label via WildcardDNSProbe.SetSubLabel to
// keep the lookup deterministic).
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
		return nil, errors.New("fakeDNS: only A records supported")
	}
	return f.answers[name], nil
}

// ---------- WildcardDNSProbe ----------

func TestWildcardDNSProbe_Resolves_MatchesIP(t *testing.T) {
	dns := &fakeDNS{
		answers: map[string][]string{
			"kdc-dns-test-abcd1234.acme.example.com": {"213.111.154.233"},
		},
	}
	p := NewWildcardDNSProbe("acme.example.com", "213.111.154.233", dns)
	p.SetSubLabel("abcd1234")

	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
	if r.Severity != ports.SeverityInfo {
		t.Errorf("severity=%v want Info", r.Severity)
	}
}

func TestWildcardDNSProbe_NXDOMAIN_Blocker(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{}}
	p := NewWildcardDNSProbe("acme.example.com", "213.111.154.233", dns)
	p.SetSubLabel("nx")

	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("status=%v want missing", r.Status)
	}
	if r.Severity != ports.SeverityBlocker {
		t.Errorf("severity=%v want Blocker", r.Severity)
	}
	if len(r.FixHint.Records) != 1 {
		t.Errorf("FixHint should carry the wildcard record: %+v", r.FixHint)
	}
	rec := r.FixHint.Records[0]
	if rec.Name != "*.acme.example.com" || rec.Value != "213.111.154.233" || rec.Type != "A" || rec.TTL != 300 {
		t.Errorf("FixHint record wrong: %+v", rec)
	}
}

func TestWildcardDNSProbe_WrongIP_Blocker(t *testing.T) {
	dns := &fakeDNS{
		answers: map[string][]string{
			"kdc-dns-test-bad.acme.example.com": {"203.0.113.99"},
		},
	}
	p := NewWildcardDNSProbe("acme.example.com", "213.111.154.233", dns)
	p.SetSubLabel("bad")

	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want partial+blocker", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "203.0.113.99") || !strings.Contains(r.Detail, "213.111.154.233") {
		t.Errorf("Detail should call out both IPs: %q", r.Detail)
	}
}

func TestWildcardDNSProbe_MultipleA_OneMatches_OK(t *testing.T) {
	// Some providers return multiple A records (e.g. Cloudflare).
	// One match should be sufficient.
	dns := &fakeDNS{
		answers: map[string][]string{
			"kdc-dns-test-multi.acme.example.com": {"203.0.113.99", "213.111.154.233"},
		},
	}
	p := NewWildcardDNSProbe("acme.example.com", "213.111.154.233", dns)
	p.SetSubLabel("multi")

	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed (one of multiple A records matches)", r.Status)
	}
}

func TestWildcardDNSProbe_EmptyDomain_Blocker(t *testing.T) {
	p := NewWildcardDNSProbe("", "1.2.3.4", &fakeDNS{})
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want missing+blocker", r.Status, r.Severity)
	}
}

func TestWildcardDNSProbe_ResolverError_Warn(t *testing.T) {
	dns := &fakeDNS{err: errors.New("connection refused")}
	p := NewWildcardDNSProbe("acme.example.com", "1.2.3.4", dns)
	p.SetSubLabel("err")

	r := p.Run(context.Background())
	if r.Severity != ports.SeverityWarn {
		t.Errorf("severity=%v want Warn (transport, not absence)", r.Severity)
	}
}

func TestWildcardDNSProbe_RandomSubLabel_PerRun(t *testing.T) {
	// Two Run() calls without SetSubLabel must use DIFFERENT
	// sub-labels — defeats negative-resolution cache.
	dns := &fakeDNS{answers: map[string][]string{}} // every name returns []
	p := NewWildcardDNSProbe("acme.example.com", "1.2.3.4", dns)

	// Track the names we resolved by hooking into the fakeDNS via
	// closure.
	var seenNames []string
	dnsWrapper := &recordingDNS{inner: dns, seen: &seenNames}
	p.dns = dnsWrapper

	p.Run(context.Background())
	p.Run(context.Background())

	if len(seenNames) != 2 {
		t.Fatalf("expected 2 lookups, got %d", len(seenNames))
	}
	if seenNames[0] == seenNames[1] {
		t.Errorf("two runs used the same sub-label: %q", seenNames[0])
	}
	for _, n := range seenNames {
		if !strings.HasPrefix(n, "kdc-dns-test-") {
			t.Errorf("sub-label not in canonical shape: %q", n)
		}
	}
}

func TestRandomSubLabel_Length(t *testing.T) {
	s := randomSubLabel()
	if len(s) != 8 {
		t.Errorf("randomSubLabel returned %q (len %d), want 8 hex chars", s, len(s))
	}
}

// ---------- ExplicitFQDNDNSProbe ----------

func TestExplicitFQDNDNSProbe_AllResolveCorrectly_Installed(t *testing.T) {
	answers := map[string][]string{}
	for _, sub := range requiredFQDNs {
		answers[sub+".acme.example.com"] = []string{"213.111.154.233"}
	}
	p := NewExplicitFQDNDNSProbe("acme.example.com", "213.111.154.233", &fakeDNS{answers: answers})

	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
	if !strings.Contains(r.FixHint.Text, "wildcard") {
		t.Errorf("Should still recommend wildcard for future-proofing: %q", r.FixHint.Text)
	}
}

func TestExplicitFQDNDNSProbe_MissingSome_Blocker(t *testing.T) {
	answers := map[string][]string{
		"kube-api.acme.example.com": {"213.111.154.233"},
		"console.acme.example.com":  {"213.111.154.233"},
	}
	p := NewExplicitFQDNDNSProbe("acme.example.com", "213.111.154.233", &fakeDNS{answers: answers})

	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want missing+Blocker", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "login") || !strings.Contains(r.Detail, "grafana") || !strings.Contains(r.Detail, "s3") {
		t.Errorf("Detail should call out missing FQDNs: %q", r.Detail)
	}
	if len(r.FixHint.Records) != 1 || r.FixHint.Records[0].Name != "*.acme.example.com" {
		t.Errorf("FixHint should still recommend the single wildcard record: %+v", r.FixHint)
	}
}

func TestExplicitFQDNDNSProbe_AllResolveWrongIP_Blocker(t *testing.T) {
	answers := map[string][]string{}
	for _, sub := range requiredFQDNs {
		answers[sub+".acme.example.com"] = []string{"203.0.113.99"} // wrong IP
	}
	p := NewExplicitFQDNDNSProbe("acme.example.com", "213.111.154.233", &fakeDNS{answers: answers})

	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want partial+Blocker", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "203.0.113.99") {
		t.Errorf("Detail should call out wrong IP: %q", r.Detail)
	}
}

func TestExplicitFQDNDNSProbe_NoNodeIP_StillResolves(t *testing.T) {
	// Early `doctor` runs may not have --node-external-ip yet. The
	// FQDN probe should still report which records exist; the
	// FixHint uses a placeholder for the missing IP.
	answers := map[string][]string{}
	for _, sub := range requiredFQDNs {
		answers[sub+".acme.example.com"] = []string{"203.0.113.99"}
	}
	p := NewExplicitFQDNDNSProbe("acme.example.com", "", &fakeDNS{answers: answers})

	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed when no expected IP", r.Status)
	}
}

// ---------- AllDNSProbes ----------

func TestAllDNSProbes_ReturnsTwo(t *testing.T) {
	probes := AllDNSProbes("acme.example.com", "1.2.3.4", &fakeDNS{})
	if len(probes) != 2 {
		t.Errorf("AllDNSProbes returned %d, want 2", len(probes))
	}
}

// ports.Probe contract: probes MUST return immediately on
// ctx.Done(). Verified for both DNS probes.
func TestWildcardDNSProbe_CancelledContext_ReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewWildcardDNSProbe("acme.example.com", "1.2.3.4", &fakeDNS{})
	r := p.Run(ctx)
	if !strings.Contains(r.Detail, "cancelled") {
		t.Errorf("not cancelled: %q", r.Detail)
	}
}

// Mid-loop cancellation: cancel ctx after the first FQDN resolves;
// the loop's per-iter ctx check must surface the cancel rather than
// completing the remaining 4 lookups.
func TestExplicitFQDNDNSProbe_CancelMidLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dns := &cancellingDNS{cancel: cancel, after: 1}
	p := NewExplicitFQDNDNSProbe("acme.example.com", "1.2.3.4", dns)
	r := p.Run(ctx)
	if !strings.Contains(r.Detail, "cancelled") {
		t.Errorf("mid-loop cancellation not surfaced: %q", r.Detail)
	}
	if dns.calls > 2 {
		t.Errorf("loop continued past cancellation: %d resolves", dns.calls)
	}
}

// Nil DNSClient: should surface a clear "wiring bug" error instead
// of nil-pointer panicking.
func TestWildcardDNSProbe_NilDNS_NoPanic(t *testing.T) {
	p := NewWildcardDNSProbe("acme.example.com", "1.2.3.4", nil)
	r := p.Run(context.Background())
	if !strings.Contains(r.Detail, "not configured") {
		t.Errorf("nil dns should surface clear error, got: %q", r.Detail)
	}
}

func TestExplicitFQDNDNSProbe_NilDNS_NoPanic(t *testing.T) {
	p := NewExplicitFQDNDNSProbe("acme.example.com", "1.2.3.4", nil)
	r := p.Run(context.Background())
	if !strings.Contains(r.Detail, "not configured") {
		t.Errorf("nil dns should surface clear error, got: %q", r.Detail)
	}
}

// cancellingDNS fires ctx.cancel after N Resolve calls and counts
// each call so the test can assert the loop bailed early.
type cancellingDNS struct {
	cancel context.CancelFunc
	after  int
	calls  int
}

func (c *cancellingDNS) Resolve(_ context.Context, _ string, _ string) ([]string, error) {
	c.calls++
	if c.calls == c.after {
		c.cancel()
	}
	return nil, nil
}

// ---------- helpers ----------

// recordingDNS captures every Resolve target so the random-sub-label
// test can assert uniqueness across runs.
type recordingDNS struct {
	inner ports.DNSClient
	seen  *[]string
}

func (r *recordingDNS) Resolve(ctx context.Context, name, t string) ([]string, error) {
	*r.seen = append(*r.seen, name)
	return r.inner.Resolve(ctx, name, t)
}
