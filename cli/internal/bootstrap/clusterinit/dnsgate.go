// M4-T08 — DNS verification gate for `bootstrap init --apply`.
//
// Sits between the cobra layer's session build and clusterinit.Apply
// so that when the operator's wildcard DNS isn't yet in place we
// refuse to mutate — either the fleet repo (add-cluster.sh writes
// clusters/<name>/) or the cluster (flux-install.sh drops manifests
// into flux-system). Better to fail fast with a copy-paste record-set
// than to land a broken install where TLS certs sit Pending forever
// on Let's Encrypt HTTP-01 challenges against a domain that resolves
// nowhere.
//
// **Escape hatch**: `--allow-dns-not-ready` (already wired on the
// InitOptions struct + cobra flag surface since M4-T01). Operator
// takes on the promise "I know DNS is deferred; TLS will land Pending
// until I wire the records; that's fine for my install shape (e.g.,
// pre-configured internal split-horizon DNS the probe can't see, or
// intentional preview cluster)."
//
// **Not in dry-run**: dry-run must have no external side effects (per
// M4-T02 contract); DNS resolution is technically a side effect
// (traffic to a resolver). The dry-run plan renderer notes that the
// DNS gate WILL run at apply time so operators aren't surprised.
// Only runApplyEngine calls CheckDNSReady.
//
// **Verification-only, no provisioning** (B-003): the gate reuses the
// existing WildcardDNSProbe (canonical) with ExplicitFQDNDNSProbe as
// a fallback for operators who wired per-hostname A records instead
// of a wildcard. If either probe reports Installed → pass. If both
// fail → render one wildcard A record as the recommended fix, refuse
// with ErrDNSGateBlocked (unless AllowDNSNotReady).
package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ErrDNSGateBlocked is returned when both DNS probes come back
// non-Installed AND the operator did not opt out via
// --allow-dns-not-ready. Wraps a probe detail so cobra tests can
// errors.Is against the sentinel and still assert on the specific
// failure mode.
var ErrDNSGateBlocked = errors.New("init: DNS records not in place")

// DNSGateOptions is the parameter bundle for CheckDNSReady. The
// Domain / NodeExternalIP fields are the same values the plan
// carries (they came out of InitOptions.Validate); DNS is the real
// adapter from bootstrap.Session (or a fake in tests). SubLabel is a
// test seam — production leaves it empty so WildcardDNSProbe
// generates a fresh random label per Run (defeats resolver negative
// caching).
type DNSGateOptions struct {
	Domain           string
	NodeExternalIP   string
	AllowDNSNotReady bool
	DNS              ports.DNSClient
	Out              io.Writer

	// SubLabel pins the wildcard probe's random sub-label so tests
	// can wire a fake DNSClient with a deterministic answer set.
	// Empty in production (probe generates a fresh label per Run).
	SubLabel string
}

