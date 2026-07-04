package clusterinit

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// M4-T04 — preset → env-map table.
//
// Per installer-prd §4.1.2, `--preset` collapses the most error-prone
// block of `cluster-config.env` into a single flag. Presets are
// defaults only — `--set KEY=VALUE` always wins. The defaults shipped
// here mirror the values `bootstrap/add-cluster.sh` writes today,
// minus the per-operator values (VLAN IDs, NICs, public-VLAN CIDR)
// which are surfaced as `RequiredKeys` and must come from `--set`.
//
// **Why the explicit `RequiredKeys` list** (vs. inferring from
// `CHANGEME` placeholders in add-cluster.sh): the script emits
// CHANGEME for a few keys to signal "operator must edit", but it's
// not the canonical truth — some keys with CHANGEME values are
// actually fine to leave for the operator's manual post-edit pass
// (e.g. METALLB_FLOATING_IP). The preset table draws the line per
// installer-prd §4.1.2: required = "the cluster won't boot without
// this", optional = "operator may post-edit". Validation against
// `RequiredKeys` is the gate that catches CI configurations missing
// a VLAN ID before the plan is ever written to disk.
//
// **Why `Kustomizations`**: each preset includes a different set of
// Flux Kustomizations. `cloud+public-vlan` adds `infra-public-network`;
// `internal-only` omits it. The engine slice that scaffolds
// `clusters/<name>/kustomization.yaml` (M4-T10) consumes this list so
// the file lists the correct entries verbatim.

// PresetSpec is the typed definition of one preset's defaults +
// required-from-operator keys + Kustomization layer set.
//
// Defaults is the set of `cluster-config.env` keys with sensible
// values that don't depend on the operator's infrastructure
// topology (CIDR allocations, MTU, default network types).
//
// RequiredKeys lists keys the operator MUST supply via `--set` — the
// values are topology-specific (per-deployment VLAN IDs, the NIC
// name, the public-VLAN's CIDR + gateway).
//
// Kustomizations is the ordered list of Flux Kustomization names the
// new cluster's `kustomization.yaml` references. Insertion order is
// the canonical apply order: infra-cni → infra-core → optional
// infra-public-network → infra-object-storage → platform → addons.
type PresetSpec struct {
	Defaults       map[string]string
	RequiredKeys   []string
	Kustomizations []string
}

// ErrPresetMissingRequired is returned by EnvMapFor / Validate when
// the operator-supplied --set values are missing one or more keys
// the chosen preset requires. The error message lists the missing
// keys so operators don't have to guess.
var ErrPresetMissingRequired = errors.New("init: preset missing required --set values")

// ErrPresetInvalidValue is returned by ValidatePresetValues when a
// required key's value fails semantic validation (empty after
// trim, VLAN ID out of range, malformed CIDR/IP, gateway not in
// CIDR, empty NIC name). M4-T04+T13+T09 review-pass — P1/P2:
// catching these BEFORE T10 writes them to cluster-config.env on
// disk. Without this, `--set=EXT_PUBLIC_CIDR=` would pass the
// required-key gate (key present) but produce an unbootable
// cluster.
var ErrPresetInvalidValue = errors.New("init: preset value failed semantic validation")

// --- Preset definitions ---
//
// Default values mirror `kube-dc-fleet/bootstrap/add-cluster.sh:33-69`
// verbatim where the script's value is universal (not per-operator).
// Keys with operator-specific values surface in RequiredKeys instead
// of getting a CHANGEME default — the validation gate is louder than
// a placeholder that survives into committed cluster-config.env.

// universalNetworkDefaults are the network knobs every preset
// inherits — pod/svc CIDRs, MTUs, the join CIDR. These don't depend
// on the operator's external topology so live in one shared block.
var universalNetworkDefaults = map[string]string{
	"POD_CIDR":       "10.100.0.0/16",
	"POD_GATEWAY":    "10.100.0.1",
	"SVC_CIDR":       "10.101.0.0/16",
	"K8S_SERVICE_IP": "10.101.0.1",
	"CLUSTER_DNS":    "10.101.0.11",
	"JOIN_CIDR":      "172.30.0.0/22",
}

// universalMonitoringDefaults are the Prometheus storage knobs every
// preset inherits. Operator can override per-cluster via --set.
var universalMonitoringDefaults = map[string]string{
	"PROM_STORAGE":         "20Gi",
	"PROM_RETENTION":       "365d",
	"PROM_RETENTION_SIZE":  "17GiB",
}

