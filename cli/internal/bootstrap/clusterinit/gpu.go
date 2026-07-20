package clusterinit

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var gpuVersionRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
var gpuProfileRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

// GPUPlatformMode controls whether bootstrap installs GPU components. Detect-only
// records discovered hardware without assigning a device owner or exposing a
// tenant product.
type GPUPlatformMode string

const (
	GPUPlatformDisabled   GPUPlatformMode = "disabled"
	GPUPlatformEnabled    GPUPlatformMode = "enabled"
	GPUPlatformDetectOnly GPUPlatformMode = "detect-only"
)

var AllGPUPlatformModes = []GPUPlatformMode{
	GPUPlatformDetectOnly, GPUPlatformDisabled, GPUPlatformEnabled,
}

type GPUDriverSource string

const (
	GPUDriverOperator     GPUDriverSource = "gpu-operator"
	GPUDriverPreinstalled GPUDriverSource = "preinstalled"
)

var AllGPUDriverSources = []GPUDriverSource{GPUDriverOperator, GPUDriverPreinstalled}

// GPUSharedAllocator selects the one allocator which owns a Shared GPU pool.
// Auto is a preflight choice, never a runtime fallback: once apply starts it
// resolves to DRA or fails without silently enabling the compatibility path.
type GPUSharedAllocator string

const (
	GPUSharedAllocatorAuto   GPUSharedAllocator = "auto"
	GPUSharedAllocatorDRA    GPUSharedAllocator = "dra"
	GPUSharedAllocatorLegacy GPUSharedAllocator = "hami-device-plugin"
)

var AllGPUSharedAllocators = []GPUSharedAllocator{
	GPUSharedAllocatorDRA, GPUSharedAllocatorLegacy, GPUSharedAllocatorAuto,
}

type GPUNodeMode string

const (
	GPUNodeDisabled      GPUNodeMode = "disabled"
	GPUNodePodHAMi       GPUNodeMode = "pod-hami"
	GPUNodePodHAMiDRA    GPUNodeMode = "pod-hami-dra"
	GPUNodeVMPassthrough GPUNodeMode = "vm-passthrough"
	GPUNodeVMVGPU        GPUNodeMode = "vm-vgpu"
)

var AllGPUNodeModes = []GPUNodeMode{
	GPUNodeDisabled, GPUNodePodHAMi, GPUNodePodHAMiDRA, GPUNodeVMPassthrough, GPUNodeVMVGPU,
}

// Qualified pilot defaults. These mirror the versions proven on the
// approved non-production V100 qualification cluster. They are defaults, not secrets.
const (
	DefaultGPUOperatorVersion       = "v26.3.3"
	DefaultNVIDIADriverVersion      = "580.126.20"
	DefaultNVIDIAToolkitVersion     = "v1.19.1"
	DefaultHAMiVersion              = "2.9.0"
	DefaultHAMiSchedulerKubeVersion = "v1.35.3"
)

// GPUConfig is the non-secret installer/fleet contract. NodeModes owns device
// ownership; a node can have exactly one entry. VGPUSecretReady records only
// that the separate SOPS workflow is ready and never carries license or
// registry credentials.
type GPUConfig struct {
	Platform             GPUPlatformMode
	DriverSource         GPUDriverSource
	OperatorVersion      string
	DriverVersion        string
	ToolkitVersion       string
	HAMiEnabled          bool
	SharedAllocator      GPUSharedAllocator
	HAMiVersion          string
	HAMiSchedulerVersion string
	NodeModes            map[string]GPUNodeMode
	Profiles             []string
	AllowUnassigned      bool
	VGPUSecretReady      bool
}

func (o *InitOptions) GPU() GPUConfig {
	if o == nil {
		return GPUConfig{}
	}
	return GPUConfig{
		Platform: o.GPUPlatform, DriverSource: o.GPUDriverSource,
		OperatorVersion: o.GPUOperatorVersion, DriverVersion: o.NVIDIADriverVersion,
		ToolkitVersion: o.NVIDIAToolkitVersion, HAMiEnabled: o.HAMiEnabled,
		SharedAllocator: o.GPUSharedAllocator,
		HAMiVersion:     o.HAMiVersion, HAMiSchedulerVersion: o.HAMiSchedulerVersion,
		NodeModes: o.GPUNodeModes, Profiles: canonicalGPUProfiles(o.GPUProfiles),
		AllowUnassigned: o.AllowUnassignedGPUs, VGPUSecretReady: o.VGPUSecretReady,
	}
}

