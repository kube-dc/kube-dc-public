package initform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func tenHostInputs(t *testing.T) *clusterinit.InitOptions {
	t.Helper()
	env, err := config.LoadEnv(filepath.Join("..", "..", "..", "clusterinit", "testdata", "variants", "ten-host-bond.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clusterinit.ValidateInputSpec(env.AsMap()); err != nil {
		t.Fatal(err)
	}
	o := &clusterinit.InitOptions{}
	clusterinit.ImportMap(o, env.AsMap(), func(string) bool { return false })
	// The panel accepts only credential files present on this workstation.
	dir := secureDir(t)
	o.TLSCert, o.TLSKey, o.TrustedCABundle = dir+"/cert.pem", dir+"/key.pem", dir+"/ca.pem"
	return o
}

func TestTenHostSiteRoundTripAndUnrelatedEdit(t *testing.T) {
	o := tenHostInputs(t)
	o.PAYG = true // an explicit install input, separate from derived billing state
	o.VMGoldens = []string{"debian-12"}
	want := clusterinit.ExportMap(o)
	var state State
	state.FromOptions(o)
	if len(state.CephNodes) != 10 || state.CephMonNodes != "server-01,server-02,server-03" {
		t.Fatal("host list or monitor placement truncated")
	}
	var scratch clusterinit.InitOptions
	if err := state.Apply(&scratch); err != nil {
		t.Fatal(err)
	}
	// The form supplies a default for blank GW type and GPU mode. Explicit site
	// inputs must retain their values even when the default form gains fields.
	got := clusterinit.ExportMap(&scratch)
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s was lost or changed", key)
		}
	}
	next, err := state.editedConfig("EXT_NET_MTU", "1450", false)
	if err != nil {
		t.Fatal(err)
	}
	values, err := next.configMap()
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if key != "EXT_NET_MTU" && values[key] != value {
			t.Errorf("unrelated edit changed %s", key)
		}
	}
	// Runtime consent and credentials must not be reconstructed from a draft.
	scratch.Yes, scratch.NoPush, scratch.GitHubToken = true, true, "runtime-only"
	if err := next.Apply(&scratch); err != nil {
		t.Fatal(err)
	}
	if !scratch.Yes || !scratch.NoPush || scratch.GitHubToken != "runtime-only" {
		t.Fatal("input restoration changed runtime state")
	}
	next, err = next.editedConfig(clusterinit.InitPrefix+"SITE_COMPONENTS", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Apply(&scratch); err != nil || scratch.SiteComponents != "" {
		t.Fatal("removed component leaked into accepted options")
	}
}

