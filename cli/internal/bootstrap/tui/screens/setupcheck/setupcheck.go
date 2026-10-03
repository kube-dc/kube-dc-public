// Package setupcheck shows the read-only guided setup readiness report.
package setupcheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

var ErrCancelled = errors.New("setup check cancelled")

// Probe performs the same checks used by text and JSON output.
type Probe func(context.Context) (setup.ReadinessReport, error)

type resultMsg struct {
	report setup.ReadinessReport
	err    error
}

type tickMsg struct{}

type model struct {
	ctx    context.Context
	cancel context.CancelFunc
	probe  Probe

	width, height int
	report        setup.ReadinessReport
	err           error
	done          bool
	cancelled     bool
	page          int // 0: findings; 1: open gates
	selected      int
	detail        bool
	detailScroll  int
	frame         int
}

// Run checks the setup in a background command and keeps the result visible
// until the operator exits. Quitting during a probe cancels its context.
func Run(ctx context.Context, probe Probe) (setup.ReadinessReport, error) {
	return runWithOptions(ctx, probe)
}

func runWithOptions(ctx context.Context, probe Probe, options ...tea.ProgramOption) (setup.ReadinessReport, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := &model{ctx: ctx, cancel: cancel, probe: probe, width: 80, height: 24}
	options = append(options, tea.WithContext(ctx))
	final, err := tea.NewProgram(m, options...).Run()
	fm, ok := final.(*model)
	if ok && fm.cancelled {
		return setup.ReadinessReport{}, ErrCancelled
	}
	if ctx.Err() != nil {
		return setup.ReadinessReport{}, ErrCancelled
	}
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	if !ok {
		return setup.ReadinessReport{}, fmt.Errorf("setup check TUI returned an unexpected model")
	}
	return fm.report, fm.err
}

func (m *model) probeCmd() tea.Cmd {
	return func() tea.Msg {
		report, err := m.probe(m.ctx)
		return resultMsg{report: report, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.probeCmd(), tickCmd()) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampDetailScroll()
	case resultMsg:
		m.report, m.err, m.done = msg.report, msg.err, true
		m.selected = 0
	case tickMsg:
		if !m.done {
			m.frame++
			return m, tickCmd()
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			if m.detail {
				m.detail = false
				m.detailScroll = 0
				return m, nil
			}
			fallthrough
		case "q", "ctrl+c":
			if !m.done {
				m.cancel()
				m.cancelled = true
			}
			return m, tea.Quit
		case "r":
			if m.done {
				m.done, m.err = false, nil
				m.report = setup.ReadinessReport{}
				m.selected, m.frame, m.detailScroll = 0, 0, 0
				m.detail = false
				return m, tea.Batch(m.probeCmd(), tickCmd())
			}
		case "enter":
			if m.done && m.err == nil && m.page == 0 && m.itemCount() > 0 {
				m.detail = true
				m.detailScroll = 0
			}
		case "tab", "right", "left":
			if m.detail {
				m.detail = false
				m.detailScroll = 0
			} else if m.done && m.err == nil {
				m.page = 1 - m.page
				m.selected = 0
			}
		case "up", "k":
			if m.detail {
				m.detailScroll--
				if m.detailScroll < 0 {
					m.detailScroll = 0
				}
			} else {
				m.move(-1)
			}
		case "down", "j":
			if m.detail {
				m.detailScroll++
			} else {
				m.move(1)
			}
		case "pgup":
			if m.detail {
				m.detailScroll -= m.listHeight()
				if m.detailScroll < 0 {
					m.detailScroll = 0
				}
			} else {
				m.move(-m.listHeight())
			}
		case "pgdown":
			if m.detail {
				m.detailScroll += m.listHeight()
			} else {
				m.move(m.listHeight())
			}
		case "home":
			m.selected = 0
		case "end":
			m.selected = m.itemCount() - 1
			if m.selected < 0 {
				m.selected = 0
			}
		}
	}
	m.clampDetailScroll()
	return m, nil
}

func (m *model) detailBody() []string {
	if !m.detail || m.page != 0 || m.selected >= len(m.report.Findings) {
		return nil
	}
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	f := m.report.Findings[m.selected]
	body := []string{}
	if f.Scope != "" || f.Resource != "" {
		body = append(body, wrapPlain("Scope: "+f.Scope+" · Resource: "+f.Resource, w)...)
	}
	if !f.ObservedAt.IsZero() {
		body = append(body, wrapPlain("Observed: "+f.ObservedAt.UTC().Format(time.RFC3339), w)...)
	}
	body = append(body, wrapPlain("Detail: "+f.Detail, w)...)
	if f.NextAction != "" {
		body = append(body, "")
		body = append(body, wrapPlain("Next: "+f.NextAction, w)...)
	}
	return body
}

