package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

const validGPUUpgradeQualification = `apiVersion: kube-dc.io/v1alpha1
kind: GPUUpgradeQualification
id: v100-kernel-canary-1
state: qualified
approvedBy: platform-review
sourceRevision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
hardware: [10de:1db6/10de:124a]
target:
  kernel: 6.8.0-135-generic
  rke2: v1.36.1+rke2r1
  driver: 580.130.00
  gpuOperator: v26.3.4
  dcgmExporter: 4.4.1-4.6.0-ubuntu22.04
canary:
  completedAt: 2026-07-14T12:00:00Z
  allocationPassed: true
  monitoringPassed: true
  rollbackPassed: true
  evidence: change-record-42
`

func gpuUpgradeArgs(path string) []string {
	return []string{
		"--qualification", path, "--pci-id", "10de:1db6/10de:124a",
		"--current-kernel", "6.8.0-134-generic", "--target-kernel", "6.8.0-135-generic",
		"--current-rke2", "v1.35.3+rke2r3", "--target-rke2", "v1.36.1+rke2r1",
		"--current-driver", "580.126.20", "--target-driver", "580.130.00",
		"--current-gpu-operator", "v26.3.3", "--target-gpu-operator", "v26.3.4",
		"--current-dcgm-exporter", "4.4.1-4.6.0-ubuntu22.04", "--target-dcgm-exporter", "4.4.1-4.6.0-ubuntu22.04",
	}
}

func TestRequireGPUCreationClosedFailsClosedOnMissingOrOpenGate(t *testing.T) {
	env := config.NewEnv()
	env.Set("GPU_SHARED_CREATION_ENABLED", "false")
	if err := requireGPUCreationClosed(env); err == nil || !strings.Contains(err.Error(), "GPU_VM_CREATION_ENABLED") {
		t.Fatalf("missing gate must block, got %v", err)
	}
	env.Set("GPU_VM_CREATION_ENABLED", "true")
	if err := requireGPUCreationClosed(env); err == nil || !strings.Contains(err.Error(), "GPU_VM_CREATION_ENABLED") {
		t.Fatalf("open gate must block, got %v", err)
	}
	env.Set("GPU_VM_CREATION_ENABLED", "false")
	if err := requireGPUCreationClosed(env); err != nil {
		t.Fatalf("closed gates rejected: %v", err)
	}
}

func TestGPUDRARegistersReadOnlyPostflightAndRecoveryCommands(t *testing.T) {
	cmd := bootstrapGPUDRACmd(time.Now)
	for _, name := range []string{"doctor", "postflight", "recovery-plan"} {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found == nil || found.Name() != name {
			t.Fatalf("DRA subcommand %q not registered: found=%v err=%v", name, found, err)
		}
	}
}

func TestGPUUpgradeCheckPassesExactReviewedTuple(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qualification.yaml")
	if err := os.WriteFile(path, []byte(validGPUUpgradeQualification), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newGPUUpgradeCheckCmd(func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(gpuUpgradeArgs(path))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GPU upgrade gate: PASS", "v100-kernel-canary-1", "serialized canary plan"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestGPUUpgradeCheckBlocksUnknownTargetWithoutMutationAdvice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qualification.yaml")
	if err := os.WriteFile(path, []byte(validGPUUpgradeQualification), 0o600); err != nil {
		t.Fatal(err)
	}
	args := gpuUpgradeArgs(path)
	for i := range args {
		if args[i] == "580.130.00" {
			args[i] = "999.0"
		}
	}
	cmd := newGPUUpgradeCheckCmd(func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "GPU upgrade blocked") || !strings.Contains(err.Error(), "not the qualified value") {
		t.Fatalf("expected exact-tuple blocker, got %v", err)
	}
	if strings.Contains(out.String(), "Next:") {
		t.Fatalf("blocked command emitted mutation advice:\n%s", out.String())
	}
}
