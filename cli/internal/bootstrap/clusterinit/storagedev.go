package clusterinit

// storagedev.go — B3: raw OSD block-device presence/emptiness probe.
//
// The #1 silent rook-ceph install failure is pointing an OSD at a device that
// does not exist on the node, or at one that already carries a filesystem /
// partitions / a mount (rook refuses to consume a non-empty device, so the OSD
// never comes up and object storage — Mimir, Loki, CNPG WAL backups — is dead
// while every other install step reports green).
//
// This is a read-only pre-CNI SSH probe. The apply path requires a confirmed
// empty raw device. An unknown result must stop the installation.
// It runs only for modes that consume a raw device (rook-ceph-local /
// rook-ceph-multi-node) and only for EXPLICITLY configured devices — an empty
// or loopN device means a loop-file backing that the fleet creates at install
// time, which has nothing to pre-check.

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// loopDeviceRegex matches the loop-file backing names (loop0, loop12) the fleet
// materializes at install time — there is no raw device to pre-check for those.
var loopDeviceRegex = regexp.MustCompile(`^loop[0-9]+$`)

// StorageDeviceState is the classification of one OSD device on its node.
type StorageDeviceState string

const (
	StorageDevEmpty   StorageDeviceState = "empty"   // exists, block device, no fs/partitions/mounts → good
	StorageDevMissing StorageDeviceState = "missing" // no such block device on the node
	StorageDevInUse   StorageDeviceState = "in-use"  // has a filesystem, partitions, or a mount → rook refuses it
	StorageDevUnknown StorageDeviceState = "unknown" // could not determine (no lsblk, node unreachable)
)

// StorageDeviceResult reports one (node, device) probe.
type StorageDeviceResult struct {
	Node   string
	Device string // bare name as configured (no /dev/)
	State  StorageDeviceState
	Detail string
}

// StorageDeviceProbeOptions parameterizes ProbeStorageDevice.
type StorageDeviceProbeOptions struct {
	SSH    ports.SSHClient
	Host   ports.SSHHost
	Node   string // k8s node name (for reporting)
	Device string // bare device name (no /dev/), already deviceNameRegex-validated
	Out    io.Writer
}

// RawOSDDevices returns the (node, bareDevice) pairs a raw-device rook mode will
// consume, in deterministic order, skipping loop-file backings (empty / loopN).
// PVC and external modes have no raw device and return nothing.
func (s ObjectStorageSpec) RawOSDDevices() [][2]string {
	var out [][2]string
	add := func(node, dev string) {
		dev = strings.TrimPrefix(dev, "/dev/")
		if node == "" || dev == "" || loopDeviceRegex.MatchString(dev) {
			return
		}
		out = append(out, [2]string{node, dev})
	}
	switch s.Mode {
	case RookCephLocal:
		add(s.OSDNode, s.OSDDevice)
	case RookCephMultiNode:
		nodes := make([]string, 0, len(s.CephNodes))
		for n := range s.CephNodes {
			nodes = append(nodes, n)
		}
		sort.Strings(nodes)
		for _, n := range nodes {
			add(n, s.CephNodes[n])
		}
	}
	return out
}