// universalPlatformEndpointDefaults are the kube-api internal endpoint
// knobs every preset inherits. Both are safe-by-default — empty VIP
// + opt-in flag — so the feature stays off until the operator
// consciously picks a VIP, widens ext-cloud Subnet.excludeIps, adds
// the VIP to BOTH INGRESS_GLOBAL_ALLOWLIST and EGRESS_GLOBAL_ALLOWLIST,
// and flips the enabled flag.
//
// Why Defaults (not RequiredKeys): the cluster boots fine with the
// feature disabled. Forcing the operator to supply a VIP at scaffold
// time would be the wrong UX — they may not want the feature on
// day 1, and the VIP choice depends on coordinated allowlist work
// that we can't validate at preset-render time. See PRD §6.D.2
// (docs/prd/internal-platform-endpoints-implementation.md).
var universalPlatformEndpointDefaults = map[string]string{
	"KUBE_API_INTERNAL_VIP":              "",
	"PLATFORM_ENDPOINT_KUBE_API_ENABLED": "false",
}

// universalAnchorDefaults are the per-node anchor-IP knobs every
// preset inherits. Anchors are the L3 source-IPs MetalLB uses for its
// GARP announcements on br-ext-cloud; without one bound to a host
// interface on every gateway node, MetalLB silently degrades to a
// single-speaker-on-the-anchor-host topology (the load-bearing
// failure mode that bit atlantis on 2026-05-30 — Phase-0's
// hand-bound .11 turned out to be MetalLB's only viable speaker).
//
// EXT_NET_ANCHOR_IPS is a comma-separated `host=CIDR` map; hostnames
// MUST be a subset of KUBE_OVN_GW_NODES (cross-checked in
// ValidatePresetValues). EXT_NET_ANCHOR_INTERFACE defaults to
// br-ext-cloud (the kube-ovn-cni external-bridge name); operators on
// non-default ProviderNetwork names override it. EXT_NET_ANCHOR_REQUIRED
// gates the post-init `kube-dc bootstrap anchors apply` step from
// running on a cluster that legitimately has no anchors yet (greenfield
// install pre-§B.5 rollout).
//
// Safe-by-default posture mirrors universalPlatformEndpointDefaults:
// the cluster boots fine with EXT_NET_ANCHOR_IPS empty; the platform-
// endpoint feature requires anchors but is itself opt-in. See PRD
// §6.D (docs/prd/internal-platform-endpoints-implementation.md).
var universalAnchorDefaults = map[string]string{
	"EXT_NET_ANCHOR_IPS":       "",
	"EXT_NET_ANCHOR_INTERFACE": "br-ext-cloud",
	"EXT_NET_ANCHOR_REQUIRED":  "false",
	// EXT_NET_ANCHOR_SSH_HOSTS maps Kubernetes node names (the keys in
	// EXT_NET_ANCHOR_IPS) to real SSH targets the operator's laptop
	// can reach (bare IP, FQDN, or ssh_config alias). Required when
	// the operator's ~/.ssh/config does NOT alias the Kubernetes node
	// names. Empty default preserves the legacy ssh_config path.
	// Per-node override: `kube-dc bootstrap anchors apply --ssh-host-map
	// host5-a=10.0.0.5` (precedence: flag > fleet > ssh_config).
	"EXT_NET_ANCHOR_SSH_HOSTS": "",
}

// universalEmail is a placeholder — the operator's --email flag
// populates the actual EMAIL key downstream, so we don't ship a
// preset default for it (would otherwise shadow the flag).
//
// universalRookDefaults: skipped — Rook lives in its own
// `--rook-mode` flag tree, not in the preset table.

