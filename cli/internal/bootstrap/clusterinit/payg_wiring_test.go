package clusterinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- the login Secret: every entry checked on its own, one document only ---

func TestCheckPAYGDBSecret_Bypasses(t *testing.T) {
	enc := encryptedLogin(false)
	// data-only (every value encrypted) is a valid shape too.
	dataOnly := strings.Replace(enc, "stringData:", "data:", 1)
	if err := checkPAYGDBSecret([]byte(dataOnly)); err != nil {
		t.Fatalf("encrypted data-only login refused: %v", err)
	}
	for name, tc := range map[string]struct{ body, want string }{
		// Merging data into stringData let the encrypted stringData.password
		// mask a readable base64 data.password that stayed in the file.
		"plaintext data masked by encrypted stringData": {
			strings.Replace(enc, "stringData:", "data:\n    password: aHVudGVyMg==\nstringData:", 1), "data.password is not SOPS-encrypted"},
		"same key encrypted in both maps": {
			strings.Replace(enc, "stringData:", "data:\n    password: ENC[AES256_GCM,data:q,iv:x,tag:y,type:str]\nstringData:", 1), "in both data and stringData"},
		// Only the first document used to be read.
		"appended plaintext Secret": {
			enc + "---\napiVersion: v1\nkind: Secret\nmetadata:\n    name: leak\n    namespace: monitoring\nstringData:\n    password: hunter2\n", "more than one YAML document"},
		"trailing empty document": {enc + "---\n", "more than one YAML document"},
		"readable top-level key":  {strings.Replace(enc, "type: kubernetes.io/basic-auth", "type: kubernetes.io/basic-auth\npassword: hunter2", 1), "unexpected top-level key"},
		"readable annotation is metadata, not a value": {
			strings.Replace(enc, "    namespace: monitoring", "    namespace: monitoring\n    annotations:\n        note: fine", 1), ""},
	} {
		err := checkPAYGDBSecret([]byte(tc.body))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}

// --- resume without --payg still enforces the billing rule and the login ---

// wiredPAYGFleet is an overlay the CLI fully wired for PAYG.
func wiredPAYGFleet(t *testing.T) (fleet, clusterDir string) {
	t.Helper()
	fakeSopsOnPath(t)
	fleet = paygFleet(t, paygBaseEnv)
	if err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil); err != nil {
		t.Fatal(err)
	}
	return fleet, filepath.Join(fleet, "clusters", "c1")
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPAYGOnResume_WithoutFlagStillChecks(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, clusterDir string)
		wantErr error
		wantSub string
	}{
		{"partner billing marker", func(t *testing.T, d string) {
			appendFile(t, filepath.Join(d, "cluster-config.env"), "CLOUDSIGMA_CHART_VERSION=0.1.0\n")
		}, ErrPAYGRefused, "billed by a partner"},
		{"billing provider none", func(t *testing.T, d string) {
			p := filepath.Join(d, "cluster-config.env")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "BILLING_PROVIDER=stripe", "BILLING_PROVIDER=none", 1)), 0o644)
		}, ErrPAYGRefused, "BILLING_PROVIDER=none"},
		{"readable login", func(t *testing.T, d string) {
			_ = os.WriteFile(filepath.Join(d, paygDBSecretsFileName), []byte(encryptedLogin(true)), 0o644)
		}, ErrPAYGRefused, "uri is not SOPS-encrypted"},
		{"emptied payg.yaml", func(t *testing.T, d string) {
			_ = os.WriteFile(filepath.Join(d, paygLayerFileName), []byte("# nothing\n{}\n"), 0o644)
		}, ErrPAYGRefused, "differs from the layer"},
		{"re-pointed payg.yaml", func(t *testing.T, d string) {
			p := filepath.Join(d, paygLayerFileName)
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "path: ./platform/payg", "path: ./platform/other", 1)), 0o644)
		}, ErrPAYGRefused, "differs from the layer"},
		{"role patch deleted, marker left behind", func(t *testing.T, d string) {
			_ = os.WriteFile(filepath.Join(d, "platform.yaml"), []byte(addClusterPlatformYAML+"    "+paygDBRoleMarker+"\n"), 0o644)
		}, ErrPAYGRefused, "no patch that adds"},
		{"role patch retargeted", func(t *testing.T, d string) {
			p := filepath.Join(d, "platform.yaml")
			// Still selects the Cluster (names are regexes), but not exactly it.
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "        name: grafana-pg\n        namespace: monitoring", "        name: grafana-.*\n        namespace: monitoring", 1)), 0o644)
		}, ErrPAYGRefused, "might select"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet, clusterDir := wiredPAYGFleet(t)
			if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, false); err != nil || c {
				t.Fatalf("baseline: complete=%v err=%v", c, err)
			}
			tc.mutate(t, clusterDir)
			for _, requested := range []bool{false, true} {
				_, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, requested)
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("requested=%v: want %v containing %q, got %v", requested, tc.wantErr, tc.wantSub, err)
				}
			}
		})
	}
}

