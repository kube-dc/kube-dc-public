package clusterinit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseOverlay lays down the files add-cluster.sh produces, so the tests
// exercise WriteAddons against the real starting state.
func baseOverlay(t *testing.T, env string) (repo, cluster string) {
	t.Helper()
	repo, cluster = t.TempDir(), "atlantis"
	for _, source := range []string{addonPathMetalLB, addonPathConfigL2, addonPathConfigBGP} {
		dir := filepath.Join(repo, source)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(repo, "clusters", cluster)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	root := `apiVersion: kustomize.config.k8s.io/v1beta1
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
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	platform := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  path: ./platform
`
	if err := os.WriteFile(filepath.Join(dir, "platform.yaml"), []byte(platform), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, cluster
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

// TestWriteAddons_MetalLBDefault_EmitsBothLayers — the on-prem regression.
// INGRESS_MODE=metallb-lb with all METALLB_* keys present produced NO
// metallb-system namespace and no IPAddressPool, because the addons layers
// were never scaffolded. The Gateway then bound a node IP and
// METALLB_FLOATING_IP went unused.
func TestWriteAddons_MetalLBDefault_EmitsBothLayers(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\nMETALLB_MODE=l2\n"+
		"METALLB_FLOATING_IP=198.51.100.2\nMETALLB_INTERFACE=br-ext-cloud\n")
	var out bytes.Buffer
	if err := WriteAddons(repo, cluster, &out); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	dir := filepath.Join(repo, "clusters", cluster)
	for _, f := range []string{
		"addons.yaml", "addons-config.yaml",
		"addons/kustomization.yaml", "addons-config/kustomization.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to be scaffolded: %v", f, err)
		}
	}
	root := read(t, dir, "kustomization.yaml")
	for _, e := range []string{"- addons.yaml", "- addons-config.yaml"} {
		if !strings.Contains(root, e) {
			t.Errorf("root kustomization missing %q — Flux would never see the layer:\n%s", e, root)
		}
	}
	// The CRs must not ride along with the operator.
	if got := read(t, dir, "addons/kustomization.yaml"); !strings.Contains(got, addonPathMetalLB) ||
		strings.Contains(got, addonPathConfigL2) {
		t.Errorf("addons/ must install the operator ONLY (CRs belong in addons-config):\n%s", got)
	}
	if got := read(t, dir, "addons-config/kustomization.yaml"); !strings.Contains(got, addonPathConfigL2) {
		t.Errorf("addons-config/ must reference metallb-config:\n%s", got)
	}
	if !strings.Contains(out.String(), "addons wired") {
		t.Errorf("expected a scaffold line, got %q", out.String())
	}
}

// TestWriteAddons_OrderingIsHealthGated — applying the MetalLB CRs before
// the operator's CRDs exist fails SSA dry-run with `no matches for kind
// "L2Advertisement"` and wedges the layer. dependsOn alone does not prevent
// it; the healthCheck on metallb-controller is what makes the ordering real.
func TestWriteAddons_OrderingIsHealthGated(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n")
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	dir := filepath.Join(repo, "clusters", cluster)
	cfg := read(t, dir, "addons-config.yaml")
	if !strings.Contains(cfg, "dependsOn") || !strings.Contains(cfg, "name: addons") {
		t.Error("addons-config must dependOn addons")
	}
	if !strings.Contains(cfg, "healthChecks") || !strings.Contains(cfg, metallbControllerName) {
		t.Errorf("addons-config must health-gate on %s:\n%s", metallbControllerName, cfg)
	}
	if a := read(t, dir, "addons.yaml"); !strings.Contains(a, "name: platform") {
		t.Error("addons must dependOn platform")
	}
}

// TestWriteAddons_BGPModeSelectsBGPConfig — METALLB_MODE=bgp advertises
// each VIP as a /32 to a peer and needs the BGP config tree, not the L2 one.
func TestWriteAddons_BGPModeSelectsBGPConfig(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\nMETALLB_MODE=bgp\n")
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	got := read(t, filepath.Join(repo, "clusters", cluster), "addons-config/kustomization.yaml")
	if !strings.Contains(got, addonPathConfigBGP) || strings.Contains(got, addonPathConfigL2+"\n") {
		t.Errorf("METALLB_MODE=bgp must select metallb-config-bgp:\n%s", got)
	}
}

