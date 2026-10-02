package setup

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func storageHostFixture(node string, sizeGiB uint64, now time.Time) HostResources {
	return HostResources{FileBackingAvailable: true, Host: HostObservation{ID: node, ObservedAt: now, Facts: HostFacts{
		MachineID: node, FreeBytes: 100 << 30,
		HostKey: &ports.SSHHostKeyEvidence{FingerprintSHA256: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))},
	}, Checks: []HostCheck{{ID: "host-key", Status: "pass"}}}, Disks: []DiskCandidate{
		{Path: "/dev/sda", SizeBytes: 2000 << 30, InUse: true},
		{Path: "/dev/sdb", SizeBytes: sizeGiB << 30, Checked: true, StableID: "wwn:" + node},
	}}
}

func layoutByID(layouts []StorageLayout, id string) *StorageLayout {
	for i := range layouts {
		if layouts[i].ID == id {
			return &layouts[i]
		}
	}
	return nil
}

func TestStorageAdviceCapacityAndHostMapping(t *testing.T) {
	now := time.Now()
	hosts := []HostResources{storageHostFixture("c", 100, now), storageHostFixture("a", 300, now), storageHostFixture("b", 200, now)}
	layouts := SuggestStorageLayouts(hosts, "b", now)
	three := layoutByID(layouts, "raw")
	if three == nil || len(three.Assignments) != 3 || three.Replicas != 2 || three.CapacityBytes != (100<<30)/5*4/2*3 {
		t.Fatalf("unexpected three-host layout: %+v", three)
	}
	for i, node := range []string{"a", "b", "c"} {
		if three.Assignments[i].Node != node || three.Assignments[i].Device != "/dev/sdb" {
			t.Fatalf("wrong assignment %+v", three.Assignments)
		}
	}
	if file := layoutByID(layouts, "file-one"); file == nil || file.BackingSizeGiB != 50 || file.Assignments[0].Node != "b" {
		t.Fatalf("unexpected file proposal %+v", file)
	}
}

func TestStorageAdviceRejectsUntrustedAndOccupiedDisks(t *testing.T) {
	now := time.Now()
	for _, variant := range []string{"stale", "future", "no-pin", "no-machine", "blocked-key", "unchecked", "occupied", "no-disk-id", "alias", "shared-disk"} {
		t.Run(variant, func(t *testing.T) {
			hosts := []HostResources{storageHostFixture("a", 100, now), storageHostFixture("b", 100, now), storageHostFixture("c", 100, now)}
			h := &hosts[2]
			switch variant {
			case "stale":
				h.Host.ObservedAt = now.Add(-5 * time.Minute)
			case "future":
				h.Host.ObservedAt = now.Add(time.Second)
			case "no-pin":
				h.Host.Facts.HostKey = nil
			case "no-machine":
				h.Host.Facts.MachineID = ""
			case "blocked-key":
				h.Host.Checks[0].Status = "blocked"
			case "unchecked":
				h.Disks[1].Checked = false
			case "occupied":
				h.Disks[1].InUse = true
			case "no-disk-id":
				h.Disks[1].StableID = ""
			case "shared-disk":
				h.Disks[1].StableID = hosts[0].Disks[1].StableID
			case "alias":
				h.Host.Facts.MachineID = hosts[0].Host.Facts.MachineID
			}
			if layout := layoutByID(SuggestStorageLayouts(hosts, "a", now), "raw"); layout != nil && len(layout.Assignments) >= 3 {
				t.Fatal("unsafe three-host proposal")
			}
		})
	}
}

func TestBackingFileProposalReservesSharedSpace(t *testing.T) {
	for _, tc := range []struct {
		free uint64
		want int
	}{{0, 0}, {69, 0}, {70, 20}, {100, 50}, {200, 100}, {4000, 500}} {
		t.Run(fmt.Sprint(tc.free), func(t *testing.T) {
			if got := suggestedBackingGiB(tc.free << 30); got != tc.want {
				t.Fatalf("%d GiB free: got %d, want %d", tc.free, got, tc.want)
			}
		})
	}
	if layouts := SuggestStorageLayouts(nil, "", time.Now()); len(layouts) != 1 || layouts[0].ID != "pvc" {
		t.Fatal("unobserved host received a disk proposal")
	}
}
