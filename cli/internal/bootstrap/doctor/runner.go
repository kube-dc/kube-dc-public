package doctor

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// PerProbeTimeout is the per-probe deadline the runner enforces. Per
// the ports.Probe contract: "Return within a per-probe timeout (the
// probe set runner caps at 5s each so a slow probe doesn't block the
// whole report)". The runner wraps each probe's ctx with this
// timeout — the probe sees a ctx that fires at the same wall-clock
// point as the runner's own deadline.
const PerProbeTimeout = 5 * time.Second

// CategorizedProbe pairs a Probe with the category it lands in. The
// runner uses Category when assembling NamedResults so the printer
// can do its three-section layout without the caller doing an
// extra sort pass.
type CategorizedProbe struct {
	Category Category
	Probe    ports.Probe
}

// RunAll executes every probe in parallel with a per-probe timeout
// and returns the gathered NamedResults sorted by (Category, Name)
// for deterministic snapshot output.
//
// Parallel execution matters here: M1's "fresh cluster" doctor run
// against a real cluster can have ~20 probes; running them serially
// at the 5s timeout each would worst-case at 100s. Parallel caps
// the total wall-clock at the slowest probe's timeout.
//
// Caller's `ctx` is the outer deadline; per-probe `ctx.WithTimeout`
// is layered on top. Cancelling the outer ctx cancels every in-
// flight probe in addition to whatever per-probe timeouts are
// running.
func RunAll(ctx context.Context, probes []CategorizedProbe) []NamedResult {
	if len(probes) == 0 {
		return nil
	}

	results := make([]NamedResult, len(probes))
	var wg sync.WaitGroup
	wg.Add(len(probes))

	for i, cp := range probes {
		i, cp := i, cp
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, PerProbeTimeout)
			defer cancel()
			r := cp.Probe.Run(pctx)
			results[i] = NamedResult{
				Category: cp.Category,
				Name:     cp.Probe.Name(),
				Result:   r,
			}
		}()
	}

	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		return results[i].Name < results[j].Name
	})
	return results
}
