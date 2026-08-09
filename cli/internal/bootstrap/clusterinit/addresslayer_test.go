package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ingress design v2 (§5a): the data plane is identical on every cluster, so
// the ONLY front-door choice is who owns the address. These tests pin the
// derivation, because three places depend on agreeing about it — the fleet
// template's ${ENVOY_*} scalars, the plan preview, and the validator.

func TestAddressLayerEnv_DerivesTheEnvoyServiceShape(t *testing.T) {
	cases := map[string]struct{ svcType, lbClass, etp, metallb string }{
		// BOTH loadBalancerClass and externalTrafficPolicy must be null on the
		// no-VIP layer. Verified by server-side dry-run against a live API
		// server: ClusterIP + externalTrafficPolicy=Local is rejected with
		// "may only be set for externally-accessible services", so shipping
		// Local here made the FAIL-SAFE layer render a Service that could not
		// be applied — no front door at all. The live host-bind cluster agrees:
		// its ClusterIP Envoy Service carries no externalTrafficPolicy.
		AddressLayerNone: {"ClusterIP", "null", "null", "l2"},
		// A ClusterIP Service carrying loadBalancerClass is REJECTED by the
		// API server ("may only be used when type is 'LoadBalancer'"), and
		// "" is rejected too — the literal null is the only accepted value,
		// and in a strategic-merge value it also deletes the key.
		AddressLayerMetalLBL2:  {"LoadBalancer", "metallb", "Local", "l2"},
		AddressLayerMetalLBBGP: {"LoadBalancer", "metallb", "Local", "bgp"},
	}
	for layer, want := range cases {
		got := addressLayerEnv(layer)
		if got["ENVOY_SERVICE_TYPE"] != want.svcType {
			t.Errorf("%s: service type = %q, want %q", layer, got["ENVOY_SERVICE_TYPE"], want.svcType)
		}
		if got["ENVOY_LB_CLASS"] != want.lbClass {
			t.Errorf("%s: lb class = %q, want %q", layer, got["ENVOY_LB_CLASS"], want.lbClass)
		}
		// Local is non-negotiable on a VIP layer: Cluster SNATs the client
		// IP away, which silently defeats every IP-based guardrail.
		if got["ENVOY_TRAFFIC_POLICY"] != want.etp {
			t.Errorf("%s: traffic policy = %q, want %q", layer, got["ENVOY_TRAFFIC_POLICY"], want.etp)
		}
		if got["METALLB_MODE"] != want.metallb {
			t.Errorf("%s: metallb mode = %q, want %q", layer, got["METALLB_MODE"], want.metallb)
		}
	}
	// An unknown layer must fall back to the SAFE shape, never to a
	// LoadBalancer that hangs pending on a VIP nobody assigned.
	if got := addressLayerEnv("nonsense")["ENVOY_SERVICE_TYPE"]; got != "ClusterIP" {
		t.Errorf("unknown layer must fall back to ClusterIP, got %q", got)
	}
}

