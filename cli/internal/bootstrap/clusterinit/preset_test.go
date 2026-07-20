package clusterinit

import (
	"errors"
	"strings"
	"testing"
)

func TestSpecFor_AllPresetsRegistered(t *testing.T) {
	// Every Preset constant must have a registered PresetSpec —
	// SpecFor returning false at runtime would mean BuildPlan
	// silently failed for an operator-passed --preset value. This
	// test is the regression guard.
	for _, p := range AllPresets {
		if _, ok := SpecFor(p); !ok {
			t.Errorf("preset %q has no PresetSpec registered", p)
		}
	}
}

func TestEnvMapFor_InternalOnly(t *testing.T) {
	// internal-only requires VLAN_ID + INTERFACE; no public VLAN
	// block; DEFAULT_*_NETWORK_TYPE all cloud.
	got, err := EnvMapFor(PresetInternalOnly, map[string]string{
		"EXT_NET_VLAN_ID":   "1100",
		"EXT_NET_INTERFACE": "eno1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for k, want := range map[string]string{
		"EXT_NET_NAME":                "ext-cloud",
		"EXT_NET_TYPE":                "cloud",
		"EXT_NET_VLAN_ID":             "1100",
		"EXT_NET_INTERFACE":           "eno1",
		"DEFAULT_GW_NETWORK_TYPE":     "cloud",
		"DEFAULT_EIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_FIP_NETWORK_TYPE":    "cloud",
		"DEFAULT_SVC_LB_NETWORK_TYPE": "cloud",
		"POD_CIDR":                    "10.100.0.0/16",
		"PROM_STORAGE":                "20Gi",
	} {
		if got[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, got[k], want)
		}
	}
	// internal-only must NOT have EXT_PUBLIC_* defaults.
	for _, k := range []string{"EXT_PUBLIC_VLAN_ID", "EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY"} {
		if _, present := got[k]; present {
			t.Errorf("internal-only leaked %s into env", k)
		}
	}
}

func TestEnvMapFor_CloudPublicVLAN_Atlantis(t *testing.T) {
	// The actual atlantis flag set: --preset=cloud+public-vlan
	// + the 5 required --set values mapped from the README's CGNAT
	// + public-IP topology.
	got, err := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":    "1103",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100", // private VLAN per README
		"EXT_PUBLIC_CIDR":    "203.0.113.0/29",
		"EXT_PUBLIC_GATEWAY": "203.0.113.49",
	})
	if err != nil {
		t.Fatalf("atlantis env should validate, got %v", err)
	}
	// All EXT_* + DEFAULT_* + universal keys present.
	for k, want := range map[string]string{
		"EXT_NET_NAME":                "ext-cloud",
		"EXT_NET_VLAN_ID":             "1103",
		"EXT_PUBLIC_VLAN_ID":          "1100",
		"EXT_PUBLIC_CIDR":             "203.0.113.0/29",
		"DEFAULT_EIP_NETWORK_TYPE":    "public",
		"DEFAULT_FIP_NETWORK_TYPE":    "public",
		"DEFAULT_SVC_LB_NETWORK_TYPE": "public",
		"POD_CIDR":                    "10.100.0.0/16",
	} {
		if got[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, got[k], want)
		}
	}
}

func TestEnvMapFor_MissingRequired_Errors(t *testing.T) {
	cases := []struct {
		name        string
		preset      Preset
		sets        map[string]string
		wantMissing []string
	}{
		{
			name:        "internal-only missing both",
			preset:      PresetInternalOnly,
			sets:        nil,
			wantMissing: []string{"EXT_NET_INTERFACE", "EXT_NET_VLAN_ID"},
		},
		{
			name:   "internal-only missing one",
			preset: PresetInternalOnly,
			sets: map[string]string{
				"EXT_NET_VLAN_ID": "1100",
			},
			wantMissing: []string{"EXT_NET_INTERFACE"},
		},
		{
			name:   "cloud+public-vlan missing public block",
			preset: PresetCloudPublicVLAN,
			sets: map[string]string{
				"EXT_NET_VLAN_ID":   "1103",
				"EXT_NET_INTERFACE": "bond0",
			},
			wantMissing: []string{"EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY", "EXT_PUBLIC_VLAN_ID"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := EnvMapFor(tc.preset, tc.sets)
			if !errors.Is(err, ErrPresetMissingRequired) {
				t.Fatalf("expected ErrPresetMissingRequired, got %v", err)
			}
			for _, k := range tc.wantMissing {
				if !strings.Contains(err.Error(), k) {
					t.Errorf("error %q missing %q", err.Error(), k)
				}
			}
			// Error should also identify the preset by name.
			if !strings.Contains(err.Error(), string(tc.preset)) {
				t.Errorf("error %q missing preset name %q", err.Error(), tc.preset)
			}
		})
	}
}

