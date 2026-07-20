package clusterinit

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	ErrGPUPreinstalledDriverScaffoldDeferred = fmt.Errorf("gpu: preinstalled-driver fleet rendering is deferred until the shared GPU Operator package exposes a driver.enabled substitution")
	ErrGPUVGPUScaffoldDeferred               = fmt.Errorf("gpu: vm-vgpu fleet rendering is deferred until the encrypted license and private-registry package is generated")
	ErrGPUProfileScaffoldDeferred            = fmt.Errorf("gpu: selected profile is not installer-renderable yet")
	ErrGPUProfileContractDrift               = fmt.Errorf("gpu: shared fleet profile does not match the installer resource contract")
)

const installerSharedV100Profile = "nvidia-v100-hami"

// WriteGPUInfrastructure renders the GitOps ownership layer for qualified GPU
// nodes. Disabled and detect-only configurations write nothing. The generated
// catalog remains non-billable and both creation paths stay disabled.
func WriteGPUInfrastructure(fleetRepo, clusterName string, g GPUConfig, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if err := ValidateGPUScaffold(fleetRepo, g); err != nil {
		return err
	}
	if g.Platform == "" || g.Platform == GPUPlatformDisabled || g.Platform == GPUPlatformDetectOnly {
		return nil
	}

	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	modeDir := filepath.Join(clusterDir, "gpu-node-modes")
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		return fmt.Errorf("gpu: mkdir %s: %w", modeDir, err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "nodefeaturerule.yaml"), []byte(gpuNodeFeatureRuleYAML(clusterName, g.NodeModes)), 0o644); err != nil {
		return fmt.Errorf("gpu: write node mode rule: %w", err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "kustomization.yaml"), []byte(gpuNodeModesKustomizationYAML()), 0o644); err != nil {
		return fmt.Errorf("gpu: write node mode kustomization: %w", err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "gpu.yaml"), []byte(gpuFluxYAML(clusterName, g.HAMiEnabled, effectiveGPUSharedAllocator(g))), 0o644); err != nil {
		return fmt.Errorf("gpu: write gpu.yaml: %w", err)
	}
	if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"), patchKustomizationGPU); err != nil {
		return fmt.Errorf("gpu: patch kustomization.yaml: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] GPU infrastructure wired (nodes=%s, hami=%t, allocator=%s, billing=false, creation=false)\n", canonicalGPUNodeModes(g.NodeModes), g.HAMiEnabled, effectiveGPUSharedAllocator(g))
	return nil
}

// ValidateGPUScaffold checks whether the current fleet packages can render the
// requested accelerator configuration. Scaffold calls it before running the
// cluster-generation script so deferred variants cannot leave partial output.
func ValidateGPUScaffold(fleetRepo string, g GPUConfig) error {
	if g.Platform == "" || g.Platform == GPUPlatformDisabled || g.Platform == GPUPlatformDetectOnly {
		return nil
	}
	if g.DriverSource == GPUDriverPreinstalled {
		return ErrGPUPreinstalledDriverScaffoldDeferred
	}
	for _, mode := range g.NodeModes {
		if mode == GPUNodeVMVGPU {
			return ErrGPUVGPUScaffoldDeferred
		}
	}
	profiles := canonicalGPUProfiles(g.Profiles)
	for _, profile := range profiles {
		if profile != installerSharedV100Profile {
			return fmt.Errorf("%w: %q (currently supported: %s)", ErrGPUProfileScaffoldDeferred, profile, installerSharedV100Profile)
		}
	}
	sharedRuntimePath := "infrastructure/hami"
	if allocator := effectiveGPUSharedAllocator(g); allocator == GPUSharedAllocatorDRA || allocator == GPUSharedAllocatorAuto {
		sharedRuntimePath = "infrastructure/hami-dra"
	}
	for _, sharedPath := range []string{"infrastructure/gpu-operator", sharedRuntimePath} {
		if sharedPath == sharedRuntimePath && !g.HAMiEnabled {
			continue
		}
		path := filepath.Join(fleetRepo, sharedPath)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("gpu: required fleet package %s is missing", path)
		}
	}
	if len(profiles) > 0 {
		if err := validateInstallerSharedProfile(filepath.Join(fleetRepo, "platform", "kube-dc", "helmrelease.yaml"), effectiveGPUSharedAllocator(g)); err != nil {
			return err
		}
	}
	return nil
}

