package screens

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

func obProbe() discover.ProbeResult {
	return discover.ProbeResult{
		Status: discover.StatusDrifted,
		Reconcilers: []discover.ReconcilerStatus{
			{Name: "flux-system", Ready: true},
			{Name: "platform", Ready: true},
		},
		OpenBao: &discover.OpenBaoStatus{
			ReadyPods: 2, TotalPods: 3,
			Pods:      []discover.OpenBaoPod{{Name: "openbao-0", Ready: true}, {Name: "openbao-2", Ready: false}},
			Finalized: true, AuthSetup: true,
		},
	}
}

func TestRenderFleetOpenBao_CompactContentAndNil(t *testing.T) {
	if !strings.Contains(renderFleetOpenBao(nil), "no OpenBao data") {
		t.Error("nil OpenBao should render the no-data note")
	}
	out := renderFleetOpenBao(obProbe().OpenBao)
	for _, want := range []string{
		"openbao-0", "ready",
		"openbao-2", "sealed?",
		"2/3 pods ready",
		"finalized: yes",
		"auth:      yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compact openbao render missing %q:\n%s", want, out)
		}
	}
}

func newFleetForDetails(t *testing.T) *FleetModel {
	t.Helper()
	m := NewFleetModel("/tmp/fleet")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.clusters = []discover.Cluster{{Name: "cloud", Domain: "kube-dc.cloud", KubeAPIURL: "https://x"}}
	m.loading = false
	m.statuses["cloud"] = obProbe()
	m.selected = 0
	m.refreshDetails()
	return m
}

// OpenBao is the first drill target in the details pane (rendered after
// status), so cursor 0 → Enter opens the OpenBao panel; a Kustomization
// (cursor 1+) opens the Kustomization panel.
func TestFleetModel_OpenBaoIsFirstDetailTarget(t *testing.T) {
	m := newFleetForDetails(t)
	targets := m.detailTargets()
	// obProbe() has no drift → [OpenBao, flux-system, platform].
	if len(targets) != 3 || targets[0].kind != targetOpenBao || targets[1].kind != targetKustomization {
		t.Fatalf("expected [openbao, flux-system, platform], got %+v", targets)
	}
	// The details pane renders an OpenBao row after status (the full
	// View() sizes the details viewport before rendering).
	if !strings.Contains(m.View().Content, "OpenBao") {
		t.Errorf("details pane should show the OpenBao row:\n%s", m.View().Content)
	}

	// Focus the details pane, Enter on cursor 0 → OpenBao panel opens.
	m.focus = paneFocusDetails
	m.kustomizationCursor = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.drillDownOpen || m.drillDownTitle != openBaoDrillTitle {
		t.Fatalf("Enter on the OpenBao row should open the OpenBao panel (open=%v title=%q)", m.drillDownOpen, m.drillDownTitle)
	}
}

