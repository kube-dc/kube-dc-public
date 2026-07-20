package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeNodeLabels is a minimal NodeLabelsProvider for hermetic tests
// — no client-go, no apiserver. Test cases wire the raw label map
// they want the detector to see.
type fakeNodeLabels struct {
	labels map[string]map[string]string
	err    error
	calls  int
}

func (f *fakeNodeLabels) NodeLabels(_ context.Context) (map[string]map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.labels, nil
}

// TestNFDDetect_NoNFDInstalled — cluster with vanilla node labels
// (no feature.node.kubernetes.io/* prefix anywhere). Detector must
// report NFDInstalled=false and empty capability lists.
func TestNFDDetect_NoNFDInstalled(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"host1-a": {"kubernetes.io/hostname": "host1-a"},
		"host2-a": {"kubernetes.io/hostname": "host2-a"},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.NFDInstalled {
		t.Error("NFDInstalled must be false when no feature.node.kubernetes.io labels present")
	}
	if r.CompositesInstalled {
		t.Error("CompositesInstalled must be false")
	}
	if r.TotalNodes != 2 {
		t.Errorf("TotalNodes = %d, want 2", r.TotalNodes)
	}
	if r.KubeVirtEligibleCount() != 0 || r.NvidiaGPUCount() != 0 || r.AMDGPUCount() != 0 {
		t.Errorf("capability counts must be zero: %+v", r)
	}
}

