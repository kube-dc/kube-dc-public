package tui

import "github.com/charmbracelet/lipgloss"

// Palette — chosen to read well on dark backgrounds and roughly match
// the Grafana Alertmanager UI.
var (
	colorBg        = lipgloss.Color("#181B1F")
	colorSurface   = lipgloss.Color("#22252B")
	colorBorder    = lipgloss.Color("#2E3137")
	colorBorderHi  = lipgloss.Color("#7E5CAD")
	colorText      = lipgloss.Color("#E6E9EF")
	colorMuted     = lipgloss.Color("#8B92A5")
	colorAccent    = lipgloss.Color("#5794F2")
	colorAccentAlt = lipgloss.Color("#2F9E72")
	colorWhite     = lipgloss.Color("#FFFFFF")
)

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
			Foreground(lipgloss.Color("#F2495C")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#F2495C")).
			Padding(0, 1)

	SearchBox = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(colorBorderHi).
			Padding(0, 1)

	KeyLabel = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	Muted = lipgloss.NewStyle().
		Foreground(colorMuted)

	Text = lipgloss.NewStyle().
		Foreground(colorText)
)

// Pill renders a subtle "key value" label. The key is colored in its
// own hue (no background), the value is in the regular text color.
// This keeps labels scannable without flooding the UI with color blocks.
func Pill(fg lipgloss.Color, key, value string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(fg).
		Bold(true)
	valStyle := lipgloss.NewStyle().
		Foreground(colorText)
	return keyStyle.Render(key) + " " + valStyle.Render(value)
}

// Badge renders a single-colored pill for a headline (like severity).
func Badge(bg lipgloss.Color, text string) string {
	return lipgloss.NewStyle().
		Foreground(colorWhite).
		Background(bg).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

// Dot returns a severity dot with its native color.
func Dot(color lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(color).Render("●")
}
