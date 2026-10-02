package clusterinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"gopkg.in/yaml.v3"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// testMeteringImage is the digest-pinned shape the starter pins carry.
const testMeteringImage = "shalb/kube-dc-metering:v0.9.1-rc7-payg.20260928.2@sha256:dc00b2201f8e69983f88e494ec1f1d90a7b1b7fc1b6d21fa1b1571319d65b195"

const testPAYGDomain = "example.com"

// paygBaseEnv is a rendered cluster-config.env of an installation that bills
// through Kube-DC and carries the starter's metering pin.
const paygBaseEnv = "DOMAIN=example.com\nBILLING_PROVIDER=stripe\n\n# release pins\nPAYG_METERING_IMAGE=" + testMeteringImage + "\n"

// addClusterPlatformYAML is the tail of the platform.yaml add-cluster.sh
// writes today: it already carries a spec.patches list WITHOUT a kube-dc
// marker (the managed-K8s backend values), which the PAYG role patch must
// compose with.
const addClusterPlatformYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  path: ./platform
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
  patches:
    - target:
        kind: HelmRelease
        name: kube-dc
        namespace: kube-dc
      patch: |
        - op: add
          path: /spec/values/backend/managedK8sWorkerMTU
          value: ${MANAGED_K8S_WORKER_MTU:=0}
`

// grafanaPGBase is the shared platform CNPG Cluster the role lands on, as the
// fleet ships it: no managed block.
const grafanaPGBase = `apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: grafana-pg
  namespace: monitoring
spec:
  instances: 2
  storage:
    storageClass: ${MONITORING_STORAGE_CLASS:=local-path}
