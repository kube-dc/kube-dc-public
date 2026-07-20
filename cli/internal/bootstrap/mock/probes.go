package mock

import (
	"context"
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Factory implements discover.Factory backed by a scenario's
// fixtures. Lets the M1-T06 doctor command produce deterministic
// output under KUBE_DC_MOCK without shelling out to the operator's
// real local environment — the acceptance contract from the
// installer-agentic-implementation-plan:
//
//	KUBE_DC_MOCK=cloud kube-dc bootstrap doctor --no-tty            # exit 0
//	KUBE_DC_MOCK=fresh kube-dc bootstrap doctor --no-tty            # exit 1
//	KUBE_DC_MOCK=openbao-sealed kube-dc bootstrap doctor --no-tty   # exit 1
type Factory struct {
	Scenario *Scenario
}

// Compile-time assertion.
var _ discover.Factory = (*Factory)(nil)

// Tools emits a StaticProbe per tool in the scenario's `tools:`
// section. Reuses the version-floor + gh-scope check logic from
// `discover` so mock + real share the same semantics — only the
// data source differs.
func (f *Factory) Tools() []ports.Probe {
	if f == nil || f.Scenario == nil {
		return nil
	}
	// Deterministic order so snapshot tests stay stable.
	names := make([]string, 0, len(f.Scenario.Tools))
	for n := range f.Scenario.Tools {
		names = append(names, n)
	}
	sortStrings(names)
	probes := make([]ports.Probe, 0, len(names))
	for _, name := range names {
		fixture := f.Scenario.Tools[name]
		probes = append(probes, discover.NewStaticProbe(name, toolResultFromFixture(name, fixture)))
	}
	return probes
}

// Host emits StaticProbes per host check. When mode=Off OR the
// scenario has no Host fixture, every probe short-circuits to the
// "not applicable" Result the real Off-mode path produces — keeps
// the printer output shape identical across modes.
func (f *Factory) Host(mode discover.HostProbeMode) []ports.Probe {
	hostNames := []string{
		"rke2-server", "kernel-modules", "sysctl", "netplan",
		"ipv6", "nic-inventory", "memory",
	}
	if mode == discover.HostProbeOff || f == nil || f.Scenario == nil || f.Scenario.Host == nil {
		out := make([]ports.Probe, 0, len(hostNames))
		for _, n := range hostNames {
			out = append(out, discover.NewStaticProbe(n, notApplicableResult(n)))
		}
		return out
	}
	h := f.Scenario.Host
	out := make([]ports.Probe, 0, len(hostNames))
	out = append(out, discover.NewStaticProbe("rke2-server", rke2ResultFromFixture(h)))
	out = append(out, discover.NewStaticProbe("kernel-modules", kernelModulesResultFromFixture(h)))
	// sysctl + netplan + memory + ipv6 don't have dedicated fixture
	// fields — they exist on the host but scenarios don't model the
	// granular numbers. Emit Installed+Info to reflect "host has
	// these surfaces, nothing alarming to report" rather than
	// fabricating a partial state.
	out = append(out, discover.NewStaticProbe("sysctl",
		okResult("all required sysctl values at or above floor")))
	out = append(out, discover.NewStaticProbe("netplan",
		netplanResultFromFixture(h)))
	out = append(out, discover.NewStaticProbe("ipv6", ipv6ResultFromFixture(h)))
	out = append(out, discover.NewStaticProbe("nic-inventory", nicResultFromFixture(h)))
	out = append(out, discover.NewStaticProbe("memory", memoryResultFromFixture(h)))
	return out
}

// Accelerators mirrors the real factory's dedicated doctor category. Existing
// scenarios intentionally default to no accelerator fixture; hardware-specific
// facts are covered by discover's sysfs fixtures instead of fabricated here.
func (f *Factory) Accelerators(mode discover.HostProbeMode) []ports.Probe {
	result := notApplicableResult("accelerator-pci")
	if mode == discover.HostProbeOn {
		result = ports.Result{
			Status: ports.StatusMissing, Severity: ports.SeverityInfo,
			Detail: "no PCI class 0300/0302 accelerator fixture",
		}
	}
	return []ports.Probe{discover.NewStaticProbe("accelerator-pci", result)}
}

// DNS delegates to the real DNS probes — the mock session's
// DNSClient is already scenario-backed (mock.DNSClient resolves
// against the scenario's `dns:` map), so wiring the real probes
// through it gives us scenario-deterministic output.
func (f *Factory) DNS(domain, nodeIP string, dns ports.DNSClient) []ports.Probe {
	if domain == "" {
		return nil
	}
	return discover.AllDNSProbes(domain, nodeIP, dns)
}

// Cluster wraps the real NFDProbe against the scenario's mock
// K8sClient (which returns `scenario.cluster.nodeLabels` from its
// NodeLabels method). Scenarios that omit `nodeLabels:` produce an
// NFDResult with TotalNodes=0, which the probe renders as
// StatusMissing — exactly what the doctor should show for a fresh
// cluster without NFD.
//
// Consistent with the DNS shape above (mock wraps real probes
// against mock adapters) so mock + real doctor output stay in
// lockstep — only the data source differs.
func (f *Factory) Cluster(k8s ports.K8sClient) []ports.Probe {
	if k8s == nil {
		return nil
	}
	return []ports.Probe{discover.NewNFDProbe(k8s)}
}

// ClusterOpenBao mirrors the Cluster() shape but for the M5-T07
// PolicyGenerationProbe. Wraps the real discover probe against the
// mock OpenBaoClient (whose GetAnnotation returns scenario-declared
// annotations from `openbao.controllerAuthAnnotation` +
// `openbao.bootstrapFinalizedAnnotation` — the same map used by the
// M5-T04 status renderer). Scenarios that pre-date M5-T07 (no
// policy-generation stamp) surface as StatusMissing/absent, which
// is the correct signal for legacy installs.
func (f *Factory) ClusterOpenBao(bao ports.OpenBaoClient) []ports.Probe {
	if bao == nil {
		return nil
	}
	return []ports.Probe{discover.NewPolicyGenerationProbe(bao)}
}

// StatusRows builds per-cluster rows from the scenario's
// `fleet.statuses` fixture. When `Statuses` is empty, falls back
// to one Unknown row per `fleet.clusterDirs` entry so the cobra
// surface is still exercisable. `fleetRepo` is ignored — mock mode
// uses scenario data, not the filesystem.
func (f *Factory) StatusRows(ctx context.Context, fleetRepo string) ([]discover.StatusRow, error) {
	_ = ctx
	_ = fleetRepo
	if f == nil || f.Scenario == nil || f.Scenario.Fleet == nil {
		return nil, nil
	}
	fleet := f.Scenario.Fleet

	// Per-name index of the scenario's status fixtures.
	byName := make(map[string]StatusFixture, len(fleet.Statuses))
	for _, st := range fleet.Statuses {
		byName[st.Name] = st
	}

	// Union of cluster dirs + status names, deterministically
	// ordered.
	names := append([]string(nil), fleet.ClusterDirs...)
	for n := range byName {
		if !stringSliceContains(names, n) {
			names = append(names, n)
		}
	}
	sortStrings(names)

	rows := make([]discover.StatusRow, 0, len(names))
	for _, name := range names {
		if st, ok := byName[name]; ok {
			rows = append(rows, discover.StatusRow{
				Name:   name,
				Status: parseClusterStatus(st.Status),
				Detail: st.Detail,
			})
			continue
		}
		rows = append(rows, discover.StatusRow{
			Name:   name,
			Status: discover.StatusUnknown,
			Detail: "no scenario fixture for this cluster",
		})
	}
	return rows, nil
}

// StatusDeep returns the per-cluster detail view from the
// scenario's StatusFixture. Returns ErrClusterNotFound for names
// not in `fleet.statuses` or `fleet.clusterDirs`.
func (f *Factory) StatusDeep(ctx context.Context, fleetRepo, name string) (*discover.StatusDeepResult, error) {
	_ = ctx
	_ = fleetRepo
	if f == nil || f.Scenario == nil || f.Scenario.Fleet == nil {
		return nil, discover.ErrClusterNotFound
	}
	fleet := f.Scenario.Fleet
	for _, st := range fleet.Statuses {
		if st.Name == name {
			return statusDeepFromFixture(st), nil
		}
	}
	// Fall back to "Unknown but listed" for dirs without a status
	// fixture.
	for _, d := range fleet.ClusterDirs {
		if d == name {
			return &discover.StatusDeepResult{
				Name: name,
				Result: discover.ProbeResult{
					Status: discover.StatusUnknown,
					Detail: "no scenario fixture for this cluster",
				},
			}, nil
		}
	}
	return nil, discover.ErrClusterNotFound
}

func statusDeepFromFixture(st StatusFixture) *discover.StatusDeepResult {
	out := &discover.StatusDeepResult{
		Name:   st.Name,
		Domain: st.Domain,
		APIURL: st.APIURL,
		Result: discover.ProbeResult{
			Status:  parseClusterStatus(st.Status),
			Detail:  st.Detail,
			FixHint: st.FixHint,
		},
	}
	for _, r := range st.Reconcilers {
		out.Result.Reconcilers = append(out.Result.Reconcilers, discover.ReconcilerStatus{
			Name:      r.Name,
			Ready:     r.Ready,
			Message:   r.Message,
			Suspended: r.Suspended,
		})
	}
	for _, d := range st.Drifts {
		out.Result.Drifts = append(out.Result.Drifts, discover.ImageDrift{
			Deployment: d.Deployment,
			Namespace:  d.Namespace,
			EnvVar:     d.EnvVar,
			Expected:   d.Expected,
			Running:    d.Running,
		})
	}
	return out
}

// parseClusterStatus maps a YAML-friendly string to the typed
// discover.ClusterStatus. Unknown strings fall back to
// StatusUnknown so a typo in a scenario file is loud (Unknown
// rows print) but not fatal.
func parseClusterStatus(s string) discover.ClusterStatus {
	switch s {
	case "Ready":
		return discover.StatusReady
	case "Reconciling":
		return discover.StatusReconciling
	case "Drifted":
		return discover.StatusDrifted
	case "Failed":
		return discover.StatusFailed
	case "Unreachable":
		return discover.StatusUnreachable
	case "Unknown", "":
		return discover.StatusUnknown
	}
	return discover.StatusUnknown
}

func stringSliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---------- per-fixture Result translators ----------

func toolResultFromFixture(name string, t ToolFixture) ports.Result {
	if !t.Installed {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   fmt.Sprintf("%s not found in PATH", name),
			FixHint:  ports.FixHint{Text: discover.InstallPrereqsHint},
		}
	}
	// Parse version, compare to floor.
	ver, err := discover.ParseSemver(t.Version)
	if err != nil {
		// gh / ssh / bao may have non-semver shapes; the real
		// adapter handles those via per-tool parsers. For mock we
		// trust the fixture's `version:` value as-is.
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Version:  t.Version,
			Detail:   fmt.Sprintf("%s %s", name, t.Version),
		}
	}
	if min, hasFloor := discover.MinVersions[name]; hasFloor && ver.Less(min) {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Version:  t.Version,
			Detail:   fmt.Sprintf("%s %s < required %s", name, t.Version, min.String()),
			FixHint: ports.FixHint{
				Text: fmt.Sprintf("Upgrade %s to ≥%s via %s", name, min.String(), discover.InstallPrereqsHint),
			},
		}
	}
	// gh scope check.
	if name == "gh" {
		missing := discover.RequiredGHScopes(t.Scopes)
		if len(missing) > 0 {
			return ports.Result{
				Status:   ports.StatusPartial,
				Severity: ports.SeverityWarn,
				Version:  t.Version,
				Detail:   fmt.Sprintf("gh missing scopes: %s", strings.Join(missing, ", ")),
				FixHint: ports.FixHint{
					Text: "Run `gh auth refresh --scopes repo,workflow` to grant missing scopes",
				},
			}
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Version:  t.Version,
		Detail:   fmt.Sprintf("%s %s", name, t.Version),
	}
}

