package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func seedGPUScaffold(t *testing.T, clusterName string, hami bool) string {
	t.Helper()
	repo := seedScaffold(t, clusterName)
	packages := []string{"infrastructure/gpu-operator"}
	if hami {
		packages = append(packages, "infrastructure/hami")
	}
	for _, name := range packages {
		if err := os.MkdirAll(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	platformDir := filepath.Join(repo, "platform", "kube-dc")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformDir, "helmrelease.yaml"), []byte(installerSharedProfileFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

const installerSharedProfileFixture = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  values:
    gpu:
      profiles:
        - id: nvidia-v100-hami
          workload: pod
          allocation: hami
          resourceName: nvidia.com/gpu
          request:
            countResource: nvidia.com/gpu
            memoryResource: nvidia.com/gpumem
            coreResource: nvidia.com/gpucores
`

const installerSharedDRAProfileFixture = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  values:
    gpu:
      profiles:
        - id: nvidia-v100-hami
          workload: pod
          allocation: hami
          allocationBackend: dra
          resourceName: nvidia.com/gpu
          request:
            countResource: nvidia.com/gpu
            memoryResource: nvidia.com/gpumem
            coreResource: nvidia.com/gpucores
          dra:
            driver: hami-core-gpu.project-hami.io
            deviceClassName: kube-dc-nvidia-v100-shared-8g
`

func readGPUScaffoldFile(t *testing.T, repo, clusterName, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "clusters", clusterName, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sharedGPUScaffoldConfig() GPUConfig {
	return GPUConfig{
		Platform:     GPUPlatformEnabled,
		DriverSource: GPUDriverOperator,
		HAMiEnabled:  true,
		NodeModes: map[string]GPUNodeMode{
			"gpu-worker-a": GPUNodePodHAMi,
			"gpu-disabled": GPUNodeDisabled,
		},
		Profiles: []string{installerSharedV100Profile},
	}
}

func TestWriteGPUInfrastructureSharedEndToEndAndIdempotent(t *testing.T) {
	repo := seedGPUScaffold(t, "atlantis", true)
	g := sharedGPUScaffoldConfig()
	var out bytes.Buffer
	if err := WriteGPUInfrastructure(repo, "atlantis", g, &out); err != nil {
		t.Fatal(err)
	}

	rule := readGPUScaffoldFile(t, repo, "atlantis", "gpu-node-modes/nodefeaturerule.yaml")
	for _, want := range []string{
		"feature: system.name", `value: ["gpu-worker-a"]`,
		"feature: pci.device", `value: ["10de"]`, `value: ["0300", "0302"]`,
		"kube-dc.com/gpu.workload-mode: pod-hami",
		"nvidia.com/gpu.workload.config: container",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("node rule missing %q:\n%s", want, rule)
		}
	}
	if strings.Contains(rule, "gpu-disabled") {
		t.Fatalf("disabled node received an ownership rule:\n%s", rule)
	}

	flux := readGPUScaffoldFile(t, repo, "atlantis", "gpu.yaml")
	for _, want := range []string{
		"name: gpu-node-mode", "name: gpu-operator", "name: hami",
		"name: nvidia-driver-daemonset", "name: nvidia-container-toolkit-daemonset",
		"name: nvidia-operator-validator", "name: gpu-feature-discovery",
		"name: nvidia-dcgm-exporter", "HAMI_CANARY_SUSPENDED: \"false\"",
	} {
		if !strings.Contains(flux, want) {
			t.Errorf("gpu flux stack missing %q:\n%s", want, flux)
		}
	}
	if !strings.Contains(out.String(), "billing=false, creation=false") {
		t.Fatalf("operator output does not state closed gates: %q", out.String())
	}

	firstKustomization := readGPUScaffoldFile(t, repo, "atlantis", "kustomization.yaml")
	if strings.Count(firstKustomization, "- gpu.yaml") != 1 {
		t.Fatalf("gpu resource count after first write:\n%s", firstKustomization)
	}
	if err := WriteGPUInfrastructure(repo, "atlantis", g, nil); err != nil {
		t.Fatal(err)
	}
	secondKustomization := readGPUScaffoldFile(t, repo, "atlantis", "kustomization.yaml")
	if firstKustomization != secondKustomization {
		t.Fatalf("idempotent write changed kustomization:\nfirst:\n%s\nsecond:\n%s", firstKustomization, secondKustomization)
	}
}

func TestWriteGPUInfrastructureDRAFirst(t *testing.T) {
	repo := seedGPUScaffold(t, "atlantis", false)
	if err := os.MkdirAll(filepath.Join(repo, "infrastructure", "hami-dra"), 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(repo, "platform", "kube-dc", "helmrelease.yaml")
	if err := os.WriteFile(profilePath, []byte(installerSharedDRAProfileFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	g := sharedGPUScaffoldConfig()
	g.SharedAllocator = GPUSharedAllocatorDRA
	g.NodeModes = map[string]GPUNodeMode{"gpu-worker-a": GPUNodePodHAMiDRA}
	if err := WriteGPUInfrastructure(repo, "atlantis", g, nil); err != nil {
		t.Fatal(err)
	}
	rule := readGPUScaffoldFile(t, repo, "atlantis", "gpu-node-modes/nodefeaturerule.yaml")
	if !strings.Contains(rule, "kube-dc.com/gpu.workload-mode: pod-hami-dra") || !strings.Contains(rule, "nvidia.com/gpu.workload.config: container") {
		t.Fatalf("DRA node ownership is incomplete:\n%s", rule)
	}
	flux := readGPUScaffoldFile(t, repo, "atlantis", "gpu.yaml")
	for _, want := range []string{"name: hami-dra", "path: ./infrastructure/hami-dra", "name: hami-dra-driver-kubelet-plugin", "HAMI_DRA_CANARY_SUSPENDED: \"false\""} {
		if !strings.Contains(flux, want) {
			t.Errorf("DRA flux stack missing %q:\n%s", want, flux)
		}
	}
	if strings.Contains(flux, "path: ./infrastructure/hami\n") {
		t.Fatalf("DRA scaffold also enabled legacy HAMi:\n%s", flux)
	}
}

func TestWriteGPUInfrastructureMixedModesAreSorted(t *testing.T) {
	repo := seedGPUScaffold(t, "atlantis", true)
	g := sharedGPUScaffoldConfig()
	g.NodeModes = map[string]GPUNodeMode{
		"gpu-worker-z": GPUNodeVMPassthrough,
		"gpu-worker-a": GPUNodePodHAMi,
	}
	if err := WriteGPUInfrastructure(repo, "atlantis", g, nil); err != nil {
		t.Fatal(err)
	}
	rule := readGPUScaffoldFile(t, repo, "atlantis", "gpu-node-modes/nodefeaturerule.yaml")
	a := strings.Index(rule, "gpu-worker-a-pod-hami")
	z := strings.Index(rule, "gpu-worker-z-vm-passthrough")
	if a < 0 || z < 0 || a >= z {
		t.Fatalf("node rules are not deterministic:\n%s", rule)
	}
	if !strings.Contains(rule, "nvidia.com/gpu.workload.config: vm-passthrough") {
		t.Fatalf("passthrough ownership value missing:\n%s", rule)
	}
}

func TestWriteGPUInfrastructurePassthroughOnlyDoesNotRequireHAMi(t *testing.T) {
	repo := seedGPUScaffold(t, "atlantis", false)
	g := GPUConfig{
		Platform:     GPUPlatformEnabled,
		DriverSource: GPUDriverOperator,
		NodeModes:    map[string]GPUNodeMode{"gpu-worker-a": GPUNodeVMPassthrough},
	}
	if err := WriteGPUInfrastructure(repo, "atlantis", g, nil); err != nil {
		t.Fatal(err)
	}
	flux := readGPUScaffoldFile(t, repo, "atlantis", "gpu.yaml")
	if strings.Contains(flux, "name: hami") || strings.Contains(flux, "./infrastructure/hami") {
		t.Fatalf("passthrough-only stack unexpectedly enables HAMi:\n%s", flux)
	}
}

func TestWriteGPUInfrastructureNoOpModes(t *testing.T) {
	for _, platform := range []GPUPlatformMode{"", GPUPlatformDisabled, GPUPlatformDetectOnly} {
		t.Run(string(platform), func(t *testing.T) {
			repo := seedScaffold(t, "atlantis")
			if err := WriteGPUInfrastructure(repo, "atlantis", GPUConfig{Platform: platform}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(repo, "clusters", "atlantis", "gpu.yaml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("platform %q wrote gpu.yaml: %v", platform, err)
			}
		})
	}
}

func TestWriteGPUInfrastructureDeferredPathsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		g    GPUConfig
		want error
	}{
		{
			name: "preinstalled driver",
			g:    GPUConfig{Platform: GPUPlatformEnabled, DriverSource: GPUDriverPreinstalled},
			want: ErrGPUPreinstalledDriverScaffoldDeferred,
		},
		{
			name: "vgpu",
			g:    GPUConfig{Platform: GPUPlatformEnabled, DriverSource: GPUDriverOperator, NodeModes: map[string]GPUNodeMode{"gpu-a": GPUNodeVMVGPU}},
			want: ErrGPUVGPUScaffoldDeferred,
		},
		{
			name: "passthrough profile",
			g:    GPUConfig{Platform: GPUPlatformEnabled, DriverSource: GPUDriverOperator, NodeModes: map[string]GPUNodeMode{"gpu-a": GPUNodeVMPassthrough}, Profiles: []string{"nvidia-v100-passthrough"}},
			want: ErrGPUProfileScaffoldDeferred,
		},
		{
			name: "unknown profile",
			g:    GPUConfig{Platform: GPUPlatformEnabled, DriverSource: GPUDriverOperator, NodeModes: map[string]GPUNodeMode{"gpu-a": GPUNodePodHAMi}, Profiles: []string{"nvidia-a100-hami"}},
			want: ErrGPUProfileScaffoldDeferred,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := seedGPUScaffold(t, "atlantis", true)
			if err := WriteGPUInfrastructure(repo, "atlantis", tc.g, nil); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriteGPUInfrastructureRequiresSharedPackages(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	err := WriteGPUInfrastructure(repo, "atlantis", sharedGPUScaffoldConfig(), nil)
	if err == nil || !strings.Contains(err.Error(), "infrastructure/gpu-operator") {
		t.Fatalf("missing GPU Operator package: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "infrastructure/gpu-operator"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = WriteGPUInfrastructure(repo, "atlantis", sharedGPUScaffoldConfig(), nil)
	if err == nil || !strings.Contains(err.Error(), "infrastructure/hami") {
		t.Fatalf("missing HAMi package: %v", err)
	}
}

func TestWriteGPUInfrastructureRejectsSharedProfileContractDrift(t *testing.T) {
	repo := seedGPUScaffold(t, "atlantis", true)
	path := filepath.Join(repo, "platform", "kube-dc", "helmrelease.yaml")
	drifted := strings.Replace(installerSharedProfileFixture, "nvidia.com/gpumem", "requests.nvidia.com/gpumem", 1)
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteGPUInfrastructure(repo, "atlantis", sharedGPUScaffoldConfig(), nil)
	if !errors.Is(err, ErrGPUProfileContractDrift) || !strings.Contains(err.Error(), "memoryResource") {
		t.Fatalf("profile contract drift error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "clusters", "atlantis", "gpu.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("contract drift wrote gpu.yaml: %v", statErr)
	}
}

func TestScaffoldGPUPreflightRunsBeforeClusterScript(t *testing.T) {
	runner := &fakeScriptRunner{}
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{
			ClusterName: "atlantis",
			Domain:      "kdc.atlantis.example.com",
			Preset:      PresetCloudPublicVLAN,
		},
		FleetRepo:      t.TempDir(),
		NodeExternalIP: "203.0.113.52",
		GPU:            sharedGPUScaffoldConfig(),
		Runner:         runner,
	})
	if err == nil || !strings.Contains(err.Error(), "infrastructure/gpu-operator") {
		t.Fatalf("preflight error=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("cluster script ran before GPU preflight: %+v", runner.calls)
	}
}

func TestGPUFilesForOptionsMirrorsScaffold(t *testing.T) {
	o := validSharedGPUOptions()
	files := filesForOptions(&o, FleetState{})
	joined := make([]string, 0, len(files))
	for _, file := range files {
		joined = append(joined, file.Path+"|"+file.Description)
	}
	preview := strings.Join(joined, "\n")
	for _, want := range []string{
		"gpu-node-modes/nodefeaturerule.yaml|exact-node GPU ownership rules",
		"gpu-node-modes/kustomization.yaml|GPU node-mode overlay",
		"gpu.yaml|GPU Operator + optional HAMi Flux layers",
		"kustomization.yaml|kustomization.yaml (Flux entry) incl. gpu.yaml",
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("plan preview missing %q:\n%s", want, preview)
		}
	}
}

func TestGPUObjectNameAndPreview(t *testing.T) {
	longCluster := "region/" + strings.Repeat("very-long-cluster-name-", 4)
	name := gpuObjectName(longCluster)
	if len(name) > 63 || strings.Contains(name, "/") {
		t.Fatalf("invalid generated object name %q (len=%d)", name, len(name))
	}
	if name != gpuObjectName(longCluster) {
		t.Fatal("object-name shortening is not deterministic")
	}

	g := sharedGPUScaffoldConfig()
	files := gpuFiles("eu/dc1", g)
	want := []string{
		"clusters/eu/dc1/gpu-node-modes/nodefeaturerule.yaml",
		"clusters/eu/dc1/gpu-node-modes/kustomization.yaml",
		"clusters/eu/dc1/gpu.yaml",
		"clusters/eu/dc1/kustomization.yaml (+ gpu.yaml resource)",
	}
	if strings.Join(files, "|") != strings.Join(want, "|") {
		t.Fatalf("preview files=%v", files)
	}
	if got := gpuFiles("atlantis", GPUConfig{Platform: GPUPlatformDetectOnly}); got != nil {
		t.Fatalf("detect-only preview=%v", got)
	}
}

func TestGPUGeneratedYAMLParses(t *testing.T) {
	documents := map[string]string{
		"node feature rule": gpuNodeFeatureRuleYAML("atlantis", map[string]GPUNodeMode{
			"gpu-a": GPUNodePodHAMi,
			"gpu-b": GPUNodeVMPassthrough,
		}),
		"node mode kustomization": gpuNodeModesKustomizationYAML(),
		"flux with hami":          gpuFluxYAML("atlantis", true, GPUSharedAllocatorLegacy),
		"flux with hami dra":      gpuFluxYAML("atlantis", true, GPUSharedAllocatorDRA),
		"flux without hami":       gpuFluxYAML("atlantis", false, GPUSharedAllocatorLegacy),
	}
	for name, body := range documents {
		t.Run(name, func(t *testing.T) {
			decoder := yaml.NewDecoder(strings.NewReader(body))
			count := 0
			for {
				var doc map[string]any
				err := decoder.Decode(&doc)
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					t.Fatalf("generated YAML does not parse: %v\n%s", err, body)
				}
				if len(doc) > 0 {
					count++
					if doc["apiVersion"] == nil || doc["kind"] == nil {
						t.Fatalf("document lacks type metadata: %#v", doc)
					}
				}
			}
			if count == 0 {
				t.Fatal("generated no YAML documents")
			}
		})
	}
}
