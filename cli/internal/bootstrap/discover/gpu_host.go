package discover

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const pciDevicesPath = "/sys/bus/pci/devices"

var nvidiaKernelVersion = regexp.MustCompile(`(?i)kernel module\s+([0-9]+(?:\.[0-9]+)+)`)

// GPUDevice is the non-secret, host-local PCI identity needed to decide which
// accelerator modes are possible. Address is retained for installer-side
// diagnostics and must not be copied into tenant-facing APIs or catalogs.
type GPUDevice struct {
	Address           string
	Class             string
	VendorID          string
	DeviceID          string
	SubsystemVendorID string
	SubsystemDeviceID string
	Driver            string
	DriverVersion     string
	IOMMUGroup        string
}

// Variant returns the exact PCI product and subsystem tuple. Keeping the
// subsystem IDs prevents two board variants with the same chip ID from being
// silently treated as equivalent.
func (d GPUDevice) Variant() string {
	base := d.VendorID + ":" + d.DeviceID
	if d.SubsystemVendorID == "" || d.SubsystemDeviceID == "" {
		return base
	}
	return base + "/" + d.SubsystemVendorID + ":" + d.SubsystemDeviceID
}

// GPUHostInventory is a read-only discovery result. GPU presence is derived
// exclusively from PCI display-controller classes 0300 and 0302; vendor-only
// matches are deliberately forbidden because NVIDIA/AMD machines commonly
// expose non-GPU audio, USB, and bridge functions with the same vendor ID.
type GPUHostInventory struct {
	// SystemUUID binds an SSH-discovered machine to the Kubernetes Node read
	// from an explicit target kubeconfig. It is installer-local identity and
	// must never be copied into fleet config or tenant-facing APIs.
	SystemUUID       string
	Devices          []GPUDevice
	IOMMUEnabled     bool
	VFIOReady        bool
	KVMReady         bool
	SharedReady      bool
	PassthroughReady bool
}

// DiscoverGPUHost reads Linux sysfs/procfs and returns exact accelerator facts.
// It performs no mutations and does not shell out to optional tools such as
// lspci, making installer results deterministic on minimal hosts.
func DiscoverGPUHost(h hostFS) (GPUHostInventory, error) {
	if h == nil {
		h = realHostFS{}
	}
	entries, err := h.ReadDir(pciDevicesPath)
	if err != nil {
		return GPUHostInventory{}, fmt.Errorf("read PCI devices: %w", err)
	}

	iommuGroups := discoverIOMMUGroups(h)
	inv := GPUHostInventory{
		SystemUUID: strings.ToLower(strings.TrimSpace(readOptionalFile(h, "/sys/class/dmi/id/product_uuid"))),
	}
	for _, entry := range entries {
		address := entry.Name()
		root := pciDevicesPath + "/" + address
		class := readHex(h, root+"/class")
		if len(class) < 4 || (class[:4] != "0300" && class[:4] != "0302") {
			continue
		}
		vendorID := readHex(h, root+"/vendor")
		// ASPEED display controllers provide the server's BMC/remote console;
		// they are not workload accelerators. Keeping this exclusion exact
		// preserves class-based discovery for unknown accelerator vendors while
		// preventing a common management framebuffer from invalidating GPU hosts.
		if class[:4] == "0300" && vendorID == "1a03" {
			continue
		}
		uevent := readKeyValues(h, root+"/uevent")
		driver := strings.ToLower(uevent["DRIVER"])
		device := GPUDevice{
			Address:           address,
			Class:             class[:4],
			VendorID:          vendorID,
			DeviceID:          readHex(h, root+"/device"),
			SubsystemVendorID: readHex(h, root+"/subsystem_vendor"),
			SubsystemDeviceID: readHex(h, root+"/subsystem_device"),
			Driver:            driver,
			DriverVersion:     discoverDriverVersion(h, driver),
			IOMMUGroup:        iommuGroups[address],
		}
		inv.Devices = append(inv.Devices, device)
	}
	sort.Slice(inv.Devices, func(i, j int) bool { return inv.Devices[i].Address < inv.Devices[j].Address })

	modules := loadedKernelModules(h)
	inv.IOMMUEnabled = len(iommuGroups) > 0
	inv.VFIOReady = modules["vfio_pci"] && dirHasEntry(h, "/dev/vfio", "vfio")
	inv.KVMReady = modules["kvm"] && dirHasEntry(h, "/dev", "kvm")
	inv.SharedReady = len(inv.Devices) > 0
	inv.PassthroughReady = len(inv.Devices) > 0 && inv.VFIOReady && inv.KVMReady
	for _, device := range inv.Devices {
		if device.Driver != "nvidia" && device.Driver != "amdgpu" {
			inv.SharedReady = false
		}
		if device.IOMMUGroup == "" {
			inv.PassthroughReady = false
		}
	}
	return inv, nil
}