// TestNFDDetect_RawNFDOnly_KubeVirtEligible — NFD is installed but
// the M6-T02 composites aren't yet deployed. Detector falls back
// to raw NFD labels. Full truth table for the (virt-flag, kvm)
// pair, plus a "no NFD virt labels at all" case:
//
//	VMX ∧ kvm  → eligible (Intel, typical)
//	SVM ∧ kvm  → eligible (AMD, typical)
//	VMX ∧ ¬kvm → NOT eligible (Intel host that hasn't
//	             loaded the kvm module — e.g. bare-metal
//	             container platform, sysadmin removed kvm)
//	SVM ∧ ¬kvm → NOT eligible (AMD, same shape as above —
//	             mirror of vmx-no-kvm; added 2026-07-02 for
//	             symmetric coverage after the M4-T08 review)
//	¬VMX ∧ ¬SVM ∧ kvm → NOT eligible (kvm module without any
//	             hardware-virt flag; near-impossible on x86 —
//	             the module load would fail without VMX/SVM —
//	             but a defensive assertion catches a future
//	             arm64/riscv path where CPUID flags aren't
//	             the right signal at all)
//	no virt labels at all → NOT eligible (container-only host)
func TestNFDDetect_RawNFDOnly_KubeVirtEligible(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"intel-node": {
			"feature.node.kubernetes.io/cpu-cpuid.VMX":     "true",
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
		"amd-node": {
			"feature.node.kubernetes.io/cpu-cpuid.SVM":     "true",
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
		"vmx-no-kvm-node": {
			// VT-x capable but KVM module not loaded — not eligible.
			"feature.node.kubernetes.io/cpu-cpuid.VMX": "true",
		},
		"svm-no-kvm-node": {
			// AMD-V capable but KVM module not loaded — not eligible.
			// Mirror of vmx-no-kvm-node to prove the AND branch
			// works symmetrically for AMD hosts.
			"feature.node.kubernetes.io/cpu-cpuid.SVM": "true",
		},
		"kvm-no-virt-node": {
			// kvm module loaded without any hardware-virt cpuid flag.
			// Near-impossible on x86 (module init would fail without
			// VMX/SVM) but defensive against future arm64/riscv NFD
			// paths where the CPUID flag isn't the right signal.
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
		"container-only-node": {
			// No CPUID VMX/SVM at all — not eligible.
			"feature.node.kubernetes.io/cpu-hardware_multithreading": "true",
		},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.NFDInstalled {
		t.Error("NFDInstalled must be true — labels present")
	}
	if r.CompositesInstalled {
		t.Error("CompositesInstalled must be false — no kube-dc.com/* labels")
	}
	want := []string{"amd-node", "intel-node"}
	if !equalStringsNFD(r.KubeVirtEligibleNodes, want) {
		t.Errorf("KubeVirtEligibleNodes = %v, want %v (sorted)", r.KubeVirtEligibleNodes, want)
	}
}

// TestNFDDetect_RawNFDOnly_Fallback_LoadedmoduleLabel — some NFD
// deployments emit `kernel-loadedmodule.kvm` instead of
// `kernel-module.kvm`. Detector must accept both aliases so the
// raw-fallback path isn't fragile to NFD's own version/config.
func TestNFDDetect_RawNFDOnly_Fallback_LoadedmoduleLabel(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"intel-node-alias": {
			"feature.node.kubernetes.io/cpu-cpuid.VMX":           "true",
			"feature.node.kubernetes.io/kernel-loadedmodule.kvm": "true",
		},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.KubeVirtEligibleCount() != 1 {
		t.Errorf("expected 1 kubevirt-eligible node via loadedmodule alias, got %d", r.KubeVirtEligibleCount())
	}
}

// TestNFDDetect_RawNFDOnly_GPUsRequireDisplayClass proves raw fallback
// accepts only PCI display/3D controller classes and never vendor-only labels.
func TestNFDDetect_RawNFDOnly_GPUsRequireDisplayClass(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"gpu-node-classy": {
			// Default NFD shape: pci-<class>_<vendor>. 0300 = display.
			"feature.node.kubernetes.io/pci-0300_10de.present": "true",
		},
		"gpu-node-3d": {
			"feature.node.kubernetes.io/pci-0302_1002.present": "true",
		},
		"vendor-only-false-positive": {
			// Could represent audio/USB/encryption; must never count.
			"feature.node.kubernetes.io/pci-1002.present": "true",
		},
		"vendor-audio-false-positive": {
			"feature.node.kubernetes.io/pci-0403_10de.present": "true",
		},
		"cpu-only-node": {
			// Non-GPU PCI (e.g. Intel Ethernet 8086) shouldn't count.
			"feature.node.kubernetes.io/pci-0200_8086.present": "true",
		},
		"gpu-with-false-value": {
			// Present-value != "true" must not count (defensive).
			"feature.node.kubernetes.io/pci-10de.present": "false",
		},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !equalStringsNFD(r.NvidiaGPUNodes, []string{"gpu-node-classy"}) {
		t.Errorf("NvidiaGPUNodes = %v, want [gpu-node-classy]", r.NvidiaGPUNodes)
	}
	if !equalStringsNFD(r.AMDGPUNodes, []string{"gpu-node-3d"}) {
		t.Errorf("AMDGPUNodes = %v, want [gpu-node-3d]", r.AMDGPUNodes)
	}
}

// TestNFDDetect_CompositesInstalled_PreferredOverRaw — when M6-T02
// composites are on the cluster, the detector must use them
// authoritatively — even if the raw labels would say something
// different. Regression for "composite overrides raw" ordering.
func TestNFDDetect_CompositesInstalled_PreferredOverRaw(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"trusted-composite-yes": {
			// Composite says yes; raw would also say yes.
			"kube-dc.com/kubevirt-eligible":                "true",
			"feature.node.kubernetes.io/cpu-cpuid.VMX":     "true",
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
		"trusted-composite-no": {
			// Composite says no — even though raw VMX+kvm would say
			// yes. M6-T02's rule saw a reason to exclude (e.g. host
			// tainted, /dev/kvm not accessible, whatever). Detector
			// must trust the composite.
			"kube-dc.com/kubevirt-eligible":                "false",
			"feature.node.kubernetes.io/cpu-cpuid.VMX":     "true",
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
		"trusted-composite-nvidia": {
			"kube-dc.com/gpu.nvidia": "true",
			// No raw pci-10de present — composite is the ONLY signal.
		},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.CompositesInstalled {
		t.Error("CompositesInstalled must be true when kube-dc.com/* labels exist")
	}
	// Only the yes-composite node is kubevirt-eligible.
	want := []string{"trusted-composite-yes"}
	if !equalStringsNFD(r.KubeVirtEligibleNodes, want) {
		t.Errorf("KubeVirtEligibleNodes = %v, want %v (composite=false must override raw=true)", r.KubeVirtEligibleNodes, want)
	}
	// NVIDIA composite is authoritative even without raw pci-10de.
	if !equalStringsNFD(r.NvidiaGPUNodes, []string{"trusted-composite-nvidia"}) {
		t.Errorf("NvidiaGPUNodes = %v, want [trusted-composite-nvidia]", r.NvidiaGPUNodes)
	}
}

// TestNFDDetect_MixedCluster — realistic shape: 3 nodes, 2
// kubevirt-eligible, 1 with GPU. Composites installed.
func TestNFDDetect_MixedCluster(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"host5-a": {
			"kube-dc.com/kubevirt-eligible": "true",
		},
		"host6-a": {
			"kube-dc.com/kubevirt-eligible": "true",
			"kube-dc.com/gpu.nvidia":        "true",
		},
		"host7-a": {
			// Not kubevirt-eligible (arm64 or container-only node)
			"kube-dc.com/kubevirt-eligible": "false",
		},
	}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TotalNodes != 3 {
		t.Errorf("TotalNodes = %d, want 3", r.TotalNodes)
	}
	if !equalStringsNFD(r.KubeVirtEligibleNodes, []string{"host5-a", "host6-a"}) {
		t.Errorf("KubeVirtEligibleNodes = %v", r.KubeVirtEligibleNodes)
	}
	if !equalStringsNFD(r.NvidiaGPUNodes, []string{"host6-a"}) {
		t.Errorf("NvidiaGPUNodes = %v", r.NvidiaGPUNodes)
	}
	if r.AMDGPUCount() != 0 {
		t.Errorf("AMDGPUCount = %d, want 0", r.AMDGPUCount())
	}
}

// TestNFDDetect_EmptyCluster — a fresh cluster with no nodes at all.
// TotalNodes=0, capability lists nil, no error.
func TestNFDDetect_EmptyCluster(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{}}
	r, err := NFDDetect(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TotalNodes != 0 || r.NFDInstalled || r.CompositesInstalled {
		t.Errorf("expected zero-value result, got %+v", r)
	}
}

// TestNFDDetect_NodeLabelsErrorPropagates — a real K8sClient can
// fail (RBAC denied, apiserver unreachable). Detector must surface
// the error, not silently return an empty result.
func TestNFDDetect_NodeLabelsErrorPropagates(t *testing.T) {
	sentinel := errors.New("apiserver unreachable")
	src := &fakeNodeLabels{err: sentinel}
	_, err := NFDDetect(context.Background(), src)
	if err == nil || !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel error, got: %v", err)
	}
}

// TestNFDDetect_NilProvider — defensive guard.
func TestNFDDetect_NilProvider(t *testing.T) {
	_, err := NFDDetect(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil provider")
	}
}

// TestNFDDetect_DeterministicOrdering — Go map iteration is
// randomised; the detector sorts node names alphabetically so
// consumer-facing output (doctor row, preset error messages,
// snapshot tests) stays stable across runs. This test would flake
// under randomised order — its passing is the guarantee.
func TestNFDDetect_DeterministicOrdering(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"zzz-node": {"kube-dc.com/kubevirt-eligible": "true"},
		"aaa-node": {"kube-dc.com/kubevirt-eligible": "true"},
		"mmm-node": {"kube-dc.com/kubevirt-eligible": "true"},
	}}
	for i := 0; i < 20; i++ {
		r, err := NFDDetect(context.Background(), src)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		want := []string{"aaa-node", "mmm-node", "zzz-node"}
		if !equalStringsNFD(r.KubeVirtEligibleNodes, want) {
			t.Errorf("iter %d: order not stable: %v", i, r.KubeVirtEligibleNodes)
		}
	}
}

// ---------- NFDProbe (doctor-row wrapper) ----------

// TestNFDProbe_Installed — composites + NFD both live; probe emits
// StatusInstalled + Info severity + a detail line naming the counts.
func TestNFDProbe_Installed(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"srv1": {"kube-dc.com/kubevirt-eligible": "true"},
	}}
	res := NewNFDProbe(src).Run(context.Background())
	if res.Status != ports.StatusInstalled {
		t.Errorf("Status = %v, want StatusInstalled", res.Status)
	}
	if res.Severity != ports.SeverityInfo {
		t.Errorf("Severity = %v, want SeverityInfo", res.Severity)
	}
	for _, want := range []string{"NFD + composites", "1 kubevirt-eligible", "1 nodes"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("Detail missing %q: %s", want, res.Detail)
		}
	}
}

