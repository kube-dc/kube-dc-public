package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Root-level TLS Secrets and the private-CA ConfigMap need their namespaces in
// the same apply; otherwise a fresh cluster's root Kustomization never applies.
func TestWriteWildcardTLS_EmitsCreateOnlyRootNamespaces(t *testing.T) {
	fleet := tlsFleet(t)
	fakeSops(t)
	if err := WriteWildcardTLS(fleet, "c1", "example.com", wildcardMaterial(t), nil); err != nil {
		t.Fatal(err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	if err := ensureRootNamespaces(clusterDir, []string{"kube-dc", "extra"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(clusterDir, rootNamespacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	got := map[string]bool{}
	for {
		var doc struct {
			Kind     string
			Metadata struct {
				Name        string
				Annotations map[string]string
			}
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind != "Namespace" {
			t.Fatalf("unexpected kind %q", doc.Kind)
		}
		if doc.Metadata.Annotations["kustomize.toolkit.fluxcd.io/ssa"] != "IfNotPresent" ||
			doc.Metadata.Annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" {
			t.Fatalf("%s is not create-only and unpruned: %v", doc.Metadata.Name, doc.Metadata.Annotations)
		}
		got[doc.Metadata.Name] = true
	}
	for _, t2 := range wildcardSecretTargets() {
		if t2.Namespace != "flux-system" && !got[t2.Namespace] {
			t.Errorf("namespace %s missing", t2.Namespace)
		}
	}
	if got["flux-system"] {
		t.Error("flux-system is declared by the bootstrap manifests and must not be repeated")
	}
	if !got["extra"] {
		t.Error("a second writer's namespaces were not merged")
	}
	kust, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if strings.Count(string(kust), "- "+rootNamespacesFileName) != 1 {
		t.Fatalf("root namespaces not wired exactly once:\n%s", kust)
	}
	before := string(body)
	if err := ensureRootNamespaces(clusterDir, []string{"extra"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(clusterDir, rootNamespacesFileName))
	if string(after) != before {
		t.Fatal("re-run changed the file")
	}
}
