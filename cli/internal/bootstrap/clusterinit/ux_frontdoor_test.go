package clusterinit

import (
	"os"
	"path/filepath"
	"testing"
)

// The operator-facing UX contract: ONE question about the address, a default
// that is inferred from what they already declared, and no silent override of
// an answer they gave.
func TestResolveAddressLayer_UXContract(t *testing.T) {
	cases := []struct {
		name, explicit, vip, mode string
		wantLayer                 string
		wantAmbiguous             bool
	}{
		// Nothing declared → the fail-safe. Needs nothing from the network and
		// always yields a working front door; a VIP default would leave the
		// Service <pending> forever on sites with no spare address.
		{"silent operator gets the fail-safe", "", "", "", AddressLayerNone, false},
		// A reserved address is NOT proof that it is the front door: of four
		// live clusters declaring one, only two served on it — the others held
		// a spare and an address the fabric never delivers. Ambiguous → ask.
		{"declared VIP is ambiguous, never inferred", "", "192.0.2.10", "", AddressLayerNone, true},
		{"declared VIP + bgp mode is still ambiguous", "", "192.0.2.10", "bgp", AddressLayerNone, true},
		{"CHANGEME placeholder is not a declaration", "", "CHANGEME", "", AddressLayerNone, false},
		// An EXPLICIT answer is never overridden — not even by a declared VIP.
		{"explicit none wins over a VIP", AddressLayerNone, "192.0.2.10", "", AddressLayerNone, false},
		{"explicit l2 wins over bgp mode", AddressLayerMetalLBL2, "192.0.2.10", "bgp", AddressLayerMetalLBL2, false},
	}
	for _, c := range cases {
		gotLayer, gotAmbiguous := resolveAddressLayer(c.explicit, c.vip, c.mode)
		if gotLayer != c.wantLayer || gotAmbiguous != c.wantAmbiguous {
			t.Errorf("%s: got (%q ambiguous=%v), want (%q %v)",
				c.name, gotLayer, gotAmbiguous, c.wantLayer, c.wantAmbiguous)
		}
	}
}

// A declared VIP with no layer must be REFUSED with both answers spelled out.
// Guessing here would have rewired live production front doors to addresses
// their fabrics cannot carry.
func TestExplicitNoneWithVIPIsRefused(t *testing.T) {
	var errs []string
	validateIngressAndMetalLB(map[string]string{
		"INGRESS_ADDRESS_LAYER": AddressLayerNone,
		"METALLB_FLOATING_IP":   "192.0.2.10",
		"INGRESS_NODE_LABEL":    "kube-dc.com/ingress",
	}, &errs)
	if len(errs) == 0 {
		t.Fatal("explicit none + a declared VIP must be refused")
	}
	joined := ""
	for _, e := range errs {
		joined += e + ";"
	}
	for _, want := range []string{"never be announced", "--ingress-address-layer=metallb-l2"} {
		if !contains(joined, want) {
			t.Errorf("the error must name the fix (%q), got: %s", want, joined)
		}
	}
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()
}

