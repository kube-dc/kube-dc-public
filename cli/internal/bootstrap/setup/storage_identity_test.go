package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func storageIdentityFixture() (*evidenceHostSSHStub, Host) {
	stub, req := resourceFixture()
	a := stub.answers["server-1"]
	a["sudo -n udevadm info --query=symlink --name='/dev/sdb' && printf '\\nKDC-UDEV-END\\n'"] = "disk/by-id/wwn-0x456\nKDC-UDEV-END\n"
	a["readlink -e -- '/dev/disk/by-id/wwn-0x456'"] = "/dev/sdb"
	return stub, req.Host
}

func TestStorageIdentityPersistsActualConsumerPath(t *testing.T) {
	stub, host := storageIdentityFixture()
	binding, err := inspectStorageIdentity(context.Background(), host, "/dev/sdb", stub)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BoundObjectStorage(clusterinit.ObjectStorageSpec{Mode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{"server-1": "sdb"}}, []StorageIdentity{binding})
	if err != nil {
		t.Fatal(err)
	}
	if bound.CephNodes["server-1"] != "/dev/disk/by-id/wwn-0x456" {
		t.Fatal("consumer retained a reusable kernel name")
	}
	if _, err := BoundObjectStorage(clusterinit.ObjectStorageSpec{Mode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{"server-1": "sdb", "server-2": "sdc"}}, []StorageIdentity{binding}); err == nil {
		t.Fatal("unreviewed imported OSD accepted")
	}
	for _, variant := range []string{"missing-id", "alias-replaced", "occupied", "wrong-key"} {
		t.Run(variant, func(t *testing.T) {
			stub, host := storageIdentityFixture()
			a := stub.answers["server-1"]
			switch variant {
			case "missing-id":
				a["sudo -n udevadm info --query=symlink --name='/dev/sdb' && printf '\\nKDC-UDEV-END\\n'"] = "KDC-UDEV-END"
			case "alias-replaced":
				a["readlink -e -- '/dev/disk/by-id/wwn-0x456'"] = "/dev/sdc"
			case "occupied":
				a["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "ext4\nKDC-WIPEFS-END\n"
			case "wrong-key":
				stub.evidence.FingerprintSHA256 = "SHA256:" + strings.Repeat("B", 43)
			}
			if _, err := inspectStorageIdentity(context.Background(), host, "/dev/sdb", stub); err == nil {
				t.Fatal("unsafe OSD accepted")
			}
			if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
				t.Fatal("storage guard made an unsafe call")
			}
		})
	}
}
