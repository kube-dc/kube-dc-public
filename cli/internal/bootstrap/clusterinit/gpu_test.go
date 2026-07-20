package clusterinit

import (
	"reflect"
	"strings"
	"testing"
)

func validSharedGPUOptions() InitOptions {
	o := validBase()
	o.GPUPlatform = GPUPlatformEnabled
	o.GPUDriverSource = GPUDriverOperator
	o.GPUOperatorVersion = DefaultGPUOperatorVersion
	o.NVIDIADriverVersion = DefaultNVIDIADriverVersion
	o.NVIDIAToolkitVersion = DefaultNVIDIAToolkitVersion
	o.HAMiEnabled = true
	o.HAMiVersion = DefaultHAMiVersion
	o.HAMiSchedulerVersion = DefaultHAMiSchedulerKubeVersion
	o.GPUNodeModes = map[string]GPUNodeMode{"gpu-worker-a": GPUNodePodHAMi}
	o.GPUProfiles = []string{"nvidia-v100-hami"}
	return o
}

func validDRAGPUOptions() InitOptions {
	o := validSharedGPUOptions()
	o.GPUSharedAllocator = GPUSharedAllocatorDRA
	o.GPUNodeModes = map[string]GPUNodeMode{"gpu-worker-a": GPUNodePodHAMiDRA}
	return o
}

func TestParseGPUNodeModesCanonicalAndDuplicateSafe(t *testing.T) {
	modes, err := ParseGPUNodeModes([]string{"gpu-worker-b=vm-passthrough,gpu-worker-a=pod-hami"})
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalGPUNodeModes(modes); got != "gpu-worker-a=pod-hami,gpu-worker-b=vm-passthrough" {
		t.Fatalf("canonical modes = %q", got)
	}
	if _, err := ParseGPUNodeModes([]string{"gpu-worker-a=pod-hami", "gpu-worker-a=vm-passthrough"}); err == nil {
		t.Fatal("duplicate ownership must fail closed")
	}
}

