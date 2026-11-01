package clusterinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRawStorageReplicaDefaultsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		servers int
		value   string
		want    int
		valid   bool
	}{
		{1, "", 1, true}, {2, "", 2, true}, {3, "", 2, true}, {3, "3", 3, true}, {2, "1", 1, true},
		{1, "2", 2, false}, {2, "3", 3, false}, {2, "0", 0, false}, {3, "4", 0, false}, {1, "wrong", 0, false}, {0, "", 1, false}, {4, "", 2, true}, {10, "3", 3, true}, {MaxCephSlots + 1, "", 0, false},
	} {
		t.Run(fmt.Sprintf("%d-servers-%s", tc.servers, tc.value), func(t *testing.T) {
			o := validBase()
			o.RookMode = RookCephMultiNode
			o.CephNodes = map[string]string{}
			for i := 1; i <= tc.servers; i++ {
				o.CephNodes[fmt.Sprintf("server-%d", i)] = "sdb"
			}
			o.Sets = map[string]string{}
			if tc.value != "" {
				o.Sets["CEPH_REPLICATION_SIZE"] = tc.value
			}
			if err := o.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid && o.ObjectStorage().replication() != tc.want {
				t.Fatal("incorrect effective replication")
			}
		})
	}
}

func TestOldStoragePlansRequireNewReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-plan.json")
	data, _ := json.Marshal(map[string]any{"version": "v7"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlan(path); !errors.Is(err, ErrPlanSchemaVersion) {
		t.Fatalf("old storage plan accepted: %v", err)
	}
}

// This also runs with the actual Fleet sources when explicitly supplied. The
// copied tree and generated overlay remain local to the test's temp directory.
func TestRawStorageTopologyRendering(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize required")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if fleet := os.Getenv("KUBE_DC_TEST_FLEET"); fleet != "" {
		if err := os.CopyFS(filepath.Join(root, "infrastructure/object-storage"), os.DirFS(filepath.Join(fleet, "infrastructure/object-storage"))); err != nil {
			t.Fatal(err)
		}
	} else {
		base := "infrastructure/object-storage/modes/rook-ceph-multi-node/"
		write(base+"kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [resources.yaml]\n")
		write(base+"resources.yaml", `apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata: {name: rook-ceph, namespace: rook-ceph}
spec:
  mon: {count: 3, allowMultiplePerNode: false}
  mgr: {count: 2}
  storage:
    useAllNodes: true
    useAllDevices: true
    nodes:
      - {name: "${CEPH_NODE_1}", devices: [{name: "${CEPH_NODE_1_DEVICE}"}]}
      - {name: "${CEPH_NODE_2}", devices: [{name: "${CEPH_NODE_2_DEVICE}"}]}
      - {name: "${CEPH_NODE_3}", devices: [{name: "${CEPH_NODE_3_DEVICE}"}]}
---
apiVersion: ceph.rook.io/v1
kind: CephObjectStore
metadata: {name: my-store, namespace: rook-ceph}
spec:
  metadataPool: {failureDomain: host, replicated: {size: 3}}
  dataPool: {failureDomain: host, replicated: {size: 3}}
  gateway: {instances: 2}
---
apiVersion: ceph.rook.io/v1
kind: CephBlockPool
metadata: {name: rbd-pool, namespace: rook-ceph}
spec: {failureDomain: host, replicated: {size: 3}}
`)
		write("infrastructure/object-storage/bucket-provisioning/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n")
	}
	for _, tc := range []struct {
		hosts, replicas int
		byID            bool
	}{{1, 0, false}, {2, 0, false}, {3, 0, false}, {3, 3, false}, {2, 1, false}, {1, 0, true}, {2, 0, true}} {
		t.Run(fmt.Sprintf("%d-hosts-%d-copies-by-id-%v", tc.hosts, tc.replicas, tc.byID), func(t *testing.T) {
			spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{}, ReplicationSize: tc.replicas, NoS3Exposure: true}
			for i := 1; i <= tc.hosts; i++ {
				spec.CephNodes[fmt.Sprintf("server-%d", i)] = "sdb"
				if tc.byID {
					spec.CephNodes[fmt.Sprintf("server-%d", i)] = fmt.Sprintf("/dev/disk/by-id/wwn-test-%d", i)
				}
			}
			write("clusters/test/object-storage/kustomization.yaml", objectStorageOverlayYAML("test", spec))
			out, err := exec.Command(kustomize, "build", filepath.Join(root, "clusters/test/object-storage")).CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			body := string(out)
			for _, pair := range objectStorageEnvKeys("example.test", spec) {
				body = strings.ReplaceAll(body, "${"+pair[0]+"}", pair[1])
			}
			if strings.Contains(body, "${CEPH_NODE_") {
				t.Fatal("unused or unresolved node slot")
			}
			objects := map[string]map[string]any{}
			dec := yaml.NewDecoder(strings.NewReader(body))
			for {
				var obj map[string]any
				if err := dec.Decode(&obj); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if kind, ok := obj["kind"].(string); ok {
					objects[kind] = obj
				}
			}
			cluster := objects["CephCluster"]["spec"].(map[string]any)
			storage := cluster["storage"].(map[string]any)
			if storage["useAllNodes"] != false || storage["useAllDevices"] != false || len(storage["nodes"].([]any)) != tc.hosts {
				t.Fatal("incorrect device selection")
			}
			for _, entry := range storage["nodes"].([]any) {
				node := entry.(map[string]any)
				devices := node["devices"].([]any)
				if len(devices) != 1 || devices[0].(map[string]any)["name"] != spec.CephNodes[node["name"].(string)] {
					t.Fatal("Rook changed the reviewed device selector")
				}
			}
			mon := cluster["mon"].(map[string]any)
			wantMon := 3
			if tc.hosts == 1 {
				wantMon = 1
			}
			if mon["count"] != wantMon || mon["allowMultiplePerNode"] != (tc.hosts == 2) {
				t.Fatalf("mon placement: %+v", mon)
			}
			global := cluster["cephConfig"].(map[string]any)["global"].(map[string]any)
			if global["osd_pool_default_size"] != fmt.Sprint(spec.replication()) {
				t.Fatal("global replica count must match and remain a string")
			}
			store := objects["CephObjectStore"]["spec"].(map[string]any)
			pools := []map[string]any{store["metadataPool"].(map[string]any), store["dataPool"].(map[string]any), objects["CephBlockPool"]["spec"].(map[string]any)}
			for _, pool := range pools {
				rep := pool["replicated"].(map[string]any)
				if pool["failureDomain"] != "host" || rep["size"] != spec.replication() || rep["requireSafeReplicaSize"] != (spec.replication() > 1) {
					t.Fatalf("incorrect pool %+v", pool)
				}
			}
		})
	}
}

