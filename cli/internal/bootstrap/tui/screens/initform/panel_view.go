package initform

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// ErrPanelCancelled is returned by RunPanel when the operator quits the
// install panel without applying (Esc/Ctrl+C on the section pane).
var ErrPanelCancelled = errors.New("install panel cancelled")

func (m *PanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateNav(msg)
	}
	return m, nil
}

// updateEditing handles keys while a text field is being edited.
func (m *PanelModel) updateEditing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		f := m.editingField()
		if f != nil && val != "" && f.Validate != nil {
			if err := f.Validate(val); err != nil {
				// Keep editing; the footer shows the error.
				return m, nil
			}
		}
		if f != nil {
			f.Set(m.st, val)
		}
		m.editing = false
		m.input.Blur()
		return m, nil
	case "esc":
		m.editing = false
		m.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// editingField is the field currently being edited (or nil).
func (m *PanelModel) editingField() *panelField {
	fs := m.currentFields()
	if m.fieldCursor >= 0 && m.fieldCursor < len(fs) {
		return &fs[m.fieldCursor]
	}
	return nil
}

// updateNav handles keys while navigating (not editing).
func (m *PanelModel) updateNav(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.cancelled = true
		return m, tea.Quit
	case "esc":
		if m.focus == focusFields {
			m.focus = focusSections
		} else {
			m.cancelled = true
			return m, tea.Quit
		}
		return m, nil
	case "tab", "right", "l":
		if m.focus == focusSections {
			m.focus = focusFields
			m.clampFieldCursor()
		}
		return m, nil
	case "shift+tab", "left", "h":
		if m.focus == focusFields {
			m.focus = focusSections
		}
		return m, nil
	case "up", "k":
		if m.focus == focusSections {
			if m.secCursor > 0 {
				m.secCursor--
				m.fieldCursor = 0
			}
		} else if m.fieldCursor > 0 {
			m.fieldCursor--
		}
		return m, nil
	case "down", "j":
		if m.focus == focusSections {
			if m.secCursor < len(m.visibleSections())-1 {
				m.secCursor++
				m.fieldCursor = 0
			}
		} else if m.fieldCursor < len(m.currentFields())-1 {
			m.fieldCursor++
		}
		return m, nil
	case "enter":
		return m.activate()
	}
	return m, nil
}

// activate acts on the focused row: sections → jump into fields; a field
// → edit (text) / cycle (select) / flip (toggle) / apply (action).
func (m *PanelModel) activate() (tea.Model, tea.Cmd) {
	if m.focus == focusSections {
		m.focus = focusFields
		m.clampFieldCursor()
		return m, nil
	}
	f := m.editingField()
	if f == nil {
		return m, nil
	}
	switch f.Kind {
	case panelText:
		m.editing = true
		m.input.SetValue(f.Get(m.st))
		m.input.CursorEnd()
		m.input.Focus()
		return m, nil
	case panelSelect:
		f.Set(m.st, cycleOption(f.Options, f.Get(m.st)))
		m.clampCursors() // a mode/preset/OSMode change can hide/show fields+sections
		return m, nil
	case panelToggle:
		cur := f.Get(m.st) == "yes"
		f.Set(m.st, boolStr(!cur))
		return m, nil
	case panelAction:
		if len(m.validationErrors()) == 0 {
			m.applied = true
			return m, tea.Quit
		}
		// Invalid → stay; footer shows what's missing.
		return m, nil
	}
	return m, nil
}

// cycleOption returns the option after cur (wraps); first if cur unknown.
func cycleOption(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	if len(opts) > 0 {
		return opts[0]
	}
	return cur
}

func (m *PanelModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		v := tea.NewView("Initializing…")
		v.AltScreen = true
		return v
	}
	w := m.width - 2
	title := joinSpaced(w,
		bttui.Title.Render(" Kube-DC — New Cluster ")+"  "+bttui.Muted.Render(m.st.Mode+" · "+nonEmptyOr(m.st.Preset, "preset?")),
		bttui.Muted.Render("tab panes · ↑↓ move · enter edit/apply · esc back"))

	// Body chrome: 1 title + 1 footer (+ error rows).
	verrs := m.validationErrors()
	bodyH := m.height - 3
	if bodyH < 8 {
		bodyH = 8
	}

	// Left: sections. Right: the selected section's fields.
	leftW := 22
	rightW := w - leftW - 1
	if rightW < 30 {
		rightW = 30
	}

	secStyle := bttui.ListPane
	fldStyle := bttui.DetailsPane
	if m.focus == focusSections {
		secStyle = bttui.ListPaneFocused
	} else {
		fldStyle = bttui.DetailsPaneFocused
	}
	left := secStyle.Width(leftW).Height(bodyH - 2).Render(m.renderSections())
	right := fldStyle.Width(rightW).Height(bodyH - 2).Render(m.renderFields(rightW - 4))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Footer: validation summary + help.
	var footer string
	if len(verrs) > 0 {
		footer = bttui.ErrorBox.Width(w).Render(fmt.Sprintf("%d setting(s) need attention — Review ▸ Apply is blocked", len(verrs)))
	} else {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("#2F9E72")).Render("  ✓ ready — go to Review and press enter to Apply")
	}
	// PanelModel is a top-level program model (RunPanel) — v2 declares the
	// alt-screen flag on the View, not as a NewProgram option.
	v := tea.NewView(bttui.AppStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, body, footer)))
	v.AltScreen = true
	return v
}

