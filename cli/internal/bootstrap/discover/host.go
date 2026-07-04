package discover

// M1-T02 host probes. Read-only checks against the host the CLI is
// running on (or skip gracefully when the CLI is in cluster-only
// mode). Each probe implements ports.Probe.
//
// **Scope gate**: host probes run only when the operator explicitly
// opts in via the `--host-probes` flag OR when `init` is in install-
// mode (operator's laptop IS the target node). In all other modes
// they return StatusMissing + SeverityInfo with Detail "not
// applicable (cluster-only mode)" — the printer shows them grey'd
// out per installer-ux §4.
//
// **Linux-only**: probes read /proc, /sys, /etc/netplan. On
// macOS/Windows we short-circuit each probe to "not applicable on
// this host" rather than emitting a confusing missing-file error.
//
// Each probe takes its own injected backend (an `hostFS` for
// filesystem reads, an `execHook` for systemctl) so unit tests stay
// hermetic without touching the operator's actual /sys or systemd.

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// hostFS abstracts filesystem reads so tests can drop in a fixture
// directory without touching the operator's /sys, /proc, /etc.
type hostFS interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]fs.DirEntry, error)
}

type realHostFS struct{}

func (realHostFS) ReadFile(p string) ([]byte, error)      { return os.ReadFile(p) }
func (realHostFS) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }

// HostProbeMode tells host probes whether they should run or
// short-circuit. The discover layer doesn't decide mode itself —
// the cobra wiring at M1-T06 passes the operator's flag set in.
type HostProbeMode int

const (
	// HostProbeOff: short-circuit every probe to "not applicable".
	// Used when running `doctor` against a remote cluster from a
	// laptop that isn't the target node.
	HostProbeOff HostProbeMode = iota

	// HostProbeOn: run all probes against the local host. Used when
	// `--host-probes` is passed or `init` is in install-mode.
	HostProbeOn
)

// requiredKernelModules + requiredSysctls are the canonical Kube-OVN
// prereqs from `installer/install.sh` (the production install script
// in this monorepo). The bare-metal-workers doc (docs/platform/
// deploy-metal3-bare-metal-workers.md) calls out the same set:
//
//   fs.inotify.max_user_watches  = 1524288
//   fs.inotify.max_user_instances = 4024
//   net.ipv4.ip_forward          = 1
//   modprobe nf_conntrack
//
// openvswitch is loaded automatically when openvswitch-switch is
// installed (it's also the kube-ovn-controller's daemon path), so
// the host probe doesn't enforce it — operators who install
// Kube-OVN via the fleet chart get it for free.
var requiredKernelModules = []string{"nf_conntrack"}

var requiredSysctls = map[string]string{
	"net.ipv4.ip_forward":           "1",
	"fs.inotify.max_user_watches":   "1524288",
	"fs.inotify.max_user_instances": "4024",
}

// ---------- RKE2Probe ----------

// RKE2Probe checks `systemctl is-active rke2-server`. SeverityInfo
// in doctor mode (might be deliberately stopped), SeverityBlocker in
// init --post-rke2 mode (caller flags that via its own severity
// override; this probe always emits Info).
type RKE2Probe struct {
	mode HostProbeMode
	exec execHook
}

func NewRKE2Probe(mode HostProbeMode, e execHook) *RKE2Probe {
	if e == nil {
		e = realExec
	}
	return &RKE2Probe{mode: mode, exec: e}
}

var _ ports.Probe = (*RKE2Probe)(nil)

func (p *RKE2Probe) Name() string { return "rke2-server" }

func (p *RKE2Probe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("rke2-server")
	}
	stdout, _, _ := p.exec(ctx, "systemctl", "is-active", "rke2-server")
	state := strings.TrimSpace(string(stdout))
	if state == "active" {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "rke2-server unit is active",
		}
	}
	return ports.Result{
		Status:   ports.StatusMissing,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("rke2-server unit %s", stateOrUnknown(state)),
		FixHint:  ports.FixHint{Text: "Start with `sudo systemctl start rke2-server` if the node should be running RKE2"},
	}
}

// ---------- KernelModulesProbe ----------

// KernelModulesProbe reads /proc/modules and asserts the canonical
// RKE2 + Kube-OVN modules are loaded. Missing modules emit a Partial
// result listing which ones are absent.
type KernelModulesProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewKernelModulesProbe(mode HostProbeMode, h hostFS) *KernelModulesProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &KernelModulesProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*KernelModulesProbe)(nil)

func (p *KernelModulesProbe) Name() string { return "kernel-modules" }

