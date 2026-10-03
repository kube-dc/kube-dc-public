package clusterinit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shadowReconcilerYAML is a second Flux Kustomization building the PAYG bundle
// with its own inline substitution — the reconciler the build check refuses.
const shadowReconcilerYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: payg-shadow
  namespace: flux-system
spec:
  path: ./platform/payg
  postBuild:
    substitute:
      PAYG_INSTALLATION_UID: 22222222-3333-4444-8555-666666666666
`

// shadowReconcilerJSON is the same object as JSON.
const shadowReconcilerJSON = `{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
 "metadata": {"name": "payg-shadow", "namespace": "flux-system"},
 "spec": {"path": "./platform/payg", "postBuild": {"substitute": {"PAYG_INSTALLATION_UID": "22222222-3333-4444-8555-666666666666"}}}}
`

// includeInRoot adds a resource to the cluster root kustomization.
func includeInRoot(t *testing.T, clusterDir, resource string) {
	t.Helper()
	p := filepath.Join(clusterDir, "kustomization.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - "+resource+"\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeUnder writes files relative to base, creating directories.
func writeUnder(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Every route around the file-level checks that goes through the cluster
// directory's own build (Codex round 4). Each must be refused by a resume,
// with or without --payg, and by a re-run of the writer.
func TestPAYG_Round4ClusterBuild(t *testing.T) {
	scaffoldGen := "configMapGenerator:\n  - name: cluster-config\n    namespace: flux-system\n    envs:\n      - cluster-config.env\n"
	cases := []struct {
		name    string
		mutate  func(t *testing.T, fleet, clusterDir string)
		wantSub string
	}{
		{"a generator pointing at a copied env", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "kustomization.yaml")
			writeUnder(t, d, map[string]string{"copied.env": "PAYG_INSTALLATION_UID=22222222-3333-4444-8555-666666666666\n"})
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "      - cluster-config.env\n", "      - copied.env\n", 1)), 0o644)
		}, `configMapGenerator entry for "cluster-config" other than the scaffold's`},
		{"a second generator merging into cluster-config", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), scaffoldGen, scaffoldGen+"  - name: cluster-config\n    behavior: merge\n    literals:\n      - PAYG_METERING_IMAGE=other\n", 1)), 0o644)
		}, "other than the scaffold's"},
		{"a secretGenerator for the login", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "secretGenerator:\n  - name: kube-dc-metering-db\n    namespace: monitoring\n    literals:\n      - password=hunter2\n")
		}, `secretGenerator entry for "kube-dc-metering-db"`},
		{"generatorOptions changed", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "disableNameSuffixHash: true", "disableNameSuffixHash: false", 1)), 0o644)
		}, "generatorOptions must be exactly the scaffold's"},
		{"root components: [./payg-overrides]", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"payg-overrides/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\npatches:\n  - target: {kind: Kustomization, name: payg}\n    patch: |\n      - op: add\n        path: /spec/postBuild/substitute\n        value: {PAYG_METERING_IMAGE: other}\n"})
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "components:\n  - ./payg-overrides\n")
		}, `uses "components"`},
		{"root transformers: [override.yaml]", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"override.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: override\npatch: '[{\"op\": \"replace\", \"path\": \"/spec/path\", \"value\": \"./elsewhere\"}]'\ntarget:\n  name: payg\n"})
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "transformers:\n  - override.yaml\n")
		}, `uses "transformers"`},
		{"root validators", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "validators:\n  - v.yaml\n")
		}, `uses "validators"`},
		{"root namespace transformer", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "namespace: elsewhere\n")
		}, `uses "namespace"`},
		{"a transformer in a directory the root includes", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"extra/kustomization.yaml": "resources: []\ntransformers:\n  - t.yaml\n"})
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - extra\n", 1)), 0o644)
		}, `extra/kustomization.yaml uses "transformers"`},
		{"cluster-config generated in an included directory", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"extra/kustomization.yaml": "resources: []\n" + scaffoldGen})
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - extra\n", 1)), 0o644)
		}, "other than the scaffold's"},
		{"a second Flux Kustomization with path ./platform/payg", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"payg-shadow.yaml": shadowReconcilerYAML})
		}, "builds ./platform/payg"},
		// Round 5: a reachable resource is checked whatever its extension and
		// wherever it lives; a List cannot hide a reconciler.
		{"a reachable .json resource holding a second reconciler", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"payg-shadow.json": shadowReconcilerJSON})
			includeInRoot(t, d, "payg-shadow.json")
		}, "builds ./platform/payg"},
		{"a resource included from a nested cluster directory", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{
				"nested/cluster-config.env": "BILLING_PROVIDER=stripe\n",
				"nested/shadow.yaml":        shadowReconcilerYAML,
			})
			includeInRoot(t, d, "nested/shadow.yaml")
		}, "builds ./platform/payg"},
		{"a v1 List wrapping a second reconciler", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"export.yaml": "apiVersion: v1\nkind: List\nitems:\n" + indentLines("- "+strings.ReplaceAll(strings.TrimRight(shadowReconcilerYAML, "\n"), "\n", "\n  ")+"\n", "  ")})
		}, "builds ./platform/payg"},
		{"a typed *List wrapping a second reconciler", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"export.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: KustomizationList\nitems:\n" + indentLines("- "+strings.ReplaceAll(strings.TrimRight(shadowReconcilerYAML, "\n"), "\n", "\n  ")+"\n", "  ")})
		}, "builds ./platform/payg"},
		{"a Flux Kustomization below platform/payg", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"sub/x.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: x\n  namespace: flux-system\nspec:\n  path: platform/payg/../payg/\n"})
		}, "builds platform/payg"},
		{"a second object named payg in flux-system", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"other.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: payg\n  namespace: flux-system\ndata: {}\n"})
		}, "second ConfigMap named flux-system/payg"},
		{"cluster-config declared by hand", func(t *testing.T, _, d string) {
			writeUnder(t, d, map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cluster-config\n  namespace: flux-system\ndata:\n  PAYG_INSTALLATION_UID: x\n"})
		}, "declares ConfigMap cluster-config by hand"},
		{"the bundle included straight into the root build", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - ../../platform/payg\n", 1)), 0o644)
		}, "only payg.yaml may build the PAYG bundle"},
		{"a remote resource", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "kustomization.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - https://example.com/k.yaml\n", 1)), 0o644)
		}, "remote or empty resource"},
		{"a root patch replacing platform /spec/patches", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "patches:\n  - target:\n      kind: Kustomization\n      name: platform\n    patch: |\n      - op: replace\n        path: /spec/patches\n        value: []\n")
		}, "might select the Kustomization flux-system/platform"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet, clusterDir := wiredPAYGFleet(t)
			if _, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, false); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			tc.mutate(t, fleet, clusterDir)
			for _, requested := range []bool{false, true} {
				_, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, requested)
				if !errors.Is(err, ErrPAYGRefused) || !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("resume requested=%v: want ErrPAYGRefused containing %q, got %v", requested, tc.wantSub, err)
				}
			}
			if err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("re-run of the writer: want %q, got %v", tc.wantSub, err)
			}
		})
	}
}

