package initform

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func (m *PanelModel) storageLayouts() []setup.StorageLayout {
	requests, err := m.resourceRequests()
	if err != nil {
		return nil
	}
	var hosts []setup.HostResources
	for _, req := range requests {
		snapshot, ok := m.workflow.hostSnapshots[req.Host.ID]
		if !ok || snapshot.Err != nil || resourceIdentity(req) != resourceIdentity(snapshot.Request) {
			continue
		}
		hosts = append(hosts, snapshot.Resources)
	}
	layouts := setup.SuggestStorageLayouts(hosts, m.request().Host.ID, time.Now().UTC())
	// A disk-mapping suggestion must reflect an explicit durability choice.
	if replicas, err := strconv.Atoi(m.st.CephReplicationSize); err == nil && replicas >= 1 && replicas <= 3 {
		for i := range layouts {
			layout := &layouts[i]
			if layout.Mode != clusterinit.RookCephMultiNode || string(layout.Mode) != m.st.OSMode || replicas > len(layout.Assignments) {
				continue
			}
			minimum := layout.Assignments[0].SizeBytes
			for _, a := range layout.Assignments {
				minimum = min(minimum, a.SizeBytes)
			}
			layout.Replicas = replicas
			layout.CapacityBytes = minimum / 5 * 4 / uint64(replicas) * uint64(len(layout.Assignments))
		}
	}
	return layouts
}

func (s *State) storageReplication() string {
	if s.OSMode == string(clusterinit.RookCephLocal) {
		return "1"
	}
	if s.OSMode != string(clusterinit.RookCephMultiNode) && s.OSMode != string(clusterinit.RookCephPVC) {
		return ""
	}
	if s.CephReplicationSize != "" {
		return s.CephReplicationSize
	}
	if s.OSMode == string(clusterinit.RookCephMultiNode) && len(s.storageDevices()) == 1 {
		return "1"
	}
	return "2"
}

func (s *State) storageDevices() map[string]string {
	devices := map[string]string{}
	for _, pair := range []string{s.CephNode1, s.CephNode2, s.CephNode3} {
		if node, device, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(node) != "" && strings.TrimSpace(device) != "" {
			devices[strings.TrimSpace(node)] = strings.TrimSpace(device)
		}
	}
	return devices
}

func (s *State) setStorageDevice(node, device string) error {
	devices := s.storageDevices()
	delete(devices, node)
	if device != "" {
		devices[node] = device
	}
	if len(devices) > 3 {
		return fmt.Errorf("Select at most three storage servers.")
	}
	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)
	s.CephNode1, s.CephNode2, s.CephNode3 = "", "", ""
	slots := []*string{&s.CephNode1, &s.CephNode2, &s.CephNode3}
	for i, name := range names {
		*slots[i] = name + "=" + devices[name]
	}
	return nil
}

func (s *State) storageChoice() string {
	switch s.OSMode {
	case string(clusterinit.RookCephMultiNode):
		return "raw"
	case string(clusterinit.RookCephPVC):
		return "pvc"
	case string(clusterinit.RookCephLocal):
		if s.OSDDevice == "" || strings.HasPrefix(s.OSDDevice, "loop") {
			return "file-one"
		}
		return "raw"
	}
	return ""
}

func (m *PanelModel) selectStorageMode(id string) {
	if m.st.Mode != string(clusterinit.ModeInstall) {
		m.notice = "Storage changes require a separate migration for adopt or resume."
		return
	}
	if m.st.storageChoice() == id {
		return
	}
	m.st.CephReplicationSize = ""
	switch id {
	case "raw":
		m.st.OSMode = string(clusterinit.RookCephMultiNode)
	case "file-one":
		m.st.OSMode = string(clusterinit.RookCephLocal)
		m.st.OSDNode, m.st.OSDDevice, m.st.OSDSizeGB = m.st.HostID, "loop0", ""
	case "pvc":
		m.st.OSMode = string(clusterinit.RookCephPVC)
	default:
		return
	}
	m.st.DisabledConsent = false
	m.changed()
	m.notice = "Storage layout selected. Configure only this layout."
}