func TestValidateGPUSharedPilot(t *testing.T) {
	o := validSharedGPUOptions()
	if err := o.Validate(); err != nil {
		t.Fatalf("valid shared GPU: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*InitOptions)
		want   string
	}{
		{"missing hami", func(o *InitOptions) { o.HAMiEnabled = false }, "requires --hami-enabled"},
		{"invalid node", func(o *InitOptions) { o.GPUNodeModes = map[string]GPUNodeMode{"GPU-1": GPUNodePodHAMi} }, "node \"GPU-1\" is invalid"},
		{"profile mode mismatch", func(o *InitOptions) { o.GPUProfiles = []string{"nvidia-v100-passthrough"} }, "requires a vm-passthrough node"},
		{"env injection", func(o *InitOptions) { o.HAMiVersion = "2.9.0\nEVIL=true" }, "unsupported characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validSharedGPUOptions()
			tc.mutate(&got)
			if err := got.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateGPUDRAAllocatorAndOwnership(t *testing.T) {
	valid := validDRAGPUOptions()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid DRA GPU: %v", err)
	}
	tests := []struct {
		name      string
		allocator GPUSharedAllocator
		mode      GPUNodeMode
		want      string
	}{
		{"DRA on legacy node", GPUSharedAllocatorDRA, GPUNodePodHAMi, "pod-hami-dra"},
		{"legacy on DRA node", GPUSharedAllocatorLegacy, GPUNodePodHAMiDRA, "requires dra or auto"},
		{"auto on legacy node", GPUSharedAllocatorAuto, GPUNodePodHAMi, "pod-hami-dra"},
		{"unknown allocator", GPUSharedAllocator("latest"), GPUNodePodHAMiDRA, "must be auto, dra, or hami-device-plugin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := validDRAGPUOptions()
			o.GPUSharedAllocator = tc.allocator
			o.GPUNodeModes = map[string]GPUNodeMode{"gpu-worker-a": tc.mode}
			if err := o.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateGPUEnabledInstallRejectsUnqualifiedVersionTuple(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*InitOptions)
		flag   string
	}{
		{"operator", func(o *InitOptions) { o.GPUOperatorVersion = "v26.3.4" }, "--gpu-operator-version"},
		{"driver", func(o *InitOptions) { o.NVIDIADriverVersion = "580.130.00" }, "--nvidia-driver-version"},
		{"toolkit", func(o *InitOptions) { o.NVIDIAToolkitVersion = "v1.20.0" }, "--nvidia-toolkit-version"},
		{"hami", func(o *InitOptions) { o.HAMiVersion = "2.9.1" }, "--hami-version"},
		{"scheduler", func(o *InitOptions) { o.HAMiSchedulerVersion = "v1.35.4" }, "--hami-scheduler-version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validSharedGPUOptions()
			tc.mutate(&o)
			err := o.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.flag) || !strings.Contains(err.Error(), "not in the qualified GPU install tuple") {
				t.Fatalf("got %v, want qualified-tuple error for %s", err, tc.flag)
			}
		})
	}
}

func TestGPUConfigEnvIsPublicAndDeterministic(t *testing.T) {
	o := validSharedGPUOptions()
	o.GPUNodeModes["gpu-worker-b"] = GPUNodeVMPassthrough
	env := GPUConfigEnv(o.GPU())
	if env["GPU_NODE_MODES"] != "gpu-worker-a=pod-hami,gpu-worker-b=vm-passthrough" {
		t.Fatalf("modes=%q", env["GPU_NODE_MODES"])
	}
	if env["GPU_PROFILES"] != "nvidia-v100-hami" || env["GPU_CATALOG_ENABLED"] != "true" {
		t.Fatalf("env=%v", env)
	}
	if env["GPU_BILLING_ELIGIBLE"] != "false" || env["GPU_SHARED_CREATION_ENABLED"] != "false" || env["GPU_VM_CREATION_ENABLED"] != "false" {
		t.Fatalf("installer must keep commercial/create gates closed: %v", env)
	}
	for key := range env {
		if strings.Contains(strings.ToLower(key), "license") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "uuid") {
			t.Fatalf("secret/device identity boundary broken by key %q", key)
		}
	}
}

func TestGPUConfigEnvCatalogRequiresSupportedProfile(t *testing.T) {
	o := validSharedGPUOptions()
	g := o.GPU()
	g.Profiles = nil
	if got := GPUConfigEnv(g)["GPU_CATALOG_ENABLED"]; got != "false" {
		t.Fatalf("infrastructure-only config exposed catalog: %q", got)
	}
	g.Profiles = []string{"nvidia-v100-passthrough"}
	if got := GPUConfigEnv(g)["GPU_CATALOG_ENABLED"]; got != "false" {
		t.Fatalf("deferred profile exposed catalog: %q", got)
	}
	g.Profiles = []string{installerSharedV100Profile}
	if got := GPUConfigEnv(g)["GPU_CATALOG_ENABLED"]; got != "true" {
		t.Fatalf("supported shared profile did not expose catalog: %q", got)
	}
	for _, platform := range []GPUPlatformMode{GPUPlatformDisabled, GPUPlatformDetectOnly} {
		env := GPUConfigEnv(GPUConfig{Platform: platform, Profiles: []string{installerSharedV100Profile}})
		for _, key := range []string{"GPU_CATALOG_ENABLED", "GPU_BILLING_ELIGIBLE", "GPU_SHARED_CREATION_ENABLED", "GPU_VM_CREATION_ENABLED"} {
			if env[key] != "false" {
				t.Fatalf("platform %q gate %s=%q", platform, key, env[key])
			}
		}
	}
}

func TestGPUConfigPrefillRoundTripAndHash(t *testing.T) {
	orig := validDRAGPUOptions()
	orig.AllowUnassignedGPUs = false
	m := ExportMap(&orig)
	got := &InitOptions{}
	if ignored := ImportMap(got, m, noFlagsChanged); len(ignored) != 0 {
		t.Fatalf("ignored=%v", ignored)
	}
	if !reflect.DeepEqual(got.GPU(), orig.GPU()) {
		t.Fatalf("GPU round trip:\n got=%+v\nwant=%+v", got.GPU(), orig.GPU())
	}
	for _, key := range []string{"GPU_ENABLED", "GPU_CATALOG_ENABLED", "GPU_SHARED_ALLOCATOR", "GPU_NODE_MODES", "GPU_PROFILES"} {
		if _, leaked := got.Sets[key]; leaked {
			t.Fatalf("promoted GPU key %s leaked into --set", key)
		}
	}

	h1, err := ComputeInputHash(&orig)
	if err != nil {
		t.Fatal(err)
	}
	orig.GPUNodeModes["gpu-worker-b"] = GPUNodeVMPassthrough
	h2, err := ComputeInputHash(&orig)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("GPU node ownership must participate in plan input hash")
	}
	orig2 := validDRAGPUOptions()
	legacyHash, err := ComputeInputHash(&orig2)
	if err != nil {
		t.Fatal(err)
	}
	orig2.GPUSharedAllocator = GPUSharedAllocatorAuto
	autoHash, err := ComputeInputHash(&orig2)
	if err != nil {
		t.Fatal(err)
	}
	if legacyHash == autoHash {
		t.Fatal("GPU allocator must participate in plan input hash")
	}
}

func TestGPUConfigPrefillUnderstandsExistingFleetCatalogGate(t *testing.T) {
	o := &InitOptions{}
	ImportMap(o, map[string]string{"GPU_CATALOG_ENABLED": "true"}, noFlagsChanged)
	if o.GPUPlatform != GPUPlatformEnabled {
		t.Fatalf("existing fleet catalog gate should promote GPU platform, got %q", o.GPUPlatform)
	}
	if _, leaked := o.Sets["GPU_CATALOG_ENABLED"]; leaked {
		t.Fatal("promoted catalog gate must not leak into generic --set")
	}
}