// TestWriteAddons_HostNetworkIsNoOp — Envoy on hostNetwork needs no
// LoadBalancer, so installing MetalLB would be pure noise.
func TestWriteAddons_HostNetworkIsNoOp(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=hostnetwork\n")
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", cluster, "addons.yaml")); !os.IsNotExist(err) {
		t.Error("hostnetwork must not scaffold the addons layer")
	}
}

// TestWriteAddons_UnsetIngressModeDefaultsToMetalLB — the scaffold default
// is metallb-lb, so treating an absent key as "no ingress" would recreate
// the silent omission.
func TestWriteAddons_UnsetIngressModeDefaultsToMetalLB(t *testing.T) {
	repo, cluster := baseOverlay(t, "DOMAIN=example.test\n")
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "clusters", cluster, "addons.yaml")); err != nil {
		t.Errorf("absent INGRESS_MODE must follow the metallb-lb default: %v", err)
	}
}

// TestWriteAddons_PartialStateConverges — a run interrupted after the layer
// files were written must still gain its root-kustomization entries. An
// "already wired" short-circuit keyed on the first file would skip them
// forever, leaving files Flux never reads.
func TestWriteAddons_PartialStateConverges(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := os.WriteFile(filepath.Join(dir, "addons.yaml"), []byte(addonsLayerYAML(cluster)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	root := read(t, dir, "kustomization.yaml")
	for _, e := range []string{"- addons.yaml", "- addons-config.yaml"} {
		if !strings.Contains(root, e) {
			t.Errorf("partial state did not converge — missing %q:\n%s", e, root)
		}
	}
}

// TestWriteAddons_Idempotent — re-running must not duplicate entries;
// kustomize rejects a repeated resource outright.
func TestWriteAddons_Idempotent(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n")
	for i := 0; i < 3; i++ {
		if err := WriteAddons(repo, cluster, nil); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	root := read(t, filepath.Join(repo, "clusters", cluster), "kustomization.yaml")
	if n := strings.Count(root, "- addons.yaml"); n != 1 {
		t.Errorf("addons.yaml listed %d times, want 1:\n%s", n, root)
	}
	if n := strings.Count(root, "- addons-config.yaml"); n != 1 {
		t.Errorf("addons-config.yaml listed %d times, want 1:\n%s", n, root)
	}
}

// TestWriteAddons_NeverClobbersOperatorEdits — operators customise these;
// an existing cluster adds a SOPS-encrypted Keycloak client secret to
// addons/. Rewriting the file on a resume would silently discard that.
func TestWriteAddons_NeverClobbersOperatorEdits(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " +
		addonPathMetalLB + "\n  - keycloak-backend-client-secret.enc.yaml\n"
	if err := os.WriteFile(filepath.Join(dir, "addons/kustomization.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	if got := read(t, dir, "addons/kustomization.yaml"); got != custom {
		t.Errorf("operator customisation was overwritten:\ngot:\n%s\nwant:\n%s", got, custom)
	}
}

// TestWriteAddons_GatewayVIPPatch_WhenVIPDiffersFromNodeIP — the Envoy
// Service sat at <pending> forever with a correct MetalLB pool beside it,
// because gateway.yaml asks for ${NODE_EXTERNAL_IP} and MetalLB will not
// hand out an address outside its pool. Normal as soon as the node LAN and
// the ingress VLAN are separate segments.
func TestWriteAddons_GatewayVIPPatch_WhenVIPDiffersFromNodeIP(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n"+
		"NODE_EXTERNAL_IP=203.0.113.11\nMETALLB_FLOATING_IP=198.51.100.2\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	got := read(t, dir, "platform.yaml")
	if !strings.Contains(got, "patches:") || !strings.Contains(got, gatewayVIPMarker) ||
		!strings.Contains(got, gatewayServiceVIPMarker) {
		t.Fatalf("expected a Gateway address patch:\n%s", got)
	}
	if !strings.Contains(got, `value: "198.51.100.2"`) ||
		!strings.Contains(got, `metallb.universe.tf/loadBalancerIPs: "198.51.100.2"`) {
		t.Errorf("patch must carry the resolved VIP literal:\n%s", got)
	}
	// A variable here would need a second substitution pass that may not run.
	if strings.Contains(got, "${METALLB_FLOATING_IP}") {
		t.Errorf("patch must not defer to envsubst:\n%s", got)
	}
	// Idempotent — a duplicated patch entry would apply twice.
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, marker := range []string{gatewayVIPMarker, gatewayServiceVIPMarker} {
		if n := strings.Count(read(t, dir, "platform.yaml"), marker); n != 1 {
			t.Errorf("patch %q written %d times, want 1", marker, n)
		}
	}
}

func TestWriteAddons_GatewayVIPPatch_RerunTracksVIP(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n"+
		"NODE_EXTERNAL_IP=192.0.2.11\nMETALLB_FLOATING_IP=192.0.2.20\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte(
		"INGRESS_MODE=metallb-lb\nNODE_EXTERNAL_IP=192.0.2.11\nMETALLB_FLOATING_IP=192.0.2.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	got := read(t, dir, "platform.yaml")
	if strings.Contains(got, "192.0.2.20") {
		t.Errorf("rerun left the old VIP in a managed patch:\n%s", got)
	}
	if n := strings.Count(got, "192.0.2.21"); n != 2 {
		t.Errorf("new VIP appears %d times, want Gateway and Service patches:\n%s", n, got)
	}
}

// TestWriteAddons_ServiceRequest_WhenVIPMatchesNodeIP — the Gateway address
// needs no override, but autoAssign: false still requires the Service request.
func TestWriteAddons_ServiceRequest_WhenVIPMatchesNodeIP(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n"+
		"NODE_EXTERNAL_IP=203.0.113.11\nMETALLB_FLOATING_IP=203.0.113.11\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	got := read(t, dir, "platform.yaml")
	if strings.Contains(got, gatewayVIPMarker) {
		t.Error("no Gateway address patch expected when the VIP equals the node IP")
	}
	if !strings.Contains(got, gatewayServiceVIPMarker) ||
		!strings.Contains(got, `metallb.universe.tf/loadBalancerIPs: "203.0.113.11"`) {
		t.Errorf("autoAssign=false still requires an explicit Service request:\n%s", got)
	}
}

// TestWriteAddons_NoGatewayPatch_WhenVIPUnset — cloud/stage leave
// METALLB_FLOATING_IP empty and must keep the node address.
func TestWriteAddons_NoGatewayPatch_WhenVIPUnset(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\nNODE_EXTERNAL_IP=203.0.113.11\n")
	dir := filepath.Join(repo, "clusters", cluster)
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	if got := read(t, dir, "platform.yaml"); strings.Contains(got, gatewayVIPMarker) || strings.Contains(got, gatewayServiceVIPMarker) {
		t.Error("no patch expected when METALLB_FLOATING_IP is unset")
	}
}

func TestWriteAddons_NestedClusterUsesCorrectRelativePaths(t *testing.T) {
	repo, _ := baseOverlay(t, "INGRESS_MODE=metallb-lb\nMETALLB_MODE=l2\n")
	flat := filepath.Join(repo, "clusters", "atlantis")
	nested := filepath.Join(repo, "clusters", "region", "atlantis")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(flat, nested); err != nil {
		t.Fatal(err)
	}
	if err := WriteAddons(repo, "region/atlantis", nil); err != nil {
		t.Fatalf("WriteAddons nested: %v", err)
	}
	operator := read(t, nested, "addons/kustomization.yaml")
	config := read(t, nested, "addons-config/kustomization.yaml")
	if !strings.Contains(operator, "../../../../addons/metallb") {
		t.Errorf("nested operator path does not reach fleet root:\n%s", operator)
	}
	if !strings.Contains(config, "../../../../addons/metallb-config") {
		t.Errorf("nested config path does not reach fleet root:\n%s", config)
	}
}

func TestWriteAddons_MissingStarterSourceFailsBeforeWriting(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\nMETALLB_MODE=l2\n")
	if err := os.Rename(filepath.Join(repo, addonPathConfigL2), filepath.Join(repo, addonPathConfigL2+".missing")); err != nil {
		t.Fatal(err)
	}
	err := WriteAddons(repo, cluster, nil)
	if err == nil || !strings.Contains(err.Error(), "starter is missing") {
		t.Fatalf("expected actionable starter-version error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "clusters", cluster, "addons.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("writer must not leave partial files on source validation failure: %v", statErr)
	}
}
