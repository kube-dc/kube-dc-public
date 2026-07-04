package anchors

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Entry is one parsed `host=CIDR` pair from EXT_NET_ANCHOR_IPS.
//
// Host is the cluster node name (must match KUBE_OVN_GW_NODES);
// CIDR is the L3 anchor address bound to the interface on that node
// (e.g. 100.64.0.11/16 on br-ext-cloud). CIDR keeps the prefix
// length — `ip addr replace` needs it and dropping to a bare IP
// would lose the broadcast / on-link semantics.
type Entry struct {
	Host string
	CIDR string
}

// ParseAnchorMap parses the comma-separated host=CIDR map shipped in
// `EXT_NET_ANCHOR_IPS`. Returns entries sorted by host so apply
// order is deterministic regardless of operator input ordering.
//
// Validation here is the LAST line of defense — preset.go already
// validates the same shape at clusterinit time. We re-validate
// because (a) operators can hand-edit cluster-config.env post-init
// (b) the CLI may be run against an existing fleet checkout where
// the file predates preset validation. Re-validation costs nothing.
//
// Returns an empty slice on empty input; nil error. Callers should
// treat empty as "no anchors configured for this cluster" — not a
// failure.
func ParseAnchorMap(raw string) ([]Entry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var entries []Entry
	seen := make(map[string]struct{})
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		host, cidr, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("anchors: %q missing '=' (expected host=CIDR)", pair)
		}
		host = strings.TrimSpace(host)
		cidr = strings.TrimSpace(cidr)
		if host == "" {
			return nil, fmt.Errorf("anchors: %q has empty host", pair)
		}
		if _, dup := seen[host]; dup {
			return nil, fmt.Errorf("anchors: host %q listed more than once", host)
		}
		seen[host] = struct{}{}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("anchors: %q invalid CIDR: %w", cidr, err)
		}
		entries = append(entries, Entry{Host: host, CIDR: cidr})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Host < entries[j].Host })
	return entries, nil
}
