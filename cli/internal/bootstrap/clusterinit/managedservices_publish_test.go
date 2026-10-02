package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishManagedServicesFilesIsGuardedAndIdempotent(t *testing.T) {
	for _, name := range []string{"eu/dc1", "sample"} {
		if !ValidManagedServicesClusterPath(name) {
			t.Fatalf("valid Fleet cluster path refused: %s", name)
		}
	}
	for _, name := range []string{"../sample", "/sample", "eu//dc1", "eu/dc1/..", "eu\\dc1"} {
		if ValidManagedServicesClusterPath(name) {
			t.Fatalf("unsafe Fleet cluster path accepted: %s", name)
		}
	}
	const catalog = "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: services-catalog\n  namespace: flux-system\nspec:\n  suspend: true\n  path: ./platform/kube-dc-services-catalog\n"
	const flag = "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false\n"
	setup := func(t *testing.T, cat, env string) (string, string, string) {
		t.Helper()
		repo := t.TempDir()
		dir := filepath.Join(repo, "clusters", "sample")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		catPath, envPath := filepath.Join(dir, "services-catalog.yaml"), filepath.Join(dir, "cluster-config.env")
		if err := os.WriteFile(catPath, []byte(cat), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
		return repo, catPath, envPath
	}
	t.Run("publish and retry", func(t *testing.T) {
		repo, catPath, envPath := setup(t, catalog, flag)
		changed, err := PublishManagedServicesFiles(repo, "sample")
		if err != nil || !changed {
			t.Fatalf("publish: changed=%v err=%v", changed, err)
		}
		actualCat, _ := os.ReadFile(catPath)
		actualEnv, _ := os.ReadFile(envPath)
		if !strings.Contains(string(actualCat), "suspend: false") || !strings.Contains(string(actualEnv), "ALL_ORGANIZATIONS=true") {
			t.Fatalf("publication incomplete: %s %s", actualCat, actualEnv)
		}
		changed, err = PublishManagedServicesFiles(repo, "sample")
		if err != nil || changed {
			t.Fatalf("retry: changed=%v err=%v", changed, err)
		}
	})
	for _, tc := range []struct{ name, cat, env, want string }{
		{"wrong resource", strings.Replace(catalog, "name: services-catalog", "name: other", 1), flag, "identity"},
		{"missing suspension", strings.Replace(catalog, "  suspend: true\n", "", 1), flag, "suspend"},
		{"duplicate flag", catalog, flag + flag, "duplicated"},
		{"partial publication", strings.Replace(catalog, "suspend: true", "suspend: false", 1), flag, "partly published"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, catPath, envPath := setup(t, tc.cat, tc.env)
			if changed, err := PublishManagedServicesFiles(repo, "sample"); err == nil || changed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("changed=%v err=%v want %q", changed, err, tc.want)
			}
			actualCat, _ := os.ReadFile(catPath)
			actualEnv, _ := os.ReadFile(envPath)
			if string(actualCat) != tc.cat || string(actualEnv) != tc.env {
				t.Fatal("refusal modified Fleet files")
			}
		})
	}
}