`

// v4UUID is the exact shape newPAYGInstallationUID must produce.
var v4UUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// fakeSopsOnPath prepends a fake `sops` to PATH that turns the three login
// values into ENC[...] ciphertexts and adds SOPS metadata — the shape the
// writer's strict check requires — without the real binary or age keys.
func fakeSopsOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
f="$3"
[ -n "$f" ] || exit 2
sed -E 's/^(    (username|password|uri): ).*/\1ENC[AES256_GCM,data:fake,iv:x,tag:y,type:str]/' "$f" > "$f.x" && mv "$f.x" "$f"
printf 'sops:\n    mac: ENC[AES256_GCM,data:mac,iv:x,tag:y,type:str]\n    age: []\n' >> "$f"
`
	if err := os.WriteFile(filepath.Join(dir, "sops"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// failingSopsOnPath simulates a transient sops failure (no age key reachable).
func failingSopsOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sops"), []byte("#!/bin/sh\necho 'no age identity' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func fixPAYGClock(t *testing.T) {
	t.Helper()
	orig := paygNow
	paygNow = func() time.Time { return time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { paygNow = orig })
}

// writePAYGStarter adds what the fleet-starter ships for PAYG: the bundle and
// the platform CNPG Cluster the role is added to.
func writePAYGStarter(t *testing.T, fleet string) {
	t.Helper()
	for path, body := range map[string]string{
		filepath.Join(paygBundlePath, "kustomization.yaml"):                   "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - metering.yaml\n",
		filepath.Join("platform", "monitoring", "grafana-pg", "cluster.yaml"): grafanaPGBase,
	} {
		full := filepath.Join(fleet, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// paygFleet is a scaffolded overlay as add-cluster.sh leaves it, plus the
// starter's PAYG pieces.
// scaffoldRootKustomization is the cluster root add-cluster.sh writes: the
// resources, and cluster-config generated from cluster-config.env.
const scaffoldRootKustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - flux-system
  - infrastructure.yaml
  - platform.yaml
  - secrets.enc.yaml
configMapGenerator:
  - name: cluster-config
    namespace: flux-system
    envs:
      - cluster-config.env
generatorOptions:
  disableNameSuffixHash: true
`

func paygFleet(t *testing.T, env string) string {
	t.Helper()
	fleet := tempFleet(t, "c1")
	writePAYGStarter(t, fleet)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	if err := os.WriteFile(filepath.Join(clusterDir, "platform.yaml"), []byte(addClusterPlatformYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "kustomization.yaml"), []byte(scaffoldRootKustomization), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return fleet
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// encryptedLogin is a SOPS-shaped kube-dc-metering-db Secret; plainURI leaves
// the uri readable (a partially encrypted file).
func encryptedLogin(plainURI bool) string {
	uri := "ENC[AES256_GCM,data:u,iv:x,tag:y,type:str]"
	if plainURI {
		uri = "postgresql://kube_dc_metering:hunter2@grafana-pg-rw.monitoring.svc:5432/kube_dc_metering"
	}
	return "apiVersion: v1\nkind: Secret\nmetadata:\n    name: kube-dc-metering-db\n    namespace: monitoring\ntype: kubernetes.io/basic-auth\nstringData:\n" +
		"    username: ENC[AES256_GCM,data:n,iv:x,tag:y,type:str]\n" +
		"    password: ENC[AES256_GCM,data:p,iv:x,tag:y,type:str]\n" +
		"    uri: " + uri + "\n" +
		"sops:\n    mac: ENC[AES256_GCM,data:mac,iv:x,tag:y,type:str]\n    age: []\n"
}

func TestWritePAYG_Disabled_WritesNothing(t *testing.T) {
	fleet := paygFleet(t, paygBaseEnv)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	kustBefore := readFile(t, filepath.Join(clusterDir, "kustomization.yaml"))

	t.Setenv("PATH", t.TempDir()) // sops absent: must not even be looked for
	if err := WritePAYG(fleet, "c1", testPAYGDomain, false, nil); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	for _, f := range []string{paygLayerFileName, paygDBSecretsFileName} {
		if _, err := os.Stat(filepath.Join(clusterDir, f)); !os.IsNotExist(err) {
			t.Errorf("disabled must not write %s", f)
		}
	}
	if got := readFile(t, filepath.Join(clusterDir, "cluster-config.env")); got != paygBaseEnv {
		t.Errorf("disabled must leave cluster-config.env alone:\n%s", got)
	}
	if got := readFile(t, filepath.Join(clusterDir, "kustomization.yaml")); got != kustBefore {
		t.Errorf("disabled must leave kustomization.yaml alone:\n%s", got)
	}
	if got := readFile(t, filepath.Join(clusterDir, "platform.yaml")); got != addClusterPlatformYAML {
		t.Errorf("disabled must leave platform.yaml alone:\n%s", got)
	}
}

func TestWritePAYG_WiresInstallation(t *testing.T) {
	fakeSopsOnPath(t)
	fixPAYGClock(t)
	fleet := paygFleet(t, paygBaseEnv)
	clusterDir := filepath.Join(fleet, "clusters", "c1")

	var out strings.Builder
	if err := WritePAYG(fleet, "c1", testPAYGDomain, true, &out); err != nil {
		t.Fatalf("WritePAYG: %v", err)
	}

	// cluster-config.env: the switch, a fresh v4 identity bound to this
	// installation, the revision, and the pinned image untouched.
	env := readFile(t, filepath.Join(clusterDir, "cluster-config.env"))
	uid := envValue(env, PAYGInstallationUIDKey)
	if !v4UUID.MatchString(uid) {
		t.Fatalf("PAYG_INSTALLATION_UID %q is not a v4 UUID:\n%s", uid, env)
	}
	for k, want := range map[string]string{
		PAYGEnabledKey:             "true",
		PAYGInstallationBindingKey: "c1/example.com",
		PAYGProductsRevisionKey:    "c1-20261001",
		paygConnectionLimitKey:    "30",
		PAYGMeteringImageKey:       testMeteringImage,
	} {
		if got := envValue(env, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(out.String(), uid) {
		t.Errorf("the generated UID must be shown to the operator:\n%s", out.String())
	}

	// payg.yaml: the README's Flux Kustomization.
	var layer struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			DependsOn []struct{ Name string }     `yaml:"dependsOn"`
			Path      string                      `yaml:"path"`
			Prune     bool                        `yaml:"prune"`
			Wait      bool                        `yaml:"wait"`
			SourceRef struct{ Kind, Name string } `yaml:"sourceRef"`
			PostBuild struct {
				SubstituteFrom []struct{ Kind, Name string } `yaml:"substituteFrom"`
			} `yaml:"postBuild"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(clusterDir, paygLayerFileName))), &layer); err != nil {
		t.Fatalf("payg.yaml is not YAML: %v", err)
	}
	if layer.Kind != "Kustomization" || layer.Metadata.Name != "payg" || layer.Metadata.Namespace != "flux-system" ||
		layer.Spec.Path != "./platform/payg" || !layer.Spec.Prune || !layer.Spec.Wait ||
		len(layer.Spec.DependsOn) != 1 || layer.Spec.DependsOn[0].Name != "platform" ||
		layer.Spec.SourceRef.Name != "flux-system" ||
		len(layer.Spec.PostBuild.SubstituteFrom) != 1 || layer.Spec.PostBuild.SubstituteFrom[0].Name != "cluster-config" {
		t.Errorf("payg.yaml does not match the bundle README: %+v", layer)
	}

	// Both new files are wired into the root kustomization.
	kust := readFile(t, filepath.Join(clusterDir, "kustomization.yaml"))
	for _, res := range []string{paygLayerFileName, paygDBSecretsFileName} {
		if !strings.Contains(kust, "  - "+res+"\n") {
			t.Errorf("kustomization.yaml does not list %s:\n%s", res, kust)
		}
	}

	// The login went through sops and passes the strict check.
	secret := readFile(t, filepath.Join(clusterDir, paygDBSecretsFileName))
	if err := checkPAYGDBSecret([]byte(secret)); err != nil || strings.Contains(secret, "postgresql://") {
		t.Errorf("metering-db.enc.yaml is not a fully encrypted login (%v):\n%s", err, secret)
	}

	// platform.yaml: still valid YAML, the add-cluster.sh patch kept, the
	// role patch appended to the SAME list, creating spec.managed because the
	// base Cluster has none.
	platform := readFile(t, filepath.Join(clusterDir, "platform.yaml"))
	if n := strings.Count(platform, "  patches:"); n != 1 {
		t.Fatalf("platform.yaml has %d patches: keys, want 1:\n%s", n, platform)
	}
	patches := platformPatches(t, platform)
	if len(patches) != 2 || patches[0].Target["name"] != "kube-dc" {
		t.Fatalf("want the add-cluster patch followed by the role patch, got %+v", patches)
	}
	role := patches[1]
	if role.Target["kind"] != "Cluster" || role.Target["name"] != "grafana-pg" || role.Target["namespace"] != "monitoring" {
		t.Errorf("role patch targets %v", role.Target)
	}
	patched := applyRolePatch(t, grafanaPGBase, role.Patch)
	if names := managedRoleNames(t, patched); strings.Join(names, ",") != "kube_dc_metering" {
		t.Errorf("roles after the patch = %v", names)
	}
	if !strings.Contains(platform, "connectionLimit: 30") {
		t.Error("default role connection limit must match the PAYG Database bundle")
	}

	// Re-run: nothing rotates, nothing duplicates — and sops is not needed,
	// because an existing encrypted login is kept.
	t.Setenv("PATH", t.TempDir())
	changed, err := writePAYG(fleet, "c1", testPAYGDomain, true, nil)
	if err != nil || changed {
		t.Fatalf("re-run: changed=%v err=%v", changed, err)
	}
	if got := readFile(t, filepath.Join(clusterDir, "cluster-config.env")); got != env {
		t.Fatalf("re-run changed cluster-config.env:\n%s", got)
	}
	for name, before := range map[string]string{paygDBSecretsFileName: secret, "platform.yaml": platform, "kustomization.yaml": kust} {
		if got := readFile(t, filepath.Join(clusterDir, name)); got != before {
			t.Errorf("re-run changed %s", name)
		}
	}
}