func TestEnvMapFor_CustomPreset_NoRequiredKeys(t *testing.T) {
	// Custom preset has no RequiredKeys — operator vouches by
	// picking `custom`. Pass empty --set and assert no error.
	got, err := EnvMapFor(PresetCustom, nil)
	if err != nil {
		t.Fatalf("custom preset with no --set should validate, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("custom preset should ship no defaults, got %d keys", len(got))
	}
}

func TestEnvMapFor_SetOverridesDefaults(t *testing.T) {
	// --set EXT_NET_CIDR=10.0.0.0/16 must override the preset's
	// default 100.65.0.0/16.
	got, err := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":    "1103",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR":    "203.0.113.0/29",
		"EXT_PUBLIC_GATEWAY": "203.0.113.49",
		// Override the default CIDR.
		"EXT_NET_CIDR": "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["EXT_NET_CIDR"] != "10.0.0.0/16" {
		t.Errorf("--set didn't override default: got %q, want %q", got["EXT_NET_CIDR"], "10.0.0.0/16")
	}
}

func TestEnvMapFor_SetCanIntroduceNewKey(t *testing.T) {
	// Presets are defaults, not allow-lists. An operator can
	// introduce arbitrary cluster-config.env keys via --set (the
	// SCREAMING_SNAKE_CASE check in options.go's validateSets
	// catches typos; semantic validity is the cluster-config
	// parser's concern).
	got, _ := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":    "1103",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR":    "203.0.113.0/29",
		"EXT_PUBLIC_GATEWAY": "203.0.113.49",
		"CUSTOM_TUNABLE":     "yes",
	})
	if got["CUSTOM_TUNABLE"] != "yes" {
		t.Errorf("--set CUSTOM_TUNABLE not propagated: got %q", got["CUSTOM_TUNABLE"])
	}
}

