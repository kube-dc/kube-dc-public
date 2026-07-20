package initform

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/viewport"
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
	case tea.MouseWheelMsg:
		// Wheel scrolls the fields pane (the only scrollable region).
		if m.focus == focusFields && !m.editing {
			var cmd tea.Cmd
			m.fieldsVP, cmd = m.fieldsVP.Update(msg)
			return m, cmd
		}
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
				m.editError = err.Error()
				return m, nil
			}
		}
		if f != nil {
			f.Set(m.st, val)
		}
		m.editError = ""
		m.editing = false
		m.input.Blur()
		return m, nil
	case "esc":
		m.editError = ""
		m.editing = false
		m.input.Blur()
		return m, nil
	}
	m.editError = ""
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
	m.notice = "" // transient — cleared on the next keystroke, re-set by 'S'
	switch msg.String() {
	case "ctrl+c", "q":
		// Quit keys match the Fleet TUI (q / ctrl+c). Only reachable in
		// nav mode — while a text field is being edited, updateEditing
		// owns the keys ('q' types a char, esc cancels the edit).
		m.cancelled = true
		return m, tea.Quit
	case "esc":
		// Esc is "back", never an exit — same as the Fleet TUI, where Esc
		// steps focus back and q / ctrl+c quit. From the fields pane it
		// returns to the sections list; on the sections pane it's a no-op.
		if m.focus == focusFields {
			m.focus = focusSections
		}
		return m, nil
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "tab":
		// Toggle focus both ways (a single, obvious key).
		if m.focus == focusSections {
			m.focus = focusFields
			m.clampFieldCursor()
		} else {
			m.focus = focusSections
		}
		return m, nil
	case "shift+tab":
		if m.focus == focusFields {
			m.focus = focusSections
		} else {
			m.focus = focusFields
			m.clampFieldCursor()
		}
		return m, nil
	case "l":
		if m.focus == focusSections {
			m.focus = focusFields
			m.clampFieldCursor()
		}
		return m, nil
	case "h":
		if m.focus == focusFields {
			m.focus = focusSections
		}
		return m, nil
	case "left", "right":
		// ←/→ cycle a select field in place (forward/back); no-op otherwise.
		if m.focus == focusFields {
			if f := m.editingField(); f != nil && f.Kind == panelSelect {
				if msg.String() == "left" {
					f.Set(m.st, cycleOptionBack(f.Options, f.Get(m.st)))
				} else {
					f.Set(m.st, cycleOption(f.Options, f.Get(m.st)))
				}
				m.clampCursors()
			}
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
	case "S":
		// Save the current answers as a draft spec (decide later). Works
		// from any pane, even with required fields still blank.
		m.saveDraft()
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
		m.editError = ""
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

// cycleOptionBack returns the option before cur (wraps); last if unknown.
func cycleOptionBack(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i-1+len(opts))%len(opts)]
		}
	}
	if len(opts) > 0 {
		return opts[len(opts)-1]
	}
	return cur
}

// ensureLineVisible scrolls vp so content line `line` stays on screen as
// the field cursor moves past the fold. No-op when line is off or the vp
// is unsized (must run after the vp Height is set — View sizes it).
func ensureLineVisible(vp *viewport.Model, line int) {
	if line < 0 || vp.Height() <= 0 {
		return
	}
	top := vp.YOffset()
	bottom := top + vp.Height() - 1
	switch {
	case line < top:
		vp.SetYOffset(line)
	case line > bottom:
		vp.SetYOffset(line - vp.Height() + 1)
	}
}