type platformPatch struct {
	Target map[string]string `yaml:"target"`
	Patch  string            `yaml:"patch"`
}

func platformPatches(t *testing.T, platform string) []platformPatch {
	t.Helper()
	var doc struct {
		Spec struct {
			Patches []platformPatch `yaml:"patches"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(platform), &doc); err != nil {
		t.Fatalf("platform.yaml no longer parses: %v\n%s", err, platform)
	}
	return doc.Spec.Patches
}

// applyRolePatch applies the JSON6902 body to a base manifest the way
// kustomize does, returning the patched object as JSON.
func applyRolePatch(t *testing.T, baseYAML, patchYAML string) []byte {
	t.Helper()
	toJSON := func(y string) []byte {
		var v any
		if err := yaml.Unmarshal([]byte(y), &v); err != nil {
			t.Fatalf("yaml: %v\n%s", err, y)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	p, err := jsonpatch.DecodePatch(toJSON(patchYAML))
	if err != nil {
		t.Fatalf("role patch is not JSON6902: %v\n%s", err, patchYAML)
	}
	out, err := p.Apply(toJSON(baseYAML))
	if err != nil {
		t.Fatalf("role patch does not apply to the base Cluster: %v\n%s", err, patchYAML)
	}
	return out
}

func managedRoleNames(t *testing.T, clusterJSON []byte) []string {
	t.Helper()
	var c struct {
		Spec struct {
			Managed struct {
				Roles []struct {
					Name            string
					Login           bool
					ConnectionLimit int
					PasswordSecret  struct{ Name string }
				}
			}
		}
	}
	if err := json.Unmarshal(clusterJSON, &c); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range c.Spec.Managed.Roles {
		names = append(names, r.Name)
		if r.Name == paygDBRole && (!r.Login || r.ConnectionLimit < 1 || r.PasswordSecret.Name != paygDBSecretName) {
			t.Errorf("role = %+v", r)
		}
	}
	return names
}

// The role is ADDED to what the base Cluster declares, never a replacement.
func TestRenderPAYGRolePatch_KeepsExistingRoles(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		shape paygManagedShape
		want  string
	}{
		{"no managed block", grafanaPGBase, paygNoManaged, "kube_dc_metering"},
		{"managed without roles", grafanaPGBase + "  managed:\n    services: {}\n", paygManagedNoRoles, "kube_dc_metering"},
		{"existing roles", grafanaPGBase + "  managed:\n    roles:\n      - name: grafana_ro\n        login: true\n", paygManagedRoles, "grafana_ro,kube_dc_metering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet := t.TempDir()
			path := filepath.Join(fleet, "platform", "monitoring", "grafana-pg", "cluster.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("# the shared platform database\n---\n"+tc.base), 0o644); err != nil {
				t.Fatal(err)
			}
			shape, err := findPAYGBaseCluster(fleet, "grafana-pg")
			if err != nil || shape != tc.shape {
				t.Fatalf("shape = %v err=%v, want %v", shape, err, tc.shape)
			}
			lines, _, err := patchPlatformPAYGRole(renderPAYGRolePatch("grafana-pg", 8, shape))(strings.Split(genPlatformYAML, "\n"))
			if err != nil {
				t.Fatal(err)
			}
			patches := platformPatches(t, strings.Join(lines, "\n"))
			names := managedRoleNames(t, applyRolePatch(t, tc.base, patches[0].Patch))
			if strings.Join(names, ",") != tc.want {
				t.Errorf("roles = %v, want %s", names, tc.want)
			}
		})
	}

	// A base that already declares the role is a conflict, and a Cluster the
	// fleet does not ship cannot be patched safely.
	fleet := t.TempDir()
	path := filepath.Join(fleet, "platform", "db.yaml")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(grafanaPGBase+"  managed:\n    roles:\n      - name: kube_dc_metering\n"), 0o644)
	if _, err := findPAYGBaseCluster(fleet, "grafana-pg"); err == nil || !strings.Contains(err.Error(), "already declares") {
		t.Errorf("want an already-declares refusal, got %v", err)
	}
	if _, err := findPAYGBaseCluster(fleet, "billing-pg"); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("want a not-found refusal, got %v", err)
	}
}

func TestWritePAYG_KeepsBoundIdentityAndHonoursOverrides(t *testing.T) {
	fakeSopsOnPath(t)
	fixPAYGClock(t)
	const existing = "11111111-2222-4333-8444-555555555555"
	fleet := paygFleet(t, paygBaseEnv+
		PAYGInstallationUIDKey+"="+existing+"\n"+
		PAYGInstallationBindingKey+"=c1/example.com\n"+
		PAYGProductsRevisionKey+"=c1-launch\n"+
		"PAYG_DB_CONNECTION_LIMIT=4\n")
	orig := paygNewUID
	paygNewUID = func() (string, error) { t.Fatal("an existing UID must never be regenerated"); return "", nil }
	t.Cleanup(func() { paygNewUID = orig })

	if err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil); err != nil {
		t.Fatalf("WritePAYG: %v", err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	env := readFile(t, filepath.Join(clusterDir, "cluster-config.env"))
	if got := envValue(env, PAYGInstallationUIDKey); got != existing {
		t.Errorf("UID = %q, want the existing %q", got, existing)
	}
	if got := envValue(env, PAYGProductsRevisionKey); got != "c1-launch" {
		t.Errorf("revision = %q, want the operator's c1-launch", got)
	}
	if !strings.Contains(readFile(t, filepath.Join(clusterDir, "platform.yaml")), "connectionLimit: 4") {
		t.Error("role patch must follow PAYG_DB_CONNECTION_LIMIT")
	}
}

// Every refusal must leave the overlay exactly as it was: no half-wired PAYG,
// and no login left behind by a run that failed.
func TestWritePAYG_RefusalsLeaveOverlayUntouched(t *testing.T) {
	const uid = "11111111-2222-4333-8444-555555555555"
	cases := []struct {
		name    string
		env     string
		sops    string // "fake" (default), "none", "failing"
		prepare func(t *testing.T, fleet string)
		wantErr error
		wantSub string
	}{
		{name: "provider none", env: strings.Replace(paygBaseEnv, "BILLING_PROVIDER=stripe", "BILLING_PROVIDER=none", 1), wantErr: ErrPAYGRefused, wantSub: "BILLING_PROVIDER=none"},
		{name: "provider unset", env: strings.Replace(paygBaseEnv, "BILLING_PROVIDER=stripe\n", "", 1), wantErr: ErrPAYGRefused, wantSub: "not set"},
		{name: "partner billing marker", env: paygBaseEnv + "CLOUDSIGMA_CHART_VERSION=0.1.0\n", wantErr: ErrPAYGRefused, wantSub: "billed by a partner"},
		{name: "no metering pin", env: "BILLING_PROVIDER=whmcs\n", wantSub: "--set PAYG_METERING_IMAGE="},
		{name: "tag-only pin", env: "BILLING_PROVIDER=stripe\nPAYG_METERING_IMAGE=shalb/kube-dc-metering:v0.9.1\n", wantSub: "not digest-pinned"},
		{name: "malformed UID", env: paygBaseEnv + "PAYG_INSTALLATION_UID=cloud\nPAYG_INSTALLATION_BINDING=c1/example.com\n", wantErr: ErrPAYGIdentity, wantSub: "not a UUID"},
		{name: "UID without binding (copied overlay)", env: paygBaseEnv + "PAYG_INSTALLATION_UID=" + uid + "\n", wantErr: ErrPAYGIdentity, wantSub: "no " + PAYGInstallationBindingKey},
		{name: "UID bound to another installation", env: paygBaseEnv + "PAYG_INSTALLATION_UID=" + uid + "\nPAYG_INSTALLATION_BINDING=other/other.example.com\n", wantErr: ErrPAYGIdentity, wantSub: "bound to other/other.example.com"},
		{name: "UID also in another cluster", env: paygBaseEnv + "PAYG_INSTALLATION_UID=" + uid + "\nPAYG_INSTALLATION_BINDING=c1/example.com\n", wantErr: ErrPAYGIdentity, wantSub: "clusters/c2/cluster-config.env",
			prepare: func(t *testing.T, fleet string) {
				dir := filepath.Join(fleet, "clusters", "c2")
				_ = os.MkdirAll(dir, 0o755)
				if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte("PAYG_INSTALLATION_UID="+uid+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "partially encrypted existing login", env: paygBaseEnv, wantSub: "uri is not SOPS-encrypted",
			prepare: func(t *testing.T, fleet string) {
				if err := os.WriteFile(filepath.Join(fleet, "clusters", "c1", paygDBSecretsFileName), []byte(encryptedLogin(true)), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "unknown database cluster", env: paygBaseEnv + "PAYG_DB_CLUSTER=billing-pg\n", wantSub: "not in this fleet checkout"},
		{name: "conflicting managed patch", env: paygBaseEnv, wantSub: "spec.managed",
			prepare: func(t *testing.T, fleet string) {
				p := filepath.Join(fleet, "clusters", "c1", "platform.yaml")
				body := addClusterPlatformYAML + "    - target:\n        kind: Cluster\n        name: grafana-pg\n      patch: |\n        - op: add\n          path: /spec/managed\n          value: {}\n"
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "no bundle in starter", env: paygBaseEnv, wantSub: "platform/payg/kustomization.yaml",
			prepare: func(t *testing.T, fleet string) { _ = os.RemoveAll(filepath.Join(fleet, paygBundlePath)) }},
		{name: "no sops", env: paygBaseEnv, sops: "none", wantSub: "sops"},
		{name: "sops fails", env: paygBaseEnv, sops: "failing", wantSub: "no age identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.sops {
			case "none":
				t.Setenv("PATH", t.TempDir())
			case "failing":
				failingSopsOnPath(t)
			default:
				fakeSopsOnPath(t)
			}
			fleet := paygFleet(t, tc.env)
			if tc.prepare != nil {
				tc.prepare(t, fleet)
			}
			clusterDir := filepath.Join(fleet, "clusters", "c1")
			before := map[string]string{}
			for _, f := range []string{"cluster-config.env", "kustomization.yaml", "platform.yaml", paygDBSecretsFileName} {
				if b, err := os.ReadFile(filepath.Join(clusterDir, f)); err == nil {
					before[f] = string(b)
				}
			}

			err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("want an error containing %q (is %v), got %v", tc.wantSub, tc.wantErr, err)
			}
			for f, want := range before {
				if got := readFile(t, filepath.Join(clusterDir, f)); got != want {
					t.Errorf("%s changed by a refused run:\n%s", f, got)
				}
			}
			if _, ok := before[paygDBSecretsFileName]; !ok {
				if _, err := os.Stat(filepath.Join(clusterDir, paygDBSecretsFileName)); !os.IsNotExist(err) {
					t.Error("a refused run left a database login behind")
				}
			}
			if _, err := os.Stat(filepath.Join(clusterDir, paygLayerFileName)); !os.IsNotExist(err) {
				t.Error("payg.yaml written despite the refusal")
			}
		})
	}
}

func TestCheckPAYGDBSecret(t *testing.T) {
	if err := checkPAYGDBSecret([]byte(encryptedLogin(false))); err != nil {
		t.Fatalf("fully encrypted login refused: %v", err)
	}
	for name, body := range map[string]string{
		"partially encrypted (uri readable)": encryptedLogin(true),
		"a mention of sops is not metadata":  strings.Replace(encryptedLogin(false), "sops:\n    mac: ENC[AES256_GCM,data:mac,iv:x,tag:y,type:str]\n", "# sops: see README\n", 1),
		"another Secret":                     strings.Replace(encryptedLogin(false), "name: kube-dc-metering-db", "name: grafana-pg-app", 1),
		"another namespace":                  strings.Replace(encryptedLogin(false), "namespace: monitoring", "namespace: kube-dc", 1),
		"missing password":                   strings.Replace(encryptedLogin(false), "    password: ENC[AES256_GCM,data:p,iv:x,tag:y,type:str]\n", "", 1),
		"extra readable value":               strings.Replace(encryptedLogin(false), "stringData:\n", "stringData:\n    host: grafana-pg-rw\n", 1),
		"not YAML":                           "sops: [\nENC[",
	} {
		if err := checkPAYGDBSecret([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPatchPlatformPAYGRole_Shapes(t *testing.T) {
	entry := renderPAYGRolePatch("grafana-pg", 8, paygNoManaged)

	// No patches list yet (the pre-2026-09-10 add-cluster.sh shape): create it.
	got, changed, err := patchPlatformPAYGRole(entry)(strings.Split(genPlatformYAML, "\n"))
	if err != nil || !changed {
		t.Fatalf("no-patches shape: changed=%v err=%v", changed, err)
	}
	var doc struct {
		Spec struct {
			Patches []map[string]any `yaml:"patches"`
			Force   bool             `yaml:"force"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(got, "\n")), &doc); err != nil || len(doc.Spec.Patches) != 1 || !doc.Spec.Force {
		t.Fatalf("created list does not parse as spec.patches (err=%v): %+v\n%s", err, doc, strings.Join(got, "\n"))
	}

	// A patches list followed by another spec key: appending at EOF would put
	// the entry under the wrong key. Refuse.
	if _, _, err := patchPlatformPAYGRole(entry)(strings.Split(addClusterPlatformYAML+"  timeout: 15m\n", "\n")); err == nil {
		t.Fatal("expected a refusal when spec.patches is not the last key")
	}

	// Composes with the other kube-dc platform writers' marker list.
	found := false
	for _, m := range ownedPlatformPatchMarkers {
		if m == paygDBRoleMarker {
			found = true
		}
	}
	if !found {
		t.Error("paygDBRoleMarker must be in ownedPlatformPatchMarkers so the other writers compose with it")
	}
}

