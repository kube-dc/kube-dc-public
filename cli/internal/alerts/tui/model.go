package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/browser"
	"github.com/shalb/kube-dc/cli/internal/alerts"
)

// Filter is the active filter stack applied to the alert list.
type Filter struct {
	Severity  string // "all", "critical", "warning", "info", "none"
	State     string // "all", "active", "suppressed"
	Namespace string
	Source    string // substring match on "job" label
	Search    string // fuzzy-ish substring over all labels/annotations
}

// GroupBy determines how alerts are grouped in the list.
type GroupBy string

const (
	GroupNone      GroupBy = "none"
	GroupAlertname GroupBy = "alertname"
	GroupSeverity  GroupBy = "severity"
	GroupNamespace GroupBy = "namespace"
)

type loadedMsg struct {
	alerts []alerts.Alert
	at     time.Time
}

type errMsg struct {
	err error
}

type tickMsg struct{}

type reconnectMsg struct{}

type reconnectFailedMsg struct {
	err error
}

// Focus enum: which pane receives arrow keys.
type focus int

const (
	focusList focus = iota
	focusDetails
)

// Model is the full-screen TUI.
type Model struct {
	// dependencies
	client  *alerts.AlertmanagerClient
	cluster string
	pf      *alerts.PortForward

	// layout
	width, height int

	// state
	allAlerts    []alerts.Alert
	filtered     []alerts.Alert
	fingerprints map[string]*alerts.Alert
	loading      bool
	refreshing   bool
	reconnecting bool
	err          error
	lastLoadedAt time.Time
	focus        focus
	showHelp     bool

	// filter
	filter  Filter
	tabs    []string
	tabIdx  int
	groupBy GroupBy
	groups  []GroupBy
	groupIx int

	// widgets
	list     list.Model
	details  viewport.Model
	search   textinput.Model
	help     help.Model
	spinner  spinner.Model
	keys     KeyMap

	// modes
	searching bool
}

// NewModel constructs the TUI model. The cluster name is used purely for
// cosmetic decoration in the title bar.
func NewModel(client *alerts.AlertmanagerClient, cluster string, pf *alerts.PortForward) *Model {
	keys := DefaultKeyMap()

	// Search input.
	ti := textinput.New()
	ti.Prompt = "🔍  "
	ti.Placeholder = "search by alertname, label, annotation…"
	ti.CharLimit = 128
	ti.PromptStyle = KeyLabel

	// Spinner for refresh feedback.
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)

	// Help widget.
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	h.Styles.ShortDesc = Muted
	h.Styles.ShortSeparator = Muted
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc

	// Alert list (custom delegate, no built-in title/filter).
	l := list.New(nil, itemDelegate{width: 40}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(true)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false) // we implement our own search
	l.DisableQuitKeybindings()

	return &Model{
		client:  client,
		cluster: cluster,
		pf:      pf,
		keys:    keys,
		tabs:    []string{"all", alerts.SeverityCritical, alerts.SeverityWarning, alerts.SeverityInfo, alerts.SeverityNone},
		tabIdx:  0,
		groups:  []GroupBy{GroupNone, GroupAlertname, GroupSeverity, GroupNamespace},
		groupIx: 0,
		groupBy: GroupNone,
		filter:  Filter{Severity: "all", State: "all"},
		list:    l,
		details: viewport.New(0, 0),
		search:  ti,
		help:    h,
		spinner: sp,
		loading: true,
		focus:   focusList,
	}
}

// SetSeverity pre-selects a severity tab. Accepts "all" or a severity name.
func (m *Model) SetSeverity(sev string) {
	sev = strings.ToLower(sev)
	for i, t := range m.tabs {
		if t == sev {
			m.tabIdx = i
			m.filter.Severity = sev
			return
		}
	}
}

// SetNamespace applies an initial namespace filter.
func (m *Model) SetNamespace(ns string) { m.filter.Namespace = ns }

// SetSource applies an initial source filter.
func (m *Model) SetSource(src string) { m.filter.Source = src }

// Init starts the initial fetch and spinner.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.spinner.Tick, m.tickCmd())
}

// loadCmd returns a tea.Cmd that fetches alerts.
func (m *Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		all, err := m.client.GetAlerts(ctx)
		if err != nil {
			return errMsg{err: err}
		}
		return loadedMsg{alerts: all, at: time.Now()}
	}
}

