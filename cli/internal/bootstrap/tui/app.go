package tui

import (
	tea "charm.land/bubbletea/v2"
)

// RunRoot starts the integrated bootstrap TUI (the RootModel built in
// the screens package, wrapping fleet + contexts + future tabs behind
// a top tab bar — see installer-prd §9.9.5). The cobra entry points
// pass a factory that constructs the right RootModel for the chosen
// starting tab.
//
// In Bubble Tea v2 the alt-screen + mouse-mode flags are declarative
// View fields, not NewProgram options — RootModel.View() sets AltScreen
// and MouseMode, so the program constructor is now bare.
func RunRoot(newModel func() tea.Model) error {
	p := tea.NewProgram(newModel())
	_, err := p.Run()
	return err
}
