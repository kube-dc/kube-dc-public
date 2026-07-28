package clusterinit

import (
	"strings"
	"testing"
)

// Derivation happy path: a cloud+public-vlan init with gateway nodes
// gets per-node anchors VIP+1.., the anchor VLAN, the METALLB
// interface, and IPAM reservation covering VIP..last-anchor.
func TestDerivePublicAnchor_HappyPath(t *testing.T) {
	env, err := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":       "300",
		"EXT_NET_INTERFACE":     "bond0",
		"EXT_PUBLIC_VLAN_ID":    "301",
		"EXT_PUBLIC_CIDR":       "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":    "192.0.2.1",
		"METALLB_FLOATING_IP":   "192.0.2.2",
		"KUBE_OVN_GW_NODES":     "node1,node2,node3",
		"KUBE_OVN_MASTER_NODES": "198.51.100.11,198.51.100.12,198.51.100.13",
	})
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if got, want := env["EXT_NET_PUBLIC_ANCHOR_IPS"], "node1=192.0.2.3/28,node2=192.0.2.4/28,node3=192.0.2.5/28"; got != want {
		t.Errorf("ANCHOR_IPS = %q, want %q", got, want)
	}
	if got, want := env["EXT_NET_PUBLIC_ANCHOR_VLAN"], "301"; got != want {
		t.Errorf("ANCHOR_VLAN = %q, want %q", got, want)
	}
	if got, want := env["EXT_NET_ANCHOR_VLAN"], "300"; got != want {
		t.Errorf("EXT_NET_ANCHOR_VLAN = %q, want %q (cloud VLAN follows EXT_NET_VLAN_ID)", got, want)
	}
	if got, want := env["METALLB_INTERFACE"], "ext-pub-anchor"; got != want {
		t.Errorf("METALLB_INTERFACE = %q, want %q", got, want)
	}
	if got, want := env["EXT_PUBLIC_EXCLUDE_IPS_1"], "192.0.2.2..192.0.2.5"; got != want {
		t.Errorf("EXCLUDE_IPS_1 = %q, want %q", got, want)
	}
	if got, want := env["EXT_PUBLIC_EXCLUDE_IPS_2"], "192.0.2.1"; got != want {
		t.Errorf("EXCLUDE_IPS_2 = %q, want %q", got, want)
	}
	// The derived map must pass its own validation.
	if err := ValidatePresetValues(PresetCloudPublicVLAN, env); err != nil {
		t.Errorf("derived env failed validation: %v", err)
	}
}

// The gateway sits inside the would-be anchor run: derivation must
// step over it, not hand it to a node.
func TestDerivePublicAnchor_SkipsGateway(t *testing.T) {
	env := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":              "192.0.2.3",
		"METALLB_FLOATING_IP":             "192.0.2.2",
		"KUBE_OVN_GW_NODES":               "node1,node2",
	}
	derivePublicAnchorEnv(env)
	if got, want := env["EXT_NET_PUBLIC_ANCHOR_IPS"], "node1=192.0.2.4/28,node2=192.0.2.5/28"; got != want {
		t.Errorf("ANCHOR_IPS = %q, want %q", got, want)
	}
}

// Operator-set values are never overwritten by derivation.
func TestDerivePublicAnchor_OperatorWins(t *testing.T) {
	env := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_NET_PUBLIC_ANCHOR_IPS":       "node1=192.0.2.9/28",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
		"METALLB_FLOATING_IP":             "192.0.2.2",
		"METALLB_INTERFACE":               "br-custom",
		"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.1..192.0.2.9",
		"KUBE_OVN_GW_NODES":               "node1",
	}
	derivePublicAnchorEnv(env)
	if got := env["EXT_NET_PUBLIC_ANCHOR_IPS"]; got != "node1=192.0.2.9/28" {
		t.Errorf("operator ANCHOR_IPS overwritten: %q", got)
	}
	if got := env["METALLB_INTERFACE"]; got != "br-custom" {
		t.Errorf("operator METALLB_INTERFACE overwritten: %q", got)
	}
	if got := env["EXT_PUBLIC_EXCLUDE_IPS_1"]; got != "192.0.2.1..192.0.2.9" {
		t.Errorf("operator EXCLUDE_IPS_1 overwritten: %q", got)
	}
}