func rke2ResultFromFixture(h *HostFixture) ports.Result {
	if h.RKE2Active {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "rke2-server unit is active",
		}
	}
	return ports.Result{
		Status:   ports.StatusMissing,
		Severity: ports.SeverityInfo,
		Detail:   "rke2-server unit inactive",
		FixHint:  ports.FixHint{Text: "Start with `sudo systemctl start rke2-server` if the node should be running RKE2"},
	}
}

func kernelModulesResultFromFixture(h *HostFixture) ports.Result {
	// Mirror the real probe's contract: only `nf_conntrack` is
	// strictly required.
	want := "nf_conntrack"
	for _, m := range h.KernelModules {
		if m == want {
			return ports.Result{
				Status:   ports.StatusInstalled,
				Severity: ports.SeverityInfo,
				Detail:   "all required modules loaded (nf_conntrack)",
			}
		}
	}
	return ports.Result{
		Status:   ports.StatusPartial,
		Severity: ports.SeverityBlocker,
		Detail:   "missing kernel modules: nf_conntrack",
		FixHint:  ports.FixHint{Text: "Run `sudo modprobe nf_conntrack`"},
	}
}

func netplanResultFromFixture(h *HostFixture) ports.Result {
	if len(h.NetplanFiles) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "/etc/netplan absent or empty",
		}
	}
	names := make([]string, len(h.NetplanFiles))
	for i, full := range h.NetplanFiles {
		names[i] = baseName(full)
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("netplan files: %s", strings.Join(names, ", ")),
	}
}

