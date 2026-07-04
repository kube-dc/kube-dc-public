package mock

import (
	"context"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Probe is a one-shot scenario-backed implementation of ports.Probe.
// Each instance owns a name + the scenario it pulls from; the discover
// layer (M1) builds a ProbeSet of these from a single scenario when
// running in mock mode.
type Probe struct {
	name string
	run  func(context.Context) ports.Result
}

// NewProbe wraps a name + result-producer into a ports.Probe. The
// run function captures the scenario reference + any per-probe
// resolution logic (e.g. "tool kubectl" → looks up scenario.Tools["kubectl"]).
func NewProbe(name string, run func(context.Context) ports.Result) *Probe {
	return &Probe{name: name, run: run}
}

func (p *Probe) Name() string { return p.name }

func (p *Probe) Run(ctx context.Context) ports.Result {
	if err := ctx.Err(); err != nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "probe cancelled",
		}
	}
	return p.run(ctx)
}