// Ten storage servers render ten slots, and CEPH_MON_NODES pins the monitors.
func TestMultiNodeStoragePatchesListEverySlotAndPinMonitors(t *testing.T) {
	spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{}, ReplicationSize: 3,
		MonNodes: cephMonNodes("node-03, node-01,node-02")}
	for i := 1; i <= 10; i++ {
		spec.CephNodes[fmt.Sprintf("node-%02d", i)] = "disk/by-id/nvme-x"
	}
	body := multiNodeStoragePatches(spec)
	var doc struct {
		Patches []struct {
			Target struct{ Kind string }
			Patch  string
		}
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Path  string
		Value any
	}
	for _, p := range doc.Patches {
		if p.Target.Kind == "CephCluster" {
			if err := json.Unmarshal([]byte(p.Patch), &ops); err != nil {
				t.Fatal(err)
			}
		}
	}
	found := map[string]any{}
	for _, op := range ops {
		found[op.Path] = op.Value
	}
	nodes, _ := found["/spec/storage/nodes"].([]any)
	if len(nodes) != 10 || !strings.Contains(fmt.Sprint(nodes[9]), "${CEPH_NODE_10}") {
		t.Fatalf("storage nodes = %v", nodes)
	}
	placement := fmt.Sprint(found["/spec/placement"])
	if !strings.Contains(placement, "[node-01 node-02 node-03]") {
		t.Fatalf("monitor placement = %s", placement)
	}
	keys := objectStorageEnvKeys("example.com", spec)
	if fmt.Sprint(keys) == "" || !strings.Contains(fmt.Sprint(keys), "CEPH_NODE_10_DEVICE") {
		t.Fatalf("env keys = %v", keys)
	}
}

func TestValidateCephMonNodes(t *testing.T) {
	o := validBase()
	o.RookMode = RookCephMultiNode
	o.CephNodes = map[string]string{"a": "sdb", "b": "sdb", "c": "sdb", "d": "sdb"}
	o.Sets = map[string]string{"CEPH_MON_NODES": "a,b"}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "CEPH_MON_NODES") {
		t.Fatalf("two monitors for a three-monitor cluster accepted: %v", err)
	}
	o.Sets["CEPH_MON_NODES"] = "a,b,c"
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Rook matches a device by bare kernel name or by full /dev path only.
func TestRookDeviceName(t *testing.T) {
	for in, want := range map[string]string{
		"nvme1n1": "nvme1n1", "/dev/sdb": "sdb",
		"disk/by-id/nvme-X": "/dev/disk/by-id/nvme-X", "/dev/disk/by-id/nvme-X": "/dev/disk/by-id/nvme-X",
	} {
		if got := rookDeviceName(in); got != want {
			t.Errorf("rookDeviceName(%q) = %q, want %q", in, got, want)
		}
	}
	spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{"n1": "disk/by-id/nvme-X"}, ReplicationSize: 1}
	if !strings.Contains(fmt.Sprint(objectStorageEnvKeys("example.com", spec)), "CEPH_NODE_1_DEVICE /dev/disk/by-id/nvme-X") {
		t.Fatalf("env keys = %v", objectStorageEnvKeys("example.com", spec))
	}
}