// TestNFDProbe_PartialRawOnly — NFD present, composites absent.
// Result must be StatusPartial + Info + a hint pointing at M6-T02.
func TestNFDProbe_PartialRawOnly(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"srv1": {
			"feature.node.kubernetes.io/cpu-cpuid.VMX":     "true",
			"feature.node.kubernetes.io/kernel-module.kvm": "true",
		},
	}}
	res := NewNFDProbe(src).Run(context.Background())
	if res.Status != ports.StatusPartial {
		t.Errorf("Status = %v, want StatusPartial", res.Status)
	}
	if res.Severity != ports.SeverityInfo {
		t.Errorf("Severity = %v, want SeverityInfo", res.Severity)
	}
	if !strings.Contains(res.Detail, "raw fallback") {
		t.Errorf("Detail must mention raw fallback: %s", res.Detail)
	}
	if !strings.Contains(res.FixHint.Text, "M6-T02") {
		t.Errorf("FixHint must point at M6-T02: %s", res.FixHint.Text)
	}
}

// TestNFDProbe_Missing — no NFD anywhere. Result StatusMissing +
// Warn + a hint pointing at M6-T01.
func TestNFDProbe_Missing(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{
		"srv1": {"kubernetes.io/hostname": "srv1"},
	}}
	res := NewNFDProbe(src).Run(context.Background())
	if res.Status != ports.StatusMissing {
		t.Errorf("Status = %v, want StatusMissing", res.Status)
	}
	if res.Severity != ports.SeverityWarn {
		t.Errorf("Severity = %v, want SeverityWarn (M6 is optional, not a Blocker)", res.Severity)
	}
	if !strings.Contains(res.FixHint.Text, "M6-T01") {
		t.Errorf("FixHint must point at M6-T01: %s", res.FixHint.Text)
	}
}

