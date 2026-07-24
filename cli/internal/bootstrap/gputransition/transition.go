package gputransition

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type Mode string

const (
	ModePodHAMi       Mode = "pod-hami"
	ModePodHAMiDRA    Mode = "pod-hami-dra"
	ModeVMPassthrough Mode = "vm-passthrough"

	activeModeLabel   = "kube-dc.com/gpu.workload-mode"
	expectedModeLabel = "kube-dc.com/gpu.expected-workload-mode"
)

type Fleet interface {
	Prepare(ctx context.Context, node string) (Mode, error)
	Apply(ctx context.Context, node string, from, to Mode) (string, error)
}

type Options struct {
	Node           string
	From           Mode
	To             Mode
	DryRun         bool
	ResumeCordoned bool
	Timeout        time.Duration
	PollInterval   time.Duration
	Out            io.Writer
	Cluster        ports.GPUTransitionClient
	Fleet          Fleet
	Wait           func(context.Context, time.Duration) error
}

type Result struct {
	Revision string
	Resumed  bool
}

func Run(ctx context.Context, o Options) (Result, error) {
	if err := validateOptions(o); err != nil {
		return Result{}, err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Wait == nil {
		o.Wait = waitContext
	}

	state, err := o.Cluster.GPUNodeTransitionState(ctx, o.Node)
	if err != nil {
		return Result{}, fmt.Errorf("gpu transition: observe node: %w", err)
	}
	fleetMode, err := o.Fleet.Prepare(ctx, o.Node)
	if err != nil {
		return Result{}, fmt.Errorf("gpu transition: prepare fleet: %w", err)
	}
	active, expected := Mode(state.Runtime.Labels[activeModeLabel]), Mode(state.Runtime.Labels[expectedModeLabel])
	resumed := fleetMode == o.To && ((active == o.To && expected == o.To) ||
		(state.Unschedulable && o.ResumeCordoned && transitionalMode(active, o.From, o.To) && transitionalMode(expected, o.From, o.To)))
	if !resumed && (active != o.From || expected != o.From || fleetMode != o.From) {
		return Result{}, fmt.Errorf("gpu transition blocked: mode drift active=%q expected=%q fleet=%q, want %q", active, expected, fleetMode, o.From)
	}
	if len(state.Holders) > 0 {
		return Result{}, fmt.Errorf("gpu transition blocked: node %s has GPU holders: %s", o.Node, formatHolders(state.Holders))
	}
	if state.Unschedulable && !o.ResumeCordoned {
		return Result{}, fmt.Errorf("gpu transition blocked: node %s is already cordoned; inspect the prior failure and pass --resume-cordoned only when this workflow owns the cordon", o.Node)
	}
	readyWithoutCordon := state
	readyWithoutCordon.Unschedulable = true
	if active == o.To && expected == o.To && fleetMode == o.To && !state.Unschedulable && targetReady(readyWithoutCordon, o.To) {
		fmt.Fprintf(o.Out, "GPU node transition: %s already at %s; no cordon, GitOps write, or runtime mutation was made.\n", o.Node, o.To)
		return Result{Resumed: true}, nil
	}

	fmt.Fprintf(o.Out, "GPU node transition: %s %s -> %s\n", o.Node, o.From, o.To)
	fmt.Fprintln(o.Out, "  preflight: zero native GPU Pod/VMI holders")
	fmt.Fprintln(o.Out, "  order: cordon -> recheck holders -> GitOps mode commit -> wait for exact labels/plugin -> uncordon")
	if o.DryRun {
		fmt.Fprintln(o.Out, "[gpu transition] preview only; no cordon, file write, commit, push, or runtime change was made.")
		return Result{Resumed: resumed}, nil
	}

	if !state.Unschedulable {
		if err := o.Cluster.SetGPUNodeSchedulable(ctx, o.Node, false); err != nil {
			return Result{}, fmt.Errorf("gpu transition: cordon node: %w", err)
		}
		fmt.Fprintln(o.Out, "[gpu transition] node cordoned")
	}
	failCordoned := func(stage string, cause error) (Result, error) {
		return Result{}, fmt.Errorf("gpu transition: %s: %w; node %s remains cordoned", stage, cause, o.Node)
	}

	state, err = o.Cluster.GPUNodeTransitionState(ctx, o.Node)
	if err != nil {
		return failCordoned("post-cordon observation", err)
	}
	if !state.Unschedulable {
		return failCordoned("post-cordon verification", fmt.Errorf("node is still schedulable"))
	}
	if len(state.Holders) > 0 {
		return failCordoned("post-cordon zero-holder gate", fmt.Errorf("new GPU holders appeared: %s", formatHolders(state.Holders)))
	}

	revision := ""
	if !resumed {
		revision, err = o.Fleet.Apply(ctx, o.Node, o.From, o.To)
		if err != nil {
			return failCordoned("GitOps mode change", err)
		}
		fmt.Fprintf(o.Out, "[gpu transition] GitOps revision %s pushed\n", revision)
	} else {
		fmt.Fprintln(o.Out, "[gpu transition] resuming an already-applied target mode")
	}

	deadline := time.Now().Add(o.Timeout)
	for {
		state, err = o.Cluster.GPUNodeTransitionState(ctx, o.Node)
		if err == nil && targetReady(state, o.To) {
			break
		}
		if time.Now().After(deadline) {
			if err != nil {
				return failCordoned("wait for target runtime", err)
			}
			return failCordoned("wait for target runtime", fmt.Errorf("timeout after %s (active=%q expected=%q owners=%s ready=%t)", o.Timeout, state.Runtime.Labels[activeModeLabel], state.Runtime.Labels[expectedModeLabel], formatOwners(state.Runtime.PluginOwners), state.Ready))
		}
		if err := o.Wait(ctx, o.PollInterval); err != nil {
			return failCordoned("wait for target runtime", err)
		}
	}
	if len(state.Holders) > 0 {
		return failCordoned("final zero-holder gate", fmt.Errorf("GPU holders appeared: %s", formatHolders(state.Holders)))
	}
	if err := o.Cluster.SetGPUNodeSchedulable(ctx, o.Node, true); err != nil {
		return failCordoned("uncordon node", err)
	}
	fmt.Fprintln(o.Out, "[gpu transition] target runtime Ready, zero holders, node uncordoned")
	return Result{Revision: revision, Resumed: resumed}, nil
}

func transitionalMode(value, from, to Mode) bool {
	return value == from || value == to
}

func validateOptions(o Options) error {
	if strings.TrimSpace(o.Node) == "" || o.Cluster == nil || o.Fleet == nil {
		return fmt.Errorf("gpu transition: node, cluster client, and fleet transaction are required")
	}
	allowed := map[Mode]bool{ModePodHAMi: true, ModePodHAMiDRA: true, ModeVMPassthrough: true}
	if !allowed[o.From] || !allowed[o.To] || o.From == o.To {
		return fmt.Errorf("gpu transition: only pod-hami, pod-hami-dra, and vm-passthrough ownership changes are supported (got %q -> %q)", o.From, o.To)
	}
	return nil
}

func targetReady(state ports.GPUNodeTransitionState, target Mode) bool {
	if !state.Ready || !state.Unschedulable || len(state.Holders) > 0 ||
		Mode(state.Runtime.Labels[activeModeLabel]) != target || Mode(state.Runtime.Labels[expectedModeLabel]) != target {
		return false
	}
	owners := recognizedOwners(state.Runtime.PluginOwners)
	if owners["nvidia-device-plugin-daemonset"] {
		return false
	}
	switch target {
	case ModePodHAMi:
		return owners["hami-device-plugin"] && !owners["hami-dra-driver-kubelet-plugin"] && !owners["nvidia-sandbox-device-plugin-daemonset"]
	case ModePodHAMiDRA:
		return owners["hami-dra-driver-kubelet-plugin"] && !owners["hami-device-plugin"] && !owners["nvidia-sandbox-device-plugin-daemonset"]
	case ModeVMPassthrough:
		return owners["nvidia-sandbox-device-plugin-daemonset"] && !owners["hami-device-plugin"] && !owners["hami-dra-driver-kubelet-plugin"]
	default:
		return false
	}
}

func recognizedOwners(owners []ports.GPUPluginOwner) map[string]bool {
	out := map[string]bool{}
	for _, owner := range owners {
		if owner.Kind == "DaemonSet" && (owner.Namespace == "gpu-operator" || owner.Namespace == "hami-system") {
			out[owner.Name] = true
		}
	}
	return out
}

func formatHolders(holders []ports.GPUHolder) string {
	items := make([]string, 0, len(holders))
	for _, holder := range holders {
		items = append(items, holder.Namespace+"/"+holder.Kind+"/"+holder.Name+"["+strings.Join(holder.Resources, ",")+"]")
	}
	sort.Strings(items)
	return strings.Join(items, "; ")
}

func formatOwners(owners []ports.GPUPluginOwner) string {
	items := make([]string, 0, len(owners))
	for _, owner := range owners {
		items = append(items, owner.Namespace+"/"+owner.Kind+"/"+owner.Name)
	}
	sort.Strings(items)
	return strings.Join(items, ",")
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