func TestNewPAYGInstallationUID_RandomV4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		uid, err := newPAYGInstallationUID()
		if err != nil {
			t.Fatal(err)
		}
		if !v4UUID.MatchString(uid) {
			t.Fatalf("%q is not a v4 UUID", uid)
		}
		if seen[uid] {
			t.Fatalf("duplicate UID %q", uid)
		}
		seen[uid] = true
	}
}

func TestPAYGProductsRevisionAndBinding(t *testing.T) {
	now := time.Date(2026, 10, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600)) // 04:30 UTC on 10-02
	if got := paygProductsRevision("eu/dc1", now); got != "eu-dc1-20261002" {
		t.Errorf("revision %q", got)
	}
	if got := paygBinding("eu/dc1", " Kdc.Example.COM. "); got != "eu/dc1/kdc.example.com" {
		t.Errorf("binding %q", got)
	}
}

// A nested cluster (eu/dc1) inside eu/ is ANOTHER installation for eu.
func TestPAYGUIDElsewhere_NestedClusters(t *testing.T) {
	const uid = "11111111-2222-4333-8444-555555555555"
	fleet := t.TempDir()
	for dir, body := range map[string]string{
		"clusters/eu":     "PAYG_INSTALLATION_UID=" + uid + "\n",
		"clusters/eu/dc1": "PAYG_INSTALLATION_UID=" + uid + "\n",
	} {
		_ = os.MkdirAll(filepath.Join(fleet, dir), 0o755)
		_ = os.WriteFile(filepath.Join(fleet, dir, "cluster-config.env"), []byte(body), 0o644)
	}
	hit, err := paygUIDElsewhere(fleet, filepath.Join(fleet, "clusters", "eu"), uid)
	if err != nil || hit != filepath.Join("clusters", "eu", "dc1", "cluster-config.env") {
		t.Errorf("eu: hit=%q err=%v", hit, err)
	}
	hit, _ = paygUIDElsewhere(fleet, filepath.Join(fleet, "clusters", "eu", "dc1"), uid)
	if hit != filepath.Join("clusters", "eu", "cluster-config.env") {
		t.Errorf("eu/dc1: hit=%q", hit)
	}
}