func mergeInto(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

// internalOnlyPreset — lab / dev / first-install. Tenant VPCs route
// only internally; no public EIPs. `EXT_NET_*` still required (the
// cluster needs a CGNAT pool for internal egress), but no
// `EXT_PUBLIC_*` block.
var internalOnlyPreset = func() PresetSpec {
	defaults := map[string]string{
		"EXT_NET_NAME":              "ext-cloud",
		"EXT_NET_TYPE":              "cloud",
		"EXT_NET_CIDR":              "100.65.0.0/16",
		"EXT_NET_GATEWAY":           "100.65.0.1",
		"EXT_NET_MTU":               "1400",
		"DEFAULT_GW_NETWORK_TYPE":   "cloud",
		// No DEFAULT_EIP_NETWORK_TYPE / DEFAULT_FIP_NETWORK_TYPE /
		// DEFAULT_SVC_LB_NETWORK_TYPE=public for internal-only —
		// these route to the cloud network by default in this preset.
		"DEFAULT_EIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_FIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_SVC_LB_NETWORK_TYPE": "cloud",
	}
	mergeInto(defaults, universalNetworkDefaults)
	mergeInto(defaults, universalMonitoringDefaults)
	mergeInto(defaults, universalPlatformEndpointDefaults)
	mergeInto(defaults, universalAnchorDefaults)
	return PresetSpec{
		Defaults: defaults,
		RequiredKeys: []string{
			"EXT_NET_VLAN_ID",
			"EXT_NET_INTERFACE",
		},
		Kustomizations: []string{
			"infra-cni",
			"infra-core",
			"infra-object-storage",
			"platform",
			"addons",
		},
	}
}()

// cloudVLANPreset — cloud NAT-only deployment. `EXT_NET_*` required;
// `EXT_PUBLIC_*` omitted. Used by the early kube-dc.cloud phase
// before the public VLAN was added.
var cloudVLANPreset = func() PresetSpec {
	defaults := map[string]string{
		"EXT_NET_NAME":              "ext-cloud",
		"EXT_NET_TYPE":              "cloud",
		"EXT_NET_CIDR":              "100.65.0.0/16",
		"EXT_NET_GATEWAY":           "100.65.0.1",
		"EXT_NET_MTU":               "1400",
		"DEFAULT_GW_NETWORK_TYPE":   "cloud",
		"DEFAULT_EIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_FIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_SVC_LB_NETWORK_TYPE": "cloud",
	}
	mergeInto(defaults, universalNetworkDefaults)
	mergeInto(defaults, universalMonitoringDefaults)
	mergeInto(defaults, universalPlatformEndpointDefaults)
	mergeInto(defaults, universalAnchorDefaults)
	return PresetSpec{
		Defaults: defaults,
		RequiredKeys: []string{
			"EXT_NET_VLAN_ID",
			"EXT_NET_INTERFACE",
		},
		Kustomizations: []string{
			"infra-cni",
			"infra-core",
			"infra-object-storage",
			"platform",
			"addons",
		},
	}
}()

// cloudPublicVLANPreset — production default. Both `EXT_NET_*`
// (cloud NAT pool for internal egress) and `EXT_PUBLIC_*` (public
// VLAN for routable EIPs) blocks. Used by kube-dc.cloud, stage, and
// (per the atlantis sprint) atlantis once the operator
// supplies the per-rack VLAN IDs.
var cloudPublicVLANPreset = func() PresetSpec {
	defaults := map[string]string{
		"EXT_NET_NAME":              "ext-cloud",
		"EXT_NET_TYPE":              "cloud",
		"EXT_NET_CIDR":              "100.65.0.0/16",
		"EXT_NET_GATEWAY":           "100.65.0.1",
		"EXT_NET_MTU":               "1400",
		"DEFAULT_GW_NETWORK_TYPE":   "cloud",
		"DEFAULT_EIP_NETWORK_TYPE":    "public",
		"DEFAULT_FIP_NETWORK_TYPE":    "public",
		"DEFAULT_SVC_LB_NETWORK_TYPE": "public",
	}
	mergeInto(defaults, universalNetworkDefaults)
	mergeInto(defaults, universalMonitoringDefaults)
	mergeInto(defaults, universalPlatformEndpointDefaults)
	mergeInto(defaults, universalAnchorDefaults)
	return PresetSpec{
		Defaults: defaults,
		RequiredKeys: []string{
			"EXT_NET_VLAN_ID",
			"EXT_NET_INTERFACE",
			"EXT_PUBLIC_VLAN_ID",
			"EXT_PUBLIC_CIDR",
			"EXT_PUBLIC_GATEWAY",
		},
		Kustomizations: []string{
			"infra-cni",
			"infra-core",
			"infra-public-network",
			"infra-object-storage",
			"platform",
			"addons",
		},
	}
}()

// customPreset — operator manages `cluster-config.env` directly.
// `init` validates the env-map shape but doesn't apply preset
// defaults. No required keys (operator vouches for the env by
// passing --preset=custom); no inherited defaults.
var customPreset = PresetSpec{
	Defaults:     map[string]string{},
	RequiredKeys: nil,
	// The Kustomization layer set still has a sensible fallback —
	// operators picking `custom` usually still want the full
	// production layer set. They can opt out per-layer via a future
	// --no-layer flag (deferred, not in v1).
	Kustomizations: []string{
		"infra-cni",
		"infra-core",
		"infra-public-network",
		"infra-object-storage",
		"platform",
		"addons",
	},
}

// presetSpecs is the lookup table. Indexed by the typed Preset enum.
var presetSpecs = map[Preset]PresetSpec{
	PresetInternalOnly:    internalOnlyPreset,
	PresetCloudVLAN:       cloudVLANPreset,
	PresetCloudPublicVLAN: cloudPublicVLANPreset,
	PresetCustom:          customPreset,
}

// SpecFor returns the PresetSpec for the named preset. Returns
// `(zero, false)` if the preset isn't recognised — callers should
// have run Validate first (which catches unknown presets).
func SpecFor(p Preset) (PresetSpec, bool) {
	s, ok := presetSpecs[p]
	return s, ok
}

// EnvMapFor returns the merged env map for the preset + operator
// `--set` overrides. Merge order:
//
//   1. Universal defaults (network/monitoring) from the preset spec.
//   2. Preset-specific defaults (EXT_NET_*, DEFAULT_*_NETWORK_TYPE).
//   3. `--set KEY=VALUE` deltas — these win over defaults.
//
// Returns ErrPresetMissingRequired if any RequiredKeys aren't in
// the final merged map (after --set is layered). The error message
// lists the missing keys + the preset name so operators don't have
// to look them up.
//
// Special case for PresetCustom: no defaults applied; --set values
// pass through verbatim; no RequiredKeys check (operator vouches by
// picking `custom`).
func EnvMapFor(p Preset, sets map[string]string) (map[string]string, error) {
	spec, ok := SpecFor(p)
	if !ok {
		return nil, fmt.Errorf("init: unknown preset %q", p)
	}

	out := make(map[string]string, len(spec.Defaults)+len(sets))
	for k, v := range spec.Defaults {
		out[k] = v
	}
	for k, v := range sets {
		// --set wins — including when the key isn't in the preset's
		// default set. This is intentional: presets are defaults, not
		// allow-lists; operators can layer arbitrary cluster-config
		// keys via --set (and the SCREAMING_SNAKE_CASE check in
		// options.go's validateSets catches typos).
		out[k] = v
	}

	// Required-key check.
	var missing []string
	for _, k := range spec.RequiredKeys {
		if _, ok := out[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: preset=%s; missing %s (pass via --set KEY=VALUE)",
			ErrPresetMissingRequired, p, strings.Join(missing, ", "))
	}

	return out, nil
}

// ValidatePresetRequiredKeys is the cobra-friendly entry point for
// the preset's required-key check. Runs EnvMapFor + value-semantic
// validation; used when callers only need the validation, not the
// merged env.
//
// Returns ErrPresetMissingRequired (key absent), ErrPresetInvalidValue
// (key present but empty/malformed), or nil on success.
func ValidatePresetRequiredKeys(o *InitOptions) error {
	if o == nil {
		return fmt.Errorf("ValidatePresetRequiredKeys: nil options")
	}
	envMap, err := EnvMapFor(o.Preset, o.Sets)
	if err != nil {
		return err
	}
	return ValidatePresetValues(o.Preset, envMap)
}

// ValidatePresetValues runs the semantic-validation pass over the
// merged env map (M4-T04+T13+T09 review-pass — P1/P2). Catches the
// "key present but unusable value" footgun before T10 writes the
// env to cluster-config.env on disk.
//
// Validation rules per required key:
//
//   - Every required key must have a non-whitespace value.
//   - `EXT_NET_VLAN_ID`, `EXT_PUBLIC_VLAN_ID`: integer in [0, 4094].
//     1..4094 are the IEEE 802.1Q usable tags; 0 means "untagged"
//     (used by kube-ovn provider networks whose carrier NIC IS the
//     VLAN — e.g. CloudSigma eu/dc1 where the L2 segment is a
//     CloudSigma VLAN by UUID, not an 802.1Q tag inside the VM).
//     4095 remains reserved.
//   - `EXT_PUBLIC_CIDR`: parseable IPv4/IPv6 CIDR.
//   - `EXT_PUBLIC_GATEWAY`: parseable IP address, AND inside the
//     `EXT_PUBLIC_CIDR` range (when both are valid).
//   - `EXT_NET_INTERFACE`: non-empty NIC token (letters/digits/-_./
//     allowed; bond0, enp1s0, eno1, etc.).
//
// Optional-but-overridable keys (EXT_NET_CIDR, EXT_NET_GATEWAY)
// are validated when present so an operator typo via `--set` is
// caught too.
//
// Multiple failures are collected + joined with `; ` so the operator
// sees every issue at once rather than fix-rerun-fail-loop.
func ValidatePresetValues(p Preset, envMap map[string]string) error {
	if envMap == nil {
		return nil
	}
	spec, ok := SpecFor(p)
	if !ok {
		return nil // unknown preset — caller validated earlier
	}

	var errs []string

	// Every required key: value must be non-whitespace.
	for _, k := range spec.RequiredKeys {
		v := envMap[k]
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Sprintf("%s: empty value (pass --set %s=<actual-value>)", k, k))
		}
	}

	// Per-key semantic rules.
	if v, ok := envMap["EXT_NET_VLAN_ID"]; ok && strings.TrimSpace(v) != "" {
		if msg := validateVLANID(v); msg != "" {
			errs = append(errs, "EXT_NET_VLAN_ID: "+msg)
		}
	}
	if v, ok := envMap["EXT_PUBLIC_VLAN_ID"]; ok && strings.TrimSpace(v) != "" {
		if msg := validateVLANID(v); msg != "" {
			errs = append(errs, "EXT_PUBLIC_VLAN_ID: "+msg)
		}
	}
	if v, ok := envMap["EXT_NET_INTERFACE"]; ok && strings.TrimSpace(v) != "" {
		if msg := validateNICName(v); msg != "" {
			errs = append(errs, "EXT_NET_INTERFACE: "+msg)
		}
	}
	// EXT_NET_ANCHOR_INTERFACE — same Linux NIC name rules as
	// EXT_NET_INTERFACE. The anchor unit's ExecStart embeds this
	// token; downstream apply.go shell-quotes it as defense-in-depth,
	// but catching a typo at preset time is the right place.
	if v, ok := envMap["EXT_NET_ANCHOR_INTERFACE"]; ok && strings.TrimSpace(v) != "" {
		if msg := validateNICName(v); msg != "" {
			errs = append(errs, "EXT_NET_ANCHOR_INTERFACE: "+msg)
		}
	}
	// CIDR + Gateway pairs — validate independently, then
	// cross-check that the gateway is inside the CIDR when both
	// parsed.
	publicCIDR, publicCIDRok := parseCIDRIfPresent(envMap, "EXT_PUBLIC_CIDR", &errs)
	checkGatewayInCIDR(envMap, "EXT_PUBLIC_GATEWAY", publicCIDR, publicCIDRok, &errs)
	extCIDR, extCIDRok := parseCIDRIfPresent(envMap, "EXT_NET_CIDR", &errs)
	checkGatewayInCIDR(envMap, "EXT_NET_GATEWAY", extCIDR, extCIDRok, &errs)

	// Per-node anchor IPs (productized per-node MetalLB L3 anchor
	// design). Validation only fires when EXT_NET_ANCHOR_IPS is set;
	// the empty default is OK on greenfield clusters that haven't
	// reached Phase D yet. KUBE_OVN_GW_NODES is treated as
	// authoritative — anchor hosts must be a subset; under
	// EXT_NET_ANCHOR_REQUIRED=true, every gw node MUST appear as an
	// anchor key (coverage check — partial coverage with REQUIRED=true
	// is the silent-failover bug captured by the 2026-05-30 incident
	// review).
	validateAnchorIPs(envMap, extCIDR, extCIDRok, &errs)
	validateAnchorSSHHosts(envMap, &errs)

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("%w: preset=%s; %s", ErrPresetInvalidValue, p, strings.Join(errs, "; "))
	}
	return nil
}