// The reviewed-inputs block must ALWAYS state the front door in plain words,
// naming the address users will dial and the nodes that answer. Operators
// routinely discover this fact after an install instead of during the review;
// a line that only echoes the layer token they typed does not help them.
func TestFrontDoorPlanSummaryIsConcrete(t *testing.T) {
	cases := []struct {
		name  string
		o     *InitOptions
		want  []string
		avoid []string
	}{{
		name: "VIP layer names the VIP and the announcement mechanism",
		o: &InitOptions{
			Preset:              PresetInternalOnly,
			IngressAddressLayer: AddressLayerMetalLBL2,
			IngressNodes:        []string{"n2", "n1", "n2"},
			Sets:                map[string]string{"METALLB_FLOATING_IP": "192.0.2.10"},
		},
		want: []string{"192.0.2.10", "ARP", "n1, n2"},
	}, {
		name: "no VIP tells the operator to point DNS at the nodes",
		o: &InitOptions{
			Preset:              PresetInternalOnly,
			IngressAddressLayer: AddressLayerNone,
			IngressNodes:        []string{"n1"},
			Sets:                map[string]string{},
		},
		want:  []string{"no VIP", "point DNS", "n1"},
		avoid: []string{"MetalLB"},
	}, {
		name: "silent operator still gets a resolved statement, never a blank",
		o: &InitOptions{
			Preset: PresetInternalOnly,
			Sets:   map[string]string{},
		},
		want: []string{"no VIP", "NOT DECLARED"},
	}, {
		// The recommended shape: name no ingress nodes and inherit the gateway
		// set, which satisfies the announcer co-location invariant by
		// construction. The plan must still NAME the machines.
		name: "an undeclared ingress set is derived from the gateway nodes",
		o: &InitOptions{
			Preset:              PresetInternalOnly,
			IngressAddressLayer: AddressLayerMetalLBL2,
			Sets: map[string]string{
				"METALLB_FLOATING_IP": "192.0.2.10",
				"KUBE_OVN_GW_NODES":   "gw2,gw1",
			},
		},
		want: []string{"192.0.2.10", "gw1, gw2", "derived from KUBE_OVN_GW_NODES"},
	}, {
		name: "an unresolved VIP says UNRESOLVED instead of a confident none",
		o: &InitOptions{
			Preset: PresetInternalOnly,
			Sets:   map[string]string{"METALLB_FLOATING_IP": "192.0.2.10"},
		},
		want: []string{"UNRESOLVED", "192.0.2.10", "--ingress-address-layer"},
	}}
	for _, c := range cases {
		layer, summary := frontDoorPlan(c.o)
		if layer == "" {
			t.Errorf("%s: the plan must carry a RESOLVED layer, got empty", c.name)
		}
		for _, w := range c.want {
			if !contains(summary, w) {
				t.Errorf("%s: summary must mention %q, got: %s", c.name, w, summary)
			}
		}
		for _, a := range c.avoid {
			if contains(summary, a) {
				t.Errorf("%s: summary must NOT mention %q, got: %s", c.name, a, summary)
			}
		}
	}
}

// The plan HEADER carries the resolved layer while the HASH carries the
// explicit flag. Collapsing the two would make an absent flag hash as "none"
// and invalidate every plan file written by an older CLI — a spurious drift
// error in the middle of an install.
func TestPlanHashUsesExplicitLayerNotResolved(t *testing.T) {
	silent := &InitOptions{Preset: PresetInternalOnly, Sets: map[string]string{}}
	if got := silent.inputsForHash().IngressAddressLayer; got != "" {
		t.Errorf("hash input must stay empty when the flag is absent, got %q", got)
	}
	if layer, _ := frontDoorPlan(silent); layer != AddressLayerNone {
		t.Errorf("plan header must resolve an absent flag to %q, got %q", AddressLayerNone, layer)
	}
}

