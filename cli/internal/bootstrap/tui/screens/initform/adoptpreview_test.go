package initform

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
)

func TestBuildAdoptPreview_NoOverlayWinsOverRows(t *testing.T) {
	// Even with detected components, a missing overlay is the boundary —
	// mirrors CheckAdoptPinned checking OverlayMissing first.
	res := &adopt.PinResult{
		Pins:       []adopt.PinChange{{Component: "cert-manager", VersionKey: "CERT_MANAGER_VERSION", Current: "v1.20.1", Live: "v1.14.4"}},
		Undetected: []string{"rook-ceph"},
	}
	p := BuildAdoptPreview("acme", res, false /* overlayExists */)
	if p.Status != AdoptNoOverlay {
		t.Fatalf("no overlay must win regardless of rows, got %q", p.Status)
	}
	if p.NeedsPinning() {
		t.Error("no-overlay has nowhere to write → NeedsPinning must be false even with pending pins")
	}
	out := p.Render()
	if !strings.Contains(out, "no fleet overlay") || !strings.Contains(out, "scaffold") {
		t.Errorf("no-overlay render should give the boundary guidance:\n%s", out)
	}
	if !strings.Contains(out, "isn't automated yet") {
		t.Errorf("should note foreign import isn't automated:\n%s", out)
	}
}

func TestBuildAdoptPreview_SafeWhenAllPinned(t *testing.T) {
	res := &adopt.PinResult{
		AlreadyPinned: []string{"cert-manager (CERT_MANAGER_VERSION=v1.14.4)", "kube-ovn (CNI) (KUBE_OVN_VERSION=v1.15.0)"},
	}
	p := BuildAdoptPreview("acme", res, true)
	if p.Status != AdoptSafe {
		t.Fatalf("all pinned + overlay → safe, got %q", p.Status)
	}
	if p.NeedsPinning() {
		t.Error("safe preview has no drift → NeedsPinning should be false")
	}
	// Component names extracted from the "comp (KEY=ver)" strings.
	if p.AlreadyPinned[0] != "cert-manager" || p.AlreadyPinned[1] != "kube-ovn (CNI)" {
		t.Errorf("component-name extraction wrong: %v", p.AlreadyPinned)
	}
	if !strings.Contains(p.Render(), "safe to adopt in place") {
		t.Errorf("safe render wrong:\n%s", p.Render())
	}
}

func TestBuildAdoptPreview_DriftTableAndCounts(t *testing.T) {
	res := &adopt.PinResult{
		Pins: []adopt.PinChange{
			{Component: "cert-manager", VersionKey: "CERT_MANAGER_VERSION", Current: "v1.20.1", Live: "v1.14.4"},
			{Component: "kubevirt", VersionKey: "KUBEVIRT_VERSION", Current: "", Live: "v1.8.1", Manual: true},
		},
		Undetected:    []string{"rook-ceph"},
		AlreadyPinned: []string{"keycloak (KEYCLOAK_VERSION=24.3.0)"},
	}
	p := BuildAdoptPreview("acme", res, true)
	if p.Status != AdoptDrift {
		t.Fatalf("pending pins → drift, got %q", p.Status)
	}
	if !p.NeedsPinning() {
		t.Error("drift with pins → NeedsPinning should be true")
	}
	out := p.Render()
	for _, want := range []string{
		"Version drift",
		"cert-manager", "v1.20.1", "v1.14.4",
		"kubevirt", "(unset)", "v1.8.1", "(--manual-pin)",
		"Unreadable", "rook-ceph",
		"Already pinned (safe): keycloak",
		"2 to pin · 1 unreadable · 1 already pinned",
		"adopt acme --pin-versions --yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drift render missing %q:\n%s", want, out)
		}
	}
}

func TestBuildAdoptPreview_UndetectedOnlyIsDriftButNotPinnable(t *testing.T) {
	// A detected component with no readable version + overlay present:
	// gate would fail closed (drift status), but there's nothing to
	// write-pin (needs --manual-pin/--skip-component), so NeedsPinning
	// is false — the wizard must NOT offer "write pins now" here.
	res := &adopt.PinResult{Undetected: []string{"rook-ceph"}}
	p := BuildAdoptPreview("acme", res, true)
	if p.Status != AdoptDrift {
		t.Fatalf("undetected → drift (gate fails closed), got %q", p.Status)
	}
	if p.NeedsPinning() {
		t.Error("undetected-only has no pinnable drift → NeedsPinning must be false")
	}
}