func (m *PanelModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return m.frame("Initializing…")
	}
	w := m.width - 2
	verrs := m.validationErrors()

	// Title: brand + mode·preset on the left, a compact live STATUS on the
	// right (✓ ready / N need attention / transient notice).
	var status string
	switch {
	case m.notice != "":
		status = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Render(m.notice)
	case m.editing && m.editError != "":
		status = lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ " + m.editError)
	case len(verrs) > 0:
		status = lipgloss.NewStyle().Foreground(colorWarnFG()).Render(fmt.Sprintf("⚠ %d setting(s) need attention", len(verrs)))
	default:
		status = lipgloss.NewStyle().Foreground(lipgloss.Color("#2F9E72")).Render("✓ ready — open Review → Apply")
	}
	title := joinSpaced(w,
		bttui.Title.Render(" Kube-DC — New Cluster ")+"  "+bttui.Muted.Render(m.st.Mode+" · "+nonEmptyOr(m.st.Preset, "preset?")),
		status)

	// Footer help bar (dynamic height): short by default, full on '?'.
	m.help.SetWidth(w)
	var footer string
	if m.showHelp {
		footer = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		footer = m.help.ShortHelpView(m.keys.ShortHelp())
	}
	footerH := lipgloss.Height(footer)

	// Body height = total − title(1) − footer(N). Best-practice: measure,
	// don't hardcode.
	bodyH := m.height - 1 - footerH
	if bodyH < 8 {
		bodyH = 8
	}

	// Left: sections. Right: the selected section's fields (scrollable).
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

	// Fields pane: fixed section header + a scrolling viewport of the fields
	// so a long section (Network/Storage) never overflows the pane.
	hdr := bttui.Title.Render(" " + m.currentSection() + " ")
	vpH := bodyH - 2 /*border*/ - 2 /*hdr+blank*/
	if vpH < 3 {
		vpH = 3
	}
	m.fieldsVP.SetWidth(rightW - 2 /*border*/ - 2 /*padding*/)
	m.fieldsVP.SetHeight(vpH)
	content, cursorLine := m.renderFieldsBody(rightW - 4)
	m.fieldsVP.SetContent(content)
	if m.focus == focusFields {
		ensureLineVisible(&m.fieldsVP, cursorLine)
	}
	// Scroll affordance in the header when there's more below/above.
	scroll := ""
	if m.fieldsVP.TotalLineCount() > m.fieldsVP.Height() {
		scroll = "  " + bttui.Muted.Render(fmt.Sprintf("%3.0f%% ↑↓", m.fieldsVP.ScrollPercent()*100))
	}
	fieldsInner := hdr + scroll + "\n\n" + m.fieldsVP.View()
	right := fldStyle.Width(rightW).Height(bodyH - 2).Render(fieldsInner)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return m.frame(lipgloss.JoinVertical(lipgloss.Left, title, body, footer))
}

// frame wraps content in the panel's top-level tea.View (alt-screen +
// mouse, v2-declarative). Every View() path goes through here.
func (m *PanelModel) frame(content string) tea.View {
	v := tea.NewView(bttui.AppStyle.Render(content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *PanelModel) renderSections() string {
	var b strings.Builder
	verrs := m.validationErrors()
	secErr := func(s string) bool {
		for _, e := range verrs {
			if strings.HasPrefix(e, s+"/") || strings.HasPrefix(e, s+":") {
				return true
			}
		}
		return false
	}
	for i, s := range m.visibleSections() {
		marker := "  "
		if i == m.secCursor {
			if m.focus == focusSections {
				marker = bttui.KeyLabel.Render("▸ ")
			} else {
				marker = bttui.Muted.Render("▸ ")
			}
		}
		// ⚠ if a field needs attention, ✓ once the section is satisfied.
		badge := ""
		switch {
		case secErr(s):
			badge = " " + lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠")
		case len(m.visibleInSection(s)) > 0:
			badge = " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#2F9E72")).Render("✓")
		}
		b.WriteString(marker + bttui.Text.Render(s) + badge + "\n")
	}
	return b.String()
}

