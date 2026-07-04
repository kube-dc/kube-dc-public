package mock

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ScriptRunner replays a script's fixture from the scenario YAML. The
// fixture describes a list of (After, Stream, Text) lines plus an
// optional sentinel block + an exit code. The runner streams the lines
// with their configured delays, captures any sentinel payload into the
// SentinelCallback (registered via WithSentinelCallback), and emits the
// final StreamExit Line.
//
// Failure injection: when KUBE_DC_MOCK_FAIL=<scriptKind> is set in the
// environment, the matching script's exit code is forced to 1 even if
// the scenario YAML said 0. Used by integration tests of the failure
// paths (e.g. `init` with --no-tty when flux-bootstrap explodes).
type ScriptRunner struct {
	scenario *Scenario
	cb       ports.SentinelCallback
}

// NewScriptRunner constructs a runner bound to the given scenario.
// Callback is optional — when nil, sentinel-bracketed payloads are
// silently dropped (matching the production contract: never emit raw
// payload to the regular stream).
func NewScriptRunner(s *Scenario, cb ports.SentinelCallback) *ScriptRunner {
	return &ScriptRunner{scenario: s, cb: cb}
}

// WithSentinelCallback returns a copy of r with the callback replaced.
// Used by M5-T01 to install the share-capture callback for the openbao-
// init.sh invocation while leaving the surrounding session unchanged.
// Returns ports.ScriptRunner so the call site can stay
// adapter-agnostic.
func (r *ScriptRunner) WithSentinelCallback(cb ports.SentinelCallback) ports.ScriptRunner {
	cp := *r
	cp.cb = cb
	return &cp
}

// Run streams the configured fixture for `name`. Returns ErrUnknownScript
// when the scenario doesn't have a fixture for the requested ScriptKind
// (mock parity with the real adapter, which validates against the
// M3-T03 registry).
func (r *ScriptRunner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	if r.scenario == nil {
		return nil, fmt.Errorf("mock: ScriptRunner.Run: scenario is nil")
	}
	fixture, ok := r.scenario.Scripts[string(name)]
	if !ok {
		return nil, fmt.Errorf("%w: scenario %q has no fixture for %s", ports.ErrUnknownScript, r.scenario.Name, name)
	}

	out := make(chan ports.Line, 16)
	go r.replay(ctx, name, fixture, out)
	return out, nil
}

// replay runs in its own goroutine; closes `out` when the script
// completes or ctx cancels.
func (r *ScriptRunner) replay(ctx context.Context, kind ports.ScriptKind, f ScriptFixture, out chan<- ports.Line) {
	defer close(out)

	start := time.Now()

	// Build the timeline: a sorted slice of (offset, emit) closures.
	// We don't need true topo-sort because YAML preserves list order
	// in yaml.v3; we replay in the order the author wrote them. The
	// sentinel block emits at its declared offset (also relative to
	// start).
	type event struct {
		after time.Duration
		emit  func() error // returns non-nil → terminate (sentinel callback failed)
	}

	var events []event

	for _, ln := range f.Lines {
		ln := ln // capture
		d, err := parseDuration(ln.After)
		if err != nil {
			out <- ports.Line{Stream: ports.StreamStderr, Text: fmt.Sprintf("mock: bad After duration %q: %v", ln.After, err), Time: time.Now()}
			continue
		}
		events = append(events, event{
			after: d,
			emit: func() error {
				stream := ports.LineStream(ln.Stream)
				if stream == "" {
					stream = ports.StreamStdout
				}
				out <- ports.Line{Stream: stream, Text: ln.Text, Time: time.Now()}
				return nil
			},
		})
	}

	if f.Sentinel != nil {
		sf := f.Sentinel
		d, err := parseDuration(sf.After)
		if err != nil {
			out <- ports.Line{Stream: ports.StreamStderr, Text: fmt.Sprintf("mock: bad sentinel After %q: %v", sf.After, err), Time: time.Now()}
		} else {
			events = append(events, event{
				after: d,
				emit: func() error {
					return r.handleSentinel(kind, sf, out)
				},
			})
		}
	}

	// Emit events in declared order, sleeping between them. We DON'T
	// sort by After — authors expect lines in the order they wrote
	// them. Mismatched After values just become tight emissions.
	for _, e := range events {
		target := start.Add(e.after)
		now := time.Now()
		if delay := target.Sub(now); delay > 0 {
			select {
			case <-ctx.Done():
				out <- ports.Line{Stream: ports.StreamExit, Text: "130", Time: time.Now()} // 128 + SIGINT
				return
			case <-time.After(delay):
			}
		}
		if err := e.emit(); err != nil {
			// Per agent rule 7 / B-003: callback failure terminates
			// the script with a non-zero exit. Never emit raw payload.
			out <- ports.Line{Stream: ports.StreamStderr, Text: fmt.Sprintf("mock: sentinel callback failed: %v", err), Time: time.Now()}
			out <- ports.Line{Stream: ports.StreamExit, Text: "1", Time: time.Now()}
			return
		}
	}

	// Resolve the exit code, honouring KUBE_DC_MOCK_FAIL.
	exitCode := f.ExitCode
	if forced := os.Getenv("KUBE_DC_MOCK_FAIL"); forced != "" && strings.EqualFold(forced, string(kind)) {
		exitCode = 1
	}
	out <- ports.Line{Stream: ports.StreamExit, Text: strconv.Itoa(exitCode), Time: time.Now()}
}

// handleSentinel routes the sentinel payload through the callback (or
// drops it when no callback is registered). NEVER emits the raw payload
// to `out` — only a placeholder.
func (r *ScriptRunner) handleSentinel(kind ports.ScriptKind, sf *ScriptSentinel, out chan<- ports.Line) error {
	placeholder := fmt.Sprintf("[mock: sentinel %q payload diverted — %d bytes]", sf.Marker, len(sf.Payload))
	out <- ports.Line{Stream: ports.StreamStdout, Text: placeholder, Time: time.Now()}

	if r.cb == nil {
		// No callback registered → drop. Production runners with no
		// callback drop too (sentinel payloads are only meaningful
		// when someone's listening for them).
		return nil
	}
	return r.cb(kind, sf.Marker, []byte(sf.Payload))
}
