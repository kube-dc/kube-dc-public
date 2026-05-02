package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap describes every keystroke the bootstrap TUI binds. Modeled on
// cli/internal/alerts/tui/keys.go so the two TUIs feel identical to use.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
	Tab      key.Binding
	Enter    key.Binding
	Esc      key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Quit     key.Binding

	// Fleet-landing actions.
	NewInstall key.Binding // [n]
	Adopt      key.Binding // [a]
	Status     key.Binding // [s]
	Config     key.Binding // [c]
	Discover   key.Binding // [d]

	// In-TUI actions wired in v1.2 — operator hits these on the selected
	// row/context to launch login / auth-test without leaving the TUI.
	LoginAdmin key.Binding // [L] admin login
	LoginOrg   key.Binding // [l] tenant login (org prompt)
	TestAuth   key.Binding // [t] HEAD /readyz with cached creds
	Delete     key.Binding // [d] delete (context view only — collides with Discover in fleet)
}

// ShortHelp is the compact help row at the bottom of the screen. Keep
// it under ~80 cols so it fits on a single line on a typical terminal;
// less-frequent actions are surfaced via the '?' full help.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.LoginAdmin, k.Refresh, k.Help, k.Quit}
}

// FullHelp is the expanded help shown on '?'.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End},
		{k.Enter, k.LoginAdmin, k.LoginOrg, k.TestAuth},
		{k.Status, k.Config, k.Discover, k.Delete},
		{k.NewInstall, k.Adopt, k.Refresh},
		{k.Help, k.Quit},
	}
}

// DefaultKeyMap returns the keybindings used by the bootstrap TUI.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn", "page down")),
		Home:     key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "first")),
		End:      key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "last")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle pane")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "open")),
		Esc:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		NewInstall: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new install")),
		Adopt:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "adopt")),
		Status:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "status")),
		Config:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "config")),
		Discover:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "discover")),

		LoginAdmin: key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "admin login")),
		LoginOrg:   key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "tenant login")),
		TestAuth:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "test auth")),
		Delete:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
	}
}