func TestValidatePAYG(t *testing.T) {
	base := func() *InitOptions {
		return &InitOptions{Name: "atlantis", Domain: "kdc.atlantis.example.com", Sets: map[string]string{}}
	}
	cases := []struct {
		name    string
		mutate  func(*InitOptions)
		wantSub string // "" = valid
	}{
		{"off", func(o *InitOptions) {}, ""},
		{"off without a provider is fine", func(o *InitOptions) { o.Sets[billingProviderKey] = "none" }, ""},
		{"on with stripe", func(o *InitOptions) { o.PAYG = true; o.Sets[billingProviderKey] = "stripe" }, ""},
		{"on with whmcs", func(o *InitOptions) { o.PAYG = true; o.Sets[billingProviderKey] = "whmcs" }, ""},
		{"on without a provider", func(o *InitOptions) { o.PAYG = true }, "not set"},
		{"on with provider none", func(o *InitOptions) { o.PAYG = true; o.Sets[billingProviderKey] = "none" }, "BILLING_PROVIDER=none"},
		{"on with a partner billing marker", func(o *InitOptions) {
			o.PAYG = true
			o.Sets[billingProviderKey] = "stripe"
			o.Sets["CLOUDSIGMA_CHART_VERSION"] = "0.1.0"
		}, "billed by a partner"},
		{"undigested image", func(o *InitOptions) { o.Sets[PAYGMeteringImageKey] = "shalb/kube-dc-metering:latest" }, "digest-pinned"},
		{"switch via --set", func(o *InitOptions) { o.Sets[PAYGEnabledKey] = "true" }, "use --payg"},
		{"copied UID", func(o *InitOptions) { o.Sets[PAYGInstallationUIDKey] = "11111111-2222-4333-8444-555555555555" }, "never copied"},
		{"copied binding", func(o *InitOptions) { o.Sets[PAYGInstallationBindingKey] = "x/y" }, "never copied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := base()
			tc.mutate(o)
			errs := strings.Join(validatePAYG(o), "; ")
			if tc.wantSub == "" && errs != "" {
				t.Fatalf("unexpected errors: %s", errs)
			}
			if tc.wantSub != "" && !strings.Contains(errs, tc.wantSub) {
				t.Fatalf("want %q, got %q", tc.wantSub, errs)
			}
		})
	}
}

