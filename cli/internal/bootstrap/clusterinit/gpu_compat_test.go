package clusterinit

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

func readyNVIDIAInventory() discover.GPUHostInventory {
	return discover.GPUHostInventory{
		Devices: []discover.GPUDevice{{
			Class: "0302", VendorID: "10de", DeviceID: "1db6",
			Driver: "nvidia", DriverVersion: DefaultNVIDIADriverVersion, IOMMUGroup: "42",
		}},
		IOMMUEnabled: true, VFIOReady: true, KVMReady: true,
		SharedReady: true, PassthroughReady: true,
	}
}

func TestValidateGPUHostCompatibility_MixedReadyModes(t *testing.T) {
	g := GPUConfig{
		Platform: GPUPlatformEnabled, DriverSource: GPUDriverOperator,
		NodeModes: map[string]GPUNodeMode{"host5-a": GPUNodePodHAMi, "host6-a": GPUNodeVMPassthrough},
	}
	inventories := map[string]discover.GPUHostInventory{
		"host5-a": readyNVIDIAInventory(), "host6-a": readyNVIDIAInventory(),
	}
	if err := ValidateGPUHostCompatibility(g, inventories); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGPUHostCompatibility_MissingFactsFailClosed(t *testing.T) {
	g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"host5-a": GPUNodePodHAMi}}
	err := ValidateGPUHostCompatibility(g, nil)
	if err == nil || !strings.Contains(err.Error(), "discovery is missing") {
		t.Fatalf("expected missing-discovery error, got %v", err)
	}
}

func TestValidateGPUHostCompatibility_PassthroughRequirements(t *testing.T) {
	inv := readyNVIDIAInventory()
	inv.IOMMUEnabled, inv.VFIOReady, inv.KVMReady = false, false, false
	inv.Devices[0].IOMMUGroup = ""
	g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"host5-a": GPUNodeVMPassthrough}}
	err := ValidateGPUHostCompatibility(g, map[string]discover.GPUHostInventory{"host5-a": inv})
	if err == nil {
		t.Fatal("expected passthrough readiness failure")
	}
	for _, want := range []string{"IOMMU group", "vfio-pci", "/dev/kvm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestValidateGPUHostCompatibility_SharedDriverAndPin(t *testing.T) {
	inv := readyNVIDIAInventory()
	inv.SharedReady = false
	inv.Devices[0].Driver = "nouveau"
	inv.Devices[0].DriverVersion = "535.1"
	g := GPUConfig{
		Platform: GPUPlatformEnabled, DriverSource: GPUDriverPreinstalled,
		DriverVersion: DefaultNVIDIADriverVersion,
		NodeModes:     map[string]GPUNodeMode{"host5-a": GPUNodePodHAMi},
	}
	err := ValidateGPUHostCompatibility(g, map[string]discover.GPUHostInventory{"host5-a": inv})
	if err == nil || !strings.Contains(err.Error(), "supported vendor driver") || !strings.Contains(err.Error(), "does not match pin") {
		t.Fatalf("expected driver and pin failures, got %v", err)
	}
}

func TestValidateGPUHostCompatibility_VGPURejectsUnqualifiedVendor(t *testing.T) {
	inv := readyNVIDIAInventory()
	inv.Devices[0].VendorID = "1002"
	g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"host5-a": GPUNodeVMVGPU}}
	err := ValidateGPUHostCompatibility(g, map[string]discover.GPUHostInventory{"host5-a": inv})
	if err == nil || !strings.Contains(err.Error(), "qualified NVIDIA") {
		t.Fatalf("expected vGPU qualification failure, got %v", err)
	}
}

func TestValidateGPUHostCompatibility_AMDDiscoveredButNotQualified(t *testing.T) {
	inv := readyNVIDIAInventory()
	inv.Devices[0].VendorID = "1002"
	inv.Devices[0].Driver = "amdgpu"
	for _, mode := range []GPUNodeMode{GPUNodePodHAMi, GPUNodeVMPassthrough} {
		g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"host5-a": mode}}
		err := ValidateGPUHostCompatibility(g, map[string]discover.GPUHostInventory{"host5-a": inv})
		if err == nil || !strings.Contains(err.Error(), "qualified NVIDIA") {
			t.Errorf("mode %s must fail closed for discovered-but-unqualified AMD hardware: %v", mode, err)
		}
	}
}

func TestValidateGPUHostCompatibility_DisabledDoesNotRequireInventory(t *testing.T) {
	if err := ValidateGPUHostCompatibility(GPUConfig{Platform: GPUPlatformDisabled}, nil); err != nil {
		t.Fatal(err)
	}
}
