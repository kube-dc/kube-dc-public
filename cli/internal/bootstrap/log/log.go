// Package log is the structured logging spine for `kube-dc bootstrap`.
//
// Why a wrapper around log/slog instead of using slog directly:
//
//   - The 5-layer redaction model (see redact.go) is a structural
//     guarantee: secret material captured by ScriptRunner — OpenBao
//     shares, root token, GitHub PAT, age secret-key, sealed JSON
//     payloads — must NEVER appear in the log file or TUI viewport raw.
//     A redacting slog.Handler is the only place we can enforce that
//     uniformly; callers can't be trusted to remember per-call.
//   - The TUI's log-viewport (M7 phase-5 waterfall) needs a live tee
//     of what just got written. slog.Logger has no built-in fan-out;
//     this wrapper exposes a `Tee() <-chan string` that the TUI binds
//     to without affecting the file sink.
//   - The bootstrap CLI may run in three output modes — TTY (text), no-
//     TTY pipe (JSON), and TUI (JSON file + tee channel only, no
//     stdout). Centralising mode selection here keeps every command
//     consistent.
//
// File format: one log file per invocation, written to
// `$XDG_STATE_HOME/kube-dc/logs/` if set, otherwise
// `~/.kube-dc/logs/`. Filename pattern is
// `kube-dc-<sanitized-RFC3339>.log` (colons replaced with `-` so the
// path is also Windows-safe). Always JSON regardless of stdout mode —
// the file is for grep / jq / support escalation.
//
// See agent rule 7 of `docs/prd/installer-agentic-implementation-plan.md`
// and M0-T05 in the same plan for the contract this package satisfies.
package log

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// CorrelationIDKey is the slog attribute key carrying the per-invocation
// UUIDv4 stamped on every line. Exported so downstream code that builds
// its own slog.Handler (tests, debug helpers) can stay consistent.
const CorrelationIDKey = "correlation_id"

// RedactedMarker is the literal string substituted for any value the
// redaction engine has masked. Exported so tests in this package and
// in consuming packages can assert on it without re-declaring the
// constant.
const RedactedMarker = "[REDACTED]"

// teeBufferLines is the capacity of the TUI tee channel. Beyond this,
// the writer drops the oldest line to make room — the TUI is a
// best-effort viewer, not an authoritative log sink (the file is).
const teeBufferLines = 256

// Options configures a Logger. Zero values pick sensible defaults; see
// each field's doc-comment.
type Options struct {
	// Dir is the directory log files land in. Empty -> resolves to
	// `$XDG_STATE_HOME/kube-dc/logs/` or `~/.kube-dc/logs/`.
	Dir string

	// Verbose lifts the level threshold from Info to Debug.
	Verbose bool

	// Stdout selects the stdout sink. nil -> defaults to os.Stdout. Set
	// to io.Discard in TUI mode (the TUI owns the screen; raw stdout
	// would corrupt it).
	Stdout io.Writer

	// ForceJSON forces JSON output on stdout regardless of TTY
	// detection. Used by `--log-json` flags. ForceText is the inverse.
	ForceJSON bool
	ForceText bool

	// NowFn lets tests inject a deterministic timestamp for filename
	// generation. nil -> time.Now.
	NowFn func() time.Time

	// CorrelationID overrides the auto-generated UUIDv4. Tests use this
	// to assert on a fixed value; production callers leave it empty.
	CorrelationID string
}

// Logger is the wrapper. Construct via New(opts); close via Close().
// Concurrent use is safe (slog.Logger is safe; the tee channel sender
// holds an internal mutex around line splitting).
type Logger struct {
	slog          *slog.Logger
	correlationID string

	file *os.File
	tee  *teeWriter
}

// New constructs a Logger. Returns an error if the log directory can't
// be created or the file can't be opened. On error the returned Logger
// is nil — callers should fall back to slog.Default() with no
// redaction if they want to keep running. (The bootstrap CLI does not:
// it exits non-zero if logging can't start, because every downstream
// step assumes a sink.)
func New(opts Options) (*Logger, error) {
	now := time.Now
	if opts.NowFn != nil {
		now = opts.NowFn
	}

	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = defaultLogDir()
		if err != nil {
			return nil, fmt.Errorf("resolve log dir: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}

	// Generate the correlation ID up front so the log filename can
	// include a short prefix — guarantees a fresh file per invocation
	// even when two CLI runs land in the same wall-clock second
	// (RFC3339 only resolves to seconds; PID alone would let multiple
	// threads within one process collide, and O_EXCL retry just adds
	// race surface).
	corrID := opts.CorrelationID
	if corrID == "" {
		corrID = newCorrelationID()
	}

	filename := fmt.Sprintf("kube-dc-%s-%s.log", sanitizeRFC3339(now()), shortCorrelationID(corrID))
	path := filepath.Join(dir, filename)
	// O_EXCL guards against the (vanishingly unlikely) UUIDv4 collision
	// within the same second. On collision we surface the error rather
	// than silently appending — the file is supposed to be a 1:1 record
	// of this invocation.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}

	tee := newTeeWriter()

	// File sink: always JSON, includes the correlation ID + redaction.
	fileHandler := slog.NewJSONHandler(io.MultiWriter(f, tee), &slog.HandlerOptions{
		Level:     levelFor(opts.Verbose),
		AddSource: false,
	})

	// Stdout sink: text on TTY, JSON when piped or when explicitly
	// forced. TUI mode passes Stdout=io.Discard so this becomes a
	// silent passthrough.
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	var stdoutHandler slog.Handler
	switch {
	case opts.ForceJSON:
		stdoutHandler = slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: levelFor(opts.Verbose)})
	case opts.ForceText:
		stdoutHandler = slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: levelFor(opts.Verbose)})
	case stdout == io.Discard:
		stdoutHandler = slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: levelFor(opts.Verbose)})
	case isTerminal(stdout):
		stdoutHandler = slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: levelFor(opts.Verbose)})
	default:
		stdoutHandler = slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: levelFor(opts.Verbose)})
	}

	// Wrap the multiplexed handler in the redacting layer so layers 1+2+
	// map-walk happen once, regardless of sink.
	inner := &fanoutHandler{handlers: []slog.Handler{fileHandler, stdoutHandler}}
	redacted := newRedactingHandler(inner)

	lg := slog.New(redacted).With(slog.String(CorrelationIDKey, corrID))

	return &Logger{
		slog:          lg,
		correlationID: corrID,
		file:          f,
		tee:           tee,
	}, nil
}