func (p *KernelModulesProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("kernel-modules")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("kernel-modules (non-Linux host)")
	}
	body, err := p.fs.ReadFile("/proc/modules")
	if err != nil {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("read /proc/modules: %v", err),
		}
	}
	loaded := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			loaded[fields[0]] = true
		}
	}
	var missing []string
	for _, m := range requiredKernelModules {
		if !loaded[m] {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   fmt.Sprintf("all required modules loaded (%s)", strings.Join(requiredKernelModules, ", ")),
		}
	}
	return ports.Result{
		Status:   ports.StatusPartial,
		Severity: ports.SeverityBlocker,
		Detail:   fmt.Sprintf("missing kernel modules: %s", strings.Join(missing, ", ")),
		FixHint: ports.FixHint{
			Text: fmt.Sprintf("Run `sudo modprobe %s` (and add to /etc/modules-load.d/ for persistence)", strings.Join(missing, " ")),
		},
	}
}

// ---------- SysctlProbe ----------

// SysctlProbe verifies the canonical sysctl values are set to the
// expected RKE2/Kube-OVN floor.
type SysctlProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewSysctlProbe(mode HostProbeMode, h hostFS) *SysctlProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &SysctlProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*SysctlProbe)(nil)

func (p *SysctlProbe) Name() string { return "sysctl" }

func (p *SysctlProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("sysctl")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("sysctl (non-Linux host)")
	}

	// Iterate map in a deterministic order so the Detail output is
	// stable across runs (Go map iteration is randomized).
	keys := make([]string, 0, len(requiredSysctls))
	for k := range requiredSysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var bad []string
	for _, key := range keys {
		if err := ctxCanceled(ctx); err != nil {
			return *err
		}
		wantVal := requiredSysctls[key]
		path := "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
		body, err := p.fs.ReadFile(path)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: read failed (%v)", key, err))
			continue
		}
		got := strings.TrimSpace(string(body))
		if !sysctlAtLeast(got, wantVal) {
			bad = append(bad, fmt.Sprintf("%s=%s want ≥%s", key, got, wantVal))
		}
	}
	sort.Strings(bad)
	if len(bad) == 0 {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "all required sysctl values at or above floor",
		}
	}
	return ports.Result{
		Status:   ports.StatusPartial,
		Severity: ports.SeverityBlocker,
		Detail:   strings.Join(bad, "; "),
		FixHint:  ports.FixHint{Text: "Set via sysctl + persist in /etc/sysctl.d/ (see scripts/install-prerequisites.sh)"},
	}
}

// sysctlAtLeast compares sysctl integer values where applicable.
// Falls back to exact match for non-integer values.
func sysctlAtLeast(got, want string) bool {
	gi, gErr := strconv.Atoi(got)
	wi, wErr := strconv.Atoi(want)
	if gErr == nil && wErr == nil {
		return gi >= wi
	}
	return got == want
}

// ---------- NetplanProbe ----------

// NetplanProbe enumerates /etc/netplan/*.yaml. Pure inventory — no
// validation of the contents. Used by M4 to know which files are
// candidates for the customInterfaces patch.
type NetplanProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewNetplanProbe(mode HostProbeMode, h hostFS) *NetplanProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &NetplanProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*NetplanProbe)(nil)

func (p *NetplanProbe) Name() string { return "netplan" }

func (p *NetplanProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("netplan")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("netplan (non-Linux host)")
	}
	entries, err := p.fs.ReadDir("/etc/netplan")
	if err != nil {
		if os.IsNotExist(err) {
			return ports.Result{
				Status:   ports.StatusMissing,
				Severity: ports.SeverityInfo,
				Detail:   "/etc/netplan absent (host may use ifupdown / NetworkManager)",
			}
		}
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("read /etc/netplan: %v", err),
		}
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "/etc/netplan present but empty",
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("netplan files: %s", strings.Join(files, ", ")),
	}
}

// ---------- IPv6Probe ----------

// IPv6Probe surfaces whether IPv6 is enabled on the host. The
// Kube-DC stack itself is IPv4-only in v1 (see installer-prd Non-
// Goals), so a host with IPv6 disabled is fine; the probe is
// purely informational.
type IPv6Probe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewIPv6Probe(mode HostProbeMode, h hostFS) *IPv6Probe {
	if h == nil {
		h = realHostFS{}
	}
	return &IPv6Probe{mode: mode, fs: h}
}

var _ ports.Probe = (*IPv6Probe)(nil)

func (p *IPv6Probe) Name() string { return "ipv6" }

func (p *IPv6Probe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("ipv6")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("ipv6 (non-Linux host)")
	}
	body, err := p.fs.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	if err != nil {
		// /proc/sys/net/ipv6 absent → kernel built without IPv6
		// support (uncommon on modern distros). Informational.
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "IPv6 not supported by kernel (v1 stack is IPv4-only — harmless)",
		}
	}
	disabled := strings.TrimSpace(string(body)) == "1"
	if disabled {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "IPv6 disabled (v1 stack is IPv4-only — harmless)",
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   "IPv6 enabled (informational — v1 stack uses IPv4)",
	}
}

// ---------- NICProbe ----------

// NICProbe enumerates network interfaces by reading /sys/class/net.
// Output feeds M4-T11's customInterfaces patch validation — the
// operator-declared --node-nic name must appear in this inventory.
type NICProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewNICProbe(mode HostProbeMode, h hostFS) *NICProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &NICProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*NICProbe)(nil)

