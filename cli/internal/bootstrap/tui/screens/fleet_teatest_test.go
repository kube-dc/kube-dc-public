package screens

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// TestFleet_Teatest_DrillDownsNoCrash runs the REAL Fleet program and
// drives the per-cluster drill-downs (OpenBao 'o', then a Kustomization
// via Tab→Enter) to catch render crashes like the one teatest found in
// the install panel. Uses a fixture cluster with no KUBE_API_EXTERNAL_URL
// so the probe returns immediately (no network / no 8s timeout).
func TestFleet_Teatest_DrillDownsNoCrash(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "clusters", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "clusters", "foo", "cluster-config.env"),
		[]byte("DOMAIN=foo.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewFleetModel(tmp)
	// Force ASCII output (Bubble Tea v2 profile downgrade is a program
	// option now, not lipgloss.SetColorProfile) so substring asserts are
	// stable and color-free.
	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(140, 40),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

	// The fleet landing renders (title + the fixture cluster).
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Kube-DC Fleet")) && bytes.Contains(b, []byte("foo"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	// Open + close the OpenBao side panel (no data → renders the note, no crash).
	tm.Send(tea.KeyPressMsg{Code: 'o', Text: "o"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	// Enter the details pane, drill a row, back out.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	// Reaching here without a panic is the assertion (teatest re-raises
	// any panic from the program goroutine as a test failure).
}
