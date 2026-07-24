package screens

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
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

	// repoRoot is the fleet checkout the root TUI was launched against
	// (--repo resolution) — the New Cluster tab inherits it, including
	// on the discard-reset path.
	repoRoot string

	// Init-tab plumbing (T6 root-router embed). initOpts is the
	// InitOptions the embedded panel writes into on Apply; initPanel is
	// the live panel model (rebuilt fresh when the operator backs out
	// so a later visit starts clean). Read post-run via InitResult.
	initOpts  *clusterinit.InitOptions
	initPanel *initform.PanelModel
}

// tabSpec is one entry in the top tab bar.
type tabSpec struct {
	name  string
	model tea.Model
	// modal marks a screen whose body accepts free TEXT input (the init
	// panel). The root must not intercept its keys — 'q', '[', ']' and
	// digits are typed characters there, not navigation. Only Ctrl+C
	// stays global.
	modal bool
}

// RootTab indexes the tabs in display order. Keep these stable — the
// cobra entry points use them to pick the starting tab.
type RootTab int

const (
	RootTabFleet RootTab = iota
	RootTabContext
	RootTabInit
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

	// The embedded init panel inherits the ROOT's fleet context
	// (manual TTY finding 2026-07-20: launching `bootstrap --repo
	// <existing-fleet>` and opening New Cluster showed new-repo with an
	// empty repo path — the operator's context was dropped). Repo path
	// prefills from the root's --repo resolution; when that checkout
	// already carries scaffolded cluster overlays, the mode defaults to
	// existing-fleet (add cluster N+1, inherit sibling pins) — both
	// remain ordinary editable fields. No live-cluster probe here: the
	// gather is synchronous (3s budget) and would delay every
	// `bootstrap` launch; the standalone `bootstrap init` path carries
	// it. On Apply the root program exits and the cobra layer prints
	// the equivalent init command.
	initOpts, initPanel := newInitTabPanel(repoRoot)

	r := &RootModel{
		tabs: []tabSpec{
			{name: "Fleet", model: fleet},
			{name: "Contexts", model: contexts},
			{name: "New Cluster", model: initPanel, modal: true},
		},
		active:    int(startTab),
		keys:      bttui.DefaultKeyMap(),
		repoRoot:  repoRoot,
		initOpts:  initOpts,
		initPanel: initPanel,
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
	case tea.KeyPressMsg:
		// A modal tab (the init panel) owns its keys DYNAMICALLY
		// (manual TTY finding 2026-07-20 — a static always-modal gate
		// trapped operators on the tab):
		//   - while a text field is EDITING, every key except Ctrl+C is
		//     typed input ('q', digits, brackets included);
		//   - in NAV mode the root's tab-switch keys work normally and
		//     PRESERVE the panel state (come back later, nothing lost);
		//     'q' stays the panel's own "discard + back" (handled in
		//     forwardToActive), and Ctrl+C quits globally.
		if m.tabs[m.active].modal {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			if m.initPanel != nil && m.initPanel.Editing() {
				return m.forwardToActive(msg)
			}
			switch {
			case key.Matches(msg, m.keys.TopTabNext):
				m.active = (m.active + 1) % len(m.tabs)
				return m, nil
			case key.Matches(msg, m.keys.TopTabPrev):
				m.active = (m.active - 1 + len(m.tabs)) % len(m.tabs)
				return m, nil
			case key.Matches(msg, m.keys.TopTab1):
				m.active = 0
				return m, nil
			case key.Matches(msg, m.keys.TopTab2):
				m.active = 1
				return m, nil
			case key.Matches(msg, m.keys.TopTab3):
				return m, nil // already here
			}
			return m.forwardToActive(msg)
		}
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
		case key.Matches(msg, m.keys.TopTab3):
			if 2 < len(m.tabs) {
				m.active = 2
			}
			return m, nil
		}
	}
	// Forward everything else (unhandled keys, ticks, probe completions)
	// to the ACTIVE screen. Both non-modal tabs are coherent under this:
	// Fleet is the start tab and owns the async probes/ticks it starts;
	// Contexts loads synchronously in its constructor (Init is a no-op),
	// so it has no in-flight async result that could be dropped here.
	return m.forwardToActive(msg)
}

