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

// FleetModel is the multi-cluster landing screen — the home of the
// `kube-dc bootstrap` TUI when invoked without arguments.
//
// Layout: left list 45% (cluster names + status pills), right pane 55%
// (selected-cluster details). Mirrors cli/internal/alerts/tui's two-pane
// shape so the two TUIs feel like siblings.
type FleetModel struct {
	repoRoot string

	width, height int

	clusters     []discover.Cluster
	selected     int
	loading      bool
	err          error
	lastLoadedAt time.Time

	// Per-cluster probe results, keyed by Cluster.Name. Slow clusters
	// don't block fast ones — each ClusterProbeMsg arrives independently.
	statuses map[string]discover.ProbeResult

	details viewport.Model
	help    help.Model
	keys    bttui.KeyMap
}

// NewFleetModel constructs the fleet landing model. repoRoot is the
// absolute path to the kube-dc-fleet repo on disk; clones/git-pulls land
// in a future iteration (see installer-prd §9.6).
func NewFleetModel(repoRoot string) *FleetModel {
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Bold(true)
	h.Styles.ShortDesc = bttui.Muted
	h.Styles.ShortSeparator = bttui.Muted
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc

	return &FleetModel{
		repoRoot: repoRoot,
		statuses: map[string]discover.ProbeResult{},
		details:  viewport.New(0, 0),
		help:     h,
		keys:     bttui.DefaultKeyMap(),
		loading:  true,
	}
}

// Init kicks off the first fleet enumeration and starts the 60s refresh
// tick. The tick fires regardless of foreground activity so the status
// pills stay roughly fresh while the operator is reading details.
func (m *FleetModel) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.tickCmd())
}

func (m *FleetModel) loadCmd() tea.Cmd {
	return func() tea.Msg {
		// Currently a synchronous local read; cheap enough that we don't
		// need a context here. When git pull lands this gains a timeout.
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
		// Kick off one probe per cluster, all in parallel. tea.Batch
		// runs each command in its own goroutine; Bubble Tea collects
		// the messages and feeds them back to Update independently.
		return m, tea.Batch(m.probeAllCmds()...)
	case bttui.FleetErrorMsg:
		m.loading = false
		m.err = msg.Err
	case bttui.ClusterProbeMsg:
		// Slow probes can land after the user has already refreshed —
		// only update if the cluster is still in the current list.
		for _, c := range m.clusters {
			if c.Name == msg.Name {
				m.statuses[msg.Name] = msg.Result
				break
			}
		}
		m.refreshDetails()
	case bttui.TickMsg:
		// Re-probe everything in the background and arm the next tick.
		// We deliberately don't re-enumerate the fleet here — the env
		// files only change when the operator runs `git pull` or saves
		// from the day-2 editor; both will trigger an explicit reload.
		cmds := append(m.probeAllCmds(), m.tickCmd())
		return m, tea.Batch(cmds...)
	case bttui.LoginDoneMsg:
		// Surface the subprocess outcome and re-probe just the affected
		// row so the operator sees the status pill update without
		// pressing `r`. Errors from non-zero exit propagate via m.err
		// so they land in the red footer banner.
		if msg.Err != nil {
			m.err = fmt.Errorf("login %s failed: %w", msg.Cluster, msg.Err)
		} else {
			m.err = nil
		}
		return m, m.reprobeOne(msg.Cluster)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// probeAllCmds returns one tea.Cmd per cluster. Bubble Tea runs them
// concurrently via tea.Batch, and each command sends its own
// ClusterProbeMsg back when it completes. A 5s probe timeout per cluster
// keeps the fleet view responsive even when one cluster is unreachable.
func (m *FleetModel) probeAllCmds() []tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.clusters))
	for _, c := range m.clusters {
		c := c // capture
		if c.KubeAPIURL == "" {
			// No API URL → no probe possible. Synthesise an Unknown
			// result so the row still updates from "loading".
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
			// Tell the probe which Deployments to compare against
			// cluster-config.env's *_TAG vars for drift detection.
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

// tickCmd schedules the next 60s background refresh. Mirrors the alerts
// TUI's tick pattern (cli/internal/alerts/tui/model.go:200-203) so
// status pills follow cluster reality without the operator pressing `r`.
func (m *FleetModel) tickCmd() tea.Cmd {
	return tea.Tick(60*time.Second, func(time.Time) tea.Msg { return bttui.TickMsg{} })
}

// execLoginCmd suspends the TUI and exec's `kube-dc login` for the
// currently-selected cluster. tea.ExecProcess hands the terminal over
// to the subprocess (browser opens, operator authenticates, output is
// printed normally), then restores the TUI. On return we re-probe just
// the affected row so the status pill updates from Unreachable to
// whatever's now true.
//
// admin=true → `--admin`; admin=false → `--org <prompted>`. For now
// the org-login path falls back to a notice that asks the operator to
// run the command outside the TUI, since we don't yet have an inline
// huh.Input for the org name.
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
		// Tenant org-login needs an org name. Until we add an inline
		// prompt, surface the exact command instead of half-running it.
		m.err = fmt.Errorf("tenant login from the TUI is not yet implemented — run `kube-dc login --domain %s --org <your-org>` directly", c.Domain)
		return nil
	}

	args := []string{"login", "--domain", c.Domain, "--admin"}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return bttui.LoginDoneMsg{Cluster: c.Name, Admin: true, Err: err}
	})
}