func (m *PanelModel) renderSections() string {
	var b strings.Builder
	verrs := m.validationErrors()
	for i, s := range m.visibleSections() {
		marker := "  "
		if i == m.secCursor {
			if m.focus == focusSections {
				marker = bttui.KeyLabel.Render("▸ ")
			} else {
				marker = bttui.Muted.Render("▸ ")
			}
		}
		// A ⚠ on a section that has an unsatisfied field.
		badge := ""
		for _, e := range verrs {
			if strings.HasPrefix(e, s+"/") || strings.HasPrefix(e, s+":") {
				badge = " " + lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠")
				break
			}
		}
		b.WriteString(marker + bttui.Text.Render(s) + badge + "\n")
	}
	return b.String()
}

func (m *PanelModel) renderFields(maxW int) string {
	fs := m.currentFields()
	var b strings.Builder
	b.WriteString(bttui.Title.Render(" "+m.currentSection()+" ") + "\n\n")
	if len(fs) == 0 {
		b.WriteString(bttui.Muted.Render("(no settings)"))
		return b.String()
	}
	for i, f := range fs {
		cursor := "  "
		if m.focus == focusFields && i == m.fieldCursor {
			cursor = bttui.KeyLabel.Render("▸ ")
		}
		// Action fields (Apply) carry no Get closure — guard the nil.
		val := ""
		if f.Get != nil {
			val = f.Get(m.st)
		}
		// The field being edited shows the live input.
		if m.editing && m.focus == focusFields && i == m.fieldCursor {
			b.WriteString(cursor + bttui.Text.Render(padRight(f.Label, 24)) + " " + m.input.View() + "\n")
		} else {
			shown := val
			switch f.Kind {
			case panelSelect:
				shown = val + "  " + bttui.Muted.Render("(enter cycles)")
			case panelToggle:
				shown = val + "  " + bttui.Muted.Render("(enter toggles)")
			case panelAction:
				shown = bttui.KeyLabel.Render("[ enter ]")
			case panelText:
				if shown == "" {
					shown = bttui.Muted.Render("(empty)")
				}
			}
			b.WriteString(cursor + bttui.Text.Render(padRight(f.Label, 24)) + " " + shown + "\n")
		}
		// Description under the selected field.
		if m.focus == focusFields && i == m.fieldCursor && f.Desc != "" {
			b.WriteString("    " + bttui.Muted.Render(f.Desc) + "\n")
		}
	}
	// Review section: show the equivalent flags preview + any hint.
	if m.currentSection() == "Review" {
		b.WriteString("\n")
		if verrs := m.validationErrors(); len(verrs) > 0 {
			b.WriteString(bttui.Muted.Render("blocked — fix:") + "\n")
			for _, e := range verrs {
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ "+e) + "\n")
			}
		}
		if m.hint != "" {
			b.WriteString("\n" + bttui.Muted.Render(m.hint) + "\n")
		}
	}
	return b.String()
}

func nonEmptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// RunPanel runs the install settings panel, applies the result onto o,
// and returns the equivalent-flags rendering (thin-generator contract).
// A cancelled panel returns ErrPanelCancelled and leaves o untouched.
func RunPanel(o *clusterinit.InitOptions, siblingHint string) (string, error) {
	st := &State{
		Mode:      string(clusterinit.ModeInstall),
		FleetMode: string(o.FleetMode),
		Provider:  "github",
		Preset:    string(clusterinit.PresetCloudVLAN),
		Repo:      o.Repo,
	}
	if st.FleetMode == "" {
		st.FleetMode = string(clusterinit.FleetExistingFleet)
	}
	m := NewPanelModel(st, siblingHint)
	res, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	pm, ok := res.(*PanelModel)
	if !ok || pm.Cancelled() || !pm.Applied() {
		return "", ErrPanelCancelled
	}
	if err := st.Apply(o); err != nil {
		return "", err
	}
	return st.EquivalentFlags(o), nil
}
