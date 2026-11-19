package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/script"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/mock"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// A session built on a kubeconfig hands that file to its scripts when the
// shell sets no KUBECONFIG; a session with another kind of runner is left
// alone.
func TestSessionUseKubeconfigForScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nprintf '%s' \"$KUBECONFIG\" > \"$KUBE_DC_TEST_OUTPUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bootstrap", "flux-install.sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", "")
	kubeconfig := filepath.Join(dir, "admin.yaml")
	s := &Session{Scripts: script.New(dir, "", nil)}
	s.UseKubeconfigForScripts(kubeconfig)
	output := filepath.Join(dir, "seen")
	lines, err := s.Scripts.Run(context.Background(), ports.ScriptFluxInstall, map[string]string{"KUBE_DC_TEST_OUTPUT": output}, "test")
	if err != nil {
		t.Fatalf("the script was refused: %v", err)
	}
	for range lines {
	}
	if got, _ := os.ReadFile(output); string(got) != kubeconfig {
		t.Fatalf("the script got %q, want the session's kubeconfig", got)
	}

	// No path: nothing changes. Another runner: untouched. A nil session: no panic.
	plain := script.New(dir, "", nil)
	s = &Session{Scripts: plain}
	s.UseKubeconfigForScripts("")
	if s.Scripts != ports.ScriptRunner(plain) {
		t.Fatal("an empty path replaced the runner")
	}
	other := mock.NewScriptRunner(nil, nil)
	s = &Session{Scripts: other}
	s.UseKubeconfigForScripts(kubeconfig)
	if s.Scripts != ports.ScriptRunner(other) {
		t.Fatal("a runner that is not the script adapter was replaced")
	}
	var none *Session
	none.UseKubeconfigForScripts(kubeconfig)
}