func TestFleetStorageVariantRoundTrips(t *testing.T) {
	for _, variant := range []string{"ten-raw", "multi-device", "provider-pvc", "evaluation", "local-ha"} {
		t.Run(variant, func(t *testing.T) {
			o := tenHostInputs(t)
			switch variant {
			case "multi-device":
				o.SetCephDevices(map[string][]string{"server-01": {"sdb", "sdc"}, "server-02": {"sdb", "sdc"}})
				o.Sets["CEPH_REPLICATION_SIZE"] = "2"
			case "provider-pvc":
				o.CephNodes, o.RookMode = nil, clusterinit.RookCephPVC
				o.CephStorageClass, o.CephOSDCount, o.CephOSDVolumeSizeGB = "provider-block", 5, 400
				delete(o.Sets, "CEPH_MON_NODES")
				o.Sets["CEPH_REPLICATION_SIZE"] = "2"
			case "evaluation":
				o.CephNodes, o.RookMode = nil, clusterinit.RookCephLocal
				o.RookOSDNode, o.RookOSDDevice, o.RookOSDSizeGB = "server-01", "loop0", 100
				o.Sets["CEPH_REPLICATION_SIZE"] = "1"
				delete(o.Sets, "CEPH_MON_NODES")
			case "local-ha":
				o.ServicesHAStorageClass, o.ServicesHAStorageBudget = "services-local", "128Gi"
			}
			var state State
			state.FromOptions(o)
			var restored clusterinit.InitOptions
			if err := state.Apply(&restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(o.ObjectStorage().DeviceSets(), restored.ObjectStorage().DeviceSets()) {
				t.Fatal("device lists changed")
			}
			for key, value := range clusterinit.ExportMap(o) {
				if clusterinit.ExportMap(&restored)[key] != value {
					t.Errorf("%s changed", key)
				}
			}
			text, err := clusterinit.RenderSpec(&restored)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "site.env")
			if err := clusterinit.WriteSpec(&restored, path); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("workstation draft is not private")
			}
			env, err := config.LoadEnv(path)
			if err != nil {
				t.Fatal(err)
			}
			var reloaded clusterinit.InitOptions
			clusterinit.ImportMap(&reloaded, env.AsMap(), func(string) bool { return false })
			again, _ := clusterinit.RenderSpec(&reloaded)
			if text != again {
				t.Fatal("saved file is not stable")
			}
			plan, err := clusterinit.BuildPlan(&restored, clusterinit.FleetState{})
			if err != nil {
				t.Fatal(err)
			}
			reloadedPlan, err := clusterinit.BuildPlan(&reloaded, clusterinit.FleetState{})
			if err != nil || plan.PlanHash != reloadedPlan.PlanHash {
				t.Fatal("file reload changed the resolved plan")
			}
			if variant == "multi-device" && !strings.Contains(state.EquivalentFlags(&restored), "--ceph-devices=") {
				t.Fatal("equivalent command omitted the full topology")
			}
		})
	}
}

func TestReviewOnlyCannotReturnInstallOptions(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	path := filepath.Join(t.TempDir(), "draft.env")
	m.ConfigureServices(PanelServices{ReviewOnly: true})
	m.draftPath = path
	m.openSection("Review")
	m.workflow.attempted = true
	if m.primaryBlocker() != "" {
		t.Fatal("missing inspection evidence blocks saving a review-only draft")
	}
	m.primaryAction()
	if m.Applied() {
		t.Fatal("review-only action accepted installation")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("review-only action did not save the draft")
	}
	m.applied = true // Result is a separate guard against a future action bug.
	if _, err := m.Result(&clusterinit.InitOptions{}); err == nil {
		t.Fatal("review-only returned install options")
	}
}

func TestNetworkSiteVariantsPreserveAdvancedInputs(t *testing.T) {
	for _, variant := range []string{"bonded-cloud", "separate-nics", "cloud-and-public", "routed-ingress"} {
		t.Run(variant, func(t *testing.T) {
			o := tenHostInputs(t)
			switch variant {
			case "separate-nics":
				o.NodeNICs = map[string]string{"server-01": "eno4", "server-10": "enp3s0"}
			case "cloud-and-public":
				o.Preset = clusterinit.PresetCloudPublicVLAN
				for k, v := range map[string]string{"EXT_PUBLIC_VLAN_ID": "230", "EXT_PUBLIC_CIDR": "198.51.100.0/24", "EXT_PUBLIC_GATEWAY": "198.51.100.1", "EXT_PUBLIC_EXCLUDE_IPS_1": "198.51.100.1..198.51.100.15"} {
					o.Sets[k] = v
				}
			case "routed-ingress":
				o.IngressAddressLayer = clusterinit.AddressLayerMetalLBBGP
				for k, v := range map[string]string{"METALLB_MODE": "bgp", "METALLB_INTERFACE": "", "METALLB_BGP_LOCAL_ASN": "64512", "METALLB_BGP_PEER_ASN": "64513", "METALLB_BGP_PEER_ADDRESS": "192.0.2.254", "METALLB_BGP_PEER_PORT": "179", "METALLB_BGP_HOLD_TIME": "90s"} {
					o.Sets[k] = v
				}
			}
			var state State
			state.FromOptions(o)
			var out clusterinit.InitOptions
			if err := state.Apply(&out); err != nil {
				t.Fatal(err)
			}
			for k, v := range clusterinit.ExportMap(o) {
				if clusterinit.ExportMap(&out)[k] != v {
					t.Errorf("network variant lost %s", k)
				}
			}
			if out.SiteComponents != o.SiteComponents || out.ProviderHost != o.ProviderHost {
				t.Fatal("network edit lost the private endpoint or Git authority")
			}
		})
	}
}