// CheckDNSReady runs the wildcard DNS probe against the operator's
// domain + expected node-external-IP. On a clean pass (StatusInstalled)
// it prints a one-line info banner and returns nil. On failure it
// falls back to the explicit-FQDN probe (for operators who wired
// per-hostname A records instead of a wildcard); a clean pass there
// also returns nil.
//
// If BOTH probes fail: renders the wildcard record the operator
// should add (or the explicit FQDN records if the wildcard probe
// couldn't compose a hint), then either returns nil with a warning
// (AllowDNSNotReady) or ErrDNSGateBlocked (default).
//
// Never mutates anything — read-only DNS traffic. Safe to run
// multiple times.
func CheckDNSReady(ctx context.Context, opts DNSGateOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.DNS == nil {
		return fmt.Errorf("%w: DNSClient not configured (internal wiring bug)", ErrDNSGateBlocked)
	}
	if opts.Domain == "" || opts.NodeExternalIP == "" {
		// InitOptions.Validate already refuses these upstream — but
		// callers who bypass Validate (tests, future engine paths)
		// deserve a clear error rather than a panic downstream.
		return fmt.Errorf("%w: Domain and NodeExternalIP must be set", ErrDNSGateBlocked)
	}

	// M4-T08-fu1 polish: bound the two DNS probes so a misconfigured
	// resolver / slow SERVFAIL loop can't hang the whole `bootstrap
	// init` invocation. 10s covers a normal recursive lookup + one
	// retry on both the wildcard + explicit-FQDN probes. Caller ctx
	// still wins when it has a shorter deadline.
	dnsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fmt.Fprintf(out, "[apply] DNS gate: probing *.%s → %s\n", opts.Domain, opts.NodeExternalIP)

	// Primary probe: wildcard A record. The M1-T03 randomised
	// sub-label defeats resolver negative caches so a run at
	// apply-time isn't fooled by a stale NXDOMAIN cached during
	// dry-run.
	wp := discover.NewWildcardDNSProbe(opts.Domain, opts.NodeExternalIP, opts.DNS)
	if opts.SubLabel != "" {
		wp.SetSubLabel(opts.SubLabel)
	}
	r := wp.Run(dnsCtx)
	if r.Status == ports.StatusInstalled {
		fmt.Fprintf(out, "[apply] DNS gate: PASS (wildcard) — %s\n", r.Detail)
		return nil
	}

	// Fallback: explicit-FQDN probe (kube-api / console / login /
	// grafana / s3). Operators who prefer per-hostname records over
	// a wildcard (e.g., stricter DNS policies) still pass the gate.
	// StatusInstalled here means ALL 5 canonical subdomains resolve
	// and match the node IP.
	ep := discover.NewExplicitFQDNDNSProbe(opts.Domain, opts.NodeExternalIP, opts.DNS)
	rExplicit := ep.Run(dnsCtx)
	if rExplicit.Status == ports.StatusInstalled {
		fmt.Fprintf(out, "[apply] DNS gate: PASS (explicit FQDNs) — %s\n", rExplicit.Detail)
		return nil
	}

	// Both probes failed. Render the record-set + decide based on
	// the escape hatch.
	fmt.Fprint(out, renderDNSGateFailure(opts.Domain, opts.NodeExternalIP, r, rExplicit))
	if opts.AllowDNSNotReady {
		fmt.Fprintln(out, "[apply] DNS gate: --allow-dns-not-ready set; proceeding.")
		fmt.Fprintln(out, "[apply]   TLS certs (Let's Encrypt HTTP-01) will land Pending until the records above resolve.")
		return nil
	}
	return fmt.Errorf("%w: %s (pass --allow-dns-not-ready to proceed anyway)",
		ErrDNSGateBlocked, r.Detail)
}

// renderDNSGateFailure formats the record-set the operator needs to
// create. Prefers the wildcard probe's FixHint.Records when
// populated (it's the canonical single-record shape B-003 mandates);
// falls back to the explicit-FQDN probe's records only when the
// wildcard probe couldn't compose them (e.g., NodeExternalIP was
// empty — though that case is caught earlier).
//
// Table format matches the M1-T05 doctor printer's DNS block so
// operators see the same shape across `doctor` and `init` failure
// paths. Column widths are wide enough for a 40-char apex + typical
// IPv4 dotted-quad without ragged edges.
func renderDNSGateFailure(domain, nodeIP string, r1, r2 ports.Result) string {
	var b strings.Builder
	b.WriteString("\nDNS records are not in place. Add the following at your registrar, then re-run --apply:\n\n")

	// Prefer the wildcard hint (single row — B-003 canonical). Only
	// fall back to the explicit-FQDN records when the wildcard hint
	// has none (defensive; probe always populates it when domain +
	// nodeIP are set, and we already gated those above).
	hint := r1.FixHint
	if len(hint.Records) == 0 && len(r2.FixHint.Records) > 0 {
		hint = r2.FixHint
	}
	if len(hint.Records) > 0 {
		b.WriteString(fmt.Sprintf("  %-6s %-40s %-15s %-6s\n", "TYPE", "NAME", "VALUE", "TTL"))
		for _, rec := range hint.Records {
			b.WriteString(fmt.Sprintf("  %-6s %-40s %-15s %-6d\n", rec.Type, rec.Name, rec.Value, rec.TTL))
		}
	} else {
		// Compose the canonical record inline as a last-resort
		// fallback — should never fire in practice (both probes
		// populate Records when Domain + NodeExternalIP are set),
		// but this keeps the operator-visible message useful even
		// if a future probe change strips FixHint.Records.
		b.WriteString(fmt.Sprintf("  %-6s %-40s %-15s %-6d\n",
			ports.DNSRecordTypeA, "*."+domain, nodeIP, 300))
	}

	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Diagnostics: wildcard probe %s, explicit-FQDN probe %s.\n",
		statusStr(r1), statusStr(r2)))
	return b.String()
}

// statusStr shortens a probe result to a one-word tag for the
// diagnostics line. Matches the M1-T05 doctor printer's status
// column vocabulary so operators recognise the terms.
func statusStr(r ports.Result) string {
	switch r.Status {
	case ports.StatusInstalled:
		return "OK"
	case ports.StatusPartial:
		return "partial"
	case ports.StatusMissing:
		return "missing"
	case ports.StatusManaged:
		return "managed"
	default:
		return string(r.Status)
	}
}
