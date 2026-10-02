package initform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
)

// PanelServices supplies operations, not a second form. Both bootstrap entry
// points use this PanelModel, State, field schema, validation, and Apply mapping.
type PanelServices struct {
	Context          context.Context
	Discover         func(context.Context, setup.ResourceRequest) (setup.HostResources, error)
	Demo             *setupdemo.Coordinator
	DraftPath        string
	Session          SessionControl
	SessionDirectory string
}

type panelWorkflow struct {
	services         PanelServices
	generation       uint64
	cancel           context.CancelFunc
	busy             bool
	resources        setup.HostResources
	resourceHash     string
	resourceErr      error
	attempted        bool
	review           setupdemo.Review
	reviewHash       string
	reviewedHash     string
	scenario         string
	phase            string
	result           string
	resultErr        error
	hostSnapshots    map[string]hostSnapshot
	inspectedHost    string
	storageDetails   bool
	networkDetails   bool
	networkAdvanced  bool
	sessionStatus    setup.CoordinatorStatus
	sessionInputHash string
	sessionInspected bool
}

// PanelEvent is addressed to the originating panel and generation. Root routes
// it even when another tab is active; an old panel/result can never update a new one.
type PanelEvent struct {
	Owner         *PanelModel
	generation    uint64
	kind, hash    string
	resources     setup.HostResources
	sessionStatus setup.CoordinatorStatus
	review        setupdemo.Review
	result        string
	err           error
	hostSnapshots map[string]hostSnapshot
}

func (m *PanelModel) ConfigureServices(s PanelServices) {
	if s.Context == nil {
		s.Context = context.Background()
	}
	m.workflow = &panelWorkflow{services: s, scenario: "success", hostSnapshots: map[string]hostSnapshot{}}
	if s.Session != nil {
		m.workflow.sessionInputHash = m.stateHash()
	}
	m.rebuildWorkflowFields()
}

func (m *PanelModel) Close() {
	if w := m.workflow; w != nil {
		w.generation++
		if w.cancel != nil {
			w.cancel()
		}
		w.busy = false
	}
}

func (m *PanelModel) Busy() bool { return m.workflow != nil && m.workflow.busy }

func (m *PanelModel) request() setup.ResourceRequest {
	node := m.st.HostID
	if m.workflow != nil && m.workflow.inspectedHost != "" {
		requests, _ := m.resourceRequests()
		for _, req := range requests {
			if req.Host.ID == m.workflow.inspectedHost {
				return req
			}
		}
	}
	return m.requestForHost(node)
}

func (m *PanelModel) requestForHost(node string) setup.ResourceRequest {
	h := setup.Host{ID: m.st.HostID, SSHAlias: m.st.SSHHost, Role: "server", Primary: true, HostKeySHA256: m.st.HostKeySHA256, ManagementAddress: m.st.ManagementAddress, NIC: m.st.NetInterface}
	if node != m.st.HostID {
		targets, _ := clusterinit.ParseSetPairs(splitComma(m.st.NodeSSHHosts))
		keys, _ := clusterinit.ParseSetPairs(splitComma(m.st.NodeSSHHostKeys))
		h = setup.Host{ID: node, SSHAlias: targets[node], HostKeySHA256: keys[node], Role: "agent", NIC: m.st.NetInterface}
	}
	for _, pair := range splitComma(m.st.NodeNICs) {
		node, nic, ok := strings.Cut(pair, "=")
		if ok && node == h.ID {
			h.NIC = nic
		}
	}

	if m.st.OSMode == string(clusterinit.RookCephLocal) && m.st.OSDNode == h.ID {
		h.Disk = "/dev/" + strings.TrimPrefix(nonEmpty(m.st.OSDDevice, "loop0"), "/dev/")
	}
	if m.st.OSMode == string(clusterinit.RookCephMultiNode) {
		for _, pair := range []string{m.st.CephNode1, m.st.CephNode2, m.st.CephNode3} {
			node, disk, ok := strings.Cut(pair, "=")
			if ok && node == h.ID {
				h.Disk = "/dev/" + disk
			}
		}
	}
	return setup.ResourceRequest{Host: h, Intent: clusterinit.Mode(m.st.Mode), KVMRequired: !m.st.NoKubeVirt && !m.st.AllowNoKubevirtEligible, PlatformInit: true}
}

