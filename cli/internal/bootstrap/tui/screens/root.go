package screens

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// RootModel is the top-level Bubble Tea model that composes every
// bootstrap screen (fleet view, context manager, future day-2 editor)
// behind a single tab bar — see installer-prd §9.9.5.
//
// Top-tab keys (`]`/`[` cycle, `1`/`2` jump) are intercepted here; every
// other tea.Msg is forwarded to the active screen unchanged. WindowSize
// is forwarded with Height reduced by the tab bar's row count so each
// screen's existing layout math keeps working.
type RootModel struct {
	width, height int

	tabs   []tabSpec
	active int

	keys bttui.KeyMap
}

// tabSpec is one entry in the top tab bar.
type tabSpec struct {
	name  string
	model tea.Model
}

// RootTab indexes the tabs in display order. Keep these stable — the
// cobra entry points use them to pick the starting tab.
type RootTab int

const (
	RootTabFleet RootTab = iota
	RootTabContext
)

// NewRootModel builds the integrated bootstrap TUI rooted at the named
// fleet repo. startTab decides which screen is active on launch (the
// `kube-dc bootstrap` cobra entry uses RootTabFleet; `kube-dc bootstrap
// context` uses RootTabContext). When the context model fails to load
// — typically a malformed kubeconfig — we still return the program with
// the fleet tab usable; the contexts tab renders the load error in
// place so the operator can switch to it and see what went wrong.
func NewRootModel(repoRoot string, startTab RootTab) *RootModel {
	fleet := NewFleetModel(repoRoot)

	var contexts tea.Model
	if cm, err := NewContextModel(); err == nil {
		contexts = cm
	} else {
		contexts = newContextLoadErrorModel(err)
	}

	r := &RootModel{
		tabs: []tabSpec{
			{name: "Fleet", model: fleet},
			{name: "Contexts", model: contexts},
		},
		active: int(startTab),
		keys:   bttui.DefaultKeyMap(),
	}
	if r.active < 0 || r.active >= len(r.tabs) {
		r.active = 0
	}
	return r
}

// Init forwards to every child so they kick off their initial commands
// up front (probes, kubeconfig load) and not just when first focused.
// This keeps the inactive tab "warm" so switching is instant.
func (m *RootModel) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.tabs))
	for i := range m.tabs {
		if c := m.tabs[i].model.Init(); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

// Update intercepts the top-tab keys and forwards everything else to
// the active screen. Quit (q / Ctrl+C) is also handled here so a single
// shortcut quits the whole program from any tab.
func (m *RootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Reserve one row for the tab bar; forward the reduced size to
		// every child so they re-layout without knowing about the root.
		child := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - 1}
		var cmds []tea.Cmd
		for i := range m.tabs {
			var cmd tea.Cmd
			m.tabs[i].model, cmd = m.tabs[i].model.Update(child)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.TopTabNext):
			m.active = (m.active + 1) % len(m.tabs)
			return m, nil
		case key.Matches(msg, m.keys.TopTabPrev):
			m.active = (m.active - 1 + len(m.tabs)) % len(m.tabs)
			return m, nil
		case key.Matches(msg, m.keys.TopTab1):
			if 0 < len(m.tabs) {
				m.active = 0
			}
			return m, nil
		case key.Matches(msg, m.keys.TopTab2):
			if 1 < len(m.tabs) {
				m.active = 1
			}
			return m, nil
		}
	}
	// Forward everything else (including unhandled keys, ticks, probe
	// completions) ONLY to the active screen. Inactive screens still
	// receive the initial WindowSizeMsg + Init's commands' results, so
	// their state stays coherent on switch-back.
	var cmd tea.Cmd
	m.tabs[m.active].model, cmd = m.tabs[m.active].model.Update(msg)
	return m, cmd
}

// View stacks the tab bar above the active screen.
func (m *RootModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	tabBar := m.renderTabBar()
	body := m.tabs[m.active].model.View()
	return lipgloss.JoinVertical(lipgloss.Left, tabBar, body)
}

// renderTabBar produces a single-row top bar. Active tab uses the
// Title (filled badge) style; inactive tabs use Muted with a hint
// number (`1`, `2`, …) so the digit shortcut is discoverable.
func (m *RootModel) renderTabBar() string {
	var parts []string
	for i, t := range m.tabs {
		label := t.name
		if i == m.active {
			parts = append(parts, bttui.Title.Render(" "+label+" "))
		} else {
			// `1 ` muted prefix mirrors the digit shortcut.
			prefix := bttui.KeyLabel.Render(string(rune('1'+i)) + " ")
			parts = append(parts, prefix+bttui.Muted.Render(label))
		}
	}
	bar := strings.Join(parts, "  ")
	// Right-side hint so operators discover ]/[ without opening help.
	hint := bttui.Muted.Render("] / [ cycle")
	pad := m.width - lipgloss.Width(bar) - lipgloss.Width(hint)
	if pad < 1 {
		pad = 1
	}
	return " " + bar + strings.Repeat(" ", pad) + hint
}

// contextLoadErrorModel is the placeholder we render in place of the
// real ContextModel when NewContextModel returns an error (typically a
// malformed kubeconfig). Lets the user switch to that tab and see why
// it failed instead of crashing the whole program.
type contextLoadErrorModel struct {
	err           error
	width, height int
}

func newContextLoadErrorModel(err error) tea.Model {
	return &contextLoadErrorModel{err: err}
}

func (m *contextLoadErrorModel) Init() tea.Cmd { return nil }

func (m *contextLoadErrorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = sz.Width, sz.Height
	}
	return m, nil
}

func (m *contextLoadErrorModel) View() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	body := bttui.WarnBox.
		Width(w - 4).
		Render("Could not load kubeconfig.\n\n" + m.err.Error() + "\n\nFix the kubeconfig file and re-launch `kube-dc bootstrap`.")
	return bttui.AppStyle.Render(body)
}