// tickCmd schedules a periodic refresh (every 30s).
func (m *Model) tickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) reconnectCmd() tea.Cmd {
	return func() tea.Msg {
		// Stop existing port-forward if any
		if m.pf != nil {
			_ = m.pf.Stop()
		}
		// Start fresh port-forward
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.pf.Start(ctx); err != nil {
			return reconnectFailedMsg{err: err}
		}
		// Update client with new URL
		m.client = alerts.NewAlertmanagerClient(m.pf.URL())
		return reconnectMsg{}
	}
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connect: connection refused") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "broken pipe")
}

// Update routes messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()

	case loadedMsg:
		m.loading = false
		m.refreshing = false
		m.err = nil
		m.allAlerts = msg.alerts
		m.lastLoadedAt = msg.at
		m.indexFingerprints()
		m.applyFilter()

	case errMsg:
		m.loading = false
		m.refreshing = false
		m.err = msg.err
		// Auto-reconnect on connection refused errors if we have a port-forward.
		if m.pf != nil && isConnectionError(msg.err) && !m.reconnecting {
			m.reconnecting = true
			cmds = append(cmds, m.reconnectCmd())
		}
		// Keep previously-loaded alerts visible so a transient port-forward
		// hiccup doesn't blank the screen. The error banner surfaces the issue.

	case tickMsg:
		cmds = append(cmds, m.tickCmd())
		if !m.refreshing && !m.loading && !m.reconnecting {
			m.refreshing = true
			cmds = append(cmds, m.loadCmd())
		}
	case reconnectMsg:
		m.reconnecting = false
		m.err = nil
		m.refreshing = true
		cmds = append(cmds, m.loadCmd())
	case reconnectFailedMsg:
		m.reconnecting = false
		m.err = fmt.Errorf("reconnect failed: %w", msg.err)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case tea.KeyMsg:
		// Search-mode takes all keys except Esc/Enter.
		if m.searching {
			return m.updateSearch(msg)
		}
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}
	}

	// Route navigation keys: scroll keys go to the details viewport so the
	// right pane is always scrollable without an explicit focus toggle.
	var listCmd, detailsCmd tea.Cmd
	if km, ok := msg.(tea.KeyMsg); ok && isDetailsScrollKey(km) {
		m.details, detailsCmd = m.details.Update(msg)
	} else {
		m.list, listCmd = m.list.Update(msg)
		// Mouse wheel over the right half should scroll the details pane.
		if mm, ok := msg.(tea.MouseMsg); ok && m.isOverDetails(mm) {
			m.details, detailsCmd = m.details.Update(msg)
		}
	}
	cmds = append(cmds, listCmd, detailsCmd)

	// Refresh details pane if selection changed.
	m.refreshDetails()

	return m, tea.Batch(cmds...)
}

// handleKey handles top-level key presses. Returns (cmd, true) if handled.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
		m.help.ShowAll = m.showHelp
		m.relayout()
		return nil, true
	case key.Matches(msg, m.keys.Refresh):
		if !m.refreshing {
			m.refreshing = true
			return m.loadCmd(), true
		}
		return nil, true
	case key.Matches(msg, m.keys.Reconnect):
		if m.pf != nil && !m.reconnecting {
			m.reconnecting = true
			return m.reconnectCmd(), true
		}
		return nil, true
	case key.Matches(msg, m.keys.Search):
		m.searching = true
		m.search.SetValue(m.filter.Search)
		m.search.Focus()
		return textinput.Blink, true
	case key.Matches(msg, m.keys.Tab):
		m.tabIdx = (m.tabIdx + 1) % len(m.tabs)
		m.filter.Severity = m.tabs[m.tabIdx]
		m.applyFilter()
		return nil, true
	case key.Matches(msg, m.keys.ShiftTab):
		m.tabIdx = (m.tabIdx - 1 + len(m.tabs)) % len(m.tabs)
		m.filter.Severity = m.tabs[m.tabIdx]
		m.applyFilter()
		return nil, true
	case key.Matches(msg, m.keys.Group):
		m.groupIx = (m.groupIx + 1) % len(m.groups)
		m.groupBy = m.groups[m.groupIx]
		m.applyFilter()
		return nil, true
	case key.Matches(msg, m.keys.Focus):
		if m.focus == focusList {
			m.focus = focusDetails
		} else {
			m.focus = focusList
		}
		return nil, true
	case key.Matches(msg, m.keys.OpenURL):
		if a := m.selectedAlert(); a != nil {
			if url := bestURL(a); url != "" {
				_ = browser.OpenURL(url)
			}
		}
		return nil, true
	case key.Matches(msg, m.keys.Esc):
		// Cycle back from details pane.
		if m.focus == focusDetails {
			m.focus = focusList
			return nil, true
		}
	}
	return nil, false
}