// Without --payg a resume of a partner-billed PAYG overlay must stop before
// anything is pushed or installed.
func TestApply_Resume_WithoutFlagRefusesPartnerBilledPAYG(t *testing.T) {
	fleet, clusterDir := wiredPAYGFleet(t)
	writeStarterScaffoldSources(t, fleet)
	// The fleet's own directory layout: the overlay under clusters/atlantis.
	target := filepath.Join(fleet, "clusters", "atlantis")
	if err := os.Rename(clusterDir, target); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(target, "cluster-config.env")
	_ = os.WriteFile(env, []byte(strings.Replace(readFile(t, env), "PAYG_INSTALLATION_BINDING=c1/example.com", "PAYG_INSTALLATION_BINDING=atlantis/kdc.atlantis.example.com", 1)+"CLOUDSIGMA_CHART_VERSION=0.1.0\n"), 0o644)

	git := &fakeGit{preSHA: "abc123", commitSHA: "def456"}
	opts := atlantisApplyOpts(t, fleet, applyRunner(t, fleet), git)
	opts.PAYG = false
	if err := Apply(context.Background(), opts); !errors.Is(err, ErrPAYGRefused) {
		t.Fatalf("want ErrPAYGRefused, got %v", err)
	}
	if git.pushed {
		t.Fatal("a refused resume must not push")
	}
}

// A hand-made PAYG setup (PAYG_ENABLED, login and role, no CLI identity)
// resumes fine without --payg, but --payg must not "complete" it with a
// second identity.
func TestCheckPAYGOnResume_HandMadeSetup(t *testing.T) {
	fakeSopsOnPath(t)
	fleet := paygFleet(t, paygBaseEnv+"PAYG_ENABLED=true\n")
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	_ = os.WriteFile(filepath.Join(clusterDir, paygDBSecretsFileName), []byte(encryptedLogin(false)), 0o644)
	role := renderPAYGRolePatch("grafana-pg", 8, paygNoManaged)
	_ = os.WriteFile(filepath.Join(clusterDir, "platform.yaml"), []byte(addClusterPlatformYAML+strings.Replace(role, "    "+paygDBRoleMarker+"\n", "", 1)), 0o644)

	if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, false); err != nil || c {
		t.Fatalf("without --payg: complete=%v err=%v", c, err)
	}
	if _, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); !errors.Is(err, ErrPAYGChangeOnResume) || !strings.Contains(err.Error(), "second identity") {
		t.Fatalf("with --payg: want a refusal to mint a second identity, got %v", err)
	}
}

// --- exact matching plus conservative refusal (review round 3) ---