func TestCheckPAYGOnResume(t *testing.T) {
	fakeSopsOnPath(t)
	fleet := paygFleet(t, paygBaseEnv)
	clusterDir := filepath.Join(fleet, "clusters", "c1")

	if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, false); err != nil || c {
		t.Fatalf("no request on a non-PAYG overlay: complete=%v err=%v", c, err)
	}
	if _, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); !errors.Is(err, ErrPAYGChangeOnResume) {
		t.Fatalf("--payg on a non-PAYG overlay must refuse, got %v", err)
	}
	if _, err := CheckPAYGOnResume(fleet, "missing", testPAYGDomain, true); !errors.Is(err, ErrPAYGChangeOnResume) {
		t.Fatalf("--payg with no cluster-config.env must be an error, got %v", err)
	}

	if err := WritePAYG(fleet, "c1", testPAYGDomain, true, nil); err != nil {
		t.Fatal(err)
	}
	if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); err != nil || c {
		t.Fatalf("complete overlay: complete=%v err=%v", c, err)
	}

	// Interrupted scaffold: the layer and its kustomization entry are gone.
	_ = os.Remove(filepath.Join(clusterDir, paygLayerFileName))
	kust := filepath.Join(clusterDir, "kustomization.yaml")
	_ = os.WriteFile(kust, []byte(strings.Replace(readFile(t, kust), "  - payg.yaml\n", "", 1)), 0o644)
	if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); err != nil || !c {
		t.Fatalf("partial overlay must ask for completion: complete=%v err=%v", c, err)
	}
	if changed, err := CompletePAYGOnResume(fleet, "c1", testPAYGDomain, nil); err != nil || !changed {
		t.Fatalf("completion: changed=%v err=%v", changed, err)
	}
	if c, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); err != nil || c {
		t.Fatalf("after completion: complete=%v err=%v", c, err)
	}

	// The same overlay resumed as ANOTHER installation (a copy) is refused,
	// with or without --payg.
	for _, requested := range []bool{false, true} {
		if _, err := CheckPAYGOnResume(fleet, "c1", "copy.example.com", requested); !errors.Is(err, ErrPAYGIdentity) {
			t.Errorf("requested=%v: a foreign binding must refuse, got %v", requested, err)
		}
	}

	// A login whose uri is readable is not part of a valid contract.
	_ = os.WriteFile(filepath.Join(clusterDir, paygDBSecretsFileName), []byte(encryptedLogin(true)), 0o644)
	if _, err := CheckPAYGOnResume(fleet, "c1", testPAYGDomain, true); err == nil || !strings.Contains(err.Error(), "uri is not SOPS-encrypted") {
		t.Errorf("a partially encrypted login must refuse, got %v", err)
	}
}

