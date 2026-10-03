package setup

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func resourceFixture() (*evidenceHostSSHStub, ResourceRequest) {
	pin := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["printf KDC-IDENTITY"] = "KDC-IDENTITY"
	answers[diskListCommand] = `{"blockdevices":[{"path":"/dev/sda","type":"disk","size":107374182400,"ro":false,"maj:min":"8:0","wwn":"0x123","serial":"disk-a","mountpoints":["/"],"fstype":"ext4"},{"path":"/dev/sdb","type":"disk","size":107374182400,"ro":false,"maj:min":"8:16","wwn":"0x456","serial":"disk-b","mountpoints":[null]}]}`
	answers["readlink -e -- '/dev/sdb'"] = "/dev/sdb"
	answers["sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL -- '/dev/sdb'"] = `{"blockdevices":[{"path":"/dev/sdb","type":"disk","size":107374182400,"ro":false,"maj:min":"8:16","wwn":"0x456","serial":"disk-b","mountpoints":[null]}]}`
	answers["sudo -n find /sys/dev/block/8:16/holders -mindepth 1 -maxdepth 1 -printf '%f\\n' && printf 'KDC-HOLDERS-END\\n'"] = "KDC-HOLDERS-END\n"
	answers["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "KDC-WIPEFS-END\n"
	answers[defaultRouteCommand] = `[{"dev":"eth0"}]`
	answers[networkLinksCommand] = `[{"ifname":"eth0","link_type":"ether","address":"02:00:00:00:00:01","mtu":1500,"operstate":"UP","flags":["UP"]}]`
	answers[networkRoutesCommand] = `[{"dev":"eth0","dst":"default"}]`
	answers[loopListCommand] = `{"loopdevices":[]}`
	answers[backingStateCommand] = "clear"
	stub := &evidenceHostSSHStub{hostSSHStub: &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}, evidence: ports.SSHHostKeyEvidence{Address: "192.0.2.10:22", Algorithm: "ssh-ed25519", FingerprintSHA256: pin}}
	return stub, ResourceRequest{Host: Host{ID: "server-1", SSHAlias: "server-1", Role: "server", Primary: true, HostKeySHA256: pin}, Intent: clusterinit.ModeInstall}
}
func TestDiscoveryRequiresExplicitPinBeforeInventory(t *testing.T) {
	for _, mode := range []string{"empty", "wrong", "missing-evidence"} {
		t.Run(mode, func(t *testing.T) {
			stub, req := resourceFixture()
			switch mode {
			case "empty":
				req.Host.HostKeySHA256 = ""
			case "wrong":
				req.Host.HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
			case "missing-evidence":
				stub.evidence = ports.SSHHostKeyEvidence{}
			}
			result, err := DiscoverResources(context.Background(), req, stub)
			if stub.runs != 1 {
				t.Fatalf("inventory executed without identity: %d reads", stub.runs)
			}
			if mode == "empty" {
				if err != nil || result.Host.Facts.HostKey == nil || result.Host.Facts.MachineID != "" || len(result.Disks) != 0 {
					t.Fatalf("identity-only result: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("bad identity accepted")
			}
		})
	}
}
func TestDiscoveryListsCandidatesWithoutSelectingHardware(t *testing.T) {
	stub, req := resourceFixture()
	r, err := DiscoverResources(context.Background(), req, stub)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Disks) != 2 || !r.Disks[0].InUse || r.Disks[1].InUse || !r.Disks[1].Checked || r.Disks[1].StableID != "wwn:0x456" || r.Host.Facts.SelectedDisk != nil || len(r.DefaultRouteNICs) != 1 || len(r.Host.Facts.NICs) == 0 {
		t.Fatalf("unexpected resources %+v", r)
	}
	if stub.uncappedRuns != 0 || stub.writeCalls != 0 {
		t.Fatal("discovery used uncapped or write operation")
	}
	stub, req = resourceFixture()
	stub.differentCommand = diskListCommand
	stub.differentEvidence = stub.evidence
	stub.differentEvidence.FingerprintSHA256 = "changed"
	r, err = DiscoverResources(context.Background(), req, stub)
	if err == nil {
		t.Fatal("identity change returned no error")
	}
	if len(r.Disks) != 0 || hostCheckStatus(r.Host, "disk-candidates") != "blocked" {
		t.Fatal("changed-key candidates accepted")
	}
}
func TestPinnedMismatchStopsRemainingHostReads(t *testing.T) {
	stub, req := resourceFixture()
	req.Host.HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	r := inspectHost(context.Background(), stub, req.Host, req.Intent, false)
	if stub.runs != 1 || hostCheckStatus(r, "host-key") != "blocked" || r.Facts.OSID != "" {
		t.Fatal("mismatch continued inventory")
	}
}

func TestDiscoveryFailsClosedForCandidateAndLateIdentityFailures(t *testing.T) {
	const holders = "sudo -n find /sys/dev/block/8:16/holders -mindepth 1 -maxdepth 1 -printf '%f\\n' && printf 'KDC-HOLDERS-END\\n'"
	const signatures = "sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"
	for _, variant := range []string{"holder", "signature", "missing-signatures", "empty-readlink", "late-key-change"} {
		t.Run(variant, func(t *testing.T) {
			stub, req := resourceFixture()
			a := stub.answers["server-1"]
			switch variant {
			case "holder":
				a[holders] = "dm-0\nKDC-HOLDERS-END\n"
			case "signature":
				a[signatures] = "LVM2_member\nKDC-WIPEFS-END\n"
			case "missing-signatures":
				a[signatures] = ""
			case "empty-readlink":
				a["readlink -e -- '/dev/sdb'"] = ""
			case "late-key-change":
				stub.differentCommand = defaultRouteCommand
				stub.differentEvidence = stub.evidence
				stub.differentEvidence.FingerprintSHA256 = "changed"
			}
			r, err := DiscoverResources(context.Background(), req, stub)
			if len(r.Disks) != 2 || r.Disks[1].Checked {
				t.Fatalf("unsafe candidate %+v", r.Disks)
			}
			if variant == "late-key-change" && (err == nil || r.FileBackingAvailable) {
				t.Fatal("late host identity change preserved a storage proposal")
			}
		})
	}
}

func TestDiscoveryQualifiesFileBackingSeparately(t *testing.T) {
	for _, variant := range []string{"clear", "occupied-loop", "occupied-file", "bad-loops"} {
		t.Run(variant, func(t *testing.T) {
			stub, req := resourceFixture()
			a := stub.answers["server-1"]
			switch variant {
			case "occupied-loop":
				a[loopListCommand] = `{"loopdevices":[{"name":"/dev/loop0","back-file":"/snap/core.img"}]}`
			case "occupied-file":
				a[backingStateCommand] = "occupied"
			case "bad-loops":
				a[loopListCommand] = `{}`
			}
			r, err := DiscoverResources(context.Background(), req, stub)
			if err != nil {
				t.Fatal(err)
			}
			if r.FileBackingAvailable != (variant == "clear") {
				t.Fatalf("wrong backing availability: %+v", r)
			}
		})
	}
}

func TestPlatformDiscoveryAllowsPreparedRKE2ButKeepsFreshDiskSafety(t *testing.T) {
	stub, req := resourceFixture()
	req.PlatformInit = true
	a := stub.answers["server-1"]
	a["systemctl show -p LoadState -p ActiveState rke2-server"] = "LoadState=loaded\nActiveState=active"
	a["if test -e /etc/rancher/rke2/config.yaml || test -e /var/lib/rancher/rke2; then printf present; else printf absent; fi"] = "present"
	a[listeningPortsCommand] = "LISTEN 0 4096 0.0.0.0:6443 0.0.0.0:*\nKDC-SS-END"
	r, err := DiscoverResources(context.Background(), req, stub)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rke2-server", "rke2-files", "tcp-ports"} {
		if hostCheckStatus(r.Host, id) == "blocked" {
			t.Fatalf("prepared platform host blocked: %s", id)
		}
	}
	req.PlatformInit = false
	r, err = DiscoverResources(context.Background(), req, stub)
	if err != nil {
		t.Fatal(err)
	}
	if hostCheckStatus(r.Host, "rke2-server") != "blocked" || hostCheckStatus(r.Host, "rke2-files") != "blocked" || hostCheckStatus(r.Host, "tcp-ports") != "blocked" {
		t.Fatal("greenfield setup lost fresh-RKE2 guards")
	}
	req.PlatformInit = true
	req.Host.Disk = "/dev/sda"
	a["readlink -e -- '/dev/sda'"] = "/dev/sda"
	a["sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL -- '/dev/sda'"] = `{"blockdevices":[{"path":"/dev/sda","type":"disk","size":107374182400,"ro":false,"maj:min":"8:0","wwn":"0x123","serial":"disk-a","mountpoints":["/"],"fstype":"ext4"}]}`
	r, err = DiscoverResources(context.Background(), req, stub)
	if err != nil {
		t.Fatal(err)
	}
	if hostCheckStatus(r.Host, "selected-disk") != "blocked" {
		t.Fatal("prepared RKE2 policy allowed an in-use fresh-install disk")
	}
}
