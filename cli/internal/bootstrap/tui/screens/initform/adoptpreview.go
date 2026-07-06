package initform

import (
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
)

// adoptpreview is the pure view over an adopt.PinResult that the init
// wizard renders in --mode=adopt BEFORE Apply, so the operator sees
// exactly what the CheckAdoptPinned gate will decide — and, in the
// interactive flow, can choose to write the pins first.
//
// Pure by design: BuildAdoptPreview takes an already-computed PinResult
// (+ whether a fleet overlay exists) — the live cluster read stays in
// the cobra layer. Classification mirrors CheckAdoptPinned EXACTLY
// (overlay-missing first, then pinned-vs-drift), so the preview never
// disagrees with the gate that follows.

// AdoptStatus is the gate decision the preview predicts.
type AdoptStatus string

const (
	// AdoptNoOverlay: no clusters/<name>/cluster-config.env yet — adopt
	// pins INTO an existing overlay, so this is the scaffold-first
	// boundary (foreign-cluster import isn't automated).
	AdoptNoOverlay AdoptStatus = "no-overlay"
	// AdoptDrift: overlay exists but ≥1 component would drift (pending
	// pin) or is undetected — the gate fails closed unless pinned first.
	AdoptDrift AdoptStatus = "drift"
	// AdoptSafe: overlay exists, every detected component already pinned
	// to its live version — the gate passes.
	AdoptSafe AdoptStatus = "safe"
)

// AdoptDriftRow is one component whose fleet pin doesn't match live.
type AdoptDriftRow struct {
	Component string
	FleetPin  string // current cluster-config.env value, "(unset)" if empty
	Live      string // live version to pin to
	Manual    bool   // Live came from --manual-pin
}

// AdoptPreview is the classified, displayable form of a PinResult.
type AdoptPreview struct {
	Cluster       string
	Status        AdoptStatus
	Drift         []AdoptDriftRow // components that would be pinned
	Undetected    []string        // detected but live version unreadable
	AlreadyPinned []string        // component names already at live (safe)
}

// BuildAdoptPreview classifies res the same way CheckAdoptPinned does.
// overlayExists=false forces AdoptNoOverlay regardless of the rows (the
// boundary is checked first).
func BuildAdoptPreview(cluster string, res *adopt.PinResult, overlayExists bool) AdoptPreview {
	p := AdoptPreview{Cluster: cluster}
	for _, pin := range res.Pins {
		cur := pin.Current
		if cur == "" {
			cur = "(unset)"
		}
		p.Drift = append(p.Drift, AdoptDriftRow{
			Component: pin.Component,
			FleetPin:  cur,
			Live:      pin.Live,
			Manual:    pin.Manual,
		})
	}
	p.Undetected = append(p.Undetected, res.Undetected...)
	for _, a := range res.AlreadyPinned {
		p.AlreadyPinned = append(p.AlreadyPinned, componentOf(a))
	}

	switch {
	case !overlayExists:
		p.Status = AdoptNoOverlay
	case len(res.Pins) == 0 && !res.HasUnresolved():
		p.Status = AdoptSafe
	default:
		p.Status = AdoptDrift
	}
	return p
}

// NeedsPinning reports whether the operator has actionable, writable
// drift — the signal the wizard uses to offer "write pins now". True
// only when an overlay EXISTS (AdoptDrift) and there are pending pins:
//   - AdoptNoOverlay → false (nowhere to write; scaffold first).
//   - Undetected-only → false (needs --manual-pin/--skip-component, not a
//     value the wizard can write).
func (p AdoptPreview) NeedsPinning() bool {
	return p.Status == AdoptDrift && len(p.Drift) > 0
}

// Render produces the human-facing preview block.
func (p AdoptPreview) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "== adopt preview — %s ==\n", p.Cluster)

	switch p.Status {
	case AdoptNoOverlay:
		fmt.Fprintf(&b, "  ✗ cluster %q has no fleet overlay (clusters/%s/cluster-config.env) yet.\n", p.Cluster, p.Cluster)
		fmt.Fprintln(&b, "    Adopt-in-place pins INTO an existing overlay — scaffold the cluster into the")
		fmt.Fprintln(&b, "    fleet first (importing a foreign cluster with no overlay isn't automated yet).")
		if n := len(p.Drift) + len(p.Undetected); n > 0 {
			fmt.Fprintf(&b, "    (%d component(s) detected but unpinnable without an overlay)\n", n)
		}
		return b.String()

	case AdoptSafe:
		fmt.Fprintf(&b, "  ✓ %d detected component(s) already pinned to their live versions — safe to adopt in place.\n", len(p.AlreadyPinned))
		return b.String()
	}

	// AdoptDrift.
	if len(p.Drift) > 0 {
		fmt.Fprintln(&b, "  Version drift — these will be pinned to the LIVE versions:")
		w := colWidth(p.Drift)
		for _, r := range p.Drift {
			src := ""
			if r.Manual {
				src = "  (--manual-pin)"
			}
			fmt.Fprintf(&b, "    %-*s  %-12s → %s%s\n", w, r.Component, r.FleetPin, r.Live, src)
		}
	}
	if len(p.Undetected) > 0 {
		fmt.Fprintf(&b, "  Unreadable live version (resolve with --manual-pin KEY=VER or --skip-component NAME):\n")
		fmt.Fprintf(&b, "    %s\n", strings.Join(p.Undetected, ", "))
	}
	if len(p.AlreadyPinned) > 0 {
		fmt.Fprintf(&b, "  Already pinned (safe): %s\n", strings.Join(p.AlreadyPinned, ", "))
	}
	fmt.Fprintf(&b, "  %d to pin · %d unreadable · %d already pinned\n",
		len(p.Drift), len(p.Undetected), len(p.AlreadyPinned))
	fmt.Fprintf(&b, "  Pin them:  kube-dc bootstrap adopt %s --pin-versions --yes\n", p.Cluster)
	return b.String()
}

// colWidth sizes the component column so the arrows line up.
func colWidth(rows []AdoptDriftRow) int {
	w := 0
	for _, r := range rows {
		if len(r.Component) > w {
			w = len(r.Component)
		}
	}
	return w
}

// componentOf extracts the component name from an AlreadyPinned entry
// formatted as "component (KEY=version)". The version-key parens are
// always the LAST " (" — cut there so component names that themselves
// contain parens (e.g. "kube-ovn (CNI)") survive intact. Falls back to
// the whole string if the shape is unexpected.
func componentOf(alreadyPinned string) string {
	if i := strings.LastIndex(alreadyPinned, " ("); i > 0 {
		return alreadyPinned[:i]
	}
	return alreadyPinned
}
