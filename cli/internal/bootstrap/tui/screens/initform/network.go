package initform

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func (s *State) networkChoice() string {
	if s.Preset == string(clusterinit.PresetInternalOnly) {
		return string(clusterinit.PresetCloudVLAN)
	}
	return s.Preset
}

func (m *PanelModel) selectNetworkPattern(pattern string) {
	if m.st.Mode != string(clusterinit.ModeInstall) {
		m.notice = "Network changes require a separate migration for adopt or resume."
		return
	}
	if m.st.networkChoice() == pattern {
		return
	}
	if pattern != string(clusterinit.PresetCloudVLAN) && pattern != string(clusterinit.PresetCloudPublicVLAN) && pattern != string(clusterinit.PresetCustom) {
		return
	}
	m.st.Preset = pattern
	if pattern != string(clusterinit.PresetCustom) {
		for _, key := range []string{"DEFAULT_GW_NETWORK_TYPE", "DEFAULT_EIP_NETWORK_TYPE", "DEFAULT_FIP_NETWORK_TYPE", "DEFAULT_SVC_LB_NETWORK_TYPE"} {
			delete(m.st.ExtraSets, key)
		}
	}
	if pattern == string(clusterinit.PresetCloudVLAN) {
		for key := range m.st.ExtraSets {
			if strings.HasPrefix(key, "EXT_PUBLIC_") || strings.HasPrefix(key, "EXT_NET_PUBLIC_") {
				delete(m.st.ExtraSets, key)
			}
		}
	}
	m.changed()
	m.notice = "Tenant network selected. Confirm the address plan and switch configuration."
}

var platformAccessLabels = []string{"Node addresses / upstream NAT", "Floating VIP (L2)", "Routed VIP (BGP)"}
var platformAccessLayers = []string{clusterinit.AddressLayerNone, clusterinit.AddressLayerMetalLBL2, clusterinit.AddressLayerMetalLBBGP}

func (s *State) platformAccessLabel() string {
	for i, layer := range platformAccessLayers {
		if s.IngressAddressLayer == layer {
			return platformAccessLabels[i]
		}
	}
	return s.IngressAddressLayer
}

// A deliberate address-layer change clears only the prior layer's inactive
// knobs. Loading a conflicting file still reaches the normal validator.
func (s *State) selectPlatformAccess(label string) {
	for i, candidate := range platformAccessLabels {
		if label != candidate || s.IngressAddressLayer == platformAccessLayers[i] {
			continue
		}
		s.IngressAddressLayer = platformAccessLayers[i]
		for _, key := range []string{"ENVOY_SERVICE_TYPE", "ENVOY_LB_CLASS", "ENVOY_TRAFFIC_POLICY"} {
			delete(s.ExtraSets, key)
		}
		s.MetalLBMode = "l2"
		keys := []string{}
		if s.IngressAddressLayer == clusterinit.AddressLayerMetalLBBGP {
			s.MetalLBMode = "bgp"
			s.MetalLBInterface = ""
			keys = append(keys, "METALLB_INTERFACE")
		} else {
			s.MetalLBLocalASN, s.MetalLBPeerASN, s.MetalLBPeerAddress, s.MetalLBPeerPort, s.MetalLBHoldTime = "", "", "", "", ""
			keys = append(keys, "METALLB_BGP_LOCAL_ASN", "METALLB_BGP_PEER_ASN", "METALLB_BGP_PEER_ADDRESS", "METALLB_BGP_PEER_PORT", "METALLB_BGP_HOLD_TIME")
		}
		if s.IngressAddressLayer == clusterinit.AddressLayerNone {
			s.MetalLBVIP, s.MetalLBInterface = "", ""
			keys = append(keys, "METALLB_FLOATING_IP", "METALLB_INTERFACE")
		}
		for _, key := range keys {
			delete(s.ExplicitEmptySets, key)
			delete(s.ExtraSets, key)
		}
	}
}

func (s *State) networkValue(key string) string {
	if value, exists := s.ExtraSets[key]; exists {
		return value
	}
	spec, _ := clusterinit.SpecFor(clusterinit.Preset(s.Preset))
	return spec.Defaults[key]
}

