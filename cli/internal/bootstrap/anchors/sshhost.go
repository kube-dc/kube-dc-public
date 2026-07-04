package anchors

import (
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// HostResolver maps a Kubernetes node name (as it appears in
// EXT_NET_ANCHOR_IPS / KUBE_OVN_GW_NODES) to an ports.SSHHost the
// dialler can connect to.
//
// The legacy resolver returns SSHHost{Alias: node} — relies on the
// operator's ~/.ssh/config to map the alias to an IP/Hostname.
// `NewHostResolver(nil)` returns this legacy behaviour so existing
// callers and tests are not regressed.
//
// Resolution precedence at the call site:
//   1. --ssh-host-map flag (per-invocation override)
//   2. EXT_NET_ANCHOR_SSH_HOSTS in cluster-config.env (fleet default)
//   3. ssh_config alias (legacy path)
//
// Validation happens at preset.go (validateAnchorSSHHosts) for the
// fleet value AND at flag parse time for --ssh-host-map. Both feed
// into MergeSSHHostMaps which produces the final map passed to
// NewHostResolver.
type HostResolver func(node string) ports.SSHHost

// NewHostResolver returns a HostResolver that overrides the legacy
// alias-only behaviour for any node present in `overrides`. Nodes
// not in the map fall through to the legacy `SSHHost{Alias: node}`
// path, so partial overrides are valid.
//
// `nil` or empty `overrides` returns the legacy resolver verbatim —
// safe to pass into Apply/Probe options on a cluster that hasn't
// configured EXT_NET_ANCHOR_SSH_HOSTS.
func NewHostResolver(overrides map[string]string) HostResolver {
	return func(node string) ports.SSHHost {
		if h, ok := overrides[node]; ok && strings.TrimSpace(h) != "" {
			// Alias kept for log/error formatting (sshTarget prefers
			// Alias when set). Hostname forces the dial target;
			// resolveHostConfig in adapters/ssh honours Hostname
			// over Alias-resolved HostName.
			return ports.SSHHost{Alias: node, Hostname: h}
		}
		return ports.SSHHost{Alias: node}
	}
}

// ParseSSHHostMap parses a comma-separated `node=host` map from
// cluster-config.env. Same shape contract as ParseAnchorMap. Returns
// nil on empty input (not error — partial fleet config is valid).
//
// Re-validation here mirrors validateAnchorSSHHosts in preset.go;
// the duplication is intentional so the parser can be invoked
// directly from Cobra flag parsing without round-tripping through
// preset validation.
func ParseSSHHostMap(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		node, host, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q missing '=' (expected node=host, e.g. host5-a=10.0.0.5)", pair)
		}
		node = strings.TrimSpace(node)
		host = strings.TrimSpace(host)
		if node == "" {
			return nil, fmt.Errorf("%q has empty node", pair)
		}
		if host == "" {
			return nil, fmt.Errorf("node %q has empty host", node)
		}
		if strings.ContainsAny(host, " \t=") {
			return nil, fmt.Errorf("host %q for node %q contains whitespace or '='", host, node)
		}
		if _, dup := out[node]; dup {
			return nil, fmt.Errorf("node %q listed more than once", node)
		}
		out[node] = host
	}
	return out, nil
}

// ParseSSHHostMapSlice handles cobra's StringSlice flag form. Each
// element is treated as one `node=host` pair (operators pass
// `--ssh-host-map a=1 --ssh-host-map b=2`; cobra accumulates them
// into a slice). Returns nil on empty slice; aggregates duplicates
// across elements.
func ParseSSHHostMapSlice(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string)
	for _, pair := range pairs {
		single, err := ParseSSHHostMap(pair)
		if err != nil {
			return nil, err
		}
		for k, v := range single {
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("node %q listed more than once across --ssh-host-map values", k)
			}
			out[k] = v
		}
	}
	return out, nil
}

// MergeSSHHostMaps applies flag overrides on top of fleet defaults.
// Flag wins per-node; absent nodes inherit the fleet entry; nodes
// in neither map are not present in the result, so the resolver
// falls through to the legacy alias path for them.
//
// Returns a non-nil empty map when both inputs are empty (callers
// can range over the result without nil-checks); ParseSSHHostMap's
// "nil on empty" is preserved upstream.
func MergeSSHHostMaps(fleet, flag map[string]string) map[string]string {
	out := make(map[string]string, len(fleet)+len(flag))
	for k, v := range fleet {
		out[k] = v
	}
	for k, v := range flag {
		out[k] = v
	}
	return out
}
