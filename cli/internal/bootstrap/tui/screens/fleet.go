// Package screens contains the per-screen Bubble Tea models that compose
// the bootstrap TUI. Each model is independently testable via teatest.
package screens

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// fleetPaneFocus enumerates which pane currently owns the keyboard.
// Per installer-prd §9.9.1, Tab cycles focus and arrows scope to the
// focused pane only.
type fleetPaneFocus int

const (
	paneFocusList fleetPaneFocus = iota
	paneFocusDetails
	paneFocusDrillDown
)

// FleetModel is the multi-cluster landing screen — the home of the
// `kube-dc bootstrap` TUI when invoked without arguments.
//
// Layout:
//   - Top pane: cluster list with status pills.
//   - Bottom pane: cluster details (pills, Kustomizations sub-list,
//     image-tag drift). When focused on a non-Ready Kustomization row
//     and Enter is pressed, a right-side drill-down panel opens with
//     the full condition Reason/Message — see §9.9.4.
type FleetModel struct {
	repoRoot string

	width, height int

	clusters     []discover.Cluster
	selected     int
	loading      bool
	err          error
	lastLoadedAt time.Time

	// Per-cluster probe results, keyed by Cluster.Name.
	statuses map[string]discover.ProbeResult

	details viewport.Model

	// Pane focus + sub-cursors. The details pane has its own cursor for
	// the Kustomization sub-list; the drill-down has its own viewport.
	focus               fleetPaneFocus
	kustomizationCursor int
	drillDown           viewport.Model
	drillDownOpen       bool
	drillDownTitle      string

	// pendingActionFor records the cluster currently running a dispatched
	// FixAction (admin login, …) so the row's status pill shows "running…"
	// while the subprocess is in flight. Empty when no action is pending.
	pendingActionFor string

	help help.Model
	keys bttui.KeyMap
}

// NewFleetModel constructs the fleet landing model. repoRoot is the
// absolute path to the kube-dc-fleet repo on disk.
func NewFleetModel(repoRoot string) *FleetModel {
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Bold(true)
	h.Styles.ShortDesc = bttui.Muted
	h.Styles.ShortSeparator = bttui.Muted
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc

	return &FleetModel{
		repoRoot:  repoRoot,
		statuses:  map[string]discover.ProbeResult{},
		details:   viewport.New(0, 0),
		drillDown: viewport.New(0, 0),
		help:      h,
		keys:      bttui.DefaultKeyMap(),
		loading:   true,
		focus:     paneFocusList,
	}
}

// Init kicks off the first fleet enumeration and starts the 60s refresh
// tick.
func (m *FleetModel) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.tickCmd())
}

func (m *FleetModel) loadCmd() tea.Cmd {
	return func() tea.Msg {
		_ = context.Background
		clusters, err := discover.ListClusters(m.repoRoot)
		if err != nil {
			return bttui.FleetErrorMsg{Err: err}
		}
		return bttui.FleetLoadedMsg{Clusters: clusters, At: time.Now()}
	}
}

// Update handles messages and key events.
func (m *FleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		m.refreshDetails()
	case bttui.FleetLoadedMsg:
		m.loading = false
		m.err = nil
		m.clusters = msg.Clusters
		m.lastLoadedAt = msg.At
		if m.selected >= len(m.clusters) {
			m.selected = 0
		}
		m.refreshDetails()
		return m, tea.Batch(m.probeAllCmds()...)
	case bttui.FleetErrorMsg:
		m.loading = false
		m.err = msg.Err
	case bttui.ClusterProbeMsg:
		for _, c := range m.clusters {
			if c.Name == msg.Name {
				m.statuses[msg.Name] = msg.Result
				break
			}
		}
		// Probes can land while the cursor sits on a different
		// Kustomization than the one whose data was just refreshed —
		// clamp the cursor + redraw.
		m.clampKustomizationCursor()
		m.refreshDetails()
	case bttui.TickMsg:
		cmds := append(m.probeAllCmds(), m.tickCmd())
		return m, tea.Batch(cmds...)
	case bttui.LoginDoneMsg:
		m.pendingActionFor = ""
		if msg.Err != nil {
			m.err = fmt.Errorf("login %s failed: %w", msg.Cluster, msg.Err)
		} else {
			m.err = nil
		}
		return m, m.reprobeOne(msg.Cluster)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	// Forward viewport scroll messages (mouse wheel) to whichever pane
	// has focus so wheel-scroll behaves intuitively.
	switch m.focus {
	case paneFocusDetails:
		var cmd tea.Cmd
		m.details, cmd = m.details.Update(msg)
		return m, cmd
	case paneFocusDrillDown:
		var cmd tea.Cmd
		m.drillDown, cmd = m.drillDown.Update(msg)
		return m, cmd
	}
	return m, nil
}

