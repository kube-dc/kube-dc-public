package mock

import (
	"context"
	"strings"
)

// DNSClient resolves names against the scenario's DNS fixture. Wildcard
// entries are honoured: a scenario can declare `"*.example.com": ["1.2.3.4"]`
// and any sub-label (`foo.example.com`, `kdc-dns-test-abc.example.com`)
// resolves to that IP unless a more-specific entry shadows it.
//
// This wildcard matcher is what makes M1-T03's random-sub-label probe
// useful in mocks — the probe generates `kdc-dns-test-<rand>.<domain>`
// and the wildcard fixture catches it.
type DNSClient struct {
	scenario *Scenario
}

func NewDNSClient(s *Scenario) *DNSClient { return &DNSClient{scenario: s} }

func (c *DNSClient) Resolve(ctx context.Context, name, recordType string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.DNS == nil {
		return []string{}, nil
	}
	if recordType == "" {
		recordType = "A"
	}
	// v1 only handles A; AAAA is v2. Quietly return [] for anything
	// else (matches the production "NXDOMAIN as missing, not error"
	// behaviour for fields the mock doesn't model).
	if recordType != "A" {
		return []string{}, nil
	}

	// Most-specific match wins. Walk the map preferring exact > wildcard.
	if vals, ok := c.scenario.DNS[name]; ok {
		out := make([]string, len(vals))
		copy(out, vals)
		return out, nil
	}
	// Wildcard pass: any "*.<suffix>" key whose suffix matches the
	// trailing portion of `name`.
	for pattern, vals := range c.scenario.DNS {
		if !strings.HasPrefix(pattern, "*.") {
			continue
		}
		suffix := strings.TrimPrefix(pattern, "*.")
		if !strings.HasSuffix(name, "."+suffix) && name != suffix {
			continue
		}
		// Defence against the wildcard matching its own suffix — a
		// query for "example.com" against pattern "*.example.com"
		// should NOT match.
		if strings.TrimSuffix(name, "."+suffix) == "" {
			continue
		}
		out := make([]string, len(vals))
		copy(out, vals)
		return out, nil
	}
	return []string{}, nil
}
