package initform

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// ErrPanelCancelled is returned by RunPanel when the operator quits the
// install panel without applying (Esc/Ctrl+C on the section pane).
var ErrPanelCancelled = errors.New("install panel cancelled")

func (m *PanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := fingerprint(m.st)
	defer func() {
		if before != fingerprint(m.st) {
			m.changed()
		}
		if w := m.workflow; w != nil && m.currentSection() == "Review" && w.review.CanRun && w.reviewHash == m.stateHash() {
			w.reviewedHash = w.reviewHash
		}
	}()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 && msg.Height > 0 {
			m.width, m.height = msg.Width, msg.Height
		}
		return m, nil
	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
		m.input.SetStyles(textinput.DefaultStyles(!m.light))
		return m, nil
	case PanelEvent:
		m.handlePanelEvent(msg)
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
		if msg.String() == "ctrl+c" {
			m.Close()
			m.cancelled = true
			return m, tea.Quit
		}
		if m.tooSmall() {
			return m, nil
		}
		if m.Busy() {
			if msg.String() == "q" {
				m.Close()
				m.cancelled = true
				return m, tea.Quit
			}
			return m, nil
		}
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateNav(msg)
	default:
		if m.editing && !m.tooSmall() {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
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
		m.Close()
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
	case "space", " ":
		if m.focus == focusFields {
			if f := m.editingField(); f != nil && f.Kind == panelRadio {
				return m.activate()
			}
		}
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
		m.manualScroll = false
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
		m.manualScroll = false
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
	case "pgup", "pgdown":
		m.manualScroll = true
		if msg.String() == "pgup" {
			m.fieldsVP.ScrollUp(5)
		} else {
			m.fieldsVP.ScrollDown(5)
		}
		return m, nil
	case "n":
		return m, m.primaryAction()
	case "r":
		if m.workflow != nil {
			return m, m.startReadiness()
		}
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
		m.input.CharLimit = 512
		if f.Section == "Configuration" {
			m.input.CharLimit = 1 << 20
		}
		m.input.SetValue(f.Get(m.st))
		m.input.CursorEnd()
		return m, m.input.Focus()
	case panelSelect:
		f.Set(m.st, cycleOption(f.Options, f.Get(m.st)))
		m.clampCursors() // a mode/preset/OSMode change can hide/show fields+sections
		return m, nil
	case panelToggle:
		cur := f.Get(m.st) == "yes"
		f.Set(m.st, boolStr(!cur))
		return m, nil
	case panelRadio, panelAction:
		if f.Action != "" {
			return m, m.activateWorkflow(f.Action)
		}
		if m.workflow != nil && m.workflow.services.Session != nil {
			return m, m.startSession("session-gates")
		}
		if len(m.validationErrors()) == 0 {
			if reason := m.discoveryBlocker(); reason != "" {
				m.notice = reason
				return m, nil
			}
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
				marker = m.theme().accent.Render("▸ ")
			} else {
				marker = m.theme().muted.Render("▸ ")
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
		if override, ok := m.workflowBadge(s); ok && !secErr(s) {
			badge = override
		}
		b.WriteString(marker + m.theme().text.Render(s) + badge + "\n")
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
		b.WriteString(m.theme().muted.Render("(no settings)"))
		return b.String(), 0
	}
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#2F9E72"))
	warnStyle := lipgloss.NewStyle().Foreground(colorWarnFG())
	labelWidth := 24
	if m.currentSection() == "Configuration" {
		for _, f := range fs {
			labelWidth = max(labelWidth, lipgloss.Width(f.Label))
		}
		labelWidth = min(labelWidth, max(24, maxW/2))
	}
	for i, f := range fs {
		focused := m.focus == focusFields && i == m.fieldCursor
		cursor := "  "
		if focused {
			cursor = m.theme().accent.Render("▸ ")
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
			val = safeText(f.Get(m.st))
		}
		row := cursor + glyph + " " + m.theme().text.Render(padRight(lbl, labelWidth)) + " "
		if f.Kind == panelRadio {
			mark := "( )"
			if val == "yes" {
				mark = "(●)"
			}
			row = cursor + m.theme().accent.Render(mark) + " " + m.theme().text.Render(lbl)
		}
		if m.editing && focused {
			b.WriteString(row + "\n  " + m.input.View() + "\n")
			if m.editError != "" {
				b.WriteString("      " + warnStyle.Render(m.editError) + "\n")
			}
		} else {
			var shown string
			switch f.Kind {
			case panelSelect:
				shown = m.theme().text.Render("‹ "+val+" ›") + "  " + m.theme().muted.Render("←→")
			case panelToggle:
				box := "[ ]"
				if val == "yes" {
					box = "[x]"
				}
				shown = box + "  " + m.theme().muted.Render("↵")
			case panelRadio:
				shown = ""
			case panelAction:
				shown = m.theme().accent.Render("[ enter ]")
			case panelText:
				if val == "" {
					shown = m.theme().muted.Render("(empty)")
				} else {
					shown = safeText(val)
				}
			}
			b.WriteString(row + shown + "\n")
		}
		if focused && f.Desc != "" {
			for _, line := range wrapPlain(f.Desc, maxW-4) {
				b.WriteString("    " + m.theme().muted.Render(line) + "\n")
			}
		}
	}
	// Review section: show the equivalent flags preview + any hint.
	if m.currentSection() == "Review" && (m.workflow == nil || m.workflow.services.Demo == nil) {
		b.WriteString("\n")
		if verrs := m.validationErrors(); len(verrs) > 0 {
			b.WriteString(m.theme().muted.Render("blocked — fix:") + "\n")
			for _, e := range verrs {
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ "+e) + "\n")
			}
		} else if flags, err := m.equivalentPreview(); err != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(colorWarnFG()).Render("⚠ cannot build preview: "+err.Error()) + "\n")
		} else {
			b.WriteString(m.theme().muted.Render("equivalent command (safe preview):") + "\n")
			for _, line := range strings.Split(flags, "\n") {
				b.WriteString("  " + m.theme().text.Render(safeText(line)) + "\n")
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
			b.WriteString("\n" + m.theme().muted.Render(fmt.Sprintf("advanced (--set): %d preserved key(s)", len(keys))) + "\n")
			for _, k := range keys {
				b.WriteString("  " + m.theme().muted.Render(safeText(k+"="+m.st.ExtraSets[k])) + "\n")
			}
		}
		if o, err := m.st.draftOptions(); err == nil {
			if text, err := clusterinit.RenderSpec(o); err == nil {
				b.WriteString("\n" + m.theme().accent.Render("Install input file · S saves this configuration") + "\n")
				b.WriteString(m.theme().muted.Render("Edit all input keys in Configuration. Fleet defaults and patches are generated during installation.") + "\n")
				for _, line := range strings.Split(text, "\n") {
					b.WriteString(m.theme().text.Render(safeText(line)) + "\n")
				}
			} else {
				b.WriteString("\n" + m.theme().bad.Render(safeText(err.Error())) + "\n")
			}
		}
		if m.hint != "" {
			b.WriteString("\n" + m.theme().muted.Render(safeText(m.hint)) + "\n")
		}
	}
	return m.finishBody(b.String(), cursorLine, maxW)
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
		Mode: string(clusterinit.ModeInstall), OSMode: string(clusterinit.RookCephMultiNode), Provider: "github", Preset: string(clusterinit.PresetCloudVLAN), InstallationKind: "kube-dc", ManagedServicesMode: "auto",
		// NOTE: the front-door default is applied AFTER the prefill overlay,
		// not here — see the block below FromOptions. Seeding it here would
		// convert an untouched recommendation into an explicit answer for a
		// config file that declared a VIP and no layer, which the flag path
		// REFUSES as ambiguous. The wizard must not launder that ambiguity.
		MetalLBMode: "l2",
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
	// Front-door default, applied only when the prefill left the question
	// genuinely OPEN. A greenfield operator gets the recommended answer
	// (metallb-l2 — a stable address is what DNS wants); an operator opening
	// the wizard over a config that declares a VIP but no layer gets an EMPTY
	// field, so the same ambiguity the flag path refuses surfaces here as a
	// question rather than being answered on their behalf.
	if st.IngressAddressLayer == "" && strings.TrimSpace(st.MetalLBVIP) == "" {
		st.IngressAddressLayer = clusterinit.AddressLayerMetalLBL2
	}
	return st
}