// Close flushes the file sink and shuts down the tee channel. Idempotent.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	if l.tee != nil {
		l.tee.close()
	}
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// Debug emits at slog.LevelDebug. msg is the format-free message; args
// are alternating key/value pairs per slog convention.
func (l *Logger) Debug(msg string, args ...any) { l.slog.Debug(msg, args...) }

// Info emits at slog.LevelInfo.
func (l *Logger) Info(msg string, args ...any) { l.slog.Info(msg, args...) }

// Warn emits at slog.LevelWarn.
func (l *Logger) Warn(msg string, args ...any) { l.slog.Warn(msg, args...) }

// Error emits at slog.LevelError.
func (l *Logger) Error(msg string, args ...any) { l.slog.Error(msg, args...) }

// With returns a child logger that injects additional structured fields
// into every record. Fields are redacted at construction time (layer 1)
// so a `With("token", "xyz")` correctly logs `token=[REDACTED]`.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		slog:          l.slog.With(args...),
		correlationID: l.correlationID,
		file:          l.file,
		tee:           l.tee,
	}
}

// CorrelationID returns the UUIDv4 stamped on every line of this
// Logger's output. Surfaced for support-bundle generation ("share
// correlation ID 1a2b3c… when filing a ticket").
func (l *Logger) CorrelationID() string { return l.correlationID }

// Tee returns the channel that emits every formatted line as it's
// written. The TUI's log-viewport drains this and renders the tail.
// Channel is closed by Close(). Slow consumers cause the OLDEST
// buffered line to be dropped (the file always retains everything; the
// tee is just a viewer hint).
func (l *Logger) Tee() <-chan string { return l.tee.ch }

// ---------- helpers ----------

func levelFor(verbose bool) slog.Level {
	if verbose {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func defaultLogDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "kube-dc", "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube-dc", "logs"), nil
}

// sanitizeRFC3339 replaces ':' with '-' so the filename is portable
// (Windows doesn't allow ':' in paths) and grep-friendly.
func sanitizeRFC3339(t time.Time) string {
	return strings.ReplaceAll(t.UTC().Format(time.RFC3339), ":", "-")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// shortCorrelationID returns the first hex segment of a UUIDv4 (8
// chars before the first dash). Used in the log filename to guarantee
// uniqueness across two same-second invocations without exposing the
// full ID in filesystem listings — the full ID is in every log
// record, so support can correlate without scanning filenames.
func shortCorrelationID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// newCorrelationID generates a UUIDv4 from crypto/rand. Returns a
// stable zero-UUID if rand.Read fails (effectively never on a healthy
// host; the fallback exists so a logger never panics on init).
func newCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------- fanoutHandler ----------

// fanoutHandler dispatches each record to every wrapped handler. It
// lives in this package (rather than reaching for slogmulti or
// log/slog/multi) to keep the dep surface zero.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, sub := range h.handlers {
		if sub.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, sub := range h.handlers {
		if !sub.Enabled(ctx, r.Level) {
			continue
		}
		if err := sub.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	subs := make([]slog.Handler, len(h.handlers))
	for i, sub := range h.handlers {
		subs[i] = sub.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: subs}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	subs := make([]slog.Handler, len(h.handlers))
	for i, sub := range h.handlers {
		subs[i] = sub.WithGroup(name)
	}
	return &fanoutHandler{handlers: subs}
}

// ---------- teeWriter ----------

// teeWriter splits incoming writes on '\n' and pushes each completed
// line into ch. Used as one of the io.Writer fan-out targets for the
// file handler.
//
// Drop-oldest semantics: when ch is full (TUI not draining fast
// enough), one buffered line is discarded to make room. This keeps
// `Write` non-blocking — slog inside a controller reconcile loop must
// never stall on a stuck viewport.
type teeWriter struct {
	ch     chan string
	mu     sync.Mutex
	pend   strings.Builder
	closed bool
}

func newTeeWriter() *teeWriter {
	return &teeWriter{ch: make(chan string, teeBufferLines)}
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return len(p), nil
	}
	t.pend.Write(p)
	buf := t.pend.String()
	for {
		i := strings.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		line := buf[:i]
		buf = buf[i+1:]
		select {
		case t.ch <- line:
		default:
			select {
			case <-t.ch:
			default:
			}
			select {
			case t.ch <- line:
			default:
			}
		}
	}
	t.pend.Reset()
	t.pend.WriteString(buf)
	return len(p), nil
}

func (t *teeWriter) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	close(t.ch)
}
