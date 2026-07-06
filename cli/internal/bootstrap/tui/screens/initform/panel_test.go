package initform

import (
	"testing"

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
	// Fill the required set → clean.
	m.st.Name = "e2e"
	m.st.Domain = "e2e.kube-dc.cloud"
	m.st.NodeIP = "203.0.113.52"
	m.st.Email = "ops@example.com"
	m.st.KubeOVNMasterNodes = "10.77.0.22"
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Errorf("complete config should have no validation errors, got %v", errs)
	}
}

func TestPanel_DisabledNeedsConsent(t *testing.T) {
	m := NewPanelModel(&State{
		Mode: "install", Preset: "internal-only", OSMode: "disabled",
		Name: "e2e", Domain: "e2e.kube-dc.cloud", NodeIP: "203.0.113.52",
		Email: "ops@example.com", KubeOVNMasterNodes: "10.77.0.22",
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
