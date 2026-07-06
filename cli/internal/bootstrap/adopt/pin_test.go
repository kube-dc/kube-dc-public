package adopt

import (
	"context"
	"strings"
	"testing"
)

type fakeEnv map[string]string

func (e fakeEnv) GetOr(k, fallback string) string {
	if v, ok := e[k]; ok {
		return v
	}
	return fallback
}

func pinFor(res *PinResult, key string) *PinChange {
	for i := range res.Pins {
		if res.Pins[i].VersionKey == key {
			return &res.Pins[i]
		}
	}
	return nil
}

func TestPinVersions(t *testing.T) {
	insp := fakeInspector{
		crds: []string{
			"certificates.cert-manager.io", // cert-manager
			"subnets.kubeovn.io",           // kube-ovn
			"cephclusters.ceph.rook.io",    // rook-ceph (no live chart → Undetected)
		},
		nss: []string{"ingress-nginx"}, // no VersionKey → skipped entirely
		charts: map[string]string{
			"cert-manager/cert-manager": "v1.14.4",
			"kube-system/kube-ovn":      "v1.15.0",
			// rook-ceph release intentionally absent
		},
	}
	env := fakeEnv{
		"CERT_MANAGER_VERSION": "v1.20.1   # fleet default, drifted from live",
		"KUBE_OVN_VERSION":     "v1.15.0", // already at live → AlreadyPinned
	}

	res, err := PinVersions(context.Background(), insp, env, PinOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// cert-manager: env v1.20.1 (comment stripped) → pin to live v1.14.4.
	cm := pinFor(res, "CERT_MANAGER_VERSION")
	if cm == nil || cm.Current != "v1.20.1" || cm.Live != "v1.14.4" {
		t.Errorf("cert-manager pin wrong: %+v", cm)
	}
	// kube-ovn: env already == live → AlreadyPinned, not a pin.
	if pinFor(res, "KUBE_OVN_VERSION") != nil {
		t.Error("kube-ovn should be AlreadyPinned, not a pending pin")
	}
	if !containsSubstr(res.AlreadyPinned, "kube-ovn") {
		t.Errorf("kube-ovn should be in AlreadyPinned: %v", res.AlreadyPinned)
	}
	// rook-ceph: detected but no live chart version → Undetected.
	if !containsSubstr(res.Undetected, "rook-ceph") {
		t.Errorf("rook-ceph should be Undetected: %v", res.Undetected)
	}
	// ingress-nginx: no VersionKey → not anywhere.
	for _, p := range res.Pins {
		if strings.Contains(p.Component, "ingress") {
			t.Error("ingress-nginx has no VersionKey and must not appear in pins")
		}
	}
}

// KubeVirt/CDI aren't Helm releases; their version comes from the
// operator CR (item 4) — so they should NOT be undetected when the CR
// exposes a version.
func TestPinVersions_CRVersionFallback(t *testing.T) {
	insp := fakeInspector{
		crds: []string{"kubevirts.kubevirt.io", "datavolumes.cdi.kubevirt.io"},
		// No Helm releases for either.
		charts: map[string]string{},
		// Operator CRs expose the running versions.
		crFields: map[string]string{"kubevirt": "v1.8.1", "cdi": "v1.65.0"},
	}
	env := fakeEnv{
		"KUBEVIRT_VERSION":     "v1.7.0",  // drifted → pin to CR live v1.8.1
		"KUBEVIRT_CDI_VERSION": "v1.65.0", // matches CR → already pinned
	}
	res, err := PinVersions(context.Background(), insp, env, PinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUnresolved() {
		t.Errorf("kubevirt+cdi should resolve via CR, got undetected %v", res.Undetected)
	}
	kv := pinFor(res, "KUBEVIRT_VERSION")
	if kv == nil || kv.Live != "v1.8.1" || kv.Manual {
		t.Errorf("kubevirt should pin to CR live v1.8.1 (not manual): %+v", kv)
	}
	if !containsSubstr(res.AlreadyPinned, "cdi") {
		t.Errorf("cdi should be already-pinned (CR v1.65.0 == env): %v", res.AlreadyPinned)
	}
}

func TestPinVersions_CRErrorPropagates(t *testing.T) {
	insp := fakeInspector{
		crds:   []string{"kubevirts.kubevirt.io"},
		charts: map[string]string{},
		crErr:  context.DeadlineExceeded,
	}
	if _, err := PinVersions(context.Background(), insp, fakeEnv{}, PinOptions{}); err == nil {
		t.Error("a CR-read error must propagate (not silently undetected)")
	}
}

func TestPinVersions_ChartErrorPropagates(t *testing.T) {
	insp := fakeInspector{
		crds:     []string{"certificates.cert-manager.io"},
		chartErr: context.DeadlineExceeded,
	}
	if _, err := PinVersions(context.Background(), insp, fakeEnv{}, PinOptions{}); err == nil {
		t.Error("HelmReleaseChartVersions error must propagate")
	}
}

func TestPinVersions_Escapes(t *testing.T) {
	// kubevirt + cdi detected but NOT Helm releases (no chart entry) →
	// undetected unless rescued. ingress-nginx has no VersionKey (skipped
	// silently). cert-manager has a live chart.
	insp := fakeInspector{
		crds: []string{
			"certificates.cert-manager.io", // cert-manager (live chart)
			"kubevirts.kubevirt.io",        // kubevirt (no helm release)
			"datavolumes.cdi.kubevirt.io",  // cdi (no helm release)
		},
		charts: map[string]string{"cert-manager/cert-manager": "v1.14.4"},
	}
	env := fakeEnv{"CERT_MANAGER_VERSION": "v1.20.1"}

	// No escapes → kubevirt + cdi undetected.
	base, _ := PinVersions(context.Background(), insp, env, PinOptions{})
	if !base.HasUnresolved() || len(base.Undetected) != 2 {
		t.Fatalf("expected 2 undetected (kubevirt, cdi), got %v", base.Undetected)
	}

	// --skip-component cdi + --manual-pin KUBEVIRT_VERSION=v1.8.1 → resolved.
	res, err := PinVersions(context.Background(), insp, env, PinOptions{
		Skip:   map[string]bool{"cdi": true},
		Manual: map[string]string{"KUBEVIRT_VERSION": "v1.8.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUnresolved() {
		t.Errorf("cdi skipped + kubevirt manual-pinned → nothing unresolved, got %v", res.Undetected)
	}
	if !containsSubstr(res.Skipped, "cdi") {
		t.Errorf("cdi should be Skipped: %v", res.Skipped)
	}
	kv := pinFor(res, "KUBEVIRT_VERSION")
	if kv == nil || kv.Live != "v1.8.1" || !kv.Manual {
		t.Errorf("kubevirt should be a manual pin to v1.8.1: %+v", kv)
	}

	// A manual pin matching no detected component → UnusedManual.
	res2, _ := PinVersions(context.Background(), insp, env, PinOptions{
		Skip:   map[string]bool{"cdi": true, "kubevirt": true},
		Manual: map[string]string{"NONEXISTENT_VERSION": "v9"},
	})
	if !containsSubstr(res2.UnusedManual, "NONEXISTENT_VERSION") {
		t.Errorf("unmatched manual-pin should be reported as unused: %v", res2.UnusedManual)
	}
}

func TestStripInlineComment_Engine(t *testing.T) {
	cases := map[string]string{
		"v1.2.3   # why": "v1.2.3",
		"v1.2.3":         "v1.2.3",
		"a\t# c":         "a",
	}
	for in, want := range cases {
		if got := stripInlineComment(in); got != want {
			t.Errorf("stripInlineComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func containsSubstr(items []string, sub string) bool {
	for _, it := range items {
		if strings.Contains(it, sub) {
			return true
		}
	}
	return false
}
