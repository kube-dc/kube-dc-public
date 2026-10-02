package initform

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

type panelSessionFixture struct {
	inspect, claim, release int
	takeover                bool
	result                  setup.CoordinatorStatus
}

func (s *panelSessionFixture) Status(context.Context) (setup.CoordinatorStatus, error) {
	s.inspect++
	return s.result, nil
}
func (s *panelSessionFixture) Acquire(_ context.Context, takeover bool) (setup.CoordinatorStatus, error) {
	s.claim++
	s.takeover = takeover
	return s.result, nil
}
func (s *panelSessionFixture) Release(context.Context) (setup.CoordinatorStatus, error) {
	s.release++
	return s.result, nil
}
func sessionPanel(t *testing.T) (*PanelModel, *panelSessionFixture) {
	t.Helper()
	s := &panelSessionFixture{result: setup.CoordinatorStatus{Session: setup.SessionSummary{OwnershipPhase: "held"}, Targets: []setup.TargetClaimStatus{{HostID: "server-1", State: "reserved", CurrentRun: true, SecondsRemaining: 1800}}}}
	m := NewPanelModel(validE2EState(), "")
	m.ConfigureServices(PanelServices{Context: context.Background(), Session: s, SessionDirectory: "/private/session"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m.OpenContinue()
	return m, s
}
func TestSessionUsesExistingFormAndExplicitActions(t *testing.T) {
	m, s := sessionPanel(t)
	for _, section := range []string{"Basics", "Hosts", "Fleet", "Network", "Storage", "Configuration", "Review", "Continue"} {
		if sectionIndex(m, section) < 0 {
			t.Fatal(section)
		}
	}
	if s.claim != 0 || s.inspect != 0 {
		t.Fatal("constructor performed remote operation")
	}
	complete(t, m, m.activateWorkflow("session-inspect"))
	view := ansi.Strip(m.View().Content)
	for _, text := range []string{"Continue", "Reserve reviewed hosts", "Ownership: held", "server-1", "Installation remains blocked"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q\n%s", text, view)
		}
	}
	complete(t, m, m.activateWorkflow("session-claim"))
	if s.claim != 1 || s.takeover {
		t.Fatal("implicit takeover")
	}
	complete(t, m, m.activateWorkflow("session-takeover"))
	if s.claim != 2 || !s.takeover {
		t.Fatal("takeover not explicit")
	}
	complete(t, m, m.activateWorkflow("session-release"))
	if s.release != 1 {
		t.Fatal("release")
	}
	m.openSection("Review")
	m.primaryAction()
	if m.Applied() {
		t.Fatal("session escaped to legacy apply")
	}
	for _, f := range m.fields {
		if f.Section == "Review" && f.Kind == panelAction && f.Action == "" {
			t.Fatalf("legacy Apply action in session form: %+v", f)
		}
	}
}
func TestSessionRejectsChangedInputsAndLateResults(t *testing.T) {
	m, s := sessionPanel(t)
	command := m.startSession("session-inspect")
	if command == nil {
		t.Fatal("inspect")
	}
	m.st.Domain = "changed.example.test"
	m.changed()
	m.Update(command())
	if m.workflow.sessionInspected {
		t.Fatal("stale result updated edited form")
	}
	if m.startSession("session-claim") != nil || m.startSession("session-takeover") != nil || s.claim != 0 {
		t.Fatal("claims used edited inputs")
	}
	// Inspect and release still refer to the immutable saved specification.
	complete(t, m, m.startSession("session-inspect"))
	complete(t, m, m.startSession("session-release"))
	if s.release != 1 {
		t.Fatal("edited form stranded safe reservation release")
	}
}
func TestSessionNarrowTerminalRetainsControls(t *testing.T) {
	m, _ := sessionPanel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	view := ansi.Strip(m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("overflow: %q", line)
		}
	}
	if !strings.Contains(view, "Continue") {
		t.Fatal(view)
	}
}