func (p *NICProbe) Name() string { return "nic-inventory" }

func (p *NICProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("nic-inventory")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("nic-inventory (non-Linux host)")
	}
	entries, err := p.fs.ReadDir("/sys/class/net")
	if err != nil {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("read /sys/class/net: %v", err),
		}
	}
	nics := make([]ports.NIC, 0, len(entries))
	for _, e := range entries {
		if err := ctxCanceled(ctx); err != nil {
			return *err
		}
		name := e.Name()
		if name == "lo" {
			continue // loopback is uninteresting
		}
		nic := ports.NIC{Name: name}
		// state
		if op, err := p.fs.ReadFile(filepath.Join("/sys/class/net", name, "operstate")); err == nil {
			nic.State = strings.TrimSpace(string(op))
		} else {
			nic.State = "unknown"
		}
		// mtu
		if mtuBytes, err := p.fs.ReadFile(filepath.Join("/sys/class/net", name, "mtu")); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(mtuBytes))); err == nil {
				nic.MTU = v
			}
		}
		nics = append(nics, nic)
	}
	sort.Slice(nics, func(i, j int) bool { return nics[i].Name < nics[j].Name })
	if len(nics) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "no NICs found in /sys/class/net (excluding lo)",
		}
	}
	names := make([]string, 0, len(nics))
	for _, n := range nics {
		names = append(names, fmt.Sprintf("%s(%s)", n.Name, n.State))
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("NICs: %s", strings.Join(names, ", ")),
	}
}

// ---------- MemoryProbe ----------

// MemoryProbe reads /proc/meminfo and reports MemTotal. Currently
// emits Info only — M4 may add a floor (≥8 GiB recommended for the
// RKE2 + Kube-DC stack) but v1 is purely descriptive.
type MemoryProbe struct {
	mode HostProbeMode
	fs   hostFS
}

func NewMemoryProbe(mode HostProbeMode, h hostFS) *MemoryProbe {
	if h == nil {
		h = realHostFS{}
	}
	return &MemoryProbe{mode: mode, fs: h}
}

var _ ports.Probe = (*MemoryProbe)(nil)

func (p *MemoryProbe) Name() string { return "memory" }

func (p *MemoryProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.mode == HostProbeOff {
		return notApplicable("memory")
	}
	if runtime.GOOS != "linux" {
		return notApplicable("memory (non-Linux host)")
	}
	body, err := p.fs.ReadFile("/proc/meminfo")
	if err != nil {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("read /proc/meminfo: %v", err),
		}
	}
	kb := parseMemTotalKB(body)
	if kb == 0 {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   "MemTotal not found in /proc/meminfo",
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("total memory: %.1f GiB", float64(kb)/1024/1024),
	}
}

func parseMemTotalKB(body []byte) int64 {
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			n, _ := strconv.ParseInt(fields[1], 10, 64)
			return n
		}
	}
	return 0
}

// ---------- AllHostProbes ----------

// AllHostProbes returns every host probe wired against the real
// /proc /sys /etc. Caller passes the operator's HostProbeMode (off
// for cluster-only doctor runs, on for --host-probes + install
// mode). Tests build their own slice with fixture filesystems.
func AllHostProbes(mode HostProbeMode) []ports.Probe {
	return []ports.Probe{
		NewRKE2Probe(mode, realExec),
		NewKernelModulesProbe(mode, realHostFS{}),
		NewSysctlProbe(mode, realHostFS{}),
		NewNetplanProbe(mode, realHostFS{}),
		NewIPv6Probe(mode, realHostFS{}),
		NewNICProbe(mode, realHostFS{}),
		NewMemoryProbe(mode, realHostFS{}),
	}
}

// ---------- helpers ----------

// notApplicable is the canonical short-circuit Result for probes
// that intentionally skip on a non-target host.
func notApplicable(name string) ports.Result {
	return ports.Result{
		Status:   ports.StatusMissing,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("%s: not applicable (cluster-only mode)", name),
	}
}

func stateOrUnknown(s string) string {
	if s == "" {
		return "unknown (systemctl unavailable)"
	}
	return s
}

// ctxCanceled returns a *ports.Result describing the cancellation
// when ctx is done, or nil otherwise. Per ports.Probe contract,
// probes MUST return immediately on `<-ctx.Done()` — every Run
// method calls this at entry, and loop-based probes call it
// between iterations so a slow backend can't outlast the
// per-probe timeout.
//
// The returned Result is Status=Missing + Severity=Warn (NOT
// Blocker — a cancelled probe is operationally indeterminate, not
// a known-bad state). The doctor printer surfaces this as a
// "probe timed out" row.
func ctxCanceled(ctx context.Context) *ports.Result {
	if err := ctx.Err(); err != nil {
		return &ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("probe cancelled: %v", err),
		}
	}
	return nil
}