// Clone-from-sibling must never carry a PAYG identity; a saved spec carries
// the --payg decision and nothing else.
func TestPrefill_PAYGIsNeverClonedButRoundTrips(t *testing.T) {
	o := &InitOptions{}
	ignored := ImportMap(o, map[string]string{
		"CLUSTER_NAME":             "clone",
		PAYGEnabledKey:             "true",
		PAYGInstallationUIDKey:     "11111111-2222-4333-8444-555555555555",
		PAYGInstallationBindingKey: "sibling/sibling.example.com",
		PAYGProductsRevisionKey:    "sibling-20260928",
		PAYGMeteringImageKey:       testMeteringImage,
		"PAYG_REGION":              "eu-1",
	}, func(string) bool { return false })
	if o.PAYG {
		t.Fatal("a sibling's PAYG_ENABLED must not turn --payg on")
	}
	for _, k := range []string{PAYGEnabledKey, PAYGInstallationUIDKey, PAYGInstallationBindingKey, PAYGProductsRevisionKey, PAYGMeteringImageKey} {
		if _, ok := o.Sets[k]; ok {
			t.Errorf("%s was cloned into --set", k)
		}
		if !strings.Contains(strings.Join(ignored, ","), k) {
			t.Errorf("%s should be reported as ignored, got %v", k, ignored)
		}
	}
	if !strings.Contains(strings.Join(o.PrefillNotes, "\n"), "pass --payg") {
		t.Errorf("cloning a PAYG installation must say how to opt in, notes: %v", o.PrefillNotes)
	}

	saved := ExportMap(&InitOptions{Name: "n", PAYG: true})
	if saved[KeyPAYG] != "true" {
		t.Fatalf("ExportMap must persist the decision: %v", saved)
	}
	if err := ValidateInputSpec(saved); err != nil {
		t.Fatalf("saved spec invalid: %v", err)
	}
	back := &InitOptions{}
	ImportMap(back, saved, func(string) bool { return false })
	if !back.PAYG {
		t.Fatal("KUBE_DC_INIT_PAYG=true must restore --payg")
	}
	if _, ok := ExportMap(&InitOptions{Name: "n"})[KeyPAYG]; ok {
		t.Error("PAYG off must not be written to a saved spec")
	}
}

// paygAddClusterRunner is the fake add-cluster.sh of the Scaffold/Apply
// tests: the files the real script writes, including the release pins with
// the metering image. It is a no-op on its second call (flux-install).
func paygAddClusterRunner(t *testing.T, repo string) *fakeScriptRunner {
	t.Helper()
	return &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if _, err := os.Stat(filepath.Join(clusterDir, "cluster-config.env")); err == nil {
				return nil
			}
			files := map[string]string{
				"cluster-config.env":  "CLUSTER_NAME=atlantis\nDOMAIN=kdc.atlantis.example.com\nEXT_NET_VLAN_ID=CHANGEME\nEXT_NET_INTERFACE=CHANGEME\n\n# --- Component versions (release pins) ---\nPAYG_METERING_IMAGE=" + testMeteringImage + "\n",
				"infrastructure.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-cni\n  namespace: flux-system\n",
				"platform.yaml":       addClusterPlatformYAML,
				"kustomization.yaml":  scaffoldRootKustomization,
				"secrets.enc.yaml":    "stringData:\n    K: ENC[AES256_GCM,data:abc]\nsops:\n    mac: ENC[AES256_GCM,data:mac]\n",
			}
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(clusterDir, name), []byte(body), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
		lines: []ports.Line{{Stream: ports.StreamExit, Text: "0", Time: time.Now()}},
	}
}

