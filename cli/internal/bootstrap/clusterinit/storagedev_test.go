package clusterinit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestRawOSDDevices(t *testing.T) {
	cases := []struct {
		name string
		spec ObjectStorageSpec
		want string // "node:dev,node:dev" or ""
	}{
		{
			name: "local raw device",
			spec: ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "kube-master-1", OSDDevice: "sdb"},
			want: "kube-master-1:sdb",
		},
		{
			name: "local loop-file backing is skipped",
			spec: ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "kube-master-1", OSDDevice: "loop0"},
			want: "",
		},
		{
			name: "local empty device (fleet default loop) is skipped",
			spec: ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "kube-master-1", OSDDevice: ""},
			want: "",
		},
		{
			name: "multi-node sorts by node name",
			spec: ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{
				"n3": "nvme0n1", "n1": "sdb", "n2": "loop0", // n2 is a loop → skipped
			}},
			want: "n1:sdb,n3:nvme0n1",
		},
		{
			name: "pvc mode has no raw device",
			spec: ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast"},
			want: "",
		},
		{
			name: "a /dev/ prefix is tolerated and stripped",
			spec: ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "n1", OSDDevice: "/dev/sdc"},
			want: "n1:sdc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, nd := range tc.spec.RawOSDDevices() {
				got = append(got, nd[0]+":"+nd[1])
			}
			if g := strings.Join(got, ","); g != tc.want {
				t.Errorf("RawOSDDevices = %q, want %q", g, tc.want)
			}
		})
	}
}

// devSSH answers the lsblk probe with a scripted classifier token.
type devSSH struct {
	out string
	err error
}

func (s *devSSH) Run(context.Context, ports.SSHHost, string) ([]byte, error) {
	return []byte(s.out), s.err
}
func (s *devSSH) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) { return nil, nil }
func (s *devSSH) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return nil
}

func TestProbeStorageDevice(t *testing.T) {
	cases := []struct {
		name string
		ssh  *devSSH
		dev  string
		want StorageDeviceState
	}{
		{"empty device is ready", &devSSH{out: "KDCPROBE:EMPTY\n"}, "sdb", StorageDevEmpty},
		{"missing device warns", &devSSH{out: "KDCPROBE:MISSING\n"}, "sdb", StorageDevMissing},
		{"in-use device warns", &devSSH{out: "KDCPROBE:INUSE\n"}, "sdb", StorageDevInUse},
		{"no lsblk / unprivileged is unknown, not a false negative", &devSSH{out: "KDCPROBE:UNKNOWN\n"}, "sdb", StorageDevUnknown},
		{"unreachable node fails open to unknown", &devSSH{err: errors.New("dial timeout")}, "sdb", StorageDevUnknown},
		{"ambiguous output is unknown", &devSSH{out: "\n"}, "sdb", StorageDevUnknown},
		// Shell-rc noise that merely CONTAINS a keyword must not be misread as a
		// verdict — only the exact sentinel line counts (codex LOW).
		{"shell noise containing EMPTY is not a verdict", &devSSH{out: "EMPTY environment variable FOO\n"}, "sdb", StorageDevUnknown},
		{"sentinel wins even amid rc banner lines", &devSSH{out: "Welcome to Ubuntu\nKDCPROBE:INUSE\nLast login: today\n"}, "sdb", StorageDevInUse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ProbeStorageDevice(context.Background(), StorageDeviceProbeOptions{
				SSH: tc.ssh, Host: ports.SSHHost{Alias: "n1"}, Node: "n1", Device: tc.dev,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.State != tc.want {
				t.Errorf("got state=%s, want %s (detail=%q)", res.State, tc.want, res.Detail)
			}
		})
	}
}

func TestProbeStorageDevice_NilSSHErrors(t *testing.T) {
	if _, err := ProbeStorageDevice(context.Background(), StorageDeviceProbeOptions{Device: "sdb"}); err == nil {
		t.Fatal("want error for a nil SSH client")
	}
}
