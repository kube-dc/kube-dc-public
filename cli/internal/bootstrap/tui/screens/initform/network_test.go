package initform

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func TestNetworkPatternRadiosAndProgressiveDetails(t *testing.T) {
	m := demoPanel(t)
	m.openSection("Network")
	m.focus = focusFields
	selected := func() int {
		n := 0
		for _, f := range m.currentFields() {
			if f.Kind == panelRadio && f.Get(m.st) == "yes" {
				n++
			}
		}
		return n
	}
	if selected() != 1 || m.st.Preset != "internal-only" {
		t.Fatal("legacy cloud preset changed")
	}
	for i, f := range m.currentFields() {
		if f.Action == "network-pattern:cloud+public-vlan" {
			m.fieldCursor = i
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if selected() != 1 || m.st.Preset != "cloud+public-vlan" {
		t.Fatal("radio selection not exclusive")
	}
	complete(t, m, m.startHostDiscovery())
	for _, f := range m.currentFields() {
		if strings.HasPrefix(f.Action, "nic:") || f.Label == "Cloud anchor mapping" {
			t.Fatal("details shown before expansion")
		}
	}
	if len(strings.Join(m.workflowText(), "\n")) > 170 {
		t.Fatal("network hint too long")
	}
	m.activateWorkflow("network-details")
	if !strings.Contains(strings.Join(m.workflowText(), "\n"), "default route") {
		t.Fatal("network evidence hidden after expansion")
	}
	m.activateWorkflow("network-advanced")
	found := false
	for _, f := range m.currentFields() {
		if f.Label == "Cloud anchor mapping" {
			found = true
		}
	}
	if !found {
		t.Fatal("advanced network fields missing")
	}
}

func TestNetworkSuggestionsRoundTripPerHostMappings(t *testing.T) {
	m := demoPanel(t)
	threeStorageHosts(m)
	m.st.NodeNICs = "outside-host=eno4"
	// A mapping without SSH access stays visible but cannot supply a suggestion.
	m.changed()
	complete(t, m, m.startHostDiscovery())
	m.selectSuggestedInterfaces()
	want := "outside-host=eno4,server-1=eth1,server-2=eth1,server-3=eth1"
	if m.st.NodeNICs != want || m.st.NetInterface != "eth0" {
		t.Fatalf("mapping/default changed incorrectly: %s %s", m.st.NodeNICs, m.st.NetInterface)
	}
	values, err := m.st.configMap()
	if err != nil {
		t.Fatal(err)
	}
	o := &clusterinit.InitOptions{}
	clusterinit.ImportMap(o, values, func(string) bool { return false })
	var loaded State
	loaded.FromOptions(o)
	if loaded.NodeNICs != want {
		t.Fatal("saved environment lost per-host mapping")
	}
	if m.discoveryBlocker() == "" {
		t.Fatal("unchecked network-only host escaped handoff check")
	}
}

func TestNetworkSuggestionExcludesStaleAmbiguousOrUntrustedHosts(t *testing.T) {
	for _, variant := range []string{"expired", "pin-change", "ambiguous", "duplicate-machine"} {
		t.Run(variant, func(t *testing.T) {
			m := demoPanel(t)
			threeStorageHosts(m)
			complete(t, m, m.startHostDiscovery())
			snapshot := m.workflow.hostSnapshots["server-1"]
			switch variant {
			case "expired":
				snapshot.Resources.Host.ObservedAt = time.Now().Add(-6 * time.Minute)
			case "pin-change":
				m.st.HostKeySHA256 = "SHA256:" + strings.Repeat("B", 43)
			case "ambiguous":
				snapshot.Resources.Host.Facts.NICs = append(snapshot.Resources.Host.Facts.NICs, setup.NICFact{Name: "eth2", MAC: "02:00:00:00:00:12", MTU: 1500})
				snapshot.Resources.Network.Links = append(snapshot.Resources.Network.Links, setup.NetworkLink{Name: "eth2", Kind: "ether", MAC: "02:00:00:00:00:12", MTU: 1500, State: "UP", Up: true})
			case "duplicate-machine":
				snapshot.Resources.Host.Facts.MachineID = m.workflow.hostSnapshots["server-2"].Resources.Host.Facts.MachineID
			}
			m.workflow.hostSnapshots["server-1"] = snapshot
			if m.suggestedInterfaces()["server-1"] != "" {
				t.Fatal("uncertain host received automatic mapping")
			}
		})
	}
}

func TestNetworkSelectionRejectsUnknownAndOwnedLinks(t *testing.T) {
	m := demoPanel(t)
	complete(t, m, m.startHostDiscovery())
	before := m.st.NodeNICs
	m.activateWorkflow("nic:missing")
	if m.st.NodeNICs != before {
		t.Fatal("fabricated NIC selected")
	}
	m.workflow.resources.Network.Links[1].Master = "bond0"
	m.activateWorkflow("nic:eth1")
	if m.st.NodeNICs != before {
		t.Fatal("bond member selected")
	}
}

func TestNetworkPatternsKeepIndependentIngressAndPublicConfig(t *testing.T) {
	for _, preset := range []string{"cloud-vlan", "cloud+public-vlan", "custom"} {
		for _, label := range platformAccessLabels {
			t.Run(preset+"/"+label, func(t *testing.T) {
				m := demoPanel(t)
				m.selectNetworkPattern(preset)
				m.st.selectPlatformAccess(label)
				m.st.PubVLANID, m.st.PubCIDR, m.st.PubGateway = "300", "198.51.100.0/24", "198.51.100.1"
				m.st.PubExclude1, m.st.PubExclude2 = "198.51.100.1..198.51.100.10", "198.51.100.11"
				m.st.GWNodes = "server-1"
				if m.st.IngressAddressLayer != "none" {
					m.st.MetalLBVIP = "198.51.100.10"
				}
				if m.st.IngressAddressLayer == "metallb-l2" {
					m.st.MetalLBInterface = "ext-pub-anchor"
				}
				if m.st.IngressAddressLayer == "metallb-bgp" {
					m.st.MetalLBLocalASN, m.st.MetalLBPeerASN, m.st.MetalLBPeerAddress = "64512", "64513", "192.0.2.1"
				}
				o, err := m.st.draftOptions()
				if err != nil {
					t.Fatal(err)
				}
				values := clusterinit.ExportMap(o)
				loaded := &clusterinit.InitOptions{}
				clusterinit.ImportMap(loaded, values, func(string) bool { return false })
				if !reflect.DeepEqual(values, clusterinit.ExportMap(loaded)) {
					t.Fatal("network environment round trip changed values")
				}
				if preset != "cloud-vlan" && loaded.Sets["EXT_PUBLIC_CIDR"] == "" {
					t.Fatal("custom/public pool lost on save")
				}
				if preset == "cloud-vlan" && loaded.Sets["EXT_PUBLIC_CIDR"] != "" {
					t.Fatal("inactive public pool exported")
				}
				if loaded.IngressAddressLayer != m.st.IngressAddressLayer {
					t.Fatal("tenant preset overwrote platform access")
				}
				if preset != "custom" {
					if err := loaded.Validate(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSwitchPlatformAccessClearsInactiveKnobs(t *testing.T) {
	m := demoPanel(t)
	m.st.selectPlatformAccess(platformAccessLabels[2])
	m.st.MetalLBVIP, m.st.MetalLBLocalASN, m.st.MetalLBPeerASN, m.st.MetalLBPeerAddress = "198.51.100.10", "64512", "64513", "192.0.2.1"
	m.st.selectPlatformAccess(platformAccessLabels[1])
	if m.st.MetalLBMode != "l2" || m.st.MetalLBLocalASN != "" || m.st.MetalLBVIP == "" {
		t.Fatal("BGP to L2 produced stale or lost fields")
	}
	m.st.selectPlatformAccess(platformAccessLabels[0])
	if m.st.MetalLBVIP != "" || m.st.MetalLBInterface != "" {
		t.Fatal("no-VIP choice retained active VIP fields")
	}
}

func TestImportedEnvironmentSwitchesReconcileOwnedNetworkDefaults(t *testing.T) {
	m := demoPanel(t)
	m.selectNetworkPattern("cloud+public-vlan")
	m.st.selectPlatformAccess(platformAccessLabels[1])
	m.st.MetalLBVIP, m.st.MetalLBInterface = "198.51.100.10", "ext-pub-anchor"
	m.st.GWNodes = "server-1"
	m.st.PubVLANID, m.st.PubCIDR, m.st.PubGateway = "300", "198.51.100.0/24", "198.51.100.1"
	m.st.PubExclude1, m.st.PubExclude2 = "198.51.100.1..198.51.100.11", "198.51.100.12"
	o, err := m.st.draftOptions()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := clusterinit.ResolvedEnvFor(o)
	if err != nil {
		t.Fatal(err)
	}
	values := clusterinit.ExportMap(o)
	for key, value := range resolved {
		values[key] = value
	}
	loaded := &clusterinit.InitOptions{}
	clusterinit.ImportMap(loaded, values, func(string) bool { return false })
	m.st = &State{}
	m.st.FromOptions(loaded)
	if m.st.ExtraSets["DEFAULT_EIP_NETWORK_TYPE"] != "public" || m.st.ExtraSets["ENVOY_SERVICE_TYPE"] != "LoadBalancer" {
		t.Fatal("fixture did not retain generated environment")
	}
	m.selectNetworkPattern("cloud-vlan")
	m.st.selectPlatformAccess(platformAccessLabels[0])
	next, err := m.st.draftOptions()
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	env, err := clusterinit.ResolvedEnvFor(next)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DEFAULT_GW_NETWORK_TYPE", "DEFAULT_EIP_NETWORK_TYPE", "DEFAULT_FIP_NETWORK_TYPE", "DEFAULT_SVC_LB_NETWORK_TYPE"} {
		if env[key] != "cloud" {
			t.Fatalf("%s retained absent public network: %s", key, env[key])
		}
	}
	if env["ENVOY_SERVICE_TYPE"] != "ClusterIP" || env["ENVOY_LB_CLASS"] != "null" || env["ENVOY_TRAFFIC_POLICY"] != "null" {
		t.Fatal("previous VIP service shape retained")
	}
	if env["EXT_NET_PUBLIC_ANCHOR_IPS"] != "" {
		t.Fatal("inactive public anchors retained")
	}
}