// NewEmbeddedPanel builds a PanelModel over o's prefill for embedding
// in a parent TUI (the root-router Init tab) — same state setup as
// RunPanel without owning a tea.Program. The parent reads the outcome
// via Applied()/Cancelled() + Result().
//
// probe (nil-safe) carries live-cluster facts gathered best-effort by
// the cobra layer; they fill ONLY fields still empty after the
// config/env/flag prefill (lowest precedence — see prefill_probe.go)
// and the filled values are named in the hint line so the operator
// knows they came from the cluster, not their own input.
func NewEmbeddedPanel(o *clusterinit.InitOptions, siblingHint string, probe *ProbePrefill) *PanelModel {
	st := initialState(o)
	if probe != nil {
		if notes := probe.ApplyTo(st); len(notes) > 0 {
			probedLine := "probed from live cluster: " + strings.Join(notes, "; ")
			if siblingHint != "" {
				siblingHint += "\n" + probedLine
			} else {
				siblingHint = probedLine
			}
		}
	}
	pm := NewPanelModel(st, siblingHint)
	pm.MarkEmbedded()
	return pm
}

// Result applies an Applied panel's state onto o and returns the
// equivalent-flags rendering (thin-generator contract). A panel that
// was cancelled — or never reached Apply — returns ErrPanelCancelled
// and leaves o untouched.
func (m *PanelModel) Result(o *clusterinit.InitOptions) (string, error) {
	if m.Cancelled() || !m.Applied() {
		return "", ErrPanelCancelled
	}
	// m.st is the same *State initialState built, mutated by the
	// operator's edits during the run.
	if err := m.st.Apply(o); err != nil {
		return "", err
	}
	return m.st.EquivalentFlags(o), nil
}

// RunPanel runs the install settings panel standalone (the bare-TTY
// `bootstrap init` path), applies the result onto o, and returns the
// equivalent-flags rendering. A cancelled panel returns
// ErrPanelCancelled and leaves o untouched. See NewEmbeddedPanel for
// the probe contract.
func RunPanel(o *clusterinit.InitOptions, siblingHint string, probe *ProbePrefill, services ...PanelServices) (string, error) {
	m := NewEmbeddedPanel(o, siblingHint, probe)
	if len(services) > 0 {
		m.ConfigureServices(services[0])
	}
	defer m.Close()
	res, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	pm, ok := res.(*PanelModel)
	if !ok {
		return "", ErrPanelCancelled
	}
	return pm.Result(o)
}