// 'o' opens the OpenBao side panel from anywhere; the panel content +
// close hint render; Esc closes it.
func TestFleetModel_OKeyOpensPanelWithCloseHint(t *testing.T) {
	m := newFleetForDetails(t)
	m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if !m.drillDownOpen || m.drillDownTitle != openBaoDrillTitle {
		t.Fatalf("'o' should open the OpenBao panel")
	}
	view := m.View().Content
	for _, want := range []string{"openbao-0", "2/3 pods ready", "esc", "close"} {
		if !strings.Contains(view, want) {
			t.Errorf("OpenBao panel view missing %q", want)
		}
	}
	// Sealed pod (2/3) → unseal action offered.
	if !strings.Contains(view, "unseal") {
		t.Errorf("sealed cluster should offer the unseal action:\n%s", view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.drillDownOpen {
		t.Error("Esc should close the OpenBao panel")
	}
}

// 'u' dispatches the unseal subprocess only while the OpenBao panel is
// open (asserted via pendingActionFor, which execUnsealCmd sets); it's
// inert otherwise.
func TestFleetModel_UnsealOnlyInOpenBaoPanel(t *testing.T) {
	m := newFleetForDetails(t)
	// Not in the panel → 'u' is a no-op.
	m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if m.pendingActionFor != "" {
		t.Errorf("'u' outside the OpenBao panel must not dispatch unseal")
	}
	// Open the panel, then 'u' dispatches for the selected cluster.
	m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if m.pendingActionFor != "cloud" {
		t.Errorf("'u' in the OpenBao panel should dispatch unseal for cloud, got pending=%q", m.pendingActionFor)
	}
}

// reconcilerGlyph: ✓ ready, ◑ reconciling (not an error), ✗ otherwise.
func TestReconcilerGlyph(t *testing.T) {
	cases := []struct {
		rec  discover.ReconcilerStatus
		want string
	}{
		{discover.ReconcilerStatus{Ready: true}, "✓"},
		{discover.ReconcilerStatus{Reconciling: true}, "◑"},
		{discover.ReconcilerStatus{}, "✗"}, // failed / unknown
	}
	for _, c := range cases {
		if got := reconcilerGlyph(c.rec); got != c.want {
			t.Errorf("reconcilerGlyph(%+v) = %q, want %q", c.rec, got, c.want)
		}
	}
}

// ensureDetailsCursorVisible scrolls the details viewport to follow the
// selected row past the fold (the "not scrolled down" bug).
func TestEnsureDetailsCursorVisible(t *testing.T) {
	m := NewFleetModel("/tmp/fleet")
	m.focus = paneFocusDetails
	m.details.SetHeight(10)
	m.details.SetContent(strings.Repeat("x\n", 60)) // 60 content lines

	// Cursor below the fold → scrolls down so it's visible.
	m.detailsSelLine = 30
	m.details.SetYOffset(0)
	m.ensureDetailsCursorVisible()
	top, bottom := m.details.YOffset(), m.details.YOffset()+m.details.Height()-1
	if m.detailsSelLine < top || m.detailsSelLine > bottom {
		t.Errorf("line 30 not visible after scroll: YOffset=%d height=%d", m.details.YOffset(), m.details.Height())
	}

	// Cursor back near the top → scrolls up.
	m.detailsSelLine = 1
	m.ensureDetailsCursorVisible()
	if m.details.YOffset() > 1 {
		t.Errorf("should scroll up to reveal line 1, YOffset=%d", m.details.YOffset())
	}

	// Not focused on details → no-op.
	m.focus = paneFocusList
	m.details.SetYOffset(5)
	m.detailsSelLine = 40
	m.ensureDetailsCursorVisible()
	if m.details.YOffset() != 5 {
		t.Errorf("scroll must not move when details pane isn't focused (YOffset=%d)", m.details.YOffset())
	}
}

func driftProbe() discover.ProbeResult {
	return discover.ProbeResult{
		Status: discover.StatusDrifted,
		Reconcilers: []discover.ReconcilerStatus{
			{Name: "flux-system", Ready: true},
			{Name: "platform", Ready: true},
		},
		Drifts: []discover.ImageDrift{
			{Deployment: "db-manager", EnvVar: "DB_MANAGER_TAG", Expected: "v0.1.11", Running: ""},
		},
	}
}

func newFleetWithDrift(t *testing.T) *FleetModel {
	t.Helper()
	m := NewFleetModel("/tmp/fleet")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.clusters = []discover.Cluster{{Name: "cloud", Domain: "kube-dc.cloud", KubeAPIURL: "https://x"}}
	m.loading = false
	m.statuses["cloud"] = driftProbe()
	m.selected = 0
	m.refreshDetails()
	return m
}

// When a cluster is drifted, an "Image drift" row appears after OpenBao;
// Enter opens a panel with both remediation directions.
func TestFleetModel_DriftRowAndPanel(t *testing.T) {
	m := newFleetWithDrift(t)
	targets := m.detailTargets()
	// [OpenBao, Drift, flux-system, platform]
	if len(targets) != 4 || targets[0].kind != targetOpenBao || targets[1].kind != targetDrift {
		t.Fatalf("expected drift row at index 1, got %+v", targets)
	}
	if !strings.Contains(m.View().Content, "Image drift") {
		t.Errorf("details pane should show the Image drift row:\n%s", m.View().Content)
	}

	// Enter on the drift row opens the drift panel with both remediations.
	m.focus = paneFocusDetails
	m.kustomizationCursor = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.drillDownOpen || m.drillDownTitle != driftDrillTitle {
		t.Fatalf("Enter on the drift row should open the drift panel (title=%q)", m.drillDownTitle)
	}
	view := m.View().Content
	for _, want := range []string{"db-manager", "DB_MANAGER_TAG=v0.1.11", "config set", "flux reconcile", "reconcile", "esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("drift panel missing %q", want)
		}
	}
}

// 'R' in the drift panel is a two-press confirm: first arms (no dispatch),
// second dispatches the reconcile. Outside the panel it's inert.
func TestFleetModel_ReconcileTwoPressConfirm(t *testing.T) {
	m := newFleetWithDrift(t)
	// Not in the drift panel → 'R' inert.
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.pendingActionFor != "" || m.driftReconcileArmed {
		t.Fatal("'R' outside the drift panel must be inert")
	}
	// Open drift panel, first 'R' arms (no dispatch).
	m.focus = paneFocusDetails
	m.kustomizationCursor = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if !m.driftReconcileArmed || m.pendingActionFor != "" {
		t.Fatalf("first R should arm, not dispatch (armed=%v pending=%q)", m.driftReconcileArmed, m.pendingActionFor)
	}
	if !strings.Contains(m.View().Content, "CONFIRM") {
		t.Errorf("armed panel should show the CONFIRM warning:\n%s", m.View().Content)
	}
	// Second 'R' dispatches + disarms.
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.pendingActionFor != "cloud" || m.driftReconcileArmed {
		t.Errorf("second R should dispatch reconcile + disarm (pending=%q armed=%v)", m.pendingActionFor, m.driftReconcileArmed)
	}
}
