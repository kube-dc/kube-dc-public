package initform

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func sectionIndex(m *PanelModel, name string) int {
	for i, s := range m.visibleSections() {
		if s == name {
			return i
		}
	}
	return -1
}

func fieldLabels(m *PanelModel, section string) []string {
	var out []string
	for _, f := range m.visibleInSection(section) {
		out = append(out, f.Label)
	}
	return out
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func TestPanel_SectionOrder(t *testing.T) {
	m := NewPanelModel(&State{Mode: "install"}, "")
	want := []string{"Basics", "Fleet", "Network", "Storage", "Gates", "Review"}
	// Adopt only appears in adopt mode (visibleSections filters it out here).
	got := m.visibleSections()
	if hasLabel(got, "Adopt") {
		t.Errorf("Adopt section must be hidden in install mode: %v", got)
	}
	gi := 0
	for _, w := range want {
		found := false
		for ; gi < len(got); gi++ {
			if got[gi] == w {
				found = true
				gi++
				break
			}
		}
		if !found {
			t.Fatalf("section %q missing or out of order in %v", w, got)
		}
	}
}

func TestPanel_Visibility(t *testing.T) {
	// internal-only → no EXT_PUBLIC_* in Network; cloud+public-vlan → yes.
	m := NewPanelModel(&State{Mode: "install", Preset: "internal-only"}, "")
	if hasLabel(fieldLabels(m, "Network"), "EXT_PUBLIC_CIDR") {
		t.Error("internal-only should hide EXT_PUBLIC_CIDR")
	}
	m.st.Preset = "cloud+public-vlan"
	if !hasLabel(fieldLabels(m, "Network"), "EXT_PUBLIC_CIDR") {
		t.Error("cloud+public-vlan should show EXT_PUBLIC_CIDR")
	}
	// KUBE_OVN_MASTER_NODES is always in Network (required for every install).
	if !hasLabel(fieldLabels(m, "Network"), "KUBE_OVN_MASTER_NODES") {
		t.Error("KUBE_OVN_MASTER_NODES must always be present")
	}

	// Gates always exposes both bypass toggles — the no-KVM one is what lets
	// a first-time user install on a nested/cloud VM without /dev/kvm.
	if !hasLabel(fieldLabels(m, "Gates"), "Allow DNS not ready") ||
		!hasLabel(fieldLabels(m, "Gates"), "Allow node without /dev/kvm") {
		t.Errorf("Gates must expose both allow toggles: %v", fieldLabels(m, "Gates"))
	}

	// Storage fields follow OSMode.
	m.st.OSMode = "rook-ceph-local"
	if !hasLabel(fieldLabels(m, "Storage"), "OSD node") || hasLabel(fieldLabels(m, "Storage"), "Ceph node 1") {
		t.Errorf("rook-ceph-local should show OSD node, hide Ceph node 1: %v", fieldLabels(m, "Storage"))
	}
	m.st.OSMode = "rook-ceph-multi-node"
	if !hasLabel(fieldLabels(m, "Storage"), "Ceph node 1") || hasLabel(fieldLabels(m, "Storage"), "OSD node") {
		t.Errorf("multi-node should show Ceph node 1, hide OSD node: %v", fieldLabels(m, "Storage"))
	}

	// Adopt section only in adopt mode.
	if sectionIndex(m, "Adopt") == -1 {
		// sections list is built once; visibility of the Adopt FIELD is
		// what matters — check the field is hidden in install mode.
		if len(m.visibleInSection("Adopt")) != 0 {
			t.Error("Adopt fields should be hidden in install mode")
		}
	}
	m.st.Mode = "adopt"
	if len(m.visibleInSection("Adopt")) == 0 {
		t.Error("Adopt fields should be visible in adopt mode")
	}
}

func TestPanel_ValidationGate(t *testing.T) {
	// Empty → required Basics fields error.
	m := NewPanelModel(&State{Mode: "install", Preset: "internal-only", OSMode: "rook-ceph-local"}, "")
	if len(m.validationErrors()) == 0 {
		t.Fatal("empty required fields should produce validation errors")
	}
	// Fill the required set → clean (internal-only still needs the preset's
	// EXT_NET_* keys, now surfaced by validationErrors).
	m.st.Name = "e2e"
	m.st.Domain = "e2e.kube-dc.cloud"
	m.st.NodeIP = "203.0.113.52"
	m.st.Email = "ops@example.com"
	m.st.KubeOVNMasterNodes = "10.77.0.22"
	m.st.NetInterface = "enp1s0"
	m.st.NetVLANID = "0"
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Errorf("complete config should have no validation errors, got %v", errs)
	}
}

func TestPanel_DisabledNeedsConsent(t *testing.T) {
	m := NewPanelModel(&State{
		Mode: "install", Preset: "internal-only", OSMode: "disabled",
		Name: "e2e", Domain: "e2e.kube-dc.cloud", NodeIP: "203.0.113.52",
		Email: "ops@example.com", KubeOVNMasterNodes: "10.77.0.22",
		NetInterface: "enp1s0", NetVLANID: "0",
	}, "")
	if len(m.validationErrors()) == 0 {
		t.Error("disabled object storage without consent must block Apply")
	}
	m.st.DisabledConsent = true
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Errorf("disabled + consent should be clean, got %v", errs)
	}
}