func TestPresetKustomizations(t *testing.T) {
	cases := []struct {
		preset Preset
		want   []string
	}{
		{
			PresetInternalOnly,
			[]string{"infra-cni", "infra-core", "infra-object-storage", "platform", "addons"},
		},
		{
			PresetCloudVLAN,
			[]string{"infra-cni", "infra-core", "infra-object-storage", "platform", "addons"},
		},
		{
			PresetCloudPublicVLAN,
			[]string{"infra-cni", "infra-core", "infra-public-network", "infra-object-storage", "platform", "addons"},
		},
		{
			PresetCustom,
			[]string{"infra-cni", "infra-core", "infra-public-network", "infra-object-storage", "platform", "addons"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.preset), func(t *testing.T) {
			got, ok := PresetKustomizations(tc.preset)
			if !ok {
				t.Fatalf("PresetKustomizations(%s) returned !ok", tc.preset)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got %v)", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Errorf("kustomization[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestPresetKustomizations_PublicVLANHasInfraPublicNetwork(t *testing.T) {
	// Critical invariant: only cloud+public-vlan includes
	// infra-public-network. Guard against a future preset edit that
	// accidentally enables it for internal-only (would attempt to
	// configure a public VLAN the cluster doesn't have, breaking
	// reconcile).
	for _, p := range []Preset{PresetInternalOnly, PresetCloudVLAN} {
		ks, _ := PresetKustomizations(p)
		for _, k := range ks {
			if k == "infra-public-network" {
				t.Errorf("preset %s must NOT include infra-public-network; got %v", p, ks)
			}
		}
	}
	// And cloud+public-vlan MUST include it.
	ks, _ := PresetKustomizations(PresetCloudPublicVLAN)
	has := false
	for _, k := range ks {
		if k == "infra-public-network" {
			has = true
		}
	}
	if !has {
		t.Errorf("cloud+public-vlan must include infra-public-network; got %v", ks)
	}
}

func TestValidatePresetValues_EmptyRequiredValue_Rejected(t *testing.T) {
	// Review-pass P1/P2: an empty value for a required key must be
	// rejected. Otherwise --set=EXT_PUBLIC_CIDR= would pass the
	// required-key gate (key present) and T10 would write it to
	// cluster-config.env producing an unbootable cluster.
	cases := []struct {
		name  string
		value string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tab", "\t"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sets := map[string]string{
				"EXT_NET_VLAN_ID":    "1103",
				"EXT_NET_INTERFACE":  "bond0",
				"EXT_PUBLIC_VLAN_ID": "1100",
				"EXT_PUBLIC_CIDR":    tc.value,
				"EXT_PUBLIC_GATEWAY": "203.0.113.49",
			}
			envMap, err := EnvMapFor(PresetCloudPublicVLAN, sets)
			if err != nil {
				t.Fatalf("EnvMapFor unexpected err: %v", err)
			}
			err = ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if !errors.Is(err, ErrPresetInvalidValue) {
				t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
			}
			if !strings.Contains(err.Error(), "EXT_PUBLIC_CIDR") {
				t.Errorf("error should name the empty key; got %v", err)
			}
			if !strings.Contains(err.Error(), "empty value") {
				t.Errorf("error should explain empty-value; got %v", err)
			}
		})
	}
}

func TestValidatePresetValues_VLANID(t *testing.T) {
	base := func(extVLAN string) map[string]string {
		return map[string]string{
			"EXT_NET_VLAN_ID":    extVLAN,
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "203.0.113.48/29",
			"EXT_PUBLIC_GATEWAY": "203.0.113.49",
		}
	}
	cases := []struct {
		name    string
		vlan    string
		wantErr bool
		wantSub string
	}{
		{"valid mid-range", "1103", false, ""},
		{"valid low edge", "1", false, ""},
		{"valid high edge", "4094", false, ""},
		// VLAN 0 accepted as "untagged": kube-ovn provider networks
		// whose carrier interface is itself the VLAN (e.g. CloudSigma
		// eu/dc1 uses ens5 with EXT_NET_VLAN_ID=0 — the L2 segment is
		// a CloudSigma VLAN by UUID, not an 802.1Q tag inside the VM).
		// Prior test asserted rejection; widened alongside the
		// validateVLANID range change in preset.go.
		{"untagged 0", "0", false, ""},
		{"reserved 4095", "4095", true, "outside"},
		{"too high", "9999", true, "outside"},
		{"non-numeric", "abc", true, "not a number"},
		{"negative", "-1", true, "outside"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, _ := EnvMapFor(PresetCloudPublicVLAN, base(tc.vlan))
			err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if !strings.Contains(err.Error(), "EXT_NET_VLAN_ID") {
					t.Errorf("error should name VLAN_ID key; got %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestValidatePresetValues_CIDR(t *testing.T) {
	base := func(cidr, gw string) map[string]string {
		return map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    cidr,
			"EXT_PUBLIC_GATEWAY": gw,
		}
	}
	cases := []struct {
		name     string
		cidr, gw string
		wantErr  bool
		wantSub  string
	}{
		{"valid pair", "203.0.113.48/29", "203.0.113.49", false, ""},
		{"valid /24", "10.0.0.0/24", "10.0.0.1", false, ""},
		{"valid ipv6", "2a0c:d880:1100::/64", "2a0c:d880:1100::1", false, ""},
		{"malformed CIDR", "203.0.113.48", "203.0.113.49", true, "not a valid CIDR"},
		{"bad CIDR mask", "10.0.0.0/99", "10.0.0.1", true, "not a valid CIDR"},
		{"gateway outside CIDR", "10.0.0.0/24", "10.0.1.1", true, "outside CIDR"},
		{"gateway not an IP", "10.0.0.0/24", "not-an-ip", true, "not a valid IP"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, _ := EnvMapFor(PresetCloudPublicVLAN, base(tc.cidr, tc.gw))
			err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestValidatePresetValues_NICName(t *testing.T) {
	base := func(iface string) map[string]string {
		return map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  iface,
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "203.0.113.48/29",
			"EXT_PUBLIC_GATEWAY": "203.0.113.49",
		}
	}
	cases := []struct {
		name    string
		iface   string
		wantErr bool
		wantSub string
	}{
		{"bond0", "bond0", false, ""},
		{"enp1s0", "enp1s0", false, ""},
		{"long production name", "enp94s0f0np0", false, ""},
		{"with dot", "eth0.100", false, ""},
		{"with colon", "br-ext:cloud", false, ""},
		{"shell metachar reject", "bond0;rm", true, "unsupported character"},
		{"space reject", "bond 0", true, "unsupported character"},
		{"too long", "this-name-is-way-too-long-for-linux", true, "IFNAMSIZ"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, _ := EnvMapFor(PresetCloudPublicVLAN, base(tc.iface))
			err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestValidatePresetValues_CollectsMultipleErrors(t *testing.T) {
	// Empty CIDR + bad VLAN — operator should see both at once,
	// not fix-rerun-discover-the-next-one.
	envMap, _ := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":    "9999",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR":    "",
		"EXT_PUBLIC_GATEWAY": "10.0.0.1",
	})
	err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
	if !errors.Is(err, ErrPresetInvalidValue) {
		t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
	}
	if !strings.Contains(err.Error(), "EXT_NET_VLAN_ID") {
		t.Errorf("error missing VLAN_ID")
	}
	if !strings.Contains(err.Error(), "EXT_PUBLIC_CIDR") {
		t.Errorf("error missing EXT_PUBLIC_CIDR")
	}
}

func TestValidatePresetRequiredKeys_DelegatesToEnvMapFor(t *testing.T) {
	o := validBase()
	o.Preset = PresetCloudPublicVLAN
	o.Sets = nil // missing all 5 required keys
	err := ValidatePresetRequiredKeys(&o)
	if !errors.Is(err, ErrPresetMissingRequired) {
		t.Fatalf("expected ErrPresetMissingRequired, got %v", err)
	}
}

// TestEnvMapFor_PlatformEndpointDefaults_AllPresets pins the
// universalPlatformEndpointDefaults block: every active preset
// (internal-only, cloud-vlan, cloud+public-vlan) MUST seed both
// KUBE_API_INTERNAL_VIP and PLATFORM_ENDPOINT_KUBE_API_ENABLED with
// safe-default values. PresetCustom is excluded — it ships no
// defaults by design (operator manages cluster-config.env directly).
//
// The default values matter:
//   - KUBE_API_INTERNAL_VIP="" — operator post-edits with the chosen
//     VIP after widening EXT_NET_EXCLUDE_IPS and both ALLOWLISTs
//   - PLATFORM_ENDPOINT_KUBE_API_ENABLED="false" — feature opt-in
//
// This is the regression guard for the PRD §6.D.2 contract; without
// it a future preset added to the table could silently miss the
// platform-endpoint defaults and break Helm rendering.
func TestEnvMapFor_PlatformEndpointDefaults_AllPresets(t *testing.T) {
	type setsFor func() map[string]string
	cases := []struct {
		name   string
		preset Preset
		sets   setsFor
	}{
		{
			"internal-only",
			PresetInternalOnly,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":   "1100",
					"EXT_NET_INTERFACE": "eno1",
				}
			},
		},
		{
			"cloud-vlan",
			PresetCloudVLAN,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":   "1100",
					"EXT_NET_INTERFACE": "eno1",
				}
			},
		},
		{
			"cloud+public-vlan",
			PresetCloudPublicVLAN,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":    "1103",
					"EXT_NET_INTERFACE":  "bond0",
					"EXT_PUBLIC_VLAN_ID": "1100",
					"EXT_PUBLIC_CIDR":    "203.0.113.0/29",
					"EXT_PUBLIC_GATEWAY": "203.0.113.49",
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EnvMapFor(tc.preset, tc.sets())
			if err != nil {
				t.Fatalf("EnvMapFor: %v", err)
			}
			vip, present := got["KUBE_API_INTERNAL_VIP"]
			if !present {
				t.Errorf("KUBE_API_INTERNAL_VIP missing from %s preset env (regression: universalPlatformEndpointDefaults not merged)", tc.name)
			}
			if vip != "" {
				t.Errorf("KUBE_API_INTERNAL_VIP default = %q, want %q (operator post-edits the VIP after widening allowlists)", vip, "")
			}
			enabled, present := got["PLATFORM_ENDPOINT_KUBE_API_ENABLED"]
			if !present {
				t.Errorf("PLATFORM_ENDPOINT_KUBE_API_ENABLED missing from %s preset env (regression)", tc.name)
			}
			if enabled != "false" {
				t.Errorf("PLATFORM_ENDPOINT_KUBE_API_ENABLED default = %q, want %q (opt-in)", enabled, "false")
			}
		})
	}
}

// TestEnvMapFor_PlatformEndpoint_SetOverridesDefaults confirms the
// operator-flow: pick a VIP via --set, flip the enabled flag, and
// see those values land in the env map (overriding the safe-defaults).
func TestEnvMapFor_PlatformEndpoint_SetOverridesDefaults(t *testing.T) {
	got, err := EnvMapFor(PresetCloudPublicVLAN, map[string]string{
		"EXT_NET_VLAN_ID":                    "1103",
		"EXT_NET_INTERFACE":                  "bond0",
		"EXT_PUBLIC_VLAN_ID":                 "1100",
		"EXT_PUBLIC_CIDR":                    "203.0.113.0/29",
		"EXT_PUBLIC_GATEWAY":                 "203.0.113.49",
		"KUBE_API_INTERNAL_VIP":              "100.64.0.30",
		"PLATFORM_ENDPOINT_KUBE_API_ENABLED": "true",
	})
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if got["KUBE_API_INTERNAL_VIP"] != "100.64.0.30" {
		t.Errorf("--set KUBE_API_INTERNAL_VIP not honored: got %q", got["KUBE_API_INTERNAL_VIP"])
	}
	if got["PLATFORM_ENDPOINT_KUBE_API_ENABLED"] != "true" {
		t.Errorf("--set PLATFORM_ENDPOINT_KUBE_API_ENABLED not honored: got %q", got["PLATFORM_ENDPOINT_KUBE_API_ENABLED"])
	}
}

// TestEnvMapFor_PlatformEndpoint_CustomPresetUntouched confirms
// PresetCustom does NOT receive the platform-endpoint defaults — the
// operator owns cluster-config.env entirely when they pick custom.
func TestEnvMapFor_PlatformEndpoint_CustomPresetUntouched(t *testing.T) {
	got, err := EnvMapFor(PresetCustom, nil)
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PresetCustom should ship zero defaults, got %d keys: %v", len(got), got)
	}
}

// TestEnvMapFor_AnchorDefaults_AllPresets pins the universalAnchorDefaults
// block: every active preset (internal-only, cloud-vlan, cloud+public-vlan)
// MUST seed the three EXT_NET_ANCHOR_* keys with safe-default values.
//
// Why the defaults matter:
//   - EXT_NET_ANCHOR_IPS="" — greenfield clusters boot without anchors
//     and reach Phase D later via `kube-dc bootstrap anchors apply`
//   - EXT_NET_ANCHOR_INTERFACE="br-ext-cloud" — the kube-ovn-cni external
//     bridge name; only operators on non-default ProviderNetwork names
//     override
//   - EXT_NET_ANCHOR_REQUIRED="false" — gating flag; flipped to "true"
//     post-rollout to fail-loud on config drift
func TestEnvMapFor_AnchorDefaults_AllPresets(t *testing.T) {
	type setsFor func() map[string]string
	cases := []struct {
		name   string
		preset Preset
		sets   setsFor
	}{
		{
			"internal-only",
			PresetInternalOnly,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":   "1100",
					"EXT_NET_INTERFACE": "eno1",
				}
			},
		},
		{
			"cloud-vlan",
			PresetCloudVLAN,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":   "1100",
					"EXT_NET_INTERFACE": "eno1",
				}
			},
		},
		{
			"cloud+public-vlan",
			PresetCloudPublicVLAN,
			func() map[string]string {
				return map[string]string{
					"EXT_NET_VLAN_ID":    "1103",
					"EXT_NET_INTERFACE":  "bond0",
					"EXT_PUBLIC_VLAN_ID": "1100",
					"EXT_PUBLIC_CIDR":    "203.0.113.0/29",
					"EXT_PUBLIC_GATEWAY": "203.0.113.49",
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EnvMapFor(tc.preset, tc.sets())
			if err != nil {
				t.Fatalf("EnvMapFor: %v", err)
			}
			for k, want := range map[string]string{
				"EXT_NET_ANCHOR_IPS":       "",
				"EXT_NET_ANCHOR_INTERFACE": "br-ext-cloud",
				"EXT_NET_ANCHOR_REQUIRED":  "false",
			} {
				v, present := got[k]
				if !present {
					t.Errorf("%s missing from %s preset env (regression: universalAnchorDefaults not merged)", k, tc.name)
				}
				if v != want {
					t.Errorf("%s default = %q, want %q", k, v, want)
				}
			}
		})
	}
}

