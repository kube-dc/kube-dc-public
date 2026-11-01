// M4-T07 — auto-install prerequisites.
//
// Reads the doctor's tool-probe surface (M1-T01), identifies missing
// binaries, and runs `scripts/install-prerequisites.sh` from the
// fleet repo via ScriptRunner to install them. Then re-probes to
// confirm everything landed.
//
// Design decisions:
//
//   - **Detection reuses M1-T01 probes.** No parallel "which tools"
//     enumeration — the tool set the CLI cares about is exactly
//     what the doctor already knows, so drift between "detected"
//     and "documented" is impossible.
//
//   - **Install path is fleet-owned.** The actual install commands
//     (apt-get / dnf / brew / pacman detection + package names)
//     live in `scripts/install-prerequisites.sh` in the fleet
//     repo. This engine is a thin wrapper: enumerate, prompt,
//     shell out via ScriptRunner, re-probe. Adding a new
//     supported platform means editing the script, not this
//     engine.
//
//   - **Consent-gated.** The engine refuses to run unless the
//     operator opts in via `Assume` (true when --yes is set) OR
//     the caller collects a positive Prompt response. Non-
//     interactive callers who forgot --yes get
//     ErrInstallPrereqsConsentRequired.
//
//   - **Idempotent by design.** If no tools are missing, the
//     engine returns nil without touching ScriptRunner — cheap
//     re-runs (e.g. init calling this on every apply) don't cost
//     anything.
//
// Scope: engine + standalone `bootstrap install-prereqs` subcommand
// this slice. Init wire-in (`init` runs install-prereqs at the top
// of runApplyEngine unless --no-install-prereqs) is a follow-up
// commit so the two concerns can be reviewed independently.

package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// InstallPrereqsOptions is the parameter bundle for InstallPrereqs.
type InstallPrereqsOptions struct {
	// Runner shells out to `scripts/install-prerequisites.sh`. Real
	// flow wires the script adapter from bootstrap.Session; tests
	// inject a fake.
	Runner ports.ScriptRunner

	// Probes lists the tool probes to check. When nil, the engine
	// uses `discover.AllToolProbes()` — the same set the doctor
	// enumerates. Tests inject a shorter list to stay fast.
	Probes []ports.Probe

	// Assume is true when the caller already knows the operator
	// consented (`--yes` on the cobra layer). When false, the
	// engine collects consent via Prompt (below); if Prompt is
	// nil too, refuses with ErrInstallPrereqsConsentRequired.
	Assume bool

	// Prompt collects operator consent when Assume is false and
	// missing tools are detected. Returns true if the operator
	// confirmed (Enter / y / yes), false otherwise. Nil is safe
	// (same as returning false).
	Prompt func(missing []string) bool

	// Out is the operator-facing log writer. Nil is safe.
	Out io.Writer

	// FleetRepo, when its starter carries scripts/tool-versions.json, makes
	// the engine enforce those exact, checksummed tool versions: missing or
	// different versions are installed into ToolsDir, which UsePinnedTools
	// puts first on PATH. Presence alone is not enough: the Fleet scripts
	// refuse other versions (CloudFerro rehearsal, 2026-10-31).
	FleetRepo string
	// ToolsDir defaults to PinnedToolsDir().
	ToolsDir string
}

// PinnedToolsDir is where the CLI installs the starter's pinned tools. It is
// owned by the CLI, so a shared workstation's system tools stay unchanged.
func PinnedToolsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".kube-dc", "tools", "bin")
}

