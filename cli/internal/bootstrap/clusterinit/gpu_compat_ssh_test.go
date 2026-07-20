package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type compatSSHFixture struct {
	files map[string]string
	dirs  map[string]string
	hosts []ports.SSHHost
	err   map[string]error
}

func compatKey(host ports.SSHHost, path string) string { return host.Alias + ":" + path }

func (f *compatSSHFixture) Fetch(_ context.Context, host ports.SSHHost, path string) ([]byte, error) {
	f.hosts = append(f.hosts, host)
	key := compatKey(host, path)
	if err := f.err[key]; err != nil {
		return nil, err
	}
	if body, ok := f.files[key]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("not found")
}

func (f *compatSSHFixture) Run(_ context.Context, host ports.SSHHost, command string) ([]byte, error) {
	f.hosts = append(f.hosts, host)
	const prefix = "find -- '"
	const suffix = "' -mindepth 1 -maxdepth 1 -printf '%f\\n'"
	if !strings.HasPrefix(command, prefix) || !strings.HasSuffix(command, suffix) {
		return nil, fmt.Errorf("unexpected command: %s", command)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(command, prefix), suffix)
	key := compatKey(host, path)
	if err := f.err[key]; err != nil {
		return nil, err
	}
	if body, ok := f.dirs[key]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("not found")
}

func (*compatSSHFixture) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return errors.New("Put must not be called by GPU compatibility discovery")
}

func addCompatibleNVIDIAHost(f *compatSSHFixture, alias, address string) {
	file := func(path, body string) { f.files[alias+":"+path] = body }
	dir := func(path, body string) { f.dirs[alias+":"+path] = body }
	root := "/sys/bus/pci/devices/" + address
	dir("/sys/bus/pci/devices", address+"\n")
	file(root+"/class", "0x030200\n")
	file(root+"/vendor", "0x10de\n")
	file(root+"/device", "0x1db4\n")
	file(root+"/subsystem_vendor", "0x10de\n")
	file(root+"/subsystem_device", "0x1212\n")
	file(root+"/uevent", "DRIVER=nvidia\n")
	file("/proc/driver/nvidia/version", "NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.126.20\n")
	file("/proc/modules", "nvidia 1 0 - Live 0x0\nvfio_pci 1 0 - Live 0x0\nkvm 1 0 - Live 0x0\n")
	dir("/sys/kernel/iommu_groups", "42\n")
	dir("/sys/kernel/iommu_groups/42/devices", address+"\n")
	dir("/dev/vfio", "vfio\n42\n")
	dir("/dev", "kvm\n")
}

func TestDiscoverAndValidateGPUHostsSSHCollectsSelectedNodes(t *testing.T) {
	fake := &compatSSHFixture{files: map[string]string{}, dirs: map[string]string{}, err: map[string]error{}}
	addCompatibleNVIDIAHost(fake, "ssh-a", "0000:65:00.0")
	addCompatibleNVIDIAHost(fake, "ssh-b", "0000:b3:00.0")
	g := GPUConfig{
		Platform:      GPUPlatformEnabled,
		DriverSource:  GPUDriverPreinstalled,
		DriverVersion: "580.126.20",
		NodeModes: map[string]GPUNodeMode{
			"gpu-a": GPUNodePodHAMi,
			"gpu-b": GPUNodeVMPassthrough,
			"cpu-c": GPUNodeDisabled,
		},
	}
	resolve := func(node string) ports.SSHHost {
		return ports.SSHHost{Alias: "ssh-" + strings.TrimPrefix(node, "gpu-")}
	}

	inventories, err := DiscoverAndValidateGPUHostsSSH(context.Background(), g, fake, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventories) != 2 || len(inventories["gpu-a"].Devices) != 1 || len(inventories["gpu-b"].Devices) != 1 {
		t.Fatalf("unexpected inventories: %+v", inventories)
	}
	for _, host := range fake.hosts {
		if host.Alias == "cpu-c" {
			t.Fatal("disabled node was probed")
		}
	}
}

func TestDiscoverAndValidateGPUHostsSSHFailureIsNodeAttributed(t *testing.T) {
	boom := errors.New("dial refused")
	fake := &compatSSHFixture{
		files: map[string]string{}, dirs: map[string]string{},
		err: map[string]error{"gpu-a:/sys/bus/pci/devices": boom},
	}
	g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"gpu-a": GPUNodePodHAMi}}
	_, err := DiscoverAndValidateGPUHostsSSH(context.Background(), g, fake, nil)
	if err == nil || !errors.Is(err, ErrValidation) || !errors.Is(err, boom) || !strings.Contains(err.Error(), "node gpu-a") {
		t.Fatalf("expected node-attributed discovery error, got %v", err)
	}
}

func TestDiscoverAndValidateGPUHostsSSHDisabledModesAreNetworkFree(t *testing.T) {
	for _, platform := range []GPUPlatformMode{GPUPlatformDisabled, GPUPlatformDetectOnly} {
		inventories, err := DiscoverAndValidateGPUHostsSSH(context.Background(), GPUConfig{Platform: platform}, nil, nil)
		if err != nil || len(inventories) != 0 {
			t.Fatalf("platform %s: inventories=%v err=%v", platform, inventories, err)
		}
	}
}
