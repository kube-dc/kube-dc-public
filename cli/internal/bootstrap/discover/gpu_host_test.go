package discover

import (
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func addPCIFunction(f *fakeFS, address, class, vendor, device, subVendor, subDevice, driver string) *fakeFS {
	root := pciDevicesPath + "/" + address
	f.addFile(root+"/class", []byte("0x"+class+"\n")).
		addFile(root+"/vendor", []byte("0x"+vendor+"\n")).
		addFile(root+"/device", []byte("0x"+device+"\n")).
		addFile(root+"/subsystem_vendor", []byte("0x"+subVendor+"\n")).
		addFile(root+"/subsystem_device", []byte("0x"+subDevice+"\n"))
	if driver != "" {
		f.addFile(root+"/uevent", []byte("DRIVER="+driver+"\nPCI_ID="+vendor+":"+device+"\n"))
	}
	return f
}

func addVirtualizationReadiness(f *fakeFS, address string) *fakeFS {
	return f.
		addFile("/sys/kernel/iommu_groups/42/devices/"+address+"/marker", nil).
		addFile("/proc/modules", []byte("vfio_pci 16384 0 - Live 0x0\nkvm 999999 1 kvm_intel, Live 0x0\nkvm_intel 99999 0 - Live 0x0\n")).
		addFile("/dev/vfio/vfio", nil).
		addFile("/dev/kvm", nil)
}

func TestDiscoverGPUHost_NVIDIAExactVariantAndReadiness(t *testing.T) {
	fs := addPCIFunction(newFakeFS(), "0000:65:00.0", "030200", "10DE", "1DB6", "10DE", "1212", "nvidia")
	addVirtualizationReadiness(fs, "0000:65:00.0")
	fs.addFile("/proc/driver/nvidia/version", []byte("NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.126.20  Wed May 13\n"))

	inv, err := DiscoverGPUHost(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Devices) != 1 {
		t.Fatalf("devices=%d want 1", len(inv.Devices))
	}
	d := inv.Devices[0]
	if d.Class != "0302" || d.Variant() != "10de:1db6/10de:1212" {
		t.Fatalf("unexpected exact PCI variant: %+v", d)
	}
	if d.Driver != "nvidia" || d.DriverVersion != "580.126.20" || d.IOMMUGroup != "42" {
		t.Fatalf("unexpected driver/IOMMU facts: %+v", d)
	}
	if !inv.IOMMUEnabled || !inv.VFIOReady || !inv.KVMReady || !inv.SharedReady || !inv.PassthroughReady {
		t.Fatalf("expected fully ready inventory: %+v", inv)
	}
}

func TestDiscoverGPUHost_AMDAPUClass0300(t *testing.T) {
	fs := addPCIFunction(newFakeFS(), "0000:05:00.0", "030000", "1002", "164E", "1043", "8877", "amdgpu")
	fs.addFile("/sys/module/amdgpu/version", []byte("6.12.12\n"))

	inv, err := DiscoverGPUHost(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Devices) != 1 || inv.Devices[0].Class != "0300" {
		t.Fatalf("AMD APU display controller must be discovered: %+v", inv.Devices)
	}
	if !inv.SharedReady || inv.PassthroughReady {
		t.Fatalf("vendor driver should be Shared-ready but no IOMMU/VFIO/KVM means no passthrough: %+v", inv)
	}
}

func TestDiscoverGPUHost_VendorOnlyFunctionsAreNotGPUs(t *testing.T) {
	fs := newFakeFS()
	// NVIDIA HDA audio and AMD encryption functions are deliberately not
	// accelerators despite their vendor IDs.
	addPCIFunction(fs, "0000:65:00.1", "040300", "10de", "10f7", "10de", "0000", "snd_hda_intel")
	addPCIFunction(fs, "0000:06:00.2", "108000", "1002", "1649", "1043", "0000", "ccp")

	inv, err := DiscoverGPUHost(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Devices) != 0 {
		t.Fatalf("vendor-only fallback produced false GPUs: %+v", inv.Devices)
	}
}

func TestDiscoverGPUHost_ClassBasedUnknownVendorStillInventoried(t *testing.T) {
	fs := addPCIFunction(newFakeFS(), "0000:01:00.0", "030200", "1234", "5678", "1234", "0001", "")
	inv, err := DiscoverGPUHost(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Devices) != 1 || inv.Devices[0].Variant() != "1234:5678/1234:0001" {
		t.Fatalf("class-based discovery lost unknown accelerator: %+v", inv.Devices)
	}
	if inv.SharedReady {
		t.Fatal("unknown/unbound accelerator must not be Shared-ready")
	}
}

func TestDiscoverGPUHost_ASPEEDManagementDisplayIsNotAccelerator(t *testing.T) {
	fs := newFakeFS()
	addPCIFunction(fs, "0000:03:00.0", "030000", "1a03", "2000", "1458", "1000", "ast")
	addPCIFunction(fs, "0000:81:00.0", "030200", "10de", "1db6", "10de", "124a", "nvidia")

	inv, err := DiscoverGPUHost(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Devices) != 1 || inv.Devices[0].VendorID != "10de" {
		t.Fatalf("management framebuffer leaked into accelerator inventory: %+v", inv.Devices)
	}
	if !inv.SharedReady {
		t.Fatalf("BMC display must not invalidate NVIDIA shared readiness: %+v", inv)
	}
}

func TestGPUHostProbe_OutputAndRemediation(t *testing.T) {
	fs := addPCIFunction(newFakeFS(), "0000:05:00.0", "030000", "1002", "164e", "1043", "8877", "amdgpu")
	r := NewGPUHostProbe(HostProbeOn, fs).Run(context.Background())
	if r.Status != ports.StatusPartial || r.Severity != ports.SeverityWarn {
		t.Fatalf("status=%s severity=%d want partial/warn", r.Status, r.Severity)
	}
	for _, want := range []string{"1002:164e/1043:8877", "class 0300", "driver amdgpu", "IOMMU unavailable", "VFIO unavailable", "KVM unavailable"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail %q missing %q", r.Detail, want)
		}
	}
	if !strings.Contains(r.FixHint.Text, "Dedicated GPU VM") {
		t.Fatalf("missing mode-specific remediation: %q", r.FixHint.Text)
	}
}

func TestGPUHostProbe_NoGPUIsInformational(t *testing.T) {
	fs := addPCIFunction(newFakeFS(), "0000:00:1f.3", "040300", "8086", "7a50", "1028", "0001", "snd_hda_intel")
	r := NewGPUHostProbe(HostProbeOn, fs).Run(context.Background())
	if r.Status != ports.StatusMissing || r.Severity != ports.SeverityInfo {
		t.Fatalf("ordinary GPU-disabled host must remain clean: %+v", r)
	}
}

func TestGPUHostProbe_OffDoesNotReadHost(t *testing.T) {
	r := NewGPUHostProbe(HostProbeOff, newFakeFS()).Run(context.Background())
	if r.Status != ports.StatusMissing || !strings.Contains(r.Detail, "not applicable") {
		t.Fatalf("off-mode result=%+v", r)
	}
}