func networkSetting(label, key, hint string) panelField {
	return panelField{Section: "Network", Label: label, Kind: panelText, Desc: hint + " (" + key + ")",
		Get: func(s *State) string { return s.networkValue(key) },
		Set: func(s *State, value string) {
			if s.ExtraSets == nil {
				s.ExtraSets = map[string]string{}
			}
			s.ExtraSets[key] = value
		},
		Validate: func(value string) error { return clusterinit.ValidateInputSpec(map[string]string{key: value}) }}
}

func (s *State) setNodeInterface(node, nic string) error {
	pairs, err := clusterinit.ParseSetPairs(splitComma(s.NodeNICs))
	if err != nil {
		return err
	}
	if pairs == nil {
		pairs = map[string]string{}
	}
	if nic == "" {
		delete(pairs, node)
	} else {
		pairs[node] = nic
	}
	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+pairs[key])
	}
	s.NodeNICs = strings.Join(values, ",")
	return nil
}

func (m *PanelModel) suggestedInterfaces() map[string]string {
	requests, err := m.resourceRequests()
	if err != nil {
		return nil
	}
	machines := map[string]int{}
	for _, req := range requests {
		machines[m.workflow.hostSnapshots[req.Host.ID].Resources.Host.Facts.MachineID]++
	}
	result := map[string]string{}
	for _, req := range requests {
		snapshot, exists := m.workflow.hostSnapshots[req.Host.ID]
		if !exists || snapshot.Err != nil || req.Host.HostKeySHA256 == "" || resourceIdentity(req) != resourceIdentity(snapshot.Request) || machines[snapshot.Resources.Host.Facts.MachineID] != 1 {
			continue
		}
		var choices []string
		for _, c := range setup.NetworkCandidates(snapshot.Resources, time.Now().UTC()) {
			if c.Suggested {
				choices = append(choices, c.Name)
			}
		}
		if len(choices) == 1 {
			result[req.Host.ID] = choices[0]
		}
	}
	return result
}

func (m *PanelModel) selectSuggestedInterfaces() {
	if m.st.Mode != string(clusterinit.ModeInstall) {
		m.notice = "Review the existing network before adopt or resume."
		return
	}
	choices := m.suggestedInterfaces()
	if len(choices) == 0 {
		m.notice = "No unambiguous interface suggestion. Inspect hosts and review network details."
		return
	}
	copy := *m.st
	for node, nic := range choices {
		if err := copy.setNodeInterface(node, nic); err != nil {
			m.notice = err.Error()
			return
		}
	}
	if copy.NetInterface == "" {
		copy.NetInterface = choices[copy.HostID]
	}
	*m.st = copy
	m.changed()
	m.notice = "Interface mapping selected. Confirm switch VLANs, then inspect again."
}