// Anchors would run past the subnet: derive nothing (half a map is
// worse than none — validation then fails loudly).
func TestDerivePublicAnchor_SubnetTooSmall(t *testing.T) {
	env := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/29", // usable .1-.6
		"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
		"METALLB_FLOATING_IP":             "192.0.2.4",
		"KUBE_OVN_GW_NODES":               "node1,node2,node3",
	}
	derivePublicAnchorEnv(env)
	if got := env["EXT_NET_PUBLIC_ANCHOR_IPS"]; got != "" {
		t.Errorf("expected empty ANCHOR_IPS for undersized subnet, got %q", got)
	}
}

// No public VLAN at all → derivation is a no-op and the empty keys
// pass validation (internal-only topologies).
func TestDerivePublicAnchor_NoPublicVLAN(t *testing.T) {
	env, err := EnvMapFor(PresetInternalOnly, map[string]string{
		"EXT_NET_VLAN_ID":   "300",
		"EXT_NET_INTERFACE": "bond0",
	})
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if env["EXT_NET_PUBLIC_ANCHOR_IPS"] != "" || env["EXT_NET_PUBLIC_ANCHOR_VLAN"] != "" {
		t.Errorf("derivation ran without a public VLAN: IPS=%q VLAN=%q",
			env["EXT_NET_PUBLIC_ANCHOR_IPS"], env["EXT_NET_PUBLIC_ANCHOR_VLAN"])
	}
	if err := ValidatePresetValues(PresetInternalOnly, env); err != nil {
		t.Errorf("internal-only env failed validation: %v", err)
	}
}

// L2 VIP configured but no way to derive anchors (no gw nodes, no
// explicit map): loud validation error, not a silent broken VIP.
func TestValidatePublicAnchor_L2VIPWithoutAnchorsFails(t *testing.T) {
	env, err := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":     "300",
		"EXT_NET_INTERFACE":   "bond0",
		"EXT_PUBLIC_VLAN_ID":  "301",
		"EXT_PUBLIC_CIDR":     "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":  "192.0.2.1",
		"METALLB_FLOATING_IP": "192.0.2.2",
		// no KUBE_OVN_GW_NODES → derivation cannot run
	})
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	err = ValidatePresetValues(PresetCloudPublicVLAN, env)
	if err == nil || !strings.Contains(err.Error(), "EXT_NET_PUBLIC_ANCHOR_IPS") {
		t.Errorf("expected anchor-missing validation error, got: %v", err)
	}
}

// An anchor outside the IPAM reservation is the tenant-LRP collision
// class — must be rejected.
func TestValidatePublicAnchor_UncoveredAnchorFails(t *testing.T) {
	env := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_NET_PUBLIC_ANCHOR_VLAN":      "301",
		"EXT_NET_PUBLIC_ANCHOR_IPS":       "node1=192.0.2.8/28",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
		"METALLB_FLOATING_IP":             "192.0.2.2",
		"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.1..192.0.2.5",
		"KUBE_OVN_GW_NODES":               "node1",
	}
	var errs []string
	validatePublicAnchor(env, &errs)
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "not covered by any exclude range") {
		t.Errorf("expected reservation-coverage error, got: %q", joined)
	}
}

// Anchor equal to the VIP or gateway, wrong prefix, unknown node,
// duplicate IP — each individually rejected.
func TestValidatePublicAnchor_ShapeErrors(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
			"EXT_PUBLIC_VLAN_ID":              "301",
			"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
			"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
			"METALLB_FLOATING_IP":             "192.0.2.2",
			"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.1..192.0.2.6",
			"KUBE_OVN_GW_NODES":               "node1,node2",
		}
	}
	cases := []struct {
		name    string
		anchors string
		wantSub string
	}{
		{"vip-collision", "node1=192.0.2.2/28", "MetalLB floating IP itself"},
		{"gw-collision", "node1=192.0.2.1/28", "public gateway address"},
		{"wrong-prefix", "node1=192.0.2.3/24", "must match the public network"},
		{"unknown-node", "node9=192.0.2.3/28", "not in KUBE_OVN_GW_NODES"},
		{"duplicate-ip", "node1=192.0.2.3/28,node2=192.0.2.3/28", "claimed by both"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base()
			env["EXT_NET_PUBLIC_ANCHOR_IPS"] = tc.anchors
			var errs []string
			validatePublicAnchor(env, &errs)
			if !strings.Contains(strings.Join(errs, "; "), tc.wantSub) {
				t.Errorf("want error containing %q, got: %v", tc.wantSub, errs)
			}
		})
	}
}