// probeAllCmds returns one tea.Cmd per cluster.
func (m *FleetModel) probeAllCmds() []tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.clusters))
	for _, c := range m.clusters {
		c := c
		if c.KubeAPIURL == "" {
			cmds = append(cmds, func() tea.Msg {
				return bttui.ClusterProbeMsg{
					Name: c.Name,
					Result: discover.ProbeResult{
						Status:  discover.StatusUnknown,
						Detail:  "no KUBE_API_EXTERNAL_URL in cluster-config.env",
						FixHint: "edit clusters/" + c.Name + "/cluster-config.env",
					},
					At: time.Now(),
				}
			})
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			probe, err := discover.NewClusterProbe(ctx, c.KubeAPIURL, 3*time.Second)
			if err != nil {
				return bttui.ClusterProbeMsg{
					Name: c.Name,
					Result: discover.ProbeResult{
						Status: discover.StatusUnreachable,
						Detail: "probe init: " + err.Error(),
					},
					At: time.Now(),
				}
			}
			if c.Env != nil {
				probe.ExpectedTags = discover.DefaultExpectedTags(c.Env)
			}
			return bttui.ClusterProbeMsg{
				Name:   c.Name,
				Result: probe.Run(ctx),
				At:     time.Now(),
			}
		})
	}
	return cmds
}

func (m *FleetModel) tickCmd() tea.Cmd {
	return tea.Tick(60*time.Second, func(time.Time) tea.Msg { return bttui.TickMsg{} })
}

// dispatchFixAction runs the structured FixAction for the row whose
// Detail line shows a "Run: …" hint — see installer-prd §9.9.3. Returns
// nil when the action isn't dispatchable (no FixAction, missing domain,
// or kind we don't recognise).
func (m *FleetModel) dispatchFixAction(name string, action *discover.FixAction) tea.Cmd {
	if action == nil {
		return nil
	}
	switch action.Kind {
	case discover.FixActionAdminLogin:
		if action.Domain == "" {
			m.err = fmt.Errorf("cannot dispatch admin login for %q: no domain in FixAction", name)
			return nil
		}
		m.pendingActionFor = name
		args := []string{"login", "--domain", action.Domain, "--admin"}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return bttui.LoginDoneMsg{Cluster: name, Admin: true, Err: err}
		})
	case discover.FixActionTenantLogin:
		// Tenant org-prompt isn't wired yet — surface the exact command.
		m.err = fmt.Errorf("tenant login from the TUI is not yet implemented — run `kube-dc login --domain %s --org <your-org>` directly", action.Domain)
		return nil
	default:
		m.err = fmt.Errorf("unknown FixAction kind %q for %q", action.Kind, name)
		return nil
	}
}

// execLoginCmd is the explicit-key (L) admin-login path, used when the
// operator presses L on a row regardless of whether the row has a
// FixAction. Distinct from dispatchFixAction because L works even on
// Ready rows (e.g. for re-login after token expiry).
func (m *FleetModel) execLoginCmd(admin bool) tea.Cmd {
	if len(m.clusters) == 0 {
		return nil
	}
	c := m.clusters[m.selected]
	if c.Domain == "" {
		m.err = fmt.Errorf("cluster %q has no DOMAIN in cluster-config.env", c.Name)
		return nil
	}
	if !admin {
		m.err = fmt.Errorf("tenant login from the TUI is not yet implemented — run `kube-dc login --domain %s --org <your-org>` directly", c.Domain)
		return nil
	}
	m.pendingActionFor = c.Name
	args := []string{"login", "--domain", c.Domain, "--admin"}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return bttui.LoginDoneMsg{Cluster: c.Name, Admin: true, Err: err}
	})
}