// updateSearch is active while the search input is focused.
func (m *Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.searching = false
		m.search.Blur()
		m.filter.Search = strings.TrimSpace(m.search.Value())
		m.applyFilter()
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.filter.Search = strings.TrimSpace(m.search.Value())
	m.applyFilter()
	return m, cmd
}

// indexFingerprints builds a fingerprint → alert lookup for detail lookups.
func (m *Model) indexFingerprints() {
	m.fingerprints = make(map[string]*alerts.Alert, len(m.allAlerts))
	for i := range m.allAlerts {
		m.fingerprints[m.allAlerts[i].Fingerprint] = &m.allAlerts[i]
	}
}

// applyFilter computes filtered + re-populates the list.
func (m *Model) applyFilter() {
	filtered := alerts.ApplyFilter(m.allAlerts, alerts.FilterSpec{
		Severity:  m.filter.Severity,
		State:     m.filter.State,
		Namespace: m.filter.Namespace,
		Source:    m.filter.Source,
		Search:    m.filter.Search,
	})
	alerts.SortAlerts(filtered)
	m.filtered = filtered

	items := make([]list.Item, 0, len(filtered))
	switch m.groupBy {
	case GroupNone:
		for i := range filtered {
			items = append(items, alertItem{a: &filtered[i]})
		}
	default:
		items = m.groupedItems(filtered)
	}
	m.list.SetItems(items)
	m.refreshDetails()
}

// groupedItems inserts header-less grouping: we sort by the group key and
// rely on the delegate's dot+pills to convey grouping visually. Real
// section headers would require a custom list implementation; keep it simple.
func (m *Model) groupedItems(filtered []alerts.Alert) []list.Item {
	keyFn := func(a *alerts.Alert) string {
		switch m.groupBy {
		case GroupAlertname:
			return a.AlertName
		case GroupSeverity:
			return fmt.Sprintf("%d-%s", 10-alerts.SeverityPriority(a.Severity), a.Severity)
		case GroupNamespace:
			return a.Labels["namespace"]
		}
		return ""
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		ki, kj := keyFn(&filtered[i]), keyFn(&filtered[j])
		if ki != kj {
			return ki < kj
		}
		return alerts.SeverityPriority(filtered[i].Severity) > alerts.SeverityPriority(filtered[j].Severity)
	})
	items := make([]list.Item, 0, len(filtered))
	for i := range filtered {
		items = append(items, alertItem{a: &filtered[i]})
	}
	return items
}

// selectedAlert returns the currently highlighted alert or nil.
func (m *Model) selectedAlert() *alerts.Alert {
	sel := m.list.SelectedItem()
	if it, ok := sel.(alertItem); ok {
		return it.a
	}
	return nil
}

// relayout re-applies sizes to the list / details / search widgets based
// on the current window dimensions.
func (m *Model) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	w, h := m.width-2, m.height-2 // AppStyle padding

	// Header rows: title (1) + stat bar (1) + tabs+filters (1) + blank (1) = 4
	// Footer rows: help (1 or N) + search (1 if active) + error (2 if err)
	headerH := 4
	footerH := 1
	if m.showHelp {
		footerH += 3
	}
	if m.err != nil {
		footerH += 2
	}
	if m.searching {
		footerH++
	}

	bodyH := h - headerH - footerH
	if bodyH < 5 {
		bodyH = 5
	}
	listW := w * 45 / 100
	if listW < 40 {
		listW = 40
	}
	detailsW := w - listW - 1

	m.list.SetSize(listW-2, bodyH-2) // -2 for border
	m.list.SetDelegate(itemDelegate{width: listW - 4})
	m.details.Width = detailsW - 2
	m.details.Height = bodyH - 2
	m.search.Width = w - 4
	m.help.Width = w
}

