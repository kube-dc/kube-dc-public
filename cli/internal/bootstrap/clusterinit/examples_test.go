package clusterinit

import (
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// The public example files are executable installer inputs, not illustrative
// fragments. Keep them on the same complete-input gate as CLI/TUI Apply so a
// new required fleet value cannot make the documented first run fail later in
// Flux with an unresolved placeholder.
func TestPublishedInstallExamplesPassCompletePresetValidation(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "examples", "install", "*.env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no published install examples found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			env, err := config.LoadEnv(path)
			if err != nil {
				t.Fatalf("load example: %v", err)
			}
			o := &InitOptions{}
			ImportMap(o, env.AsMap(), func(string) bool { return false })
			if err := ValidatePresetRequiredKeys(o); err != nil {
				t.Fatalf("published example no longer produces a complete installer config: %v", err)
			}
		})
	}
}
