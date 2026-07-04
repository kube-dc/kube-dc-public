package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/dns"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/flux"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/git"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/k8s"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/netplan"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/script"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/sops"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/systemctl"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/mock"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ErrRealAdaptersNotReady is preserved as a sentinel for callers that
// pre-dated M0-T06 batch 2. As of this commit, NewSession can return
// a real Session whenever it has a kubeconfig to point at; the
// sentinel is now used only when the operator explicitly opts out of
// k8s-required setup (a Session is constructed without a working
// kubeconfig and a caller requests K8s).
var ErrRealAdaptersNotReady = errors.New("bootstrap: cannot construct real session (no kubeconfig resolved)")

// NewSession constructs a Session bag of ports. When KUBE_DC_MOCK is
// set in the environment, every port is a scenario-backed mock; when
// unset, the real adapters are wired against the operator's
// kubeconfig. Caller MUST `defer s.Close()`.
func NewSession(opts Options) (*Session, error) {
	if scenario := os.Getenv("KUBE_DC_MOCK"); scenario != "" {
		return newMockSession(scenario)
	}
	return newRealSession(opts)
}

// newRealSession assembles the production Session from the
// `adapters/` packages. Each adapter is independently constructable —
// the wire layer is the only place that has to know about all of
// them.
func newRealSession(opts Options) (*Session, error) {
	k8sClient, err := k8s.New(opts.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRealAdaptersNotReady, err)
	}

	fluxClient, err := flux.New(opts.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: build flux adapter: %w", err)
	}

	// ScriptRunner needs both fleet + kube-dc roots. The kube-dc repo
	// is resolved from the operator's environment ($KUBE_DC_REPO) or
	// left empty when the operator hasn't checked out the monorepo —
	// only ScriptOpenBaoSetupControllerAuth needs it, and the script
	// adapter returns a clear "configure --kube-dc-repo" error if
	// invoked unconfigured.
	scripts := script.New(opts.FleetRepoPath, os.Getenv("KUBE_DC_REPO"), nil)

	_, cancel := context.WithCancel(context.Background())
	return &Session{
		Scenario:  "",
		Probes:    discover.RealFactory{},
		Scripts:   scripts,
		Flux:      fluxClient,
		K8s:       k8sClient,
		OpenBao:   openbao.New(k8sClient),
		Git:       git.New(),
		SOPS:      sops.New(),
		Systemctl: systemctl.New(),
		Netplan:   netplan.New(),
		DNS:       dns.New(),
		SSH:       ssh.New(),
		Probe:     nil, // discover layer constructs probes on demand
		cancel:    cancel,
	}, nil
}

// newMockSession wires the mock package's Session into the parent
// bootstrap.Session shape. The two structs are deliberately distinct
// (see mock/session.go) to avoid an import cycle.
func newMockSession(name string) (*Session, error) {
	ms, err := mock.NewSession(name)
	if err != nil {
		return nil, err
	}
	_, cancel := context.WithCancel(context.Background())
	return &Session{
		Scenario:  name,
		Probes:    &mock.Factory{Scenario: ms.Scenario},
		Scripts:   ms.Scripts,
		Flux:      ms.Flux,
		K8s:       ms.K8s,
		OpenBao:   ms.OpenBao,
		Git:       ms.Git,
		SOPS:      ms.SOPS,
		Systemctl: ms.Systemctl,
		Netplan:   ms.Netplan,
		DNS:       ms.DNS,
		SSH:       ms.SSH,
		// Probe is nil at the session level — discover layer (M1)
		// builds concrete Probes from the scenario data via
		// mock.NewProbe. The session bag just holds ports; the
		// probe-set runner is its own piece.
		Probe:  nil,
		cancel: cancel,
	}, nil
}

// ListMockScenarios returns the available scenario names. Surfaced via
// `kube-dc bootstrap --list-scenarios` (M0-T02 acceptance smoke) and
// the ErrRealAdaptersNotReady message.
func ListMockScenarios() ([]string, error) {
	return mock.ListScenarios()
}

// NewSOPSOnly constructs just the SOPS port. Deliberately isolated
// from NewSession so subcommands that don't need K8s / OpenBao /
// Flux (M5-T05 reveal-shares; M5-T06 generate-root's SOPS-decrypt
// step; any future SOPS-only ceremony) can proceed on a machine
// without a valid kubeconfig — the emergency-recovery case where
// the operator has the fleet + SOPS key but the cluster is
// unreachable.
//
// KUBE_DC_MOCK is honoured — mock scenarios wire the in-memory
// SOPSClient — so tests keep the mock path available without
// paying the k8s.New() price. Never returns nil client on nil
// error; never touches kubeconfig / apiserver / any other adapter.
func NewSOPSOnly() (ports.SOPSClient, error) {
	if scenario := os.Getenv("KUBE_DC_MOCK"); scenario != "" {
		s, err := mock.Load(scenario)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: load mock scenario %q: %w", scenario, err)
		}
		return mock.NewSOPSClient(s), nil
	}
	return sops.New(), nil
}
