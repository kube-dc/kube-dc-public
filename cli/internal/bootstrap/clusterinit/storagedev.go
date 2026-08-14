package clusterinit

// storagedev.go — B3: raw OSD block-device presence/emptiness probe.
//
// The #1 silent rook-ceph install failure is pointing an OSD at a device that
// does not exist on the node, or at one that already carries a filesystem /
// partitions / a mount (rook refuses to consume a non-empty device, so the OSD
// never comes up and object storage — Mimir, Loki, CNPG WAL backups — is dead
// while every other install step reports green).
//
// This is a pre-CNI, best-effort SSH probe that fails OPEN: it only WARNS, so a
// false negative (an unreachable node, no lsblk) never blocks a valid install.
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
	StorageDevUnknown StorageDeviceState = "unknown" // could not determine (no lsblk, node unreachable) → fail-open
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
// fails OPEN — an SSH/tooling failure yields StorageDevUnknown, never a false
// "missing"/"in-use".
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
	// lsblk is the classifier. A device is IN-USE if it carries a filesystem, a
	// partition-TABLE signature (PTTYPE — ceph-volume rejects a bare-GPT disk
	// even with no partitions), a mount, or any child partition; EMPTY only when
	// none of those hold. Correctness details, all forced by codex review:
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
			"command -v lsblk >/dev/null 2>&1 || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"[ -b \"$D\" ] || { echo KDCPROBE:MISSING; exit 0; }; "+
			"SIG=$(sudo -n lsblk -dnro FSTYPE,PTTYPE,MOUNTPOINT \"$D\" 2>/dev/null) || { echo KDCPROBE:UNKNOWN; exit 0; }; "+
			"CH=$(sudo -n lsblk -nro NAME \"$D\" 2>/dev/null | tail -n +2); "+
			"if [ -n \"$(printf '%%s' \"$SIG$CH\" | tr -d '[:space:]')\" ]; then echo KDCPROBE:INUSE; else echo KDCPROBE:EMPTY; fi",
		dev)
	out, err := opts.SSH.Run(ctx, opts.Host, probe)
	if err != nil {
		res.State = StorageDevUnknown
		res.Detail = fmt.Sprintf("node unreachable or probe failed (%v)", err)
		return res, nil
	}
	res.State = parseStorageSentinel(string(out))
	if res.State == StorageDevUnknown {
		res.Detail = "lsblk unavailable/unprivileged or ambiguous output"
	}
	return res, nil
}

// parseStorageSentinel extracts the classifier from the probe output by
// EXACT-matching one KDCPROBE: line (trimmed), so surrounding shell-rc noise is
// ignored. No sentinel → UNKNOWN (fail-open).
func parseStorageSentinel(out string) StorageDeviceState {
	for _, ln := range strings.Split(out, "\n") {
		switch strings.TrimSpace(ln) {
		case "KDCPROBE:MISSING":
			return StorageDevMissing
		case "KDCPROBE:INUSE":
			return StorageDevInUse
		case "KDCPROBE:EMPTY":
			return StorageDevEmpty
		case "KDCPROBE:UNKNOWN":
			return StorageDevUnknown
		}
	}
	return StorageDevUnknown
}