func (m *PanelModel) networkFields(base []panelField) []panelField {
	var fields []panelField
	for _, option := range []struct{ id, title, hint string }{
		{string(clusterinit.PresetCloudVLAN), "Cloud network", "One tenant external pool. Confirm its upstream gateway."},
		{string(clusterinit.PresetCloudPublicVLAN), "Cloud + public network", "Adds a public tenant address pool on the same provider trunk."},
		{string(clusterinit.PresetCustom), "Custom configuration", "Expert path. Review the complete environment and Fleet layers."},
	} {
		id := option.id
		fields = append(fields, panelField{Section: "Network", Label: option.title, Desc: option.hint, Kind: panelRadio, Action: "network-pattern:" + id,
			Get: func(s *State) string { return boolStr(s.networkChoice() == id) }})
	}
	fields = append(fields, panelField{Section: "Network", Label: "Inspect network hosts", Kind: panelAction, Action: "discover-hosts", Desc: "Read interfaces, link ownership, and routes over verified SSH. No host changes."})
	requests, _ := m.resourceRequests()
	var nodes []string
	for _, req := range requests {
		nodes = append(nodes, req.Host.ID)
	}
	if len(nodes) > 1 {
		fields = append(fields, panelField{Section: "Network", Label: "Inspect server", Kind: panelSelect, Options: nodes,
			Get: func(*State) string { return m.request().Host.ID }, Set: func(_ *State, node string) { m.selectInspectedHost(node) }, Desc: "Choose the cached host inventory shown in network details."})
	}
	if choices := m.suggestedInterfaces(); len(choices) > 0 && m.st.Mode == string(clusterinit.ModeInstall) {
		var mappings []string
		for _, node := range nodes {
			if nic := choices[node]; nic != "" {
				mappings = append(mappings, node+" → "+nic)
			}
		}
		fields = append(fields, panelField{Section: "Network", Label: "Use suggested interfaces", Kind: panelAction, Action: "network-suggest", Desc: strings.Join(mappings, ", ") + ". Confirm switch connectivity."})
	}
	for _, f := range base {
		if f.Section != "Network" {
			continue
		}
		if _, known := clusterinit.KnownSetting(f.Label); known {
			key := f.Label
			f.Validate = func(value string) error { return clusterinit.ValidateInputSpec(map[string]string{key: value}) }
		}
		switch f.Label {
		case "Preset", "MetalLB mode", "Per-node NIC overrides":
			continue
		case "EXT_NET_INTERFACE":
			f.Label, f.Desc = "Default interface", "Provider trunk or dedicated NIC. Existing management links require a reviewed host network design."
		case "EXT_NET_VLAN_ID":
			f.Label, f.Desc = "Cloud VLAN", "0 means untagged; otherwise enter the switch VLAN ID."
		case "KUBE_OVN_MASTER_NODES":
			f.Label, f.Desc = "Control-plane IPs", "Internal control-plane IPv4 addresses, separated by commas."
		case "Gateway nodes":
			f.Desc = "Kubernetes node names with access to the tenant external networks."
		case "Ingress nodes":
			f.Desc = "Empty uses gateway nodes. VIP ingress must run on gateway nodes."
		case "Front door":
			f.Label, f.Desc, f.Options = "Platform access", "Independent of tenant networks. Node addresses, an L2 VIP, or a router-advertised BGP VIP.", platformAccessLabels
			f.Get, f.Set = func(s *State) string { return s.platformAccessLabel() }, func(s *State, v string) { s.selectPlatformAccess(v) }
		case "MetalLB VIP":
			f.Label, f.Desc = "Platform VIP", "An allocated spare address. Confirm availability and the return route."
		case "MetalLB L2 interface":
			f.Label, f.Desc = "VIP interface", "Host-facing VIP segment. A public OVS VLAN uses its anchor interface."
		case "EXT_PUBLIC_VLAN_ID":
			f.Label, f.Desc = "Public VLAN", "Switch tag for the public tenant pool."
		case "EXT_PUBLIC_CIDR":
			f.Label, f.Desc = "Public CIDR", "Public address range routed by your provider."
		case "EXT_PUBLIC_GATEWAY":
			f.Label, f.Desc = "Public gateway", "Upstream router on the public segment."
		case "EXT_PUBLIC_EXCLUDE_IPS_1":
			f.Label, f.Desc = "Public reserved range 1", "Reserve router, host, VIP, and anchor addresses before tenant allocation."
		case "EXT_PUBLIC_EXCLUDE_IPS_2":
			f.Label, f.Desc = "Public reserved range 2", "Additional reserved address or start..end range."
		}
		if !m.workflow.networkAdvanced && (f.Label == "Gateway type" || f.Label == "Ingress nodes" || f.Label == "BGP peer port" || f.Label == "BGP hold time") {
			continue
		}
		fields = append(fields, f)
		if f.Label == "Default interface" {
			for _, node := range nodes {
				fields = append(fields, panelField{Section: "Network", Label: "Interface · " + node, Kind: panelText, Desc: "Per-server override. Empty uses Default interface.",
					Get: func(s *State) string {
						pairs, _ := clusterinit.ParseSetPairs(splitComma(s.NodeNICs))
						return pairs[node]
					},
					Set: func(s *State, value string) {
						if err := s.setNodeInterface(node, value); err != nil {
							m.notice = err.Error()
						}
					},
					Validate: func(value string) error {
						if value == "" {
							return nil
						}
						return clusterinit.ValidateInputSpec(map[string]string{clusterinit.KeyNodeNICs: node + "=" + value})
					}})
			}
			fields = append(fields, networkSetting("Cloud CIDR", "EXT_NET_CIDR", "Tenant external address pool"), networkSetting("Cloud gateway", "EXT_NET_GATEWAY", "Upstream router on the cloud segment"))
		}
	}
	advancedLabel := "More network settings"
	if m.workflow.networkAdvanced {
		advancedLabel = "Fewer network settings"
	}
	fields = append(fields, panelField{Section: "Network", Label: advancedLabel, Kind: panelAction, Action: "network-advanced", Desc: "MTU, reserved addresses, anchors, and private platform endpoints. All other keys remain in Configuration."})
	if m.workflow.networkAdvanced {
		for _, spec := range []struct{ label, key, hint string }{
			{"External MTU", "EXT_NET_MTU", "Confirm the full path MTU; local link MTU is not an end-to-end test"},
			{"Cloud reserved range", "EXT_NET_EXCLUDE_IPS", "Exclude router, host, anchor, and private platform addresses"},
			{"Ingress source CIDR", "INGRESS_HOST_CIDR", "Actual source addresses used by ingress nodes to reach platform services"},
			{"Cloud anchor mapping", "EXT_NET_ANCHOR_IPS", "NODE=IP/PREFIX, separated by commas; allocate addresses before use"},
			{"Cloud anchor interface", "EXT_NET_ANCHOR_INTERFACE", "Host interface that carries the cloud anchor addresses"},
			{"Require cloud anchors", "EXT_NET_ANCHOR_REQUIRED", "true requires one reviewed anchor for every gateway node"},
			{"Node egress NAT", "EXT_NET_NODE_EGRESS_ENABLED", "Site-specific true/false; requires anchors and a reviewed upstream NAT path"},
			{"Private API VIP", "KUBE_API_INTERNAL_VIP", "Reserve in the cloud pool and add to both global allowlists"},
			{"Private API enabled", "PLATFORM_ENDPOINT_KUBE_API_ENABLED", "true/false; requires its endpoint overlay and reserved VIP"},
			{"Private ingress VIP", "ENVOY_GATEWAY_INTERNAL_VIP", "Reserve in the cloud pool; review controller and tenant reachability"},
			{"Private ingress enabled", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED", "true/false; requires its endpoint overlay and reserved VIP"},
		} {
			fields = append(fields, networkSetting(spec.label, spec.key, spec.hint))
		}
		if clusterinit.PresetHasPublicNetwork(clusterinit.Preset(m.st.Preset)) {
			fields = append(fields,
				networkSetting("Public anchor mapping", "EXT_NET_PUBLIC_ANCHOR_IPS", "NODE=IP/PREFIX; derived spare addresses still need allocation approval"),
				networkSetting("Public anchor interface", "EXT_NET_PUBLIC_ANCHOR_INTERFACE", "OVS internal port; do not use a competing Linux VLAN interface"),
				networkSetting("Public anchor default route", "EXT_NET_PUBLIC_ANCHOR_DEFAULT_ROUTE", "true moves host default routes; requires an explicit host-IP map and recovery plan"))
		}
	}
	detailsLabel := "Show network details"
	if m.workflow.networkDetails {
		detailsLabel = "Hide network details"
	}
	fields = append(fields, panelField{Section: "Network", Label: detailsLabel, Kind: panelAction, Action: "network-details", Desc: "Show link ownership, addresses, routes, and individual interface choices."})
	if m.workflow.networkDetails && m.resourcesCurrent() {
		for _, c := range setup.NetworkCandidates(m.workflow.resources, time.Now().UTC()) {
			if c.Selectable {
				fields = append(fields, panelField{Section: "Network", Label: "Use " + c.Name + " on " + m.request().Host.ID, Kind: panelAction, Action: "nic:" + c.Name, Desc: c.Reason})
			}
		}
	}
	return fields
}

func (m *PanelModel) networkSummary() []string {
	if m.st.Preset == string(clusterinit.PresetCustom) {
		return []string{"Custom environment · review Fleet layers in Configuration and Review."}
	}
	choices := m.suggestedInterfaces()
	return []string{fmt.Sprintf("%d hosts with interface suggestions · confirm switch VLANs and gateway.", len(choices))}
}
