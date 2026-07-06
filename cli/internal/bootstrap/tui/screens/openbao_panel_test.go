package screens

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	if len(targets) != 3 || !targets[0].openbao || targets[1].openbao {
		t.Fatalf("expected [openbao, flux-system, platform], got %+v", targets)
	}
	// The details pane renders an OpenBao row after status (the full
	// View() sizes the details viewport before rendering).
	if !strings.Contains(m.View(), "OpenBao") {
		t.Errorf("details pane should show the OpenBao row:\n%s", m.View())
	}

	// Focus the details pane, Enter on cursor 0 → OpenBao panel opens.
	m.focus = paneFocusDetails
	m.kustomizationCursor = 0
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.drillDownOpen || m.drillDownTitle != openBaoDrillTitle {
		t.Fatalf("Enter on the OpenBao row should open the OpenBao panel (open=%v title=%q)", m.drillDownOpen, m.drillDownTitle)
	}
}

// 'o' opens the OpenBao side panel from anywhere; the panel content +
// close hint render; Esc closes it.
func TestFleetModel_OKeyOpensPanelWithCloseHint(t *testing.T) {
	m := newFleetForDetails(t)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if !m.drillDownOpen || m.drillDownTitle != openBaoDrillTitle {
		t.Fatalf("'o' should open the OpenBao panel")
	}
	view := m.View()
	for _, want := range []string{"openbao-0", "2/3 pods ready", "esc", "close"} {
		if !strings.Contains(view, want) {
			t.Errorf("OpenBao panel view missing %q", want)
		}
	}
	// Sealed pod (2/3) → unseal action offered.
	if !strings.Contains(view, "unseal") {
		t.Errorf("sealed cluster should offer the unseal action:\n%s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
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
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if m.pendingActionFor != "" {
		t.Errorf("'u' outside the OpenBao panel must not dispatch unseal")
	}
	// Open the panel, then 'u' dispatches for the selected cluster.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
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