func fingerprint(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (m *PanelModel) stateHash() string {
	return fingerprint(struct {
		State    *State
		Scenario string
	}{m.st, m.workflow.scenario})
}

func (m *PanelModel) changed() {
	m.manualScroll = false
	w := m.workflow
	if w == nil {
		return
	}
	m.Close()
	w.reviewHash, w.reviewedHash, w.phase, w.result = "", "", "", ""
	if w.resourceHash != "" && w.resourceHash != fingerprint(m.request()) {
		w.resourceHash = ""
		w.resources = setup.HostResources{}
		w.resourceErr = nil
	}
	m.restoreInspectedHost()
	m.rebuildWorkflowFields()
}

func (m *PanelModel) handlePanelEvent(e PanelEvent) {
	w := m.workflow
	if w == nil || e.Owner != m || e.generation != w.generation {
		return
	}
	w.busy = false
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	switch e.kind {
	case "session":
		if e.hash != m.stateHash() {
			return
		}
		if e.err == nil {
			w.sessionStatus = e.sessionStatus
			w.sessionInspected = true
			m.notice = "Session ownership updated. Installation remains blocked."
		} else {
			w.sessionInspected = false
		}

	case "discover":
		if e.hash != fingerprint(m.request()) {
			return
		}
		w.resources, w.resourceErr, w.resourceHash = e.resources, e.err, e.hash
		w.hostSnapshots[m.request().Host.ID] = hostSnapshot{Request: m.request(), Resources: e.resources, Err: e.err}
		m.notice = "Host observations updated. Select interfaces and disks explicitly."
	case "discover-hosts":
		requests, err := m.resourceRequests()
		if err != nil || e.hash != fingerprint(requests) {
			return
		}
		w.hostSnapshots = e.hostSnapshots
		m.restoreInspectedHost()
		m.notice = "Storage hosts inspected. Review a layout or select disks per server."
	case "check":
		if e.hash != m.stateHash() {
			return
		}
		w.review, w.reviewHash, w.resourceErr = e.review, e.hash, e.err
		m.notice = "Readiness updated. Review the evidence."
	case "run":
		if e.hash != m.stateHash() {
			return
		}
		w.phase, w.result, w.resultErr = "Install", e.result, e.err
		m.rebuildWorkflowFields()
		m.openSection("Install")
	}
	if e.err != nil {
		m.notice = e.err.Error()
	}
	m.rebuildWorkflowFields()
}

func (m *PanelModel) startDiscovery() tea.Cmd {
	w := m.workflow
	if w == nil || w.services.Discover == nil || w.busy {
		return nil
	}
	req := m.request()
	if req.Host.SSHAlias == "" {
		m.notice = "Enter the SSH host in Basics first."
		return nil
	}
	m.Close()
	w.busy, w.attempted = true, true
	w.resources, w.resourceHash, w.reviewHash, w.reviewedHash = setup.HostResources{}, "", "", ""
	ctx, cancel := context.WithTimeout(w.services.Context, 35*time.Second)
	w.cancel = cancel
	e := PanelEvent{Owner: m, generation: w.generation, kind: "discover", hash: fingerprint(req)}
	discover := w.services.Discover
	return func() tea.Msg { e.resources, e.err = discover(ctx, req); return e }
}

func (m *PanelModel) startReadiness() tea.Cmd {
	w := m.workflow
	if w == nil || w.busy {
		return nil
	}
	m.openSection("Readiness")
	if w.services.Demo == nil {
		return m.startDiscovery()
	}
	if errs := m.validationErrors(); len(errs) > 0 {
		m.notice = "Correct the settings before checking readiness."
		return nil
	}
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		m.notice = err.Error()
		return nil
	}
	m.Close()
	w.busy = true
	w.reviewHash, w.reviewedHash = "", ""
	ctx, cancel := context.WithCancel(w.services.Context)
	w.cancel = cancel
	req, scenario, demo := m.requestForHost(m.st.HostID), w.scenario, w.services.Demo
	e := PanelEvent{Owner: m, generation: w.generation, kind: "check", hash: m.stateHash()}
	return func() tea.Msg { e.review, e.err = demo.Check(ctx, o, req.Host, scenario); return e }
}

