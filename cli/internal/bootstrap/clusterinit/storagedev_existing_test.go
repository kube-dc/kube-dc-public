package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scaffoldRawOSDOverlay(t *testing.T, spec ObjectStorageSpec) string {
	t.Helper()
	repo := seedScaffold(t, "example")
	if err := WriteObjectStorage(repo, "example", "example.test", spec, nil); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repo, "clusters", "example", "cluster-config.env")
}

func TestExistingRawOSDSelectionAcceptsCurrentAndLegacyScaffold(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "node-1", OSDDevice: "sdb", OSDSizeGB: 100}
	path := scaffoldRawOSDOverlay(t, spec)
	if err := RequireUnchangedRawOSDSelection(path, spec); err != nil {
		t.Fatalf("current scaffold: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(data), "OBJECT_STORAGE_MODE=rook-ceph-local\n", "", 1)
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnchangedRawOSDSelection(path, spec); err != nil {
		t.Fatalf("legacy scaffold mode from kustomization: %v", err)
	}
	spec.OSDDevice = "sdc"
	if err := RequireUnchangedRawOSDSelection(path, spec); err == nil || !strings.Contains(err.Error(), "CEPH_LOCAL_OSD_DEVICE") {
		t.Fatalf("changed raw disk accepted: %v", err)
	}
	if err := RequireUnchangedRawOSDSelection(filepath.Join(t.TempDir(), "absent.env"), spec); err == nil {
		t.Fatal("missing overlay accepted as existing raw disk")
	}
}

func TestExistingRawOSDSelectionChecksOverlayMode(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "node-1", OSDDevice: "sdb", OSDSizeGB: 100}
	path := scaffoldRawOSDOverlay(t, spec)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "OBJECT_STORAGE_MODE=rook-ceph-local", "OBJECT_STORAGE_MODE=rook-ceph-pvc", 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnchangedRawOSDSelection(path, spec); err == nil || !strings.Contains(err.Error(), "OBJECT_STORAGE_MODE") {
		t.Fatalf("env storage mode conflict accepted: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(filepath.Dir(path), "object-storage", "kustomization.yaml")
	body, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	changed = strings.Replace(string(body), "/modes/rook-ceph-local", "/modes/rook-ceph-pvc", 1)
	if err := os.WriteFile(overlay, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnchangedRawOSDSelection(path, spec); err == nil || !strings.Contains(err.Error(), "object-storage overlay") {
		t.Fatalf("overlay storage mode conflict accepted: %v", err)
	}
}

func TestExistingMultiNodeOSDSelectionChecksEverySlot(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{"n1": "sdb", "n2": "sdc"}}
	path := scaffoldRawOSDOverlay(t, spec)
	if err := RequireUnchangedRawOSDSelection(path, spec); err != nil {
		t.Fatal(err)
	}
	spec.CephNodes["n2"] = "sdd"
	if err := RequireUnchangedRawOSDSelection(path, spec); err == nil {
		t.Fatal("changed second OSD accepted")
	}
	delete(spec.CephNodes, "n2")
	if err := RequireUnchangedRawOSDSelection(path, spec); err == nil || !strings.Contains(err.Error(), "CEPH_NODE_2") {
		t.Fatalf("removed second OSD accepted: %v", err)
	}
}

func TestExistingRawOSDCannotBeDroppedByStorageModeChange(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "node-1", OSDDevice: "sdb", OSDSizeGB: 100}
	path := scaffoldRawOSDOverlay(t, spec)
	for _, proposed := range []ObjectStorageSpec{
		{Mode: RookCephLocal, OSDNode: "node-1", OSDDevice: "loop0", OSDSizeGB: 100},
		{Mode: RookCephPVC, StorageClass: "fast"},
		{Mode: RookDisabled},
	} {
		if err := RequireUnchangedRawOSDSelection(path, proposed); err == nil || !strings.Contains(err.Error(), "storage migration") {
			t.Fatalf("raw OSD abandoned for %s: %v", proposed.Mode, err)
		}
	}
}

func TestOldRawKeyInPVCOverlayDoesNotBlockUnchangedResume(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast"}
	path := scaffoldRawOSDOverlay(t, spec)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("CEPH_LOCAL_OSD_DEVICE=sdb\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnchangedRawOSDSelection(path, spec); err != nil {
		t.Fatalf("inactive stale raw key blocked PVC resume: %v", err)
	}
}
