package clusterinit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func TestMultiDeviceInputRoundTripHashAndEveryConsumer(t *testing.T) {
	o := validBase()
	o.RookMode = RookCephMultiNode
	o.SetCephDevices(map[string][]string{"server-1": {"/dev/sdc", "sdb"}, "server-2": {"disk/by-id/example-2"}})
	if err := o.ValidateFields(); err != nil {
		t.Fatal(err)
	}
	spec := o.ObjectStorage()
	if spec.replication() != 2 || len(spec.RawOSDDevices()) != 3 {
		t.Fatal("device count replaced host failure-domain count")
	}
	input := ExportMap(&o)
	if input[KeyCephDevices] == "" || input["CEPH_NODE_1"] != "" {
		t.Fatal("multi-device input must use its versioned representation")
	}
	if err := ValidateInputSpec(input); err != nil {
		t.Fatal(err)
	}
	var loaded InitOptions
	ImportMap(&loaded, input, func(string) bool { return false })
	if !reflect.DeepEqual(spec.DeviceSets(), loaded.ObjectStorage().DeviceSets()) {
		t.Fatal("import dropped a device")
	}
	before, _ := ComputeInputHash(&o)
	loaded.CephDevices["server-1"] = []string{"sdb"}
	after, _ := ComputeInputHash(&loaded)
	if after == before {
		t.Fatal("second device not bound by input hash")
	}
	values := map[string]string{}
	for _, pair := range objectStorageEnvKeys("example.test", spec) {
		values[pair[0]] = pair[1]
	}
	if err := ValidateInputSpec(values); err != nil {
		t.Fatal(err)
	}
	if values["CEPH_NODE_3"] != "server-2" || values["CEPH_NODE_2_DEVICE"] != "sdc" {
		t.Fatal("scaffold device slots were not complete")
	}
	var generated InitOptions
	ImportMap(&generated, values, func(string) bool { return false })
	if !reflect.DeepEqual(spec.DeviceSets(), generated.ObjectStorage().DeviceSets()) {
		t.Fatal("generated env dropped a device")
	}
	patch := multiNodeStoragePatches(spec)
	if strings.Count(patch, `"devices"`) != 2 || !strings.Contains(patch, "CEPH_NODE_2_DEVICE") || !strings.Contains(patch, "CEPH_NODE_3_DEVICE") {
		t.Fatal("Rook patches do not group all devices by host")
	}
}

func TestCephInputsRejectAmbiguousSlotsAndDeviceLists(t *testing.T) {
	for _, values := range []map[string]string{
		{"CEPH_NODE_1": "one", "CEPH_NODE_1_DEVICE": "sdb", "CEPH_NODE_2": "one", "CEPH_NODE_2_DEVICE": "sdc"},
		{"CEPH_NODE_1_DEVICE": "sdb"}, {"CEPH_NODE_1": "one"}, {"CEPH_NODE_17": "one"}, {"CEPH_NODE_01": "one"},
		{KeyCephDevices: `{"version":2,"nodes":{"one":["sdb"]}}`},
		{KeyCephDevices: `{"version":1,"nodes":{"one":["sdb","/dev/sdb"]}}`},
		{KeyCephDevices: `{"version":1,"nodes":{"one":["disk/../../sdb"]}}`},
		{KeyCephDevices: `{"version":1,"nodes":{"one":["sdb"]},"unknown":true}`},
		{KeyCephDevices: `{"version":1,"nodes":{"one":[]}}`},
		{KeyCephDevices: `{"version":1,"nodes":{"one":["sdb"]}}`, "CEPH_NODE_1": "one", "CEPH_NODE_1_DEVICE": "sdc"},
	} {
		if err := ValidateInputSpec(values); err == nil {
			t.Errorf("accepted ambiguous or invalid topology: %v", values)
		}
	}
	o := validBase()
	o.RookMode = RookCephMultiNode
	o.CephNodes, o.CephDevices = map[string]string{"one": "sdb"}, map[string][]string{"one": {"sdc"}}
	if err := o.ValidateFields(); err == nil {
		t.Fatal("mixed native representations accepted")
	}
	for _, devices := range []map[string][]string{{"one": {"sdb", "sdc"}}, {"one": {"sdb"}, "two": {"sdb"}}} {
		o.SetCephDevices(devices)
		o.Sets = map[string]string{"CEPH_REPLICATION_SIZE": "2"}
		err := o.ValidateFields()
		if (len(devices) == 1) != (err != nil) {
			t.Fatal("replicas must count hosts")
		}
	}
}

func TestMultiDeviceExistingSelectionRejectsRemovalAndModeChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "object-storage"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephDevices: map[string][]string{"one": {"sdb", "sdc"}, "two": {"sdb"}}}
	if err := os.WriteFile(filepath.Join(root, "object-storage", "kustomization.yaml"), []byte("resources:\n- ../../../infrastructure/object-storage/modes/rook-ceph-multi-node\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := config.NewEnv()
	for _, pair := range objectStorageEnvKeys("example.test", spec) {
		file.Set(pair[0], pair[1])
	}
	env := filepath.Join(root, "cluster-config.env")
	if err := file.Write(env); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnchangedRawOSDSelection(env, spec); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []ObjectStorageSpec{
		{Mode: RookCephMultiNode, CephNodes: map[string]string{"one": "sdb", "two": "sdb"}},
		{Mode: RookCephPVC}, {Mode: RookDisabled}, {Mode: RookCephLocal, OSDNode: "one", OSDDevice: "loop0"},
	} {
		if err := RequireUnchangedRawOSDSelection(env, changed); err == nil {
			t.Fatal("existing device selection changed without migration")
		}
	}
}