func (m *PanelModel) activateWorkflow(action string) tea.Cmd {
	w := m.workflow
	if w == nil || w.busy {
		return nil
	}
	switch {
	case strings.HasPrefix(action, "session-"):
		return m.startSession(action)
	case action == "discover-hosts":
		return m.startHostDiscovery()
	case strings.HasPrefix(action, "network-pattern:"):
		m.selectNetworkPattern(strings.TrimPrefix(action, "network-pattern:"))
		return nil
	case action == "network-suggest":
		m.selectSuggestedInterfaces()
		return nil
	case action == "network-details":
		w.networkDetails = !w.networkDetails
		m.rebuildWorkflowFields()
		return nil
	case action == "network-advanced":
		w.networkAdvanced = !w.networkAdvanced
		m.rebuildWorkflowFields()
		return nil
	case strings.HasPrefix(action, "storage-mode:"):
		m.selectStorageMode(strings.TrimPrefix(action, "storage-mode:"))
		return nil
	case action == "storage-details":
		w.storageDetails = !w.storageDetails
		m.rebuildWorkflowFields()
		return nil
	case strings.HasPrefix(action, "storage-layout:"):
		m.selectStorageLayout(strings.TrimPrefix(action, "storage-layout:"))
		return nil
	case action == "discover":
		return m.startDiscovery()
	case action == "check":
		return m.startReadiness()
	case action == "review":
		if w.services.Demo != nil && (!w.review.CanRun || w.reviewHash != m.stateHash() || w.resourceErr != nil) {
			m.notice = "Run readiness checks and resolve findings before Review."
			return nil
		}
		w.reviewedHash = w.reviewHash
		m.openSection("Review")
	case action == "demo-pin":
		m.st.HostKeySHA256 = setupdemo.HostKey
		m.changed()
		m.notice = "Synthetic fingerprint restored."
	case action == "scenario":
		w.scenario = cycleOption([]string{"success", "blocked", "failed", "stopped"}, w.scenario)
		m.changed()
	case action == "run":
		if w.services.Demo == nil || !w.review.CanRun || w.reviewedHash == "" || w.reviewedHash != w.reviewHash || w.reviewHash != m.stateHash() {
			m.notice = "Check and review the current inputs before simulation."
			return nil
		}
		m.Close()
		w.busy = true
		ctx, cancel := context.WithCancel(w.services.Context)
		w.cancel = cancel
		demo, scenario := w.services.Demo, w.scenario
		e := PanelEvent{Owner: m, generation: w.generation, kind: "run", hash: m.stateHash()}
		return func() tea.Msg { e.result, e.err = demo.Run(ctx, scenario); return e }
	case action == "verify":
		w.phase = "Verify"
		m.openSection("Verify")
	case action == "result":
		w.phase = "Result"
		m.openSection("Result")
	case action == "continue":
		w.phase = "Continue"
		m.openSection("Continue")
	case strings.HasPrefix(action, "nic:"):
		if m.st.Mode != string(clusterinit.ModeInstall) {
			m.notice = "Review the existing network before adopt or resume."
			return nil
		}
		if err := clusterinit.ValidateK8sNodeNameField(m.request().Host.ID); err != nil || m.request().Host.ID == "" {
			m.notice = "Enter the actual node name in Hosts before assigning hardware."
			return nil
		}
		if !m.resourcesCurrent() {
			m.notice = "Discover this host again before selecting an interface."
			return nil
		}
		nic := strings.TrimPrefix(action, "nic:")
		found := false
		for _, c := range setup.NetworkCandidates(w.resources, time.Now().UTC()) {
			if c.Name == nic && c.Selectable {
				found = true
			}
		}
		if !found {
			m.notice = "This interface is unavailable. Inspect again and review link ownership."
			return nil
		}
		if err := m.st.setNodeInterface(m.request().Host.ID, nic); err != nil {
			m.notice = err.Error()
			return nil
		}
		m.changed()
		m.notice = "Tenant interface selected. Run checks again."
	case strings.HasPrefix(action, "disk:"):
		node := m.request().Host.ID
		if err := clusterinit.ValidateK8sNodeNameField(node); err != nil || node == "" {
			m.notice = "Enter the actual node name in Hosts before assigning hardware."
			return nil
		}
		if !m.resourcesCurrent() {
			m.notice = "Discover this host again before selecting a disk."
			return nil
		}
		path := strings.TrimPrefix(action, "disk:")
		foundDisk := false
		for _, disk := range w.resources.Disks {
			if disk.Path == path {
				foundDisk = true
				if disk.InUse || !disk.Checked {
					m.notice = "This disk is in use or its identity and usage checks are incomplete. Discover again or choose another disk."
					return nil
				}
			}
		}
		if !foundDisk {
			m.notice = "The disk is not in this host's current inventory."
			return nil
		}
		device := strings.TrimPrefix(path, "/dev/")
		if m.st.OSMode == string(clusterinit.RookCephLocal) {
			m.st.OSDNode, m.st.OSDDevice = node, device
		} else if m.st.OSMode == string(clusterinit.RookCephMultiNode) {
			slots := []*string{&m.st.CephNode1, &m.st.CephNode2, &m.st.CephNode3}
			found := false
			for _, slot := range slots {
				assignedNode, _, ok := strings.Cut(*slot, "=")
				if ok && assignedNode == node {
					*slot = node + "=" + device
					found = true
					break
				}
			}
			if !found {
				for _, slot := range slots {
					if *slot == "" {
						*slot = node + "=" + device
						found = true
						break
					}
				}
			}
			if !found {
				m.notice = "All Ceph node slots are filled. Edit the intended node explicitly."
				return nil
			}
		} else {
			m.notice = "Choose local or multi-node raw Ceph storage before selecting a disk."
			return nil
		}
		m.changed()
		m.notice = "Disk selected; its identity, holders, mounts, and signatures must be checked again."
	}
	m.rebuildWorkflowFields()
	return nil
}