// forwardToActive routes msg to the active screen, then post-processes
// the init panel's terminal states: a CANCELLED panel ('q' in nav mode)
// becomes "back to Fleet" with a fresh panel (its tea.Quit is
// swallowed — quitting the whole program because the operator backed
// out of one tab is wrong); an APPLIED panel lets the quit through so
// the cobra layer can print the equivalent init command (InitResult).
func (m *RootModel) forwardToActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.tabs[m.active].model, cmd = m.tabs[m.active].model.Update(msg)
	if m.tabs[m.active].modal && m.initPanel != nil {
		if m.initPanel.Cancelled() {
			// Discard = fresh FORM, same CONTEXT — the rebuilt panel
			// re-inherits the root's fleet repo/mode, exactly like the
			// first visit.
			m.initOpts, m.initPanel = newInitTabPanel(m.repoRoot)
			m.tabs[m.active].model = m.initPanel
			m.active = int(RootTabFleet)
			return m, nil // swallow the panel's tea.Quit
		}
		if m.initPanel.Applied() {
			return m, tea.Quit
		}
	}
	return m, cmd
}

// View stacks the tab bar above the active screen. As the top-level
// program model it also declares the terminal modes that used to be
// NewProgram options in v1 (alt-screen + mouse cell motion) — see
// RunRoot. Child screens return their own tea.View; we compose their
// rendered .Content into this frame.
func (m *RootModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return m.frame("Initializing…")
	}
	tabBar := m.renderTabBar()
	body := m.tabs[m.active].model.View().Content
	return m.frame(lipgloss.JoinVertical(lipgloss.Left, tabBar, body))
}

// frame wraps rendered content in a tea.View carrying the program's
// terminal-mode flags (v2 declares these on the View, not the program).
func (m *RootModel) frame(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
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

// newInitTabPanel builds the New Cluster tab's options+panel with the
// root's fleet context inherited (repo path always; existing-fleet mode
// when the checkout has scaffolded siblings). Used at construction AND
// on the discard-reset path so both start identically.
func newInitTabPanel(repoRoot string) (*clusterinit.InitOptions, *initform.PanelModel) {
	o := &clusterinit.InitOptions{Repo: repoRoot}
	if hasFleetSiblings(repoRoot) {
		o.FleetMode = clusterinit.FleetExistingFleet
	}
	return o, initform.NewEmbeddedPanel(o, "", nil)
}

// hasFleetSiblings reports whether repoRoot already contains at least
// one scaffolded cluster overlay (clusters/<name>/cluster-config.env,
// including the nested eu/dc1-shape one level deeper). That is the
// signal the checkout is an EXISTING fleet — the New Cluster tab then
// defaults to fleet-mode existing-fleet instead of new-repo.
func hasFleetSiblings(repoRoot string) bool {
	base := filepath.Join(repoRoot, "clusters")
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, e.Name(), "cluster-config.env")); err == nil {
			return true
		}
		// Nested cluster names (clusters/eu/dc1/…) sit one level deeper.
		sub, err := os.ReadDir(filepath.Join(base, e.Name()))
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() {
				if _, err := os.Stat(filepath.Join(base, e.Name(), se.Name(), "cluster-config.env")); err == nil {
					return true
				}
			}
		}
	}
	return false
}

// InitResult reports the embedded init panel's outcome after the
// program exits: the equivalent `kube-dc bootstrap init …` command and
// true when the operator completed Apply; ("", false) otherwise. The
// cobra `bootstrap` entry prints the command so the operator can run
// the actual install (the root TUI never runs the apply engine itself).
func (m *RootModel) InitResult() (string, bool) {
	if m.initPanel == nil || m.initOpts == nil {
		return "", false
	}
	eq, err := m.initPanel.Result(m.initOpts)
	if err != nil {
		return "", false
	}
	return eq, true
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

func (m *contextLoadErrorModel) View() tea.View {
	w := m.width
	if w == 0 {
		w = 80
	}
	body := bttui.WarnBox.
		Width(w - 4).
		Render("Could not load kubeconfig.\n\n" + m.err.Error() + "\n\nFix the kubeconfig file and re-launch `kube-dc bootstrap`.")
	return tea.NewView(bttui.AppStyle.Render(body))
}
