package gputransition

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeCluster struct {
	states      []ports.GPUNodeTransitionState
	index       int
	schedulable []bool
}

func (f *fakeCluster) GPUNodeTransitionState(context.Context, string) (ports.GPUNodeTransitionState, error) {
	if len(f.states) == 0 {
		return ports.GPUNodeTransitionState{}, errors.New("no state")
	}
	i := f.index
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	f.index++
	return f.states[i], nil
}

func (f *fakeCluster) SetGPUNodeSchedulable(_ context.Context, _ string, value bool) error {
	f.schedulable = append(f.schedulable, value)
	return nil
}

type fakeFleet struct {
	mode       Mode
	applyCalls int
}

func (f *fakeFleet) Prepare(context.Context, string) (Mode, error) { return f.mode, nil }
func (f *fakeFleet) Apply(_ context.Context, _ string, from, to Mode) (string, error) {
	if f.mode != from {
		return "", errors.New("mode drift")
	}
	f.applyCalls++
	f.mode = to
	return "abcdef12", nil
}

func nodeState(mode Mode, cordoned bool, owner string, holders ...ports.GPUHolder) ports.GPUNodeTransitionState {
	namespace := "gpu-operator"
	if owner == "hami-dra-driver-kubelet-plugin" {
		namespace = "hami-system"
	}
	return ports.GPUNodeTransitionState{
		Runtime: ports.GPUNodeRuntime{Labels: map[string]string{activeModeLabel: string(mode), expectedModeLabel: string(mode)}, PluginOwners: []ports.GPUPluginOwner{{Namespace: namespace, Kind: "DaemonSet", Name: owner}}},
		Ready:   true, Unschedulable: cordoned, Holders: holders,
	}
}

func runOptions(cluster *fakeCluster, fleet *fakeFleet) Options {
	return Options{
		Node: "gpu-a", From: ModePodHAMi, To: ModeVMPassthrough,
		Cluster: cluster, Fleet: fleet, Timeout: time.Second,
		Wait: func(context.Context, time.Duration) error { return nil },
	}
}

func TestRunCordonRecheckApplyVerifyUncordon(t *testing.T) {
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{
		nodeState(ModePodHAMi, false, "hami-device-plugin"),
		nodeState(ModePodHAMi, true, "hami-device-plugin"),
		nodeState(ModeVMPassthrough, true, "nvidia-sandbox-device-plugin-daemonset"),
	}}
	fleet := &fakeFleet{mode: ModePodHAMi}
	var out bytes.Buffer
	o := runOptions(cluster, fleet)
	o.Out = &out
	result, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != "abcdef12" || fleet.applyCalls != 1 {
		t.Fatalf("result=%+v applyCalls=%d", result, fleet.applyCalls)
	}
	if len(cluster.schedulable) != 2 || cluster.schedulable[0] || !cluster.schedulable[1] {
		t.Fatalf("schedulable calls=%v", cluster.schedulable)
	}
	if !strings.Contains(out.String(), "zero holders") || !strings.Contains(out.String(), "node uncordoned") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestRunReverseVMPassthroughToPodHAMi(t *testing.T) {
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{
		nodeState(ModeVMPassthrough, false, "nvidia-sandbox-device-plugin-daemonset"),
		nodeState(ModeVMPassthrough, true, "nvidia-sandbox-device-plugin-daemonset"),
		nodeState(ModePodHAMi, true, "hami-device-plugin"),
	}}
	fleet := &fakeFleet{mode: ModeVMPassthrough}
	o := runOptions(cluster, fleet)
	o.From, o.To = ModeVMPassthrough, ModePodHAMi
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if fleet.mode != ModePodHAMi || len(cluster.schedulable) != 2 || !cluster.schedulable[1] {
		t.Fatalf("reverse transition incomplete: fleet=%s sched=%v", fleet.mode, cluster.schedulable)
	}
}

func TestRunLegacyHAMiToDRA(t *testing.T) {
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{
		nodeState(ModePodHAMi, false, "hami-device-plugin"),
		nodeState(ModePodHAMi, true, "hami-device-plugin"),
		nodeState(ModePodHAMiDRA, true, "hami-dra-driver-kubelet-plugin"),
	}}
	fleet := &fakeFleet{mode: ModePodHAMi}
	o := runOptions(cluster, fleet)
	o.To = ModePodHAMiDRA
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if fleet.mode != ModePodHAMiDRA || len(cluster.schedulable) != 2 || !cluster.schedulable[1] {
		t.Fatalf("DRA transition incomplete: fleet=%s sched=%v", fleet.mode, cluster.schedulable)
	}
}

