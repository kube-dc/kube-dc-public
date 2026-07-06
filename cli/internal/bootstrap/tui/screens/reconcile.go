package screens

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// reconcile.go is the T7 "Reconcile" tab: a thin live shell over the
// tested RenderWaterfall. It reflects the operator's CURRENT kubeconfig
// context (not a fleet cluster — DiscoverFluxGraph needs a client-go
// client), fetching the Flux dependency graph on open + on 'r'. Auto-tick
// refresh is deliberately omitted (tick routing across tabs is fiddly and
// can't be headless-validated) — manual 'r' is predictable + testable.
//
// The fetch (session build + DiscoverFluxGraph) is the live shell; the
// render is RenderWaterfall (unit-tested), and Update/View are
// headless-tested by feeding reconcileLoadedMsg.

// reconcileLoadedMsg carries the result of one graph fetch.
type reconcileLoadedMsg struct {
	graph ports.Graph
	err   error
	at    time.Time
}

// ReconcileModel is the T7 tab model.
type ReconcileModel struct {
	width, height int
	graph         ports.Graph
	loaded        bool
	err           error
	lastAt        time.Time
	vp            viewport.Model
	keys          bttui.KeyMap
}

// NewReconcileModel builds the tab. It fetches lazily (Init), so
// construction never touches a cluster.
func NewReconcileModel() *ReconcileModel {
	return &ReconcileModel{vp: viewport.New(0, 0), keys: bttui.DefaultKeyMap()}
}

func (m *ReconcileModel) Init() tea.Cmd { return fetchReconcileGraph() }

// fetchReconcileGraph builds a k8s session from the current kubeconfig
// and reads the Flux graph. Errors (no kubeconfig, Flux absent) ride the
// message and render as guidance rather than crashing the tab.
func fetchReconcileGraph() tea.Cmd {
	return func() tea.Msg {
		session, err := bootstrap.NewSession(bootstrap.Options{})
		if session != nil {
			defer session.Close()
		}
		if err != nil || session.K8s == nil {
			return reconcileLoadedMsg{err: fmt.Errorf("no cluster client — set KUBECONFIG to the target context (%v)", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		g, gerr := session.K8s.DiscoverFluxGraph(ctx)
		return reconcileLoadedMsg{graph: g, err: gerr, at: time.Now()}
	}
}

func (m *ReconcileModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width = maxInt(msg.Width-4, 1)
		m.vp.Height = maxInt(msg.Height-4, 1)
		m.refresh()
	case reconcileLoadedMsg:
		m.loaded = true
		m.graph = msg.graph
		m.err = msg.err
		m.lastAt = msg.at
		m.refresh()
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.Refresh) {
			m.loaded = false
			m.refresh()
			return m, fetchReconcileGraph()
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// refresh recomputes the viewport body from current state.
func (m *ReconcileModel) refresh() {
	m.vp.SetContent(m.body())
}

// body is the pure state→string mapping (no viewport) so tests can
// assert it directly without a WindowSize dance.
func (m *ReconcileModel) body() string {
	switch {
	case !m.loaded:
		return bttui.Muted.Render("loading Flux graph from the current kubeconfig context…")
	case errors.Is(m.err, ports.ErrFluxNotInstalled):
		return bttui.Muted.Render("Flux is not installed on the current kubeconfig context.\n" +
			"This tab reflects your CURRENT context (KUBECONFIG), not a fleet cluster.")
	case m.err != nil:
		return bttui.WarnBox.Render("could not read the Flux graph:\n" + m.err.Error())
	default:
		return RenderWaterfall(m.graph)
	}
}

func (m *ReconcileModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	w := m.width - 2

	right := bttui.Muted.Render("press r to load")
	if !m.lastAt.IsZero() {
		right = bttui.Muted.Render(fmt.Sprintf("updated %ds ago", int(time.Since(m.lastAt).Seconds())))
	}
	title := joinSpaced(w, bttui.Title.Render(" Flux Reconcile ")+"  "+
		bttui.Muted.Render("current kubeconfig context"), right)

	help := bttui.HelpBar.Render(m.keys.Refresh.Help().Key + " refresh · ] / [ tabs · q quit")
	body := lipgloss.JoinVertical(lipgloss.Left, title, m.vp.View(), help)
	return bttui.AppStyle.Render(body)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