func readOptionalFile(h hostFS, path string) string {
	body, err := h.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(body)
}

func discoverIOMMUGroups(h hostFS) map[string]string {
	result := map[string]string{}
	groups, err := h.ReadDir("/sys/kernel/iommu_groups")
	if err != nil {
		return result
	}
	for _, group := range groups {
		devices, err := h.ReadDir("/sys/kernel/iommu_groups/" + group.Name() + "/devices")
		if err != nil {
			continue
		}
		for _, device := range devices {
			result[device.Name()] = group.Name()
		}
	}
	return result
}

func loadedKernelModules(h hostFS) map[string]bool {
	result := map[string]bool{}
	body, err := h.ReadFile("/proc/modules")
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			result[fields[0]] = true
		}
	}
	return result
}

func readHex(h hostFS, path string) string {
	body, err := h.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(string(body)), "0x"))
}

func readKeyValues(h hostFS, path string) map[string]string {
	result := map[string]string{}
	body, err := h.ReadFile(path)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			result[key] = strings.TrimSpace(value)
		}
	}
	return result
}

func discoverDriverVersion(h hostFS, driver string) string {
	switch driver {
	case "nvidia":
		body, err := h.ReadFile("/proc/driver/nvidia/version")
		if err == nil {
			if match := nvidiaKernelVersion.FindStringSubmatch(string(body)); len(match) == 2 {
				return match[1]
			}
		}
	case "amdgpu":
		body, err := h.ReadFile("/sys/module/amdgpu/version")
		if err == nil {
			return strings.TrimSpace(string(body))
		}
	}
	return ""
}

func dirHasEntry(h hostFS, path, name string) bool {
	entries, err := h.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// GPUHostProbe projects the reusable inventory into the existing doctor row
// contract. Absence is informational so GPU-disabled installations remain a
// clean success; incomplete detected hardware is a warning with remediation.
type GPUHostProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewGPUHostProbe(mode HostProbeMode, h hostFS) *GPUHostProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &GPUHostProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*GPUHostProbe)(nil)

func (p *GPUHostProbe) Name() string { return "accelerator-pci" }

func (p *GPUHostProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable(p.Name())
	}
	if runtime.GOOS != "linux" {
		return notApplicable("accelerator-pci (non-Linux host)")
	}
	inv, err := DiscoverGPUHost(p.fs)
	if err != nil {
		return ports.Result{Status: ports.StatusPartial, Severity: ports.SeverityWarn, Detail: err.Error()}
	}
	if len(inv.Devices) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "no PCI class 0300/0302 accelerator found",
		}
	}

	variants := make([]string, 0, len(inv.Devices))
	for _, device := range inv.Devices {
		driver := device.Driver
		if driver == "" {
			driver = "unbound"
		}
		if device.DriverVersion != "" {
			driver += " " + device.DriverVersion
		}
		variants = append(variants, fmt.Sprintf("%s class %s driver %s", device.Variant(), device.Class, driver))
	}
	detail := fmt.Sprintf("%d accelerator(s): %s; IOMMU %s; VFIO %s; KVM %s",
		len(inv.Devices), strings.Join(variants, ", "), readyWord(inv.IOMMUEnabled), readyWord(inv.VFIOReady), readyWord(inv.KVMReady))
	if inv.SharedReady && inv.PassthroughReady {
		return ports.Result{Status: ports.StatusInstalled, Severity: ports.SeverityInfo, Detail: detail}
	}

	var fixes []string
	if !inv.SharedReady {
		fixes = append(fixes, "bind each accelerator to its supported vendor driver for Shared GPU")
	}
	if !inv.PassthroughReady {
		fixes = append(fixes, "enable IOMMU and load vfio-pci plus KVM before Dedicated GPU VM mode")
	}
	return ports.Result{
		Status:   ports.StatusPartial,
		Severity: ports.SeverityWarn,
		Detail:   detail,
		FixHint:  ports.FixHint{Text: strings.Join(fixes, "; ")},
	}
}

func readyWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "unavailable"
}
