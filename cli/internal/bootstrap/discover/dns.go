package discover

// M1-T03 DNS probes. Per B-003: verification-only, no provisioning.
//
// **Wildcard-first design.** The canonical operator setup is one
// `*.<domain>` A record. WildcardDNSProbe resolves a random sub-
// label (`kdc-dns-test-<rand>.<domain>`) — random per call to defeat
// any negative-resolution cache. Wildcard matches → StatusInstalled;
// resolved IP doesn't match the operator's --node-external-ip →
// StatusPartial with a FixHint carrying the canonical single-record
// fix block.
//
// **Fallback FQDN probe.** When the wildcard probe fails, the
// operator may still have explicit A records for each of the 5
// canonical Kube-DC subdomains. ExplicitFQDNDNSProbe checks each
// and reports which exist. All 5 present + correctly pointed →
// StatusInstalled (supported, though wildcard is recommended for
// future-proofing). Any missing → StatusPartial pointing back at
// the wildcard fix block.
//
// IPv6 / AAAA support is deferred to v2 per installer-prd Non-Goals.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// requiredFQDNs is the canonical 5-subdomain fallback set when no
// wildcard exists. Matches the installer-ux mention of the 5 records
// an operator might create by hand instead of a wildcard.
var requiredFQDNs = []string{"kube-api", "console", "login", "grafana", "s3"}

// ---------- WildcardDNSProbe ----------

// WildcardDNSProbe verifies the *.{Domain} wildcard A record is in
// place AND resolves to the operator-supplied NodeExternalIP. The
// random sub-label defeats DNS-resolver negative caches that would
// otherwise let a probe stale-pass for hours.
type WildcardDNSProbe struct {
	Domain         string
	NodeExternalIP string

	dns      ports.DNSClient
	subLabel string // overridable for deterministic tests
}

// NewWildcardDNSProbe constructs a probe targeting the given domain
// + expected node-external IP. subLabel is generated lazily on each
// Run unless an explicit one is set via SetSubLabel (tests only).
func NewWildcardDNSProbe(domain, nodeIP string, dns ports.DNSClient) *WildcardDNSProbe {
	return &WildcardDNSProbe{Domain: domain, NodeExternalIP: nodeIP, dns: dns}
}

// SetSubLabel pins the random sub-label for deterministic tests.
// Production never calls this — randomSubLabel runs per Run.
func (p *WildcardDNSProbe) SetSubLabel(s string) { p.subLabel = s }

// Compile-time assertion.
var _ ports.Probe = (*WildcardDNSProbe)(nil)

func (p *WildcardDNSProbe) Name() string { return "wildcard-dns" }

func (p *WildcardDNSProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.dns == nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "DNSClient not configured (internal wiring bug)",
		}
	}
	if p.Domain == "" {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   "no domain configured (set --domain or the cluster's domain field)",
		}
	}
	sub := p.subLabel
	if sub == "" {
		sub = randomSubLabel()
	}
	name := fmt.Sprintf("kdc-dns-test-%s.%s", sub, p.Domain)

	ips, err := p.dns.Resolve(ctx, name, ports.DNSRecordTypeA)
	if err != nil {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("DNS resolver error for %s: %v", name, err),
			FixHint:  p.fixHint(),
		}
	}
	if len(ips) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   fmt.Sprintf("wildcard %s.%s did not resolve (random sub-label %s)", "*", p.Domain, name),
			FixHint:  p.fixHint(),
		}
	}
	// Cross-check resolved IPs against the operator-supplied node IP.
	// One match is sufficient — providers like Cloudflare may return
	// multiple A records (anycast).
	matched := false
	for _, ip := range ips {
		if ip == p.NodeExternalIP {
			matched = true
			break
		}
	}
	if !matched {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityBlocker,
			Detail:   fmt.Sprintf("wildcard %s.%s resolves to %v, want %s", "*", p.Domain, ips, p.NodeExternalIP),
			FixHint:  p.fixHint(),
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("wildcard %s.%s → %s", "*", p.Domain, p.NodeExternalIP),
	}
}

func (p *WildcardDNSProbe) fixHint() ports.FixHint {
	rec := ports.DNSRecord{
		Type:  ports.DNSRecordTypeA,
		Name:  "*." + p.Domain,
		Value: p.NodeExternalIP,
		TTL:   300,
	}
	if rec.Value == "" {
		rec.Value = "<node-external-ip>"
	}
	return ports.FixHint{
		Text: fmt.Sprintf(
			"Add one wildcard A record at the apex of %s:\n  %s   %s   %s   %d",
			p.Domain, rec.Type, rec.Name, rec.Value, rec.TTL,
		),
		Records: []ports.DNSRecord{rec},
	}
}

