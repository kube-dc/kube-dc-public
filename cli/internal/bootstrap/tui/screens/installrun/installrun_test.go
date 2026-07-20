package installrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	bootlog "github.com/shalb/kube-dc/cli/internal/bootstrap/log"
)

// newTestModel builds a model wired to the given engine, for teatest.
func newTestModel(engine EngineFunc) *model {
	ctx, cancel := context.WithCancel(context.Background())
	return &model{
		sub:    make(chan tea.Msg, 1024),
		engine: engine,
		ctx:    ctx,
		cancel: cancel,
		byID:   map[clusterinit.StepID]int{},
		vp:     viewport.New(),
		follow: true,
	}
}

func TestInstallRun_RendersMilestonesLogsAndCompletion(t *testing.T) {
	engine := func(ctx context.Context, out io.Writer, rep clusterinit.StepReporter) error {
		rep.Plan([]clusterinit.Step{
			{ID: clusterinit.StepScaffold, Title: "Scaffold cluster overlay"},
			{ID: clusterinit.StepFluxInstall, Title: "Bootstrap Flux"},
			{ID: clusterinit.StepKeycloakOIDC, Title: "Configure Keycloak OIDC"},
		})
		rep.Start(clusterinit.StepScaffold)
		fmt.Fprintln(out, "[apply] scaffolding clusters/e2e")
		rep.Done(clusterinit.StepScaffold, nil)

		rep.Start(clusterinit.StepFluxInstall)
		fmt.Fprintln(out, "[flux-install stdout] bootstrapping flux")
		rep.Done(clusterinit.StepFluxInstall, nil)

		// A skipped finalize step (e.g. --no-push style) exercises the
		// skip rendering + note.
		rep.Skip(clusterinit.StepKeycloakOIDC, "deferred: platform still reconciling")
		return nil
	}

	tm := teatest.NewTestModel(t, newTestModel(engine),
		teatest.WithInitialTermSize(140, 40),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Scaffold cluster overlay")) &&
			bytes.Contains(b, []byte("Bootstrap Flux")) &&
			bytes.Contains(b, []byte("Configure Keycloak OIDC")) &&
			bytes.Contains(b, []byte("scaffolding clusters/e2e")) &&
			bytes.Contains(b, []byte("deferred: platform"))
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestInstallRun_RendersFailure(t *testing.T) {
	engine := func(ctx context.Context, out io.Writer, rep clusterinit.StepReporter) error {
		rep.Plan([]clusterinit.Step{{ID: clusterinit.StepScaffold, Title: "Scaffold cluster overlay"}})
		rep.Start(clusterinit.StepScaffold)
		fmt.Fprintln(out, "[flux-install stderr] boom")
		err := errors.New("scaffold blew up")
		rep.Done(clusterinit.StepScaffold, err)
		return err
	}

	tm := teatest.NewTestModel(t, newTestModel(engine),
		teatest.WithInitialTermSize(120, 30),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("install failed")) &&
			bytes.Contains(b, []byte("scaffold blew up"))
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestAbortWaitsForEngineCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &model{
		ctx:    ctx,
		cancel: cancel,
		steps: []stepState{{
			step:   clusterinit.Step{ID: clusterinit.StepFluxInstall, Title: "Bootstrap Flux"},
			status: stRunning,
		}},
		byID: map[clusterinit.StepID]int{clusterinit.StepFluxInstall: 0},
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil {
		t.Fatalf("abort returned a quit cmd before cleanup finished")
	}
	got := updated.(*model)
	if !got.aborting || got.finished || got.quitting {
		t.Fatalf("abort state = aborting %v finished %v quitting %v", got.aborting, got.finished, got.quitting)
	}
	if ctx.Err() == nil {
		t.Fatalf("abort did not cancel the engine context")
	}
	if len(got.logs) != 1 || !strings.Contains(got.logs[0], "waiting for cleanup") {
		t.Fatalf("abort log = %#v, want cleanup hint", got.logs)
	}

	updated, cmd = got.Update(finishedMsg{err: context.Canceled})
	if cmd != nil {
		t.Fatalf("finished abort returned unexpected cmd")
	}
	got = updated.(*model)
	if !got.finished || !got.aborting {
		t.Fatalf("finished abort state = finished %v aborting %v", got.finished, got.aborting)
	}
	if got.steps[0].status != stSkipped || !strings.Contains(got.steps[0].note, "aborted") {
		t.Fatalf("running step after abort = status %v note %q", got.steps[0].status, got.steps[0].note)
	}

	updated, cmd = got.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	got = updated.(*model)
	if !got.quitting || cmd == nil {
		t.Fatalf("q after cleanup should quit: quitting %v cmd nil %v", got.quitting, cmd == nil)
	}
}

func TestFooterText_CompletionFailureAndAbort(t *testing.T) {
	tests := []struct {
		name string
		m    model
		want string
	}{
		{name: "complete", m: model{finished: true, logPath: "/tmp/install.log"}, want: "install complete"},
		{name: "failed", m: model{finished: true, runErr: errors.New("boom"), logPath: "/tmp/install.log"}, want: "rerun the same command"},
		{name: "aborting", m: model{aborting: true, logPath: "/tmp/install.log"}, want: "waiting for cleanup"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.m.footerText()
			if !strings.Contains(got, tc.want) || !strings.Contains(got, tc.m.logPath) {
				t.Fatalf("footerText() = %q, want %q and log path", got, tc.want)
			}
		})
	}
}

func TestLineWriter_RedactsAndFlushesTranscript(t *testing.T) {
	path := t.TempDir() + "/install.log"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan tea.Msg, 4)
	lw := &lineWriter{ctx: context.Background(), sub: ch, file: f}

	secret := "password: QWxhZGRpbjpvcGVuIHNlc2FtZQ1234567890"
	if _, err := lw.Write([]byte(secret + "\npartial line without newline")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := lw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "QWxhZGRpbjpvcGVu") {
		t.Fatalf("transcript leaked secret-ish base64: %s", text)
	}
	if !strings.Contains(text, bootlog.RedactedMarker) {
		t.Fatalf("transcript missing redaction marker: %s", text)
	}
	if !strings.Contains(text, "partial line without newline") {
		t.Fatalf("Flush did not persist partial line: %s", text)
	}

	var ui []string
	for len(ch) > 0 {
		msg := (<-ch).(logMsg)
		ui = append(ui, msg.line)
	}
	joined := strings.Join(ui, "\n")
	if strings.Contains(joined, "QWxhZGRpbjpvcGVu") || !strings.Contains(joined, bootlog.RedactedMarker) {
		t.Fatalf("viewport lines not redacted consistently: %s", joined)
	}
}

func TestTranscriptPathHelpers(t *testing.T) {
	if got := sanitizeRunName("  EU/DC1 install!  "); got != "eu-dc1-install" {
		t.Fatalf("sanitizeRunName = %q", got)
	}
	f, path, err := openTranscript(Options{
		RunName: "EU/DC1",
		LogDir:  t.TempDir(),
		NowFn:   func() time.Time { return time.Date(2026, 7, 7, 1, 2, 3, 4, time.UTC) },
	})
	if err != nil {
		t.Fatalf("openTranscript: %v", err)
	}
	_ = f.Close()
	if !strings.Contains(path, "kube-dc-eu-dc1-20260707T010203.000000004Z.log") {
		t.Fatalf("unexpected transcript path: %s", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("transcript mode = %v, want 0600", st.Mode().Perm())
	}
}

// TestAbortForceQuitOnSecondPress covers the escape hatch: the first
// q/ctrl+c requests a graceful abort (waits for cleanup); a second press
// while still aborting force-quits without waiting for the engine.
func TestAbortForceQuitOnSecondPress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &model{ctx: ctx, cancel: cancel, byID: map[clusterinit.StepID]int{}}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = updated.(*model)
	if !m.aborting || m.forceQuit || m.quitting || cmd != nil {
		t.Fatalf("first press: aborting=%v forceQuit=%v quitting=%v cmd nil=%v",
			m.aborting, m.forceQuit, m.quitting, cmd == nil)
	}

	updated, cmd = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = updated.(*model)
	if !m.forceQuit || !m.quitting || cmd == nil {
		t.Fatalf("second press should force-quit: forceQuit=%v quitting=%v cmd nil=%v",
			m.forceQuit, m.quitting, cmd == nil)
	}
}

// TestLineWriter_NilFileTolerated proves the degraded (no-transcript)
// path: a nil file must not panic and still feeds the viewport.
func TestLineWriter_NilFileTolerated(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	lw := &lineWriter{ctx: context.Background(), sub: ch, file: nil}
	if _, err := lw.Write([]byte("hello world\n")); err != nil {
		t.Fatalf("Write with nil file: %v", err)
	}
	if err := lw.Flush(); err != nil {
		t.Fatalf("Flush with nil file: %v", err)
	}
	select {
	case msg := <-ch:
		if _, ok := msg.(logMsg); !ok {
			t.Fatalf("expected logMsg, got %T", msg)
		}
	default:
		t.Fatal("expected a logMsg on the channel even without a transcript file")
	}
}