func canonicalGPUProfiles(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item != "" && !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	sort.Strings(out)
	return out
}

func canonicalGPUNodeModes(in map[string]GPUNodeMode) string {
	keys := make([]string, 0, len(in))
	for node := range in {
		keys = append(keys, node)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, node := range keys {
		parts = append(parts, node+"="+string(in[node]))
	}
	return strings.Join(parts, ",")
}

func effectiveGPUSharedAllocator(g GPUConfig) GPUSharedAllocator {
	if g.SharedAllocator == "" {
		return GPUSharedAllocatorLegacy
	}
	return g.SharedAllocator
}

// ParseGPUNodeModes parses repeatable/comma-separated NODE=MODE values and
// rejects duplicate ownership instead of silently accepting the last value.
func ParseGPUNodeModes(values []string) (map[string]GPUNodeMode, error) {
	out := map[string]GPUNodeMode{}
	for _, raw := range values {
		for _, pair := range strings.Split(raw, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			node, mode, ok := strings.Cut(pair, "=")
			node, mode = strings.TrimSpace(node), strings.TrimSpace(mode)
			if !ok || node == "" || mode == "" {
				return nil, fmt.Errorf("%q: expected NODE=MODE", pair)
			}
			if _, exists := out[node]; exists {
				return nil, fmt.Errorf("node %q has more than one GPU mode", node)
			}
			out[node] = GPUNodeMode(mode)
		}
	}
	return out, nil
}

func validateGPU(o *InitOptions) []string {
	g := o.GPU()
	validPlatform := map[GPUPlatformMode]bool{"": true, GPUPlatformDisabled: true, GPUPlatformEnabled: true, GPUPlatformDetectOnly: true}
	if !validPlatform[g.Platform] {
		return []string{fmt.Sprintf("--gpu-platform %q must be disabled, enabled, or detect-only", g.Platform)}
	}
	if g.Platform == "" || g.Platform == GPUPlatformDisabled {
		if len(g.NodeModes) > 0 || len(g.Profiles) > 0 || g.HAMiEnabled || g.AllowUnassigned || g.VGPUSecretReady {
			return []string{"--gpu-platform=disabled cannot carry node modes, profiles, HAMi, unassigned consent, or vGPU readiness"}
		}
		return nil
	}

	var errs []string
	if g.DriverSource != GPUDriverOperator && g.DriverSource != GPUDriverPreinstalled {
		errs = append(errs, fmt.Sprintf("--gpu-driver-source %q must be gpu-operator or preinstalled", g.DriverSource))
	}
	if g.Platform == GPUPlatformEnabled && len(g.NodeModes) == 0 {
		errs = append(errs, "--gpu-platform=enabled requires at least one --gpu-node-mode")
	}
	if g.Platform == GPUPlatformDetectOnly && len(g.NodeModes) > 0 {
		errs = append(errs, "--gpu-platform=detect-only cannot assign GPU node modes")
	}
	if g.Platform == GPUPlatformDetectOnly && (len(g.Profiles) > 0 || g.HAMiEnabled || g.VGPUSecretReady) {
		errs = append(errs, "--gpu-platform=detect-only cannot enable profiles, Shared GPU runtime, or vGPU readiness")
	}
	if g.Platform == GPUPlatformDetectOnly && !g.AllowUnassigned {
		errs = append(errs, "--gpu-platform=detect-only requires --allow-unassigned-gpus consent")
	}

	validNodeMode := map[GPUNodeMode]bool{GPUNodeDisabled: true, GPUNodePodHAMi: true, GPUNodePodHAMiDRA: true, GPUNodeVMPassthrough: true, GPUNodeVMVGPU: true}
	hasMode := map[GPUNodeMode]bool{}
	for node, mode := range g.NodeModes {
		if !k8sNodeNameRegex.MatchString(node) {
			errs = append(errs, fmt.Sprintf("--gpu-node-mode node %q is invalid", node))
		}
		if !validNodeMode[mode] {
			errs = append(errs, fmt.Sprintf("--gpu-node-mode %s=%q is invalid", node, mode))
		}
		hasMode[mode] = true
	}
	if g.Platform == GPUPlatformEnabled && !hasMode[GPUNodePodHAMi] && !hasMode[GPUNodePodHAMiDRA] && !hasMode[GPUNodeVMPassthrough] && !hasMode[GPUNodeVMVGPU] {
		errs = append(errs, "--gpu-platform=enabled requires at least one active pod-hami, pod-hami-dra, vm-passthrough, or vm-vgpu node mode")
	}
	if (hasMode[GPUNodePodHAMi] || hasMode[GPUNodePodHAMiDRA]) && !g.HAMiEnabled {
		errs = append(errs, "pod-hami node mode requires --hami-enabled")
	}
	if g.HAMiEnabled && !hasMode[GPUNodePodHAMi] && !hasMode[GPUNodePodHAMiDRA] {
		errs = append(errs, "--hami-enabled requires at least one pod-hami or pod-hami-dra node")
	}
	validAllocator := map[GPUSharedAllocator]bool{"": true, GPUSharedAllocatorAuto: true, GPUSharedAllocatorDRA: true, GPUSharedAllocatorLegacy: true}
	if !validAllocator[g.SharedAllocator] {
		errs = append(errs, fmt.Sprintf("--gpu-shared-allocator %q must be auto, dra, or hami-device-plugin", g.SharedAllocator))
	}
	effectiveAllocator := g.SharedAllocator
	if effectiveAllocator == "" {
		effectiveAllocator = GPUSharedAllocatorLegacy
	}
	if g.HAMiEnabled && effectiveAllocator == GPUSharedAllocatorLegacy && !hasMode[GPUNodePodHAMi] {
		errs = append(errs, "hami-device-plugin allocator requires a pod-hami node")
	}
	if g.HAMiEnabled && (effectiveAllocator == GPUSharedAllocatorDRA || effectiveAllocator == GPUSharedAllocatorAuto) && !hasMode[GPUNodePodHAMiDRA] {
		errs = append(errs, fmt.Sprintf("%s allocator requires a pod-hami-dra node", effectiveAllocator))
	}
	if hasMode[GPUNodePodHAMi] && effectiveAllocator != GPUSharedAllocatorLegacy {
		errs = append(errs, "pod-hami node ownership is only valid with hami-device-plugin allocator")
	}
	if hasMode[GPUNodePodHAMiDRA] && effectiveAllocator == GPUSharedAllocatorLegacy {
		errs = append(errs, "pod-hami-dra node ownership requires dra or auto allocator")
	}
	if hasMode[GPUNodeVMVGPU] && !g.VGPUSecretReady {
		errs = append(errs, "vm-vgpu node mode requires --vgpu-secret-ready after the SOPS secret workflow completes")
	}
	if g.DriverSource == GPUDriverOperator && strings.TrimSpace(g.OperatorVersion) == "" {
		errs = append(errs, "gpu-operator driver source requires --gpu-operator-version")
	}
	versionFields := []struct{ flag, value string }{
		{"--gpu-operator-version", g.OperatorVersion},
		{"--nvidia-driver-version", g.DriverVersion},
		{"--nvidia-toolkit-version", g.ToolkitVersion},
		{"--hami-version", g.HAMiVersion},
		{"--hami-scheduler-version", g.HAMiSchedulerVersion},
	}
	for _, field := range versionFields {
		if field.value != "" && !gpuVersionRegex.MatchString(field.value) {
			errs = append(errs, fmt.Sprintf("%s %q contains unsupported characters", field.flag, field.value))
		}
	}
	// An enabled install is a released compatibility tuple, not a collection of
	// independently overridable latest-version knobs. A newly qualified tuple is
	// promoted by changing these release defaults after the upgrade gate and
	// hardware canary pass; until then the installer fails closed.
	if g.Platform == GPUPlatformEnabled && g.DriverSource == GPUDriverOperator {
		qualified := []struct{ flag, got, want string }{
			{"--gpu-operator-version", g.OperatorVersion, DefaultGPUOperatorVersion},
			{"--nvidia-driver-version", g.DriverVersion, DefaultNVIDIADriverVersion},
			{"--nvidia-toolkit-version", g.ToolkitVersion, DefaultNVIDIAToolkitVersion},
		}
		if g.HAMiEnabled {
			qualified = append(qualified,
				struct{ flag, got, want string }{"--hami-version", g.HAMiVersion, DefaultHAMiVersion},
				struct{ flag, got, want string }{"--hami-scheduler-version", g.HAMiSchedulerVersion, DefaultHAMiSchedulerKubeVersion},
			)
		}
		for _, component := range qualified {
			if component.got != component.want {
				errs = append(errs, fmt.Sprintf("%s %q is not in the qualified GPU install tuple (want %s)", component.flag, component.got, component.want))
			}
		}
	}
	if hasMode[GPUNodePodHAMi] {
		if strings.TrimSpace(g.HAMiVersion) == "" || strings.TrimSpace(g.HAMiSchedulerVersion) == "" {
			errs = append(errs, "pod-hami mode requires --hami-version and --hami-scheduler-version")
		}
	}
	for _, profile := range g.Profiles {
		if !gpuProfileRegex.MatchString(profile) {
			errs = append(errs, fmt.Sprintf("GPU profile %q must be a stable lowercase ID (a-z, 0-9, '-', '.')", profile))
			continue
		}
		switch {
		case strings.HasSuffix(profile, "-hami") && !hasMode[GPUNodePodHAMi] && !hasMode[GPUNodePodHAMiDRA]:
			errs = append(errs, fmt.Sprintf("GPU profile %q requires a pod-hami or pod-hami-dra node", profile))
		case strings.HasSuffix(profile, "-passthrough") && !hasMode[GPUNodeVMPassthrough]:
			errs = append(errs, fmt.Sprintf("GPU profile %q requires a vm-passthrough node", profile))
		case strings.HasSuffix(profile, "-vgpu") && !hasMode[GPUNodeVMVGPU]:
			errs = append(errs, fmt.Sprintf("GPU profile %q requires a vm-vgpu node", profile))
		}
	}
	return errs
}

// ValidateGPUConfig exposes the accelerator-only checks to the thin TUI and
// other generators without requiring an otherwise-complete InitOptions.
func ValidateGPUConfig(o *InitOptions) error {
	if errs := validateGPU(o); len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(errs, "; "))
	}
	return nil
}

