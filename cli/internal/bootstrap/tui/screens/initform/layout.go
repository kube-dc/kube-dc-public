package initform

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}
func fit(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}
func wrapPlain(s string, width int) []string {
	if width < 1 {
		return nil
	}
	return strings.Split(ansi.Hardwrap(ansi.Wordwrap(safeText(s), width, ""), width, true), "\n")
}
func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type screenTheme struct{ text, muted, accent, good, warn, bad, selected, pane, focused lipgloss.Style }

func (m *PanelModel) theme() screenTheme {
	t := screenTheme{
		text: bttui.Text, muted: bttui.Muted, accent: bttui.KeyLabel,
		good:     lipgloss.NewStyle().Foreground(lipgloss.Color("#5ACD98")),
		warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD173")),
		bad:      lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8A96")),
		selected: lipgloss.NewStyle().Background(lipgloss.Color("#303346")).Foreground(lipgloss.Color("#FFFFFF")),
		pane:     bttui.DetailsPane, focused: bttui.DetailsPaneFocused,
	}
	if m.light {
		t.text = lipgloss.NewStyle().Foreground(lipgloss.Color("#202733"))
		t.muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#526071"))
		t.accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#215EA8")).Bold(true)
		t.good = lipgloss.NewStyle().Foreground(lipgloss.Color("#146B49"))
		t.warn = lipgloss.NewStyle().Foreground(lipgloss.Color("#825100"))
		t.bad = lipgloss.NewStyle().Foreground(lipgloss.Color("#B42333"))
		t.selected = lipgloss.NewStyle().Background(lipgloss.Color("#E8E2F4")).Foreground(lipgloss.Color("#202733"))
		t.pane = bttui.DetailsPane.BorderForeground(lipgloss.Color("#7A8290"))
		t.focused = bttui.DetailsPaneFocused.BorderForeground(lipgloss.Color("#7450A4"))
	}
	return t
}

