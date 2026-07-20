// Package installrun renders a live "install screen" for the mutating
// `kube-dc bootstrap init` apply: an alt-screen split with a milestone
// checklist on the left (✓ done / ⠋ running / ✗ failed / ◌ skipped /
// ○ pending) and a colorized, auto-following log pane on the right.
//
// It replaces the old behavior of streaming raw script output to stdout
// below whatever UI the operator was in. The engine (the cobra
// `runInit` apply path) is handed a log writer + a clusterinit.StepReporter;
// installrun marshals both onto the Bubble Tea program and draws them.
//
// Design mirrors the initform settings panel: Charm v2, `View() tea.View`
// with AltScreen, shared `bttui` styles.
package installrun

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	bootlog "github.com/shalb/kube-dc/cli/internal/bootstrap/log"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// EngineFunc runs the real install work. installrun.Run supplies the
// log writer + milestone reporter and renders their output. The engine
// runs in its own goroutine; when it returns, the screen shows the
// terminal result and waits for the operator to quit.
type EngineFunc func(ctx context.Context, out io.Writer, rep clusterinit.StepReporter) error

// Options configures RunWithOptions. Zero values are usable.
type Options struct {
	// RunName is included in the transcript filename. It is sanitized and
	// may be empty, in which case "bootstrap-init" is used.
	RunName string
	// LogDir overrides the transcript directory. Empty resolves to
	// $XDG_STATE_HOME/kube-dc/logs or ~/.kube-dc/logs.
	LogDir string
	// NowFn lets tests pin transcript filenames. nil -> time.Now.
	NowFn func() time.Time
}

// Result is returned even when the engine fails, so the caller can print
// the transcript path after Bubble Tea leaves the alt-screen.
type Result struct {
	LogPath string
}

// Run drives the install screen with default options.
func Run(ctx context.Context, engine EngineFunc) error {
	_, err := RunWithOptions(ctx, Options{}, engine)
	return err
}

// RunWithOptions drives the install screen. It builds a cancelable context
// (so q / ctrl+c requests abort), persists a redacted transcript, runs the
// Bubble Tea program, and returns the engine's error. Abort waits for the
// engine to observe cancellation and finish cleanup before the program exits.
func RunWithOptions(ctx context.Context, opts Options, engine EngineFunc) (*Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A transcript is a convenience — never block installing a cluster on
	// a log-file problem. If it can't be opened, degrade to no-persistence
	// (lineWriter tolerates a nil file) and surface the reason in the pane.
	logFile, logPath, terr := openTranscript(opts)
	var notice string
	if terr != nil {
		notice = fmt.Sprintf("[install] transcript disabled (%v) — continuing without a persisted log", terr)
		logFile, logPath = nil, ""
	}
	res := &Result{LogPath: logPath}
	if logFile != nil {
		writeTranscriptHeader(logFile, opts, logPath)
	}

	m := &model{
		sub:     make(chan tea.Msg, 1024),
		engine:  engine,
		ctx:     ctx,
		cancel:  cancel,
		byID:    map[clusterinit.StepID]int{},
		vp:      viewport.New(),
		follow:  true,
		logPath: logPath,
		logFile: logFile,
	}
	if notice != "" {
		m.logs = append(m.logs, notice)
	}
	final, runErr := tea.NewProgram(m).Run()
	fm, _ := final.(*model)

	// On force-quit (operator pressed q twice during abort) the engine
	// goroutine may still be writing to the transcript; skip Close() so we
	// don't race that write — the process exits momentarily and the OS
	// reaps the fd.
	forceQuit := fm != nil && fm.forceQuit
	var closeErr error
	if logFile != nil && !forceQuit {
		closeErr = logFile.Close()
	}

	switch {
	case runErr != nil:
		cancel()
		return res, runErr
	case fm == nil:
		return res, closeErr
	case fm.aborting: // covers force-quit (aborting is always set first)
		return res, ErrAborted
	case fm.runErr != nil:
		return res, fm.runErr
	default:
		return res, closeErr
	}
}

// ErrAborted is returned when the operator requested abort. The screen
// waits for engine cleanup before returning it.
var ErrAborted = fmt.Errorf("install aborted by operator")

// --- engine → UI events (delivered over the sub channel) ---

type planMsg struct{ steps []clusterinit.Step }
type startMsg struct{ id clusterinit.StepID }
type doneMsg struct {
	id  clusterinit.StepID
	err error
}
type skipMsg struct {
	id     clusterinit.StepID
	reason string
}
type logMsg struct{ line string }
type finishedMsg struct{ err error }
type tickMsg struct{}

const spinnerInterval = 120 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	colDone = lipgloss.Color("#2F9E72")
	colRun  = lipgloss.Color("#5794F2")
	colFail = lipgloss.Color("#E0524A")
	colSkip = lipgloss.Color("#B0A030")
)