func TestPanel_CycleAndToggle(t *testing.T) {
	if got := cycleOption([]string{"a", "b", "c"}, "b"); got != "c" {
		t.Errorf("cycle b→c, got %q", got)
	}
	if got := cycleOption([]string{"a", "b", "c"}, "c"); got != "a" {
		t.Errorf("cycle wraps c→a, got %q", got)
	}

	// activate on the Mode select cycles it.
	m := NewPanelModel(&State{Mode: "install"}, "")
	m.focus = focusFields
	m.secCursor = sectionIndex(m, "Basics")
	// Mode is the last field in Basics; find its index.
	labels := fieldLabels(m, "Basics")
	for i, l := range labels {
		if l == "Mode" {
			m.fieldCursor = i
		}
	}
	m.activate()
	if m.st.Mode != "adopt" {
		t.Errorf("activate on Mode should cycle install→adopt, got %q", m.st.Mode)
	}
}

// TestPanel_EscBackQuitParity locks the exit UX to match the Fleet TUI:
// Esc is "back" (never exits) and q / ctrl+c quit. Esc from the fields
// pane returns to the sections list; Esc on the sections pane is a no-op
// (does NOT cancel/quit); q on the sections pane cancels + quits.
func TestPanel_EscBackQuitParity(t *testing.T) {
	esc := tea.KeyPressMsg{Code: tea.KeyEsc}

	// Esc on fields → back to sections, no cancel, no quit cmd.
	m := NewPanelModel(&State{Mode: "install"}, "")
	m.focus = focusFields
	if _, cmd := m.updateNav(esc); cmd != nil {
		t.Error("Esc must not emit a command (no quit)")
	}
	if m.focus != focusSections {
		t.Errorf("Esc on fields should step back to sections, focus=%v", m.focus)
	}
	if m.Cancelled() {
		t.Error("Esc must never cancel")
	}

	// Esc on sections → no-op: stays on sections, still not cancelled.
	if _, cmd := m.updateNav(esc); cmd != nil {
		t.Error("Esc on sections must not emit a command (no quit)")
	}
	if m.focus != focusSections || m.Cancelled() {
		t.Errorf("Esc on sections must be a no-op (focus=%v cancelled=%v)", m.focus, m.Cancelled())
	}

	// q on sections → cancel + quit.
	if _, cmd := m.updateNav(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Error("q should emit tea.Quit")
	}
	if !m.Cancelled() {
		t.Error("q should cancel the panel")
	}
}

// Honest readiness: preset-required keys without a dedicated Required flag
// (EXT_PUBLIC_* for cloud+public-vlan) must block "ready" so Apply won't
// fail later at ValidatePresetRequiredKeys.
func TestPanel_PresetRequiredKeysSurfaced(t *testing.T) {
	m := NewPanelModel(&State{
		Mode: "install", Preset: "cloud+public-vlan", OSMode: "rook-ceph-local",
		Name: "dc1", Domain: "kdc.example.com", NodeIP: "203.0.113.10", Email: "ops@example.com",
		NetInterface: "bond0", NetVLANID: "100", KubeOVNMasterNodes: "10.0.0.5", OSDNode: "dc1-m1",
	}, "")
	if len(m.validationErrors()) == 0 {
		t.Fatal("cloud+public-vlan with empty EXT_PUBLIC_* must not report ready")
	}
	m.st.PubVLANID = "200"
	m.st.PubCIDR = "198.51.100.0/28"
	m.st.PubGateway = "198.51.100.1"
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Errorf("public keys filled → should be ready, got %v", errs)
	}
}

// ←/→ cycle a select field in place (forward/back), a friendlier UX than
// enter-only cycling.
func TestPanel_SelectCycleArrows(t *testing.T) {
	m := NewPanelModel(&State{Mode: "install"}, "")
	m.focus = focusFields
	m.secCursor = sectionIndex(m, "Basics")
	for i, l := range fieldLabels(m, "Basics") {
		if l == "Mode" {
			m.fieldCursor = i
		}
	}
	m.updateNav(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.st.Mode != "adopt" {
		t.Errorf("→ should cycle Mode install→adopt, got %q", m.st.Mode)
	}
	m.updateNav(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.st.Mode != "install" {
		t.Errorf("← should cycle Mode back to install, got %q", m.st.Mode)
	}
}

// '?' toggles the full-help overlay.
func TestPanel_HelpToggle(t *testing.T) {
	m := NewPanelModel(&State{Mode: "install"}, "")
	if m.showHelp {
		t.Fatal("help should be off by default")
	}
	m.updateNav(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.showHelp {
		t.Error("? should toggle help on")
	}
	m.updateNav(tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.showHelp {
		t.Error("? should toggle help off")
	}
}

// The panel's State → Apply must still produce a valid InitOptions (the
// thin-generator contract), same as the flag path.
func TestPanel_ProducesValidInitOptions(t *testing.T) {
	st := &State{
		Name: "e2e", Domain: "e2e.kube-dc.cloud", NodeIP: "203.0.113.52",
		SSHHost: "ubuntu@203.0.113.52", Email: "ops@example.com",
		Mode: "install", FleetMode: "new-repo", Provider: "github",
		Owner: "kube-dc", RepoName: "e2e-fleet-r5", Preset: "internal-only",
		NetVLANID: "0", NetInterface: "enp1s0", KubeOVNMasterNodes: "10.77.0.22",
		OSMode: "rook-ceph-local", OSDNode: "e2e-master-1", OSDSizeGB: "40",
	}
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := clusterinit.ValidatePresetRequiredKeys(o); err != nil {
		t.Fatalf("ValidatePresetRequiredKeys: %v", err)
	}
}
