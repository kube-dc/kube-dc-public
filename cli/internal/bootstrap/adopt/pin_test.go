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

	res, err := PinVersions(context.Background(), insp, env)
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

func TestPinVersions_ChartErrorPropagates(t *testing.T) {
	insp := fakeInspector{
		crds:     []string{"certificates.cert-manager.io"},
		chartErr: context.DeadlineExceeded,
	}
	if _, err := PinVersions(context.Background(), insp, fakeEnv{}); err == nil {
		t.Error("HelmReleaseChartVersions error must propagate")
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