// reprobeOne runs the per-cluster probe for exactly one cluster (used
// after a login completes — no need to re-poll the others). Returns
// nil when the cluster vanished from the list between command issue
// and message arrival.
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
	case key.Matches(msg, m.keys.LoginAdmin):
		// Suspend the TUI, exec `kube-dc login --domain X --admin`, resume
		// after the operator finishes the browser flow. We stream output
		// straight to the operator's terminal so they see the same auth
		// prompts they'd see from a normal `kube-dc login`.
		if cmd := m.execLoginCmd(true); cmd != nil {
			return m, cmd
		}
	case key.Matches(msg, m.keys.LoginOrg):
		if cmd := m.execLoginCmd(false); cmd != nil {
			return m, cmd
		}
	case key.Matches(msg, m.keys.Up):
		if m.selected > 0 {
			m.selected--
			m.refreshDetails()
		}
	case key.Matches(msg, m.keys.Down):
		if m.selected < len(m.clusters)-1 {
			m.selected++
			m.refreshDetails()
		}
	}
	return m, nil
}

// View renders the screen.
func (m *FleetModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	w, h := m.width-2, m.height-2 // AppStyle padding

	// Header: title + repo + last-updated.
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

	// Body: horizontal split — top list, bottom details. Both panes
	// span the full terminal width so long values (config paths, image
	// tags, condition messages) don't get truncated.
	bodyH := h - 4 // header + footer rows
	if bodyH < 8 {
		bodyH = 8
	}

	// List pane: enough rows for the cluster list plus 2 chrome rows.
	// Caps at ~half the body so the details pane stays useful even
	// when the fleet grows.
	listH := len(m.clusters) + 2
	if listH < 5 {
		listH = 5
	}
	if listH > bodyH/2 {
		listH = bodyH / 2
	}
	detailsH := bodyH - listH - 1

	top := bttui.ListPaneFocused.
		Width(w - 2).
		Height(listH - 2).
		Render(m.renderList(w - 6)) // -2 border, -2 padding, -2 slack

	m.details.Width = w - 4 // -2 border, -2 padding
	m.details.Height = detailsH - 2
	bottom := bttui.DetailsPane.
		Width(w - 2).
		Height(detailsH - 2).
		Render(m.details.View())

	body := lipgloss.JoinVertical(lipgloss.Left, top, bottom)

	// Footer: error/help.
	var footerLines []string
	if m.err != nil {
		footerLines = append(footerLines,
			bttui.ErrorBox.Width(w).Render("error: "+m.err.Error()))
	}
	if m.help.ShowAll {
		footerLines = append(footerLines, bttui.HelpBar.Render(m.help.FullHelpView(m.keys.FullHelp())))
	} else {
		footerLines = append(footerLines, bttui.HelpBar.Render(m.help.ShortHelpView(m.keys.ShortHelp())))
	}

	parts := append([]string{titleRow, body}, footerLines...)
	return bttui.AppStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func (m *FleetModel) renderList(maxW int) string {
	if m.loading && len(m.clusters) == 0 {
		return bttui.Muted.Render("loading clusters…")
	}
	if len(m.clusters) == 0 {
		return bttui.Muted.Render("no clusters found in fleet repo")
	}

	// Compact column layout — fixed widths so rows look like a table
	// instead of leaving a huge gap when cluster names are short.
	//
	//   ▸ ● <name=12> <status=11> <domain=…> <tags=…>
	//
	// The name column auto-grows to the longest cluster name in the
	// list (capped at 18) so cs/zrh + cloud + stage line up cleanly
	// without 24 chars of trailing whitespace.
	nameCol := maxNameWidth(m.clusters, 6, 18)
	const statusW = 11

	rowStyle := lipgloss.NewStyle().MaxWidth(maxW)

	var b strings.Builder
	for i, c := range m.clusters {
		status := "…" // probe in flight
		var detail string
		if r, ok := m.statuses[c.Name]; ok {
			status = string(r.Status)
			detail = r.Detail
		}

		marker := "  "
		if i == m.selected {
			marker = bttui.KeyLabel.Render("▸ ")
		}

		// Status column: dot + label, fixed width so the trailing
		// columns line up across rows.
		statusCell := bttui.Dot(bttui.StatusColor(status)) + " " +
			bttui.Muted.Render(padRight(status, statusW))

		row := marker +
			bttui.Text.Render(padRight(c.Name, nameCol)) + "  " +
			statusCell + "  " +
			bttui.Muted.Render(c.Domain)
		if detail != "" && status != "Ready" {
			// Condense the probe detail into the row when it adds info
			// (e.g. "auth failed (403)") — Ready rows are self-evident.
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
// [min, max]. Used to size the name column so short fleets render
// compactly while long names still get padded for alignment.
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
// internal palette consts — keeps rendering ANSI-clean even when the row
// is right at maxW.
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

	// Header: cluster name + status badge.
	b.WriteString(bttui.Title.Render(" " + c.Name + " "))
	if r, ok := m.statuses[c.Name]; ok {
		b.WriteString("  ")
		b.WriteString(bttui.Badge(bttui.StatusColor(string(r.Status)), string(r.Status)))
	}
	b.WriteString("\n\n")

	// Connection metadata pills.
	b.WriteString(bttui.Pill(lipgloss.Color("#5794F2"), "domain", nonEmpty(c.Domain)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#2F9E72"), "api", nonEmpty(c.KubeAPIURL)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#A98BD8"), "ip", nonEmpty(c.NodeExternalIP)))
	b.WriteString("\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#FF9830"), "ext-net", nonEmpty(c.ExtNetName)))
	b.WriteString("\n\n")

	// Live probe results.
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
			for _, rec := range r.Reconcilers {
				glyph := "✓"
				if !rec.Ready {
					glyph = "✗"
				}
				detail := rec.Reason
				if rec.Message != "" {
					detail = rec.Reason + ": " + rec.Message
				}
				b.WriteString("  ")
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

	m.details.SetContent(b.String())
	m.details.GotoTop()
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
