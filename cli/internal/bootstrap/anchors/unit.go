package anchors

import (
	"fmt"
	"strings"
)

// Remote paths the apply step writes to. Hard-coded — the unit
// invokes the script by absolute path, so changing one without the
// other is a bug. Tests assert both via these constants.
const (
	RemoteScriptPath = "/usr/local/sbin/kube-dc-anchor-bind"
	RemoteUnitPath   = "/etc/systemd/system/kube-dc-anchor.service"

	// ScriptMode is 0755: executable, owner-writable. The unit
	// invokes it via ExecStart so the +x bit is load-bearing.
	ScriptMode uint32 = 0o755
	// UnitMode is 0644: systemd parses .service files as root, the
	// world-readable bit is a Linux convention not a requirement.
	UnitMode uint32 = 0o644

	// UnitName is the systemd unit name — used by Apply / Probe for
	// `systemctl daemon-reload && systemctl enable --now <name>`.
	UnitName = "kube-dc-anchor.service"
)

// BindScript is the static POSIX shell script written to
// /usr/local/sbin/kube-dc-anchor-bind. Same content on every node;
// node-specific values flow in via the systemd unit's ExecStart args.
//
// The script polls for the OVS bridge to appear because kube-ovn-cni
// creates br-ext-cloud asynchronously after the daemon's CRD cache
// syncs (init.go:100-146 in kube-ovn). systemd's unit ordering
// (`After=openvswitch-switch.service`) only guarantees OVS is up, not
// that the CNI has created the bridge yet. 120s is generous; in
// practice the bridge appears within a few seconds.
//
// `ip addr replace` is idempotent — re-running over an already-bound
// CIDR is a no-op. This makes the unit safe to restart at any time
// (e.g. after a `systemctl reload` or a fleet config change).
const BindScript = `#!/bin/sh
set -e
IFACE="${1:-br-ext-cloud}"
CIDR="${2:?missing CIDR}"
TIMEOUT=120
i=0
until ip link show "$IFACE" >/dev/null 2>&1; do
  i=$((i+1)); [ $i -ge $TIMEOUT ] && { echo "timeout waiting for $IFACE" >&2; exit 1; }
  sleep 1
done
ip addr replace "$CIDR" dev "$IFACE"
ip link set "$IFACE" up
`

// UnitTemplate is the systemd unit template. Two placeholders:
// {{IFACE}} = the OVS bridge name (EXT_NET_ANCHOR_INTERFACE),
// {{CIDR}} = this node's anchor IP. RenderUnit fills both in.
//
// Ordering rationale:
//   - After=systemd-networkd.service — base network must be up;
//     `ip` commands depend on the rtnetlink stack
//   - After=rke2-server.service — wait for kubelet so OVS daemons
//     run; not strict on agent-only nodes but harmless there
//   - After=openvswitch-switch.service — bridge can't exist without
//     ovsdb-server + ovs-vswitchd running
//   - Wants=systemd-networkd.service — soft dep on Ubuntu netplan
//     graph; absent on distros where networkd isn't active
//
// Type=oneshot + RemainAfterExit=yes — bind once, stay "active" so
// `systemctl is-active` reports green. The bind script's polling
// guards against the unit firing before the bridge appears.
//
// StartLimit*Sec / StartLimitBurst live in [Unit] per systemd.unit(5)
// (systemd >=229; Ubuntu 24.04 ships 255). They previously lived in
// [Service] and were silently dropped — `systemd-analyze verify`
// flagged "Unknown key name 'StartLimitIntervalSec' in section
// 'Service', ignoring." The restart limiter was dead config until
// this fix; Restart=on-failure now actually rate-limits at 3/60s.
const UnitTemplate = `[Unit]
Description=Kube-DC per-node anchor IP on {{IFACE}}
Documentation=https://github.com/shalb/kube-dc/blob/main/docs/internal/internal-platform-endpoints-runbook.md
After=systemd-networkd.service rke2-server.service openvswitch-switch.service
Wants=systemd-networkd.service
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/kube-dc-anchor-bind {{IFACE}} {{CIDR}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// RenderBindScript returns the static binding script. Returns a copy
// (well, the same string — strings are immutable) so callers can
// pass it straight to ssh.Put.
func RenderBindScript() string { return BindScript }

// RenderUnit fills {{IFACE}} and {{CIDR}} into UnitTemplate. Returns
// an error if either input is empty — callers should have validated
// via ParseAnchorMap first.
func RenderUnit(iface, cidr string) (string, error) {
	iface = strings.TrimSpace(iface)
	cidr = strings.TrimSpace(cidr)
	if iface == "" {
		return "", fmt.Errorf("anchors: RenderUnit: empty interface")
	}
	if cidr == "" {
		return "", fmt.Errorf("anchors: RenderUnit: empty CIDR")
	}
	out := strings.ReplaceAll(UnitTemplate, "{{IFACE}}", iface)
	out = strings.ReplaceAll(out, "{{CIDR}}", cidr)
	return out, nil
}