func (m *model) clampDetailScroll() {
	if !m.detail {
		m.detailScroll = 0
		return
	}
	h := m.height
	if h < 8 {
		h = 8
	}
	visible := h - 7 // title, cluster, summary, status, spacer, spacer, help
	if visible < 1 {
		visible = 1
	}
	maxScroll := len(m.detailBody()) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.detailScroll > maxScroll {
		m.detailScroll = maxScroll
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
}

func (m *model) itemCount() int {
	if m.page == 1 {
		return len(m.report.Unresolved)
	}
	return len(m.report.Findings)
}

func (m *model) move(delta int) {
	if !m.done || m.err != nil || m.itemCount() == 0 {
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= m.itemCount() {
		m.selected = m.itemCount() - 1
	}
}

func (m *model) listHeight() int {
	n := m.height - 12
	if n < 1 {
		return 1
	}
	return n
}

func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func wrapPlain(s string, width int) []string {
	if width < 1 {
		return nil
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(clean(s)) {
		for ansi.StringWidth(word) > width {
			if line != "" {
				out = append(out, line)
				line = ""
			}
			part := ""
			for _, r := range word {
				if ansi.StringWidth(part+string(r)) > width {
					break
				}
				part += string(r)
			}
			if part == "" {
				part = string([]rune(word)[0])
			}
			out = append(out, part)
			word = strings.TrimPrefix(word, part)
		}
		if line == "" {
			line = word
		} else if ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width {
			line += " " + word
		} else {
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func (m *model) View() tea.View {
	w, h := m.width-2, m.height
	if w < 20 {
		w = 20
	}
	if h < 8 {
		h = 8
	}
	var lines []string
	lines = append(lines, clip(bttui.Title.Render("Guided setup checks"), w))
	if !m.done {
		spin := []string{"|", "/", "-", "\\"}
		lines = append(lines, "", clip(spin[m.frame%len(spin)]+" Checking release, workstation, and hosts…", w), "", clip("Press q to cancel. Host probes can take time.", w))
	} else if m.err != nil {
		lines = append(lines, "")
		box := bttui.ErrorBox.Width(w - 2).Render(clip(clean(m.err.Error()), w-6))
		lines = append(lines, strings.Split(box, "\n")...)
		lines = append(lines, "", "Press r to retry or q to exit.")
	} else {
		r := m.report
		lines = append(lines,
			clip(fmt.Sprintf("%s  •  %s", clean(r.Cluster), clean(r.Profile)), w),
			clip(fmt.Sprintf("%s  %d blocked  %d pending  %d passed", strings.ToUpper(r.State), r.BlockedCount, r.PendingCount, r.PassedCount), w),
		)
		if m.detail {
			f := r.Findings[m.selected]
			lines = append(lines, clip("["+clean(f.Status)+"] "+clean(f.ID), w), "")
			body := m.detailBody()
			visible := h - len(lines) - 2
			if visible < 1 {
				visible = 1
			}
			maxScroll := len(body) - visible
			if maxScroll < 0 {
				maxScroll = 0
			}
			scroll := m.detailScroll
			if scroll > maxScroll {
				scroll = maxScroll
			}
			end := scroll + visible
			if end > len(body) {
				end = len(body)
			}
			lines = append(lines, body[scroll:end]...)
			lines = append(lines, "", clip("↑/↓ scroll  Esc back  r rerun  q quit", w))
			v := tea.NewView(bttui.AppStyle.Render(strings.Join(lines, "\n")))
			v.AltScreen = true
			return v
		}
		lines = append(lines, clip(fmt.Sprintf("Findings (%d)   |   Open checks (%d)   [Tab to switch]", len(r.Findings), len(r.Unresolved)), w))
		if m.page == 1 {
			lines = append(lines, "OPEN CHECKS")
		} else {
			lines = append(lines, "FINDINGS")
		}
		count := m.itemCount()
		visible := m.listHeight()
		if visible > count {
			visible = count
		}
		start := m.selected - visible + 1
		if start < 0 {
			start = 0
		}
		if count == 0 {
			lines = append(lines, "No items.")
		}
		for i := start; i < start+visible; i++ {
			prefix := "  "
			if i == m.selected {
				prefix = "> "
			}
			if m.page == 1 {
				lines = append(lines, clip(prefix+clean(r.Unresolved[i]), w))
			} else {
				f := r.Findings[i]
				lines = append(lines, clip(fmt.Sprintf("%s%-14s %s", prefix, "["+clean(f.Status)+"]", clean(f.ID)), w))
			}
		}
		if m.page == 0 && count > 0 {
			f := r.Findings[m.selected]
			lines = append(lines, "", clip("Detail: "+clean(f.Detail), w))
			if f.NextAction != "" {
				lines = append(lines, clip("Next: "+clean(f.NextAction), w))
			}
		}
		lines = append(lines, "", clip("Read-only report. Installation is not authorized.", w))
		lines = append(lines, clip("↑/↓ move  Enter details  Tab switch  r rerun  q quit", w))
	}
	if len(lines) > h {
		lines = append(lines[:h-2], lines[len(lines)-2:]...)
	}
	for i := range lines {
		lines[i] = clip(lines[i], w)
	}
	v := tea.NewView(bttui.AppStyle.Render(strings.Join(lines, "\n")))
	v.AltScreen = true
	return v
}

var _ tea.Model = (*model)(nil)
