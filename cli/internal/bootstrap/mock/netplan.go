package mock

import (
	"context"
)

// NetplanClient is a trivial fixture — Snapshot returns the scenario's
// netplan-files list, Restore is a no-op. The v2 host-prep flow tests
// will need richer scenarios when V1/V4 land.
type NetplanClient struct {
	scenario *Scenario
}

func NewNetplanClient(s *Scenario) *NetplanClient { return &NetplanClient{scenario: s} }

func (c *NetplanClient) Snapshot(ctx context.Context, dst string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.Host == nil {
		return []string{}, nil
	}
	out := make([]string, len(c.scenario.Host.NetplanFiles))
	copy(out, c.scenario.Host.NetplanFiles)
	return out, nil
}

func (c *NetplanClient) Restore(ctx context.Context, src string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
