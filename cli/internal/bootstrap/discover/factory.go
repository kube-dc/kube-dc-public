package discover

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Factory builds the three probe lists doctor renders. Mock mode
// (KUBE_DC_MOCK) wires a scenario-backed factory from the mock
// package; real mode wires `RealFactory` which executes against
// the operator's actual environment.
//
// Pulling probe construction behind an interface is what makes the
// M1-T06 acceptance criterion (`KUBE_DC_MOCK=cloud doctor --no-tty`
// → exit 0) deterministic: the cobra command no longer shells out
// to the operator's local kubectl/gh/etc. when running against a
// canned scenario.
type Factory interface {
	// Tools returns the local-tooling probes (kubectl/flux/sops/age/
	// git/gh/ssh/bao). Real mode shells out; mock mode reads the
	// scenario's `tools:` section.
	Tools() []ports.Probe

	// Host returns the operator-machine probes (systemctl,
	// /proc/modules, sysctl, netplan, /sys/class/net, IPv6,
	// memory). `mode` controls whether they run at all or
	// short-circuit to "not applicable".
	Host(mode HostProbeMode) []ports.Probe

	// Accelerators returns host-local PCI/driver/IOMMU/VFIO/KVM probes.
	// It is separate from Host so doctor can render a stable Accelerators
	// category without identifying probe types by their display names.
	Accelerators(mode HostProbeMode) []ports.Probe

	// DNS returns the wildcard + explicit-FQDN probes resolved via
	// the supplied client. Empty `domain` → no DNS section.
	DNS(domain, nodeIP string, dns ports.DNSClient) []ports.Probe

	// Cluster returns probes that read cluster state through the
	// K8sClient (Node labels, Deployments, etc.). Currently just the
	// M1-T04 NFDProbe. Empty when `k8s` is nil so the cobra layer
	// can safely call this without pre-checking session shape —
	// mock scenarios that omit `nodeLabels:` still return an empty
	// slice; real factory returns a live NFDProbe.
	//
	// Category on the doctor side is `CategoryPhysical` (NFD
	// surfaces facts about the physical / VM hosts backing the
	// cluster — CPU capability, GPU presence, kernel-module state).
	Cluster(k8s ports.K8sClient) []ports.Probe

	// ClusterOpenBao returns probes that read OpenBao cluster state
	// through the OpenBaoClient (svc/openbao annotations, pod state,
	// etc.). Currently just the M5-T07 PolicyGenerationProbe. Empty
	// when `bao` is nil so the cobra layer can call this without
	// pre-checking session shape — mirrors Cluster()'s nil-safe
	// contract.
	//
	// Category on the doctor side is `CategoryVerifiesSuggests`
	// (drift is a soft signal the CLI verifies + points at a fix
	// via the operator's discretion; it never blocks).
	ClusterOpenBao(bao ports.OpenBaoClient) []ports.Probe

	// StatusRows returns one StatusRow per cluster overlay in the
	// fleet — drives `kube-dc bootstrap status` list-all mode.
	// Real factory walks `fleetRepo` (clusters/*/cluster-config.env)
	// and probes each cluster via ClusterProbe. Mock factory reads
	// the scenario's fleet fixture and ignores `fleetRepo`.
	StatusRows(ctx context.Context, fleetRepo string) ([]StatusRow, error)

	// StatusDeep returns the per-cluster deep view (reconcilers,
	// helm releases, drift, optional fix hint). Returns
	// ErrClusterNotFound when `name` isn't in the fleet.
	StatusDeep(ctx context.Context, fleetRepo, name string) (*StatusDeepResult, error)
}

// StatusRow is one cluster's high-level summary — drives the
// list-all table in `kube-dc bootstrap status`.
type StatusRow struct {
	Name   string
	Status ClusterStatus
	Detail string
}

// StatusDeepResult is the per-cluster detail view payload. Reuses
// the existing `ProbeResult` shape (reconcilers + drifts) and adds
// the cluster's metadata for the header block.
type StatusDeepResult struct {
	Name   string
	Domain string
	APIURL string
	Result ProbeResult
}

// ErrClusterNotFound is returned by StatusDeep when no cluster
// overlay matches the requested name. Callers catch this and print
// a known-list message.
var ErrClusterNotFound = errors.New("discover: cluster not found in fleet")

// RealFactory builds probes that execute against the operator's
// actual environment. Used outside KUBE_DC_MOCK.
type RealFactory struct{}

// Compile-time assertion.
var _ Factory = RealFactory{}

func (RealFactory) Tools() []ports.Probe {
	return AllToolProbes()
}

func (RealFactory) Host(mode HostProbeMode) []ports.Probe {
	return AllHostProbes(mode)
}

func (RealFactory) Accelerators(mode HostProbeMode) []ports.Probe {
	return []ports.Probe{NewGPUHostProbe(mode, realHostFS{})}
}

