package anchors

import (
	"context"
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// NodeStatus is the per-node read-only doctor view.
//
// Each field maps to one SSH probe:
//   - UnitState: `systemctl is-active kube-dc-anchor.service`
//     → "active" | "inactive" | "failed" | "not-installed"
//   - IPBound: `ip -4 addr show <iface>` contains the expected CIDR
//
// These are independent — a node can have the unit "not-installed"
// but the CIDR bound (operator hand-bound it pre-Apply); doctor
// surfaces both so the operator sees the drift.
type NodeStatus struct {
	Host         string
	ExpectedCIDR string
	Iface        string
	UnitState    string
	IPBound      bool
	Err          error // any error during the probe itself (auth, network, …)
}

// IsHealthy returns true when the unit is active AND the CIDR is
// bound — the normal-operation target state.
func (s NodeStatus) IsHealthy() bool {
	return s.Err == nil && s.UnitState == "active" && s.IPBound
}

// Probe runs the read-only checks against every entry in anchors.
// Per-node Err is captured into NodeStatus rather than aborting —
// doctor wants the full row set, not "first failure wins".
//
// The "not-installed" → "active" continuum mirrors systemctl's own
// vocabulary so the doctor printer can map states to badges without
// re-translation:
//
//   - active: unit is up and the bind script reported success
//   - inactive: unit exists but hasn't run yet (rare — Apply does
//     enable --now)
//   - failed: unit ran but bind script exited non-zero (bridge
//     missing? IP collision?)
//   - not-installed: no unit file on disk yet (pre-Apply)
//
// IPBound is checked separately so a "unit active, IP not bound"
// drift (someone ran `ip addr del` out-of-band) is visible.
//
// `resolver` maps node names to SSHHost{Alias,Hostname}. nil falls
// back to the legacy `SSHHost{Alias: e.Host}` path so callers without
// SSH host-map config aren't regressed.
func Probe(ctx context.Context, ssh ports.SSHClient, iface string, anchors []Entry, resolver HostResolver) []NodeStatus {
	if resolver == nil {
		resolver = NewHostResolver(nil)
	}
	out := make([]NodeStatus, 0, len(anchors))
	for _, e := range anchors {
		s := NodeStatus{Host: e.Host, ExpectedCIDR: e.CIDR, Iface: iface}
		host := resolver(e.Host)

		// Unit state — systemctl returns one of the documented strings
		// or "unknown" on missing unit; we map "unknown" / non-zero-exit
		// to "not-installed" for the doctor table.
		unitOut, unitErr := ssh.Run(ctx, host,
			fmt.Sprintf("systemctl is-active %s 2>/dev/null || true", UnitName))
		if unitErr != nil {
			s.Err = fmt.Errorf("unit state: %w", unitErr)
			out = append(out, s)
			continue
		}
		state := strings.TrimSpace(string(unitOut))
		switch state {
		case "active", "inactive", "failed", "activating", "deactivating":
			s.UnitState = state
		case "", "unknown":
			s.UnitState = "not-installed"
		default:
			s.UnitState = state
		}

		// IP binding — `grep -wq` exits 0 if the CIDR appears in the
		// addr listing as a whole-word match. Run.err on non-zero exit
		// is conflated with transport errors; we treat both as "not
		// bound" since the resolution (operator action) is the same.
		_, ipErr := ssh.Run(ctx, host,
			fmt.Sprintf("ip -4 addr show %s | grep -wq %s",
				shellEscape(iface), shellEscape(e.CIDR)))
		s.IPBound = ipErr == nil

		out = append(out, s)
	}
	return out
}

// shellEscape single-quotes user-supplied strings going into a remote
// shell command. Iface + CIDR are validated upstream (preset.go +
// ParseAnchorMap), but a defense-in-depth quote here is cheap.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
