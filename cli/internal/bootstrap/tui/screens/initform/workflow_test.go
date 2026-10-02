package initform

import (
	"bytes"
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
)

func demoPanel(t *testing.T) *PanelModel {
	t.Helper()
	d, err := setupdemo.NewCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := validE2EState()
	st.OSMode = "rook-ceph-local"
	st.OSDNode, st.OSDDevice, st.OSDSizeGB = "server-1", "loop0", "50"
	st.IngressAddressLayer = "none"
	st.MetalLBVIP = ""
	st.NetInterface = "eth0"
	st.HostID = "server-1"
	st.HostKeySHA256 = setupdemo.HostKey
	st.ManagementAddress = "192.0.2.10"
	st.KubeOVNMasterNodes = "192.0.2.10"
	m := NewPanelModel(st, "")
	m.ConfigureServices(PanelServices{Context: context.Background(), Discover: d.Discover, Demo: d, DraftPath: filepath.Join(t.TempDir(), "draft.json")})
	t.Cleanup(m.Close)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 39})
	return m
}
func complete(t *testing.T, m *PanelModel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("no operation: %s errors=%v", m.notice, m.validationErrors())
	}
	m.Update(cmd())
}
func nav(m *PanelModel, s string) tea.Cmd {
	_, c := m.Update(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
	return c
}

func TestIntegratedDemoUsesFullFormAndReviewGate(t *testing.T) {
	m := demoPanel(t)
	for _, s := range []string{"Basics", "Hosts", "Fleet", "Network", "Storage", "Accelerators", "Gates", "Readiness", "Review"} {
		if sectionIndex(m, s) < 0 {
			t.Fatalf("missing existing section %s", s)
		}
	}
	m.st.ExtraSets = map[string]string{"EXT_NET_MTU": "1450"}
	if m.activateWorkflow("run") != nil {
		t.Fatal("run skipped checks")
	}
	complete(t, m, m.startReadiness())
	if m.workflow.resourceErr != nil || !m.workflow.review.CanRun {
		t.Fatalf("check: %v %+v", m.workflow.resourceErr, m.workflow.review.Readiness)
	}
	m.activateWorkflow("review")
	complete(t, m, m.activateWorkflow("run"))
	if m.Applied() || m.currentSection() != "Install" {
		t.Fatal("simulation escaped into real apply")
	}
	for _, step := range []string{"verify", "result", "continue"} {
		m.activateWorkflow(step)
	}
	if m.currentSection() != "Continue" || !strings.Contains(m.workflow.result, "Ready") {
		t.Fatal("journey did not finish")
	}
	m.st.Name = "edited"
	m.changed()
	if m.activateWorkflow("run") != nil || m.workflow.reviewedHash != "" {
		t.Fatal("edit reused old review")
	}
	if m.st.ExtraSets["EXT_NET_MTU"] != "1450" {
		t.Fatal("advanced values lost")
	}
}

func TestIntegratedDemoUnknownAndBlockedStopReview(t *testing.T) {
	for _, scenario := range []string{"unknown", "blocked"} {
		t.Run(scenario, func(t *testing.T) {
			m := demoPanel(t)
			if scenario == "unknown" {
				m.st.HostKeySHA256 = ""
			} else {
				m.workflow.scenario = scenario
			}
			complete(t, m, m.startReadiness())
			m.activateWorkflow("review")
			if m.workflow.review.CanRun || m.currentSection() == "Review" || m.activateWorkflow("run") != nil {
				t.Fatal("blocked readiness allowed simulation")
			}
		})
	}
}

func TestDiscoveryOnlyOnRequestAndStaleResultsDiscarded(t *testing.T) {
	m := demoPanel(t)
	calls := 0
	m.workflow.services.Discover = func(ctx context.Context, r setup.ResourceRequest) (setup.HostResources, error) {
		calls++
		return setup.HostResources{}, nil
	}
	m.Init()
	m.View()
	if calls != 0 {
		t.Fatal("discovery on open")
	}
	cmd := m.startDiscovery()
	m.st.SSHHost = "different"
	m.changed()
	m.Update(cmd())
	if calls != 1 || m.workflow.resourceHash != "" || m.Busy() {
		t.Fatal("late discovery applied to changed host")
	}
	ctxClosed := false
	m.workflow.services.Discover = func(ctx context.Context, r setup.ResourceRequest) (setup.HostResources, error) {
		ctxClosed = ctx.Err() != nil
		return setup.HostResources{}, ctx.Err()
	}
	cmd = m.startDiscovery()
	m.Close()
	m.Update(cmd())
	if !ctxClosed {
		t.Fatal("close did not cancel discovery")
	}
}

func TestResourceSelectionsAreExplicitAndRequireRecheck(t *testing.T) {
	m := demoPanel(t)
	m.st.OSDDevice = ""
	complete(t, m, m.startDiscovery())
	if m.st.NetInterface != "eth0" || m.st.OSDDevice != "" {
		t.Fatal("discovery silently selected hardware")
	}
	m.activateWorkflow("nic:eth1")
	if m.request().Host.NIC != "eth1" || m.resourcesCurrent() {
		t.Fatal("selected NIC did not invalidate evidence")
	}
	m.st.OSMode = "rook-ceph-local"
	m.st.OSDNode = m.st.HostID
	m.changed()
	complete(t, m, m.startDiscovery())
	m.activateWorkflow("disk:/dev/sda")
	if m.st.OSDDevice != "" {
		t.Fatal("in-use disk was selected")
	}
	m.activateWorkflow("disk:/dev/sdb")
	if m.st.OSDDevice != "sdb" || m.resourcesCurrent() {
		t.Fatal("selected disk did not require recheck")
	}
	if m.discoveryBlocker() == "" {
		t.Fatal("stale disk observation allowed apply")
	}
	complete(t, m, m.startDiscovery())
	if m.workflow.resources.Host.Facts.SelectedDisk == nil {
		t.Fatal("selected disk not inspected")
	}
	m.workflow.resources.Host.ObservedAt = time.Now().Add(-6 * time.Minute)
	if m.resourcesCurrent() {
		t.Fatal("expired evidence remains selectable")
	}
}

func TestIntegratedEditorPasteResizeAndCancel(t *testing.T) {
	m := demoPanel(t)
	m.openSection("Hosts")
	m.fieldCursor = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.input.SetValue("")
	m.Update(tea.PasteMsg{Content: "ssh-ed25519 AAAA sample"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.editing || !strings.Contains(m.editError, "fingerprint") {
		t.Fatal("public key paste did not explain fingerprint")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	before := m.input.Value()
	m.Update(tea.PasteMsg{Content: "hidden"})
	if m.input.Value() != before {
		t.Fatal("hidden edit changed")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 39})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil || !m.Cancelled() || m.st.HostKeySHA256 != setupdemo.HostKey {
		t.Fatal("cancel committed editor or failed to quit")
	}
}

func TestIntegratedDraftPreservesAllSettingsButNotEvidence(t *testing.T) {
	m := demoPanel(t)
	m.st.ExtraSets = map[string]string{"EXT_NET_MTU": "1450"}
	m.st.NodeNICs = "worker-a=eno3"
	m.st.GPUNodeModes = "worker-a=pod-hami"
	path := m.workflow.services.DraftPath
	m.saveDemoDraft()
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("draft file: %v", err)
	}
	n := demoPanel(t)
	if err := n.LoadDemoDraft(path); err != nil {
		t.Fatal(err)
	}
	if n.st.ExtraSets["EXT_NET_MTU"] != "1450" || n.st.NodeNICs != m.st.NodeNICs || n.st.GPUNodeModes != m.st.GPUNodeModes || n.workflow.reviewHash != "" || n.resourcesCurrent() {
		t.Fatal("draft lost advanced input or trusted saved observations")
	}
	for _, data := range []string{`{"schemaVersion":1,"demoOnly":true}`, `{"schemaVersion":2,"demoOnly":false}`, `{"schemaVersion":2,"demoOnly":true} {}`} {
		os.WriteFile(path, []byte(data), 0600)
		if n.LoadDemoDraft(path) == nil {
			t.Fatal("invalid draft accepted")
		}
	}
}

func TestIntegratedLayoutFitsAndPreservesActions(t *testing.T) {
	for _, size := range [][2]int{{80, 17}, {80, 23}, {100, 17}, {120, 17}, {120, 39}, {160, 49}, {40, 12}} {
		for _, section := range []string{"Basics", "Hosts", "Network", "Storage", "Accelerators", "Readiness", "Review"} {
			m := demoPanel(t)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.openSection(section)
			text := ansi.Strip(m.View().Content)
			lines := strings.Split(text, "\n")
			if len(lines) > size[1] {
				t.Fatalf("%v %s height %d", size, section, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("%v %s overflow: %q", size, section, line)
				}
			}
			if size[0] >= 80 && !strings.Contains(text, "[ n ") {
				t.Fatal("primary action hidden")
			}
		}
	}
}

func TestIntegratedDemoRealProgramJourney(t *testing.T) {
	m := demoPanel(t)
	tm := asciiProgram(t, m, 120, 39)
	wait := func(want string) {
		t.Helper()
		teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(want)) }, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	}
	send := func() { tm.Send(tea.KeyPressMsg{Code: 'n', Text: "n"}) }
	wait("Check readiness")
	send()
	wait("0 blocked")
	send()
	wait("SIMULATION REVIEW")
	send()
	wait("Inspect verification")
	send()
	wait("Show result")
	send()
	wait("Continue")
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	if tm.FinalModel(t).(*PanelModel).Applied() {
		t.Fatal("demo authorized real install")
	}
}

