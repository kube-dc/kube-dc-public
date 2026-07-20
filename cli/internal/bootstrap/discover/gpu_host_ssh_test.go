package discover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type gpuSSHFixture struct {
	files    map[string]string
	dirs     map[string]string
	fetchErr map[string]error
	runErr   map[string]error
	hosts    []ports.SSHHost
}

type cancelingGPUSSHFixture struct {
	*gpuSSHFixture
	cancel context.CancelFunc
}

func (f *cancelingGPUSSHFixture) Fetch(ctx context.Context, host ports.SSHHost, path string) ([]byte, error) {
	body, err := f.gpuSSHFixture.Fetch(ctx, host, path)
	f.cancel()
	return body, err
}

func (f *gpuSSHFixture) Fetch(_ context.Context, host ports.SSHHost, path string) ([]byte, error) {
	f.hosts = append(f.hosts, host)
	if err := f.fetchErr[path]; err != nil {
		return nil, err
	}
	body, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(body), nil
}

func (f *gpuSSHFixture) Run(_ context.Context, host ports.SSHHost, command string) ([]byte, error) {
	f.hosts = append(f.hosts, host)
	for path, body := range f.dirs {
		want := "find -- " + shellQuote(path) + " -mindepth 1 -maxdepth 1 -printf '%f\\n'"
		if command == want {
			if err := f.runErr[path]; err != nil {
				return nil, err
			}
			return []byte(body), nil
		}
	}
	return nil, fmt.Errorf("unexpected command: %s", command)
}

func (*gpuSSHFixture) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return errors.New("Put must not be called by GPU discovery")
}

func TestDiscoverGPUHostSSHReusesQualifiedInventory(t *testing.T) {
	const pci = "0000:65:00.0"
	fake := &gpuSSHFixture{
		files: map[string]string{
			"/sys/class/dmi/id/product_uuid":                 "11111111-2222-4333-8444-555555555555\n",
			pciDevicesPath + "/" + pci + "/class":            "0x030200\n",
			pciDevicesPath + "/" + pci + "/vendor":           "0x10de\n",
			pciDevicesPath + "/" + pci + "/device":           "0x1db4\n",
			pciDevicesPath + "/" + pci + "/subsystem_vendor": "0x10de\n",
			pciDevicesPath + "/" + pci + "/subsystem_device": "0x1212\n",
			pciDevicesPath + "/" + pci + "/uevent":           "DRIVER=nvidia\n",
			"/proc/driver/nvidia/version":                    "NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.126.20\n",
			"/proc/modules":                                  "nvidia 1 0 - Live 0x0\nvfio_pci 1 0 - Live 0x0\nkvm 1 0 - Live 0x0\n",
		},
		dirs: map[string]string{
			pciDevicesPath:                        pci + "\n0000:65:00.1\n",
			"/sys/kernel/iommu_groups":            "42\n",
			"/sys/kernel/iommu_groups/42/devices": pci + "\n",
			"/dev/vfio":                           "vfio\n42\n",
			"/dev":                                "kvm\n",
		},
		fetchErr: map[string]error{},
		runErr:   map[string]error{},
	}
	host := ports.SSHHost{Alias: "host5-a"}

	inv, err := DiscoverGPUHostSSH(context.Background(), fake, host)
	if err != nil {
		t.Fatalf("DiscoverGPUHostSSH: %v", err)
	}
	if len(inv.Devices) != 1 || inv.Devices[0].Variant() != "10de:1db4/10de:1212" {
		t.Fatalf("unexpected devices: %+v", inv.Devices)
	}
	if inv.SystemUUID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("system UUID not normalized: %q", inv.SystemUUID)
	}
	if inv.Devices[0].DriverVersion != "580.126.20" || inv.Devices[0].IOMMUGroup != "42" {
		t.Fatalf("driver/IOMMU facts not preserved: %+v", inv.Devices[0])
	}
	if !inv.SharedReady || !inv.PassthroughReady || !inv.VFIOReady || !inv.KVMReady {
		t.Fatalf("readiness not preserved: %+v", inv)
	}
	for _, got := range fake.hosts {
		if got.Alias != host.Alias {
			t.Fatalf("SSH target drifted: got %+v want %+v", got, host)
		}
	}
}

func TestDiscoverGPUHostSSHDirectoryFailureIsAttributed(t *testing.T) {
	boom := errors.New("permission denied")
	fake := &gpuSSHFixture{
		files:    map[string]string{},
		dirs:     map[string]string{pciDevicesPath: ""},
		fetchErr: map[string]error{},
		runErr:   map[string]error{pciDevicesPath: boom},
	}
	_, err := DiscoverGPUHostSSH(context.Background(), fake, ports.SSHHost{Alias: "host5-a"})
	if err == nil || !strings.Contains(err.Error(), "list "+pciDevicesPath) || !errors.Is(err, boom) {
		t.Fatalf("expected attributed directory error, got %v", err)
	}
}

func TestDiscoverGPUHostSSHRejectsNilClient(t *testing.T) {
	_, err := DiscoverGPUHostSSH(context.Background(), nil, ports.SSHHost{Alias: "host5-a"})
	if err == nil || !strings.Contains(err.Error(), "nil client") {
		t.Fatalf("expected nil-client error, got %v", err)
	}
}

func TestDiscoverGPUHostSSHRejectsPartialInventoryAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &gpuSSHFixture{
		files:    map[string]string{"/sys/class/dmi/id/product_uuid": "fixture-uuid\n"},
		dirs:     map[string]string{pciDevicesPath: ""},
		fetchErr: map[string]error{},
		runErr:   map[string]error{},
	}
	_, err := DiscoverGPUHostSSH(ctx, &cancelingGPUSSHFixture{gpuSSHFixture: base, cancel: cancel}, ports.SSHHost{Alias: "host5-a"})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "probe incomplete") {
		t.Fatalf("expected explicit partial-probe rejection, got %v", err)
	}
}

func TestSSHHostFSFiltersUnsafeDirectoryNames(t *testing.T) {
	fake := &gpuSSHFixture{
		files:    map[string]string{},
		dirs:     map[string]string{"/safe": "ok\n../escape\n..\n.\n\n"},
		fetchErr: map[string]error{},
		runErr:   map[string]error{},
	}
	entries, err := (sshHostFS{ctx: context.Background(), client: fake, host: ports.SSHHost{Alias: "host5-a"}}).ReadDir("/safe")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "ok" {
		t.Fatalf("unsafe names were not filtered: %+v", entries)
	}
}