func TestDerivePublicAnchor_CloudVIPLeavesPublicAnchorIdle(t *testing.T) {
	o := &InitOptions{Preset: PresetCloudPublicVLAN, Sets: map[string]string{
		"EXT_NET_VLAN_ID":          "300",
		"EXT_NET_INTERFACE":        "bond0",
		"EXT_PUBLIC_VLAN_ID":       "301",
		"EXT_PUBLIC_CIDR":          "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":       "192.0.2.1",
		"EXT_PUBLIC_EXCLUDE_IPS_1": "192.0.2.1..192.0.2.5",
		"EXT_PUBLIC_EXCLUDE_IPS_2": "192.0.2.15",
		"METALLB_FLOATING_IP":      "100.65.0.20",
		"METALLB_INTERFACE":        "br-ext-cloud",
		"KUBE_OVN_MASTER_NODES":    "198.51.100.11",
	}}
	env, err := EnvMapFor(o.Preset, o.Sets)
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if got := env["EXT_NET_PUBLIC_ANCHOR_IPS"]; got != "" {
		t.Fatalf("public anchors derived for an ext-cloud VIP: %q", got)
	}
	if got := env["EXT_NET_PUBLIC_ANCHOR_VLAN"]; got != "" {
		t.Fatalf("public anchor VLAN activated for an ext-cloud VIP: %q", got)
	}
	if got := env["METALLB_INTERFACE"]; got != "br-ext-cloud" {
		t.Fatalf("METALLB_INTERFACE moved off ext-cloud: %q", got)
	}
	if err := ValidatePresetRequiredKeys(o); err != nil {
		t.Fatalf("complete ext-cloud VIP config rejected: %v", err)
	}
}

func TestDerivePublicAnchor_BGPReservesVIPWithoutHostAnchors(t *testing.T) {
	o := &InitOptions{Preset: PresetCloudPublicVLAN, Sets: map[string]string{
		"EXT_NET_VLAN_ID":          "300",
		"EXT_NET_INTERFACE":        "bond0",
		"EXT_PUBLIC_VLAN_ID":       "301",
		"EXT_PUBLIC_CIDR":          "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":       "192.0.2.1",
		"METALLB_MODE":             "bgp",
		"METALLB_FLOATING_IP":      "192.0.2.2",
		"METALLB_BGP_LOCAL_ASN":    "64512",
		"METALLB_BGP_PEER_ASN":     "64513",
		"METALLB_BGP_PEER_ADDRESS": "198.51.100.1",
		"KUBE_OVN_MASTER_NODES":    "198.51.100.11",
	}}
	env, err := EnvMapFor(o.Preset, o.Sets)
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if got := env["EXT_NET_PUBLIC_ANCHOR_IPS"]; got != "" {
		t.Fatalf("BGP unexpectedly derived host anchors: %q", got)
	}
	if got := env["EXT_NET_PUBLIC_ANCHOR_VLAN"]; got != "" {
		t.Fatalf("BGP unexpectedly activated the public L2 port: %q", got)
	}
	if got := env["EXT_PUBLIC_EXCLUDE_IPS_1"]; got != "192.0.2.2" {
		t.Fatalf("BGP VIP reservation = %q, want 192.0.2.2", got)
	}
	if got := env["EXT_PUBLIC_EXCLUDE_IPS_2"]; got != "192.0.2.1" {
		t.Fatalf("BGP gateway reservation = %q, want 192.0.2.1", got)
	}
	if err := ValidatePresetRequiredKeys(o); err != nil {
		t.Fatalf("complete BGP config rejected: %v", err)
	}
}

