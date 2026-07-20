package gpudra

import (
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func qualifiedStatus(now time.Time) ports.GPUDRAStatus {
	last := now.Add(-5 * time.Minute)
	return ports.GPUDRAStatus{
		ServerVersion: "v1.36.2+rke2r1", StableAPI: true,
		DeviceClass: "kube-dc-v100-8g", DeviceClassPresent: true,
		ResourceSlices: 1, Devices: 2, ShareableDevices: 2, ConsumableCapacity: true,
		DriverReady: 2, DriverDesired: 2,
		DriverImages:    []string{"docker.io/projecthami/k8s-dra-driver@" + DefaultDriverDigest},
		AllocatorOwners: []string{"hami-system/hami-dra-driver-kubelet-plugin"},
		DRANodes:        []string{"gpu-a", "gpu-b"},
		CanaryPresent:   true, CanaryLastSuccessfulTime: &last,
	}
}

func TestEvaluateQualifiedMigration(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	report := Evaluate(qualifiedStatus(now), Options{Now: now, RequiredDigest: DefaultDriverDigest, MigrationPlan: true})
	if !report.Ready {
		t.Fatalf("qualified report=%+v", report)
	}
}

func TestEvaluateFailsClosedAcrossSupportAndMigrationGates(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	status := qualifiedStatus(now)
	status.ServerVersion = "v1.35.9"
	status.ShareableDevices = 1
	status.DriverImages = []string{"docker.io/projecthami/k8s-dra-driver:v0.1.0"}
	status.AllocatorOwners = append(status.AllocatorOwners, "gpu-operator/hami-device-plugin")
	status.WrongModeNodes = []string{"gpu-b"}
	status.SharedCreationEnabled = true
	status.LegacyHolders = []ports.GPUHolder{{Namespace: "org-project", Kind: "Pod", Name: "trainer"}}
	report := Evaluate(status, Options{Now: now, RequiredDigest: DefaultDriverDigest, MigrationPlan: true})
	if report.Ready {
		t.Fatal("unsafe migration unexpectedly passed")
	}
	failed := 0
	for _, item := range report.Checks {
		if !item.Pass {
			failed++
		}
	}
	if failed < 6 {
		t.Fatalf("expected independent failures, got %+v", report.Checks)
	}
}

func TestRollbackRequiresZeroDRAClaims(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	status := qualifiedStatus(now)
	status.Claims = 1
	status.GPUClaims = 1
	status.DRAHolders = []ports.GPUHolder{{Namespace: "org-project", Kind: "Deployment", Name: "trainer"}}
	if Evaluate(status, Options{Now: now, RequiredDigest: DefaultDriverDigest, RollbackPlan: true}).Ready {
		t.Fatal("rollback with a DRA holder unexpectedly passed")
	}
}

func TestRollbackBlocksOnSiblingGPUResourceClaim(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	status := qualifiedStatus(now)
	status.GPUClaims = 1
	if Evaluate(status, Options{Now: now, RequiredDigest: DefaultDriverDigest, RollbackPlan: true}).Ready {
		t.Fatal("rollback with a sibling GPU ResourceClaim unexpectedly passed")
	}
}
