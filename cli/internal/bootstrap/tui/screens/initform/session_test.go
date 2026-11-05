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

type panelHandoffFixture struct {
	*panelSessionFixture
	prepared int
	accepted string
}

func (s *panelHandoffFixture) PrepareExecutorHandoff(context.Context) (setup.SessionHandoff, error) {
	s.prepared++
	h := setup.SessionHandoff{CertificateSHA256: strings.Repeat("a", 64), Phase: "preparing"}
	s.result.Handoff = &h
	s.result.Session.OwnershipPhase = "handoff"
	return h, nil
}
func (s *panelHandoffFixture) AcceptExecutorHandoff(_ context.Context, ref string) (setup.CoordinatorStatus, error) {
	s.accepted = ref
	return s.result, nil
}

func TestSessionHandoffUsesExistingControlsAndCapturesExactCertificate(t *testing.T) {
	m, base := sessionPanel(t)
	s := &panelHandoffFixture{panelSessionFixture: base}
	m.ConfigureServices(PanelServices{Context: context.Background(), Session: s, SessionDirectory: "/private/session"})
	m.OpenContinue()
	complete(t, m, m.startSession("session-handoff"))
	if s.prepared != 1 || m.workflow.sessionStatus.Handoff == nil {
		t.Fatal("handoff not retained in existing session controls")
	}
	if !strings.Contains(strings.Join(m.sessionText(), "\n"), "previous run is sealed") {
		t.Fatal(m.sessionText())
	}
	command := m.startSession("session-accept-handoff")
	if command == nil {
		t.Fatal("accept unavailable")
	}
	// UI edits reject the late result; the already scheduled control retains
	// its exact immutable session/certificate rather than reading mutable UI.
	m.st.Domain = "changed.example.test"
	m.changed()
	m.Update(command())
	if s.accepted != strings.Repeat("a", 64) || m.workflow.sessionInspected {
		t.Fatal("late handoff event used edited state")
	}
	if m.startSession("session-handoff") != nil || m.startSession("session-accept-handoff") != nil {
		t.Fatal("edited settings authorized handoff")
	}
	for _, width := range []int{80, 120, 160} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		for _, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatal("handoff controls overflow terminal")
			}
		}
	}
	if m.Applied() {
		t.Fatal("handoff escaped readiness gate")
	}
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

func TestSessionShowsClusterOwnershipWithoutGrantingApply(t *testing.T) {
	for _, tc := range []struct {
		status setup.ClusterClaimStatus
		text   string
	}{
		{setup.ClusterClaimStatus{State: "not-recorded"}, "Cluster: not-recorded"},
		{setup.ClusterClaimStatus{State: "held", CurrentRun: true, MatchesProof: true}, "Cluster: held · this run"},
		{setup.ClusterClaimStatus{State: "held", MatchesProof: true}, "Cluster: held · inspection only"},
		{setup.ClusterClaimStatus{State: "held", MatchesProof: false}, "no completion proof"},
		{setup.ClusterClaimStatus{State: "conflict", Detail: "Retain claims and inspect."}, "Retain claims and inspect."},
		{setup.ClusterClaimStatus{State: "unknown", Detail: "Inspect API access."}, "Inspect API access."},
		{setup.ClusterClaimStatus{State: "absent", Detail: "Saved effects still require inspection."}, "Saved effects still require inspection."},
	} {
		t.Run(tc.text, func(t *testing.T) {
			m, s := sessionPanel(t)
			s.result.Cluster = tc.status
			complete(t, m, m.startSession("session-inspect"))
			if !strings.Contains(strings.Join(m.sessionText(), "\n"), tc.text) {
				t.Fatal(m.sessionText())
			}
			for _, width := range []int{80, 120, 160} {
				m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				view := ansi.Strip(m.View().Content)
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatalf("overflow at %d: %q", width, line)
					}
				}
			}
			if m.Applied() || m.workflow.sessionStatus.ReadyToApply {
				t.Fatal("ownership inspection granted apply")
			}
		})
	}
}

func TestSessionPlatformActionsUseSharedNavigationAndRejectStaleEvidence(t *testing.T) {
	m, _ := sessionPanel(t)
	refresh, inspect, record := 0, 0, 0
	m.workflow.services.Platform = &PlatformServices{
		Refresh: func(context.Context) (setup.PlatformReview, error) {
			refresh++
			return setup.PlatformReview{Hash: strings.Repeat("a", 64), Effects: []setup.StagePreview{{ID: "scaffold"}}}, nil
		},
		Inspect: func(_ context.Context, save bool) (setup.PlatformRecoveryReport, error) {
			inspect++
			if save {
				record++
			}
			return setup.PlatformRecoveryReport{Findings: []setup.PlatformRecoveryFinding{{Stage: "scaffold", State: "verified"}}}, nil
		},
	}
	m.rebuildWorkflowFields()
	complete(t, m, m.startSession("session-refresh-platform"))
	complete(t, m, m.startSession("session-inspect-effects"))
	complete(t, m, m.startSession("session-record-effects"))
	if refresh != 1 || inspect != 2 || record != 1 || m.workflow.platformReview.Hash == "" || m.Applied() {
		t.Fatal("incorrect platform action or legacy Apply escape")
	}
	command := m.startSession("session-refresh-platform")
	m.st.Domain = "changed.example.test"
	m.changed()
	m.Update(command())
	if m.workflow.platformReview.Hash != "" || m.startSession("session-record-effects") != nil {
		t.Fatal("edited form reused old platform evidence")
	}
}
