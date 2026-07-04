package ports

import "context"

// DNSClient is intentionally minimal: **resolution only, no provisioning**.
// See B-003 in installer-agentic-implementation-plan.md for the full
// reasoning — customers running their own clouds use whatever DNS they
// run (BIND, PowerDNS, Windows DNS, hardware-vendor IPAM, registrar
// hosted DNS), and shipping in-tree Cloudflare/Route53 plugins would
// serve a minority while implying universal coverage.
//
// The CLI's DNS surface is verification only:
//   1. M1-T03 probes resolve a randomised sub-label (`kdc-dns-test-<rand>.
//      <domain>`) — proves a wildcard A record is correctly wired without
//      trusting the operator's claim of "I added the wildcard".
//   2. On failure, doctor prints the single A record the operator must
//      create (B-003 FixHint shape — see probe.go `FixHint.Records`).
//   3. `init` refuses to mutate when the wildcard probe fails unless
//      `--allow-dns-not-ready` is passed (certs land Pending until
//      operator wires DNS).
//
// IPv4-only in v1; AAAA / dual-stack support is v2 (the Kube-DC stack
// itself is IPv4-only today — see installer-prd.md §3 Non-Goals).
type DNSClient interface {
	// Resolve looks up `name` against the system resolver and returns
	// the resolved records. `recordType` is "A" (IPv4 — the only type
	// v1 cares about). The adapter falls back to a known public resolver
	// (1.1.1.1 / 8.8.8.8) with a soft warning when the system resolver
	// is unreachable (e.g. operator's corporate proxy blocks port 53).
	//
	// Returns an empty slice (not an error) when the name does not
	// resolve — `NXDOMAIN` is operationally a missing record, not a
	// failure. Genuine errors (timeout, malformed name, resolver
	// failure) return a non-nil error.
	Resolve(ctx context.Context, name, recordType string) ([]string, error)
}

// DNSRecord is the structured record-set entry that `FixHint.Records`
// carries when M1-T03 reports a missing wildcard. The doctor printer
// (M1-T05) renders these as a copy-paste block in the operator's
// terminal:
//
//	A     *.acme-prod.example.com    213.111.154.233    300
//
// **Type is always "A" in v1**. The struct admits AAAA for forward-compat
// but M1-T03 never emits AAAA records until v2 lands dual-stack stack
// support — the constant `DNSRecordTypeA` is the only safe value to set
// today. See installer-prd.md §3 Non-Goals (IPv6 row).
type DNSRecord struct {
	Type  string // "A" only in v1
	Name  string // e.g. "*.acme-prod.example.com"
	Value string // e.g. "213.111.154.233"
	TTL   int    // seconds; doctor defaults to 300
}

const (
	DNSRecordTypeA = "A"
)