// randomSubLabel emits 8 hex chars from crypto/rand. Per call, so
// every Run defeats the resolver's negative-cache for the previous
// label. crypto/rand is overkill for non-secret randomness but
// math/rand would need a seed dance.
func randomSubLabel() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}

// ---------- ExplicitFQDNDNSProbe ----------

// ExplicitFQDNDNSProbe is the fallback when an operator set up
// individual records instead of a wildcard. Reports which of the 5
// canonical Kube-DC subdomains resolve. All 5 → StatusInstalled with
// a "wildcard recommended for future-proofing" hint; any missing →
// StatusPartial.
type ExplicitFQDNDNSProbe struct {
	Domain         string
	NodeExternalIP string

	dns ports.DNSClient
}

func NewExplicitFQDNDNSProbe(domain, nodeIP string, dns ports.DNSClient) *ExplicitFQDNDNSProbe {
	return &ExplicitFQDNDNSProbe{Domain: domain, NodeExternalIP: nodeIP, dns: dns}
}

var _ ports.Probe = (*ExplicitFQDNDNSProbe)(nil)

func (p *ExplicitFQDNDNSProbe) Name() string { return "explicit-fqdn-dns" }

func (p *ExplicitFQDNDNSProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.dns == nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "DNSClient not configured (internal wiring bug)",
		}
	}
	if p.Domain == "" {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   "no domain configured",
		}
	}
	var missing, mismatched []string
	for _, sub := range requiredFQDNs {
		if err := ctxCanceled(ctx); err != nil {
			return *err
		}
		fqdn := sub + "." + p.Domain
		ips, err := p.dns.Resolve(ctx, fqdn, ports.DNSRecordTypeA)
		if err != nil {
			mismatched = append(mismatched, fmt.Sprintf("%s: resolver error %v", fqdn, err))
			continue
		}
		if len(ips) == 0 {
			missing = append(missing, fqdn)
			continue
		}
		if p.NodeExternalIP != "" && !dnsContains(ips, p.NodeExternalIP) {
			mismatched = append(mismatched, fmt.Sprintf("%s → %v want %s", fqdn, ips, p.NodeExternalIP))
		}
	}
	sort.Strings(missing)
	sort.Strings(mismatched)

	switch {
	case len(missing) == 0 && len(mismatched) == 0:
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   fmt.Sprintf("all 5 explicit FQDNs resolve to %s", p.NodeExternalIP),
			FixHint: ports.FixHint{
				Text: fmt.Sprintf("Consider replacing 5 explicit records with a single wildcard:\n  A   *.%s   %s   300", p.Domain, p.NodeExternalIP),
			},
		}
	case len(missing) > 0:
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   fmt.Sprintf("missing explicit FQDNs: %s", strings.Join(missing, ", ")),
			FixHint: ports.FixHint{
				Text: fmt.Sprintf(
					"Add a single wildcard A record (recommended) at %s, OR add the %d missing explicit records:\n  A   *.%s   %s   300",
					p.Domain, len(missing), p.Domain, ipOrPlaceholder(p.NodeExternalIP),
				),
				Records: []ports.DNSRecord{{
					Type:  ports.DNSRecordTypeA,
					Name:  "*." + p.Domain,
					Value: ipOrPlaceholder(p.NodeExternalIP),
					TTL:   300,
				}},
			},
		}
	default: // mismatched only
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityBlocker,
			Detail:   fmt.Sprintf("explicit FQDNs resolve but to wrong IP: %s", strings.Join(mismatched, "; ")),
			FixHint: ports.FixHint{
				Text: fmt.Sprintf("Re-point records to %s OR replace with a single wildcard (recommended):\n  A   *.%s   %s   300", p.NodeExternalIP, p.Domain, p.NodeExternalIP),
			},
		}
	}
}

// ipOrPlaceholder yields a printable record value, falling back to a
// `<node-external-ip>` token when the operator hasn't supplied one
// yet (early `doctor` runs before --node-external-ip is set).
func ipOrPlaceholder(ip string) string {
	if ip == "" {
		return "<node-external-ip>"
	}
	return ip
}

// ---------- combined ----------

// AllDNSProbes returns both probes wired against the supplied DNS
// adapter. Caller passes the operator's domain + node-external-ip
// from cluster-config.env or CLI flags.
func AllDNSProbes(domain, nodeIP string, dns ports.DNSClient) []ports.Probe {
	return []ports.Probe{
		NewWildcardDNSProbe(domain, nodeIP, dns),
		NewExplicitFQDNDNSProbe(domain, nodeIP, dns),
	}
}

// ---------- helpers ----------

// dnsContains is a local copy of slice-contains to avoid colliding
// with the existing string-substring `contains` in this package's
// test files.
func dnsContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