func paygSets(provider string) map[string]string {
	s := map[string]string{
		"EXT_NET_VLAN_ID": "1103", "EXT_NET_INTERFACE": "bond0", "EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR": "203.0.113.48/29", "EXT_PUBLIC_GATEWAY": "203.0.113.49",
	}
	if provider != "" {
		s[billingProviderKey] = provider
	}
	return s
}

// Scaffold end to end with the fake add-cluster.sh: --payg wires PAYG after
// every other writer; without it the CLI writes no PAYG switch, identity,
// layer, login or role. (The starter's metering pin stays in
// cluster-config.env, where nothing references it — inert.)
func TestScaffold_PAYG(t *testing.T) {
	for _, payg := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[payg], func(t *testing.T) {
			fakeSopsOnPath(t)
			fixPAYGClock(t)
			repo := t.TempDir()
			writeStarterScaffoldSources(t, repo)
			writePAYGStarter(t, repo)
			var out bytes.Buffer
			err := Scaffold(context.Background(), ScaffoldOptions{
				Plan:           &Plan{ClusterName: "atlantis", Domain: "kdc.atlantis.example.com", Preset: PresetCloudPublicVLAN},
				FleetRepo:      repo,
				NodeExternalIP: "203.0.113.52",
				Sets:           paygSets("stripe"),
				PAYG:           payg,
				Runner:         paygAddClusterRunner(t, repo),
				Out:            &out,
			})
			if err != nil {
				t.Fatalf("Scaffold: %v\n%s", err, out.String())
			}
			clusterDir := filepath.Join(repo, "clusters", "atlantis")
			env := readFile(t, filepath.Join(clusterDir, "cluster-config.env"))
			kust := readFile(t, filepath.Join(clusterDir, "kustomization.yaml"))
			platform := readFile(t, filepath.Join(clusterDir, "platform.yaml"))
			_, layerErr := os.Stat(filepath.Join(clusterDir, paygLayerFileName))
			_, secretErr := os.Stat(filepath.Join(clusterDir, paygDBSecretsFileName))

			if !payg {
				for _, k := range []string{PAYGEnabledKey, PAYGInstallationUIDKey, PAYGInstallationBindingKey, PAYGProductsRevisionKey} {
					if v := envValue(env, k); v != "" {
						t.Errorf("%s=%q written without --payg", k, v)
					}
				}
				if !os.IsNotExist(layerErr) || !os.IsNotExist(secretErr) {
					t.Error("PAYG files written without --payg")
				}
				if strings.Contains(kust, "payg") || strings.Contains(kust, "metering") || strings.Contains(platform, "kube_dc_metering") {
					t.Errorf("PAYG wiring without --payg:\n%s\n%s", kust, platform)
				}
				if strings.Contains(out.String(), "[scaffold] PAYG") {
					t.Errorf("PAYG output without --payg:\n%s", out.String())
				}
				return
			}
			if envValue(env, PAYGEnabledKey) != "true" || !v4UUID.MatchString(envValue(env, PAYGInstallationUIDKey)) ||
				envValue(env, PAYGInstallationBindingKey) != "atlantis/kdc.atlantis.example.com" ||
				envValue(env, PAYGProductsRevisionKey) != "atlantis-20261001" || envValue(env, billingProviderKey) != "stripe" {
				t.Errorf("PAYG keys not scaffolded:\n%s", env)
			}
			if layerErr != nil || secretErr != nil {
				t.Errorf("PAYG files missing: layer=%v secret=%v", layerErr, secretErr)
			}
			if !strings.Contains(kust, "  - payg.yaml\n") || !strings.Contains(kust, "  - metering-db.enc.yaml\n") {
				t.Errorf("kustomization.yaml not wired:\n%s", kust)
			}
			if !strings.Contains(platform, paygDBRoleMarker) || strings.Count(platform, "  patches:") != 1 {
				t.Errorf("role patch not composed into platform.yaml:\n%s", platform)
			}
		})
	}
}

// The billing rule holds at apply even when the reviewed inputs slipped past
// validation (a programmatic caller): the rendered config is checked again.
func TestScaffold_PAYG_RefusedWithoutKubeDCBilling(t *testing.T) {
	fakeSopsOnPath(t)
	repo := t.TempDir()
	writeStarterScaffoldSources(t, repo)
	writePAYGStarter(t, repo)
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan:           &Plan{ClusterName: "atlantis", Domain: "kdc.atlantis.example.com", Preset: PresetCloudPublicVLAN},
		FleetRepo:      repo,
		NodeExternalIP: "203.0.113.52",
		Sets:           paygSets(""),
		PAYG:           true,
		Runner:         paygAddClusterRunner(t, repo),
	})
	if !errors.Is(err, ErrPAYGRefused) {
		t.Fatalf("want ErrPAYGRefused at apply, got %v", err)
	}
}
