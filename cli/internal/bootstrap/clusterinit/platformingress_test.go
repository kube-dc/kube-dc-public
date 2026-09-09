package clusterinit

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The scaffold must hand the manager the front-door address whenever it can
// ESTABLISH one that sits inside the injected routes, and must stay quiet
// (empty key, no warning) when the pod reaches it over the tenant route. It
// must never seed an address nobody dials — a 1:1-NAT arrival address, a
// sibling's stale VIP, an overlay VIP whose DNS rewrite it cannot see — and it
// must never silently discard or overrule an explicit value. Every branch below
// is a file that installs cleanly; only the seeded value separates a working
// managed control plane from four parked apiserver pods (lab cluster,
// 2026-09-08).
func TestScaffold_ReconcilesPlatformIngressVIP(t *testing.T) {
	seed := func(t *testing.T, extra string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cluster-config.env")
		body := `# Cluster: pilot
CLUSTER_NAME=pilot
DOMAIN=pilot.example.com
EXT_NET_NAME=ext-cloud
EXT_NET_VLAN_ID=163
EXT_NET_INTERFACE=bond0
POD_CIDR=10.100.0.0/16
` + extra
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write seed: %v", err)
		}
		return path
	}
	planFor := func(layer string) *Plan {
		return &Plan{Preset: PresetCloudVLAN, ClusterName: "pilot", IngressAddressLayer: layer}
	}
	base := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0"}
	// The same two steps Scaffold runs, in order: post-process (MetalLB
	// seeding), then the reconcile that needs this cluster's resolved node
	// address and SingleIPNAT.
	run := func(t *testing.T, path string, plan *Plan, sets map[string]string, nodeCIDR, nodeIP string, nat bool) (string, string) {
		t.Helper()
		if err := postProcessClusterConfig(path, plan, sets, nodeCIDR); err != nil {
			t.Fatalf("post-process: %v", err)
		}
		var out bytes.Buffer
		if err := reconcilePlatformIngressVIP(path, nodeIP, nat, sets, "pilot", &out); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		body, _ := os.ReadFile(path)
		return string(body), out.String()
	}
	// What add-cluster.sh writes under a no-VIP layer: dead MetalLB
	// placeholders (blanked by post-process) and the address the CLI handed it
	// as BOTH the declared node address and the arrival address — under 1:1
	// NAT that is already the post-NAT address, indistinguishable in the file.
	none := func(nodeIP string) string {
		return "INGRESS_ADDRESS_LAYER=none\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nNODE_EXTERNAL_IP=" + nodeIP + "\nKUBE_API_ARRIVAL_IP=" + nodeIP + "\n"
	}
	seededAs := func(file, vip string) bool { return strings.Contains(file, KeyPlatformIngressVIP+"="+vip+"\n") }
	metallbSets := func(vip string) map[string]string {
		return map[string]string{
			"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0",
			"METALLB_FLOATING_IP": vip, "METALLB_INTERFACE": "bond0",
		}
	}

	t.Run("none, NAT-free, node address inside the node route: seeded", func(t *testing.T) {
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), base, "192.168.110.0/24", "192.168.110.180", false)
		if !seededAs(file, "192.168.110.180") {
			t.Fatalf("front door not seeded:\n%s", file)
		}
		if !strings.Contains(report, KeyPlatformIngressVIP+"=192.168.110.180") || strings.Contains(report, "WARNING") {
			t.Fatalf("seeding not announced: %q", report)
		}
	})
	t.Run("none behind 1:1 NAT: the arriving address is not the front door — warn, do not seed", func(t *testing.T) {
		file, report := run(t, seed(t, none("192.168.110.11")), planFor(AddressLayerNone), base, "192.168.110.0/24", "192.168.110.11", true)
		if !seededAs(file, "") {
			t.Fatalf("NAT arrival address seeded as the front door:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, KeyPlatformIngressVIP) {
			t.Fatalf("unestablished front door not warned about: %q", report)
		}
	})
	t.Run("none, cloned file carries a sibling's arrival address: seed THIS cluster's address", func(t *testing.T) {
		// The imported .180 is a valid IP, so no resolver rewrites it; the
		// destination node is .181. The scaffold must derive from what it
		// resolved for this cluster, not from the inherited field.
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), base, "192.168.110.0/24", "192.168.110.181", false)
		if !seededAs(file, "192.168.110.181") {
			t.Fatalf("sibling's arrival address won over this cluster's node address:\n%s", file)
		}
		if strings.Contains(report, "WARNING") {
			t.Fatalf("unexpected warning: %q", report)
		}
	})
	t.Run("none with the platform-endpoint overlay on: nothing establishable — warn, do not seed", func(t *testing.T) {
		// Enabling the overlay creates the VIP; pointing bao. at it is a manual
		// DNS rewrite the CLI cannot see. Neither the internal VIP nor the node
		// address may be seeded on the strength of the flag alone.
		file, report := run(t, seed(t, none("192.168.110.11")+"PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true\nENVOY_GATEWAY_INTERNAL_VIP=192.168.110.31\n"),
			planFor(AddressLayerNone), base, "192.168.110.0/24", "192.168.110.11", false)
		if !seededAs(file, "") {
			t.Fatalf("overlay topology seeded a guess:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "192.168.110.31") || !strings.Contains(report, "step 6") {
			t.Fatalf("overlay warning must name the internal VIP and the flow step: %q", report)
		}
	})
	t.Run("none, declared address outside the routes (undetected NAT): empty and quiet", func(t *testing.T) {
		file, report := run(t, seed(t, none("203.0.113.10")), planFor(AddressLayerNone), base, "192.168.110.0/24", "203.0.113.10", false)
		if !seededAs(file, "") {
			t.Fatalf("key must exist, empty:\n%s", file)
		}
		if report != "" {
			t.Fatalf("tenant-route front door must not be announced or warned about: %q", report)
		}
	})
	t.Run("explicit --set is kept, and a disagreement with the established address is called out", func(t *testing.T) {
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: "192.168.110.7"}
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.180", false)
		if !seededAs(file, "192.168.110.7") {
			t.Fatalf("explicit --set overridden:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "192.168.110.7") || !strings.Contains(report, "192.168.110.180") {
			t.Fatalf("explicit/established disagreement not called out: %q", report)
		}
	})
	t.Run("explicit --set equal to the established address is announced without warning", func(t *testing.T) {
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: "192.168.110.180"}
		_, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.180", false)
		if strings.Contains(report, "WARNING") || !strings.Contains(report, KeyPlatformIngressVIP+"=192.168.110.180") {
			t.Fatalf("agreeing explicit value mis-reported: %q", report)
		}
	})
	t.Run("explicit empty --set is a decline: normalised to none, written empty, called out when it costs reach", func(t *testing.T) {
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: ""}
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.180", false)
		if !seededAs(file, "") || strings.Contains(file, KeyPlatformIngressVIP+"="+PlatformIngressVIPNone+"\n") {
			t.Fatalf("operator-emptied key was re-seeded or left as the sentinel:\n%s", file)
		}
		// The node address IS inside the routes, so declining the rule costs
		// reach: that is a WARNING.
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "192.168.110.180") {
			t.Fatalf("declined rule that costs reach not called out: %q", report)
		}
	})
	t.Run("explicit empty --set on an established in-route MetalLB VIP scaffolds (no must-be-set abort) and is called out", func(t *testing.T) {
		// Before the normalisation the final validation saw an empty key with
		// an established VIP inside the routes and aborted the scaffold before
		// the reconcile could honour the decision (codex review 2026-09-08,
		// pass 4).
		sets := metallbSets("198.51.100.7")
		sets[KeyPlatformIngressVIP] = ""
		path := seed(t, "INGRESS_ADDRESS_LAYER=metallb-l2\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nKUBE_API_ARRIVAL_IP=CHANGEME\n")
		file, report := run(t, path, planFor(AddressLayerMetalLBL2), sets, "198.51.100.0/24", "198.51.100.7", false)
		if !seededAs(file, "") {
			t.Fatalf("declined rule re-seeded or sentinel leaked:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "198.51.100.7") {
			t.Fatalf("decline that costs reach not called out: %q", report)
		}
	})
	t.Run("none sentinel with dual-homing off is still normalised to empty in the file", func(t *testing.T) {
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: PlatformIngressVIPNone}
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), sets, "", "192.168.110.180", false)
		if !seededAs(file, "") || strings.Contains(file, KeyPlatformIngressVIP+"="+PlatformIngressVIPNone+"\n") || report != "" {
			t.Fatalf("sentinel survived in the file or was reported with dual-homing off: %q\n%s", report, file)
		}
	})
	t.Run("none sentinel: persisted decline, written empty, called out only when it costs reach", func(t *testing.T) {
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: PlatformIngressVIPNone}
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.180", false)
		if !seededAs(file, "") || strings.Contains(file, KeyPlatformIngressVIP+"="+PlatformIngressVIPNone+"\n") {
			t.Fatalf("sentinel must be written as EMPTY for the manager:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, PlatformIngressVIPNone) {
			t.Fatalf("decline that costs reach not called out: %q", report)
		}
		// Declared address outside the routes: nothing to grant, decline is free.
		file, report = run(t, seed(t, none("203.0.113.10")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "203.0.113.10", false)
		if !seededAs(file, "") || report != "" {
			t.Fatalf("free decline was reported: %q\n%s", report, file)
		}
	})
	t.Run("explicit value where nothing is establishable is kept but flagged unverifiable", func(t *testing.T) {
		// 1:1 NAT detected, an inherited key: the exact shape a clone leaves
		// behind. Announcing it as success would hide a blocked front door.
		sets := map[string]string{"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0", KeyPlatformIngressVIP: "192.168.110.180"}
		file, report := run(t, seed(t, none("192.168.110.11")), planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.11", true)
		if !seededAs(file, "192.168.110.180") {
			t.Fatalf("explicit value discarded:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "cannot be verified") {
			t.Fatalf("unverifiable explicit value announced as success: %q", report)
		}
		// Same under the overlay.
		file, report = run(t, seed(t, none("192.168.110.11")+"PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true\nENVOY_GATEWAY_INTERNAL_VIP=192.168.110.31\n"),
			planFor(AddressLayerNone), sets, "192.168.110.0/24", "192.168.110.11", false)
		if !seededAs(file, "192.168.110.180") || !strings.Contains(report, "cannot be verified") {
			t.Fatalf("overlay + explicit value mis-reported: %q\n%s", report, file)
		}
	})
	t.Run("none with the overlay VIP OUTSIDE the routes: quiet — nothing to grant, and nothing to instruct", func(t *testing.T) {
		file, report := run(t, seed(t, none("192.168.110.11")+"PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true\nENVOY_GATEWAY_INTERNAL_VIP=100.65.0.31\n"),
			planFor(AddressLayerNone), base, "192.168.110.0/24", "192.168.110.11", false)
		if !seededAs(file, "") || report != "" {
			t.Fatalf("unroutable overlay VIP produced a seed or an instruction the manager would refuse: %q\n%s", report, file)
		}
	})
	t.Run("dual-homing off leaves the key empty and silent", func(t *testing.T) {
		file, report := run(t, seed(t, none("192.168.110.180")), planFor(AddressLayerNone), base, "", "192.168.110.180", false)
		if !seededAs(file, "") || report != "" {
			t.Fatalf("dual-homing off must not seed or report: %q\n%s", report, file)
		}
	})

	// MetalLB layers: the script seeds KUBE_API_ARRIVAL_IP=CHANGEME and
	// post-process resolves it to the announced VIP. The front-door derivation
	// has to run AFTER that resolution — run before it, the key stays empty and
	// the final validation then aborts every MetalLB greenfield scaffold whose
	// VIP sits inside the node route (codex review 2026-09-08, HIGH #1).
	for _, tc := range []struct {
		name  string
		layer string
		sets  map[string]string
	}{
		{"metallb-l2", AddressLayerMetalLBL2, map[string]string{
			"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0",
			"METALLB_FLOATING_IP": "198.51.100.7", "METALLB_INTERFACE": "bond0",
		}},
		{"metallb-bgp", AddressLayerMetalLBBGP, map[string]string{
			"EXT_NET_VLAN_ID": "163", "EXT_NET_INTERFACE": "bond0",
			"METALLB_FLOATING_IP":   "198.51.100.7",
			"METALLB_BGP_LOCAL_ASN": "64512", "METALLB_BGP_PEER_ADDRESS": "198.51.100.1", "METALLB_BGP_PEER_ASN": "64513",
		}},
	} {
		t.Run(tc.name+" greenfield: placeholder arrival resolved, then seeded", func(t *testing.T) {
			path := seed(t, "INGRESS_ADDRESS_LAYER="+tc.layer+"\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nKUBE_API_ARRIVAL_IP=CHANGEME\n")
			file, report := run(t, path, planFor(tc.layer), tc.sets, "198.51.100.0/24", "198.51.100.7", false)
			if !strings.Contains(file, "KUBE_API_ARRIVAL_IP=198.51.100.7\n") {
				t.Fatalf("arrival address not resolved to the VIP:\n%s", file)
			}
			if !seededAs(file, "198.51.100.7") {
				t.Fatalf("front door not seeded from the announced VIP:\n%s", file)
			}
			if strings.Contains(report, "WARNING") {
				t.Fatalf("unexpected warning: %q", report)
			}
		})
	}
	t.Run("metallb-l2 with a sibling's stale arrival address: warn, do not seed", func(t *testing.T) {
		// A clone carried KUBE_API_ARRIVAL_IP=.180 (a valid IP, so it is not
		// re-resolved) while this cluster's VIP is .181. Seeding .180 would
		// pass every check and leave the real front door blocked.
		path := seed(t, "INGRESS_ADDRESS_LAYER=metallb-l2\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nKUBE_API_ARRIVAL_IP=198.51.100.180\n")
		file, report := run(t, path, planFor(AddressLayerMetalLBL2), metallbSets("198.51.100.181"), "198.51.100.0/24", "198.51.100.181", false)
		if strings.Contains(file, KeyPlatformIngressVIP+"=198.51.100.180") {
			t.Fatalf("sibling's arrival address seeded as this cluster's front door:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") {
			t.Fatalf("mismatched arrival/VIP not warned about: %q", report)
		}
	})
	t.Run("metallb-l2 with a sibling's key carried as explicit input: kept, but called out", func(t *testing.T) {
		// Clone-from-sibling imports the key like any operator config. The
		// scaffold writes it as given and warns that it disagrees with the
		// front door this cluster establishes.
		sets := metallbSets("198.51.100.181")
		sets[KeyPlatformIngressVIP] = "198.51.100.180"
		path := seed(t, "INGRESS_ADDRESS_LAYER=metallb-l2\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nKUBE_API_ARRIVAL_IP=CHANGEME\n")
		file, report := run(t, path, planFor(AddressLayerMetalLBL2), sets, "198.51.100.0/24", "198.51.100.181", false)
		if !seededAs(file, "198.51.100.180") {
			t.Fatalf("explicit value discarded:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "198.51.100.181") {
			t.Fatalf("sibling value not called out against the established front door: %q", report)
		}
	})
	t.Run("metallb-l2 clone carrying BOTH the sibling's arrival address and its key: unverifiable, never announced", func(t *testing.T) {
		// The real clone shape: KUBE_API_ARRIVAL_IP=.180 survives (valid IP)
		// so nothing is establishable against the new VIP .181, and the
		// inherited key .180 must not ride that gap into a success message.
		sets := metallbSets("198.51.100.181")
		sets[KeyPlatformIngressVIP] = "198.51.100.180"
		path := seed(t, "INGRESS_ADDRESS_LAYER=metallb-l2\nMETALLB_FLOATING_IP=CHANGEME\nMETALLB_INTERFACE=CHANGEME\nKUBE_API_ARRIVAL_IP=198.51.100.180\n")
		file, report := run(t, path, planFor(AddressLayerMetalLBL2), sets, "198.51.100.0/24", "198.51.100.181", false)
		if !seededAs(file, "198.51.100.180") {
			t.Fatalf("explicit value discarded:\n%s", file)
		}
		if !strings.Contains(report, "WARNING") || !strings.Contains(report, "cannot be verified") || strings.Contains(report, "is inside the infra routes;") {
			t.Fatalf("inherited key announced as success: %q", report)
		}
	})
}

// An explicit EMPTY --set of the front-door key is a decision, and it has to
// read as one at every boundary an input crosses — not only where the
// scaffold writes the file. Preflight validation reads EnvMapFor; the
// reviewed-plan hash reads o.Sets; --save-config reads ExportMap. Each of
// them must see `none` for an explicit empty, and an ABSENT key must stay
// absent (that is "let the scaffold decide", a different input) — codex
// review 2026-09-08, pass 5.
func TestPlatformIngressVIP_ExplicitEmptyIsADecisionEverywhere(t *testing.T) {
	// Preflight: the map every validator sees carries the sentinel, so an
	// established in-route MetalLB VIP no longer trips "must be set" on an
	// explicit decline.
	preflight, err := EnvMapFor(PresetCloudVLAN, map[string]string{
		"EXT_NET_INTERFACE":        "bond0",
		"EXT_NET_VLAN_ID":          "163",
		"INFRA_ATTACHMENT_ENABLED": "true",
		"INFRA_ATTACHMENT_ROUTES":  "192.168.110.0/24,172.30.0.0/22,10.100.0.0/16",
		"INGRESS_ADDRESS_LAYER":    "metallb-l2",
		"METALLB_FLOATING_IP":      "192.168.110.180",
		"KUBE_API_ARRIVAL_IP":      "192.168.110.180",
		KeyPlatformIngressVIP:      "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preflight[KeyPlatformIngressVIP] != PlatformIngressVIPNone {
		t.Fatalf("EnvMapFor kept the explicit empty as %q", preflight[KeyPlatformIngressVIP])
	}
	var errs []string
	validateInfraAttachment(preflight, &errs)
	if got := strings.Join(errs, "; "); strings.Contains(got, KeyPlatformIngressVIP) {
		t.Fatalf("preflight rejected an explicit decline: %s", got)
	}
	// Absent stays absent — and IS demanded, because nothing was decided.
	absent, _ := EnvMapFor(PresetCloudVLAN, map[string]string{
		"EXT_NET_INTERFACE":        "bond0",
		"EXT_NET_VLAN_ID":          "163",
		"INFRA_ATTACHMENT_ENABLED": "true",
		"INFRA_ATTACHMENT_ROUTES":  "192.168.110.0/24,172.30.0.0/22,10.100.0.0/16",
		"INGRESS_ADDRESS_LAYER":    "metallb-l2",
		"METALLB_FLOATING_IP":      "192.168.110.180",
		"KUBE_API_ARRIVAL_IP":      "192.168.110.180",
	})
	if _, present := absent[KeyPlatformIngressVIP]; present {
		t.Fatal("EnvMapFor invented the key for an absent input")
	}
	errs = nil
	validateInfraAttachment(absent, &errs)
	if got := strings.Join(errs, "; "); !strings.Contains(got, KeyPlatformIngressVIP+"=192.168.110.180") {
		t.Fatalf("absent key with an established in-route front door not demanded: %q", got)
	}

	// Plan hash: "" and `none` are the same input; absent is a different one.
	base := func(sets map[string]string) *InitOptions {
		return &InitOptions{Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10", Preset: PresetCloudVLAN, Sets: sets}
	}
	emptyExplicit := base(map[string]string{"EXT_NET_INTERFACE": "enp1s0", KeyPlatformIngressVIP: ""})
	sentinel := base(map[string]string{"EXT_NET_INTERFACE": "enp1s0", KeyPlatformIngressVIP: PlatformIngressVIPNone})
	absentKey := base(map[string]string{"EXT_NET_INTERFACE": "enp1s0"})
	if !reflect.DeepEqual(emptyExplicit.inputsForHash(), sentinel.inputsForHash()) {
		t.Fatal("an explicit empty and its saved form hash differently: --save-config → --apply-plan would drift")
	}
	if reflect.DeepEqual(absentKey.inputsForHash(), sentinel.inputsForHash()) {
		t.Fatal("an absent key hashes like a decline")
	}
	if emptyExplicit.Sets[KeyPlatformIngressVIP] != "" {
		t.Fatal("inputsForHash mutated the caller's Sets")
	}

	// Persistence: the saved form IS the sentinel (covered in detail by
	// TestExportImport_PlatformIngressVIPOverridesSurvive).
	if got := ExportMap(emptyExplicit)[KeyPlatformIngressVIP]; got != PlatformIngressVIPNone {
		t.Fatalf("ExportMap saved the explicit empty as %q", got)
	}
}