// The silent front-door failure this preflight exists to prevent: MetalLB
// announces the VIP only from external-gateway nodes, Envoy answers only on
// ingress nodes, and externalTrafficPolicy=Local means the announcing node
// must be BOTH. Disjoint sets install cleanly and leave the address dark.
func TestAnnouncerCoLocationPreflight(t *testing.T) {
	base := func(ingress []string, gw string) *InitOptions {
		return &InitOptions{
			Preset:              PresetInternalOnly,
			IngressAddressLayer: AddressLayerMetalLBL2,
			IngressNodes:        ingress,
			Sets: map[string]string{
				"METALLB_FLOATING_IP": "192.0.2.10",
				"KUBE_OVN_GW_NODES":   gw,
				// Preset-required keys, unrelated to the front door.
				"EXT_NET_INTERFACE": "enp1s0",
				"EXT_NET_VLAN_ID":   "0",
			},
		}
	}
	// Disjoint → refused, and the message must name both sets and all three fixes.
	o := base([]string{"worker1", "worker2"}, "gw1,gw2")
	err := validateIngressAnnouncerCoLocation(o, mustResolvedEnv(t, o))
	if err == nil {
		t.Fatal("disjoint ingress/gateway sets under a VIP layer must be refused")
	}
	for _, want := range []string{"NEVER be announced", "gw1,gw2", "worker1,worker2",
		"externalTrafficPolicy=Local", "dropping --ingress-node"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q, got: %v", want, err)
		}
	}
	// Partial overlap is REFUSED under a VIP layer. One node can announce, and that is
	// exactly the problem: it is a single point of failure dressed as HA, and
	// scripts/frontdoor-check.sh preflight rejects the same shape against the live
	// cluster — so accepting it here only moves the failure to after the fleet is
	// committed.
	o = base([]string{"gw1", "worker9"}, "gw1,gw2")
	err = validateIngressAnnouncerCoLocation(o, mustResolvedEnv(t, o))
	if err == nil {
		t.Fatal("a partial ingress/gateway overlap under a VIP layer must be refused")
	}
	for _, want := range []string{"worker9", "SUBSET", "single point of failure",
		"KUBE_OVN_GW_NODES", "drop --ingress-node"} {
		if !contains(err.Error(), want) {
			t.Errorf("the partial-overlap refusal must mention %q, got: %v", want, err)
		}
	}
	// A true SUBSET passes: every ingress node is also an announcer.
	o = base([]string{"gw1"}, "gw1,gw2")
	if err := validateIngressAnnouncerCoLocation(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("an ingress set that is a subset of the gateway set must pass, got: %v", err)
	}
	// Undeclared is the recommended shape and satisfies the invariant.
	o = base(nil, "gw1,gw2")
	if err := validateIngressAnnouncerCoLocation(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("an undeclared ingress set must pass (it derives from the gw nodes), got: %v", err)
	}
	// No VIP → no announcement → no invariant to violate.
	o = base([]string{"worker1"}, "gw1,gw2")
	o.IngressAddressLayer = AddressLayerNone
	delete(o.Sets, "METALLB_FLOATING_IP")
	if err := validateIngressAnnouncerCoLocation(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("layer=none has no announcer to co-locate, got: %v", err)
	}
}

func mustResolvedEnv(t *testing.T, o *InitOptions) map[string]string {
	t.Helper()
	env, err := ResolvedEnvFor(o)
	if err != nil {
		t.Fatalf("ResolvedEnvFor: %v", err)
	}
	return env
}

// A resume writes nothing (it skips the scaffold), so a front-door change
// passed on a resume must be REFUSED rather than accepted-and-ignored — and
// an ordinary resume with no front-door flag must not be disturbed.
func TestResumeFrontDoorGuard(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, "clusters", "c1")
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cd, "cluster-config.env"),
		[]byte("INGRESS_ADDRESS_LAYER=metallb-l2\nMETALLB_FLOATING_IP=192.0.2.10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The regression this guard nearly shipped: with the RESOLVED layer
	// ("none" when the flag is absent) every plain resume of a VIP cluster
	// was refused. An empty request must always pass.
	if err := CheckFrontDoorMatchesOverlay(dir, "c1", ""); err != nil {
		t.Errorf("an ordinary resume (no front-door flag) must not be blocked: %v", err)
	}
	// Same layer restated is not a change.
	if err := CheckFrontDoorMatchesOverlay(dir, "c1", AddressLayerMetalLBL2); err != nil {
		t.Errorf("restating the SAME layer must not be blocked: %v", err)
	}
	// A real change is refused, and the message must route to the day-2 path.
	err := CheckFrontDoorMatchesOverlay(dir, "c1", AddressLayerNone)
	if err == nil {
		t.Fatal("a front-door CHANGE on a resume must be refused")
	}
	for _, want := range []string{"metallb-l2", "silently ignored", "day-2", "let Flux converge"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q, got: %v", want, err)
		}
	}
	// No overlay at all → nothing to compare, never block a fresh install.
	if err := CheckFrontDoorMatchesOverlay(dir, "does-not-exist", AddressLayerMetalLBBGP); err != nil {
		t.Errorf("a missing overlay must not block: %v", err)
	}
}