// UsePinnedTools puts dir first on this process's PATH when it exists. Scripts
// the CLI runs inherit that PATH.
func UsePinnedTools(dir string) {
	if dir == "" {
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	path := os.Getenv("PATH")
	if path == dir || strings.HasPrefix(path, dir+string(os.PathListSeparator)) {
		return
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
}

// ensurePinnedTools installs the starter's locked tool versions when the
// workstation lacks them. It reports false when the starter has no lock.
func ensurePinnedTools(ctx context.Context, opts InstallPrereqsOptions, out io.Writer) (bool, error) {
	if opts.FleetRepo == "" || opts.Runner == nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(opts.FleetRepo, "scripts", "tool-versions.json")); err != nil {
		return false, nil
	}
	dir := opts.ToolsDir
	if dir == "" {
		dir = PinnedToolsDir()
	}
	if dir == "" {
		return true, fmt.Errorf("init: install-prereqs: cannot determine the home directory for pinned tools")
	}
	UsePinnedTools(dir)
	run := func(args ...string) (int, error) {
		lines, err := opts.Runner.Run(ctx, ports.ScriptInstallPrereqs, nil, args...)
		if err != nil {
			return 0, fmt.Errorf("init: install-prereqs: run script: %w", err)
		}
		return ports.Drain(lines, func(line ports.Line) { fmt.Fprintln(out, line.Text) })
	}
	code, err := run("--check")
	if err != nil {
		return true, err
	}
	if code == 0 {
		fmt.Fprintln(out, "[install-prereqs] pinned tool versions present")
		return true, nil
	}
	consent := opts.Assume
	if !consent && opts.Prompt != nil {
		consent = opts.Prompt([]string{"pinned tool versions"})
	}
	if !consent {
		return true, fmt.Errorf("%w (pinned tool versions differ)", ErrInstallPrereqsConsentRequired)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true, fmt.Errorf("init: install-prereqs: create %s: %w", dir, err)
	}
	UsePinnedTools(dir)
	fmt.Fprintf(out, "[install-prereqs] installing pinned tool versions into %s\n", dir)
	if code, err = run("--bin-dir", dir); err != nil {
		return true, err
	}
	if code != 0 {
		return true, fmt.Errorf("init: install-prereqs: pinned tool installation exited %d", code)
	}
	if code, err = run("--check"); err != nil {
		return true, err
	}
	if code != 0 {
		return true, fmt.Errorf("%w (pinned tool versions still differ)", ErrInstallPrereqsIncomplete)
	}
	return true, nil
}

// InstallPrereqsResult is the structured summary the engine
// returns. Callers can inspect the fields for CI-friendly output.
type InstallPrereqsResult struct {
	// MissingBefore is the list of tool names that were missing at
	// the start (sorted alphabetically for stable output).
	MissingBefore []string

	// InstallRan is true when the engine actually invoked
	// ScriptRunner (i.e., there were missing tools AND the operator
	// consented). False when everything was already installed.
	InstallRan bool

	// MissingAfter is the list still missing after the install
	// attempt. Empty on the happy path; non-empty means the script
	// couldn't install everything (rare, but possible on unusual
	// platforms). Sorted alphabetically.
	MissingAfter []string
}

// --- Errors ---

// ErrInstallPrereqsMissingDependency surfaces on nil Runner.
var ErrInstallPrereqsMissingDependency = errors.New("init: install-prereqs missing required dependency")

// ErrInstallPrereqsConsentRequired signals the engine detected
// missing tools but neither Assume nor a positive Prompt response
// was available. Distinct from a generic error so the cobra layer
// can suggest `--yes` in the wrap.
var ErrInstallPrereqsConsentRequired = errors.New("init: install-prereqs consent required (pass --yes or accept the interactive prompt)")

// ErrInstallPrereqsIncomplete surfaces when the install script ran
// successfully but some tools are still missing on re-probe. The
// remaining names are in the wrapped error's message for triage.
var ErrInstallPrereqsIncomplete = errors.New("init: install-prereqs completed but some tools are still missing")

// --- Engine ---

// InstallPrereqs walks the tool probes, identifies missing binaries,
// and runs `scripts/install-prerequisites.sh` when needed. Returns a
// structured result + typed error for programmatic callers.
//
// Never mutates state when no tools are missing (cheap re-run
// contract). Consent-gated: refuses to shell out unless
// `opts.Assume` is true OR `opts.Prompt(missing)` returns true.
//
// **Order matters** — probe FIRST, then check for a Runner. The
// all-present no-op path MUST NOT require a ScriptRunner (that's
// the whole point of "cheap re-run" — an operator without a fleet
// checkout can still verify their laptop is clean).
func InstallPrereqs(ctx context.Context, opts InstallPrereqsOptions) (*InstallPrereqsResult, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	probes := opts.Probes
	if probes == nil {
		probes = discover.AllToolProbes()
	}

	res := &InstallPrereqsResult{}
	if _, err := ensurePinnedTools(ctx, opts, out); err != nil {
		return res, err
	}
	res.MissingBefore = detectMissingTools(ctx, probes)
	if len(res.MissingBefore) == 0 {
		fmt.Fprintln(out, "[install-prereqs] all tools present; nothing to do")
		return res, nil
	}

	// Missing tools detected → we need a Runner to fix. Check now
	// (rather than at the top) so the no-op path above stays
	// Runner-independent.
	if opts.Runner == nil {
		return res, fmt.Errorf("%w: Runner (missing tools detected: %v)",
			ErrInstallPrereqsMissingDependency, res.MissingBefore)
	}

	fmt.Fprintf(out, "[install-prereqs] missing tools: %v\n", res.MissingBefore)

	// Consent gate.
	consent := opts.Assume
	if !consent && opts.Prompt != nil {
		consent = opts.Prompt(res.MissingBefore)
	}
	if !consent {
		return res, fmt.Errorf("%w (missing: %v)",
			ErrInstallPrereqsConsentRequired, res.MissingBefore)
	}

	// Shell out to the fleet script. ScriptInstallPrereqs is the
	// canonical name (see ports/script.go); the adapter resolves it
	// to `<fleet>/scripts/install-prerequisites.sh` at runtime.
	// Drain the Line channel until close so the adapter doesn't
	// leak the underlying process; stream the script's stdout to
	// the operator's log so they see progress as apt-get / brew
	// install churns.
	fmt.Fprintln(out, "[install-prereqs] running scripts/install-prerequisites.sh")
	res.InstallRan = true
	lines, err := opts.Runner.Run(ctx, ports.ScriptInstallPrereqs, nil)
	if err != nil {
		return res, fmt.Errorf("init: install-prereqs: run script: %w", err)
	}
	// ports.Drain enforces the ScriptRunner contract for every engine.
	// This loop used to default exitCode to 0 and parse with
	// fmt.Sscanf("%d"), so a stream that ended without an exit record
	// read as success and "0garbage" parsed as a clean 0. The re-probe
	// below narrows the blast radius but does not enforce the contract:
	// it catches "the tool is missing", not "we never learned how the
	// installer ended".
	exitCode, derr := ports.Drain(lines, func(line ports.Line) {
		fmt.Fprintln(out, line.Text)
	})
	if derr != nil {
		return res, fmt.Errorf("init: install-prereqs: %w", derr)
	}
	if exitCode != 0 {
		return res, fmt.Errorf("init: install-prereqs: script exited %d", exitCode)
	}

	// Re-probe to confirm the install landed. A partial success
	// (script exits 0 but the tool didn't actually install) would
	// otherwise slip past the doctor gate downstream; catching it
	// here gives a clearer signal.
	res.MissingAfter = detectMissingTools(ctx, probes)
	if len(res.MissingAfter) > 0 {
		return res, fmt.Errorf("%w (still missing: %v)",
			ErrInstallPrereqsIncomplete, res.MissingAfter)
	}
	fmt.Fprintln(out, "[install-prereqs] all tools now present")
	return res, nil
}

// detectMissingTools returns missing required tools. Informational probes
// such as the local bao CLI must not trigger installation: the Fleet script
// does not install them, and the selected operation does not need them.
func detectMissingTools(ctx context.Context, probes []ports.Probe) []string {
	var missing []string
	for _, p := range probes {
		r := p.Run(ctx)
		if r.Status == ports.StatusMissing && r.Severity == ports.SeverityBlocker {
			missing = append(missing, p.Name())
		}
	}
	sort.Strings(missing)
	return missing
}
