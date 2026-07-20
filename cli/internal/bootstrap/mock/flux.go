package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// FluxClient replays the scenario's Kustomization + HelmRelease fixtures
// as one-shot events on the watch channels. The mock doesn't simulate
// reconciliation over time — every event reflects the scenario's
// declared steady state. M7 waterfall tests that need a "Pending →
// Reconciling → Ready" sequence will need a scenario fixture that
// expresses the transitions explicitly (deferred to M7).
type FluxClient struct {
	scenario *Scenario
}

func NewFluxClient(s *Scenario) *FluxClient { return &FluxClient{scenario: s} }

// Bootstrap is a no-op in mock mode — flux is "already installed"
// after returning. Real adapter shells out to `flux bootstrap github`;
// mock just records the call (TODO: future enhancement, scenario tests
// can assert Bootstrap was called).
func (c *FluxClient) Bootstrap(ctx context.Context, opts ports.FluxBootstrapOpts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.scenario == nil {
		return fmt.Errorf("mock: FluxClient.Bootstrap: scenario is nil")
	}
	return nil
}

// PullArtifact is a no-op in mock mode — the starter is "already
// extracted". Mirrors Bootstrap's posture: record-only, never touches
// the network or the filesystem.
func (c *FluxClient) PullArtifact(ctx context.Context, url, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.scenario == nil {
		return fmt.Errorf("mock: FluxClient.PullArtifact: scenario is nil")
	}
	return nil
}

// WatchKustomizations emits one event per fixture, then closes. Caller
// MUST drain to completion.
func (c *FluxClient) WatchKustomizations(ctx context.Context) (<-chan ports.KustomizationEvent, error) {
	out := make(chan ports.KustomizationEvent, 8)
	go func() {
		defer close(out)
		if c.scenario == nil || c.scenario.Cluster == nil {
			return
		}
		for _, k := range c.scenario.Cluster.Kustomizations {
			ns := k.Namespace
			if ns == "" {
				ns = "flux-system"
			}
			ev := ports.KustomizationEvent{
				Name:        k.Name,
				Namespace:   ns,
				DependsOn:   k.DependsOn,
				Ready:       k.Ready,
				Reconciling: k.Reconciling,
				Reason:      k.Reason,
				Message:     k.Message,
				Revision:    k.Revision,
				LastApplied: time.Now().Add(-2 * time.Minute), // sensible default for mocks
				LastAttempt: time.Now().Add(-30 * time.Second),
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

func (c *FluxClient) WatchHelmReleases(ctx context.Context) (<-chan ports.HelmReleaseEvent, error) {
	out := make(chan ports.HelmReleaseEvent, 8)
	go func() {
		defer close(out)
		if c.scenario == nil || c.scenario.Cluster == nil {
			return
		}
		for _, h := range c.scenario.Cluster.HelmReleases {
			ev := ports.HelmReleaseEvent{
				Name:         h.Name,
				Namespace:    h.Namespace,
				Ready:        h.Ready,
				Reconciling:  h.Reconciling,
				Reason:       h.Reason,
				Message:      h.Message,
				ChartName:    h.ChartName,
				ChartVersion: h.ChartVersion,
				LastApplied:  time.Now().Add(-2 * time.Minute),
				LastAttempt:  time.Now().Add(-30 * time.Second),
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

// Reconcile is a no-op in mock mode. Records-of-call could be added
// later for test assertions.
func (c *FluxClient) Reconcile(ctx context.Context, kind, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