type stepStatus int

const (
	stPending stepStatus = iota
	stRunning
	stDone
	stFailed
	stSkipped
)

type stepState struct {
	step   clusterinit.Step
	status stepStatus
	note   string // failure summary / skip reason
}

type model struct {
	sub    chan tea.Msg
	engine EngineFunc
	ctx    context.Context
	cancel context.CancelFunc

	steps []stepState
	byID  map[clusterinit.StepID]int
	logs  []string
	vp    viewport.Model

	width, height int
	spin          int
	follow        bool // auto-scroll the log pane to the newest line
	finished      bool
	runErr        error
	quitting      bool
	aborting      bool
	forceQuit     bool // operator pressed q twice during abort — don't wait for the engine
	logPath       string
	logFile       *os.File
}

// --- reporter + writer (engine side; send onto the sub channel) ---

type reporter struct {
	ctx context.Context
	sub chan tea.Msg
}

func (r reporter) Plan(s []clusterinit.Step)             { sendMsg(r.ctx, r.sub, planMsg{s}) }
func (r reporter) Start(id clusterinit.StepID)           { sendMsg(r.ctx, r.sub, startMsg{id}) }
func (r reporter) Done(id clusterinit.StepID, err error) { sendMsg(r.ctx, r.sub, doneMsg{id, err}) }
func (r reporter) Skip(id clusterinit.StepID, reason string) {
	sendMsg(r.ctx, r.sub, skipMsg{id, reason})
}

// lineWriter buffers partial writes, redacts every complete line, and
// tees the transcript to both the TUI and the persistent log file.
type lineWriter struct {
	ctx  context.Context
	sub  chan tea.Msg
	file io.Writer
	mu   sync.Mutex
	buf  strings.Builder
	err  error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return len(p), w.err
	}
	w.buf.Write(p)
	s := w.buf.String()
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		w.emitLocked(strings.TrimRight(s[:i], "\r"))
		s = s[i+1:]
	}
	w.buf.Reset()
	w.buf.WriteString(s)
	return len(p), w.err
}

func (w *lineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.emitLocked(strings.TrimRight(w.buf.String(), "\r"))
		w.buf.Reset()
	}
	return w.err
}

func (w *lineWriter) emitLocked(line string) {
	line = bootlog.RedactStreamLine(line)
	if w.file != nil && w.err == nil {
		if _, err := fmt.Fprintln(w.file, line); err != nil {
			w.err = err
		}
	}
	sendMsg(w.ctx, w.sub, logMsg{line: line})
}

func sendMsg(ctx context.Context, sub chan<- tea.Msg, msg tea.Msg) {
	select {
	case sub <- msg:
	case <-ctx.Done():
	}
}

// --- bubbletea model ---

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.startEngine(), waitForMsg(m.sub), tickCmd())
}

func (m *model) startEngine() tea.Cmd {
	return func() tea.Msg {
		go func() {
			lw := &lineWriter{ctx: m.ctx, sub: m.sub, file: m.logFile}
			err := m.engine(m.ctx, lw, reporter{ctx: m.ctx, sub: m.sub})
			if flushErr := lw.Flush(); err == nil && flushErr != nil {
				err = fmt.Errorf("write install log: %w", flushErr)
			}
			m.sub <- finishedMsg{err: err}
		}()
		return nil
	}
}

func waitForMsg(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-sub }
}

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.finished {
				m.quitting = true
				return m, tea.Quit
			}
			if !m.aborting {
				m.aborting = true
				m.cancel() // signal the engine to stop, then wait for cleanup
				m.logs = append(m.logs, "[install] abort requested — waiting for cleanup/rollback (press q again to force-quit)")
				return m, nil
			}
			// Second press while still aborting: operator override — force
			// quit without waiting for the engine. ctx is already canceled
			// so the engine tears down; we just stop rendering.
			m.forceQuit = true
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			m.vp.ScrollUp(1)
			m.follow = m.vp.AtBottom()
			return m, nil
		case "down", "j":
			m.vp.ScrollDown(1)
			m.follow = m.vp.AtBottom()
			return m, nil
		case "g":
			m.vp.GotoTop()
			m.follow = false
			return m, nil
		case "G":
			m.follow = true
			return m, nil
		case "pgup", "b":
			m.vp.ScrollUp(max(1, m.vp.Height()/2))
			m.follow = m.vp.AtBottom()
			return m, nil
		case "pgdown", "f":
			m.vp.ScrollDown(max(1, m.vp.Height()/2))
			m.follow = m.vp.AtBottom()
			return m, nil
		}
		return m, nil

	case tickMsg:
		if m.finished {
			return m, nil // stop animating once done
		}
		m.spin = (m.spin + 1) % len(spinnerFrames)
		return m, tickCmd()

	case planMsg:
		m.setPlan(msg.steps)
		return m, waitForMsg(m.sub)
	case startMsg:
		m.setStatus(msg.id, stRunning, "")
		return m, waitForMsg(m.sub)
	case doneMsg:
		if msg.err != nil {
			m.setStatus(msg.id, stFailed, summarize(msg.err.Error()))
		} else {
			m.setStatus(msg.id, stDone, "")
		}
		return m, waitForMsg(m.sub)
	case skipMsg:
		m.setStatus(msg.id, stSkipped, summarize(msg.reason))
		return m, waitForMsg(m.sub)
	case logMsg:
		m.logs = append(m.logs, msg.line)
		return m, waitForMsg(m.sub)
	case finishedMsg:
		m.finished = true
		m.runErr = msg.err
		if m.aborting && m.runErr == nil {
			m.runErr = ErrAborted
		}
		m.finishRunning()
		// Stop draining — all prior events are FIFO-delivered before finishedMsg.
		return m, nil
	}
	return m, nil
}

