package initform

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// asciiProgram forces the program's color profile to ASCII so teatest's
// captured Output() carries no ANSI color escapes — the Bubble Tea v2
// replacement for v1's lipgloss.SetColorProfile(termenv.Ascii). (v2 moved
// the profile downgrade from Render-time to the program's output writer,
// so it's a program option now, not a global.) Required for stable
// substring assertions across machines/CI.
func asciiProgram(t *testing.T, m tea.Model, w, h int) *teatest.TestModel {
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(w, h),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))
}

// validE2EState is a fully-valid internal-only + NAT install config.
func validE2EState() *State {
	return &State{
		Name: "e2e", Domain: "e2e.kube-dc.cloud", NodeIP: "203.0.113.52",
		SSHHost: "ubuntu@203.0.113.52", Email: "ops@example.com",
		Mode: "install", FleetMode: "new-repo", Provider: "github",
		Owner: "kube-dc", RepoName: "e2e-fleet-r5", Preset: "internal-only",
		NetVLANID: "0", NetInterface: "enp1s0", KubeOVNMasterNodes: "10.77.0.22",
		OSMode: "rook-ceph-local", OSDNode: "e2e-master-1", OSDSizeGB: "40",
	}
}

// TestPanel_Teatest_DriveToApply runs the REAL Bubble Tea program and
// drives it with keystrokes — the automated equivalent of a human at the
// TTY. It navigates the section list to Review, tabs into the fields, and
// presses Enter on Apply, then asserts the final model applied. This is
// exactly the interactive flow that previously needed manual validation.
func TestPanel_Teatest_DriveToApply(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	tm := asciiProgram(t, m, 120, 40)

	// The panel renders its sections.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Basics")) && bytes.Contains(b, []byte("Review"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	// Section list → Review (overshoot is clamped at the last section),
	// then into the fields, then Enter on the Apply action.
	for i := 0; i < 8; i++ {
		tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	fm := tm.FinalModel(t).(*PanelModel)
	if !fm.Applied() || fm.Cancelled() {
		t.Fatalf("driving to Review + Enter should Apply (applied=%v cancelled=%v)", fm.Applied(), fm.Cancelled())
	}
}

// TestPanel_Teatest_EditFieldCommits proves keystroke text-editing works
// end-to-end: focus the Basics fields, edit "Cluster name", type a value,
// commit with Enter — the value lands in State.
func TestPanel_Teatest_EditFieldCommits(t *testing.T) {
	m := NewPanelModel(&State{Mode: "install", Preset: "internal-only"}, "")
	tm := asciiProgram(t, m, 120, 40)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Cluster name"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})   // sections → fields (Basics, cursor 0 = Cluster name)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // begin editing
	tm.Type("mycluster")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})          // commit (validates)
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) // ctrl+c → quit (cancel)

	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	fm := tm.FinalModel(t).(*PanelModel)
	if fm.st.Name != "mycluster" {
		t.Fatalf("typed value should commit to State, got Name=%q", fm.st.Name)
	}
}

// TestPanel_Teatest_CancelAborts: 'q' quits + cancels (matching the Fleet
// TUI's exit keys). Esc is "back", not an exit — so we send 'q' here; the
// final model reports cancelled (init aborts cleanly, State untouched).
func TestPanel_Teatest_CancelAborts(t *testing.T) {
	m := NewPanelModel(&State{Mode: "install"}, "")
	tm := asciiProgram(t, m, 120, 40)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Basics"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"}) // quit (matches Fleet TUI)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	fm := tm.FinalModel(t).(*PanelModel)
	if !fm.Cancelled() || fm.Applied() {
		t.Fatalf("'q' should cancel (cancelled=%v applied=%v)", fm.Cancelled(), fm.Applied())
	}
}

// TestPanel_Teatest_PrefillAndSaveDraft drives the two prefill/save UX
// paths end-to-end: (1) a prefilled State opens the panel PRE-FILLED (the
// cluster name renders without any typing), and (2) 'S' writes a reusable
// draft spec ("save values, decide later") that is a valid re-loadable file.
func TestPanel_Teatest_PrefillAndSaveDraft(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	dir := t.TempDir()
	m.draftPath = filepath.Join(dir, "draft.env")
	tm := asciiProgram(t, m, 120, 40)

	// Opens pre-filled — the prefilled cluster name shows on the Basics pane.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Basics")) && bytes.Contains(b, []byte("e2e"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	// 'S' saves the draft and confirms in the footer.
	tm.Send(tea.KeyPressMsg{Code: 'S', Text: "S"})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("saved draft"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"}) // quit — decide later
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	body, err := os.ReadFile(m.draftPath)
	if err != nil {
		t.Fatalf("draft spec not written: %v", err)
	}
	if !bytes.Contains(body, []byte("CLUSTER_NAME=e2e")) || !bytes.Contains(body, []byte("KUBE_DC_INIT_MODE=install")) {
		t.Errorf("draft spec missing expected keys:\n%s", body)
	}
}

// TestPanel_Teatest_HelpOverlay drives the '?' full-help overlay end-to-end
// (bubbles/help), proving the keybinding help renders in the real program.
func TestPanel_Teatest_HelpOverlay(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	tm := asciiProgram(t, m, 120, 40)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Basics"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: '?', Text: "?"}) // open full help
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("switch pane")) || bytes.Contains(b, []byte("cycle option"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestPanel_Golden_Sections is the golden-file technique: snapshot a pure
// render (the section list) and diff against testdata on later runs.
// Regenerate with `go test ./... -run TestPanel_Golden -update`. Lip Gloss
// v2 always emits ANSI at Render-time (the profile downgrade moved to the
// program's output writer), so a direct render must be ansi.Strip'd for a
// stable, color-free snapshot.
func TestPanel_Golden_Sections(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	m.focus = focusSections
	golden.RequireEqual(t, []byte(ansi.Strip(m.renderSections())))
}

func TestPanel_Golden_Accelerators(t *testing.T) {
	st := validE2EState()
	st.GPUPlatform = "enabled"
	st.GPUDriverSource = "gpu-operator"
	st.GPUOperatorVersion = clusterinit.DefaultGPUOperatorVersion
	st.NVIDIADriverVersion = clusterinit.DefaultNVIDIADriverVersion
	st.NVIDIAToolkitVersion = clusterinit.DefaultNVIDIAToolkitVersion
	st.GPUNodeModes = "gpu-worker-a=pod-hami"
	st.GPUProfiles = "nvidia-v100-hami"
	st.HAMiEnabled = true
	st.HAMiVersion = clusterinit.DefaultHAMiVersion
	st.HAMiSchedulerVersion = clusterinit.DefaultHAMiSchedulerKubeVersion
	m := NewPanelModel(st, "")
	m.secCursor = sectionIndex(m, "Accelerators")
	m.focus = focusFields
	body, _ := m.renderFieldsBody(90)
	golden.RequireEqual(t, []byte(ansi.Strip(body)))
}
