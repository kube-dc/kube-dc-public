package clusterinit

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStarterScaffoldSources gives integration tests the same shared trees
// the published fleet-starter guarantees. Writer-specific missing-source tests
// deliberately do not call this helper.
func writeStarterScaffoldSources(t *testing.T, repo string) {
	t.Helper()
	for _, rel := range []string{
		"infrastructure/kube-ovn-network-public",
		"infrastructure/ext-net-bridge-tag",
		"addons/metallb",
		"addons/metallb-config",
		"addons/metallb-config-bgp",
	} {
		dir := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir starter source %s: %v", rel, err)
		}
		body := []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n")
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), body, 0o644); err != nil {
			t.Fatalf("write starter source %s: %v", rel, err)
		}
	}
}