func (m *model) setPlan(steps []clusterinit.Step) {
	m.steps = m.steps[:0]
	m.byID = map[clusterinit.StepID]int{}
	for _, s := range steps {
		m.byID[s.ID] = len(m.steps)
		m.steps = append(m.steps, stepState{step: s, status: stPending})
	}
}

func (m *model) setStatus(id clusterinit.StepID, st stepStatus, note string) {
	i, ok := m.byID[id]
	if !ok {
		return
	}
	m.steps[i].status = st
	if note != "" {
		m.steps[i].note = note
	}
}

func (m *model) finishRunning() {
	for i := range m.steps {
		if m.steps[i].status != stRunning {
			continue
		}
		if m.aborting {
			m.steps[i].status = stSkipped
			m.steps[i].note = "aborted by operator"
		} else if m.runErr != nil {
			m.steps[i].status = stFailed
			m.steps[i].note = summarize(m.runErr.Error())
		}
	}
}

// --- view ---

func (m *model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return frame("Starting install…")
	}
	w := m.width - 2

	title := joinSpaced(w,
		bttui.Title.Render(" Kube-DC — Installing ")+"  "+bttui.Muted.Render(m.headline()),
		m.statusPill())

	footer := bttui.Muted.Render(m.footerText())
	footerH := lipgloss.Height(footer)

	bodyH := m.height - 1 - footerH
	if bodyH < 6 {
		bodyH = 6
	}

	leftW := 34
	if leftW > w/2 {
		leftW = w / 2
	}
	rightW := w - leftW - 1
	if rightW < 20 {
		rightW = 20
	}

	left := bttui.ListPane.Width(leftW).Height(bodyH - 2).Render(m.renderMilestones(leftW - 4))

	// Log pane: colorized, auto-following viewport.
	m.vp.SetWidth(rightW - 2 /*border*/ - 2 /*pad*/)
	m.vp.SetHeight(bodyH - 2)
	m.vp.SetContent(m.renderLogs(rightW - 4))
	if m.follow {
		m.vp.GotoBottom()
	}
	right := bttui.DetailsPane.Width(rightW).Height(bodyH - 2).Render(m.vp.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return frame(lipgloss.JoinVertical(lipgloss.Left, title, body, footer))
}

func frame(content string) tea.View {
	v := tea.NewView(bttui.AppStyle.Render(content))
	v.AltScreen = true
	return v
}

