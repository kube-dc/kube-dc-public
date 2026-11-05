package setup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterReleaseBindingRejectsSourceEnvironmentAndComponentDrift(t *testing.T) {
	for _, variant := range []string{"matching", "environment", "source", "component", "escape", "secret"} {
		t.Run(variant, func(t *testing.T) {
			s, _ := fixture(t)
			dir := t.TempDir()
			binding := &StarterReleaseBinding{Pins: map[string]string{"KEYCLOAK_THEME_COMPONENT": "selected-theme", "SERVICES_CATALOG_COMPONENT": "selected-catalog", "KUBE_DC_BACKEND_DIGEST": "sha256:" + strings.Repeat("a", 64)}, Files: map[string]string{}}
			for _, name := range []string{"bootstrap/product-release.yaml", "bootstrap/ui-release.json", "bootstrap/release-pins.env", "platform/keycloak/components/selected-theme/kustomization.yaml", "platform/kube-dc-services-catalog/components/selected-catalog/kustomization.yaml"} {
				file := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				data := []byte("bound fixture " + name)
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				binding.Files[name] = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			binding.ReleaseSetSHA256 = binding.Files["bootstrap/product-release.yaml"]
			env := map[string]string{}
			for key, value := range binding.Pins {
				env[key] = value
			}
			switch variant {
			case "environment":
				env["KUBE_DC_BACKEND_DIGEST"] = "changed"
			case "source":
				_ = os.WriteFile(filepath.Join(dir, "bootstrap/product-release.yaml"), []byte("changed"), 0600)
			case "component":
				binding.Pins["KEYCLOAK_THEME_COMPONENT"] = "unselected"
			case "escape":
				binding.Files["../outside"] = strings.Repeat("a", 64)
			case "secret":
				binding.Pins["GITLAB_TOKEN"] = "synthetic-secret"
			}
			record := completeReleaseRecord(s)
			record.Artifacts.StarterBinding = binding
			writeReleaseTestRecord(t, s, record)
			c, err := Compile(s)
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyStarterReleaseBindings(context.Background(), c, dir, env)
			if (err == nil) != (variant == "matching") {
				t.Fatalf("variant %s: %v", variant, err)
			}
		})
	}
}
