package setupcheck

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func sampleReport() setup.ReadinessReport {
	return setup.ReadinessReport{
		State: "blocked", Cluster: "demo", Profile: "evaluation@v1", BlockedCount: 1, PassedCount: 1,
		Findings: []setup.ReadinessFinding{
			{ID: "host.server-1.disk", Status: "blocked", Detail: "The selected disk has an active holder.", NextAction: "Stop the holder and select a stable disk identifier. Then repeat the host inventory before installation."},
			{ID: "release.kind", Status: "pass", Detail: "The record uses the supported schema."},
		},
		Unresolved: []string{"exact-effects-diff", "live-target-identity"},
	}
}

func TestReadinessScreenUsesSuppliedProbeAndKeepsActionsReachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	m := &model{ctx: ctx, cancel: cancel, width: 80, height: 24, probe: func(context.Context) (setup.ReadinessReport, error) {
		calls++
		return sampleReport(), nil
	}}
	msg := m.probeCmd()()
	m.Update(msg)
	if calls != 1 || !m.done || m.err != nil {
		t.Fatalf("probe result not used: calls=%d done=%v err=%v", calls, m.done, m.err)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 80, Height: 24}, {Width: 120, Height: 35}} {
		m.Update(size)
		view := m.View()
		if !view.AltScreen || !strings.Contains(ansi.Strip(view.Content), "host.server-1.disk") {
			t.Fatalf("missing readable finding at %dx%d: %q", size.Width, size.Height, view.Content)
		}
		for _, line := range strings.Split(view.Content, "\n") {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("line exceeds %d columns: %q", size.Width, line)
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.detail {
		t.Fatal("Enter did not open finding detail")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "active holder") {
		t.Fatalf("detail not visible: %q", m.View().Content)
	}
	for i := 0; i < 10; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	bottom := len(m.detailBody()) - (m.height - 7)
	if m.detailScroll != bottom {
		t.Fatalf("detail scroll exceeded bottom: %d", m.detailScroll)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "the host inventory before") {
		t.Fatalf("full next action not reachable: %q", m.View().Content)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.detailScroll != bottom-1 {
		t.Fatalf("one Up did not scroll back from bottom: %d", m.detailScroll)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.detail || m.page != 1 || !strings.Contains(ansi.Strip(m.View().Content), "exact-effects-diff") {
		t.Fatalf("open checks not reachable: %q", m.View().Content)
	}
	m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.done {
		t.Fatal("retry did not return to loading state")
	}
	m.Update(m.probeCmd()())
	if calls != 2 || !m.done {
		t.Fatalf("retry did not run supplied probe: calls=%d done=%v", calls, m.done)
	}
}

func TestReadinessScreenStopsOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := runWithOptions(ctx, func(ctx context.Context) (setup.ReadinessReport, error) {
			close(started)
			<-ctx.Done()
			return setup.ReadinessReport{}, ctx.Err()
		}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignals())
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not start the probe")
	}
	cancel()
	select {
	case err := <-result:
		if err != ErrCancelled {
			t.Fatalf("parent cancellation returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TUI stayed open after parent cancellation")
	}
}

func TestReadinessScreenCancelStopsProbeContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &model{ctx: ctx, cancel: cancel, width: 40, height: 12}
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !m.cancelled || ctx.Err() == nil {
		t.Fatal("quit did not cancel active probe")
	}
}

func TestReadinessScreenSanitizesProbeText(t *testing.T) {
	m := &model{width: 80, height: 24, done: true, report: sampleReport()}
	m.report.Findings[0].Detail = "bad\x1b[31m\nline"
	m.detail = true
	view := m.View().Content
	if strings.Contains(view, "\x1b[31m") || !strings.Contains(ansi.Strip(view), "bad line") {
		t.Fatalf("unsafe probe detail: %q", view)
	}
}