// A METALLB_MODE the layer overrode must be REPORTED, not discarded. The
// derivation runs before validation sees the env map, which made the generic
// ENVOY_*-style coherence guard structurally unable to fire for this key.
func TestMetalLBModeContradictionIsReported(t *testing.T) {
	o := &InitOptions{
		Preset:              PresetInternalOnly,
		IngressAddressLayer: AddressLayerMetalLBBGP,
		Sets: map[string]string{
			"METALLB_MODE":        "l2",
			"METALLB_FLOATING_IP": "192.0.2.10",
			"EXT_NET_INTERFACE":   "enp1s0",
			"EXT_NET_VLAN_ID":     "0",
		},
	}
	err := validateMetalLBModeRequest(o, mustResolvedEnv(t, o))
	if err == nil {
		t.Fatal("METALLB_MODE=l2 under a BGP layer must be reported, not silently rewritten")
	}
	for _, want := range []string{"contradicts", "metallb-bgp", "--ingress-address-layer=metallb-l2"} {
		if !contains(err.Error(), want) {
			t.Errorf("the error must mention %q, got: %v", want, err)
		}
	}
	// Agreement is silent.
	o.Sets["METALLB_MODE"] = "bgp"
	if err := validateMetalLBModeRequest(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("a mode that AGREES with the layer must pass, got: %v", err)
	}
	// No carrier key may reach the written env — nothing strips KUBE_DC_INIT_.
	for k := range mustResolvedEnv(t, o) {
		if contains(k, "METALLB_MODE_REQUESTED") {
			t.Errorf("%s must not appear in the env map (it would be written verbatim)", k)
		}
	}
}

// The plan header must carry the SAME ingress node set its summary names, and
// the same one scaffold derives the :6443 decision from.
func TestPlanHeaderCarriesResolvedIngressNodes(t *testing.T) {
	o := &InitOptions{
		Preset: PresetInternalOnly,
		Sets: map[string]string{
			"KUBE_OVN_GW_NODES": "gw2,gw1",
			"EXT_NET_INTERFACE": "enp1s0",
			"EXT_NET_VLAN_ID":   "0",
		},
	}
	nodes, derived := resolvedIngressNodesFor(o)
	if !derived || len(nodes) != 2 || nodes[0] != "gw1" || nodes[1] != "gw2" {
		t.Errorf("undeclared ingress set must resolve to the sorted gateway nodes, got %v (derived=%v)", nodes, derived)
	}
	// An explicit set wins and is NOT replaced by the gateway nodes.
	o.IngressNodes = []string{"w9"}
	if nodes, derived := resolvedIngressNodesFor(o); derived || len(nodes) != 1 || nodes[0] != "w9" {
		t.Errorf("an explicit ingress set must win, got %v (derived=%v)", nodes, derived)
	}
}

// A resume that cannot READ the overlay must fail closed when a front-door
// change was requested — treating it as "no mismatch" is the silent-ignore the
// guard exists to prevent. With no request, an unrelated parse error must not
// block the resume.
func TestResumeGuardFailsClosedOnUnreadableOverlay(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, "clusters", "c1")
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory where the env file should be: readable path, unreadable file.
	if err := os.MkdirAll(filepath.Join(cd, "cluster-config.env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckFrontDoorMatchesOverlay(dir, "c1", AddressLayerMetalLBL2); err == nil {
		t.Error("an unreadable overlay with an explicit layer request must fail closed")
	}
	if err := CheckFrontDoorMatchesOverlay(dir, "c1", ""); err != nil {
		t.Errorf("no front-door request means an unreadable overlay must not block: %v", err)
	}
}