// validateVLANID returns an empty string when v is a valid VLAN
// ID, or an explanation otherwise. The accepted range is [0, 4094]:
// 1..4094 are the IEEE 802.1Q usable tags, and 0 means "untagged" —
// used by kube-ovn provider networks whose carrier interface is
// itself the VLAN (e.g. CloudSigma cloud VLANs attached to ens5,
// where EXT_NET_VLAN_ID=0 and the L2 segment is a CloudSigma VLAN by
// UUID, not an 802.1Q tag inside the VM). 4095 stays reserved.
func validateVLANID(v string) string {
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Sprintf("%q is not a number (VLAN IDs are 0..4094)", v)
	}
	if n < 0 || n > 4094 {
		return fmt.Sprintf("%d outside the 0..4094 range (0 = untagged; 4095 is reserved)", n)
	}
	return ""
}

// validateNICName performs a lightweight sanity check on Linux
// interface names. Accepts the shapes we see in production
// (bond0, enp1s0, eno1, enp94s0f0np0) without locking down to a
// strict regex — interface naming is wider than any one regex can
// catch. Rejects whitespace, control characters, and shell
// metacharacters.
func validateNICName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "empty interface name"
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
			// fine
		default:
			return fmt.Sprintf("%q contains an unsupported character %q (NIC names use [a-zA-Z0-9-_.:]) — typo?", v, r)
		}
	}
	if len(v) > 15 {
		// IFNAMSIZ in Linux is 16 (including the null terminator),
		// so usable length is 15. Catches an operator pasting a long
		// description by accident.
		return fmt.Sprintf("%q is %d chars; Linux IFNAMSIZ limits NIC names to 15", v, len(v))
	}
	return ""
}

