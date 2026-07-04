// Package bootstrap is the engine that drives `kube-dc bootstrap` — the
// post-RKE2 installer + day-2 surface for Kube-DC clusters. The package
// itself owns only the wiring shape (Options + Session); concrete work
// lives under sub-packages (ports/, mock/, adapters/, discover/, …).
package bootstrap

import (
	"context"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Session is the bag of ports the engine works against. Every screen
// and cobra command takes a *Session, never the individual ports
// directly — this makes mock vs real swap a single env-var (KUBE_DC_MOCK)
// and keeps the call sites uniform.
//
// Constructed via NewSession (in wire.go). Closed via Close(); callers
// MUST `defer s.Close()` for any session they create.
type Session struct {
	// Scenario is empty when the session is real, set to the scenario
	// name when KUBE_DC_MOCK was honoured. Useful for status banners
	// ("running against mock: cloud") and for tests that need to assert
	// they got the mock path.
	Scenario string

	// Probes is the factory the M1 doctor command uses to build
	// its probe set. Mock sessions wire a scenario-backed factory
	// (probes return canned Results — no exec, no FS reads); real
	// sessions wire `discover.RealFactory`. The split is what makes
	// `KUBE_DC_MOCK=cloud doctor` deterministic regardless of the
	// operator's local kubectl/gh/etc. state.
	Probes discover.Factory

	// Ports — the contract surface. All non-nil after NewSession.
	Probe     ports.Probe         // factory pattern: discover layer creates concrete Probes; the bag holds a default reference
	Scripts   ports.ScriptRunner
	Flux      ports.FluxClient
	K8s       ports.K8sClient
	OpenBao   ports.OpenBaoClient
	Git       ports.GitClient
	SOPS      ports.SOPSClient
	Systemctl ports.SystemctlClient
	Netplan   ports.NetplanClient
	DNS       ports.DNSClient
	SSH       ports.SSHClient

	// cancel is the close hook for ctx-bound resources (e.g. the real
	// flux client's watch streams). Mock sessions store a no-op.
	cancel context.CancelFunc
}

// Close releases any resources held by the session (e.g. closes the
// flux watch channels). Safe to call multiple times.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return nil
}

// Options configure NewSession. Fields populated by cobra flag parsing
// + env-var resolution at the cobra entry point. Add new fields here
// (don't grow per-port arg lists).
type Options struct {
	// FleetRepoPath is the operator-supplied fleet repo root (after
	// resolution via --repo / $KUBE_DC_FLEET / ~/.kube-dc/fleet).
	// Empty when the operator hasn't pointed at a fleet yet.
	FleetRepoPath string

	// Kubeconfig is the path to use for K8s API calls. Empty falls
	// back to $KUBECONFIG / ~/.kube/config.
	Kubeconfig string

	// Cluster, when non-empty, narrows the session to one cluster
	// (e.g. `kube-dc bootstrap status acme-prod` → "acme-prod").
	Cluster string
}
