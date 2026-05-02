package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shalb/kube-dc/cli/internal/alerts"
)

// alertItem adapts alerts.Alert to bubbles/list.Item with a custom delegate
// rendering: a colored severity dot + alertname + a few label pills + age.
type alertItem struct {
	a *alerts.Alert
}

func (i alertItem) FilterValue() string {
	if i.a == nil {
		return ""
	}
	parts := []string{i.a.AlertName, i.a.Severity}
	for k, v := range i.a.Labels {
		parts = append(parts, k+"="+v)
	}
	for _, v := range i.a.Annotations {
		parts = append(parts, v)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// itemDelegate is a compact, visual list delegate for alerts.
type itemDelegate struct {
	width int
}

func (d itemDelegate) Height() int                             { return 2 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	it, ok := li.(alertItem)
	if !ok || it.a == nil {
		return
	}
	a := it.a
	selected := index == m.Index()

	// Row 1: dot + severity word + alert name + age (right-aligned).
	sevColor := alerts.SeverityColor(a.Severity)
	dot := lipgloss.NewStyle().Foreground(sevColor).Render("●")
	sev := lipgloss.NewStyle().Foreground(sevColor).Bold(true).Render(strings.ToUpper(a.Severity))
	name := lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(a.AlertName)
	if a.State == alerts.StateSuppressed {
		name = lipgloss.NewStyle().Foreground(colorMuted).Strikethrough(true).Render(a.AlertName)
	}
	age := Muted.Render(a.Age())

	left := dot + " " + sev + "  " + name
	rightW := lipgloss.Width(age)
	leftW := lipgloss.Width(left)
	padW := d.width - leftW - rightW - 2
	if padW < 1 {
		padW = 1
	}
	row1 := left + strings.Repeat(" ", padW) + age

	// Row 2: compact muted secondary line with the most useful labels.
	// Format: 'namespace · job · pod · state(if suppressed)'. Foreground-only,
	// no backgrounds — keeps the list visually calm.
	var bits []string
	for _, k := range []string{"namespace", "job", "pod", "instance"} {
		if v, ok := a.Labels[k]; ok && v != "" {
			bits = append(bits, lipgloss.NewStyle().Foreground(colorMuted).Render(v))
		}
	}
	if a.State == alerts.StateSuppressed {
		bits = append(bits, lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9830")).Render("silenced"))
	}
	sep := lipgloss.NewStyle().Foreground(colorBorder).Render(" · ")
	row2 := "  " + strings.Join(bits, sep)
	row2 = truncate(row2, d.width-2)

	row := row1 + "\n" + row2
	if selected {
		row = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBorderHi).
			PaddingLeft(1).
			Render(row)
	} else {
		row = lipgloss.NewStyle().PaddingLeft(2).Render(row)
	}
	fmt.Fprint(w, row)
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Rough truncate — respects runes but not ANSI. Sufficient for label pills
	// since we append suffix instead of cutting inside a pill.
	runes := []rune(s)
	for i := len(runes); i > 0; i-- {
		cut := string(runes[:i])
		if lipgloss.Width(cut+"…") <= w {
			return cut + "…"
		}
	}
	return ""
}
