package clusterinit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeGPUClusterReader struct {
	runtimes map[string]ports.GPUNodeRuntime
	err      error
	nodes    []string
}

func (f *fakeGPUClusterReader) GPUNodeRuntimes(_ context.Context, nodes []string) (map[string]ports.GPUNodeRuntime, error) {
	f.nodes = append([]string(nil), nodes...)
	return f.runtimes, f.err
}

func conflictFixture(mode GPUNodeMode, owners ...string) (GPUConfig, map[string]discover.GPUHostInventory, *fakeGPUClusterReader) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	g := GPUConfig{Platform: GPUPlatformEnabled, NodeModes: map[string]GPUNodeMode{"gpu-a": mode}}
	inventories := map[string]discover.GPUHostInventory{"gpu-a": {SystemUUID: "{" + strings.ToUpper(uuid) + "}"}}
	pluginOwners := make([]ports.GPUPluginOwner, 0, len(owners))
	for _, owner := range owners {
		namespace := "gpu-operator"
		if owner == hamiDRAPluginOwner {
			namespace = "hami-system"
		}
		pluginOwners = append(pluginOwners, ports.GPUPluginOwner{Namespace: namespace, Kind: "DaemonSet", Name: owner})
	}
	reader := &fakeGPUClusterReader{runtimes: map[string]ports.GPUNodeRuntime{
		"gpu-a": {Name: "gpu-a", SystemUUID: uuid, Labels: map[string]string{
			gpuActiveModeLabel: string(mode), gpuExpectedModeLabel: string(mode),
		}, PluginOwners: pluginOwners},
	}}
	return g, inventories, reader
}

func TestValidateGPUClusterConflictsAllowsMatchingOwners(t *testing.T) {
	tests := []struct {
		mode   GPUNodeMode
		owners []string
	}{
		{GPUNodePodHAMi, []string{hamiPluginOwner}},
		{GPUNodePodHAMiDRA, []string{hamiDRAPluginOwner}},
		{GPUNodeVMPassthrough, []string{sandboxPluginOwner}},
		{GPUNodePodHAMi, nil}, // first install, before the selected plugin exists
	}
	for _, tc := range tests {
		g, inventory, reader := conflictFixture(tc.mode, tc.owners...)
		if err := ValidateGPUClusterConflicts(context.Background(), g, inventory, reader); err != nil {
			t.Fatalf("mode %s: %v", tc.mode, err)
		}
		if !reflect.DeepEqual(reader.nodes, []string{"gpu-a"}) {
			t.Fatalf("reader nodes=%v", reader.nodes)
		}
	}
}

func TestValidateGPUClusterConflictsFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(GPUConfig, map[string]discover.GPUHostInventory, *fakeGPUClusterReader)
		want   string
	}{
		{"missing SSH UUID", func(_ GPUConfig, i map[string]discover.GPUHostInventory, _ *fakeGPUClusterReader) {
			v := i["gpu-a"]
			v.SystemUUID = ""
			i["gpu-a"] = v
		}, "UUID is missing"},
		{"UUID mismatch", func(_ GPUConfig, _ map[string]discover.GPUHostInventory, r *fakeGPUClusterReader) {
			v := r.runtimes["gpu-a"]
			v.SystemUUID = "different"
			r.runtimes["gpu-a"] = v
		}, "UUID mismatch"},
		{"node absent", func(_ GPUConfig, _ map[string]discover.GPUHostInventory, r *fakeGPUClusterReader) {
			delete(r.runtimes, "gpu-a")
		}, "absent from the target kubeconfig"},
		{"label mismatch", func(_ GPUConfig, _ map[string]discover.GPUHostInventory, r *fakeGPUClusterReader) {
			v := r.runtimes["gpu-a"]
			v.Labels[gpuActiveModeLabel] = string(GPUNodeVMPassthrough)
			r.runtimes["gpu-a"] = v
		}, "mode labels"},
		{"regular plugin", func(_ GPUConfig, _ map[string]discover.GPUHostInventory, r *fakeGPUClusterReader) {
			v := r.runtimes["gpu-a"]
			v.PluginOwners = []ports.GPUPluginOwner{{Namespace: "gpu-operator", Kind: "DaemonSet", Name: regularPluginOwner}}
			r.runtimes["gpu-a"] = v
		}, "forbidden regular"},
		{"sandbox on shared", func(_ GPUConfig, _ map[string]discover.GPUHostInventory, r *fakeGPUClusterReader) {
			v := r.runtimes["gpu-a"]
			v.PluginOwners = []ports.GPUPluginOwner{{Namespace: "gpu-operator", Kind: "DaemonSet", Name: sandboxPluginOwner}}
			r.runtimes["gpu-a"] = v
		}, "VM sandbox plugin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, inventory, reader := conflictFixture(GPUNodePodHAMi, hamiPluginOwner)
			tc.mutate(g, inventory, reader)
			err := ValidateGPUClusterConflicts(context.Background(), g, inventory, reader)
			if !errors.Is(err, ErrGPUClusterConflict) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateGPUClusterConflictsRejectsHAMiOnVMNode(t *testing.T) {
	for _, owner := range []string{hamiPluginOwner, hamiDRAPluginOwner} {
		g, inventory, reader := conflictFixture(GPUNodeVMPassthrough, owner)
		err := ValidateGPUClusterConflicts(context.Background(), g, inventory, reader)
		if !errors.Is(err, ErrGPUClusterConflict) || !strings.Contains(err.Error(), "HAMi allocator") {
			t.Fatalf("owner=%s error=%v", owner, err)
		}
	}
}

func TestValidateGPUClusterConflictsRejectsLegacyAndDRACrossOwnership(t *testing.T) {
	tests := []struct {
		mode  GPUNodeMode
		owner string
		want  string
	}{
		{GPUNodePodHAMiDRA, hamiPluginOwner, "legacy HAMi plugin"},
		{GPUNodePodHAMi, hamiDRAPluginOwner, "DRA plugin"},
	}
	for _, tc := range tests {
		g, inventory, reader := conflictFixture(tc.mode, tc.owner)
		err := ValidateGPUClusterConflicts(context.Background(), g, inventory, reader)
		if !errors.Is(err, ErrGPUClusterConflict) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("mode=%s owner=%s error=%v", tc.mode, tc.owner, err)
		}
	}
}

func TestValidateGPUClusterConflictsDisabledIsNetworkFree(t *testing.T) {
	reader := &fakeGPUClusterReader{err: errors.New("must not be called")}
	for _, platform := range []GPUPlatformMode{"", GPUPlatformDisabled, GPUPlatformDetectOnly} {
		if err := ValidateGPUClusterConflicts(context.Background(), GPUConfig{Platform: platform}, nil, reader); err != nil {
			t.Fatalf("platform %q: %v", platform, err)
		}
	}
	if reader.nodes != nil {
		t.Fatalf("disabled reader called with %v", reader.nodes)
	}
}

func TestValidateGPUClusterConflictsReaderError(t *testing.T) {
	g, inventory, reader := conflictFixture(GPUNodePodHAMi)
	reader.err = errors.New("forbidden")
	err := ValidateGPUClusterConflicts(context.Background(), g, inventory, reader)
	if !errors.Is(err, ErrGPUClusterConflict) || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("error=%v", err)
	}
}