func TestIntegratedPinRoundTripAndRequiredUnknownDiskGate(t *testing.T) {
	m := demoPanel(t)
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.SSHHostKeySHA256 != setupdemo.HostKey || !strings.Contains(m.st.EquivalentFlags(o), "--ssh-host-key-sha256") {
		t.Fatal("form dropped expected host fingerprint")
	}
	exported := clusterinit.ExportMap(o)
	if exported[clusterinit.KeySSHHostKeySHA256] != setupdemo.HostKey {
		t.Fatal("saved init config dropped pin")
	}
	m.workflow.services.Demo = nil
	complete(t, m, m.startDiscovery())
	snapshot := m.workflow.hostSnapshots[m.st.HostID]
	snapshot.Resources.Host.Checks = append(snapshot.Resources.Host.Checks, setup.HostCheck{ID: "selected-disk-identity", Status: "unknown", Detail: "no stable ID"})
	m.workflow.hostSnapshots[m.st.HostID] = snapshot
	if m.discoveryBlocker() == "" {
		t.Fatal("unknown disk identity passed real handoff")
	}
}

func TestIntegratedThemeAndUntrustedText(t *testing.T) {
	m := demoPanel(t)
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if !m.light {
		t.Fatal("light terminal response ignored")
	}
	m.st.Name = "demo\x1b]52;c;evil\a"
	m.st.ExtraSets = map[string]string{"EXT_NET_MTU": "1450\x1b]52;c;evil\a"}
	m.openSection("Review")
	if strings.Contains(m.View().Content, "\x1b]52") {
		t.Fatal("untrusted form value injected terminal command")
	}
	t.Setenv("NO_COLOR", "1")
	view := m.View().Content
	if strings.Contains(view, "\x1b") || !strings.Contains(view, "SIMULATED") || !strings.Contains(view, "[ n ") {
		t.Fatal("color-free output lost status or retained escapes")
	}
}
