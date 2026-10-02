package clusterinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise Kustomize's real target-selected deletion, including Fleet versions
// with only the original prep Job. String assertions cannot verify this.
func TestRawLocalOverlayOmitsLoopJobs(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize is required for overlay integration tests")
	}
	for _, jobs := range []int{1, 2} {
		for _, device := range []string{"sdb", "/dev/nvme1n1", "loop0", ""} {
			t.Run(fmt.Sprintf("%d-jobs-%s", jobs, device), func(t *testing.T) {
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
				base := "infrastructure/object-storage/modes/rook-ceph-local/"
				resources := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - cluster.yaml\n"
				write(base+"cluster.yaml", "apiVersion: ceph.rook.io/v1\nkind: CephCluster\nmetadata:\n  name: rook-ceph\n  namespace: rook-ceph\nspec:\n  storage:\n    nodes:\n      - name: ${CEPH_LOCAL_OSD_NODE}\n        devices:\n          - name: ${CEPH_LOCAL_OSD_DEVICE:=loop0}\n")
				for i := 1; i <= jobs; i++ {
					name := "ceph-disk-prep"
					if i == 2 {
						name += "-2"
					}
					resources += "  - " + name + ".yaml\n"
					write(base+name+".yaml", "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: "+name+"\n  namespace: rook-ceph\n")
				}
				write(base+"kustomization.yaml", resources)
				write("infrastructure/object-storage/bucket-provisioning/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n")
				write("clusters/eu/test/object-storage/kustomization.yaml", objectStorageOverlayYAML("eu/test", ObjectStorageSpec{Mode: RookCephLocal, OSDDevice: device, NoS3Exposure: true}))
				out, err := exec.Command(kustomize, "build", filepath.Join(root, "clusters/eu/test/object-storage")).CombinedOutput()
				if err != nil {
					t.Fatalf("render: %v\n%s", err, out)
				}
				wantJobs := jobs
				if device == "sdb" || device == "/dev/nvme1n1" {
					wantJobs = 0
				}
				if got := strings.Count(string(out), "kind: Job"); got != wantJobs {
					t.Fatalf("got %d jobs, want %d\n%s", got, wantJobs, out)
				}
				if !strings.Contains(string(out), "kind: CephCluster") {
					t.Fatal("removed data plane")
				}
			})
		}
	}
}

// A checked-out Fleet can qualify the generated overlay against its actual
// shared resources without writing to that checkout or contacting a cluster.
func TestRawLocalOverlayAgainstFleet(t *testing.T) {
	fleet := os.Getenv("KUBE_DC_TEST_FLEET")
	if fleet == "" {
		t.Skip("set KUBE_DC_TEST_FLEET to a Fleet checkout")
	}
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "infrastructure/object-storage"), os.DirFS(filepath.Join(fleet, "infrastructure/object-storage"))); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "clusters/storage-test/object-storage")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"sdb", "loop0"} {
		spec := ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "server-1", OSDDevice: device, OSDSizeGB: 50}
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(objectStorageOverlayYAML("storage-test", spec)), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(kustomize, "build", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("Fleet render: %v\n%s", err, out)
		}
		for _, kind := range []string{"CephCluster", "CephObjectStore", "ObjectBucketClaim", "HTTPRoute"} {
			if !strings.Contains(string(out), "kind: "+kind+"\n") {
				t.Fatalf("missing %s", kind)
			}
		}
		if got := strings.Count(string(out), "kind: Job\n"); (device == "sdb" && got != 0) || (device == "loop0" && got != 2) {
			t.Fatalf("%s: unexpected Job count %d", device, got)
		}
	}
}
