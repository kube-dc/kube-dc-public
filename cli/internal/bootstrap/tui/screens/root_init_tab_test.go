package screens

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// Tests for the T6 root-router embed: the Init tab hosts the settings
// panel as a MODAL screen. The invariants that must hold (the exact
// class of breakage that got T7's experiments reverted after live TTY
// runs):
//
//   - typed characters ('q', digits, brackets) reach the panel, never
//     the root's tab navigation, while Init is active;
//   - Ctrl+C still quits globally;
//   - backing out of the panel ('q' in nav mode → Cancelled) returns
//     to the Fleet tab with a FRESH panel instead of quitting the
//     whole program;
//   - digit 3 jumps to Init from a non-modal tab; ']' cycles through it.

func newTestRoot(t *testing.T) *RootModel {
	t.Helper()
	r := NewRootModel(t.TempDir(), RootTabFleet)
	// Give every child a size so views/layout code have dimensions.
	r.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return r
}

func TestRootInitTab_DigitJumpAndTabBar(t *testing.T) {
	r := newTestRoot(t)
	if len(r.tabs) != 3 || r.tabs[2].name != "New Cluster" || !r.tabs[2].modal {
		t.Fatalf("want 3 tabs with modal New Cluster third, got %+v", r.tabs)
	}
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if r.active != int(RootTabInit) {
		t.Fatalf("digit 3 should jump to Init, active=%d", r.active)
	}
	bar := r.renderTabBar()
	if !strings.Contains(bar, "New Cluster") {
		t.Errorf("tab bar should render the New Cluster tab: %q", bar)
	}
}

func TestRootInitTab_CycleReachesInit(t *testing.T) {
	r := newTestRoot(t)
	r.Update(tea.KeyPressMsg{Code: ']', Text: "]"}) // → Contexts
	r.Update(tea.KeyPressMsg{Code: ']', Text: "]"}) // → Init
	if r.active != int(RootTabInit) {
		t.Fatalf("']' twice from Fleet should land on Init, active=%d", r.active)
	}
}

func TestRootInitTab_EditingSwallowsRootNavKeys(t *testing.T) {
	// DYNAMIC modality (manual TTY finding 2026-07-20): keys are
	// swallowed ONLY while a text field is being edited — that's when
	// '1'/']'/'q' are typed characters. Enter editing: Tab → fields
	// pane, Enter → edit the first Basics field (Cluster name, text).
	r := newTestRoot(t)
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	r.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	r.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !r.initPanel.Editing() {
		t.Fatal("expected the panel to be editing after Tab+Enter")
	}
	r.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if r.active != int(RootTabInit) {
		t.Errorf("digit 1 while EDITING must not switch tabs (active=%d)", r.active)
	}
	r.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if r.active != int(RootTabInit) {
		t.Errorf("']' while EDITING must not cycle tabs (active=%d)", r.active)
	}
}

func TestRootInitTab_NavModeTabSwitchPreservesState(t *testing.T) {
	// In NAV mode the operator can leave the tab with the normal keys
	// (the trapped-on-the-tab finding) and the panel state SURVIVES —
	// only 'q' (discard + back) resets it.
	r := newTestRoot(t)
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	before := r.initPanel

	r.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if r.active != int(RootTabFleet) {
		t.Fatalf("digit 1 in nav mode should switch to Fleet, active=%d", r.active)
	}
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if r.active != int(RootTabInit) || r.initPanel != before {
		t.Errorf("returning to the tab must find the SAME panel (state preserved)")
	}

	// ']' cycles away too (Init is last → wraps to Fleet).
	r.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if r.active != int(RootTabFleet) {
		t.Errorf("']' in nav mode should cycle off the tab, active=%d", r.active)
	}
	if r.initPanel != before {
		t.Errorf("cycling away must not reset the panel")
	}
}

func TestRootInitTab_CtrlCQuitsGlobally(t *testing.T) {
	r := newTestRoot(t)
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	_, cmd := r.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c while modal must produce a quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c cmd should be tea.Quit, got %T", cmd())
	}
}

func TestRootInitTab_PanelCancelReturnsToFleetFresh(t *testing.T) {
	r := newTestRoot(t)
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	before := r.initPanel

	// 'q' in the panel's NAV mode cancels it. The root must convert
	// that into "back to Fleet + fresh panel", swallowing the panel's
	// tea.Quit — NOT exit the whole program.
	_, cmd := r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("panel cancel must not quit the whole program")
		}
	}
	if r.active != int(RootTabFleet) {
		t.Errorf("cancel should return to Fleet, active=%d", r.active)
	}
	if r.initPanel == before {
		t.Errorf("cancelled panel must be rebuilt fresh for the next visit")
	}
	if _, ok := r.InitResult(); ok {
		t.Errorf("InitResult must be false after a cancel")
	}
}

func TestRootInitTab_InitResultFalseByDefault(t *testing.T) {
	r := newTestRoot(t)
	if eq, ok := r.InitResult(); ok || eq != "" {
		t.Errorf("fresh root must have no init result (eq=%q ok=%v)", eq, ok)
	}
}

func TestRootInitTab_InheritsFleetContext(t *testing.T) {
	// Manual TTY finding 2026-07-20: `bootstrap --repo <existing-fleet>`
	// → New Cluster showed new-repo + empty repo path. The embedded
	// panel must inherit the root's fleet context: repo path always;
	// existing-fleet mode when the checkout has scaffolded siblings.
	repo := t.TempDir()
	// flat sibling
	if err := os.MkdirAll(filepath.Join(repo, "clusters", "dc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "clusters", "dc1", "cluster-config.env"), []byte("CLUSTER_NAME=dc1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRootModel(repo, RootTabFleet)
	if r.initOpts.Repo != repo {
		t.Errorf("panel must inherit --repo: got %q", r.initOpts.Repo)
	}
	if r.initOpts.FleetMode != clusterinit.FleetExistingFleet {
		t.Errorf("existing siblings must default fleet-mode to existing-fleet, got %q", r.initOpts.FleetMode)
	}

	// Discard ('q' in nav mode) must rebuild with the SAME context —
	// fresh form, inherited repo/mode (not blank).
	r.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if r.initOpts.Repo != repo || r.initOpts.FleetMode != clusterinit.FleetExistingFleet {
		t.Errorf("discard-reset lost the fleet context: %+v", r.initOpts)
	}

	// Empty dir → repo still inherited, mode left for the panel default.
	empty := t.TempDir()
	r2 := NewRootModel(empty, RootTabFleet)
	if r2.initOpts.Repo != empty || r2.initOpts.FleetMode != "" {
		t.Errorf("empty repo: want inherited path + unset mode, got %+v", r2.initOpts)
	}
}

func TestHasFleetSiblings_NestedShape(t *testing.T) {
	repo := t.TempDir()
	// eu/dc1-shape: one level deeper
	if err := os.MkdirAll(filepath.Join(repo, "clusters", "eu", "dc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "clusters", "eu", "dc1", "cluster-config.env"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasFleetSiblings(repo) {
		t.Error("nested eu/dc1-shape sibling must be detected")
	}
	if hasFleetSiblings(t.TempDir()) {
		t.Error("empty dir must not report siblings")
	}
}
