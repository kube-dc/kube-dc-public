package setup

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func freshHostRecheckFixture(t *testing.T) (Compiled, HostObservation, *evidenceHostSSHStub) {
	return freshHostRecheckFixtureWithNIC(t, "host")
}

func freshHostRecheckFixtureWithNIC(t *testing.T, source string) (Compiled, HostObservation, *evidenceHostSSHStub) {
	t.Helper()
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	if source != "host" {
		s.Hosts[0].NIC = ""
		config := baseConfig("demo")
		if source == "default" {
			config += "EXT_NET_INTERFACE=eth0\n"
		}
		if err := os.WriteFile(s.Platform.ConfigFile, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeReleaseTestRecord(t, s, completeReleaseRecord(s))
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), s.Hosts[0].ManagementAddress, "inactive")
	answers[freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 200000000000 20000000000 180000000000 10% /\n"
	stub := &evidenceHostSSHStub{
		hostSSHStub: &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}},
		evidence:    ports.SSHHostKeyEvidence{Address: "server-1:22", Algorithm: "ssh-ed25519", FingerprintSHA256: s.Hosts[0].HostKeySHA256},
	}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || len(report.Hosts) != 1 || hostCheckStatus(report.Hosts[0], "profile-free-space") != "pass" {
		t.Fatalf("fixture has no approved observation: %+v, %v", report, err)
	}
	return c, report.Hosts[0], stub
}

func TestRecheckFreshHostAcceptsUnchangedReviewedTarget(t *testing.T) {
	c, reviewed, stub := freshHostRecheckFixture(t)
	if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], reviewed, stub); err != nil {
		t.Fatal(err)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("pre-write recheck made an unsafe SSH call: %+v", stub.hostSSHStub)
	}
}

func TestRecheckFreshHostUsesInheritedOrManagementInterface(t *testing.T) {
	for _, source := range []string{"default", "management"} {
		t.Run(source, func(t *testing.T) {
			c, reviewed, stub := freshHostRecheckFixtureWithNIC(t, source)
			if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], reviewed, stub); err != nil {
				t.Fatal(err)
			}
			stub.answers["server-1"]["ip -j addr show"] = `[{"ifname":"eth0","address":"02:00:00:00:00:02","mtu":1500,"addr_info":[{"local":"192.0.2.10"}]}]`
			if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], reviewed, stub); err == nil {
				t.Fatal("inherited interface drift passed the live recheck")
			}
			if source == "management" {
				for _, nics := range []string{
					`[{"ifname":"eth0","address":"02:00:00:00:00:01","mtu":1500,"addr_info":[]},{"ifname":"eth1","address":"02:00:00:00:00:02","mtu":1500,"addr_info":[{"local":"192.0.2.10"}]}]`,
					`[{"ifname":"eth0","address":"02:00:00:00:00:01","mtu":1500,"addr_info":[{"local":"192.0.2.10"}]},{"ifname":"eth1","address":"02:00:00:00:00:02","mtu":1500,"addr_info":[{"local":"192.0.2.10"}]}]`,
				} {
					stub.answers["server-1"]["ip -j addr show"] = nics
					if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], reviewed, stub); err == nil {
						t.Fatal("moved or duplicated management address passed the live recheck")
					}
				}
			}
		})
	}
}