func validateInstallerSharedProfile(path string, allocator GPUSharedAllocator) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrGPUProfileContractDrift, path, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrGPUProfileContractDrift, path, err)
	}
	profiles, ok := nestedGPUMap(document, "spec", "values", "gpu")["profiles"].([]any)
	if !ok {
		return fmt.Errorf("%w: %s has no spec.values.gpu.profiles list", ErrGPUProfileContractDrift, path)
	}
	for _, raw := range profiles {
		profile, ok := raw.(map[string]any)
		if !ok || profile["id"] != installerSharedV100Profile {
			continue
		}
		request, ok := profile["request"].(map[string]any)
		if !ok {
			break
		}
		expectedProfile := map[string]string{
			"workload":     "pod",
			"allocation":   "hami",
			"resourceName": "nvidia.com/gpu",
		}
		expectedRequest := map[string]string{
			"countResource":  "nvidia.com/gpu",
			"memoryResource": "nvidia.com/gpumem",
			"coreResource":   "nvidia.com/gpucores",
		}
		for key, want := range expectedProfile {
			if profile[key] != want {
				return fmt.Errorf("%w: profile %s %s=%v, want %s", ErrGPUProfileContractDrift, installerSharedV100Profile, key, profile[key], want)
			}
		}
		if allocator == GPUSharedAllocatorDRA || allocator == GPUSharedAllocatorAuto {
			if profile["allocationBackend"] != "dra" {
				return fmt.Errorf("%w: profile %s allocationBackend=%v, want dra", ErrGPUProfileContractDrift, installerSharedV100Profile, profile["allocationBackend"])
			}
			dra, ok := profile["dra"].(map[string]any)
			if !ok || dra["deviceClassName"] == nil || dra["driver"] == nil {
				return fmt.Errorf("%w: profile %s requires a DRA driver and DeviceClass", ErrGPUProfileContractDrift, installerSharedV100Profile)
			}
		}
		for key, want := range expectedRequest {
			if request[key] != want {
				return fmt.Errorf("%w: profile %s request.%s=%v, want %s", ErrGPUProfileContractDrift, installerSharedV100Profile, key, request[key], want)
			}
		}
		return nil
	}
	return fmt.Errorf("%w: profile %s is absent from %s", ErrGPUProfileContractDrift, installerSharedV100Profile, path)
}

func nestedGPUMap(root map[string]any, keys ...string) map[string]any {
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	return current
}

