package screens

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// --- T7 Reconcile tab (Update/body headless) ---

func TestReconcileModel_LoadedRendersWaterfall(t *testing.T) {
	m := NewReconcileModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(reconcileLoadedMsg{graph: ports.Graph{Nodes: []ports.GraphNode{
		{Name: "flux-system", Ready: true},
		{Name: "infra-cni", DependsOn: []string{"flux-system"}, Reconciling: true},
	}}})
	body := m.body()
	if !strings.Contains(body, "flux-system") || !strings.Contains(body, "infra-cni") {
		t.Errorf("loaded reconcile body should render the waterfall:\n%s", body)
	}
	if !strings.Contains(body, "reconciling") {
		t.Errorf("reconciling node should show in the waterfall:\n%s", body)
	}
}

func TestReconcileModel_FluxNotInstalledGuidance(t *testing.T) {
	m := NewReconcileModel()
	m.Update(reconcileLoadedMsg{err: ports.ErrFluxNotInstalled})
	if !strings.Contains(m.body(), "Flux is not installed") {
		t.Errorf("ErrFluxNotInstalled should render guidance, got:\n%s", m.body())
	}
}

func TestReconcileModel_ErrorRendersWarn(t *testing.T) {
	m := NewReconcileModel()
	m.Update(reconcileLoadedMsg{err: errors.New("boom")})
	if !strings.Contains(m.body(), "boom") {
		t.Errorf("a fetch error should surface in the body, got:\n%s", m.body())
	}
}

// --- T8 OpenBao tab render (pure) ---

func TestRenderOpenBaoStatus_SealedAndMarkers(t *testing.T) {
	res := openbao.StatusResult{
		Pods: []ports.BaoStatus{
			{Pod: "openbao-0", Initialized: true, Sealed: false, HAMode: "active", Version: "2.5.3"},
			{Pod: "openbao-2", Initialized: true, Sealed: true, Version: "2.5.3"},
		},
		BootstrapFinalized:        "2026-07-06T00:00:00Z",
		ControllerAuthInstalled:   "",
		PolicyGenerationInstalled: 2,
		PolicyGenerationExpected:  3, // drift
	}
	out := RenderOpenBaoStatus(res)
	for _, want := range []string{
		"openbao-0", "active",
		"openbao-2", "SEALED",
		"1/2 unsealed",
		"bootstrap-finalized: yes",
		"controller-auth:     no",
		"policy-generation:   2 (expected 3)",
		"drift",
		"1 pod(s) SEALED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("openbao render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderOpenBaoStatus_NoPods(t *testing.T) {
	if !strings.Contains(RenderOpenBaoStatus(openbao.StatusResult{}), "no OpenBao pods") {
		t.Error("empty status should render the no-pods note")
	}
}

// P3: an uninitialized pod must be summarized, not just shown per-row.
func TestRenderOpenBaoStatus_UninitializedWarned(t *testing.T) {
	res := openbao.StatusResult{
		Pods: []ports.BaoStatus{
			{Pod: "openbao-0", Initialized: true, HAMode: "active", Version: "2.5.3"},
			{Pod: "openbao-1", Initialized: false, Sealed: true, Version: "2.5.3"},
		},
	}
	out := RenderOpenBaoStatus(res)
	if !strings.Contains(out, "UNINITIALIZED") {
		t.Errorf("per-pod row should mark the uninitialized pod:\n%s", out)
	}
	if !strings.Contains(out, "1 pod(s) UNINITIALIZED") || !strings.Contains(out, "openbao init") {
		t.Errorf("uninitialized pods should be summarized with a remediation, got:\n%s", out)
	}
}

func TestOpenBaoModel_LoadedRenders(t *testing.T) {
	m := NewOpenBaoModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(openbaoLoadedMsg{res: openbao.StatusResult{
		Pods: []ports.BaoStatus{{Pod: "openbao-0", Initialized: true, Version: "2.5.3"}},
	}})
	if !strings.Contains(m.body(), "openbao-0") {
		t.Errorf("loaded openbao body should render the pod line:\n%s", m.body())
	}
}

// --- Root wiring: 4 tabs + digit jumps ---

func TestRootModel_HasFourTabsAndDigitJumps(t *testing.T) {
	r := NewRootModel("", RootTabFleet)
	if len(r.tabs) != 4 {
		t.Fatalf("expected 4 tabs (Fleet/Contexts/Reconcile/OpenBao), got %d", len(r.tabs))
	}
	// Digit '3' jumps to Reconcile, '4' to OpenBao.
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if r.active != int(RootTabReconcile) {
		t.Errorf("key '3' should select the Reconcile tab, got active=%d", r.active)
	}
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if r.active != int(RootTabOpenBao) {
		t.Errorf("key '4' should select the OpenBao tab, got active=%d", r.active)
	}
}

// P2 regression: an async load result that arrives while a DIFFERENT tab
// is active must still reach its owning (inactive) tab — the root
// broadcasts async messages. Init() starts every tab's fetch, so without
// the broadcast the inactive tab's result is dropped and it sits at
// "loading…" until manual refresh.
func TestRootModel_BroadcastsAsyncToInactiveTab(t *testing.T) {
	r := NewRootModel("", RootTabFleet) // Fleet is the ACTIVE tab
	r.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Deliver the Reconcile tab's load result WHILE Fleet is active.
	r.Update(reconcileLoadedMsg{graph: ports.Graph{Nodes: []ports.GraphNode{
		{Name: "flux-system", Ready: true},
	}}})

	rec, ok := r.tabs[RootTabReconcile].model.(*ReconcileModel)
	if !ok {
		t.Fatal("tab 3 is not a *ReconcileModel")
	}
	if !rec.loaded {
		t.Fatal("inactive Reconcile tab dropped its async load — should have received the broadcast")
	}
	if !strings.Contains(rec.body(), "flux-system") {
		t.Errorf("Reconcile body should render the loaded graph after broadcast:\n%s", rec.body())
	}

	// And an unmatched KEY must NOT broadcast — it stays with the active
	// tab (interaction routing). Deliver 'r' (refresh) and confirm the
	// OpenBao tab (inactive) did not flip its loaded flag off via a
	// spurious refresh (it never received the key).
	ob, ok := r.tabs[RootTabOpenBao].model.(*OpenBaoModel)
	if !ok {
		t.Fatal("tab 4 is not an *OpenBaoModel")
	}
	ob.loaded = true // pretend it had loaded
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !ob.loaded {
		t.Error("an unmatched key must go to the ACTIVE tab only, not broadcast to OpenBao")
	}
}
