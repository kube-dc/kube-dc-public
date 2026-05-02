package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap describes every keystroke the bootstrap TUI binds. Modeled on
// cli/internal/alerts/tui/keys.go so the two TUIs feel identical to use.
//
// Per installer-prd §9.9.1, screens with multiple panes drive focus with
// Tab/Shift+Tab and route arrows to the focused pane only. Per §9.9.2,
// each screen builds its own ShortHelp/FullHelp from this union — keys
// that aren't actionable in the current state stay out of the help bar.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
	Esc      key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Quit     key.Binding

	// Fleet-landing actions. The v2 actions (NewInstall, Adopt, Status,
	// Config, Discover) live in the keymap but are filtered out of the
	// fleet screen's help until those slices ship — see installer-prd
	// §17.1 (Slices 4, 5, 7, 8, 9).
	NewInstall key.Binding // [n] (v2)
	Adopt      key.Binding // [a] (v2)
	Status     key.Binding // [s] (v2)
	Config     key.Binding // [c] (v2)
	Discover   key.Binding // [d] (v2 — kept distinct from Delete; Delete is context-screen only)

	// In-TUI actions on the selected row/context.
	LoginAdmin key.Binding // [L] admin login
	LoginOrg   key.Binding // [l] tenant login (org prompt)
	TestAuth   key.Binding // [t] HEAD /readyz with cached creds
	Delete     key.Binding // [d] delete (context view only)
}

// ShortHelp is the compact help row at the bottom of the screen — the
// default used by screens whose help text is state-independent (e.g.
// ContextModel). Stateful screens like FleetModel build their own
// ShortHelp/FullHelp to honour the active-only-help rule (§9.9.2).
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Tab, k.Refresh, k.Help, k.Quit}
}

// FullHelp is the expanded help shown on '?' for screens with static
// help. State-aware screens override this locally.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End},
		{k.Tab, k.ShiftTab, k.Enter, k.Esc},
		{k.LoginAdmin, k.LoginOrg, k.TestAuth, k.Delete},
		{k.Refresh, k.Help, k.Quit},
	}
}

// DefaultKeyMap returns the keybindings used by the bootstrap TUI.
// Per-screen ShortHelp / FullHelp methods on each *Model build the
// help bar from this union based on current state.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn", "page down")),
		Home:     key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "first")),
		End:      key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "last")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev pane")),
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
