package tui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/shalb/kube-dc/cli/internal/alerts"
)

// fakeAlertsJSON is a minimal Alertmanager v2 /api/v2/alerts response.
const fakeAlertsJSON = `[
  {"fingerprint":"fp1","startsAt":"2020-01-01T00:00:00Z","labels":{"alertname":"HighCPU","severity":"critical","namespace":"monitoring"},"annotations":{"summary":"cpu is high"},"status":{"state":"active"}},
  {"fingerprint":"fp2","startsAt":"2020-01-01T00:00:00Z","labels":{"alertname":"DiskWarn","severity":"warning","namespace":"storage"},"annotations":{"summary":"disk filling"},"status":{"state":"active"}}
]`

// TestAlerts_Teatest_Smoke runs the REAL alerts Bubble Tea v2 program
// headless against a canned Alertmanager HTTP server and drives the full
// input surface (search → focus details → scroll → help → quit). It is the
// v2-migration coverage net the alerts package was missing: a panic in the
// v2 render/input path is re-raised by teatest as a test failure, and a
// clean finish proves the program renders + routes keys end-to-end.
//
// WithColorProfile(Ascii) forces color-free captured output (v2's
// replacement for v1's global lipgloss.SetColorProfile).
func TestAlerts_Teatest_Smoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeAlertsJSON))
	}))
	defer srv.Close()

	m := NewModel(alerts.NewAlertmanagerClient(srv.URL), "test", nil)
	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

	// The fetched alert rows render.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("HighCPU"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	// Drive the input surface: search, commit, focus details + scroll, help.
	tm.Send(tea.KeyPressMsg{Code: '/', Text: "/"}) // enter search
	tm.Type("cpu")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})   // commit search (→ HighCPU only)
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})   // focus the details pane
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})    // scroll details (not list)
	tm.Send(tea.KeyPressMsg{Code: '?', Text: "?"}) // toggle full help
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"}) // quit

	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	// Reaching here without a panic is the smoke assertion (teatest
	// re-raises any panic from the program goroutine as a failure).
}