// Whether Envoy can bind :6443 is a PER-NODE fact (kube-apiserver owns the port
// on a control-plane node and not elsewhere), but the listener carrying
// kube-api.<domain> is a single cluster-wide listener. So a MIXED ingress set has
// no correct global answer, and the previous behaviour picked the worst one: a
// single intersection removed the listener globally, stripping it from the worker
// nodes that were fine.
func TestClassifyIngressPlacement(t *testing.T) {
	cp := []string{"cp-1", "cp-2", "cp-3"}

	t.Run("all on control plane", func(t *testing.T) {
		on, off, ok := ClassifyIngressPlacement([]string{"cp-1", "cp-2"}, cp)
		if !ok || len(on) != 2 || len(off) != 0 {
			t.Errorf("on=%v off=%v derivable=%v", on, off, ok)
		}
	})
	t.Run("all on workers", func(t *testing.T) {
		on, off, ok := ClassifyIngressPlacement([]string{"w-1", "w-2"}, cp)
		if !ok || len(on) != 0 || len(off) != 2 {
			t.Errorf("on=%v off=%v derivable=%v", on, off, ok)
		}
	})
	t.Run("mixed is reported as mixed, not collapsed to a boolean", func(t *testing.T) {
		on, off, ok := ClassifyIngressPlacement([]string{"cp-1", "w-9"}, cp)
		if !ok || len(on) != 1 || len(off) != 1 {
			t.Fatalf("on=%v off=%v derivable=%v", on, off, ok)
		}
		if on[0] != "cp-1" || off[0] != "w-9" {
			t.Errorf("classification wrong: on=%v off=%v", on, off)
		}
	})
	t.Run("no control-plane knowledge means stand down, never guess", func(t *testing.T) {
		if _, _, ok := ClassifyIngressPlacement([]string{"a"}, nil); ok {
			t.Error("must not be derivable without control-plane names")
		}
		if _, _, ok := ClassifyIngressPlacement(nil, cp); ok {
			t.Error("must not be derivable without an ingress set")
		}
	})
	t.Run("results are sorted so messages are stable", func(t *testing.T) {
		on, _, _ := ClassifyIngressPlacement([]string{"cp-3", "cp-1"}, cp)
		if len(on) != 2 || on[0] != "cp-1" || on[1] != "cp-3" {
			t.Errorf("expected sorted, got %v", on)
		}
	})
}

// The mixed topology is refused up front, and the message must name both halves
// and both acceptable resolutions — a refusal that does not say what to do
// instead just moves the confusion.
func TestMixedIngressPlacementIsRefused(t *testing.T) {
	o := &InitOptions{
		Preset:            PresetInternalOnly,
		IngressNodes:      []string{"cp-1", "worker-9"},
		ControlPlaneNodes: []string{"cp-1", "cp-2"},
		Sets: map[string]string{
			"DOMAIN":            "dc.example.com",
			"EXT_NET_INTERFACE": "enp1s0",
			"EXT_NET_VLAN_ID":   "0",
		},
	}
	err := validateIngressPlacementNotMixed(o, mustResolvedEnv(t, o))
	if err == nil {
		t.Fatal("a mixed control-plane/worker ingress set must be refused")
	}
	for _, want := range []string{"cp-1", "worker-9", "entirely on workers",
		"entirely on control-plane nodes", ":443"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q, got:\n%v", want, err)
		}
	}

	// Not mixed → silent, in both directions.
	o.IngressNodes = []string{"cp-1", "cp-2"}
	if err := validateIngressPlacementNotMixed(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("an all-control-plane set must pass: %v", err)
	}
	o.IngressNodes = []string{"worker-9"}
	if err := validateIngressPlacementNotMixed(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("an all-worker set must pass: %v", err)
	}
	// Unknown control plane → stand down.
	o.IngressNodes = []string{"cp-1", "worker-9"}
	o.ControlPlaneNodes = nil
	if err := validateIngressPlacementNotMixed(o, mustResolvedEnv(t, o)); err != nil {
		t.Errorf("without control-plane names this must not block: %v", err)
	}
}
