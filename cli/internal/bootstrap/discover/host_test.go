package discover

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeFS wraps a testing/fstest.MapFS so the host probes can read
// fixture content without touching the operator's real /proc, /sys,
// /etc. Paths are absolute as they would appear on Linux.
type fakeFS struct {
	files map[string][]byte
	dirs  map[string][]string // dir → list of basenames
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string][]string{}}
}

func (f *fakeFS) addFile(path string, body []byte) *fakeFS {
	f.files[path] = body
	// Walk every ancestor so ReadDir finds intermediate directories.
	// e.g. /sys/class/net/enp0/operstate registers "operstate" under
	// /sys/class/net/enp0, "enp0" under /sys/class/net, "net" under
	// /sys/class, "class" under /sys.
	cur := path
	for {
		parent, base := filepath.Split(cur)
		parent = strings.TrimRight(parent, "/")
		if parent == "" || parent == cur {
			break
		}
		f.dirs[parent] = appendUnique(f.dirs[parent], base)
		cur = parent
	}
	return f
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	body, ok := f.files[path]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return body, nil
}

func (f *fakeFS) ReadDir(path string) ([]fs.DirEntry, error) {
	entries, ok := f.dirs[path]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	mfs := fstest.MapFS{}
	for _, base := range entries {
		mfs[base] = &fstest.MapFile{Data: []byte("")}
	}
	return mfs.ReadDir(".")
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// ---------- RKE2Probe ----------

func TestRKE2Probe_Active(t *testing.T) {
	exec := func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		return []byte("active\n"), nil, nil
	}
	p := NewRKE2Probe(HostProbeOn, exec)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
}

func TestRKE2Probe_Inactive(t *testing.T) {
	exec := func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		return []byte("inactive\n"), nil, nil
	}
	p := NewRKE2Probe(HostProbeOn, exec)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("status=%v want missing", r.Status)
	}
	if !strings.Contains(r.Detail, "inactive") {
		t.Errorf("detail should carry sub-state: %q", r.Detail)
	}
}

func TestRKE2Probe_OffMode_NotApplicable(t *testing.T) {
	p := NewRKE2Probe(HostProbeOff, nil)
	r := p.Run(context.Background())
	if !strings.Contains(r.Detail, "not applicable") {
		t.Errorf("Off mode should short-circuit: %q", r.Detail)
	}
}

// ---------- KernelModulesProbe ----------

func TestKernelModulesProbe_AllPresent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux semantics; runs on Linux runners")
	}
	// Per installer/install.sh the canonical required module is
	// nf_conntrack. Other modules (openvswitch, etc.) load
	// automatically with their respective packages.
	fs := newFakeFS().addFile("/proc/modules", []byte(`nf_conntrack 196608 4 ...
unrelated 16384 1 ...
`))
	p := NewKernelModulesProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
}

func TestKernelModulesProbe_MissingRequired(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().addFile("/proc/modules", []byte("unrelated 16384 1 ...\n"))
	p := NewKernelModulesProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want partial+blocker", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "nf_conntrack") {
		t.Errorf("detail should call out missing module: %q", r.Detail)
	}
	if !strings.Contains(r.FixHint.Text, "modprobe") {
		t.Errorf("FixHint should point at modprobe: %q", r.FixHint.Text)
	}
}

// ---------- SysctlProbe ----------

func TestSysctlProbe_AllAtFloor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().
		addFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n")).
		addFile("/proc/sys/fs/inotify/max_user_watches", []byte("1524288\n")).
		addFile("/proc/sys/fs/inotify/max_user_instances", []byte("4024\n"))
	p := NewSysctlProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v detail=%q want installed", r.Status, r.Detail)
	}
}

func TestSysctlProbe_BelowFloor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().
		addFile("/proc/sys/net/ipv4/ip_forward", []byte("0\n")).
		addFile("/proc/sys/fs/inotify/max_user_watches", []byte("524288\n")). // below 1524288 floor
		addFile("/proc/sys/fs/inotify/max_user_instances", []byte("128\n"))   // below 4024 floor
	p := NewSysctlProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial || r.Severity != ports.SeverityBlocker {
		t.Errorf("status=%v severity=%v want partial+blocker", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "ip_forward") || !strings.Contains(r.Detail, "max_user_watches") || !strings.Contains(r.Detail, "max_user_instances") {
		t.Errorf("Detail should call out all three sysctls: %q", r.Detail)
	}
}

// 524288 was the old floor; the documented Kube-DC floor is
// 1524288 (per installer/install.sh + docs/platform/...). Guard
// against a future regression that reverts the value.
func TestSysctlProbe_OldFloor_NowFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().
		addFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n")).
		addFile("/proc/sys/fs/inotify/max_user_watches", []byte("524288\n")).
		addFile("/proc/sys/fs/inotify/max_user_instances", []byte("4024\n"))
	p := NewSysctlProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial {
		t.Errorf("524288 should be below the 1524288 floor: %v", r.Status)
	}
}

func TestSysctlAtLeast(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"524288", "524288", true},
		{"1048576", "524288", true},
		{"8192", "524288", false},
		{"yes", "yes", true},
		{"no", "yes", false},
	}
	for _, c := range cases {
		if got := sysctlAtLeast(c.got, c.want); got != c.ok {
			t.Errorf("sysctlAtLeast(%q,%q)=%v want %v", c.got, c.want, got, c.ok)
		}
	}
}

// ---------- NetplanProbe ----------

