package screens

import (
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// fleet_clusterview.go renders the OpenBao side panel opened with 'o' on
// a Fleet cluster row — a bordered drill-down that mirrors the
// Kustomization detail panel. Reconcile status is already shown in the
// Fleet details pane's Kustomization list, so this panel adds only the
// OpenBao view the list was missing.

// renderFleetOpenBao renders the coarse, readiness-based OpenBao summary
// the ClusterProbe collects over HTTP (no pod-exec — that's why it works
// where a `bao status` exec would be Unauthorized under a tenant
// kubeconfig). For authoritative per-pod seal/init detail the operator
// runs `kube-dc bootstrap openbao status`.
func renderFleetOpenBao(ob *discover.OpenBaoStatus) string {
	if ob == nil {
		return bttui.Muted.Render("no OpenBao data")
	}
	var b strings.Builder
	for _, p := range ob.Pods {
		glyph, state := "✓", "ready"
		if !p.Ready {
			glyph, state = "✗", "sealed?"
		}
		fmt.Fprintf(&b, "%s %-11s %s\n", glyph, p.Name, state)
	}
	fmt.Fprintf(&b, "\n%d/%d pods ready\n", ob.ReadyPods, ob.TotalPods)
	fmt.Fprintf(&b, "finalized: %s\n", boolYesNo(ob.Finalized))
	fmt.Fprintf(&b, "auth:      %s\n", boolYesNo(ob.AuthSetup))
	if ob.TotalPods == 0 || ob.ReadyPods < ob.TotalPods {
		b.WriteString(bttui.Muted.Render("(see openbao status)"))
		b.WriteString("\n")
	}
	return b.String()
}

func boolYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// reconcilerGlyph maps a Kustomization's state to a glyph: ✓ ready,
// ◑ actively reconciling (progressing — NOT an error), ✗ otherwise
// (failed / genuinely unknown). Keeps a mid-reconcile row from looking
// like a failure.
func reconcilerGlyph(rec discover.ReconcilerStatus) string {
	switch {
	case rec.Ready:
		return "✓"
	case rec.Reconciling:
		return "◑"
	default:
		return "✗"
	}
}