// parseCIDRIfPresent looks up `key` in envMap; if present and
// non-whitespace, attempts to parse as a CIDR. Appends a typed
// error to `errs` on failure. Returns the parsed `*net.IPNet` and
// `ok=true` on success; `(nil, false)` on either absent or
// malformed (so callers know whether to skip the gateway-in-CIDR
// cross-check).
func parseCIDRIfPresent(envMap map[string]string, key string, errs *[]string) (*net.IPNet, bool) {
	v, ok := envMap[key]
	if !ok || strings.TrimSpace(v) == "" {
		return nil, false
	}
	_, cidr, err := net.ParseCIDR(strings.TrimSpace(v))
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %q is not a valid CIDR (e.g. 203.0.113.48/29)", key, v))
		return nil, false
	}
	return cidr, true
}

// checkGatewayInCIDR validates the `EXT_*_GATEWAY` key: must be a
// valid IP, and (when the partner CIDR parsed cleanly) must be
// inside that CIDR. A misconfigured gateway is one of the most
// expensive errors to debug post-install — catching it here saves
// the operator a doctor cycle.
func checkGatewayInCIDR(envMap map[string]string, key string, cidr *net.IPNet, cidrOK bool, errs *[]string) {
	v, ok := envMap[key]
	if !ok || strings.TrimSpace(v) == "" {
		return
	}
	ip := net.ParseIP(strings.TrimSpace(v))
	if ip == nil {
		*errs = append(*errs, fmt.Sprintf("%s: %q is not a valid IP address", key, v))
		return
	}
	if !cidrOK {
		return // partner CIDR malformed or absent — don't cascade
	}
	if !cidr.Contains(ip) {
		*errs = append(*errs, fmt.Sprintf("%s: %s is outside CIDR %s — gateway must be inside the network",
			key, v, cidr.String()))
	}
}