func TestNetplanProbe_FilesPresent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().
		addFile("/etc/netplan/00-installer.yaml", []byte("network: {}")).
		addFile("/etc/netplan/50-custom.yaml", []byte("network: {}"))
	p := NewNetplanProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
	if !strings.Contains(r.Detail, "00-installer.yaml") {
		t.Errorf("Detail should list files: %q", r.Detail)
	}
}

func TestNetplanProbe_MissingDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS() // no /etc/netplan registered
	p := NewNetplanProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("status=%v want missing", r.Status)
	}
}

// ---------- IPv6Probe ----------

func TestIPv6Probe_Enabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().addFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("0\n"))
	p := NewIPv6Probe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
}

func TestIPv6Probe_Disabled_Harmless(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().addFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("1\n"))
	p := NewIPv6Probe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing || r.Severity != ports.SeverityInfo {
		t.Errorf("status=%v severity=%v want missing+Info (harmless)", r.Status, r.Severity)
	}
}

// ---------- NICProbe ----------

func TestNICProbe_EnumeratesInterfaces(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().
		addFile("/sys/class/net/enp94s0f0np0/operstate", []byte("up\n")).
		addFile("/sys/class/net/enp94s0f0np0/mtu", []byte("9000\n")).
		addFile("/sys/class/net/eno1/operstate", []byte("down\n")).
		addFile("/sys/class/net/eno1/mtu", []byte("1500\n")).
		addFile("/sys/class/net/lo/operstate", []byte("unknown\n")) // should be skipped
	p := NewNICProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
	if !strings.Contains(r.Detail, "enp94s0f0np0(up)") {
		t.Errorf("Detail missing first NIC: %q", r.Detail)
	}
	if !strings.Contains(r.Detail, "eno1(down)") {
		t.Errorf("Detail missing second NIC: %q", r.Detail)
	}
	if strings.Contains(r.Detail, "lo(") {
		t.Errorf("loopback should be skipped: %q", r.Detail)
	}
}

// ---------- MemoryProbe ----------

func TestMemoryProbe_ParsesMemTotal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	fs := newFakeFS().addFile("/proc/meminfo", []byte(`MemTotal:       16432124 kB
MemFree:         8123456 kB
SwapTotal:             0 kB
`))
	p := NewMemoryProbe(HostProbeOn, fs)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
	if !strings.Contains(r.Detail, "GiB") {
		t.Errorf("Detail should include GiB summary: %q", r.Detail)
	}
}

func TestParseMemTotalKB(t *testing.T) {
	if got := parseMemTotalKB([]byte("MemTotal:       1024 kB\n")); got != 1024 {
		t.Errorf("got %d want 1024", got)
	}
	if got := parseMemTotalKB([]byte("no memtotal here")); got != 0 {
		t.Errorf("got %d want 0", got)
	}
}

// ---------- AllHostProbes ----------

func TestAllHostProbes_ReturnsSeven(t *testing.T) {
	probes := AllHostProbes(HostProbeOn)
	if len(probes) != 7 {
		t.Errorf("got %d probes, want 7", len(probes))
	}
	names := map[string]bool{}
	for _, p := range probes {
		names[p.Name()] = true
	}
	for _, want := range []string{"rke2-server", "kernel-modules", "sysctl", "netplan", "ipv6", "nic-inventory", "memory"} {
		if !names[want] {
			t.Errorf("missing probe %q", want)
		}
	}
}

func TestAllHostProbes_OffMode_AllNotApplicable(t *testing.T) {
	probes := AllHostProbes(HostProbeOff)
	for _, p := range probes {
		r := p.Run(context.Background())
		if !strings.Contains(r.Detail, "not applicable") {
			t.Errorf("%s in Off mode did not short-circuit: %q", p.Name(), r.Detail)
		}
	}
}

// ports.Probe contract: probes MUST return immediately on
// ctx.Done(). Cover every host probe so adding a future probe that
// forgets the entry check is loud.
func TestHostProbes_CancelledContext_ReturnsImmediately(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate

	probes := AllHostProbes(HostProbeOn)
	for _, p := range probes {
		r := p.Run(ctx)
		if !strings.Contains(r.Detail, "cancelled") {
			t.Errorf("%s did not surface cancellation: status=%v detail=%q", p.Name(), r.Status, r.Detail)
		}
	}
}

// Cancellation inside the SysctlProbe loop. Each iteration re-checks
// ctx so a partially-completed run still surfaces the cancellation.
// Build a SysctlProbe with one good file + one slow-to-read file
// (via a fakeFS that cancels ctx on the second read).
func TestSysctlProbe_CancelMidLoop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	ctx, cancel := context.WithCancel(context.Background())
	fs := &cancellingFS{
		inner:  newFakeFS().addFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n")),
		cancel: cancel,
		after:  1, // cancel after the 1st read
	}
	p := NewSysctlProbe(HostProbeOn, fs)
	r := p.Run(ctx)
	if !strings.Contains(r.Detail, "cancelled") {
		t.Errorf("mid-loop cancellation not surfaced: %q", r.Detail)
	}
}

// cancellingFS triggers ctx cancel after N reads. Used to verify
// loop-body ctx checks.
type cancellingFS struct {
	inner  *fakeFS
	cancel context.CancelFunc
	after  int
	reads  int
}

func (c *cancellingFS) ReadFile(p string) ([]byte, error) {
	c.reads++
	if c.reads == c.after {
		c.cancel()
	}
	return c.inner.ReadFile(p)
}

func (c *cancellingFS) ReadDir(p string) ([]fs.DirEntry, error) {
	return c.inner.ReadDir(p)
}
