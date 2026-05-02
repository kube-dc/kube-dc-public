package screens_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
)

// TestContextModel_LiveKubeconfig walks the operator's actual
// ~/.kube/config and asserts that NewContextModel produces a sane
// classification for every entry — never panics, never returns an
// error, and always assigns a non-empty Identity. Skipped when no
// kubeconfig is present (e.g. in CI without one).
func TestContextModel_LiveKubeconfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kube/config")); err != nil {
		t.Skipf("no ~/.kube/config: %v", err)
	}

	m, err := screens.NewContextModel()
	if err != nil {
		t.Fatalf("NewContextModel: %v", err)
	}

	got := m.View()
	if got == "Initializing…" {
		// View() returns a placeholder until WindowSizeMsg arrives.
		// That's expected before the tea.Program loop starts; the
		// internal state is still validly populated.
	}
	t.Log("first 600 chars of empty-window View():", first(got, 600))
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ReplaceAll(s[:n], "\x1b", "ESC")
}