// Every Codex example, each an overlay the resume must refuse — with or
// without --payg — and a re-run of the writer must refuse too.
func TestPAYG_Round3Refusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, fleet, clusterDir string)
		wantSub string
	}{
		{"payg.yaml: inline substitute overrides the identity", func(t *testing.T, _, d string) {
			p := filepath.Join(d, paygLayerFileName)
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "    substituteFrom:", "    substitute:\n      PAYG_INSTALLATION_UID: 22222222-3333-4444-8555-666666666666\n    substituteFrom:", 1)), 0o644)
		}, "differs from the layer"},
		{"payg.yaml: an extra substituteFrom", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, paygLayerFileName), "      - kind: ConfigMap\n        name: payg-overrides\n")
		}, "differs from the layer"},
		{"payg.yaml: a changed interval is a change too", func(t *testing.T, _, d string) {
			p := filepath.Join(d, paygLayerFileName)
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "interval: 10m", "interval: 1m", 1)), 0o644)
		}, "differs from the layer"},
		{"shared tree: a patch of the Cluster added after wiring", func(t *testing.T, fleet, _ string) {
			p := filepath.Join(fleet, "platform", "monitoring", "kustomization.yaml")
			_ = os.WriteFile(p, []byte("resources:\n  - grafana-pg/cluster.yaml\npatches:\n  - target:\n      kind: Cluster\n      name: grafana-pg\n    patch: |\n      - op: replace\n        path: /spec/instances\n        value: 3\n"), 0o644)
		}, "might select the Cluster monitoring/grafana-pg"},
		{"platform.yaml: replace /spec", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "platform.yaml"), "    - target:\n        kind: Cluster\n        name: grafana-pg\n      patch: |\n        - op: replace\n          path: /spec\n          value: {instances: 1}\n")
		}, "might select"},
		{"platform.yaml: move from /spec/managed", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "platform.yaml"), "    - target:\n        kind: Cluster\n        name: grafana-pg\n      patch: |\n        - op: move\n          from: /spec/managed\n          path: /spec/description\n")
		}, "might select"},
		{"platform.yaml: strategic-merge $patch: replace", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "platform.yaml"), "    - patch: |\n        apiVersion: postgresql.cnpg.io/v1\n        kind: Cluster\n        metadata:\n          name: grafana-pg\n          namespace: monitoring\n        spec:\n          $patch: replace\n          instances: 1\n")
		}, "might select"},
		{"platform.yaml: our role with ensure: absent", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "platform.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "ensure: present", "ensure: absent", 1)), 0o644)
		}, "might select"},
		{"platform.yaml: a label selector on our target", func(t *testing.T, _, d string) {
			p := filepath.Join(d, "platform.yaml")
			_ = os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "        namespace: monitoring\n      patch: |", "        namespace: monitoring\n        labelSelector: app=pg\n      patch: |", 1)), 0o644)
		}, "might select"},
		{"root kustomization: a patch of the login Secret", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "patches:\n  - target:\n      kind: Secret\n      name: kube-dc-metering-db\n    patch: |\n      - op: add\n        path: /stringData/password\n        value: hunter2\n")
		}, "might select the Secret monitoring/kube-dc-metering-db"},
		{"root kustomization: a patch of the payg layer", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "patches:\n  - target:\n      kind: Kustomization\n      name: payg\n    patch: |\n      - op: add\n        path: /spec/postBuild/substitute\n        value: {PAYG_METERING_IMAGE: evil}\n")
		}, "might select the Kustomization flux-system/payg"},
		{"root kustomization: a patch of cluster-config", func(t *testing.T, _, d string) {
			appendFile(t, filepath.Join(d, "kustomization.yaml"), "patchesStrategicMerge:\n  - |\n    apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: cluster-config\n    data:\n      PAYG_INSTALLATION_UID: x\n")
		}, "might select the ConfigMap flux-system/cluster-config"},
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

