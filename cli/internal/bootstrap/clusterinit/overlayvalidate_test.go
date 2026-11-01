package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOverlayYAML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := "a: 1\n---\nb:\n  - c\n"
	if err := os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateOverlayYAML(dir); err != nil {
		t.Fatal(err)
	}
	bad := "spec:\n  patches:\n  - a: 1\n    - b: 2\n"
	if err := os.WriteFile(filepath.Join(dir, "sub", "platform.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateOverlayYAML(dir); err == nil || !strings.Contains(err.Error(), "site/sub/platform.yaml") {
		t.Fatalf("err = %v", err)
	}
}