// refreshDetails renders the selected alert into the details viewport.
func (m *Model) refreshDetails() {
	a := m.selectedAlert()
	if a == nil {
		m.details.SetContent(Muted.Render("No alert selected."))
		return
	}
	var b strings.Builder

	// Big badge
	sevColor := alerts.SeverityColor(a.Severity)
	b.WriteString(Badge(sevColor, strings.ToUpper(a.Severity)))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(a.AlertName))
	b.WriteString("\n")
	b.WriteString(Muted.Render(fmt.Sprintf("state=%s · fingerprint=%s", a.State, shortFP(a.Fingerprint))))
	b.WriteString("\n\n")

	// Annotation: summary / description get top billing.
	for _, k := range []string{"summary", "description", "message"} {
		if v, ok := a.Annotations[k]; ok && v != "" {
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(strings.ToUpper(k[:1]) + k[1:]))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(colorText).Render(wrap(v, m.details.Width)))
			b.WriteString("\n\n")
		}
	}

	// Labels as pills.
	b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Labels"))
	b.WriteString("\n")
	b.WriteString(renderPills(a.Labels, m.details.Width))
	b.WriteString("\n\n")

	// Other annotations (excluding the ones we already printed).
	other := make(map[string]string, len(a.Annotations))
	for k, v := range a.Annotations {
		switch k {
		case "summary", "description", "message":
			continue
		}
		other[k] = v
	}
	if len(other) > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Annotations"))
		b.WriteString("\n")
		for _, k := range alerts.SortedKeys(other) {
			b.WriteString(KeyLabel.Render(k))
			b.WriteString("  ")
			b.WriteString(Text.Render(wrap(other[k], m.details.Width-lipgloss.Width(k)-2)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Timing.
	b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Timing"))
	b.WriteString("\n")
	b.WriteString(KeyLabel.Render("started"))
	b.WriteString("  ")
	b.WriteString(Text.Render(a.StartsAt.Local().Format("2006-01-02 15:04:05")))
	b.WriteString("  ")
	b.WriteString(Muted.Render("(" + a.Age() + " ago)"))
	b.WriteString("\n")
	if !a.UpdatedAt.IsZero() {
		b.WriteString(KeyLabel.Render("updated"))
		b.WriteString("  ")
		b.WriteString(Text.Render(a.UpdatedAt.Local().Format("2006-01-02 15:04:05")))
		b.WriteString("\n")
	}
	if url := bestURL(a); url != "" {
		b.WriteString("\n")
		b.WriteString(KeyLabel.Render("runbook"))
		b.WriteString("  ")
		b.WriteString(Text.Render(url))
		b.WriteString("  ")
		b.WriteString(Muted.Render("(press o to open)"))
	}

	m.details.SetContent(b.String())
}

// renderPills renders labels as wrapping colored pills sized to `width`.
func renderPills(labels map[string]string, width int) string {
	if len(labels) == 0 {
		return Muted.Render("—")
	}
	var rows []string
	var current string
	currentW := 0
	for _, k := range alerts.SortedKeys(labels) {
		p := Pill(alerts.ColorForKey(k), k, labels[k])
		pw := lipgloss.Width(p)
		if currentW+pw+1 > width && current != "" {
			rows = append(rows, current)
			current, currentW = p, pw
			continue
		}
		if current == "" {
			current, currentW = p, pw
			continue
		}
		current += " " + p
		currentW += pw + 1
	}
	if current != "" {
		rows = append(rows, current)
	}
	return strings.Join(rows, "\n")
}

// View renders the full screen.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}

	// Header
	cluster := m.cluster
	if cluster == "" {
		cluster = "alertmanager"
	}
	title := Title.Render(" 🔔 Alerts ") + " " + Muted.Render(cluster)
	var rightMeta string
	if m.reconnecting {
		rightMeta = m.spinner.View() + " reconnecting…"
	} else if m.refreshing {
		rightMeta = m.spinner.View() + " refreshing"
	} else if !m.lastLoadedAt.IsZero() {
		rightMeta = Muted.Render(fmt.Sprintf("updated %ds ago", int(time.Since(m.lastLoadedAt).Seconds())))
	}
	titleRow := joinSpaced(m.width-2, title, rightMeta)

	statRow := m.renderStatBar()
	tabsRow := m.renderTabsAndFilters()

	// Body
	var body string
	if m.loading {
		body = lipgloss.Place(m.width-2, m.height-6, lipgloss.Center, lipgloss.Center,
			m.spinner.View()+" loading alerts…")
	} else {
		listPane := ListPane
		detailsPane := DetailsPane
		if m.focus == focusList {
			listPane = ListPaneFocused
		} else {
			detailsPane = DetailsPaneFocused
		}
		left := listPane.Render(m.list.View())
		right := detailsPane.Render(m.details.View())
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}

	// Footer
	var footer []string
	if m.searching {
		footer = append(footer, SearchBox.Width(m.width-2).Render(m.search.View()))
	}
	if m.err != nil {
		footer = append(footer, ErrorBox.Width(m.width-2).Render("error: "+m.err.Error()))
	}
	if m.showHelp {
		footer = append(footer, HelpBar.Render(m.help.FullHelpView(m.keys.FullHelp())))
	} else {
		footer = append(footer, HelpBar.Render(m.help.ShortHelpView(m.keys.ShortHelp())))
	}

	parts := []string{titleRow, statRow, tabsRow, body}
	parts = append(parts, footer...)
	return AppStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// renderStatBar renders the "3 critical · 15 warning · …" counter.
func (m *Model) renderStatBar() string {
	counts := map[string]int{}
	active, suppressed := 0, 0
	for i := range m.allAlerts {
		a := &m.allAlerts[i]
		counts[a.Severity]++
		switch a.State {
		case alerts.StateSuppressed:
			suppressed++
		default:
			active++
		}
	}
	segs := []string{
		Dot(alerts.SeverityColor(alerts.SeverityCritical)) + " " + Text.Render(fmt.Sprintf("%d critical", counts[alerts.SeverityCritical])),
		Dot(alerts.SeverityColor(alerts.SeverityWarning)) + " " + Text.Render(fmt.Sprintf("%d warning", counts[alerts.SeverityWarning])),
		Dot(alerts.SeverityColor(alerts.SeverityInfo)) + " " + Text.Render(fmt.Sprintf("%d info", counts[alerts.SeverityInfo])),
		Dot(alerts.SeverityColor(alerts.SeverityNone)) + " " + Text.Render(fmt.Sprintf("%d none", counts[alerts.SeverityNone])),
		Muted.Render(fmt.Sprintf("· %d active · %d silenced", active, suppressed)),
	}
	return StatBar.Render(strings.Join(segs, "  "))
}

// renderTabsAndFilters renders the severity tab strip + active-filter
// summary (group/search/etc).
func (m *Model) renderTabsAndFilters() string {
	var tabs []string
	for i, t := range m.tabs {
		label := strings.ToUpper(t)
		if i == m.tabIdx {
			tabs = append(tabs, TabActive.Render(label))
		} else {
			tabs = append(tabs, TabInactive.Render(label))
		}
	}
	tabStrip := strings.Join(tabs, "")

	var meta []string
	meta = append(meta, Muted.Render("group:")+" "+Text.Render(string(m.groupBy)))
	if m.filter.Search != "" {
		meta = append(meta, Muted.Render("search:")+" "+Text.Render(m.filter.Search))
	}
	meta = append(meta, Muted.Render(fmt.Sprintf("(%d shown / %d total)", len(m.filtered), len(m.allAlerts))))

	return joinSpaced(m.width-2, tabStrip, strings.Join(meta, "  "))
}

// joinSpaced puts `left` and `right` at the edges of a line of given width.
func joinSpaced(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// wrap word-wraps `s` to `w` runes per line.
func wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		var col int
		for _, word := range strings.Fields(line) {
			ww := lipgloss.Width(word)
			if col > 0 && col+ww+1 > w {
				out.WriteByte('\n')
				col = 0
			}
			if col > 0 {
				out.WriteByte(' ')
				col++
			}
			out.WriteString(word)
			col += ww
		}
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// shortFP shortens a fingerprint for display.
func shortFP(fp string) string {
	if len(fp) > 10 {
		return fp[:10]
	}
	return fp
}

// isDetailsScrollKey returns true for keys that should scroll the right
// pane regardless of focus: PgUp, PgDn, and the vim-style shift-j / shift-k.
func isDetailsScrollKey(km tea.KeyMsg) bool {
	switch km.String() {
	case "pgup", "pgdown", "J", "K", "ctrl+d", "ctrl+u":
		return true
	}
	return false
}

// isOverDetails returns true if a mouse event x-coordinate falls within the
// details pane. Used to route mouse-wheel scroll to the right viewport.
func (m *Model) isOverDetails(mm tea.MouseMsg) bool {
	// Rough heuristic: the list pane occupies the first 45% of width.
	cutoff := (m.width - 2) * 45 / 100
	return mm.X > cutoff
}

// bestURL picks the most useful URL from an alert (runbook > generator).
func bestURL(a *alerts.Alert) string {
	if v := a.Annotations["runbook_url"]; v != "" {
		return v
	}
	if a.GeneratorURL != "" {
		return a.GeneratorURL
	}
	return ""
}