func TestEnvMapFor_DerivesAddressLayerScalars(t *testing.T) {
	env, err := EnvMapFor(PresetCloudVLAN, map[string]string{
		"EXT_NET_VLAN_ID":       "163",
		"EXT_NET_INTERFACE":     "bond0",
		"INGRESS_ADDRESS_LAYER": AddressLayerMetalLBL2,
		"METALLB_FLOATING_IP":   "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("EnvMapFor: %v", err)
	}
	if env["ENVOY_SERVICE_TYPE"] != "LoadBalancer" || env["ENVOY_LB_CLASS"] != "metallb" {
		t.Errorf("VIP layer did not derive the LB shape: %v", map[string]string{
			"type": env["ENVOY_SERVICE_TYPE"], "class": env["ENVOY_LB_CLASS"]})
	}
	// The default (no layer declared) must be the no-VIP shape.
	def, err := EnvMapFor(PresetCloudVLAN, map[string]string{
		"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0",
	})
	if err != nil {
		t.Fatalf("EnvMapFor default: %v", err)
	}
	if def["INGRESS_ADDRESS_LAYER"] != AddressLayerNone || def["ENVOY_SERVICE_TYPE"] != "ClusterIP" {
		t.Errorf("default must be the no-VIP shape, got layer=%q type=%q",
			def["INGRESS_ADDRESS_LAYER"], def["ENVOY_SERVICE_TYPE"])
	}
	if def["ENVOY_LB_CLASS"] != "null" {
		t.Errorf("no-VIP layer must null the lb class (API server forbids it on ClusterIP), got %q",
			def["ENVOY_LB_CLASS"])
	}
	// An explicit --set of a derived scalar is honoured (the validator then
	// reports the incoherence) — deriving must not silently overwrite the
	// operator.
	over, err := EnvMapFor(PresetCloudVLAN, map[string]string{
		"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0",
		"ENVOY_TRAFFIC_POLICY": "Cluster",
	})
	if err != nil {
		t.Fatalf("EnvMapFor override: %v", err)
	}
	if over["ENVOY_TRAFFIC_POLICY"] != "Cluster" {
		t.Errorf("--set must win over derivation, got %q", over["ENVOY_TRAFFIC_POLICY"])
	}
}

func TestValidateIngressNodes(t *testing.T) {
	base := func() *InitOptions {
		return &InitOptions{IngressAddressLayer: AddressLayerNone}
	}
	if errs := validateIngressNodes(base()); len(errs) != 0 {
		t.Errorf("no ingress nodes is allowed (CLI suggests the gw set): %v", errs)
	}
	o := base()
	o.IngressNodes = []string{"m1", "m2"}
	if errs := validateIngressNodes(o); len(errs) != 0 {
		t.Errorf("two distinct nodes must pass: %v", errs)
	}
	o = base()
	o.IngressNodes = []string{"m1", "m1"}
	if errs := validateIngressNodes(o); len(errs) == 0 {
		t.Error("a duplicated node must be reported")
	}
	o = base()
	o.IngressNodes = []string{"m1", "  "}
	if errs := validateIngressNodes(o); len(errs) == 0 {
		t.Error("a blank node name must be reported")
	}
	o = base()
	o.IngressAddressLayer = "cloud-lb"
	errs := validateIngressNodes(o)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "ingress-address-layer") {
		t.Errorf("an invalid layer must be reported on the flag surface: %v", errs)
	}
}

func TestCanonicalIngressNodes_OrderIndependentForTheHash(t *testing.T) {
	a := canonicalIngressNodes([]string{"m2", "m1", "m2", " "})
	b := canonicalIngressNodes([]string{"m1", "m2"})
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("flag order/dupes must not change the plan hash: %v vs %v", a, b)
	}
	if canonicalIngressNodes(nil) != nil {
		t.Error("empty set must stay nil so the plan JSON omits it")
	}
}

