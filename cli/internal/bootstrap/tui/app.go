package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// RunFleet starts the Bubble Tea program with the fleet landing screen
// rooted at repoRoot. Wraps tea.NewProgram with the same options the
// alerts TUI uses (alt-screen + mouse cell motion).
func RunFleet(repoRoot string, newModel func(repoRoot string) tea.Model) error {
	p := tea.NewProgram(newModel(repoRoot), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
