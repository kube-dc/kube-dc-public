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

func TestPlanDriverRecoverySelectsOldUnreadyPodOnReadyNode(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	status := qualifiedStatus(now)
	status.DriverReady = 0
	status.ResourceSlices, status.Devices, status.ShareableDevices = 0, 0, 0
	status.DriverPods = []ports.GPUDRADriverPod{{
		Namespace: "hami-system", Name: "hami-dra-old", Node: "gpu-a", Phase: "Pending",
		NodeReady: true, CreationTimestamp: now.Add(-time.Hour), ReadyLastTransitionTime: now.Add(-30 * time.Minute),
	}}
	plan := PlanDriverRecovery(status, now, 10*time.Minute)
	if !plan.Eligible || plan.Candidate == nil || plan.Candidate.Name != "hami-dra-old" {
		t.Fatalf("expected exact stale recovery candidate, got %+v", plan)
	}
}

func TestPlanDriverRecoveryFailsClosedDuringNodeRebootOrInventoryPresence(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	base := qualifiedStatus(now)
	base.DriverReady = 0
	base.ResourceSlices, base.Devices, base.ShareableDevices = 0, 0, 0
	base.DriverPods = []ports.GPUDRADriverPod{{
		Namespace: "hami-system", Name: "hami-dra-old", Node: "gpu-a", Phase: "Unknown",
		NodeReady: false, CreationTimestamp: now.Add(-time.Hour), ReadyLastTransitionTime: now.Add(-30 * time.Minute),
	}}
	if plan := PlanDriverRecovery(base, now, 10*time.Minute); plan.Eligible || plan.Candidate != nil {
		t.Fatalf("node-rebooting Pod must not produce a recovery command: %+v", plan)
	}
	base.DriverPods[0].NodeReady = true
	base.ResourceSlices, base.Devices = 1, 2
	if plan := PlanDriverRecovery(base, now, 10*time.Minute); plan.Eligible || plan.Candidate != nil {
		t.Fatalf("nonempty inventory must not produce total-outage recovery: %+v", plan)
	}
}

func TestPlanDriverRecoveryRejectsFreshOrDeletingPod(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	status := qualifiedStatus(now)
	status.DriverReady = 0
	status.ResourceSlices, status.Devices, status.ShareableDevices = 0, 0, 0
	status.DriverPods = []ports.GPUDRADriverPod{{
		Namespace: "hami-system", Name: "fresh", Node: "gpu-a", NodeReady: true,
		CreationTimestamp: now.Add(-time.Hour), ReadyLastTransitionTime: now.Add(-time.Minute),
	}, {
		Namespace: "hami-system", Name: "deleting", Node: "gpu-b", NodeReady: true, Deleting: true,
		CreationTimestamp: now.Add(-2 * time.Hour), ReadyLastTransitionTime: now.Add(-time.Hour),
	}, {
		Namespace: "hami-system", Name: "no-ready-transition", Node: "gpu-c", NodeReady: true,
		CreationTimestamp: now.Add(-2 * time.Hour),
	}}
	if plan := PlanDriverRecovery(status, now, 10*time.Minute); plan.Eligible || plan.Candidate != nil {
		t.Fatalf("fresh/deleting/no-transition Pods must not produce a recovery command: %+v", plan)
	}
}