func (m *PanelModel) tooSmall() bool { return m.width < 80 || m.height < 17 }
func (m *PanelModel) primaryLabel() string {
	if m.editing {
		return "Finish field edit"
	}
	if m.Busy() {
		if m.workflow.services.Session != nil {
			return "Checking session ownership…"
		}
		return "Reading host facts…"
	}
	if m.workflow != nil {
		switch m.currentSection() {
		case "Hosts":
			return "Discover host resources"
		case "Storage":
			return "Inspect all storage hosts"
		case "Network":
			return "Inspect all network hosts"
		case "Readiness":
			return "Review current settings"
		case "Review":
			if m.workflow.services.Demo != nil {
				return "Run simulation"
			}
			return "Continue to install review"
		case "Install":
			return "Inspect verification"
		case "Verify":
			return "Show result"
		case "Result":
			return "Continue"
		case "Continue":
			if m.workflow.services.Session != nil {
				return "Inspect session and targets"
			}
			return "Check again"
		default:
			return "Check readiness"
		}
	}
	return "Review settings"
}
func (m *PanelModel) primaryAction() tea.Cmd {
	if m.editing || m.Busy() {
		return nil
	}
	if m.workflow == nil {
		m.openSection("Review")
		return nil
	}
	switch m.currentSection() {
	case "Hosts":
		return m.startDiscovery()
	case "Storage", "Network":
		return m.startHostDiscovery()
	case "Readiness":
		return m.activateWorkflow("review")
	case "Continue":
		if m.workflow.services.Session != nil {
			return m.startSession("session-inspect")
		}
		return m.startReadiness()
	case "Review":
		if m.workflow.services.Demo != nil {
			return m.activateWorkflow("run")
		}
		m.focus = focusFields
		m.fieldCursor = 0
		_, cmd := m.activate()
		return cmd
	case "Install":
		return m.activateWorkflow("verify")
	case "Verify":
		return m.activateWorkflow("result")
	case "Result":
		return m.activateWorkflow("continue")
	default:
		return m.startReadiness()
	}
}
func (m *PanelModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return m.frame("Initializing…")
	}
	if m.tooSmall() {
		return m.frame(fit("Resize terminal to at least 80 × 18. Input is preserved. Ctrl+C quits.", max(1, m.width-2)))
	}
	t := m.theme()
	w := m.width - 2
	status := "Settings ready for review"
	if n := len(m.validationErrors()); n > 0 {
		status = fmt.Sprintf("%d settings need attention", n)
	}
	if m.workflow != nil && m.workflow.services.Demo != nil {
		status = "SIMULATED · No cluster changes"
	}
	title := spaced(bttui.Title.Render(" Kube-DC — New Cluster "), t.muted.Render(status), w)
	title += "\n" + t.muted.Render(fit(safeText(nonEmpty(m.st.Name, "New cluster")+" / "+m.st.Mode+" · "+m.st.Preset), w))
	m.help.SetWidth(w)
	m.help.Styles.ShortKey = t.accent
	m.help.Styles.ShortDesc = t.muted
	m.help.Styles.ShortSeparator = t.muted
	m.help.Styles.FullKey = t.accent
	m.help.Styles.FullDesc = t.muted
	m.help.Styles.FullSeparator = t.muted
	footer := m.help.ShortHelpView(m.keys.ShortHelp())
	if m.showHelp {
		footer = m.help.FullHelpView(m.keys.FullHelp())
	}
	// Help must not displace the selected field or actions on short terminals.
	if lipgloss.Height(footer) > 3 {
		footer = m.help.ShortHelpView(m.keys.ShortHelp()) + "\n" + t.muted.Render("PgUp/PgDn scroll · n primary action · r recheck · Ctrl+C quit")
	}
	primary := bttui.Title.Render("[ n " + m.primaryLabel() + " ]")
	blocker := m.primaryBlocker()
	if blocker != "" {
		primary = t.muted.Render("[ n " + m.primaryLabel() + " (blocked) ]")
	}
	actions := primary + "  " + t.text.Render("[ S Save draft ]")
	status = "Tab switches pane · PgUp/PgDn scroll · Enter edits or activates."
	if m.editing {
		status = "Enter keeps the value · Esc cancels the edit · Ctrl+C quits."
	}
	if m.notice != "" {
		status = m.notice
	}
	if blocker != "" {
		status = blocker
	}
	if m.editError != "" {
		status = m.editError
	}
	actions = fit(actions, w) + "\n" + t.muted.Render(fit(safeText(status), w))
	bodyH := m.height - lipgloss.Height(title) - lipgloss.Height(footer) - lipgloss.Height(actions)
	rail := m.width >= 100
	mainW := w
	if rail {
		mainW -= 23
	}
	innerW := mainW - 4
	vpH := max(1, bodyH-4)
	m.fieldsVP.SetWidth(innerW)
	m.fieldsVP.SetHeight(vpH)
	m.input.SetWidth(max(10, innerW-4))
	content, cursor := m.renderFieldsBody(innerW)
	m.fieldsVP.SetContent(content)
	if m.focus == focusFields && !m.manualScroll {
		ensureLineVisible(&m.fieldsVP, cursor)
	}
	heading := m.currentSection()
	if !rail {
		heading = "← " + heading + " →  (Tab: sections)"
	}
	counter := ""
	if m.fieldsVP.TotalLineCount() > vpH {
		counter = fmt.Sprintf("%3.0f%% · PgUp/PgDn", m.fieldsVP.ScrollPercent()*100)
	}
	style := t.pane
	if m.focus == focusFields {
		style = t.focused
	}
	main := style.Width(mainW).Height(bodyH).Render(spaced(t.accent.Render(heading), t.muted.Render(counter), innerW) + "\n\n" + m.fieldsVP.View())
	body := main
	if rail {
		style = t.pane
		if m.focus == focusSections {
			style = t.focused
		}
		lines := strings.Split(strings.TrimSuffix(m.renderSections(), "\n"), "\n")
		capacity := bodyH - 2
		start := max(0, m.secCursor-capacity+1)
		end := min(len(lines), start+capacity)
		left := style.Width(22).Height(bodyH).Render(strings.Join(lines[start:end], "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", main)
	}
	return m.frame(lipgloss.JoinVertical(lipgloss.Left, title, body, actions, footer))
}
func spaced(left, right string, width int) string {
	if right == "" {
		return fit(left, width)
	}
	right = fit(right, width/2)
	left = fit(left, max(0, width-ansi.StringWidth(right)-1))
	return left + strings.Repeat(" ", max(1, width-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}
func (m *PanelModel) frame(content string) tea.View {
	content = bttui.AppStyle.Render(content)
	if os.Getenv("NO_COLOR") != "" {
		content = ansi.Strip(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
