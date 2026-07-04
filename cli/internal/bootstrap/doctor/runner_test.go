package doctor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeProbe runs a no-op Run + lets the test set the returned
// Result directly. Used to verify parallel + timeout behaviour
// without spinning up real probes.
type fakeProbe struct {
	name   string
	result ports.Result
	delay  time.Duration
	runs   *int32
}

func (f *fakeProbe) Name() string { return f.name }
func (f *fakeProbe) Run(ctx context.Context) ports.Result {
	if f.runs != nil {
		atomic.AddInt32(f.runs, 1)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			// Slow probe got cancelled — surface this so the test
			// can assert RunAll honours its own per-probe timeout.
			return ports.Result{
				Status:   ports.StatusMissing,
				Severity: ports.SeverityWarn,
				Detail:   "probe cancelled by runner: " + ctx.Err().Error(),
			}
		}
	}
	return f.result
}

func TestRunAll_EmptyInput_ReturnsNil(t *testing.T) {
	got := RunAll(context.Background(), nil)
	if got != nil {
		t.Errorf("RunAll(nil) = %v, want nil", got)
	}
}

func TestRunAll_GathersResultsFromAllProbes(t *testing.T) {
	probes := []CategorizedProbe{
		{Category: CategoryAutoHandled, Probe: &fakeProbe{name: "kubectl", result: ports.Result{Severity: ports.SeverityInfo, Detail: "k8s"}}},
		{Category: CategoryPhysical, Probe: &fakeProbe{name: "rke2-server", result: ports.Result{Severity: ports.SeverityInfo, Detail: "ok"}}},
		{Category: CategoryVerifiesSuggests, Probe: &fakeProbe{name: "wildcard-dns", result: ports.Result{Severity: ports.SeverityInfo, Detail: "ok"}}},
	}
	got := RunAll(context.Background(), probes)
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
}

func TestRunAll_SortsByCategoryThenName(t *testing.T) {
	probes := []CategorizedProbe{
		{Category: CategoryVerifiesSuggests, Probe: &fakeProbe{name: "z", result: ports.Result{Severity: ports.SeverityInfo}}},
		{Category: CategoryPhysical, Probe: &fakeProbe{name: "b", result: ports.Result{Severity: ports.SeverityInfo}}},
		{Category: CategoryPhysical, Probe: &fakeProbe{name: "a", result: ports.Result{Severity: ports.SeverityInfo}}},
		{Category: CategoryAutoHandled, Probe: &fakeProbe{name: "y", result: ports.Result{Severity: ports.SeverityInfo}}},
	}
	got := RunAll(context.Background(), probes)
	wantOrder := []string{"a", "b", "y", "z"} // Physical (a,b), AutoHandled (y), VerifiesSuggests (z)
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("got[%d].Name=%q want %q\n%+v", i, got[i].Name, want, got)
		}
	}
}

func TestRunAll_PerProbeTimeout_SlowProbeDoesNotBlockOthers(t *testing.T) {
	// One probe that exceeds the per-probe timeout + one fast one.
	// RunAll's wall clock should be ~PerProbeTimeout (slow probe's
	// ctx fires) NOT slow + fast (which would mean serial).
	slow := &fakeProbe{
		name:   "slow",
		delay:  PerProbeTimeout + 2*time.Second,
		result: ports.Result{Severity: ports.SeverityInfo, Detail: "should not be returned"},
	}
	fast := &fakeProbe{
		name:   "fast",
		delay:  10 * time.Millisecond,
		result: ports.Result{Severity: ports.SeverityInfo, Detail: "fast result"},
	}
	start := time.Now()
	results := RunAll(context.Background(), []CategorizedProbe{
		{Category: CategoryAutoHandled, Probe: slow},
		{Category: CategoryAutoHandled, Probe: fast},
	})
	elapsed := time.Since(start)

	// Wall clock should be bounded by per-probe timeout + tiny slack.
	if elapsed > PerProbeTimeout+1*time.Second {
		t.Errorf("RunAll took %v; per-probe timeout should cap at ~%v", elapsed, PerProbeTimeout)
	}
	// The slow probe should have returned a cancelled-by-timeout
	// Result (the fake propagates ctx.Err()).
	var slowResult, fastResult NamedResult
	for _, r := range results {
		switch r.Name {
		case "slow":
			slowResult = r
		case "fast":
			fastResult = r
		}
	}
	if slowResult.Result.Detail != "" && slowResult.Result.Detail == "should not be returned" {
		t.Errorf("slow probe should have been cancelled, got %q", slowResult.Result.Detail)
	}
	if fastResult.Result.Detail != "fast result" {
		t.Errorf("fast probe should have completed, got %q", fastResult.Result.Detail)
	}
}

func TestRunAll_OuterCtxCancel_CancelsAllProbes(t *testing.T) {
	var counter int32
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	probes := []CategorizedProbe{
		{Category: CategoryAutoHandled, Probe: &fakeProbe{name: "p1", delay: 3 * time.Second, runs: &counter}},
		{Category: CategoryAutoHandled, Probe: &fakeProbe{name: "p2", delay: 3 * time.Second, runs: &counter}},
	}
	start := time.Now()
	results := RunAll(ctx, probes)
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("outer ctx cancel should unblock probes quickly; took %v", elapsed)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
	if atomic.LoadInt32(&counter) != 2 {
		t.Errorf("both probes should have run; got %d", counter)
	}
}

func TestRunAll_ParallelExecution(t *testing.T) {
	// 5 probes each sleeping 100ms; parallel total should be ~100ms,
	// not 500ms.
	probes := make([]CategorizedProbe, 5)
	for i := range probes {
		probes[i] = CategorizedProbe{
			Category: CategoryAutoHandled,
			Probe: &fakeProbe{
				name:   "p" + string(rune('0'+i)),
				delay:  100 * time.Millisecond,
				result: ports.Result{Severity: ports.SeverityInfo},
			},
		}
	}
	start := time.Now()
	RunAll(context.Background(), probes)
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Errorf("5x parallel probes took %v; should be ~100ms not 500ms", elapsed)
	}
}