func (m *PanelModel) resourcesCurrent() bool {
	w := m.workflow
	return w != nil && w.resourceErr == nil && w.resourceHash == fingerprint(m.request()) && !w.resources.Host.ObservedAt.IsZero() && time.Since(w.resources.Host.ObservedAt) >= 0 && time.Since(w.resources.Host.ObservedAt) < 5*time.Minute
}

func (m *PanelModel) openSection(name string) {
	for i, s := range m.visibleSections() {
		if s == name {
			m.secCursor, m.fieldCursor, m.focus = i, 0, focusFields
			m.fieldsVP.SetYOffset(0)
			m.manualScroll = false
			return
		}
	}
}

func (m *PanelModel) rebuildWorkflowFields() {
	w := m.workflow
	if w == nil {
		return
	}
	current := m.currentSection()
	base := panelFields()
	var fields []panelField
	storageAdded := false
	networkAdded := false
	action := func(section, label, id, desc string) panelField {
		return panelField{Section: section, Label: label, Action: id, Kind: panelAction, Desc: desc}
	}
	hostFields := []panelField{
		{Section: "Hosts", Label: "Primary node name", Kind: panelText, Get: func(s *State) string { return s.HostID }, Set: func(s *State, v string) { s.HostID = v }, Validate: clusterinit.ValidateK8sNodeNameField, Desc: "The Kubernetes node name for per-node NIC and disk assignments."},
		{Section: "Hosts", Label: "Primary server host key", Kind: panelText, Get: func(s *State) string { return s.HostKeySHA256 }, Set: func(s *State, v string) {
			fingerprint, _ := clusterinit.NormalizeSSHHostKeyInput(v) // validated before Set
			s.HostKeySHA256 = fingerprint
		}, Validate: func(v string) error {
			_, err := clusterinit.NormalizeSSHHostKeyInput(v)
			return err
		}, Desc: "From a trusted console, paste /etc/ssh/ssh_host_ed25519_key.pub or its SHA256 fingerprint. Your ~/.ssh/id_ed25519.pub is a login key."},
		{Section: "Hosts", Label: "Primary management IP", Kind: panelText, Get: func(s *State) string { return s.ManagementAddress }, Set: func(s *State, v string) { s.ManagementAddress = v }, Validate: clusterinit.ValidateNodeIPField, Desc: "The address on this host used for management. Discovery does not select it."},
		{Section: "Hosts", Label: "Other SSH targets", Kind: panelText, Get: func(s *State) string { return s.NodeSSHHosts }, Set: func(s *State, v string) { s.NodeSSHHosts = v }, Validate: func(v string) error {
			return clusterinit.ValidateInputSpec(map[string]string{clusterinit.KeyNodeSSHHosts: v})
		}, Desc: "Additional network or storage hosts: NODE=user@host, separated by commas. Each target uses strict known_hosts."},
		{Section: "Hosts", Label: "Other host keys", Kind: panelText, Get: func(s *State) string { return s.NodeSSHHostKeys }, Set: func(s *State, v string) { s.NodeSSHHostKeys = v }, Validate: func(v string) error {
			return clusterinit.ValidateInputSpec(map[string]string{clusterinit.KeyNodeSSHHostKeys: v})
		}, Desc: "SHA256 host-key pins for additional hosts: NODE=SHA256:fingerprint."},
		action("Hosts", "Discover host resources", "discover", "Read the displayed server over verified SSH. Choose it with Inspect server in Network or Storage. No host changes."),
	}
	if w.services.Demo != nil {
		hostFields = append(hostFields, action("Hosts", "Demo scenario", "scenario", "Enter cycles success, blocked, failed, stopped."), action("Hosts", "Restore demo fingerprint", "demo-pin", "Use the synthetic fingerprint for this fixture."))
	}
	for _, f := range base {
		if f.Section == "Network" {
			if !networkAdded {
				fields = append(fields, m.networkFields(base)...)
				networkAdded = true
			}
			continue
		}
		if f.Section == "Storage" && !storageAdded {
			fields = append(fields, m.storageFields()...)
			storageAdded = true
		}
		if f.Section == "Fleet" && (len(fields) == 0 || fields[len(fields)-1].Section == "Basics") {
			fields = append(fields, hostFields...)
		}
		if f.Section == "Review" {
			if w.services.Session != nil {
				f.Label = "Inspect installation session"
				f.Action = "session-inspect"
				f.Desc = "Use the saved, reviewed session. Installation remains blocked."
			}
			fields = append(fields, action("Readiness", "Check readiness", "check", "Refresh observations. Local host checks do not qualify a complete installation."), action("Readiness", "Review current settings", "review", "Review the settings after checking them."))
			if w.services.Demo != nil {
				f.Label, f.Action, f.Desc = "Run simulation", "run", "Run synthetic operations with the reviewed settings."
			}
		}
		if f.Section == "Storage" && (f.Label == "Object storage" || strings.HasPrefix(f.Label, "Ceph node ")) {
			continue
		}
		fields = append(fields, f)
	}
	if m.resourcesCurrent() {
		for _, d := range w.resources.Disks {
			if m.st.storageChoice() != "raw" || !w.storageDetails {
				continue
			}
			label := fmt.Sprintf("Use %s · %.0f GiB", d.Path, float64(d.SizeBytes)/(1<<30))
			if d.InUse {
				label = fmt.Sprintf("Reserved %s · %.0f GiB", d.Path, float64(d.SizeBytes)/(1<<30))
			} else if !d.Checked {
				label = "Unchecked " + d.Path
			}
			desc := m.request().Host.ID + " · " + d.Media + " · " + d.Model
			if d.Checked && !d.InUse {
				desc += " · " + d.StableID + ". Select to update this server's assignment. No disk operations run; inspect again before installation."
			} else {
				desc += ". Selection unavailable. " + d.Reason
			}
			fields = append(fields, action("Storage", label, "disk:"+d.Path, desc))
		}
	}
	if w.services.Demo != nil {
		for _, v := range []struct{ section, label, id string }{{"Install", "Inspect verification", "verify"}, {"Verify", "Show result", "result"}, {"Result", "Continue", "continue"}, {"Continue", "Check again", "check"}} {
			if w.phase != "" {
				fields = append(fields, action(v.section, v.label, v.id, "Synthetic operations only; no cluster changes."))
			}
		}
	}
	if w.services.Session != nil {
		fields = append(fields, m.sessionFields()...)
	}
	m.fields = fields
	for i, s := range m.visibleSections() {
		if s == current {
			m.secCursor = i
			break
		}
	}
	m.clampCursors()
}

