package mock

import "context"

// SystemctlClient looks up the requested unit in the scenario's Host
// fixture. Today only `rke2-server` is modelled; adding more units is
// a scenario-YAML extension, not a code change.
type SystemctlClient struct {
	scenario *Scenario
}

func NewSystemctlClient(s *Scenario) *SystemctlClient { return &SystemctlClient{scenario: s} }

func (c *SystemctlClient) IsActive(ctx context.Context, unit string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if c.scenario == nil || c.scenario.Host == nil {
		return false, "inactive", nil
	}
	switch unit {
	case "rke2-server", "rke2-server.service":
		if c.scenario.Host.RKE2Active {
			return true, "active", nil
		}
		return false, "inactive", nil
	default:
		return false, "inactive", nil
	}
}

func (c *SystemctlClient) Restart(ctx context.Context, unit string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Mock: noop. Real adapter would shell out + may surface ErrNeedsSudo.
	return nil
}