// GPUConfigEnv returns the public, deterministic fleet substitutions. Mode
// assignment and enabled profiles remain explicit too so clone/review can
// reproduce the generated overlays. No secret value is accepted here.
func GPUConfigEnv(g GPUConfig) map[string]string {
	if g.Platform == "" {
		return map[string]string{}
	}
	if g.Platform == GPUPlatformDisabled {
		return map[string]string{
			"GPU_ENABLED":                 "false",
			"GPU_CATALOG_ENABLED":         "false",
			"GPU_BILLING_ELIGIBLE":        "false",
			"GPU_SHARED_CREATION_ENABLED": "false",
			"GPU_VM_CREATION_ENABLED":     "false",
		}
	}
	profiles := canonicalGPUProfiles(g.Profiles)
	sharedCatalog := false
	for _, profile := range profiles {
		if profile == installerSharedV100Profile {
			sharedCatalog = true
			break
		}
	}
	m := map[string]string{
		"GPU_ENABLED":                 fmt.Sprintf("%t", g.Platform == GPUPlatformEnabled),
		"GPU_CATALOG_ENABLED":         fmt.Sprintf("%t", g.Platform == GPUPlatformEnabled && sharedCatalog),
		"GPU_BILLING_ELIGIBLE":        "false",
		"GPU_SHARED_CREATION_ENABLED": "false",
		"GPU_VM_CREATION_ENABLED":     "false",
		"GPU_DRIVER_SOURCE":           string(g.DriverSource),
		"GPU_OPERATOR_VERSION":        g.OperatorVersion,
		"NVIDIA_DRIVER_VERSION":       g.DriverVersion,
		"NVIDIA_TOOLKIT_VERSION":      g.ToolkitVersion,
		"HAMI_ENABLED":                fmt.Sprintf("%t", g.HAMiEnabled),
		"GPU_SHARED_ALLOCATOR":        string(g.SharedAllocator),
		"HAMI_VERSION":                g.HAMiVersion,
		"HAMI_KUBE_SCHEDULER_VERSION": g.HAMiSchedulerVersion,
		"GPU_NODE_MODES":              canonicalGPUNodeModes(g.NodeModes),
		"GPU_PROFILES":                strings.Join(profiles, ","),
	}
	for k, v := range m {
		if strings.TrimSpace(v) == "" {
			delete(m, k)
		}
	}
	return m
}