// TestNFDProbe_NilK8sClient — defensive guard for wiring bugs.
// Doctor's assembleProbes wires the probe only when the session
// resolved; a nil-k8s call is a programmer error we surface loudly.
func TestNFDProbe_NilK8sClient(t *testing.T) {
	res := NewNFDProbe(nil).Run(context.Background())
	if res.Status != ports.StatusMissing || res.Severity != ports.SeverityWarn {
		t.Errorf("expected StatusMissing/SeverityWarn for nil k8s, got %+v", res)
	}
	if !strings.Contains(res.Detail, "internal wiring bug") {
		t.Errorf("Detail must call out the internal wiring bug: %s", res.Detail)
	}
}

// TestNFDProbe_ContextCanceled — probes must respect ctx.Err() so
// the doctor's per-probe timeout budget works. Cancelled context
// short-circuits before touching the fake.
func TestNFDProbe_ContextCanceled(t *testing.T) {
	src := &fakeNodeLabels{labels: map[string]map[string]string{"srv1": {}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := NewNFDProbe(src).Run(ctx)
	if src.calls != 0 {
		t.Errorf("cancelled ctx should short-circuit before NodeLabels: %d calls", src.calls)
	}
	if res.Status != ports.StatusMissing {
		t.Errorf("Status = %v, want StatusMissing on cancel", res.Status)
	}
}

// equalStringsNFD compares slices without pulling in reflect.DeepEqual.
// Suffixed to avoid a naming collision with tools_test.go's own
// equalStrings helper — same shape, but Go rejects two package-level
// funcs with the same name in the same _test package.
func equalStringsNFD(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