func (RealFactory) DNS(domain, nodeIP string, dns ports.DNSClient) []ports.Probe {
	if domain == "" {
		return nil
	}
	return AllDNSProbes(domain, nodeIP, dns)
}

// Cluster returns the cluster-scope probes (currently just NFD).
// Empty when `k8s` is nil so a caller without a resolved
// kubeconfig doesn't need to pre-guard.
func (RealFactory) Cluster(k8s ports.K8sClient) []ports.Probe {
	if k8s == nil {
		return nil
	}
	return []ports.Probe{NewNFDProbe(k8s), NewInfraAttachProbe(k8s, "")}
}

// ClusterOpenBao returns the openbao-scope probes (currently just
// the M5-T07 policy-generation drift probe). Empty when `bao` is
// nil — same nil-safe convention as Cluster().
func (RealFactory) ClusterOpenBao(bao ports.OpenBaoClient) []ports.Probe {
	if bao == nil {
		return nil
	}
	return []ports.Probe{NewPolicyGenerationProbe(bao)}
}

// StatusRows walks `fleetRepo` for clusters/*/cluster-config.env,
// probes each concurrently via ClusterProbe, and returns one row per
// cluster sorted by name.
//
// **Single probe per row** (M2-T01/T02/T03 review-pass fix): the
// shipped cobra command ran two ClusterProbe.Run() calls per row
// (one for status, one for Detail). That doubled token/API calls
// and let status + detail drift if one call failed differently.
// This implementation runs Run() once per cluster and fills both
// fields from the same result.
func (RealFactory) StatusRows(ctx context.Context, fleetRepo string) ([]StatusRow, error) {
	if fleetRepo == "" {
		return nil, fmt.Errorf("status: empty fleet repo")
	}
	clusters, err := ListClusters(fleetRepo)
	if err != nil {
		return nil, fmt.Errorf("list clusters in %s: %w", fleetRepo, err)
	}

	rows := make([]StatusRow, len(clusters))
	var wg sync.WaitGroup
	for i, c := range clusters {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := probeFullForCluster(ctx, c)
			rows[i] = StatusRow{
				Name:   c.Name,
				Status: result.Status,
				Detail: result.Detail,
			}
		}()
	}
	wg.Wait()

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func (RealFactory) StatusDeep(ctx context.Context, fleetRepo, name string) (*StatusDeepResult, error) {
	if fleetRepo == "" {
		return nil, fmt.Errorf("status: empty fleet repo")
	}
	clusters, err := ListClusters(fleetRepo)
	if err != nil {
		return nil, fmt.Errorf("list clusters in %s: %w", fleetRepo, err)
	}
	for _, c := range clusters {
		if c.Name == name {
			result := probeFullForCluster(ctx, c)
			return &StatusDeepResult{
				Name:   c.Name,
				Domain: c.Domain,
				APIURL: c.KubeAPIURL,
				Result: result,
			}, nil
		}
	}
	return nil, ErrClusterNotFound
}

// probeFullForCluster runs ClusterProbe once and returns the
// aggregate ProbeResult. Handles missing kubeAPIURL + probe-
// construction failure gracefully.
func probeFullForCluster(ctx context.Context, c Cluster) ProbeResult {
	if c.KubeAPIURL == "" {
		return ProbeResult{
			Status: StatusUnknown,
			Detail: "no KUBE_API_EXTERNAL_URL in cluster-config.env",
		}
	}
	probe, err := NewClusterProbe(ctx, c.KubeAPIURL, 5*time.Second)
	if err != nil {
		return ProbeResult{
			Status: StatusUnreachable,
			Detail: err.Error(),
		}
	}
	if c.Env != nil {
		probe.ExpectedTags = DefaultExpectedTags(c.Env)
	}
	return probe.Run(ctx)
}

// ---------- StaticProbe ----------

// StaticProbe is a ports.Probe whose Run() returns a pre-baked
// Result. Used by mock.Factory to emit scenario-backed probe
// outcomes (no exec, no filesystem reads, no network).
//
// Tests outside the mock package can use this too — wherever a
// scenario-shaped Result is more honest than a fake exec hook.
type StaticProbe struct {
	name   string
	result ports.Result
}

// Compile-time assertion.
var _ ports.Probe = (*StaticProbe)(nil)

// NewStaticProbe wraps a (name, result) pair as a ports.Probe.
func NewStaticProbe(name string, result ports.Result) *StaticProbe {
	return &StaticProbe{name: name, result: result}
}

func (p *StaticProbe) Name() string { return p.name }

// Run honours ctx.Err() per the ports.Probe contract — even
// static probes must short-circuit on cancellation so the runner's
// per-probe timeout works correctly.
func (p *StaticProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	return p.result
}