func TestTargetReadyRejectsConflictingRegularAndDualOwners(t *testing.T) {
	base := nodeState(ModePodHAMi, true, "hami-device-plugin")
	if !targetReady(base, ModePodHAMi) {
		t.Fatal("valid HAMi target rejected")
	}
	for _, conflicting := range []string{"nvidia-device-plugin-daemonset", "nvidia-sandbox-device-plugin-daemonset", "hami-dra-driver-kubelet-plugin"} {
		state := base
		namespace := "gpu-operator"
		if conflicting == "hami-dra-driver-kubelet-plugin" {
			namespace = "hami-system"
		}
		state.Runtime.PluginOwners = append(append([]ports.GPUPluginOwner{}, base.Runtime.PluginOwners...), ports.GPUPluginOwner{Namespace: namespace, Kind: "DaemonSet", Name: conflicting})
		if targetReady(state, ModePodHAMi) {
			t.Fatalf("conflicting owner %s accepted", conflicting)
		}
	}
}

func TestRunHolderBlocksBeforeCordon(t *testing.T) {
	holder := ports.GPUHolder{Namespace: "tenant-a", Kind: "VirtualMachineInstance", Name: "gpu-vm", Resources: []string{"nvidia.com/GV100"}}
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{nodeState(ModePodHAMi, false, "hami-device-plugin", holder)}}
	fleet := &fakeFleet{mode: ModePodHAMi}
	_, err := Run(context.Background(), runOptions(cluster, fleet))
	if err == nil || !strings.Contains(err.Error(), "GPU holders") || !strings.Contains(err.Error(), "VirtualMachineInstance") {
		t.Fatalf("expected holder blocker, got %v", err)
	}
	if len(cluster.schedulable) != 0 || fleet.applyCalls != 0 {
		t.Fatalf("blocked transition mutated state: sched=%v apply=%d", cluster.schedulable, fleet.applyCalls)
	}
}

func TestRunPostCordonRaceFailsAndLeavesNodeCordoned(t *testing.T) {
	holder := ports.GPUHolder{Namespace: "tenant-a", Kind: "Job", Name: "late", Resources: []string{"nvidia.com/gpu"}}
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{
		nodeState(ModePodHAMi, false, "hami-device-plugin"),
		nodeState(ModePodHAMi, true, "hami-device-plugin", holder),
	}}
	fleet := &fakeFleet{mode: ModePodHAMi}
	_, err := Run(context.Background(), runOptions(cluster, fleet))
	if err == nil || !strings.Contains(err.Error(), "remains cordoned") || !strings.Contains(err.Error(), "new GPU holders") {
		t.Fatalf("expected post-cordon blocker, got %v", err)
	}
	if len(cluster.schedulable) != 1 || cluster.schedulable[0] || fleet.applyCalls != 0 {
		t.Fatalf("unsafe recovery: sched=%v apply=%d", cluster.schedulable, fleet.applyCalls)
	}
}

func TestRunResumeRequiresExplicitCordonedConsent(t *testing.T) {
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{nodeState(ModeVMPassthrough, true, "nvidia-sandbox-device-plugin-daemonset")}}
	fleet := &fakeFleet{mode: ModeVMPassthrough}
	o := runOptions(cluster, fleet)
	_, err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "--resume-cordoned") {
		t.Fatalf("expected resume consent blocker, got %v", err)
	}

	cluster.index = 0
	o.ResumeCordoned = true
	result, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || fleet.applyCalls != 0 || len(cluster.schedulable) != 1 || !cluster.schedulable[0] {
		t.Fatalf("resume result=%+v sched=%v apply=%d", result, cluster.schedulable, fleet.applyCalls)
	}
}

func TestRunResumesFleetTargetWhileLiveLabelsAreTransitional(t *testing.T) {
	transitional := nodeState(ModePodHAMi, true, "hami-device-plugin")
	transitional.Runtime.Labels[expectedModeLabel] = string(ModeVMPassthrough)
	cluster := &fakeCluster{states: []ports.GPUNodeTransitionState{
		transitional,
		transitional,
		nodeState(ModeVMPassthrough, true, "nvidia-sandbox-device-plugin-daemonset"),
	}}
	fleet := &fakeFleet{mode: ModeVMPassthrough}
	o := runOptions(cluster, fleet)
	o.ResumeCordoned = true
	result, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || fleet.applyCalls != 0 || len(cluster.schedulable) != 1 || !cluster.schedulable[0] {
		t.Fatalf("transitional resume result=%+v sched=%v apply=%d", result, cluster.schedulable, fleet.applyCalls)
	}
}