// renderFieldsBody renders the focused section's fields (WITHOUT the
// section title — View draws that as a fixed header above the scrolling
// viewport). It returns the content plus the content-line index of the
// focused field so View can scroll it into view.
func (m *PanelModel) renderFieldsBody(maxW int) (string, int) {
	fs := m.currentFields()
	var b strings.Builder
	cursorLine := 0
	if len(fs) == 0 {
		b.WriteString(bttui.Muted.Render("(no settings)"))
		return b.String(), 0
	}
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#2F9E72"))
	warnStyle := lipgloss.NewStyle().Foreground(colorWarnFG())
	for i, f := range fs {
		focused := m.focus == focusFields && i == m.fieldCursor
		cursor := "  "
		if focused {
			cursor = bttui.KeyLabel.Render("▸ ")
			cursorLine = strings.Count(b.String(), "\n") // this row's line
		}
		// Label + required marker.
		lbl := f.Label
		if f.Required {
			lbl += " *"
		}
		// Per-field validation glyph (text fields only).
		glyph := " "
		if f.Kind == panelText && f.Get != nil {
			v := strings.TrimSpace(f.Get(m.st))
			switch {
			case v != "" && f.Validate != nil && f.Validate(v) != nil:
				glyph = warnStyle.Render("⚠")
			case v != "":
				glyph = okStyle.Render("✓")
			}
		}
		val := ""
		if f.Get != nil {
			val = f.Get(m.st)
		}
		row := cursor + glyph + " " + bttui.Text.Render(padRight(lbl, 24)) + " "
		if m.editing && focused {
			b.WriteString(row + m.input.View() + "\n")
			if m.editError != "" {
				b.WriteString("      " + warnStyle.Render(m.editError) + "\n")
			}
		} else {
			var shown string
			switch f.Kind {
			case panelSelect:
				shown = bttui.Text.Render("‹ "+val+" ›") + "  " + bttui.Muted.Render("←→")
			case panelToggle:
				box := "[ ]"
				if val == "yes" {
					box = "[x]"
				}
				shown = box + "  " + bttui.Muted.Render("↵")
			case panelAction:
				shown = bttui.KeyLabel.Render("[ enter ]")
			case panelText:
				if val == "" {
					shown = bttui.Muted.Render("(empty)")
				} else {
					shown = val
				}
			}
			b.WriteString(row + shown + "\n")
		}
		if focused && f.Desc != "" {
			b.WriteString("      " + bttui.Muted.Render(f.Desc) + "\n")
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
		} else if flags, err := m.equivalentPreview(); err != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ cannot build preview: "+err.Error()) + "\n")
		} else {
			b.WriteString(bttui.Muted.Render("equivalent command (safe preview):") + "\n")
			for _, line := range strings.Split(flags, "\n") {
				b.WriteString("  " + bttui.Text.Render(line) + "\n")
			}
		}
		// Advanced overlay keys carried from a prefill/clone that have no
		// dedicated field — shown so they're visible (edit them in the
		// --config .env), and preserved untouched through Apply.
		if len(m.st.ExtraSets) > 0 {
			keys := make([]string, 0, len(m.st.ExtraSets))
			for k := range m.st.ExtraSets {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("\n" + bttui.Muted.Render(fmt.Sprintf("advanced (--set): %d preserved key(s)", len(keys))) + "\n")
			for _, k := range keys {
				b.WriteString("  " + bttui.Muted.Render(k+"="+m.st.ExtraSets[k]) + "\n")
			}
		}
		if m.hint != "" {
			b.WriteString("\n" + bttui.Muted.Render(m.hint) + "\n")
		}
	}
	return b.String(), cursorLine
}

func (m *PanelModel) equivalentPreview() (string, error) {
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		return "", err
	}
	return m.st.EquivalentFlags(o), nil
}

func nonEmptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// initialState builds the wizard's starting State from o. Defaults are
// chosen for the FIRST-TIME user: a brand-new operator has no fleet yet,
// so FleetMode starts on new-repo (create the fleet) — adding a cluster to
// an existing fleet is a 1-key cycle away on the Fleet mode field. An
// explicit --fleet-mode flag (non-empty o.FleetMode) always wins.
func initialState(o *clusterinit.InitOptions) *State {
	// Wizard defaults first (first-time user)…
	st := &State{
		Mode: string(clusterinit.ModeInstall), Provider: "github", Preset: string(clusterinit.PresetCloudVLAN),
		GPUPlatform: string(clusterinit.GPUPlatformDisabled), GPUDriverSource: string(clusterinit.GPUDriverOperator),
		GPUOperatorVersion:   clusterinit.DefaultGPUOperatorVersion,
		NVIDIADriverVersion:  clusterinit.DefaultNVIDIADriverVersion,
		NVIDIAToolkitVersion: clusterinit.DefaultNVIDIAToolkitVersion,
		HAMiVersion:          clusterinit.DefaultHAMiVersion,
		HAMiSchedulerVersion: clusterinit.DefaultHAMiSchedulerKubeVersion,
	}
	// …then overlay any prefill present in o (--config / KUBE_DC_INIT_* env
	// / flags). FromOptions only overlays non-empty values, so defaults
	// survive where the prefill is silent.
	st.FromOptions(o)
	if st.FleetMode == "" {
		st.FleetMode = string(clusterinit.FleetNewRepo)
	}
	return st
}

// RunPanel runs the install settings panel, applies the result onto o,
// and returns the equivalent-flags rendering (thin-generator contract).
// A cancelled panel returns ErrPanelCancelled and leaves o untouched.
func RunPanel(o *clusterinit.InitOptions, siblingHint string) (string, error) {
	m := NewPanelModel(initialState(o), siblingHint)
	res, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	pm, ok := res.(*PanelModel)
	if !ok || pm.Cancelled() || !pm.Applied() {
		return "", ErrPanelCancelled
	}
	// pm.st is the same *State initialState built, mutated by the operator's
	// edits during the run.
	if err := pm.st.Apply(o); err != nil {
		return "", err
	}
	return pm.st.EquivalentFlags(o), nil
}