// ProbeStorageDevice checks one raw OSD device on its node: exists, is a block
// device, and is empty (no filesystem, no child partitions, not mounted). It
// returns Unknown on SSH or tooling failure, never a false "empty" verdict.
func ProbeStorageDevice(ctx context.Context, opts StorageDeviceProbeOptions) (StorageDeviceResult, error) {
	res := StorageDeviceResult{Node: opts.Node, Device: opts.Device}
	if opts.SSH == nil {
		return res, fmt.Errorf("storage-device probe: nil SSH client")
	}
	dev := strings.TrimPrefix(opts.Device, "/dev/")
	// Defense-in-depth: the device name is interpolated into a root SSH command.
	// It is deviceNameRegex-validated at parse (no shell metacharacters), but
	// re-check here so this never trusts an unvalidated caller.
	if !deviceNameRegex.MatchString(dev) {
		res.State = StorageDevUnknown
		res.Detail = "device name failed validation"
		return res, nil
	}
	// Probe every known occupancy source. A holder or wipefs signature may
	// remain even when lsblk reports no filesystem or partition. Any command
	// failure stays UNKNOWN so the apply path cannot call the disk empty.
	//   - `sudo -n lsblk` (not plain): reading FSTYPE/PTTYPE needs privilege on
	//     hosts without a populated udev DB, else a formatted disk reads blank
	//     and looks EMPTY. `sudo -n` is already an installer prerequisite (RKE2
	//     install runs `sudo -n env … bash`); if it can't run we get UNKNOWN.
	//   - the authoritative lsblk exit status is captured DIRECTLY (`|| exit`),
	//     not swallowed by a `| tr` pipeline — an lsblk failure must be UNKNOWN,
	//     never a false EMPTY.
	//   - a unique KDCPROBE: sentinel on its own line, exact-matched in Go, so
	//     shell-rc noise (`EMPTY environment variable …`) can't be misread.
	// Single-quoted, deviceNameRegex-validated input (no shell metacharacters).
	probe := fmt.Sprintf(
		"D='/dev/%[1]s'; "+
			"command -v lsblk >/dev/null 2>&1 && command -v wipefs >/dev/null 2>&1 || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"[ -b \"$D\" ] || { echo KDCPROBE:MISSING; exit 0; }; "+
			"INFO=$(sudo -n lsblk -dnro FSTYPE,PTTYPE,MOUNTPOINT \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"RO=$(sudo -n lsblk -dnro RO \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"MM=$(sudo -n lsblk -dnro MAJ:MIN \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"case \"$MM\" in *[!0-9:]*|'') echo KDCPROBE:UNKNOWN; exit 0;; esac; "+
			"ALL=$(sudo -n lsblk -nro NAME \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"CH=$(printf '%%s\\n' \"$ALL\" | tail -n +2); "+
			"HOLD=$(sudo -n find \"/sys/dev/block/$MM/holders\" -mindepth 1 -maxdepth 1 -print 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"WIPE=$(sudo -n wipefs -n --noheadings -O TYPE -- \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"if [ \"$RO\" != 0 ] || [ -n \"$(printf '%%s' \"$INFO$CH$HOLD$WIPE\" | tr -d '[:space:]')\" ]; then echo KDCPROBE:INUSE; else echo KDCPROBE:EMPTY; fi",
		dev)
	out, err := opts.SSH.Run(ctx, opts.Host, probe)
	if err != nil {
		res.State = StorageDevUnknown
		res.Detail = fmt.Sprintf("node unreachable or probe failed (%v)", err)
		return res, nil
	}
	res.State = parseStorageSentinel(string(out))
	if res.State == StorageDevUnknown {
		res.Detail = "lsblk or wipefs unavailable, permission denied, or ambiguous output"
	}
	return res, nil
}

// RequireEmptyStorageDevice turns a read-only observation into the apply gate.
// The caller probes again after review and before any Fleet or host mutation.
func RequireEmptyStorageDevice(res StorageDeviceResult) error {
	if res.State == StorageDevEmpty {
		return nil
	}
	return fmt.Errorf("selected raw OSD device /dev/%s on %s is %s; inspect the device and run readiness again", res.Device, res.Node, res.State)
}

// parseStorageSentinel requires one exact classifier line. Multiple lines are
// ambiguous and cannot become an "empty" disk verdict.
func parseStorageSentinel(out string) StorageDeviceState {
	state := StorageDevUnknown
	seen := false
	for _, ln := range strings.Split(out, "\n") {
		var found StorageDeviceState
		switch strings.TrimSpace(ln) {
		case "KDCPROBE:MISSING":
			found = StorageDevMissing
		case "KDCPROBE:INUSE":
			found = StorageDevInUse
		case "KDCPROBE:EMPTY":
			found = StorageDevEmpty
		case "KDCPROBE:UNKNOWN":
			found = StorageDevUnknown
		default:
			continue
		}
		if seen {
			return StorageDevUnknown
		}
		seen, state = true, found
	}
	return state
}
