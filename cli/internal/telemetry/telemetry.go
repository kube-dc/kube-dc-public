// Package telemetry is the C2 opt-in usage-counter seam
// (installer-agentic-implementation-plan §14).
//
// Contract (v1 — deliberately minimal):
//
//   - OFF by default. Enabled ONLY when the operator sets
//     KUBE_DC_TELEMETRY=1 (exactly "1"; anything else is off).
//   - Counters only, no PII: keys are command paths ("bootstrap init")
//     and screen names — NEVER arguments, flag values, domains,
//     cluster names, or hostnames. Callers must pass static labels.
//   - Nothing leaves the machine. The v1 sink is a local JSON counter
//     file under ~/.kube-dc/telemetry/counters.json that the operator
//     can inspect (or delete) at any time. A future version MAY add an
//     opt-in exporter; that lands as a separate, documented decision —
//     this package existing is not consent to network transmission.
//   - Best-effort: counting never fails the caller. Unwritable HOME,
//     malformed prior file, races — all degrade to a silent no-op.
//     A CLI must never break because its usage counter couldn't write.
package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// envVar is the opt-in switch. Documented value: "1".
const envVar = "KUBE_DC_TELEMETRY"

// countersRelPath is the sink location under $HOME.
const countersRelPath = ".kube-dc/telemetry/counters.json"

// Enabled reports whether the operator opted in.
func Enabled() bool {
	return os.Getenv(envVar) == "1"
}

// Count increments the named counter in the local sink. No-op when
// telemetry is disabled; best-effort when enabled (all errors are
// swallowed — see the package contract).
//
// `event` MUST be a static label (command path / screen name). Never
// interpolate user input, arguments, or resolved values into it.
func Count(event string) {
	if !Enabled() || event == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, countersRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	counters := map[string]int64{}
	if body, err := os.ReadFile(path); err == nil {
		// Malformed prior content → start fresh rather than fail.
		_ = json.Unmarshal(body, &counters)
	}
	counters[event]++

	body, err := json.MarshalIndent(counters, "", "  ")
	if err != nil {
		return
	}
	// Plain write (not atomic): a torn counter file costs nothing —
	// the next Count starts fresh via the lenient Unmarshal above.
	_ = os.WriteFile(path, body, 0o644)
}