func TestValidatePublicAnchor_RejectsStaleL2StateAfterModeChange(t *testing.T) {
	base := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_NET_PUBLIC_ANCHOR_VLAN":      "301",
		"EXT_NET_PUBLIC_ANCHOR_IPS":       "node1=192.0.2.3/28",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
		"METALLB_FLOATING_IP":             "192.0.2.2",
		"METALLB_INTERFACE":               "ext-pub-anchor",
		"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.1..192.0.2.3",
		"KUBE_OVN_GW_NODES":               "node1",
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]string)
	}{
		{"bgp", func(e map[string]string) { e["METALLB_MODE"] = "bgp" }},
		{"vip-moved-to-ext-cloud", func(e map[string]string) { e["METALLB_FLOATING_IP"] = "100.65.0.20" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := make(map[string]string, len(base))
			for k, v := range base {
				env[k] = v
			}
			tc.edit(env)
			var errs []string
			validatePublicAnchor(env, &errs)
			if got := strings.Join(errs, "; "); !strings.Contains(got, "clear both keys") {
				t.Fatalf("stale public-L2 host state not rejected: %s", got)
			}
		})
	}
}

func TestValidatePublicAnchor_RequiresEveryAnnouncer(t *testing.T) {
	env := map[string]string{
		"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
		"EXT_NET_PUBLIC_ANCHOR_VLAN":      "301",
		"EXT_NET_PUBLIC_ANCHOR_IPS":       "node1=192.0.2.3/28",
		"EXT_PUBLIC_VLAN_ID":              "301",
		"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
		"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
		"METALLB_MODE":                    "l2",
		"METALLB_FLOATING_IP":             "192.0.2.2",
		"METALLB_INTERFACE":               "ext-pub-anchor",
		"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.1..192.0.2.5",
		"KUBE_OVN_GW_NODES":               "node1,node2",
	}
	var errs []string
	validatePublicAnchor(env, &errs)
	if got := strings.Join(errs, "; "); !strings.Contains(got, "node2 have no public anchor") {
		t.Fatalf("missing announcer coverage not rejected: %s", got)
	}
}

func TestValidatePublicAnchor_RejectsUnusableAddressesAndWiringDrift(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"EXT_NET_ANCHOR_INTERFACE":        "br-ext-cloud",
			"EXT_NET_PUBLIC_ANCHOR_INTERFACE": "ext-pub-anchor",
			"EXT_NET_PUBLIC_ANCHOR_VLAN":      "301",
			"EXT_NET_PUBLIC_ANCHOR_IPS":       "node1=192.0.2.3/28",
			"EXT_PUBLIC_VLAN_ID":              "301",
			"EXT_PUBLIC_CIDR":                 "192.0.2.0/28",
			"EXT_PUBLIC_GATEWAY":              "192.0.2.1",
			"METALLB_MODE":                    "l2",
			"METALLB_FLOATING_IP":             "192.0.2.2",
			"METALLB_INTERFACE":               "ext-pub-anchor",
			"EXT_PUBLIC_EXCLUDE_IPS_1":        "192.0.2.0..192.0.2.5",
			"KUBE_OVN_GW_NODES":               "node1",
		}
	}
	cases := []struct {
		name string
		edit func(map[string]string)
		want string
	}{
		{"network-anchor", func(e map[string]string) { e["EXT_NET_PUBLIC_ANCHOR_IPS"] = "node1=192.0.2.0/28" }, "network/broadcast"},
		{"broadcast-vip", func(e map[string]string) { e["METALLB_FLOATING_IP"] = "192.0.2.15" }, "network/broadcast"},
		{"missing-parent-bridge", func(e map[string]string) { e["EXT_NET_ANCHOR_INTERFACE"] = "" }, "required as the parent OVS bridge"},
		{"interface-drift", func(e map[string]string) { e["METALLB_INTERFACE"] = "br-wrong" }, "must equal EXT_NET_PUBLIC_ANCHOR_INTERFACE"},
		{"vlan-drift", func(e map[string]string) { e["EXT_NET_PUBLIC_ANCHOR_VLAN"] = "302" }, "must equal EXT_PUBLIC_VLAN_ID"},
		{"gateway-is-vip", func(e map[string]string) { e["EXT_PUBLIC_GATEWAY"] = "192.0.2.2" }, "must differ from EXT_PUBLIC_GATEWAY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base()
			tc.edit(env)
			var errs []string
			validatePublicAnchor(env, &errs)
			if got := strings.Join(errs, "; "); !strings.Contains(got, tc.want) {
				t.Fatalf("want %q, got %s", tc.want, got)
			}
		})
	}
}
