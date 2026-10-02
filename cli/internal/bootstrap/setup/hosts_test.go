package setup

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type hostSSHStub struct {
	mu           sync.Mutex
	answers      map[string]map[string]string
	badCommand   string
	badError     error
	active       int
	maxActive    int
	runs         int
	uncappedRuns int
	writeCalls   int
	pause        time.Duration
}

type evidenceHostSSHStub struct {
	*hostSSHStub
	evidence          ports.SSHHostKeyEvidence
	differentCommand  string
	differentEvidence ports.SSHHostKeyEvidence
}

func (s *evidenceHostSSHStub) RunCappedWithHostKey(ctx context.Context, host ports.SSHHost, cmd string, maxBytes int) ([]byte, ports.SSHHostKeyEvidence, error) {
	out, err := s.hostSSHStub.RunCapped(ctx, host, cmd, maxBytes)
	if cmd == s.differentCommand {
		return out, s.differentEvidence, err
	}
	return out, s.evidence, err
}

func hostCheckStatus(host HostObservation, id string) string {
	for _, check := range host.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func TestInspectHostsChecksPinnedTargetFingerprint(t *testing.T) {
	s, _ := fixture(t)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	s.Hosts[0].HostKeySHA256 = fingerprint
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub := &evidenceHostSSHStub{
		hostSSHStub: &hostSSHStub{answers: map[string]map[string]string{"server-1": hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive")}},
		evidence:    ports.SSHHostKeyEvidence{Address: "server-1:22", Algorithm: "ssh-ed25519", FingerprintSHA256: fingerprint},
	}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.Hosts[0].Facts.HostKey == nil || report.Hosts[0].Facts.HostKey.FingerprintSHA256 != fingerprint || hostCheckStatus(report.Hosts[0], "host-key") != "pass" {
		t.Fatalf("matching key not recorded: %+v", report.Hosts[0])
	}
	for _, unresolved := range report.Unresolved {
		if unresolved == "operator-confirmed-host-key-fingerprints" {
			t.Fatalf("pinned fingerprint remained unresolved: %+v", report.Unresolved)
		}
	}
	stub.evidence.FingerprintSHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || hostCheckStatus(report.Hosts[0], "host-key") != "blocked" {
		t.Fatalf("mismatched target key accepted: %+v", report)
	}
	stub.evidence.FingerprintSHA256 = fingerprint
	stub.differentCommand = "cat /etc/os-release"
	stub.differentEvidence = ports.SSHHostKeyEvidence{Address: "server-1:22", Algorithm: "ssh-ed25519", FingerprintSHA256: "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))}
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || report.Hosts[0].Facts.OSID != "" {
		t.Fatalf("facts from a second target entered the report: %+v", report.Hosts[0])
	}
	pending := false
	for _, unresolved := range report.Unresolved {
		pending = pending || unresolved == "operator-confirmed-host-key-fingerprints"
	}
	if !pending {
		t.Fatalf("changed target key cleared the review gate: %+v", report.Unresolved)
	}
}

func TestCompileRejectsMalformedPinnedFingerprint(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].HostKeySHA256 = "SHA256:bad"
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "hostKeySHA256") {
		t.Fatalf("invalid host-key pin accepted: %v", err)
	}
}

func (s *hostSSHStub) Run(context.Context, ports.SSHHost, string) ([]byte, error) {
	s.mu.Lock()
	s.uncappedRuns++
	s.mu.Unlock()
	return nil, errors.New("uncapped SSH read is forbidden")
}

func (s *hostSSHStub) RunCapped(ctx context.Context, host ports.SSHHost, cmd string, maxBytes int) ([]byte, error) {
	s.mu.Lock()
	s.runs++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.pause != 0 {
		select {
		case <-time.After(s.pause):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cmd == s.badCommand {
		if s.badError != nil {
			return nil, s.badError
		}
		return nil, errors.New("read unavailable")
	}
	name := host.Alias
	if host.Hostname != "" {
		name = host.Hostname
	}
	s.mu.Lock()
	answer, ok := s.answers[name][cmd]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unexpected read %q on %s", cmd, name)
	}
	if len(answer) > maxBytes {
		return []byte(answer[:maxBytes]), errors.New("output limit exceeded")
	}
	return []byte(answer), nil
}

func (s *hostSSHStub) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) {
	s.mu.Lock()
	s.writeCalls++
	s.mu.Unlock()
	return nil, errors.New("unexpected fetch")
}

func (s *hostSSHStub) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	s.mu.Lock()
	s.writeCalls++
	s.mu.Unlock()
	return errors.New("unexpected write")
}

func hostAnswers(machineID, address, serverState string) map[string]string {
	serverLoad := "not-found"
	if strings.TrimSpace(serverState) != "inactive" {
		serverLoad = "loaded"
	}
	return map[string]string{
		"cat /etc/machine-id":                         machineID,
		"cat /etc/os-release":                         "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
		"uname -m":                                    "x86_64\n",
		"getconf _NPROCESSORS_ONLN":                   "8\n",
		"awk '/^MemTotal:/ {print $2}' /proc/meminfo": "32768000\n",
		"if sudo -n true >/dev/null 2>&1; then printf yes; else printf no; fi": "yes",
		freeSpaceCommand:      "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 100000000000 20000000000 80000000000 20% /\n",
		clockCommand:          "yes\n",
		kvmDeviceCommand:      "present",
		listeningPortsCommand: "KDC-SS-END\n",
		"ip -j addr show":     `[{"ifname":"eth0","address":"02:00:00:00:00:01","mtu":1500,"addr_info":[{"local":"` + address + `"}]}]`,
		"systemctl show -p LoadState -p ActiveState rke2-server":                                                                 "LoadState=" + serverLoad + "\nActiveState=" + strings.TrimSpace(serverState) + "\n",
		"systemctl show -p LoadState -p ActiveState rke2-agent":                                                                  "LoadState=not-found\nActiveState=inactive\n",
		"if test -e /etc/rancher/rke2/config.yaml || test -e /var/lib/rancher/rke2; then printf present; else printf absent; fi": "absent",
	}
}

func TestInspectHostsReadsRemoteFactsWithoutWrites(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].NIC = "eth0"
	s.Hosts = append(s.Hosts, Host{ID: "a-worker", SSHAlias: "admin@worker", Role: "agent", ManagementAddress: "192.0.2.11"})
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub := &hostSSHStub{answers: map[string]map[string]string{
		"server-1": hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n"),
		"worker":   hostAnswers(strings.Repeat("b", 32), "192.0.2.11", "inactive\n"),
	}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "observed" || report.ReadyToInstall || len(report.Hosts) != 2 || report.Hosts[0].ID != "a-worker" || report.Hosts[1].ID != "server-1" {
		t.Fatalf("unexpected host report: %+v", report)
	}
	if report.Hosts[1].ObservedAt.IsZero() || report.Hosts[1].Checks[0].ObservedAt.IsZero() || report.Hosts[1].Facts.OSVersion != "24.04" || report.Hosts[1].Facts.CPUs != 8 || report.Hosts[1].Facts.MemoryKiB != 32768000 || report.Hosts[1].Facts.FreeBytes != 80000000000 || report.Hosts[1].Facts.ClockSynced != "yes" {
		t.Fatalf("missing remote facts: %+v", report.Hosts[1].Facts)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 || stub.runs != 28 || stub.maxActive > maxHostProbeParallelism {
		t.Fatalf("inventory used an unsafe SSH operation or exceeded concurrency: %+v", stub)
	}
}

func TestInspectHostsBlocksMissingPasswordlessSudo(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["if sudo -n true >/dev/null 2>&1; then printf yes; else printf no; fi"] = "no"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.PasswordlessSudo != "no" {
		t.Fatalf("missing installer privilege was accepted: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsBlocksClockAndFreshInstallPortConflicts(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers[clockCommand] = "no\n"
	answers[listeningPortsCommand] = "LISTEN 0 4096 [::]:6443 [::]:*\nLISTEN 0 4096 127.0.0.1:2379 0.0.0.0:*\nKDC-SS-END\n"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.ClockSynced != "no" {
		t.Fatalf("clock or occupied RKE2 ports were accepted: report=%+v err=%v", report, err)
	}
	var clock, ports bool
	for _, check := range report.Hosts[0].Checks {
		if (check.Status == "blocked" || check.Status == "unknown") && (check.NextAction == "" || check.Resource == "" || check.Scope == "" || check.ObservedAt.IsZero()) {
			t.Fatalf("actionable check lacks required context: %+v", check)
		}
		clock = clock || check.ID == "clock" && check.Status == "blocked"
		ports = ports || check.ID == "tcp-ports" && check.Status == "blocked" && strings.Contains(check.Detail, "6443") && strings.Contains(check.Detail, "2379")
	}
	if !clock || !ports {
		t.Fatalf("missing host blockers: %+v", report.Hosts[0].Checks)
	}
}

func TestInspectHostsRequiresKVMCapacityWhenInstallerGateApplies(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers[kvmDeviceCommand] = "absent"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || len(report.Checks) != 1 || report.Checks[0].ID != "kvm-capacity" || report.Checks[0].Status != "blocked" {
		t.Fatalf("missing cluster VM capacity was accepted: report=%+v err=%v", report, err)
	}
	s.Hosts = append(s.Hosts, Host{ID: "worker-1", SSHAlias: "worker", Role: "agent", ManagementAddress: "192.0.2.11"})
	c, err = Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub.answers["worker"] = hostAnswers(strings.Repeat("b", 32), "192.0.2.11", "inactive\n")
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.Checks[0].Status != "pass" {
		t.Fatalf("one KVM-capable host was not sufficient: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsSkipsKVMCapacityWhenVMsAreOutOfScope(t *testing.T) {
	s, _ := fixture(t)
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(baseConfig("demo")+"KUBE_DC_INIT_NO_KUBEVIRT=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers[kvmDeviceCommand] = "absent"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || len(report.Checks) != 0 {
		t.Fatalf("out-of-scope VM capacity blocked inventory: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsDoesNotTreatAdoptListenersAsInstallConflicts(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeAdopt
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "active\n")
	answers[listeningPortsCommand] = "LISTEN 0 4096 *:6443 *:*\nKDC-SS-END\n"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" {
		t.Fatalf("adopt intent was treated as fresh install: report=%+v err=%v", report, err)
	}
	var found bool
	for _, check := range report.Hosts[0].Checks {
		found = found || check.ID == "tcp-ports" && check.Status == "not-applicable"
	}
	if !found {
		t.Fatalf("adopt port check missing: %+v", report.Hosts[0].Checks)
	}
}

func TestInspectHostsBlocksMalformedListenerAndSpaceReports(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers[freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 100 50 invalid 50% /\n"
	answers[listeningPortsCommand] = "unexpected output\nKDC-SS-END\n"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.FreeBytes != 0 {
		t.Fatalf("invalid host reports were accepted: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsReportsSelectedDiskWithoutPreparingIt(t *testing.T) {
	const lsblkSDB = "sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL -- '/dev/sdb'"
	const holdersSDB = "sudo -n find /sys/dev/block/8:16/holders -mindepth 1 -maxdepth 1 -printf '%f\\n' && printf 'KDC-HOLDERS-END\\n'"
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].Disk = "/dev/sdb"
	config := strings.Replace(baseConfig("demo"), "OBJECT_STORAGE_MODE=disabled", "OBJECT_STORAGE_MODE=rook-ceph-local", 1)
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(config+"KUBE_DC_INIT_NODE_NICS=server-1=eth0\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["readlink -e -- '/dev/sdb'"] = "/dev/sdb\n"
	const cleanLSBLK = `{"blockdevices":[{"path":"/dev/sdb","type":"disk","size":100000000000,"fstype":null,"pttype":null,"mountpoints":[null],"ro":false,"maj:min":"8:16","wwn":"0x1234","serial":"disk-1"}]}`
	answers[lsblkSDB] = cleanLSBLK
	answers[holdersSDB] = "KDC-HOLDERS-END\n"
	answers["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "KDC-WIPEFS-END\n"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.ReadyToInstall {
		t.Fatalf("clean disk observation failed or authorized install: report=%+v err=%v", report, err)
	}
	disk := report.Hosts[0].Facts.SelectedDisk
	if disk == nil || disk.ResolvedPath != "/dev/sdb" || disk.SizeBytes != 100000000000 || disk.StableID != "wwn:0x1234" || disk.DeviceNumber != "8:16" || disk.HolderCount != 0 || disk.HasMounts || disk.SignatureCount != 0 || stub.writeCalls != 0 {
		t.Fatalf("unsafe or incomplete disk observation: disk=%+v writes=%d", disk, stub.writeCalls)
	}
	var diskScoped bool
	for _, check := range report.Hosts[0].Checks {
		diskScoped = diskScoped || check.ID == "selected-disk" && check.Scope == "disk" && check.Resource == "/dev/sdb"
	}
	if !diskScoped {
		t.Fatalf("selected disk check has no device scope: %+v", report.Hosts[0].Checks)
	}
	answers[lsblkSDB] = strings.Replace(cleanLSBLK, `"serial":"disk-1"`, `"serial":"disk serial 1"`, 1)
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.Hosts[0].Facts.SelectedDisk.Serial != "disk serial 1" {
		t.Fatalf("printable disk serial was rejected: report=%+v err=%v", report, err)
	}
	answers[lsblkSDB] = strings.Replace(cleanLSBLK, `"serial":"disk-1"`, `"serial":"disk\n1"`, 1)
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.Hosts[0].Facts.SelectedDisk.Serial != "" {
		t.Fatalf("unsafe disk serial was not omitted: report=%+v err=%v", report, err)
	}
	answers[lsblkSDB] = strings.Replace(cleanLSBLK, `"wwn":"0x1234"`, `"wwn":null`, 1)
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.Hosts[0].Facts.SelectedDisk.StableID != "" || hostCheckStatus(report.Hosts[0], "selected-disk-identity") != "unknown" {
		t.Fatalf("missing stable disk ID was not reported: report=%+v err=%v", report, err)
	}
	answers[lsblkSDB] = cleanLSBLK
	answers[holdersSDB] = "dm-0\nKDC-HOLDERS-END\n"
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.SelectedDisk.HolderCount != 1 {
		t.Fatalf("disk holder was accepted: report=%+v err=%v", report, err)
	}
	answers[holdersSDB] = "unsafe holder\nKDC-HOLDERS-END\n"
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" {
		t.Fatalf("invalid holder output was accepted: report=%+v err=%v", report, err)
	}
	answers[holdersSDB] = "KDC-HOLDERS-END\n"
	answers["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "ext4\nKDC-WIPEFS-END\n"
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.SelectedDisk.SignatureCount != 1 {
		t.Fatalf("disk signature was accepted: report=%+v err=%v", report, err)
	}
	s.Target.Intent = clusterinit.ModeAdopt
	adopt, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err = InspectHosts(context.Background(), adopt, stub)
	if err != nil || report.State != "observed" {
		t.Fatalf("existing storage was treated as a fresh disk: report=%+v err=%v", report, err)
	}
	var adoptDiskCheck bool
	for _, check := range report.Hosts[0].Checks {
		adoptDiskCheck = adoptDiskCheck || check.ID == "selected-disk" && check.Status == "not-applicable"
	}
	if !adoptDiskCheck {
		t.Fatalf("adopt intent has no disk ownership result: %+v", report.Hosts[0].Checks)
	}
	s.Target.Intent = clusterinit.ModeInstall
	answers["sudo -n wipefs -n --noheadings -O TYPE -- '/dev/sdb' && printf 'KDC-WIPEFS-END\\n'"] = "KDC-WIPEFS-END\n"
	answers[lsblkSDB] = `{"blockdevices":[{"path":"/dev/sdb","type":"disk","size":100000000000,"fstype":null,"pttype":"gpt","mountpoints":[null],"ro":false,"maj:min":"8:16","wwn":"0x1234","serial":"disk-1","children":[{"path":"/dev/sdb1","type":"part"}]}]}`
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || !report.Hosts[0].Facts.SelectedDisk.HasChildren || !report.Hosts[0].Facts.SelectedDisk.HasPartitionTable {
		t.Fatalf("partitioned disk was accepted: report=%+v err=%v", report, err)
	}
}

func TestSelectedDiskPathRejectsShellAndTraversalInput(t *testing.T) {
	for _, path := range []string{"/dev/sdb'; id", "/dev/sdb/../sdc", "/dev//sdb", "/tmp/sdb"} {
		if validSelectedDevicePath(path) {
			t.Fatalf("unsafe device path accepted: %q", path)
		}
	}
	if !validSelectedDevicePath("/dev/disk/by-id/wwn-1234") {
		t.Fatal("stable device path rejected")
	}
}

func TestInspectHostsSkipsRawDiskProbeForFleetLoopStorage(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeAdopt
	s.Hosts[0].Disk = "/dev/loop0"
	config := strings.Replace(baseConfig("demo"), "OBJECT_STORAGE_MODE=disabled", "OBJECT_STORAGE_MODE=rook-ceph-local", 1)
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(config+"KUBE_DC_INIT_NODE_NICS=server-1=eth0\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub := &hostSSHStub{answers: map[string]map[string]string{
		"server-1": hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n"),
	}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || report.Hosts[0].Facts.SelectedDisk == nil || stub.runs != 14 {
		t.Fatalf("Fleet loop storage was probed as a raw block device: report=%+v runs=%d err=%v", report, stub.runs, err)
	}
	var skipped bool
	for _, check := range report.Hosts[0].Checks {
		skipped = skipped || check.ID == "selected-disk" && check.Status == "not-applicable"
	}
	if !skipped {
		t.Fatalf("loop storage has no mode-aware check: %+v", report.Hosts[0].Checks)
	}
}

func TestInspectHostsBlocksDuplicateMachineAndActiveRKE2(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts = append(s.Hosts, Host{ID: "worker-1", SSHAlias: "worker", Role: "agent", ManagementAddress: "192.0.2.11"})
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	machineID := strings.Repeat("a", 32)
	stub := &hostSSHStub{answers: map[string]map[string]string{
		"server-1": hostAnswers(machineID, "192.0.2.10", "active\n"),
		"worker":   hostAnswers(machineID, "192.0.2.11", "inactive\n"),
	}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || report.ReadyToInstall {
		t.Fatalf("existing RKE2 or duplicate host was accepted: %+v", report)
	}
	var active, duplicate bool
	for _, host := range report.Hosts {
		for _, check := range host.Checks {
			active = active || (check.ID == "rke2-server" && check.Status == "blocked")
			duplicate = duplicate || (check.ID == "machine-identity" && strings.Contains(check.Detail, "same machine ID"))
		}
	}
	if !active || !duplicate {
		t.Fatalf("missing blockers: %+v", report.Hosts)
	}
}

func TestInspectHostsBlocksStoppedRKE2AndResidualFiles(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["systemctl show -p LoadState -p ActiveState rke2-server"] = "LoadState=loaded\nActiveState=inactive\n"
	answers["if test -e /etc/rancher/rke2/config.yaml || test -e /var/lib/rancher/rke2; then printf present; else printf absent; fi"] = "present"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.ReadyToInstall {
		t.Fatalf("stopped RKE2 installation was accepted: report=%+v err=%v", report, err)
	}
	var unitBlocked, filesBlocked bool
	for _, check := range report.Hosts[0].Checks {
		unitBlocked = unitBlocked || (check.ID == "rke2-server" && check.Status == "blocked")
		filesBlocked = filesBlocked || (check.ID == "rke2-files" && check.Status == "blocked")
	}
	if !unitBlocked || !filesBlocked {
		t.Fatalf("missing stopped-install blockers: %+v", report.Hosts[0].Checks)
	}
}

func TestInspectHostsBlocksMissingFactsAndNeverInventsReadiness(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.99", "inactive\n")
	answers["getconf _NPROCESSORS_ONLN"] = ""
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || report.Hosts[0].State != "blocked" || report.ReadyToInstall {
		t.Fatalf("missing CPU or management address accepted: %+v", report)
	}
	stub.badCommand = "cat /etc/machine-id"
	report, err = InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.MachineID != "" {
		t.Fatalf("failed machine ID read accepted: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsAcceptsSeparateManagementAndSelectedNICs(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].NIC = "eth0"
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["ip -j addr show"] = `[{"ifname":"eth0","address":"02:00:00:00:00:01","addr_info":[]},{"ifname":"eth1","address":"02:00:00:00:00:02","addr_info":[{"local":"192.0.2.10"}]}]`
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" {
		t.Fatalf("management and external NICs on separate interfaces were rejected: report=%+v err=%v", report, err)
	}
}

func TestInspectHostsRejectsUnsafeRemoteFactsAndSanitizesErrors(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive\n")
	answers["cat /etc/os-release"] = "ID=ubuntu\x1b[31m\nVERSION_ID=24.04\n"
	stub := &hostSSHStub{answers: map[string]map[string]string{"server-1": answers}, badCommand: "uname -m", badError: errors.New("bad\x1b]52;c;payload\a host")}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "blocked" || report.Hosts[0].Facts.OSID != "" {
		t.Fatalf("unsafe remote OS fact was accepted: report=%+v err=%v", report, err)
	}
	for _, check := range report.Hosts[0].Checks {
		if strings.ContainsRune(check.Detail, '\x1b') || strings.ContainsRune(check.Detail, '\a') {
			t.Fatalf("control sequence leaked into host check: %+v", check)
		}
	}
}

func TestInspectHostsHonorsCancellationAndParallelism(t *testing.T) {
	s, _ := fixture(t)
	for i := 0; i < 8; i++ {
		s.Hosts = append(s.Hosts, Host{ID: fmt.Sprintf("worker-%d", i), SSHAlias: fmt.Sprintf("worker-%d", i), Role: "agent", ManagementAddress: fmt.Sprintf("192.0.2.%d", i+20)})
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub := &hostSSHStub{answers: map[string]map[string]string{}, pause: time.Millisecond}
	for i, host := range s.Hosts {
		endpoint := sshHost(host.SSHAlias)
		name := endpoint.Alias
		if endpoint.Hostname != "" {
			name = endpoint.Hostname
		}
		stub.answers[name] = hostAnswers(fmt.Sprintf("%032x", i+1), host.ManagementAddress, "inactive\n")
	}
	report, err := InspectHosts(context.Background(), c, stub)
	if err != nil || report.State != "observed" || stub.maxActive > maxHostProbeParallelism || stub.maxActive < 2 {
		t.Fatalf("parallel inventory failed: report=%+v max=%d err=%v", report, stub.maxActive, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runsBefore := stub.runs
	report, err = InspectHosts(ctx, c, stub)
	if err != nil || report.State != "blocked" || report.ReadyToInstall || stub.runs != runsBefore {
		t.Fatalf("canceled inventory claimed readiness: report=%+v err=%v", report, err)
	}
}

func TestHostAliasAndTunnelAddressParsing(t *testing.T) {
	host := sshHost("admin@control-plane")
	if host.User != "admin" || host.Alias != "control-plane" || host.Hostname != "" {
		t.Fatalf("user-qualified SSH alias bypasses ssh_config: %+v", host)
	}
	tunnel := sshHost("ubuntu@127.0.0.1:2226")
	if tunnel.User != "ubuntu" || tunnel.Alias != "127.0.0.1" || tunnel.Port != 2226 {
		t.Fatalf("nondefault SSH port was not parsed: %+v", tunnel)
	}
	nics, err := parseNICFacts(`[{"ifname":"gre0","address":"0.0.0.0","addr_info":[]},{"ifname":"eth0","address":"02:00:00:00:00:01","addr_info":[{"local":"192.0.2.10"}]}]`)
	if err != nil || len(nics) != 2 || nics[0].MAC != "02:00:00:00:00:01" || nics[1].MAC != "" {
		t.Fatalf("valid tunnel address blocked or reported as a MAC: nics=%+v err=%v", nics, err)
	}
}