func ipv6ResultFromFixture(h *HostFixture) ports.Result {
	if h.IPv6Enabled {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "IPv6 enabled (informational — v1 stack uses IPv4)",
		}
	}
	return ports.Result{
		Status:   ports.StatusMissing,
		Severity: ports.SeverityInfo,
		Detail:   "IPv6 disabled (v1 stack is IPv4-only — harmless)",
	}
}

func nicResultFromFixture(h *HostFixture) ports.Result {
	if len(h.NICs) == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "no NICs found in /sys/class/net (excluding lo)",
		}
	}
	parts := make([]string, len(h.NICs))
	for i, n := range h.NICs {
		parts[i] = fmt.Sprintf("%s(up)", n)
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("NICs: %s", strings.Join(parts, ", ")),
	}
}

func memoryResultFromFixture(h *HostFixture) ports.Result {
	if h.FreeRAMGB == 0 {
		return ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "memory: fixture missing FreeRAMGB",
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("free memory: %d GiB", h.FreeRAMGB),
	}
}

// notApplicableResult mirrors the discover package's Off-mode
// short-circuit so mock + real produce visually identical "not
// applicable" rows.
func notApplicableResult(name string) ports.Result {
	return ports.Result{
		Status:   ports.StatusMissing,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("%s: not applicable (cluster-only mode)", name),
	}
}

func okResult(detail string) ports.Result {
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   detail,
	}
}

// ---------- micro-helpers ----------

// sortStrings is the obvious sort. Vendored as a tiny helper rather
// than pulling sort into a file that doesn't otherwise need it.
func sortStrings(s []string) {
	// Bubble sort — fine for the ≤8 tools we have. Avoids the
	// `sort` import; this file's surface is intentionally tiny.
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// baseName strips a directory prefix from a path. /etc/netplan/foo.yaml
// → foo.yaml. Avoids filepath import for a one-line helper.
func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
