// Package tui contains the Bubble Tea program for `kube-dc bootstrap`.
//
// The styles below mirror cli/internal/alerts/tui/styles.go on purpose —
// the two TUIs ship as visual siblings (see installer-prd §9.1). When the
// alerts palette changes, change it here too. A future refactor can lift
// the shared palette into a common package; for now, duplication keeps
// the bootstrap package from depending on alerts/tui internals.
package tui

import "github.com/charmbracelet/lipgloss"

// Palette — must stay in sync with cli/internal/alerts/tui/styles.go.
var (
	colorBg        = lipgloss.Color("#181B1F")
	colorSurface   = lipgloss.Color("#22252B")
	// colorBorder is the idle pane border. Earlier value (#2E3137) was
	// only ~5% off the bg and rendered as visually-invisible on most
	// terminals — the unfocused pane looked like it had no border at
	// all next to the focused one (#7E5CAD bright purple). Bumped so
	// idle panes are clearly bounded but still distinct from focused.
	colorBorder    = lipgloss.Color("#4A5060")
	colorBorderHi  = lipgloss.Color("#7E5CAD")
	colorText      = lipgloss.Color("#E6E9EF")
	colorMuted     = lipgloss.Color("#8B92A5")
	colorAccent    = lipgloss.Color("#5794F2")
	colorAccentAlt = lipgloss.Color("#2F9E72")
	colorWhite     = lipgloss.Color("#FFFFFF")

	// Cluster status hues — extends the alerts palette.
	colorReady       = colorAccentAlt              // green
	colorReconciling = colorAccent                 // blue
	colorDrifted     = lipgloss.Color("#FF9830")   // orange
	colorFailed      = lipgloss.Color("#F2495C")   // red
	colorUnreachable = colorMuted                  // grey
	colorUnknown     = lipgloss.Color("#A98BD8")   // light purple
)

// Suppress "declared but not used" while bootstrap screens are still being
// wired up; remove these once each color has a consumer.
var _ = colorBg
var _ = colorSurface

// Layout
var (
	AppStyle = lipgloss.NewStyle().Padding(0, 1)

	Title = lipgloss.NewStyle().
		Foreground(colorWhite).
		Background(colorBorderHi).
		Bold(true).
		Padding(0, 1)

	Subtitle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	StatBar = lipgloss.NewStyle().
		Foreground(colorText).
		Padding(0, 1)

	TabActive = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorAccent).
			Bold(true).
			Padding(0, 2)

	TabInactive = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 2)

	Divider = lipgloss.NewStyle().
		Foreground(colorBorder)

	ListPane = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	ListPaneFocused = ListPane.
			BorderForeground(colorBorderHi)

	DetailsPane = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	DetailsPaneFocused = DetailsPane.
				BorderForeground(colorBorderHi)

	HelpBar = lipgloss.NewStyle().
		Foreground(colorMuted).
		Padding(0, 1)

	ErrorBox = lipgloss.NewStyle().
			Foreground(colorFailed).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorFailed).
			Padding(0, 1)

	WarnBox = lipgloss.NewStyle().
		Foreground(colorDrifted).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDrifted).
		Padding(0, 1)

	KeyLabel = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	Muted = lipgloss.NewStyle().
		Foreground(colorMuted)

	Text = lipgloss.NewStyle().
		Foreground(colorText)
)

// Pill renders a "key value" label in the alerts-TUI shape: the key gets
// its own foreground hue (no background), the value uses the regular text
// color. Scannable without flooding the UI with color blocks.
func Pill(fg lipgloss.Color, key, value string) string {
	keyStyle := lipgloss.NewStyle().Foreground(fg).Bold(true)
	valStyle := lipgloss.NewStyle().Foreground(colorText)
	return keyStyle.Render(key) + " " + valStyle.Render(value)
}

// Badge renders a single-color filled pill for a headline (severity, phase,
// status). White foreground over the bg argument.
func Badge(bg lipgloss.Color, text string) string {
	return lipgloss.NewStyle().
		Foreground(colorWhite).
		Background(bg).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

// Dot returns a single colored bullet glyph.
func Dot(c lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(c).Render("●")
}

// StatusColor returns the foreground color for a fleet-row status string.
func StatusColor(status string) lipgloss.Color {
	switch status {
	case "Ready":
		return colorReady
	case "Reconciling":
		return colorReconciling
	case "Drifted":
		return colorDrifted
	case "Failed":
		return colorFailed
	case "Unreachable":
		return colorUnreachable
	default:
		return colorUnknown
	}
}
