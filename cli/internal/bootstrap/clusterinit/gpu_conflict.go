package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

var ErrGPUClusterConflict = errors.New("gpu: target cluster identity or device-plugin ownership conflicts with the requested node mode")

const (
	gpuActiveModeLabel   = "kube-dc.com/gpu.workload-mode"
	gpuExpectedModeLabel = "kube-dc.com/gpu.expected-workload-mode"

	hamiPluginOwner    = "hami-device-plugin"
	hamiDRAPluginOwner = "hami-dra-driver-kubelet-plugin"
	regularPluginOwner = "nvidia-device-plugin-daemonset"
	sandboxPluginOwner = "nvidia-sandbox-device-plugin-daemonset"
)

// ValidateGPUClusterConflicts proves that every target-explicit Kubernetes
// Node is the same machine discovered over SSH, then rejects mutually
// exclusive device-plugin owners and mode-label drift. It is read-only and
// uses only exact Node GETs plus node-scoped Pod LISTs through reader.
func ValidateGPUClusterConflicts(
	ctx context.Context,
	g GPUConfig,
	inventories map[string]discover.GPUHostInventory,
	reader ports.GPUClusterReader,
) error {
	if g.Platform == "" || g.Platform == GPUPlatformDisabled || g.Platform == GPUPlatformDetectOnly {
		return nil
	}
	if reader == nil {
		return fmt.Errorf("%w: target kubeconfig reader is unavailable", ErrGPUClusterConflict)
	}
	nodes := activeGPUNodeNames(g.NodeModes)
	runtimes, err := reader.GPUNodeRuntimes(ctx, nodes)
	if err != nil {
		return fmt.Errorf("%w: observe target cluster: %v", ErrGPUClusterConflict, err)
	}
	var conflicts []string
	for _, node := range nodes {
		inventory, inventoryOK := inventories[node]
		runtime, runtimeOK := runtimes[node]
		switch {
		case !inventoryOK:
			conflicts = append(conflicts, fmt.Sprintf("node %s has no SSH inventory", node))
			continue
		case !runtimeOK:
			conflicts = append(conflicts, fmt.Sprintf("node %s is absent from the target kubeconfig", node))
			continue
		}
		sshUUID := canonicalSystemUUID(inventory.SystemUUID)
		k8sUUID := canonicalSystemUUID(runtime.SystemUUID)
		if sshUUID == "" || k8sUUID == "" {
			conflicts = append(conflicts, fmt.Sprintf("node %s cannot be identity-bound because its system UUID is missing", node))
			continue
		}
		if sshUUID != k8sUUID {
			conflicts = append(conflicts, fmt.Sprintf("node %s SSH/Kubernetes system UUID mismatch", node))
			continue
		}

		requested := string(g.NodeModes[node])
		active := strings.TrimSpace(runtime.Labels[gpuActiveModeLabel])
		expected := strings.TrimSpace(runtime.Labels[gpuExpectedModeLabel])
		if active != "" || expected != "" {
			if active != requested || expected != requested {
				conflicts = append(conflicts, fmt.Sprintf("node %s mode labels active=%q expected=%q, requested=%q", node, active, expected, requested))
				continue
			}
		}

		owners := recognizedGPUPluginOwners(runtime.PluginOwners)
		if owners[regularPluginOwner] {
			conflicts = append(conflicts, fmt.Sprintf("node %s runs forbidden regular NVIDIA device plugin %s", node, regularPluginOwner))
			continue
		}
		switch g.NodeModes[node] {
		case GPUNodePodHAMi:
			if owners[hamiDRAPluginOwner] {
				conflicts = append(conflicts, fmt.Sprintf("node %s requests pod-hami but DRA plugin %s is active", node, hamiDRAPluginOwner))
			} else if owners[sandboxPluginOwner] {
				conflicts = append(conflicts, fmt.Sprintf("node %s requests pod-hami but VM sandbox plugin %s is active", node, sandboxPluginOwner))
			}
		case GPUNodePodHAMiDRA:
			if owners[hamiPluginOwner] {
				conflicts = append(conflicts, fmt.Sprintf("node %s requests pod-hami-dra but legacy HAMi plugin %s is active", node, hamiPluginOwner))
			} else if owners[sandboxPluginOwner] {
				conflicts = append(conflicts, fmt.Sprintf("node %s requests pod-hami-dra but VM sandbox plugin %s is active", node, sandboxPluginOwner))
			}
		case GPUNodeVMPassthrough:
			if owners[hamiPluginOwner] || owners[hamiDRAPluginOwner] {
				conflicts = append(conflicts, fmt.Sprintf("node %s requests vm-passthrough but a HAMi allocator is active", node))
			}
		case GPUNodeVMVGPU:
			conflicts = append(conflicts, fmt.Sprintf("node %s requests deferred vm-vgpu conflict validation", node))
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("%w: %s", ErrGPUClusterConflict, strings.Join(conflicts, "; "))
	}
	return nil
}

func activeGPUNodeNames(modes map[string]GPUNodeMode) []string {
	nodes := make([]string, 0, len(modes))
	for node, mode := range modes {
		if mode != GPUNodeDisabled {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)
	return nodes
}

func canonicalSystemUUID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "{")
	value = strings.TrimSuffix(value, "}")
	return value
}

func recognizedGPUPluginOwners(in []ports.GPUPluginOwner) map[string]bool {
	out := map[string]bool{}
	for _, owner := range in {
		if owner.Kind != "DaemonSet" {
			continue
		}
		switch owner.Name {
		case hamiPluginOwner, regularPluginOwner, sandboxPluginOwner:
			if owner.Namespace != "gpu-operator" {
				continue
			}
			out[owner.Name] = true
		case hamiDRAPluginOwner:
			if owner.Namespace != "hami-system" {
				continue
			}
			out[owner.Name] = true
		}
	}
	return out
}