// reprobeOne re-runs the probe for exactly one cluster (post-login).
func (m *FleetModel) reprobeOne(name string) tea.Cmd {
	var target *discover.Cluster
	for i := range m.clusters {
		if m.clusters[i].Name == name {
			target = &m.clusters[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	c := *target
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		probe, err := discover.NewClusterProbe(ctx, c.KubeAPIURL, 3*time.Second)
		if err != nil {
			return bttui.ClusterProbeMsg{
				Name:   c.Name,
				Result: discover.ProbeResult{Status: discover.StatusUnreachable, Detail: err.Error()},
				At:     time.Now(),
			}
		}
		if c.Env != nil {
			probe.ExpectedTags = discover.DefaultExpectedTags(c.Env)
		}
		return bttui.ClusterProbeMsg{Name: c.Name, Result: probe.Run(ctx), At: time.Now()}
	}
}

// handleKey routes keystrokes per current pane focus (§9.9.1).
func (m *FleetModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Refresh):
		m.loading = true
		return m, m.loadCmd()
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.relayout()
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		m.focusNext()
		m.refreshDetails()
		return m, nil
	case key.Matches(msg, m.keys.ShiftTab):
		m.focusPrev()
		m.refreshDetails()
		return m, nil
	case key.Matches(msg, m.keys.Esc):
		// Esc closes the drill-down or steps focus back to the list.
		if m.drillDownOpen {
			m.closeDrillDown()
			return m, nil
		}
		if m.focus != paneFocusList {
			m.focus = paneFocusList
			m.refreshDetails()
		}
		return m, nil
	case key.Matches(msg, m.keys.LoginAdmin):
		if cmd := m.execLoginCmd(true); cmd != nil {
			return m, cmd
		}
		return m, nil
	case key.Matches(msg, m.keys.LoginOrg):
		if cmd := m.execLoginCmd(false); cmd != nil {
			return m, cmd
		}
		return m, nil
	case key.Matches(msg, m.keys.Enter):
		return m.handleEnter()
	case key.Matches(msg, m.keys.Up):
		return m.handleArrow(-1)
	case key.Matches(msg, m.keys.Down):
		return m.handleArrow(+1)
	case key.Matches(msg, m.keys.PageUp), key.Matches(msg, m.keys.PageDown),
		key.Matches(msg, m.keys.Home), key.Matches(msg, m.keys.End):
		// Page/Home/End forward to the focused viewport.
		switch m.focus {
		case paneFocusDetails:
			var cmd tea.Cmd
			m.details, cmd = m.details.Update(msg)
			return m, cmd
		case paneFocusDrillDown:
			var cmd tea.Cmd
			m.drillDown, cmd = m.drillDown.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

// handleArrow routes Up/Down to whichever pane has focus.
func (m *FleetModel) handleArrow(delta int) (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneFocusList:
		next := m.selected + delta
		if next >= 0 && next < len(m.clusters) {
			m.selected = next
			m.kustomizationCursor = 0 // reset details cursor on cluster change
			m.refreshDetails()
		}
	case paneFocusDetails:
		recs := m.currentReconcilers()
		if len(recs) == 0 {
			// No selectable rows in details — let viewport scroll instead.
			if delta < 0 {
				m.details.LineUp(1)
			} else {
				m.details.LineDown(1)
			}
			return m, nil
		}
		next := m.kustomizationCursor + delta
		if next >= 0 && next < len(recs) {
			m.kustomizationCursor = next
			m.refreshDetails()
		}
	case paneFocusDrillDown:
		if delta < 0 {
			m.drillDown.LineUp(1)
		} else {
			m.drillDown.LineDown(1)
		}
	}
	return m, nil
}

// handleEnter dispatches based on focus. On the list pane, it auto-runs
// the row's FixAction (e.g. admin login) when present — the actionable-
// status pattern from §9.9.3. On the details pane, it opens the drill-
// down for the selected Kustomization (§9.9.4).
func (m *FleetModel) handleEnter() (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneFocusList:
		if len(m.clusters) == 0 {
			return m, nil
		}
		c := m.clusters[m.selected]
		if r, ok := m.statuses[c.Name]; ok && r.FixAction != nil {
			if cmd := m.dispatchFixAction(c.Name, r.FixAction); cmd != nil {
				return m, cmd
			}
		}
		// No FixAction — Enter on a Ready row drops focus into details
		// so the operator can drill into Kustomizations without a TAB.
		m.focus = paneFocusDetails
		m.kustomizationCursor = 0
		m.refreshDetails()
		return m, nil
	case paneFocusDetails:
		recs := m.currentReconcilers()
		if len(recs) == 0 {
			return m, nil
		}
		if m.kustomizationCursor >= 0 && m.kustomizationCursor < len(recs) {
			m.openDrillDown(recs[m.kustomizationCursor])
		}
		return m, nil
	case paneFocusDrillDown:
		// Enter inside drill-down is a no-op; Esc closes.
		return m, nil
	}
	return m, nil
}

// focusNext / focusPrev cycle pane focus. Skips drill-down when closed.
func (m *FleetModel) focusNext() {
	switch m.focus {
	case paneFocusList:
		m.focus = paneFocusDetails
		m.kustomizationCursor = 0
	case paneFocusDetails:
		if m.drillDownOpen {
			m.focus = paneFocusDrillDown
		} else {
			m.focus = paneFocusList
		}
	case paneFocusDrillDown:
		m.focus = paneFocusList
	}
}

func (m *FleetModel) focusPrev() {
	switch m.focus {
	case paneFocusList:
		if m.drillDownOpen {
			m.focus = paneFocusDrillDown
		} else {
			m.focus = paneFocusDetails
			m.kustomizationCursor = 0
		}
	case paneFocusDetails:
		m.focus = paneFocusList
	case paneFocusDrillDown:
		m.focus = paneFocusDetails
	}
}

// openDrillDown shows the full Kustomization status in the right-side
// panel and shifts focus to it (§9.9.4). Layout (top → bottom):
//
//   - Title pill with the resource name.
//   - Headline (state + suspend flag) in the same colour as the row glyph.
//   - All conditions with status + reason + message.
//   - Flux revisions: lastAttempted vs lastApplied — non-empty pair tells
//     you "the controller has tried X but only Y is reconciled".
//   - Copy-paste hints with the canonical kubectl + flux commands.
func (m *FleetModel) openDrillDown(rec discover.ReconcilerStatus) {
	var b strings.Builder
	b.WriteString(bttui.Title.Render(" " + rec.Name + " "))
	b.WriteString("\n\n")

	// Headline: state + suspend.
	stateGlyph := "✓ ready"
	if !rec.Ready {
		stateGlyph = "✗ not ready"
	}
	b.WriteString(bttui.Text.Render("state:     "))
	b.WriteString(bttui.Muted.Render(stateGlyph))
	if rec.Suspended {
		b.WriteString("  ")
		b.WriteString(lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⏸ suspended"))
	}
	b.WriteString("\n\n")

	// Conditions block — all of them, not just Ready, so the operator
	// can see Healthy / Reconciling / Stalled / etc. side by side.
	if len(rec.Conditions) > 0 {
		b.WriteString(bttui.Muted.Render("─ conditions ─") + "\n")
		for _, c := range rec.Conditions {
			glyph := "✓"
			if c.Status != "True" {
				glyph = "✗"
			}
			b.WriteString(bttui.Text.Render(glyph + " " + c.Type))
			if c.Reason != "" && c.Reason != c.Type {
				b.WriteString(bttui.Muted.Render("  " + c.Reason))
			}
			b.WriteString("\n")
			if c.Message != "" {
				for _, line := range strings.Split(c.Message, "\n") {
					b.WriteString(bttui.Muted.Render("    " + line))
					b.WriteString("\n")
				}
			}
		}
		b.WriteString("\n")
	} else if rec.Reason != "" || rec.Message != "" {
		// No raw conditions list (NoReadyCondition synthetic case) —
		// fall back to the synthesised reason + message that aggregate()
		// wrote in cluster.go for that path.
		b.WriteString(bttui.Muted.Render("─ summary ─") + "\n")
		if rec.Reason != "" {
			b.WriteString(bttui.Text.Render("reason:    "))
			b.WriteString(bttui.Muted.Render(rec.Reason))
			b.WriteString("\n")
		}
		if rec.Message != "" {
			for _, line := range strings.Split(rec.Message, "\n") {
				b.WriteString(bttui.Muted.Render(line))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// Flux revisions — only show when at least one is set, since for
	// a never-reconciled Kustomization both are empty and an empty
	// "revisions" block is just noise.
	if rec.LastAttemptedRevision != "" || rec.LastAppliedRevision != "" {
		b.WriteString(bttui.Muted.Render("─ revisions ─") + "\n")
		if rec.LastAttemptedRevision != "" {
			b.WriteString(bttui.Text.Render("attempted: "))
			b.WriteString(bttui.Muted.Render(rec.LastAttemptedRevision))
			b.WriteString("\n")
		}
		if rec.LastAppliedRevision != "" {
			b.WriteString(bttui.Text.Render("applied:   "))
			b.WriteString(bttui.Muted.Render(rec.LastAppliedRevision))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Copy-paste hints. Use the short kubectl/flux forms so they fit
	// in a 40%-wide pane on a typical terminal.
	b.WriteString(bttui.Muted.Render("─ investigate ─") + "\n")
	b.WriteString(bttui.Text.Render("kubectl describe ks/" + rec.Name + " -n flux-system") + "\n")
	b.WriteString(bttui.Text.Render("flux logs ks/" + rec.Name + " -n flux-system") + "\n")
	b.WriteString(bttui.Text.Render("flux reconcile kustomization " + rec.Name + " -n flux-system") + "\n")

	m.drillDown.SetContent(b.String())
	m.drillDown.GotoTop()
	m.drillDownOpen = true
	m.drillDownTitle = rec.Name
	m.focus = paneFocusDrillDown
	m.relayout()
	m.refreshDetails()
}

func (m *FleetModel) closeDrillDown() {
	m.drillDownOpen = false
	m.drillDownTitle = ""
	m.focus = paneFocusDetails
	m.relayout()
	m.refreshDetails()
}

// currentReconcilers returns the Kustomization rows for the selected
// cluster, or nil when the probe hasn't completed.
func (m *FleetModel) currentReconcilers() []discover.ReconcilerStatus {
	if len(m.clusters) == 0 {
		return nil
	}
	r, ok := m.statuses[m.clusters[m.selected].Name]
	if !ok {
		return nil
	}
	return r.Reconcilers
}

func (m *FleetModel) clampKustomizationCursor() {
	recs := m.currentReconcilers()
	if m.kustomizationCursor < 0 {
		m.kustomizationCursor = 0
	}
	if m.kustomizationCursor >= len(recs) {
		if len(recs) == 0 {
			m.kustomizationCursor = 0
		} else {
			m.kustomizationCursor = len(recs) - 1
		}
	}
}

// View renders the screen.
func (m *FleetModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	// AppStyle adds horizontal padding (left/right 1 each) only — no
	// vertical padding — so we own the full m.height for the body
	// stack. Keeping w = m.width-2 matches AppStyle's actual horizontal
	// budget. Older code subtracted 2 from height too, which left ~2
	// rows of blank space below the help bar.
	w := m.width - 2
	h := m.height

	right := bttui.Muted.Render("not yet loaded")
	if !m.lastLoadedAt.IsZero() {
		right = bttui.Muted.Render(
			fmt.Sprintf("updated %ds ago", int(time.Since(m.lastLoadedAt).Seconds())))
	}
	if m.loading {
		right = bttui.Muted.Render("loading…")
	}
	titleRow := joinSpaced(w, bttui.Title.Render(" Kube-DC Fleet ")+"  "+
		bttui.Muted.Render(m.repoRoot), right)

	// Reserve exactly: 1 title + 1 help bar + 1 error row when present.
	// No additional slack — the panes themselves carry borders, so any
	// extra here just renders as visible empty space at the bottom.
	chrome := 2
	if m.err != nil {
		chrome++
	}
	bodyH := h - chrome
	if bodyH < 8 {
		bodyH = 8
	}

	listH := len(m.clusters) + 2
	if listH < 5 {
		listH = 5
	}
	if listH > bodyH/2 {
		listH = bodyH / 2
	}
	detailsH := bodyH - listH

	// Top: cluster list, full width. Border colour reflects focus.
	topStyle := bttui.ListPaneFocused
	if m.focus != paneFocusList {
		topStyle = bttui.ListPane
	}
	top := topStyle.
		Width(w - 2).
		Height(listH - 2).
		Render(m.renderList(w - 6))

	// Bottom: details + (when open) drill-down side-by-side.
	bottomW := w - 2
	var bottom string
	if m.drillDownOpen {
		// 60/40 split: details left, drill-down right.
		drillW := bottomW * 4 / 10
		if drillW < 30 {
			drillW = 30
		}
		if drillW > bottomW-20 {
			drillW = bottomW - 20
		}
		detailsW := bottomW - drillW

		m.details.Width = detailsW - 4
		m.details.Height = detailsH - 2

		m.drillDown.Width = drillW - 4
		m.drillDown.Height = detailsH - 2

		detailsStyle := bttui.DetailsPane
		if m.focus == paneFocusDetails {
			detailsStyle = bttui.DetailsPaneFocused
		}
		drillStyle := bttui.DetailsPane
		if m.focus == paneFocusDrillDown {
			drillStyle = bttui.DetailsPaneFocused
		}

		left := detailsStyle.
			Width(detailsW - 2).
			Height(detailsH - 2).
			Render(m.details.View())
		right := drillStyle.
			Width(drillW - 2).
			Height(detailsH - 2).
			Render(m.drillDown.View())
		bottom = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	} else {
		m.details.Width = bottomW - 4
		m.details.Height = detailsH - 2
		detailsStyle := bttui.DetailsPane
		if m.focus == paneFocusDetails {
			detailsStyle = bttui.DetailsPaneFocused
		}
		bottom = detailsStyle.
			Width(bottomW - 2).
			Height(detailsH - 2).
			Render(m.details.View())
	}

	body := lipgloss.JoinVertical(lipgloss.Left, top, bottom)

	// Footer: errors + active-only help.
	var footerLines []string
	if m.err != nil {
		footerLines = append(footerLines,
			bttui.ErrorBox.Width(w).Render("error: "+m.err.Error()))
	}
	if m.help.ShowAll {
		footerLines = append(footerLines, bttui.HelpBar.Render(m.help.FullHelpView(m.activeFullHelp())))
	} else {
		footerLines = append(footerLines, bttui.HelpBar.Render(m.help.ShortHelpView(m.activeShortHelp())))
	}

	parts := append([]string{titleRow, body}, footerLines...)
	return bttui.AppStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// activeShortHelp implements §9.9.2 — only show keys actionable in the
// current state.
func (m *FleetModel) activeShortHelp() []key.Binding {
	keys := []key.Binding{m.keys.Up, m.keys.Down, m.keys.Tab}
	if m.focus == paneFocusList && m.selectedHasFixAction() {
		keys = append(keys, m.keys.Enter) // Enter runs the FixAction
	} else if m.focus == paneFocusDetails && len(m.currentReconcilers()) > 0 {
		keys = append(keys, m.keys.Enter) // Enter opens drill-down
	}
	if m.drillDownOpen {
		keys = append(keys, m.keys.Esc)
	}
	if m.canAdminLogin() {
		keys = append(keys, m.keys.LoginAdmin)
	}
	keys = append(keys, m.keys.Refresh, m.keys.Help, m.keys.Quit)
	return keys
}

func (m *FleetModel) activeFullHelp() [][]key.Binding {
	rows := [][]key.Binding{
		{m.keys.Up, m.keys.Down, m.keys.PageUp, m.keys.PageDown, m.keys.Home, m.keys.End},
		{m.keys.Tab, m.keys.ShiftTab, m.keys.Enter, m.keys.Esc},
	}
	actionRow := []key.Binding{}
	if m.canAdminLogin() {
		actionRow = append(actionRow, m.keys.LoginAdmin)
	}
	if len(actionRow) > 0 {
		rows = append(rows, actionRow)
	}
	rows = append(rows, []key.Binding{m.keys.Refresh, m.keys.Help, m.keys.Quit})
	return rows
}

func (m *FleetModel) selectedHasFixAction() bool {
	if len(m.clusters) == 0 {
		return false
	}
	r, ok := m.statuses[m.clusters[m.selected].Name]
	return ok && r.FixAction != nil
}

func (m *FleetModel) canAdminLogin() bool {
	if len(m.clusters) == 0 {
		return false
	}
	c := m.clusters[m.selected]
	return c.Domain != ""
}

func (m *FleetModel) renderList(maxW int) string {
	if m.loading && len(m.clusters) == 0 {
		return bttui.Muted.Render("loading clusters…")
	}
	if len(m.clusters) == 0 {
		return bttui.Muted.Render("no clusters found in fleet repo")
	}

	nameCol := maxNameWidth(m.clusters, 6, 18)
	const statusW = 11

	rowStyle := lipgloss.NewStyle().MaxWidth(maxW)

	var b strings.Builder
	for i, c := range m.clusters {
		status := "…"
		var detail string
		if r, ok := m.statuses[c.Name]; ok {
			status = string(r.Status)
			detail = r.Detail
		}
		// Pending action overlay: show "running…" while a dispatched
		// FixAction is in flight for this row (§9.9.3).
		if m.pendingActionFor == c.Name {
			status = "Running"
			detail = "running login…"
		}

		marker := "  "
		if i == m.selected {
			if m.focus == paneFocusList {
				marker = bttui.KeyLabel.Render("▸ ")
			} else {
				marker = bttui.Muted.Render("▸ ")
			}
		}

		statusCell := bttui.Dot(bttui.StatusColor(status)) + " " +
			bttui.Muted.Render(padRight(status, statusW))

		row := marker +
			bttui.Text.Render(padRight(c.Name, nameCol)) + "  " +
			statusCell + "  " +
			bttui.Muted.Render(c.Domain)
		if detail != "" && status != "Ready" {
			row += "  " + bttui.Muted.Render("· "+detail)
		}
		if c.HasInTreeKubeconfig {
			row += "  " + lipgloss.NewStyle().
				Foreground(colorWarnFG()).
				Render("⚠ kubeconfig-in-repo")
		}
		b.WriteString(rowStyle.Render(row))
		b.WriteByte('\n')
	}
	return b.String()
}

// maxNameWidth returns the longest cluster name length in cs, clamped to
// [min, max].
func maxNameWidth(cs []discover.Cluster, minW, maxW int) int {
	w := minW
	for _, c := range cs {
		if n := lipgloss.Width(c.Name); n > w {
			w = n
		}
	}
	if w > maxW {
		return maxW
	}
	return w
}

// colorWarnFG returns the warning hue without exposing the package's
// internal palette consts.
func colorWarnFG() lipgloss.Color {
	return lipgloss.Color("#FF9830")
}

func (m *FleetModel) refreshDetails() {
	if len(m.clusters) == 0 {
		m.details.SetContent(bttui.Muted.Render("No cluster selected."))
		return
	}
	c := m.clusters[m.selected]
	var b strings.Builder

	b.WriteString(bttui.Title.Render(" " + c.Name + " "))
	if r, ok := m.statuses[c.Name]; ok {
		b.WriteString("  ")
		b.WriteString(bttui.Badge(bttui.StatusColor(string(r.Status)), string(r.Status)))
	}
	b.WriteString("\n\n")

	b.WriteString(bttui.Pill(lipgloss.Color("#5794F2"), "domain", nonEmpty(c.Domain)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#2F9E72"), "api", nonEmpty(c.KubeAPIURL)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#A98BD8"), "ip", nonEmpty(c.NodeExternalIP)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#FF9830"), "ext-net", nonEmpty(c.ExtNetName)))
	b.WriteString("\n\n")

	if r, ok := m.statuses[c.Name]; ok {
		if r.Detail != "" {
			b.WriteString(bttui.Muted.Render("status: "))
			b.WriteString(bttui.Text.Render(r.Detail))
			b.WriteString("\n")
		}
		if r.FixHint != "" {
			b.WriteString(bttui.Muted.Render("hint:   "))
			b.WriteString(bttui.Text.Render(r.FixHint))
			b.WriteString("\n")
		}
		if len(r.Reconcilers) > 0 {
			b.WriteString("\n")
			b.WriteString(bttui.Muted.Render("Kustomizations") + "\n")
			for i, rec := range r.Reconcilers {
				glyph := "✓"
				if !rec.Ready {
					glyph = "✗"
				}
				detail := rec.Reason
				if rec.Message != "" {
					detail = rec.Reason + ": " + rec.Message
				}
				// Cursor marker shows which row Enter would drill into,
				// but only when the details pane has focus (§9.9.1).
				cursor := "  "
				if m.focus == paneFocusDetails && i == m.kustomizationCursor {
					cursor = bttui.KeyLabel.Render("▸ ")
				}
				b.WriteString(cursor)
				b.WriteString(bttui.Text.Render(padRight(glyph+" "+rec.Name, 30)))
				b.WriteString(" ")
				b.WriteString(bttui.Muted.Render(detail))
				b.WriteString("\n")
			}
		}
		if len(r.Drifts) > 0 {
			b.WriteString("\n")
			b.WriteString(bttui.Muted.Render("Image-tag drift (cluster-config.env vs running)") + "\n")
			for _, d := range r.Drifts {
				running := d.Running
				if running == "" {
					running = "missing"
				}
				b.WriteString("  ")
				b.WriteString(lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ "))
				b.WriteString(bttui.Text.Render(padRight(d.Deployment, 22)))
				b.WriteString(bttui.Muted.Render(d.EnvVar + "=" + d.Expected + "  →  running=" + running))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	} else {
		b.WriteString(bttui.Muted.Render("probing…") + "\n\n")
	}

	b.WriteString(bttui.Muted.Render("config: "+c.EnvPath) + "\n")

	if c.HasInTreeKubeconfig {
		b.WriteString("\n")
		b.WriteString(bttui.WarnBox.Render(
			"in-tree kubeconfig detected\nfleet convention is no kubeconfigs in clusters/<name>/\nsee installer-prd §9.7"))
	}

	if c.Env != nil {
		b.WriteString("\n")
		b.WriteString(bttui.Muted.Render(
			fmt.Sprintf("%d keys in cluster-config.env", len(c.Env.Keys()))))
	}

	// Indent every rendered line by 2 chars so the details content
	// left-aligns with the cluster-list rows above (which carry a
	// 2-char marker placeholder). Without this the bottom pane content
	// sits flush with the pane's inner padding while the top pane
	// content sits indented — visually misaligned.
	m.details.SetContent(indentLines(b.String(), "  "))
}

// indentLines prepends prefix to every non-empty line in s. Empty lines
// stay empty so the visual rhythm of section breaks is preserved.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func (m *FleetModel) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
}

// joinSpaced puts left and right at the edges of a line of given width.
func joinSpaced(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func nonEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