// validateAnchorIPs enforces the EXT_NET_ANCHOR_IPS schema:
//
//   - format `host=CIDR[,host=CIDR...]` (each pair has an `=`);
//   - every CIDR parses cleanly via net.ParseCIDR;
//   - every host is in KUBE_OVN_GW_NODES (anchor ⊆ gw);
//   - hosts are unique within the map;
//   - IPs are unique within the map (two hosts can't share an IP —
//     would create kernel duplicate-address conflict);
//   - every IP is inside EXT_NET_CIDR (anchors are bound on the
//     ext-cloud bridge; an IP outside the parent CIDR is a config
//     smell that won't be reachable from the broadcast domain);
//   - every anchor's prefix length equals EXT_NET_CIDR's prefix —
//     mixed masks announce a narrower broadcast domain than MetalLB
//     expects, silently degrading speaker election;
//   - if EXT_NET_ANCHOR_REQUIRED=true, EXT_NET_ANCHOR_IPS MUST be
//     non-empty AND every KUBE_OVN_GW_NODES entry MUST appear as a
//     key (coverage — partial coverage with REQUIRED=true is the
//     silent-failover bug from the 2026-05-30 review).
//
// The empty default is OK on greenfield clusters; only operators
// running through the Phase-D rollout fill this in.
//
// Multiple failures accumulate (no early-return) so the operator
// sees every problem at once.
func validateAnchorIPs(envMap map[string]string, extCIDR *net.IPNet, extCIDROK bool, errs *[]string) {
	raw := strings.TrimSpace(envMap["EXT_NET_ANCHOR_IPS"])
	required := envMap["EXT_NET_ANCHOR_REQUIRED"] == "true"

	if raw == "" {
		if required {
			*errs = append(*errs,
				"EXT_NET_ANCHOR_REQUIRED=true but EXT_NET_ANCHOR_IPS empty — "+
					"either populate the host=CIDR map or flip REQUIRED to false")
		}
		return
	}

	gwRaw, gwPresent := envMap["KUBE_OVN_GW_NODES"]
	gwRaw = strings.TrimSpace(gwRaw)
	if !gwPresent || gwRaw == "" {
		*errs = append(*errs,
			"EXT_NET_ANCHOR_IPS set but KUBE_OVN_GW_NODES empty — "+
				"anchors only bind on gateway nodes; populate KUBE_OVN_GW_NODES first")
		return
	}

	gwSet := make(map[string]struct{})
	for _, n := range strings.Split(gwRaw, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			gwSet[n] = struct{}{}
		}
	}
	// Defensive re-check: a value like ", , " trims non-empty but
	// produces zero hosts. The early-return above only catches
	// fully-empty; this catches the whitespace-only case.
	if len(gwSet) == 0 {
		*errs = append(*errs,
			"EXT_NET_ANCHOR_IPS set but KUBE_OVN_GW_NODES has no usable hosts (only whitespace?)")
		return
	}

	hostSeen := make(map[string]struct{})
	ipSeen := make(map[string]string) // normalized IP → first host that claimed it

	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		host, cidrStr, ok := strings.Cut(pair, "=")
		if !ok {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: %q missing '=' (expected host=CIDR, e.g. host5-a=100.64.0.11/16)",
				pair))
			continue
		}
		host = strings.TrimSpace(host)
		cidrStr = strings.TrimSpace(cidrStr)
		if host == "" {
			*errs = append(*errs, fmt.Sprintf("EXT_NET_ANCHOR_IPS: %q has empty host", pair))
			continue
		}
		if _, dup := hostSeen[host]; dup {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: host %q listed more than once (one anchor IP per host)", host))
			continue
		}
		hostSeen[host] = struct{}{}
		if _, ok := gwSet[host]; !ok {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: host %q not in KUBE_OVN_GW_NODES (anchors only bind on gw nodes)",
				host))
		}
		ip, anchorNet, err := net.ParseCIDR(cidrStr)
		if err != nil {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: %q invalid CIDR (e.g. 100.64.0.11/16): %v", cidrStr, err))
			continue
		}
		// Duplicate IP check. net.ParseCIDR returns the host bits in
		// the first return value, so srv5=.11/16 and srv6=.11/24
		// collide on the IP — exactly what we want.
		ipKey := ip.String()
		if firstHost, dup := ipSeen[ipKey]; dup {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: IP %s claimed by both %q and %q (each anchor IP must be unique — kernel would reject duplicate-address)",
				ipKey, firstHost, host))
		} else {
			ipSeen[ipKey] = host
		}
		// In-CIDR check. Skip if EXT_NET_CIDR didn't parse (don't
		// cascade the parent error).
		if extCIDROK && !extCIDR.Contains(ip) {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: anchor %s for host %q is outside EXT_NET_CIDR %s",
				ipKey, host, extCIDR.String()))
		}
		// Prefix-sanity check. A /24 anchor in a /16 parent announces
		// a narrower broadcast domain than MetalLB expects; silently
		// degrades. Error, not warn.
		if extCIDROK {
			anchorOnes, _ := anchorNet.Mask.Size()
			parentOnes, _ := extCIDR.Mask.Size()
			if anchorOnes != parentOnes {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_ANCHOR_IPS: anchor %s for host %q has prefix /%d but EXT_NET_CIDR is /%d — anchor mask must match the parent network",
					ipKey, host, anchorOnes, parentOnes))
			}
		}
	}

	// Coverage check (REQUIRED=true): every gw node must appear as an
	// anchor key. Captures the silent-failover bug from the
	// 2026-05-30 review — MetalLB elects a speaker on an unanchored
	// node, the speaker has no source IP for its GARP, tenant traffic
	// drops. We only flag here when REQUIRED=true; partial coverage
	// during rollout (REQUIRED=false) is intentionally allowed.
	if required {
		var missing []string
		for gw := range gwSet {
			if _, ok := hostSeen[gw]; !ok {
				missing = append(missing, gw)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_IPS: EXT_NET_ANCHOR_REQUIRED=true but gateway node(s) %s have no anchor IP — every host in KUBE_OVN_GW_NODES must appear as a key in EXT_NET_ANCHOR_IPS (MetalLB failover to an unanchored gw node silently drops tenant traffic). Either add anchors for the missing host(s) or remove them from KUBE_OVN_GW_NODES first",
				strings.Join(missing, ", ")))
		}
	}
}

