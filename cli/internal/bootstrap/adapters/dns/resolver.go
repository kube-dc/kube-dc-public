// Package dns is the real ports.DNSClient adapter. It is intentionally
// trivial: a thin wrapper around net.Resolver with a single fallback
// to a public resolver when the system resolver appears broken.
//
// **No provider plugins**, per B-003. Customers run their own DNS
// (BIND/PowerDNS/Windows DNS/registrar-hosted/IPAM). Shipping in-tree
// Cloudflare/Route53 plugins would serve a minority while implying
// universal coverage. The CLI's job is to *verify* DNS the operator
// has configured (M1-T03 probes) and *tell* them what to add when it's
// missing (FixHint with the record block) — never to mutate DNS itself.
//
// IPv4-only in v1. AAAA support waits for stack-level dual-stack
// support — see installer-prd.md §3 Non-Goals.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fallbackResolverAddr is the public resolver consulted when the system
// resolver returns a transport-level error (no nameserver reachable,
// network unreachable, etc.). 1.1.1.1 because it answers DoT/DoH and
// has a strong privacy story; if the operator's corporate proxy is
// blocking us, the fallback at least tells us the AUTHORITATIVE answer
// for their wildcard so we can compare. Soft warning only; we never
// silently substitute the fallback's answer when the system resolver
// returns NXDOMAIN.
const fallbackResolverAddr = "1.1.1.1:53"

// fallbackResolverTimeout caps the public-resolver hop. The system
// resolver inherits the caller's context deadline; the fallback adds
// its own cap so a hanging upstream can't double the operator's wait.
const fallbackResolverTimeout = 3 * time.Second

// Resolver is the ports.DNSClient implementation. Zero-value usable —
// New() exists for symmetry with the other adapters and to admit a
// future config knob (e.g. operator-supplied fallback resolver).
type Resolver struct {
	systemResolver   *net.Resolver
	fallbackResolver *net.Resolver

	// testLookup, when non-nil, overrides the net.Resolver.LookupIP
	// call inside lookup(). Used by adapter unit tests to inject
	// canned responses without making outbound DNS calls. nil in
	// production — the contract is "hermetic tests, no real-network
	// calls in CI" (see M0-T06 acceptance).
	testLookup func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error)
}

// New constructs a Resolver. systemResolver defaults to the Go runtime's
// system resolver; fallback uses 1.1.1.1 over UDP. Future knobs (custom
// fallback, disable-fallback, force-cgo-resolver) live on this
// constructor, not on the struct, so consumers can keep depending on
// the zero-value-friendly ports.DNSClient interface.
func New() *Resolver {
	return &Resolver{
		systemResolver: net.DefaultResolver,
		fallbackResolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: fallbackResolverTimeout}
				return d.DialContext(ctx, network, fallbackResolverAddr)
			},
		},
	}
}

// Resolve implements ports.DNSClient. NXDOMAIN is mapped to (nil, nil)
// per the contract — the wildcard probe interprets "no records" as
// "operator needs to add the wildcard", not as a probe failure.
//
// recordType "A" is the only v1 type. Anything else returns a typed
// error so a caller passing AAAA today gets a loud signal instead of
// a silent empty result.
func (r *Resolver) Resolve(ctx context.Context, name, recordType string) ([]string, error) {
	if recordType != ports.DNSRecordTypeA {
		return nil, fmt.Errorf("dns: record type %q not supported in v1 (A only)", recordType)
	}
	if name == "" {
		return nil, fmt.Errorf("dns: empty name")
	}

	ips, err := r.lookup(ctx, r.systemResolver, name)
	if err == nil {
		return ips, nil
	}

	if isNotFound(err) {
		// NXDOMAIN per contract — empty slice, no error.
		return nil, nil
	}

	// Transport / I/O error. Try the public fallback once. If it also
	// fails, surface the ORIGINAL error so the caller debugs the
	// system resolver, not the fallback.
	fallbackIPs, fbErr := r.lookup(ctx, r.fallbackResolver, name)
	if fbErr == nil {
		return fallbackIPs, nil
	}
	if isNotFound(fbErr) {
		return nil, nil
	}
	return nil, fmt.Errorf("dns: resolve %q: %w (fallback resolver also failed: %v)", name, err, fbErr)
}

func (r *Resolver) lookup(ctx context.Context, res *net.Resolver, name string) ([]string, error) {
	var ipAddrs []net.IP
	var err error
	if r.testLookup != nil {
		ipAddrs, err = r.testLookup(ctx, res, name)
	} else {
		ipAddrs, err = res.LookupIP(ctx, "ip4", name)
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ipAddrs))
	for _, ip := range ipAddrs {
		// LookupIP("ip4", ...) returns IPv4 already, but defensive
		// guard against future Go runtime changes.
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out, nil
}

// isNotFound reports whether err represents NXDOMAIN / "host not found"
// versus a real I/O failure. net.DNSError exposes IsNotFound on Go
// 1.13+; older resolver libraries DON'T always fill IsNotFound in even
// when the underlying error is NXDOMAIN. We therefore short-circuit
// on `dnsErr.IsNotFound == true` but fall through to string-matching
// when it's false — `&net.DNSError{Err: "no such host"}` (IsNotFound
// unset) is a real shape seen in some libraries.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") || strings.Contains(msg, "nxdomain")
}
