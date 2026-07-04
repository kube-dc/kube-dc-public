package clusterinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"gopkg.in/yaml.v3"
)

func TestBuildCustomInterfacesPatch_Empty_ReturnsEmpty(t *testing.T) {
	got, err := BuildCustomInterfacesPatch(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("empty NodeNICs should return empty string, got %q", got)
	}
}

func TestBuildCustomInterfacesPatch_GroupsByInterface(t *testing.T) {
	// Three nodes, two share an iface — must produce two groups
	// (not three) with the shared-iface group listing both nodes.
	nics := map[string]string{
		"SRV5-Kub1": "enp1s0",
		"SRV6-Kub1": "enp1s0",
		"SRV7-Kub1": "eno2",
	}
	got, err := BuildCustomInterfacesPatch(nics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both interfaces present.
	if !strings.Contains(got, "interface: enp1s0") {
		t.Errorf("missing enp1s0 group:\n%s", got)
	}
	if !strings.Contains(got, "interface: eno2") {
		t.Errorf("missing eno2 group:\n%s", got)
	}
	// Shared interface lists both nodes.
	idx := strings.Index(got, "interface: enp1s0")
	groupTail := got[idx:]
	if !strings.Contains(groupTail, "SRV5-Kub1") || !strings.Contains(groupTail, "SRV6-Kub1") {
		t.Errorf("enp1s0 group should list both SRV5-Kub1 and SRV6-Kub1:\n%s", got)
	}
	// Sole-node group lists just that node.
	idx = strings.Index(got, "interface: eno2")
	groupTail = got[idx:]
	if !strings.Contains(groupTail, "SRV7-Kub1") {
		t.Errorf("eno2 group should list SRV7-Kub1:\n%s", got)
	}
}

func TestBuildCustomInterfacesPatch_Deterministic(t *testing.T) {
	// Same input → byte-identical output, regardless of Go map
	// iteration order. The test runs BuildCustomInterfacesPatch
	// many times to amplify any iteration-order leakage.
	nics := map[string]string{
		"node-a": "enp0",
		"node-b": "enp1",
		"node-c": "enp0",
		"node-d": "enp2",
		"node-e": "enp1",
	}
	first, err := BuildCustomInterfacesPatch(nics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 50; i++ {
		next, _ := BuildCustomInterfacesPatch(nics)
		if next != first {
			t.Fatalf("non-deterministic output:\nFIRST:\n%s\nDIFF (iter %d):\n%s", first, i, next)
		}
	}
}

func TestBuildCustomInterfacesPatch_TargetShape(t *testing.T) {
	// The patch's `target:` block must match the canonical
	// clusters/cloud/infrastructure.yaml shape — group, version,
	// kind, name. The name placeholder is the literal
	// `${EXT_NET_NAME}` because Flux's postBuild substitutes it.
	got, err := BuildCustomInterfacesPatch(map[string]string{"n": "enp0"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{
		"group: kubeovn.io",
		"version: v1",
		"kind: ProviderNetwork",
		"name: ${EXT_NET_NAME}",
		"op: add",
		"path: /spec/customInterfaces",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patch missing %q:\n%s", want, got)
		}
	}
}

func TestWriteCustomInterfacesPatch_AppendsToInfraCore(t *testing.T) {
	// Sample infrastructure.yaml shape closely matching what
	// add-cluster.sh writes — two Kustomization docs: infra-cni +
	// infra-core. Only infra-core should be modified.
	body := `# Layer 1a: CNI
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-cni
  namespace: flux-system
spec:
  interval: 10m
  path: ./infrastructure/cni
---
# Layer 1b: Core infra
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-cni
  interval: 10m
  path: ./infrastructure/core
`
	dir := t.TempDir()
	path := filepath.Join(dir, "infrastructure.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCustomInterfacesPatch(path, map[string]string{
		"SRV5-Kub1": "enp1s0",
		"SRV6-Kub1": "enp1s0",
		"SRV7-Kub1": "eno2",
	}); err != nil {
		t.Fatalf("WriteCustomInterfacesPatch: %v", err)
	}

	got, _ := os.ReadFile(path)
	out := string(got)

	// The patch lives in infra-core's spec — sanity check by
	// re-parsing the file and confirming infra-core has `patches`
	// while infra-cni does NOT.
	docs := splitYAMLDocs(t, out)
	for _, doc := range docs {
		root := doc.Content[0]
		nameNode := mappingGet(mappingGet(root, "metadata"), "name")
		if nameNode == nil {
			continue
		}
		spec := mappingGet(root, "spec")
		patches := mappingGet(spec, "patches")
		switch nameNode.Value {
		case "infra-core":
			if patches == nil {
				t.Errorf("infra-core should have patches; full:\n%s", out)
			}
		case "infra-cni":
			if patches != nil {
				t.Errorf("infra-cni should NOT have patches (only infra-core); full:\n%s", out)
			}
		}
	}
	// And the customInterfaces JSON-patch op text should appear.
	for _, want := range []string{
		"customInterfaces",
		"op: add",
		"SRV5-Kub1",
		"SRV6-Kub1",
		"SRV7-Kub1",
		"enp1s0",
		"eno2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteCustomInterfacesPatch_Empty_NoOp(t *testing.T) {
	body := "apiVersion: v1\nkind: Test\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "infrastructure.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCustomInterfacesPatch(path, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != body {
		t.Errorf("empty NodeNICs should be a no-op:\nGOT:\n%s\nWANT:\n%s", got, body)
	}
}

func TestWriteCustomInterfacesPatch_NoInfraCore_Errors(t *testing.T) {
	// A malformed infrastructure.yaml without an infra-core
	// Kustomization must surface ErrCustomIfacesNoInfraCore.
	body := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-cni
  namespace: flux-system
spec:
  interval: 10m
`
	dir := t.TempDir()
	path := filepath.Join(dir, "infrastructure.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteCustomInterfacesPatch(path, map[string]string{"n": "enp0"})
	if !errors.Is(err, ErrCustomIfacesNoInfraCore) {
		t.Fatalf("expected ErrCustomIfacesNoInfraCore, got %v", err)
	}
}

func TestWriteCustomInterfacesPatch_AtomicWrite_NoTempLeak(t *testing.T) {
	body := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
  namespace: flux-system
spec:
  interval: 10m
`
	dir := t.TempDir()
	path := filepath.Join(dir, "infrastructure.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCustomInterfacesPatch(path, map[string]string{"n": "enp0"}); err != nil {
		t.Fatalf("WriteCustomInterfacesPatch: %v", err)
	}
	// No .tmp residue after success.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q leaked", e.Name())
		}
	}
}

func TestWriteCustomInterfacesPatch_ReplacesExistingPatches(t *testing.T) {
	// If a previous run wrote stale entries (operator re-running
	// with a different --node-nic set), the new patch must REPLACE
	// not accumulate. Verify by checking that the old node name
	// doesn't survive the second write.
	body := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
spec:
  interval: 10m
`
	dir := t.TempDir()
	path := filepath.Join(dir, "infrastructure.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// First pass — STALE-NODE.
	if err := WriteCustomInterfacesPatch(path, map[string]string{"STALE-NODE": "enp0"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	stale, _ := os.ReadFile(path)
	if !strings.Contains(string(stale), "STALE-NODE") {
		t.Fatal("setup: STALE-NODE didn't land in first pass")
	}
	// Second pass — FRESH-NODE only.
	if err := WriteCustomInterfacesPatch(path, map[string]string{"FRESH-NODE": "enp1"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	fresh, _ := os.ReadFile(path)
	if strings.Contains(string(fresh), "STALE-NODE") {
		t.Errorf("STALE-NODE survived second write — patches accumulated:\n%s", fresh)
	}
	if !strings.Contains(string(fresh), "FRESH-NODE") {
		t.Errorf("FRESH-NODE missing from second write:\n%s", fresh)
	}
}

// splitYAMLDocs decodes a multi-document YAML stream into Document
// nodes — local helper for test assertions that need to inspect
// individual docs.
func splitYAMLDocs(t *testing.T, body string) []*yaml.Node {
	t.Helper()
	var docs []*yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(body))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			break
		}
		docs = append(docs, &n)
	}
	return docs
}

func TestScaffold_AppliesCustomInterfacesWhenNodeNICsSet(t *testing.T) {
	// Integration: Scaffold with NodeNICs set must invoke the
	// customInterfaces patch step and surface the "[scaffold]
	// customInterfaces patch applied" log line.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	envBody := "CLUSTER_NAME=test\n"
	infraBody := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-cni
spec:
  interval: 10m
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
spec:
  dependsOn:
    - name: infra-cni
  interval: 10m
`
	encryptedSecrets := `stringData:
    KEYCLOAK_ADMIN_PASSWORD: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
sops:
    mac: ENC[AES256_GCM,data:mac,iv:miv]
`
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte(envBody), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "infrastructure.yaml"), []byte(infraBody), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(encryptedSecrets), 0o644)
		},
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> Creating cluster overlay", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}
	plan := &Plan{
		ClusterName: "test",
		Domain:      "test.example.com",
		Preset:      PresetCloudPublicVLAN,
	}
	sets := map[string]string{
		"EXT_NET_VLAN_ID":    "1100",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1200",
		"EXT_PUBLIC_CIDR":    "10.0.0.0/24",
		"EXT_PUBLIC_GATEWAY": "10.0.0.1",
	}
	var out strings.Builder
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan:           plan,
		FleetRepo:      repo,
		NodeExternalIP: "1.2.3.4",
		Sets:           sets,
		NodeNICs:       map[string]string{"SRV5-Kub1": "enp1s0", "SRV6-Kub1": "enp1s0"},
		Runner:         runner,
		Out:            &out,
	})
	if err != nil {
		t.Fatalf("Scaffold: %v\nout:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "customInterfaces patch applied (2 nodes)") {
		t.Errorf("missing customInterfaces log line:\n%s", out.String())
	}
	// The infra-core doc must now carry the patch.
	infra, _ := os.ReadFile(filepath.Join(repo, "clusters", "test", "infrastructure.yaml"))
	if !strings.Contains(string(infra), "customInterfaces") {
		t.Errorf("infrastructure.yaml missing customInterfaces:\n%s", infra)
	}
	if !strings.Contains(string(infra), "enp1s0") {
		t.Errorf("infrastructure.yaml missing enp1s0:\n%s", infra)
	}
}

