package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// RunRoot starts the integrated bootstrap TUI (the RootModel built in
// the screens package, wrapping fleet + contexts + future tabs behind
// a top tab bar — see installer-prd §9.9.5). The cobra entry points
// pass a factory that constructs the right RootModel for the chosen
// starting tab.
//
// Wraps tea.NewProgram with the same options the alerts TUI uses
// (alt-screen + mouse cell motion).
func RunRoot(newModel func() tea.Model) error {
	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