// validateAnchorSSHHosts enforces the EXT_NET_ANCHOR_SSH_HOSTS schema:
//
//   - format `node=host[,node=host...]` (each pair has an `=`);
//   - every node is in KUBE_OVN_GW_NODES (same cross-check as anchors);
//   - nodes are unique within the map;
//   - host is non-empty, free of whitespace or '=' (bare IP, FQDN, or
//     ssh_config alias). We deliberately don't require the host to
//     parse as a literal IP — operators may use FQDNs or aliases.
//
// Empty map is the default — falls back to the operator's
// ~/.ssh/config alias path for Kubernetes node names. Partial maps
// are valid (mapped nodes get the override; unmapped fall through to
// the legacy path).
func validateAnchorSSHHosts(envMap map[string]string, errs *[]string) {
	raw := strings.TrimSpace(envMap["EXT_NET_ANCHOR_SSH_HOSTS"])
	if raw == "" {
		return
	}
	gwSet := make(map[string]struct{})
	for _, n := range strings.Split(strings.TrimSpace(envMap["KUBE_OVN_GW_NODES"]), ",") {
		if n = strings.TrimSpace(n); n != "" {
			gwSet[n] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		node, host, ok := strings.Cut(pair, "=")
		if !ok {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_SSH_HOSTS: %q missing '=' (expected node=host, e.g. host5-a=203.0.113.52)", pair))
			continue
		}
		node = strings.TrimSpace(node)
		host = strings.TrimSpace(host)
		if node == "" {
			*errs = append(*errs, fmt.Sprintf("EXT_NET_ANCHOR_SSH_HOSTS: %q has empty node", pair))
			continue
		}
		if host == "" {
			*errs = append(*errs, fmt.Sprintf("EXT_NET_ANCHOR_SSH_HOSTS: node %q has empty host", node))
			continue
		}
		if strings.ContainsAny(host, " \t=") {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_SSH_HOSTS: host %q for node %q contains whitespace or '=' (expected bare IP, FQDN, or ssh_config alias)",
				host, node))
			continue
		}
		if _, dup := seen[node]; dup {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_ANCHOR_SSH_HOSTS: node %q listed more than once", node))
			continue
		}
		seen[node] = struct{}{}
		// Cross-check against gw nodes only when gw set is populated.
		// An empty gw set already triggers its own error from
		// validateAnchorIPs; don't double-report here.
		if len(gwSet) > 0 {
			if _, ok := gwSet[node]; !ok {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_ANCHOR_SSH_HOSTS: node %q not in KUBE_OVN_GW_NODES", node))
			}
		}
	}
}

