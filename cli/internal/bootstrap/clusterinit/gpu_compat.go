package clusterinit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// ValidateGPUHostCompatibility checks the selected per-node ownership modes
// against read-only facts collected on those nodes. Discovery transport is
// intentionally outside this function: callers may supply local, SSH, or
// fixture inventories without ever probing the operator's laptop implicitly.
func ValidateGPUHostCompatibility(g GPUConfig, inventories map[string]discover.GPUHostInventory) error {
	if g.Platform == "" || g.Platform == GPUPlatformDisabled || g.Platform == GPUPlatformDetectOnly {
		return nil
	}

	nodes := make([]string, 0, len(g.NodeModes))
	for node := range g.NodeModes {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	var errs []string
	for _, node := range nodes {
		mode := g.NodeModes[node]
		if mode == GPUNodeDisabled {
			continue
		}
		inv, ok := inventories[node]
		if !ok {
			errs = append(errs, fmt.Sprintf("node %s: accelerator discovery is missing", node))
			continue
		}
		if len(inv.Devices) == 0 {
			errs = append(errs, fmt.Sprintf("node %s: mode %s requires a PCI class 0300/0302 accelerator", node, mode))
			continue
		}

		switch mode {
		case GPUNodePodHAMi, GPUNodePodHAMiDRA:
			if !allNVIDIA(inv) {
				errs = append(errs, fmt.Sprintf("node %s: Shared GPU currently requires qualified NVIDIA hardware", node))
			}
			if !inv.SharedReady {
				errs = append(errs, fmt.Sprintf("node %s: Shared GPU requires every accelerator to use a supported vendor driver", node))
			}
			validatePreinstalledDriverPin(node, g, inv, &errs)
		case GPUNodeVMPassthrough:
			if !allNVIDIA(inv) {
				errs = append(errs, fmt.Sprintf("node %s: Dedicated GPU VM currently requires qualified NVIDIA hardware", node))
			}
			if !inv.IOMMUEnabled || hasDeviceWithoutIOMMU(inv) {
				errs = append(errs, fmt.Sprintf("node %s: Dedicated GPU VM requires an IOMMU group for every accelerator", node))
			}
			if !inv.VFIOReady {
				errs = append(errs, fmt.Sprintf("node %s: Dedicated GPU VM requires vfio-pci and /dev/vfio/vfio", node))
			}
			if !inv.KVMReady {
				errs = append(errs, fmt.Sprintf("node %s: Dedicated GPU VM requires KVM and /dev/kvm", node))
			}
		case GPUNodeVMVGPU:
			if !allNVIDIA(inv) {
				errs = append(errs, fmt.Sprintf("node %s: Virtual GPU VM currently requires qualified NVIDIA hardware", node))
			}
			if !inv.KVMReady {
				errs = append(errs, fmt.Sprintf("node %s: Virtual GPU VM requires KVM and /dev/kvm", node))
			}
			validatePreinstalledDriverPin(node, g, inv, &errs)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(errs, "; "))
	}
	return nil
}

func hasDeviceWithoutIOMMU(inv discover.GPUHostInventory) bool {
	for _, device := range inv.Devices {
		if device.IOMMUGroup == "" {
			return true
		}
	}
	return false
}

func allNVIDIA(inv discover.GPUHostInventory) bool {
	if len(inv.Devices) == 0 {
		return false
	}
	for _, device := range inv.Devices {
		if device.VendorID != "10de" {
			return false
		}
	}
	return true
}

func validatePreinstalledDriverPin(node string, g GPUConfig, inv discover.GPUHostInventory, errs *[]string) {
	if g.DriverSource != GPUDriverPreinstalled || strings.TrimSpace(g.DriverVersion) == "" {
		return
	}
	for _, device := range inv.Devices {
		if device.VendorID != "10de" {
			continue
		}
		if device.DriverVersion == "" {
			*errs = append(*errs, fmt.Sprintf("node %s: preinstalled NVIDIA driver version is not discoverable (want %s)", node, g.DriverVersion))
			continue
		}
		if device.DriverVersion != g.DriverVersion {
			*errs = append(*errs, fmt.Sprintf("node %s: preinstalled NVIDIA driver %s does not match pin %s", node, device.DriverVersion, g.DriverVersion))
		}
	}
}