// TestEnvMapFor_Anchor_CustomPresetUntouched confirms PresetCustom does
// NOT receive the anchor defaults — operator owns cluster-config.env.
func TestEnvMapFor_Anchor_CustomPresetUntouched(t *testing.T) {
	got, err := EnvMapFor(PresetCustom, nil)
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	for _, k := range []string{"EXT_NET_ANCHOR_IPS", "EXT_NET_ANCHOR_INTERFACE", "EXT_NET_ANCHOR_REQUIRED"} {
		if _, present := got[k]; present {
			t.Errorf("PresetCustom leaked %s into env", k)
		}
	}
}

// TestValidatePresetValues_AnchorIPs covers the host=CIDR schema, the
// KUBE_OVN_GW_NODES subset check, and the REQUIRED-implies-non-empty
// gate. Empty EXT_NET_ANCHOR_IPS with REQUIRED=false is the safe
// greenfield default and must NOT error.
func TestValidatePresetValues_AnchorIPs(t *testing.T) {
	// Helper: minimal cloud+public-vlan env plus the anchor knobs.
	base := func(gwNodes, anchors, required string) map[string]string {
		m := map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "203.0.113.48/29",
			"EXT_PUBLIC_GATEWAY": "203.0.113.49",
			"KUBE_OVN_GW_NODES":  gwNodes,
		}
		if anchors != "" {
			m["EXT_NET_ANCHOR_IPS"] = anchors
		}
		if required != "" {
			m["EXT_NET_ANCHOR_REQUIRED"] = required
		}
		return m
	}
	cases := []struct {
		name     string
		gwNodes  string
		anchors  string
		required string
		wantErr  bool
		wantSub  string
	}{
		// Anchor IPs use 100.65.0.x to match the cloud+public-vlan
		// preset's default EXT_NET_CIDR=100.65.0.0/16. Earlier cases
		// used 100.64.x; once the in-CIDR check landed those would
		// fail with a spurious "outside EXT_NET_CIDR" error.
		{
			"empty anchors ok",
			"host5-a,host6-a", "", "false",
			false, "",
		},
		{
			"REQUIRED=true full coverage passes",
			"host5-a,host6-a,host7-a",
			"host5-a=100.65.0.11/16,host6-a=100.65.0.12/16,host7-a=100.65.0.13/16",
			"true",
			false, "",
		},
		{
			"REQUIRED=false partial coverage allowed",
			"host5-a,host6-a,host7-a",
			"host5-a=100.65.0.11/16",
			"false",
			false, "",
		},
		{
			"REQUIRED=true missing coverage on one gw node",
			"host5-a,host6-a,host7-a",
			"host5-a=100.65.0.11/16,host6-a=100.65.0.12/16",
			"true",
			true, "gateway node(s) host7-a have no anchor IP",
		},
		{
			"REQUIRED=true missing coverage on multiple gw nodes — sorted",
			"host5-a,host6-a,host7-a",
			"host5-a=100.65.0.11/16",
			"true",
			true, "host6-a, host7-a",
		},
		{
			"host not in gw nodes",
			"host5-a,host6-a", "host9-x=100.65.0.99/16", "false",
			true, "not in KUBE_OVN_GW_NODES",
		},
		{
			"bad CIDR — missing mask",
			"host5-a", "host5-a=100.65.0.11", "false",
			true, "invalid CIDR",
		},
		{
			"missing equals",
			"host5-a", "host5-a-100.65.0.11/16", "false",
			true, "missing '='",
		},
		{
			"required without anchors",
			"host5-a", "", "true",
			true, "REQUIRED=true but EXT_NET_ANCHOR_IPS empty",
		},
		{
			"gw nodes empty with anchors",
			"", "host5-a=100.65.0.11/16", "false",
			true, "KUBE_OVN_GW_NODES empty",
		},
		{
			"duplicate host",
			"host5-a,host6-a",
			"host5-a=100.65.0.11/16,host5-a=100.65.0.12/16",
			"false",
			true, "listed more than once",
		},
		{
			"empty host in pair",
			"host5-a", "=100.65.0.11/16", "false",
			true, "empty host",
		},
		{
			"duplicate IP across hosts",
			"host5-a,host6-a",
			"host5-a=100.65.0.11/16,host6-a=100.65.0.11/16",
			"false",
			true, "claimed by both",
		},
		{
			"anchor outside EXT_NET_CIDR",
			"host5-a",
			"host5-a=10.0.0.11/16",
			"false",
			true, "outside EXT_NET_CIDR",
		},
		{
			"anchor prefix mismatch",
			"host5-a",
			"host5-a=100.65.0.11/24",
			"false",
			true, "anchor mask must match",
		},
		{
			"whitespace-only KUBE_OVN_GW_NODES with anchors",
			" , , ", "host5-a=100.65.0.11/16", "false",
			true, "no usable hosts",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, err := EnvMapFor(PresetCloudPublicVLAN, base(tc.gwNodes, tc.anchors, tc.required))
			if err != nil {
				t.Fatalf("EnvMapFor: %v", err)
			}
			err = ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

// TestValidatePresetValues_AnchorInterface covers the new
// EXT_NET_ANCHOR_INTERFACE hook. Default "br-ext-cloud" passes;
// --set overrides exercise the validateNICName rejection paths.
func TestValidatePresetValues_AnchorInterface(t *testing.T) {
	base := func(iface string) map[string]string {
		m := map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "203.0.113.48/29",
			"EXT_PUBLIC_GATEWAY": "203.0.113.49",
		}
		if iface != "" {
			m["EXT_NET_ANCHOR_INTERFACE"] = iface
		}
		return m
	}
	cases := []struct {
		name, iface, wantSub string
		wantErr              bool
	}{
		{"default br-ext-cloud (from preset)", "", "", false},
		{"explicit br-ext-cloud", "br-ext-cloud", "", false},
		{"alt provider bridge", "br-ext-public", "", false},
		{"shell metachar reject", "br-ext;cloud", "unsupported character", true},
		{"space reject", "br ext", "unsupported character", true},
		{"too long", "this-iface-name-is-way-too-long-for-linux", "IFNAMSIZ", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, _ := EnvMapFor(PresetCloudPublicVLAN, base(tc.iface))
			err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if !strings.Contains(err.Error(), "EXT_NET_ANCHOR_INTERFACE") {
					t.Errorf("error should name EXT_NET_ANCHOR_INTERFACE; got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

// TestValidatePresetValues_AnchorSSHHosts covers the new
// validateAnchorSSHHosts schema check. Empty default is valid
// (falls through to ~/.ssh/config alias path); non-empty maps go
// through the same node-in-gw + uniqueness + non-empty-host gates
// as anchor IPs.
func TestValidatePresetValues_AnchorSSHHosts(t *testing.T) {
	base := func(gwNodes, sshHosts string) map[string]string {
		m := map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "203.0.113.48/29",
			"EXT_PUBLIC_GATEWAY": "203.0.113.49",
			"KUBE_OVN_GW_NODES":  gwNodes,
		}
		if sshHosts != "" {
			m["EXT_NET_ANCHOR_SSH_HOSTS"] = sshHosts
		}
		return m
	}
	cases := []struct {
		name, gwNodes, sshHosts, wantSub string
		wantErr                          bool
	}{
		{"empty ok", "host5-a,host6-a", "", "", false},
		{
			"valid atlantis-style",
			"host5-a,host6-a,host7-a",
			"host5-a=203.0.113.52,host6-a=203.0.113.53,host7-a=203.0.113.54",
			"", false,
		},
		{"missing equals", "host5-a", "host5-a", "missing '='", true},
		{"empty node", "host5-a", "=203.0.113.52", "empty node", true},
		{"empty host", "host5-a", "host5-a=", "empty host", true},
		{"duplicate node", "host5-a,host6-a", "host5-a=1.1.1.1,host5-a=2.2.2.2", "listed more than once", true},
		{"node not in gw", "host5-a", "host9-x=1.1.1.1", "not in KUBE_OVN_GW_NODES", true},
		{"whitespace in host", "host5-a", "host5-a=foo bar", "contains whitespace or '='", true},
		{"FQDN host accepted", "host5-a", "host5-a=host5.bastion.example.com", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, _ := EnvMapFor(PresetCloudPublicVLAN, base(tc.gwNodes, tc.sshHosts))
			err := ValidatePresetValues(PresetCloudPublicVLAN, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if !strings.Contains(err.Error(), "EXT_NET_ANCHOR_SSH_HOSTS") {
					t.Errorf("error should name EXT_NET_ANCHOR_SSH_HOSTS; got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestEnvMapFor_IngressDefaults_AllPresets(t *testing.T) {
	// D'''''.1 / BGP slice: every non-custom preset inherits the
	// ingress-topology + MetalLB announcement-mode defaults so a
	// freshly scaffolded cluster-config.env documents both knobs.
	for _, p := range []Preset{PresetInternalOnly, PresetCloudVLAN, PresetCloudPublicVLAN} {
		sets := map[string]string{
			"EXT_NET_VLAN_ID":   "1103",
			"EXT_NET_INTERFACE": "bond0",
		}
		if p == PresetCloudPublicVLAN {
			sets["EXT_PUBLIC_VLAN_ID"] = "1100"
			sets["EXT_PUBLIC_CIDR"] = "203.0.113.48/29"
			sets["EXT_PUBLIC_GATEWAY"] = "203.0.113.49"
		}
		envMap, err := EnvMapFor(p, sets)
		if err != nil {
			t.Fatalf("preset %s: EnvMapFor unexpected err: %v", p, err)
		}
		if got := envMap["INGRESS_MODE"]; got != "metallb-lb" {
			t.Errorf("preset %s: INGRESS_MODE = %q, want %q", p, got, "metallb-lb")
		}
		if got := envMap["METALLB_MODE"]; got != "l2" {
			t.Errorf("preset %s: METALLB_MODE = %q, want %q", p, got, "l2")
		}
	}

	// Custom preset ships no defaults — operator vouches for the env.
	envMap, err := EnvMapFor(PresetCustom, nil)
	if err != nil {
		t.Fatalf("custom preset: unexpected err: %v", err)
	}
	if _, ok := envMap["INGRESS_MODE"]; ok {
		t.Errorf("custom preset must not inject INGRESS_MODE")
	}
}

func TestValidatePresetValues_IngressAndMetalLBModes(t *testing.T) {
	base := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"EXT_NET_VLAN_ID":   "1103",
			"EXT_NET_INTERFACE": "bond0",
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	cases := []struct {
		name    string
		extra   map[string]string
		wantErr bool
		wantSub string // substring the error must contain when wantErr
	}{
		{"defaults valid", nil, false, ""},
		{"explicit metallb-lb + l2", map[string]string{
			"INGRESS_MODE": "metallb-lb", "METALLB_MODE": "l2"}, false, ""},
		// hostnetwork EXISTS as a topology but init can't scaffold it yet
		// (no EnvoyProxy patch written) — accepting it would validate a
		// cluster whose front door never comes up. Rejected with an
		// actionable message until D'''''.1 automates the variant
		// (review finding 2026-07-10, P1).
		{"hostnetwork rejected until automated", map[string]string{
			"INGRESS_MODE": "hostnetwork"}, true, "not yet automated"},
		{"bad ingress mode", map[string]string{
			"INGRESS_MODE": "nodeport"}, true, "INGRESS_MODE"},
		{"bad metallb mode", map[string]string{
			"METALLB_MODE": "arp"}, true, "METALLB_MODE"},
		{"floating ip invalid", map[string]string{
			"METALLB_FLOATING_IP": "not-an-ip"}, true, "METALLB_FLOATING_IP"},
		{"floating ip valid", map[string]string{
			"METALLB_FLOATING_IP": "192.0.2.10"}, false, ""},
		{"bgp missing trio", map[string]string{
			"METALLB_MODE": "bgp"}, true, "required when METALLB_MODE=bgp"},
		{"bgp complete valid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, false, ""},
		{"bgp asn zero rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "0",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "METALLB_BGP_LOCAL_ASN"},
		{"bgp asn text rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "as64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "not a number"},
		{"bgp asn overflow rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "4294967296",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "METALLB_BGP_PEER_ASN"},
		{"bgp peer address invalid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "router.example.com",
		}, true, "METALLB_BGP_PEER_ADDRESS"},
		{"bgp peer port invalid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_PEER_PORT":    "70000",
		}, true, "METALLB_BGP_PEER_PORT"},
		{"bgp peer port valid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_PEER_PORT":    "179",
		}, false, ""},
		// IPv4-only: the fleet/chart render "<ip>/32" pools; an IPv6 VIP
		// or peer would pass net.ParseIP and then produce a broken pool
		// (MetalLB v6 needs /128 + aggregationLengthV6, which we don't
		// render). Review finding 2026-07-10.
		{"floating ip ipv6 rejected", map[string]string{
			"METALLB_FLOATING_IP": "2001:db8::10"}, true, "IPv6"},
		{"bgp peer ipv6 rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "2001:db8::1",
		}, true, "IPv6"},
		// Reserved / special ASNs: 65535 + 4294967295 (RFC 7300),
		// 23456 = AS_TRANS (RFC 6793 / RFC 7249).
		{"bgp asn 65535 reserved", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "65535",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "reserved"},
		{"bgp asn 4294967295 reserved", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "4294967295",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "reserved"},
		{"bgp asn 23456 as_trans", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "23456",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		}, true, "AS_TRANS"},
		// Hold-time: rendered verbatim into BGPPeer.spec.holdTime — a
		// typo must fail init, not Flux reconciliation. RFC 4271 §4.2:
		// 0 or >= 3s.
		{"bgp hold time valid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "90s",
		}, false, ""},
		{"bgp hold time zero valid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "0s",
		}, false, ""},
		{"bgp hold time garbage rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "fast",
		}, true, "METALLB_BGP_HOLD_TIME"},
		{"bgp hold time below 3s rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "2s",
		}, true, "at least 3s"},
		// Protocol maximum: hold time is a two-octet seconds field —
		// 65535s is the last valid value, 65536s overflows the wire
		// format. Fractional seconds can't be carried at all.
		{"bgp hold time 65535s boundary valid", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "65535s",
		}, false, ""},
		{"bgp hold time 65536s rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "65536s",
		}, true, "65535"},
		{"bgp hold time 500ms gets sub-second diagnostic", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			// Below 3s AND fractional: must hit the sub-second branch
			// (the precise diagnosis), not the generic range message —
			// P3 review finding, the ordering used to shadow it.
			"METALLB_BGP_HOLD_TIME": "500ms",
		}, true, "sub-second"},
		{"bgp hold time fractional rejected", map[string]string{
			"METALLB_MODE":             "bgp",
			"METALLB_BGP_LOCAL_ASN":    "64512",
			"METALLB_BGP_PEER_ASN":     "64513",
			"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
			"METALLB_BGP_HOLD_TIME":    "90.5s",
		}, true, "sub-second"},
		// l2 mode must NOT demand the BGP trio.
		{"l2 mode no bgp keys required", map[string]string{
			"METALLB_MODE": "l2"}, false, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			envMap, err := EnvMapFor(PresetInternalOnly, base(tc.extra))
			if err != nil {
				t.Fatalf("EnvMapFor unexpected err: %v", err)
			}
			err = ValidatePresetValues(PresetInternalOnly, envMap)
			if tc.wantErr {
				if !errors.Is(err, ErrPresetInvalidValue) {
					t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