func (m *PanelModel) storageFields() []panelField {
	var fields []panelField
	for _, option := range []struct{ id, title, hint string }{
		{"raw", "Server disks (default)", "One disk per server. Default: two copies; one on a single server."},
		{"file-one", "Evaluation file", "Uses shared filesystem space on one server. No redundancy."},
		{"pvc", "Provider volumes", "Uses a StorageClass that supplies raw block volumes."},
	} {
		id := option.id
		fields = append(fields, panelField{Section: "Storage", Label: option.title, Kind: panelRadio, Action: "storage-mode:" + id,
			Get: func(s *State) string { return boolStr(s.storageChoice() == id) }, Desc: option.hint})
	}
	fields = append(fields, panelField{Section: "Storage", Label: "Inspect storage hosts", Action: "discover-hosts", Kind: panelAction, Desc: "Read disk facts over verified SSH. No disk changes."})
	requests, _ := m.resourceRequests()
	var nodes []string
	for _, req := range requests {
		nodes = append(nodes, req.Host.ID)
	}
	if len(nodes) > 1 {
		fields = append(fields, panelField{Section: "Storage", Label: "Inspect server", Kind: panelSelect, Options: nodes,
			Get: func(*State) string { return m.request().Host.ID }, Set: func(_ *State, node string) { m.selectInspectedHost(node) }, Desc: "Choose the server shown in the disk list."})
	}
	if m.st.Mode == string(clusterinit.ModeInstall) {
		for _, layout := range m.storageLayouts() {
			if layout.ID != m.st.storageChoice() || len(layout.Assignments) == 0 {
				continue
			}
			fields = append(fields, panelField{Section: "Storage", Label: "Use suggested mapping", Kind: panelAction, Action: "storage-layout:" + layout.ID, Desc: storageLayoutDescription(layout)})
		}
	}
	if m.st.OSMode == string(clusterinit.RookCephMultiNode) {
		for _, node := range nodes {
			fields = append(fields, panelField{Section: "Storage", Label: "Disk · " + node, Kind: panelText,
				Get: func(s *State) string { return s.storageDevices()[node] }, Set: func(s *State, v string) {
					if err := s.setStorageDevice(node, v); err != nil {
						m.notice = err.Error()
					}
				},
				Validate: clusterinit.ValidateDeviceNameField, Desc: "Device name, for example sdb. Leave empty to exclude this server from storage."})
		}
	}
	label := "Show disk details"
	if m.workflow.storageDetails {
		label = "Hide disk details"
	}
	fields = append(fields, panelField{Section: "Storage", Label: label, Action: "storage-details", Kind: panelAction, Desc: "Show disk identities, usage checks, and discovery errors."})
	return fields
}

func storageLayoutDescription(layout setup.StorageLayout) string {
	var parts []string
	for _, a := range layout.Assignments {
		parts = append(parts, a.Node+" → "+a.Device)
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(" · about %.0f GiB · %d data copies", float64(layout.CapacityBytes)/(1<<30), layout.Replicas)
}

func (m *PanelModel) selectStorageLayout(id string) {
	if m.st.Mode != string(clusterinit.ModeInstall) {
		m.notice = "Storage proposals apply to a fresh install. Review existing storage before adopt or resume."
		return
	}
	for _, layout := range m.storageLayouts() {
		if layout.ID != id {
			continue
		}
		if m.st.OSMode != string(layout.Mode) {
			m.st.CephReplicationSize = ""
		}
		m.st.OSMode = string(layout.Mode)
		m.st.DisabledConsent = false
		m.st.CephNode1, m.st.CephNode2, m.st.CephNode3 = "", "", ""
		m.st.OSDNode, m.st.OSDDevice, m.st.OSDSizeGB = "", "", ""
		if layout.Mode == clusterinit.RookCephLocal {
			m.st.OSDNode = layout.Assignments[0].Node
			m.st.OSDDevice = strings.TrimPrefix(layout.Assignments[0].Device, "/dev/")
			if layout.BackingSizeGiB > 0 {
				m.st.OSDSizeGB = strconv.Itoa(layout.BackingSizeGiB)
				if m.st.ExtraSets == nil {
					m.st.ExtraSets = map[string]string{}
				}
				m.st.ExtraSets["CEPH_LOCAL_OSD_BACKING_FILE"] = "/var/lib/ceph-osd-block.img"
			}
		}
		if layout.Mode == clusterinit.RookCephMultiNode {
			slots := []*string{&m.st.CephNode1, &m.st.CephNode2, &m.st.CephNode3}
			for i, assignment := range layout.Assignments {
				*slots[i] = assignment.Node + "=" + strings.TrimPrefix(assignment.Device, "/dev/")
			}
		}
		m.changed()
		m.notice = "Mapping selected. Inspect again before installation."
		return
	}
	m.notice = "The proposal is no longer supported by fresh observations. Inspect storage hosts again."
}

func (m *PanelModel) storageSummary() []string {
	if m.st.OSMode == string(clusterinit.RookDisabled) {
		return []string{"Storage disabled: metrics, logs, and Grafana backups are unavailable."}
	}
	if m.st.storageChoice() == "raw" {
		count := len(m.st.storageDevices())
		if m.st.OSMode == string(clusterinit.RookCephLocal) && m.st.OSDNode != "" {
			count = 1
		}
		if count == 0 {
			return []string{"Inspect hosts, then select a disk mapping."}
		}
		line := fmt.Sprintf("%d storage server(s) · %s data copies", count, m.st.storageReplication())
		if m.st.storageReplication() == "1" {
			line += " · no redundancy"
		} else {
			line += " · host loss can interrupt storage"
		}
		return []string{line}
	}
	if m.st.storageChoice() == "file-one" {
		return []string{"Evaluation only · shared disk space · no redundancy"}
	}
	return []string{"Verify block-volume support and placement before installation."}
}