func TestRecheckFreshHostRejectsTargetDrift(t *testing.T) {
	for _, edit := range []struct {
		name string
		fn   func(*evidenceHostSSHStub)
	}{
		{"machine", func(s *evidenceHostSSHStub) { s.answers["server-1"]["cat /etc/machine-id"] = strings.Repeat("b", 32) }},
		{"host-key", func(s *evidenceHostSSHStub) {
			s.evidence.FingerprintSHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
		}},
		{"network-interface", func(s *evidenceHostSSHStub) {
			s.answers["server-1"]["ip -j addr show"] = `[{"ifname":"eth0","address":"02:00:00:00:00:02","mtu":1500,"addr_info":[{"local":"192.0.2.10"}]}]`
		}},
		{"free-space-floor", func(s *evidenceHostSSHStub) {
			s.answers["server-1"][freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 200000000000 150000000000 50000000000 75% /\n"
		}},
		{"rke2-state", func(s *evidenceHostSSHStub) {
			s.answers["server-1"]["if test -e /etc/rancher/rke2/config.yaml || test -e /var/lib/rancher/rke2; then printf present; else printf absent; fi"] = "present"
		}},
	} {
		t.Run(edit.name, func(t *testing.T) {
			c, reviewed, stub := freshHostRecheckFixture(t)
			edit.fn(stub)
			if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], reviewed, stub); err == nil {
				t.Fatal("changed target passed the live pre-write recheck")
			}
			if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
				t.Fatalf("recheck made an unsafe SSH call: %+v", stub.hostSSHStub)
			}
		})
	}
}

func TestRecheckFreshHostRejectsWrongSelection(t *testing.T) {
	c, reviewed, stub := freshHostRecheckFixture(t)
	host := c.Spec.Hosts[0]
	host.SSHAlias = "admin@another-machine"
	runs := stub.runs
	if err := RecheckFreshHost(context.Background(), c, host, reviewed, stub); err == nil {
		t.Fatal("unreviewed SSH target reached the live recheck")
	}
	if stub.runs != runs {
		t.Fatalf("wrong selection caused a new SSH read: %d", stub.runs)
	}
}

func TestRecheckFreshHostRejectsDiskIdentityAndUseDrift(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].Disk = "/dev/sdb"
	s.Hosts[0].HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	config := strings.Replace(baseConfig("demo"), "OBJECT_STORAGE_MODE=disabled", "OBJECT_STORAGE_MODE=rook-ceph-local", 1)
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(config+"KUBE_DC_INIT_NODE_NICS=server-1=eth0\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := completeReleaseRecord(s)
	record.Profiles[0].ObjectStorageModes = []string{"rook-ceph-local"}
	writeReleaseTestRecord(t, s, record)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive")
	answers[freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 200000000000 20000000000 180000000000 10% /\n"
	answers["readlink -e -- '/dev/sdb'"] = "/dev/sdb\n"
	const lsblk = "sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL -- '/dev/sdb'"
	const clean = `{"blockdevices":[{"path":"/dev/sdb","type":"disk","size":100000000000,"fstype":null,"pttype":null,"mountpoints":[null],"ro":false,"maj:min":"8:16","wwn":"0x1234","serial":"disk-1"}]}`
	answers[lsblk] = clean
	answers["sudo -n find /sys/dev/block/8:16/holders -mindepth 1 -maxdepth 1 -printf '%f\\n' && printf 'KDC-HOLDERS-END\\n'"] = "KDC-HOLDERS-END\n"
	answers["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "KDC-WIPEFS-END\n"
	stub := &evidenceHostSSHStub{hostSSHStub: &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}},
		evidence: ports.SSHHostKeyEvidence{Address: "server-1:22", Algorithm: "ssh-ed25519", FingerprintSHA256: s.Hosts[0].HostKeySHA256}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.Hosts[0].Facts.SelectedDisk == nil {
		t.Fatalf("disk fixture did not inspect: %+v, %v", report, err)
	}
	if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], report.Hosts[0], stub); err != nil {
		t.Fatalf("unchanged selected disk failed recheck: %v", err)
	}
	answers[lsblk] = strings.Replace(clean, `"wwn":"0x1234"`, `"wwn":"0x5678"`, 1)
	if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], report.Hosts[0], stub); err == nil {
		t.Fatal("disk identity change passed pre-write recheck")
	}
	answers[lsblk] = strings.Replace(clean, `"fstype":null`, `"fstype":"ext4"`, 1)
	if err := RecheckFreshHost(context.Background(), c, c.Spec.Hosts[0], report.Hosts[0], stub); err == nil {
		t.Fatal("filesystem appeared on selected disk without blocking")
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("disk recheck made an unsafe SSH call: %+v", stub.hostSSHStub)
	}
}
