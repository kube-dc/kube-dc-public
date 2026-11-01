package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const siteInfraYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-cni
  namespace: flux-system
spec:
  interval: 10m
  path: ./infrastructure/cni-managed-v2
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
  namespace: flux-system
spec:
  dependsOn:
  - name: infra-cni
  path: ./infrastructure/core-managed-v2
  prune: false
`

const sitePlatformYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  path: ./platform
  components:
    - ./gateway-config/components/host-bind
  # trailing comment stays
  prune: false
`

func siteFleet(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "site")
	for _, d := range []string{dir, filepath.Join(repo, "install", "a"), filepath.Join(repo, "install", "b"), filepath.Join(repo, "install", "plain")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	component := "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"
	write(filepath.Join(repo, "install", "a", "kustomization.yaml"), component)
	write(filepath.Join(repo, "install", "b", "kustomization.yaml"), component)
	write(filepath.Join(repo, "install", "plain", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
	write(filepath.Join(dir, "infrastructure.yaml"), siteInfraYAML)
	write(filepath.Join(dir, "platform.yaml"), sitePlatformYAML)
	return repo, dir
}

func TestWriteSiteComponentsAppendsRelativeToLayerPath(t *testing.T) {
	repo, dir := siteFleet(t)
	comps, err := ParseSiteComponents("infra-core=install/a, infra-core=install/b,platform=install/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSiteComponents(repo, "site", comps, nil); err != nil {
		t.Fatal(err)
	}
	infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	docs := splitLayerDocs(string(infra))
	if got := layerComponents(docs[1].text); strings.Join(got, ",") != "../../install/a,../../install/b" {
		t.Fatalf("infra-core components = %v\n%s", got, infra)
	}
	if got := layerComponents(docs[0].text); len(got) != 0 {
		t.Fatalf("infra-cni was edited: %v", got)
	}
	if got := layerComponents(string(platform)); strings.Join(got, ",") != "./gateway-config/components/host-bind,../install/a" {
		t.Fatalf("platform components = %v", got)
	}
	if !strings.Contains(string(platform), "# trailing comment stays") {
		t.Fatal("comments were not preserved")
	}
	// Idempotent.
	if err := WriteSiteComponents(repo, "site", comps, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
	if string(again) != string(infra) {
		t.Fatal("re-run changed the file")
	}
}

func TestSiteComponentsRejectUnsafeInput(t *testing.T) {
	for _, bad := range []string{"platform", "platform=/abs", "platform=../outside", "Platform=install/a", "platform=a b"} {
		if _, err := ParseSiteComponents(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	repo, _ := siteFleet(t)
	for _, c := range []SiteComponent{{"platform", "install/plain"}, {"platform", "install/missing"}, {"no-such-layer", "install/a"}} {
		if err := WriteSiteComponents(repo, "site", []SiteComponent{c}, nil); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}