// ValidateAnchorConfig runs the anchor-related validators against an
// env-map: EXT_NET_ANCHOR_INTERFACE, EXT_NET_ANCHOR_IPS (with the
// extCIDR-in-range / prefix-sanity / duplicate-IP / REQUIRED-coverage
// checks), and EXT_NET_ANCHOR_SSH_HOSTS.
//
// Used by `bootstrap anchors apply` and `bootstrap doctor anchors` to
// enforce the same guarantees the `bootstrap init` preset validator
// gives — closes the gap where a hand-edited cluster-config.env was
// only re-checked at preset-validation time, never at CLI run time
// against an existing cluster overlay.
//
// Unlike ValidatePresetValues this is scoped to anchor concerns only;
// it does NOT re-validate VLAN_ID / EXT_NET_CIDR / GATEWAY because
// those have their own gate at preset time and an operator running
// `anchors apply` against an existing cluster should not be re-
// rejected for unrelated drift. It DOES parse EXT_NET_CIDR (for the
// in-range / prefix-mask cross-check) but only surfaces parse errors
// that affect anchor validation.
func ValidateAnchorConfig(envMap map[string]string) error {
	var errs []string
	if v, ok := envMap["EXT_NET_ANCHOR_INTERFACE"]; ok && strings.TrimSpace(v) != "" {
		if msg := validateNICName(v); msg != "" {
			errs = append(errs, "EXT_NET_ANCHOR_INTERFACE: "+msg)
		}
	}
	extCIDR, extCIDROK := parseCIDRIfPresent(envMap, "EXT_NET_CIDR", &errs)
	validateAnchorIPs(envMap, extCIDR, extCIDROK, &errs)
	validateAnchorSSHHosts(envMap, &errs)
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("%w: %s", ErrPresetInvalidValue, strings.Join(errs, "; "))
	}
	return nil
}

// PresetKustomizations returns the ordered Kustomization name list
// for the named preset. Used by the M4-T10 scaffold step to write
// `clusters/<name>/kustomization.yaml` with the right resource list.
// Returns nil + false for unknown presets (caller should validate
// first).
func PresetKustomizations(p Preset) ([]string, bool) {
	spec, ok := SpecFor(p)
	if !ok {
		return nil, false
	}
	out := make([]string, len(spec.Kustomizations))
	copy(out, spec.Kustomizations)
	return out, true
}