// Other styles that might select the Cluster, on a fresh write; and patches
// of other objects, which must stay allowed.
func TestPAYGRole_ConflictingPatches(t *testing.T) {
	for name, tc := range map[string]struct {
		overlay string // appended to platform.yaml's patches
		tree    map[string]string
		ok      bool
	}{
		"JSON6902 through a regex target":    {overlay: "    - target:\n        kind: Clus.*\n        name: grafana-.*\n      patch: |\n        - op: add\n          path: /spec/description\n          value: x\n"},
		"a name-only target":                 {overlay: "    - target:\n        name: grafana-pg\n      patch: |\n        - op: add\n          path: /metadata/labels\n          value: {}\n"},
		"an annotation selector":             {overlay: "    - target:\n        annotationSelector: team=db\n      patch: |\n        - op: add\n          path: /metadata/labels\n          value: {}\n"},
		"strategic merge without a target":   {overlay: "    - patch: |\n        kind: Cluster\n        metadata:\n          name: grafana-pg\n        spec:\n          instances: 1\n"},
		"a patch of another kind is fine":    {overlay: "    - target:\n        kind: HelmRelease\n        name: grafana-pg\n      patch: |\n        - op: add\n          path: /spec/managed\n          value: {}\n", ok: true},
		"a patch of another Cluster is fine": {overlay: "    - target:\n        kind: Cluster\n        name: other-pg\n      patch: |\n        - op: add\n          path: /spec/managed\n          value: {}\n", ok: true},
		"shared tree: strategic-merge file via patches": {tree: map[string]string{
			"platform/monitoring/kustomization.yaml": "resources:\n  - grafana-pg/cluster.yaml\npatches:\n  - path: pg-tweak.yaml\n",
			"platform/monitoring/pg-tweak.yaml":      "apiVersion: postgresql.cnpg.io/v1\nkind: Cluster\nmetadata:\n  name: grafana-pg\n  namespace: monitoring\nspec:\n  instances: 3\n",
		}},
		"shared tree: patchesStrategicMerge": {tree: map[string]string{
			"platform/monitoring/kustomization.yaml": "resources:\n  - grafana-pg/cluster.yaml\npatchesStrategicMerge:\n  - tweak.yaml\n",
			"platform/monitoring/tweak.yaml":         "kind: Cluster\nmetadata:\n  name: grafana-pg\nspec:\n  instances: 3\n",
		}},
		"shared tree: a replacement into the Cluster": {tree: map[string]string{
			"platform/monitoring/kustomization.yaml": "resources:\n  - grafana-pg/cluster.yaml\nreplacements:\n  - source:\n      kind: ConfigMap\n      name: x\n    targets:\n      - select:\n          kind: Cluster\n        fieldPaths: [spec.description]\n",
		}},
		"shared tree: a HelmRelease patch is fine": {tree: map[string]string{
			"platform/monitoring/kustomization.yaml": "resources:\n  - grafana-pg/cluster.yaml\npatches:\n  - target:\n      kind: HelmRelease\n      name: prom\n    patch: |\n      - op: add\n        path: /spec/managed\n        value: {}\n",
		}, ok: true},
	} {
		t.Run(name, func(t *testing.T) {
			fakeSopsOnPath(t)
			fleet := paygFleet(t, paygBaseEnv)
			clusterDir := filepath.Join(fleet, "clusters", "c1")
			if tc.overlay != "" {
				appendFile(t, filepath.Join(clusterDir, "platform.yaml"), tc.overlay)
			}
			for rel, body := range tc.tree {
				p := filepath.Join(fleet, rel)
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			platformBefore := readFile(t, filepath.Join(clusterDir, "platform.yaml"))
			err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil)
			if tc.ok {
				if err != nil {
					t.Fatalf("unrelated patch refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "might select") {
				t.Fatalf("want a might-select refusal, got %v", err)
			}
			if got := readFile(t, filepath.Join(clusterDir, "platform.yaml")); got != platformBefore {
				t.Error("platform.yaml changed despite the refusal")
			}
		})
	}
}

// Positive recognition is exact: the rendered entry, in any key order and
// without the marker, is wired; any deviation is not the collector's role
// and — since it selects the Cluster — refused.
func TestPAYGRoleWiring_ExactRecognition(t *testing.T) {
	want, err := paygExpectedRolePatch("grafana-pg", 8, paygNoManaged)
	if err != nil {
		t.Fatal(err)
	}
	reordered := addClusterPlatformYAML + `    - patch: |
        - path: /spec/managed
          op: add
          value:
            roles:
              - passwordSecret:
                  name: kube-dc-metering-db
                connectionLimit: 8
                createrole: false
                createdb: false
                superuser: false
                login: true
                ensure: present
                name: kube_dc_metering
      target:
        namespace: monitoring
        name: grafana-pg
        kind: Cluster
        group: postgresql.cnpg.io
`
	if wired, err := paygRoleWiring([]byte(reordered), "grafana-pg", want); err != nil || !wired {
		t.Fatalf("the rendered patch, reordered and unmarked: wired=%v err=%v", wired, err)
	}
	for name, mutate := range map[string]func(string) string{
		"ensure: absent": func(s string) string { return strings.Replace(s, "ensure: present", "ensure: absent", 1) },
		"no passwordSecret": func(s string) string {
			return strings.Replace(s, "              - passwordSecret:\n                  name: kube-dc-metering-db\n", "              - ", 1)
		},
		"login false":              func(s string) string { return strings.Replace(s, "login: true", "login: false", 1) },
		"another connection limit": func(s string) string { return strings.Replace(s, "connectionLimit: 8", "connectionLimit: 80", 1) },
		"an extra op": func(s string) string {
			return strings.Replace(s, "    - patch: |\n", "    - patch: |\n        - op: remove\n          path: /spec/backup\n", 1)
		},
		"a label selector": func(s string) string {
			return strings.Replace(s, "        group: postgresql.cnpg.io\n", "        group: postgresql.cnpg.io\n        labelSelector: app=pg\n", 1)
		},
		"a regex name": func(s string) string {
			return strings.Replace(s, "        name: grafana-pg\n        kind", "        name: grafana-.*\n        kind", 1)
		},
	} {
		if wired, err := paygRoleWiring([]byte(mutate(reordered)), "grafana-pg", want); wired || err == nil || !strings.Contains(err.Error(), "might select") {
			t.Errorf("%s: wired=%v err=%v, want a might-select refusal", name, wired, err)
		}
	}
	// The same patch is not wired against a base that now declares roles:
	// its `add /spec/managed` would replace them.
	appendWant, _ := paygExpectedRolePatch("grafana-pg", 8, paygManagedRoles)
	if wired, err := paygRoleWiring([]byte(reordered), "grafana-pg", appendWant); wired || err == nil {
		t.Errorf("a create-managed patch against a base with roles: wired=%v err=%v", wired, err)
	}
}
