package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func TestInitApplyRejectsRawCephDiskWithoutSSHBeforeEffects(t *testing.T) {
	o := &clusterinit.InitOptions{
		Mode:          clusterinit.ModeInstall,
		RookMode:      clusterinit.RookCephLocal,
		RookOSDNode:   "node-1",
		RookOSDDevice: "sdb",
		NoSSH:         true,
	}
	err := runApplyEngine(context.Background(), io.Discard, o, &clusterinit.Plan{}, clusterinit.NopReporter{}, false, clusterIdentity{})
	if err == nil || !strings.Contains(err.Error(), "verified SSH access") {
		t.Fatalf("raw device bypassed safety gate: %v", err)
	}
}

func TestExistingRawOSDApplyInputRequiresUnchangedOverlay(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "clusters", "example", "cluster-config.env")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("OBJECT_STORAGE_MODE=rook-ceph-local\nCEPH_LOCAL_OSD_NODE=node-1\nCEPH_LOCAL_OSD_DEVICE=sdb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(filepath.Dir(path), "object-storage")
	if err := os.MkdirAll(overlay, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "kustomization.yaml"), []byte("resources:\n  - ../../../infrastructure/object-storage/modes/rook-ceph-local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o := &clusterinit.InitOptions{Repo: repo, Name: "example", Mode: clusterinit.ModeResume,
		RookMode: clusterinit.RookCephLocal, RookOSDNode: "node-1", RookOSDDevice: "sdb"}
	if err := validateRawOSDApplyInputs(o, false); err != nil {
		t.Fatalf("unchanged occupied disk should allow resume: %v", err)
	}
	o.Mode = clusterinit.ModeAdopt
	if err := validateRawOSDApplyInputs(o, false); err != nil {
		t.Fatalf("unchanged occupied disk should allow adopt: %v", err)
	}
	o.RookOSDDevice = "sdc"
	if err := validateRawOSDApplyInputs(o, false); err == nil {
		t.Fatal("changed disk selection accepted")
	}
	o.RookMode = clusterinit.RookCephPVC
	if err := validateRawOSDApplyInputs(o, false); err == nil {
		t.Fatal("switch from raw disk to PVC accepted")
	}
}

func TestNewMultiNodeRawOSDRequiresMappedTargetsBeforeEffects(t *testing.T) {
	o := &clusterinit.InitOptions{Mode: clusterinit.ModeInstall, SSHHost: "root@192.0.2.10",
		RookMode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{"node-1": "sdb", "node-2": "sdb"}}
	if err := validateRawOSDApplyInputs(o, true); err == nil || !strings.Contains(err.Error(), "--primary-node") {
		t.Fatalf("missing primary mapping accepted: %v", err)
	}
	o.PrimaryNode = "node-1"
	if err := validateRawOSDApplyInputs(o, true); err == nil || !strings.Contains(err.Error(), "node-2") {
		t.Fatalf("missing second target accepted: %v", err)
	}
	o.NodeSSHHosts = map[string]string{"node-2": "root@192.0.2.11"}
	if err := validateRawOSDApplyInputs(o, true); err != nil {
		t.Fatalf("complete raw OSD target map rejected: %v", err)
	}
}

func TestRawOSDTargetUsesReviewedPrimarySSHAndAdditionalHostMapping(t *testing.T) {
	o := &clusterinit.InitOptions{
		RookMode:         clusterinit.RookCephMultiNode,
		PrimaryNode:      "node-1",
		SSHHost:          "root@192.0.2.10",
		SSHHostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		NodeSSHHosts:     map[string]string{"node-1": "root@192.0.2.10", "node-2": "admin@192.0.2.11"},
		NodeSSHHostKeys:  map[string]string{"node-1": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "node-2": "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	}
	primary, err := rawOSDTarget(o, "node-1")
	if err != nil || primary.Alias != "192.0.2.10" || primary.User != "root" || primary.ExpectedHostKeySHA256 != o.SSHHostKeySHA256 {
		t.Fatalf("primary target/pin lost: %+v %v", primary, err)
	}
	other, err := rawOSDTarget(o, "node-2")
	if err != nil || other.Alias != "192.0.2.11" || other.User != "admin" || other.ExpectedHostKeySHA256 != o.NodeSSHHostKeys["node-2"] {
		t.Fatalf("additional target lost: %+v %v", other, err)
	}
	if _, err := rawOSDTarget(o, "node-3"); err == nil {
		t.Fatal("unmapped raw disk node accepted")
	}
	o.NodeSSHHosts["node-1"] = "root@192.0.2.99"
	if _, err := rawOSDTarget(o, "node-1"); err == nil {
		t.Fatal("conflicting primary target accepted")
	}
}
