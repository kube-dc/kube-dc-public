package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap describes every keystroke the TUI binds.
type KeyMap struct {
	Up             key.Binding
	Down           key.Binding
	PageUp         key.Binding
	PageDown       key.Binding
	Home           key.Binding
	End            key.Binding
	Tab            key.Binding
	ShiftTab       key.Binding
	Search         key.Binding
	Group          key.Binding
	Refresh        key.Binding
	Reconnect      key.Binding
	Focus          key.Binding
	OpenURL        key.Binding
	Help           key.Binding
	Quit           key.Binding
	Enter          key.Binding
	Esc            key.Binding
	ScrollDetails  key.Binding
}

// ShortHelp is the compact help row shown at the bottom of the screen.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.ScrollDetails, k.Tab, k.Search, k.Group, k.Refresh, k.Reconnect, k.Help, k.Quit}
}

// FullHelp is the expanded help shown on '?'.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End},
		{k.Tab, k.ShiftTab, k.Focus, k.Enter},
		{k.Search, k.Group, k.Refresh, k.Reconnect, k.OpenURL},
		{k.Help, k.Quit},
	}
}

// DefaultKeyMap returns the keybindings used by the TUI.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn", "page down")),
		Home:     key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "first")),
		End:      key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "last")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next severity")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("S-tab", "prev severity")),
		Search:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Group:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "group by")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Reconnect: key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "reconnect")),
		Focus:    key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle pane")),
		OpenURL:  key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open runbook")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
		Esc:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:          key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:          key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		ScrollDetails: key.NewBinding(key.WithKeys("J", "K", "pgup", "pgdown"), key.WithHelp("J/K", "scroll details")),
	}
}