func TestTenHostStorageScreenAtTerminalSizes(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 50}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			o := tenHostInputs(t)
			m := NewEmbeddedPanel(o, "", nil)
			m.ConfigureServices(PanelServices{})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.openSection("Storage")
			m.focus = focusFields
			seen, radios := map[string]bool{}, 0
			for i, field := range m.currentFields() {
				if field.Kind == panelRadio && field.Get(m.st) == "yes" {
					radios++
				}
				m.fieldCursor = i
				body := ansi.Strip(m.View().Content)
				for _, line := range strings.Split(body, "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatal("screen exceeds terminal width")
					}
				}
				if strings.HasPrefix(field.Label, "Disk · ") {
					seen[field.Label] = strings.Contains(body, strings.TrimPrefix(field.Label, "Disk · "))
				}
			}
			if radios != 1 || len(seen) != 10 {
				t.Fatalf("selected radios=%d, disk rows=%d", radios, len(seen))
			}
			for label, visible := range seen {
				if !visible {
					t.Errorf("cannot scroll to %s", label)
				}
			}
		})
	}
}

func TestLegacyDemoDraftMigratesStorageRows(t *testing.T) {
	var state State
	if err := json.Unmarshal([]byte(`{"Name":"saved","CephNode1":"one=sdb","CephNode2":"two=sdc"}`), &state); err != nil {
		t.Fatal(err)
	}
	if strings.Join(state.CephNodes, ",") != "one=sdb,two=sdc" || state.Name != "saved" {
		t.Fatal("old draft was not migrated")
	}
}

func TestSecondDiskCannotReuseFirstDiskReadiness(t *testing.T) {
	m := demoPanel(t)
	threeStorageHosts(m)
	complete(t, m, m.startHostDiscovery())
	m.selectStorageLayout("raw")
	complete(t, m, m.startHostDiscovery())
	if reason := m.discoveryBlocker(); reason != "" {
		t.Fatal(reason)
	}
	if err := m.st.setStorageDevice("server-1", "sdb,sdc"); err != nil {
		t.Fatal(err)
	}
	m.changed()
	if reason := m.discoveryBlocker(); !strings.Contains(reason, "per-device evidence") {
		t.Fatal("second disk reused first-disk readiness")
	}
	if _, err := m.st.configMap(); err != nil {
		t.Fatal("blocked installation also blocked saving the draft")
	}
}

func TestLegacyMoreCephNodesDraftMigration(t *testing.T) {
	var state State
	body := `{"CephNode1":"one=sdb","CephNode2":"two=sdc","CephNode3":"three=sdb","CephNodesMore":"four=sdc,five=sdb"}`
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.CephNodes) != 5 || state.CephNodes[4] != "five=sdb" {
		t.Fatal("legacy draft lost storage hosts")
	}
}

func TestReviewOnlySaveStillBlocksUnresolvedCredentialReferences(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	m.ConfigureServices(PanelServices{ReviewOnly: true})
	m.secCursor = sectionIndex(m, "Review")
	m.st.DroppedCredentialRefs = []string{keyTrustedCA}
	if !strings.Contains(m.primaryBlocker(), keyTrustedCA) {
		t.Fatal("review save hides missing credential file")
	}
	m.st.resolveDroppedCredentialRef(keyTrustedCA)
	if got := m.primaryBlocker(); got != "" {
		t.Fatal("resolved file still blocks review save", got)
	}
}