// Demo drafts persist the entire shared State. They never become init .env
// specs, carry no readiness authorization, and are written only on explicit save.
type demoDraft struct {
	SchemaVersion int    `json:"schemaVersion"`
	DemoOnly      bool   `json:"demoOnly"`
	State         State  `json:"state"`
	Scenario      string `json:"scenario"`
}

func (m *PanelModel) LoadDemoDraft(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := demoDraft{}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&d); err != nil {
		return err
	}
	if d.SchemaVersion != 2 || !d.DemoOnly {
		return fmt.Errorf("expected a version 2 integrated demo draft; old standalone drafts are not compatible")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected data after demo draft")
	}
	if m.workflow == nil || m.workflow.services.Demo == nil {
		return errors.New("demo draft requires simulated operations")
	}
	m.st = &d.State
	m.workflow.scenario = d.Scenario
	m.changed()
	return nil
}
func (m *PanelModel) saveDemoDraft() {
	path := m.workflow.services.DraftPath
	if path == "" {
		m.notice = "Use --draft PATH to save the demo."
		return
	}
	data, err := json.MarshalIndent(demoDraft{2, true, *m.st, m.workflow.scenario}, "", "  ")
	if err == nil {
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(path), ".kube-dc-demo-*")
		if err == nil {
			defer os.Remove(f.Name())
			_, err = f.Write(append(data, '\n'))
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(f.Name(), path)
			}
		}
	}
	if err != nil {
		m.notice = "Save failed: " + err.Error()
	} else {
		m.notice = "Saved full demo draft: " + path
	}
}

// SetDemoHostDefaults seeds the SAME form for fixture testing. No alternate
// field schema or demo-specific editor is constructed.
func (m *PanelModel) SetDemoHostDefaults() {
	m.st.HostID = "server-1"
	m.st.HostKeySHA256 = setupdemo.HostKey
	m.st.ManagementAddress = "192.0.2.10"
	m.st.DisabledConsent = false
}