func (m *model) renderMilestones(maxW int) string {
	if len(m.steps) == 0 {
		return bttui.Muted.Render("resolving plan…")
	}
	var b strings.Builder
	for i, s := range m.steps {
		icon, style := m.icon(s.status)
		b.WriteString(icon + " " + style.Render(truncate(s.step.Title, maxW-2)))
		if s.note != "" && (s.status == stFailed || s.status == stSkipped) {
			b.WriteString("\n   " + bttui.Muted.Render(truncate(s.note, maxW-3)))
		}
		if i < len(m.steps)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *model) icon(st stepStatus) (string, lipgloss.Style) {
	switch st {
	case stRunning:
		s := lipgloss.NewStyle().Foreground(colRun)
		return s.Render(spinnerFrames[m.spin]), s
	case stDone:
		s := lipgloss.NewStyle().Foreground(colDone)
		return s.Render("✓"), lipgloss.NewStyle()
	case stFailed:
		s := lipgloss.NewStyle().Foreground(colFail)
		return s.Render("✗"), s
	case stSkipped:
		s := lipgloss.NewStyle().Foreground(colSkip)
		return s.Render("◌"), bttui.Muted
	default:
		return bttui.Muted.Render("○"), bttui.Muted
	}
}

func (m *model) renderLogs(maxW int) string {
	if len(m.logs) == 0 {
		return bttui.Muted.Render("(waiting for output…)")
	}
	lines := make([]string, len(m.logs))
	for i, ln := range m.logs {
		lines[i] = colorizeLog(ln, maxW)
	}
	return strings.Join(lines, "\n")
}

func colorizeLog(line string, maxW int) string {
	line = truncate(line, maxW)
	switch {
	case strings.Contains(line, "stderr]") || strings.Contains(strings.ToLower(line), "error"):
		return lipgloss.NewStyle().Foreground(colFail).Render(line)
	case strings.Contains(strings.ToLower(line), "warning") || strings.Contains(line, "deferred"):
		return lipgloss.NewStyle().Foreground(colSkip).Render(line)
	case strings.HasPrefix(line, "==="):
		return lipgloss.NewStyle().Bold(true).Render(line)
	case strings.HasPrefix(line, "[apply]") || strings.HasPrefix(line, "[finalize]"):
		return lipgloss.NewStyle().Foreground(colRun).Render(line)
	default:
		return line
	}
}

func (m *model) headline() string {
	done, total := m.progress()
	return fmt.Sprintf("%d/%d steps", done, total)
}

func (m *model) statusPill() string {
	switch {
	case m.finished && m.aborting:
		return lipgloss.NewStyle().Foreground(colSkip).Render("◌ aborted")
	case m.finished && m.runErr != nil:
		return lipgloss.NewStyle().Foreground(colFail).Render("✗ failed")
	case m.finished:
		return lipgloss.NewStyle().Foreground(colDone).Render("✓ complete")
	case m.aborting:
		return lipgloss.NewStyle().Foreground(colSkip).Render("◌ aborting")
	default:
		return lipgloss.NewStyle().Foreground(colRun).Render(spinnerFrames[m.spin] + " installing")
	}
}

func (m *model) footerText() string {
	logHint := ""
	if m.logPath != "" {
		logHint = " · log " + m.logPath
	}
	if m.finished {
		if m.aborting {
			return "◌ install aborted after cleanup — scroll ↑↓ to review · press q to exit" + logHint
		}
		if m.runErr != nil {
			return "✗ install failed — fix the failed step and rerun the same command (resumes) · press q to exit" + logHint
		}
		return "✓ install complete — scroll ↑↓ to review · press q to exit" + logHint
	}
	if m.aborting {
		return "abort requested · waiting for cleanup/rollback · press q again to force-quit" + logHint
	}
	done, total := m.progress()
	return fmt.Sprintf("%d/%d done · ↑↓ scroll · pgup/pgdn page · G follow · q abort%s", done, total, logHint)
}

func (m *model) progress() (done, total int) {
	total = len(m.steps)
	for _, s := range m.steps {
		if s.status == stDone || s.status == stSkipped {
			done++
		}
	}
	return done, total
}

// --- helpers ---

func openTranscript(opts Options) (*os.File, string, error) {
	dir := opts.LogDir
	if dir == "" {
		dir = defaultLogDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create install log dir %s: %w", dir, err)
	}
	now := time.Now
	if opts.NowFn != nil {
		now = opts.NowFn
	}
	name := sanitizeRunName(opts.RunName)
	filename := fmt.Sprintf("kube-dc-%s-%s.log", name, now().UTC().Format("20060102T150405.000000000Z"))
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open install log %s: %w", path, err)
	}
	return f, path, nil
}

func writeTranscriptHeader(w io.Writer, opts Options, path string) {
	fmt.Fprintln(w, "# kube-dc bootstrap init install transcript")
	fmt.Fprintf(w, "# run: %s\n", sanitizeRunName(opts.RunName))
	fmt.Fprintf(w, "# path: %s\n", path)
	fmt.Fprintln(w, "# note: stream lines are redacted before they are written here")
}

func defaultLogDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "kube-dc", "logs")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".kube-dc", "logs")
	}
	return filepath.Join(os.TempDir(), "kube-dc", "logs")
}

func sanitizeRunName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "bootstrap-init"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = r == '-'
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "bootstrap-init"
	}
	return out
}

func truncate(s string, max int) string {
	if max <= 1 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	if max < 2 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func summarize(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	return truncate(s, 120)
}

// joinSpaced left-justifies a and right-justifies b within width w.
func joinSpaced(w int, a, b string) string {
	gap := w - lipgloss.Width(a) - lipgloss.Width(b)
	if gap < 1 {
		gap = 1
	}
	return a + strings.Repeat(" ", gap) + b
}

// compile-time guard: reporter satisfies clusterinit.StepReporter.
var _ clusterinit.StepReporter = reporter{}
var _ color.Color = colDone