// What the scaffold and the fleet already do stays allowed: an included
// directory with plain resources, flux-system's own patch of its Kustomization,
// other generators, and a nested cluster's objects.
func TestPAYG_ClusterBuildAllowsTheScaffoldShape(t *testing.T) {
	fleet, clusterDir := wiredPAYGFleet(t)
	writeUnder(t, clusterDir, map[string]string{
		"flux-system/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n- gotk-sync.yaml\npatches:\n  - path: gotk-patches.yaml\n    target:\n      group: kustomize.toolkit.fluxcd.io\n      version: v1\n      kind: Kustomization\n      name: flux-system\n",
		"flux-system/gotk-sync.yaml":     "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: flux-system\n  namespace: flux-system\nspec:\n  path: ./clusters/c1\n",
		"flux-system/gotk-patches.yaml":  "- op: add\n  path: /spec/decryption\n  value: {provider: sops}\n",
		"extra/kustomization.yaml":       "resources:\n  - cm.yaml\nconfigMapGenerator:\n  - name: unrelated\n    literals: [a=b]\n",
		"extra/cm.yaml":                  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n  namespace: flux-system\n",
		// A nested cluster's own payg.yaml is ITS business, not this one's.
		"nested/cluster-config.env": "BILLING_PROVIDER=stripe\n",
		"nested/payg.yaml":          paygLayerYAML,
	})
	p := filepath.Join(clusterDir, "kustomization.yaml")
	_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "resources:\n", "resources:\n  - extra\n", 1)), 0o644)
	for _, requested := range []bool{false, true} {
		if _, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, requested); err != nil {
			t.Errorf("requested=%v: the scaffold shape was refused: %v", requested, err)
		}
	}
	if err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil); err != nil {
		t.Errorf("re-run refused the scaffold shape: %v", err)
	}
}
