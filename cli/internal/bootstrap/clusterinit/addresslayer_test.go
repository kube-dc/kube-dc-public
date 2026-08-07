package clusterinit

import (
	"strings"
	"testing"
)

// Ingress design v2 (§5a): the data plane is identical on every cluster, so
// the ONLY front-door choice is who owns the address. These tests pin the
// derivation, because three places depend on agreeing about it — the fleet
// template's ${ENVOY_*} scalars, the plan preview, and the validator.

func TestAddressLayerEnv_DerivesTheEnvoyServiceShape(t *testing.T) {
	cases := map[string]struct{ svcType, lbClass, etp, metallb string }{
		AddressLayerNone: {"ClusterIP", "null", "Local", "l2"},
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