func TestIsLabelKey(t *testing.T) {
	ok := []string{"kube-dc.com/ingress", "ingress", "a", "a.b-c_d/x1"}
	bad := []string{"", "kube-dc.com/", "/ingress", "-x", "x-", "a/b/c", strings.Repeat("x", 64)}
	for _, s := range ok {
		if !isLabelKey(s) {
			t.Errorf("isLabelKey(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isLabelKey(s) {
			t.Errorf("isLabelKey(%q) = true, want false", s)
		}
	}
}

func TestPrefill_AddressLayerRoundTrips(t *testing.T) {
	// A flag-chosen layer must survive --save-config. Before this was
	// promoted to a dedicated prefill field, the flag never reached
	// o.Sets, so a saved spec silently omitted it and the next run fell
	// back to "none" — turning an intended VIP cluster into a ClusterIP
	// one with no error anywhere.
	o := &InitOptions{
		IngressAddressLayer: AddressLayerMetalLBL2,
		IngressNodes:        []string{"m2", "m1"},
		Sets:                map[string]string{},
	}
	m := ExportMap(o)
	if m["INGRESS_ADDRESS_LAYER"] != AddressLayerMetalLBL2 {
		t.Fatalf("export dropped the address layer: %v", m["INGRESS_ADDRESS_LAYER"])
	}
	// Node names are site-specific, so they ride the install-only
	// orchestration namespace (never inherited by a clone) and are sorted.
	if m[KeyIngressNodes] != "m1,m2" {
		t.Errorf("ingress nodes = %q, want sorted \"m1,m2\" under the init prefix", m[KeyIngressNodes])
	}

	back := &InitOptions{Sets: map[string]string{}}
	ImportMap(back, m, func(string) bool { return false })
	if back.IngressAddressLayer != AddressLayerMetalLBL2 {
		t.Errorf("import lost the layer: %q", back.IngressAddressLayer)
	}
	if strings.Join(back.IngressNodes, ",") != "m1,m2" {
		t.Errorf("import lost the ingress nodes: %v", back.IngressNodes)
	}
	// The layer must NOT also land in generic Sets — two sources of truth
	// for one value is how the export/import pair drifts.
	if _, dup := back.Sets["INGRESS_ADDRESS_LAYER"]; dup {
		t.Error("the address layer must be a dedicated field, not a generic Set")
	}
}

func TestValidate_RejectsAddressLayerViaSet(t *testing.T) {
	o := &InitOptions{
		IngressAddressLayer: AddressLayerNone,
		Sets:                map[string]string{"INGRESS_ADDRESS_LAYER": AddressLayerMetalLBL2},
	}
	if err := validateDNS01Flags(o); err == nil {
		t.Error("--set INGRESS_ADDRESS_LAYER must be rejected (dedicated flag exists)")
	}
}

func TestImportMap_LegacyIngressModeMapsToVIPLayerLoudly(t *testing.T) {
	// codex 2026-08-07 P1: a config written before the address layer carries
	// only INGRESS_MODE=metallb-lb. Defaulting it to the new "none" would turn
	// an intended VIP cluster into a ClusterIP one with no error anywhere.
	o := &InitOptions{Sets: map[string]string{}}
	ignored := ImportMap(o, map[string]string{"INGRESS_MODE": "metallb-lb"}, func(string) bool { return false })
	if o.IngressAddressLayer != AddressLayerMetalLBL2 {
		t.Fatalf("legacy metallb-lb must map to metallb-l2, got %q", o.IngressAddressLayer)
	}
	// The notice rides its OWN channel: `ignored` is printed as "ignored
	// non-input key(s)" and subtracted from the loaded count, so a translation
	// must never land there.
	if len(o.PrefillNotes) == 0 || !strings.Contains(strings.Join(o.PrefillNotes, ";"), "metallb-l2") {
		t.Errorf("the translation must be reported in PrefillNotes, got %v", o.PrefillNotes)
	}
	for _, ig := range ignored {
		if strings.Contains(ig, "translated") || strings.Contains(ig, "ingress-address-layer") {
			t.Errorf("translations must not pollute the ignored list: %q", ig)
		}
	}

	// hostnetwork meant host-bind with no VIP == the new default.
	o2 := &InitOptions{Sets: map[string]string{}}
	ImportMap(o2, map[string]string{"INGRESS_MODE": "hostnetwork"}, func(string) bool { return false })
	if o2.IngressAddressLayer != AddressLayerNone {
		t.Errorf("legacy hostnetwork must map to none, got %q", o2.IngressAddressLayer)
	}

	// An EXPLICIT layer always wins over the legacy key.
	o3 := &InitOptions{Sets: map[string]string{}}
	ImportMap(o3, map[string]string{
		"INGRESS_MODE": "metallb-lb", "INGRESS_ADDRESS_LAYER": AddressLayerNone,
	}, func(string) bool { return false })
	if o3.IngressAddressLayer != AddressLayerNone {
		t.Errorf("explicit layer must win over legacy INGRESS_MODE, got %q", o3.IngressAddressLayer)
	}
}

func TestAddressLayerRequiresVIP(t *testing.T) {
	for _, l := range []string{AddressLayerMetalLBL2, AddressLayerMetalLBBGP} {
		if !addressLayerRequiresVIP(l) {
			t.Errorf("%s must require a VIP", l)
		}
	}
	// Empty/unknown must be the SAFE no-VIP answer — this gate decides whether
	// MetalLB gets installed and whether METALLB_FLOATING_IP is demanded.
	for _, l := range []string{AddressLayerNone, "", "  ", "cloud-lb"} {
		if addressLayerRequiresVIP(l) {
			t.Errorf("%q must not require a VIP", l)
		}
	}
}

func TestAddressLayerEnv_NoLongerEmitsIngressMode(t *testing.T) {
	// INGRESS_MODE was being written as metallb-lb for EVERY layer, which made
	// layer=none install MetalLB and demand a VIP (codex P1 x2).
	for _, l := range []string{AddressLayerNone, AddressLayerMetalLBL2, AddressLayerMetalLBBGP} {
		if v, ok := addressLayerEnv(l)["INGRESS_MODE"]; ok {
			t.Errorf("%s still emits INGRESS_MODE=%q — the layer must be the sole driver", l, v)
		}
	}
}

func TestIngressCollidesWithAPIServer(t *testing.T) {
	// codex 2026-08-07 P1: the :6443 listener's fate depends on whether ENVOY
	// shares a node with kube-apiserver — a set intersection over the ingress
	// nodes — not on whether NODE_EXTERNAL_IP happens to equal a CP address.
	// Wrong in EITHER direction is harmful: keep the listener on a CP ingress
	// node and it silently never binds; remove it on dedicated workers and you
	// destroy working tenant kube-API SNI passthrough.
	cases := []struct {
		name                    string
		ingress, cp             []string
		wantCollides, wantDeriv bool
	}{
		{"CP-colocated ingress (small cluster)", []string{"m1", "m2"}, []string{"m1", "m2", "m3"}, true, true},
		{"dedicated ingress workers", []string{"w1", "w2"}, []string{"m1", "m2", "m3"}, false, true},
		{"one of several ingress nodes is CP", []string{"w1", "m2"}, []string{"m1", "m2"}, true, true},
		// No declared ingress set → not derivable; the caller must fall back to
		// the address heuristic rather than guess.
		{"no ingress nodes declared", nil, []string{"m1"}, false, false},
		{"no control-plane knowledge", []string{"w1"}, nil, false, false},
		{"whitespace is tolerated", []string{" m1 "}, []string{"m1"}, true, true},
	}
	for _, c := range cases {
		gotCollides, gotDeriv := IngressCollidesWithAPIServer(c.ingress, c.cp)
		if gotCollides != c.wantCollides || gotDeriv != c.wantDeriv {
			t.Errorf("%s: got (collides=%v derivable=%v), want (%v %v)",
				c.name, gotCollides, gotDeriv, c.wantCollides, c.wantDeriv)
		}
	}
}

func TestWriteAddons_LayerTransitionRefusesInsteadOfHalfApplying(t *testing.T) {
	// codex 2026-08-07: switching an already-scaffolded cluster to a no-VIP
	// layer used to return silently, leaving addons.yaml + addons-config.yaml
	// and the Gateway VIP patch WIRED — the cluster would claim layer=none
	// while Flux kept reconciling a MetalLB VIP.
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "c1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"),
		[]byte("INGRESS_ADDRESS_LAYER=none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Greenfield no-VIP: nothing wired yet → clean no-op.
	if err := WriteAddons(repo, "c1", nil); err != nil {
		t.Fatalf("greenfield none must be a clean no-op: %v", err)
	}
	// A generic addon layer is NOT proof of MetalLB. addons.yaml is shared by
	// rook-ceph, the SSO providers, Stripe billing and the Keycloak client
	// secret — refusing on its presence blocked most layer=none clusters for
	// no reason.
	if err := os.WriteFile(filepath.Join(dir, "addons.yaml"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addons", "kustomization.yaml"),
		[]byte("resources:\n  - ../../../addons/rook-ceph\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, "c1", nil); err != nil {
		t.Fatalf("a non-MetalLB addon must not be mistaken for a wired VIP: %v", err)
	}
	// Now simulate a prior MetalLB run: the proof is a reference to the
	// metallb source tree, which only this writer produces.
	if err := os.WriteFile(filepath.Join(dir, "addons", "kustomization.yaml"),
		[]byte("resources:\n  - ../../../addons/metallb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteAddons(repo, "c1", nil)
	if err == nil {
		t.Fatal("a metallb→none transition with wired artifacts must be refused")
	}
	if !strings.Contains(err.Error(), "addons/kustomization.yaml") {
		t.Errorf("the error must name the artifacts to remove, got: %v", err)
	}
	// The Gateway VIP patch is detected by MARKER, not by platform.yaml's
	// existence — platform.yaml is present on every cluster.
	if err := os.WriteFile(filepath.Join(dir, "addons", "kustomization.yaml"),
		[]byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform.yaml"),
		[]byte("spec:\n  patches: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, "c1", nil); err != nil {
		t.Fatalf("a plain platform.yaml must not read as a wired VIP: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform.yaml"),
		[]byte("spec:\n  patches:\n    "+gatewayVIPMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, "c1", nil); err == nil {
		t.Error("a marker-proven Gateway VIP patch must be refused under layer=none")
	}
}
