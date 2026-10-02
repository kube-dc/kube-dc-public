package setup

import (
	"fmt"
	"sort"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// StorageLayout is an editable proposal, never permission to prepare a disk.
// CapacityBytes is a conservative planning estimate before Ceph metadata.
type StorageLayout struct {
	ID, Title, Detail string
	Mode              clusterinit.RookMode
	Assignments       []StorageAssignment
	Replicas          int
	CapacityBytes     uint64
	BackingSizeGiB    int
}

type StorageAssignment struct {
	Node, Device, StableID string
	SizeBytes              uint64
}

// SuggestStorageLayouts uses fresh, pinned, read-only host observations. It
// suggests only layouts the generated Fleet overlay can express. It leaves
// mounted/partitioned disks and unknown observations out of raw-disk plans.
func SuggestStorageLayouts(hosts []HostResources, preferredHost string, now time.Time) []StorageLayout {
	var available []StorageAssignment
	var backing []HostResources
	seen := map[string]bool{}
	seenMachines := map[string]bool{}
	for _, host := range hosts {
		h := host.Host
		age := now.Sub(h.ObservedAt)
		if h.ID == "" || seen[h.ID] || h.Facts.MachineID == "" || seenMachines[h.Facts.MachineID] || h.Facts.HostKey == nil || !ValidHostKeySHA256(h.Facts.HostKey.FingerprintSHA256) || age < 0 || age >= 5*time.Minute {
			continue
		}
		trusted := false
		for _, check := range h.Checks {
			if check.ID == "host-key" && check.Status == "pass" {
				trusted = true
			}
		}
		if !trusted {
			continue
		}
		seen[h.ID], seenMachines[h.Facts.MachineID] = true, true
		var best *DiskCandidate
		for i := range host.Disks {
			d := &host.Disks[i]
			if !d.Checked || d.InUse || d.StableID == "" || d.SizeBytes == 0 || !validSelectedDevicePath(d.Path) {
				continue
			}
			if best == nil || d.SizeBytes > best.SizeBytes || (d.SizeBytes == best.SizeBytes && d.Path < best.Path) {
				best = d
			}
		}
		if best != nil {
			available = append(available, StorageAssignment{Node: h.ID, Device: best.Path, StableID: best.StableID, SizeBytes: best.SizeBytes})
		}
		if host.FileBackingAvailable && suggestedBackingGiB(host.Host.Facts.FreeBytes) > 0 {
			backing = append(backing, host)
		}
	}
	// The same LUN visible from two hosts is not two independent disks.
	identities := map[string]int{}
	for _, disk := range available {
		identities[disk.StableID]++
	}
	unique := available[:0]
	for _, disk := range available {
		if identities[disk.StableID] == 1 {
			unique = append(unique, disk)
		}
	}
	available = unique
	sort.Slice(available, func(i, j int) bool {
		if available[i].SizeBytes != available[j].SizeBytes {
			return available[i].SizeBytes > available[j].SizeBytes
		}
		return available[i].Node < available[j].Node
	})
	var layouts []StorageLayout
	if len(available) > 0 {
		selected := append([]StorageAssignment(nil), available[:min(3, len(available))]...)
		replicas := 2
		if len(selected) == 1 {
			replicas = 1
		}
		// Use the smallest disk on every host for a conservative estimate.
		capacity := selected[len(selected)-1].SizeBytes / 5 * 4 / uint64(replicas) * uint64(len(selected))
		sort.Slice(selected, func(i, j int) bool { return selected[i].Node < selected[j].Node })
		layouts = append(layouts, StorageLayout{ID: "raw", Title: "Server disks", Mode: clusterinit.RookCephMultiNode, Assignments: selected, Replicas: replicas, CapacityBytes: capacity,
			Detail: "One checked disk per server. One server uses one copy; two or more default to two copies."})
	}
	sort.Slice(backing, func(i, j int) bool {
		if backing[i].Host.ID == preferredHost {
			return true
		}
		if backing[j].Host.ID == preferredHost {
			return false
		}
		if backing[i].Host.Facts.FreeBytes != backing[j].Host.Facts.FreeBytes {
			return backing[i].Host.Facts.FreeBytes > backing[j].Host.Facts.FreeBytes
		}
		return backing[i].Host.ID < backing[j].Host.ID
	})
	if len(backing) > 0 {
		h := backing[0]
		size := suggestedBackingGiB(h.Host.Facts.FreeBytes)
		layouts = append(layouts, StorageLayout{ID: "file-one", Title: "Evaluation file", Mode: clusterinit.RookCephLocal, Assignments: []StorageAssignment{{Node: h.Host.ID, Device: "/dev/loop0"}}, Replicas: 1, CapacityBytes: uint64(size) * (1 << 30) / 5 * 4, BackingSizeGiB: size,
			Detail: fmt.Sprintf("A %d GiB sparse file under /var/lib; no partition changes. Suggestion leaves at least half the observed free space and 50 GiB for other uses. Space is shared, not reserved. One server and one copy; capacity must be monitored.", size)})
	}
	layouts = append(layouts, StorageLayout{ID: "pvc", Title: "Provider volumes", Mode: clusterinit.RookCephPVC, Replicas: 2,
		Detail: "No raw host disk selection. Choose a verified StorageClass that supplies raw block volumes. Confirm capacity and placement across failure domains; a replica count alone does not prove availability."})
	return layouts
}

func suggestedBackingGiB(free uint64) int {
	freeGiB := free >> 30
	reserve := freeGiB / 2
	if reserve < 50 {
		reserve = 50
	}
	if freeGiB <= reserve || freeGiB-reserve < 20 {
		return 0
	}
	size := freeGiB - reserve
	if size > 500 {
		size = 500
	}
	return int(size)
}