func gpuNodeFeatureRuleYAML(clusterName string, modes map[string]GPUNodeMode) string {
	nodes := make([]string, 0, len(modes))
	for node, mode := range modes {
		if mode != GPUNodeDisabled {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)

	var b strings.Builder
	b.WriteString("# Generated by kube-dc bootstrap init. Each rule requires both an exact\n")
	b.WriteString("# Kubernetes nodename and a class-qualified NVIDIA display device, preventing\n")
	b.WriteString("# one node's ownership mode from leaking onto another GPU worker.\n")
	b.WriteString("apiVersion: nfd.k8s-sigs.io/v1alpha1\nkind: NodeFeatureRule\nmetadata:\n")
	b.WriteString("  name: " + gpuObjectName(clusterName) + "\nspec:\n  rules:\n")
	for _, node := range nodes {
		mode := modes[node]
		workloadConfig := string(mode)
		if mode == GPUNodePodHAMi || mode == GPUNodePodHAMiDRA {
			workloadConfig = "container"
		}
		b.WriteString("    - name: " + node + "-" + string(mode) + "\n")
		b.WriteString("      labels:\n")
		b.WriteString("        kube-dc.com/gpu.workload-mode: " + string(mode) + "\n")
		b.WriteString("        kube-dc.com/gpu.expected-workload-mode: " + string(mode) + "\n")
		b.WriteString("        nvidia.com/gpu.workload.config: " + workloadConfig + "\n")
		b.WriteString("      matchFeatures:\n")
		b.WriteString("        - feature: system.name\n          matchExpressions:\n            nodename:\n              op: In\n              value: [\"")
		b.WriteString(node)
		b.WriteString("\"]\n")
		b.WriteString("        - feature: pci.device\n          matchExpressions:\n            vendor:\n              op: In\n              value: [\"10de\"]\n            class:\n              op: In\n              value: [\"0300\", \"0302\"]\n")
	}
	return b.String()
}

func gpuObjectName(clusterName string) string {
	base := strings.ReplaceAll(clusterName, "/", "-") + "-gpu-node-modes"
	if len(base) <= 63 {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	return strings.TrimRight(base[:54], "-.") + fmt.Sprintf("-%x", sum[:4])
}

func gpuNodeModesKustomizationYAML() string {
	return "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - nodefeaturerule.yaml\n"
}

func gpuFluxYAML(clusterName string, hami bool, allocator GPUSharedAllocator) string {
	var b strings.Builder
	b.WriteString("# Generated GPU ownership stack for " + clusterName + ". Tenant billing and\n")
	b.WriteString("# creation remain independently disabled by cluster-config.env.\n")
	b.WriteString(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: gpu-node-mode
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  retryInterval: 2m
  timeout: 5m
  path: ./clusters/` + clusterName + `/gpu-node-modes
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: gpu-operator
  namespace: flux-system
spec:
  dependsOn:
    - name: gpu-node-mode
  interval: 10m
  retryInterval: 2m
  timeout: 35m
  path: ./infrastructure/gpu-operator
  prune: false
  force: true
  wait: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
  healthChecks:
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: gpu-operator
      namespace: gpu-operator
    - apiVersion: nvidia.com/v1
      kind: ClusterPolicy
      name: cluster-policy
    # Helm readiness only proves that ClusterPolicy exists. Gate HAMi on the
    # host operands that service the Pod/HAMi ownership path as well.
    - apiVersion: apps/v1
      kind: DaemonSet
      name: nvidia-driver-daemonset
      namespace: gpu-operator
    - apiVersion: apps/v1
      kind: DaemonSet
      name: nvidia-container-toolkit-daemonset
      namespace: gpu-operator
    - apiVersion: apps/v1
      kind: DaemonSet
      name: nvidia-operator-validator
      namespace: gpu-operator
    - apiVersion: apps/v1
      kind: DaemonSet
      name: gpu-feature-discovery
      namespace: gpu-operator
    - apiVersion: apps/v1
      kind: DaemonSet
      name: nvidia-dcgm-exporter
      namespace: gpu-operator
  healthCheckExprs:
    - apiVersion: nvidia.com/v1
      kind: ClusterPolicy
      current: has(status.state) && status.state == 'ready'
      inProgress: '!has(status.state) || status.state != ''ready'''
`)
	if hami {
		name := "hami"
		path := "infrastructure/hami"
		health := `    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: hami
      namespace: hami-system
`
		if allocator == GPUSharedAllocatorDRA || allocator == GPUSharedAllocatorAuto {
			name = "hami-dra"
			path = "infrastructure/hami-dra"
			health = `    - apiVersion: apps/v1
      kind: DaemonSet
      name: hami-dra-driver-kubelet-plugin
      namespace: hami-system
`
		}
		b.WriteString(`---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ` + name + `
  namespace: flux-system
spec:
  dependsOn:
    - name: gpu-operator
  interval: 10m
  retryInterval: 2m
  timeout: 20m
  path: ./` + path + `
  prune: false
  force: true
  wait: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substitute:
      HAMI_CANARY_SUSPENDED: "false"
      HAMI_DRA_CANARY_SUSPENDED: "false"
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
  healthChecks:
` + health)
	}
	return b.String()
}

func patchKustomizationGPU(lines []string) ([]string, bool, error) {
	for _, line := range lines {
		if strings.TrimSpace(line) == "- gpu.yaml" {
			return lines, false, nil
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "- platform.yaml" {
			indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, indent+"- gpu.yaml")
			out = append(out, lines[i+1:]...)
			return out, true, nil
		}
	}
	return nil, false, fmt.Errorf("no `- platform.yaml` resources entry found (file shape drifted from add-cluster.sh output)")
}

func gpuFiles(clusterName string, g GPUConfig) []string {
	if g.Platform != GPUPlatformEnabled {
		return nil
	}
	base := filepath.Join("clusters", clusterName)
	return []string{
		filepath.Join(base, "gpu-node-modes", "nodefeaturerule.yaml"),
		filepath.Join(base, "gpu-node-modes", "kustomization.yaml"),
		filepath.Join(base, "gpu.yaml"),
		filepath.Join(base, "kustomization.yaml") + " (+ gpu.yaml resource)",
	}
}
