package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// stubProbe implements ports.Probe with a canned Result. Used to
// drive the missing-tool detection deterministically without
// spawning real subprocesses.
type stubProbe struct {
	name   string
	result ports.Result
}

func (p *stubProbe) Name() string                       { return p.name }
func (p *stubProbe) Run(_ context.Context) ports.Result { return p.result }

// fakePrereqRunner records ScriptRunner.Run calls + emits a
// canned Line stream (stdout lines + a StreamExit terminator).
// Never leaks the underlying channel.
type fakePrereqRunner struct {
	// canned
	lines    []ports.Line
	exitCode int
	runErr   error
	// dropExit omits the terminal exit record, modelling a script that
	// was killed or a runner that died mid-run.
	dropExit bool
	// exitText overrides the exit record's text verbatim (e.g. a
	// malformed code). Ignored when dropExit is set.
	exitText string

	// captured
	calls    int
	lastName ports.ScriptKind
	lastEnv  map[string]string
}

func (f *fakePrereqRunner) Run(_ context.Context, name ports.ScriptKind, env map[string]string, _ ...string) (<-chan ports.Line, error) {
	f.calls++
	f.lastName = name
	f.lastEnv = env
	if f.runErr != nil {
		return nil, f.runErr
	}
	ch := make(chan ports.Line, len(f.lines)+1)
	for _, l := range f.lines {
		ch <- l
	}
	// Terminator line carries the exit code. dropExit/exitText let a
	// spec drive the contract violations ports.Drain must reject.
	if !f.dropExit {
		text := f.exitText
		if text == "" {
			text = fmt.Sprintf("%d", f.exitCode)
		}
		ch <- ports.Line{Stream: ports.StreamExit, Text: text}
	}
	close(ch)
	return ch, nil
}
func (f *fakePrereqRunner) WithSentinelCallback(_ ports.SentinelCallback) ports.ScriptRunner {
	return f
}

// missing marker helper.
var (
	missingResult = ports.Result{Status: ports.StatusMissing, Severity: ports.SeverityBlocker}
	presentResult = ports.Result{Status: ports.StatusInstalled, Severity: ports.SeverityInfo}
)

func probeSet(nameToStatus map[string]ports.Result) []ports.Probe {
	out := make([]ports.Probe, 0, len(nameToStatus))
	for n, r := range nameToStatus {
		out = append(out, &stubProbe{name: n, result: r})
	}
	return out
}

// TestInstallPrereqs_AllPresent_NoOp — canonical happy path where
// every tool is already installed. Engine MUST NOT invoke the
// ScriptRunner (cheap re-run contract).
func TestInstallPrereqs_AllPresent_NoOp(t *testing.T) {
	runner := &fakePrereqRunner{}
	res, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probeSet(map[string]ports.Result{
			"kubectl": presentResult,
			"sops":    presentResult,
		}),
	})
	if err != nil {
		t.Fatalf("all-present: unexpected error %v", err)
	}
	if res.InstallRan {
		t.Errorf("InstallRan should be false when nothing is missing")
	}
	if runner.calls != 0 {
		t.Errorf("ScriptRunner should NOT be invoked when nothing is missing; calls=%d", runner.calls)
	}
	if len(res.MissingBefore) != 0 {
		t.Errorf("MissingBefore should be empty; got %v", res.MissingBefore)
	}
}

// TestInstallPrereqs_MissingWithAssumeYes_RunsAndReprobes — the
// canonical CI path: --yes is set, so consent is granted; the
// engine runs the script; re-probes report the tool is now
// present.
func TestInstallPrereqs_MissingWithAssumeYes_RunsAndReprobes(t *testing.T) {
	// First probe pass says sops is missing, second says it's
	// present. Use a mutable stub that flips after the first call.
	call := 0
	sopsProbe := &flippingProbe{name: "sops", first: missingResult, later: presentResult, callp: &call}
	probes := []ports.Probe{sopsProbe, &stubProbe{name: "kubectl", result: presentResult}}

	runner := &fakePrereqRunner{
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "[install] apt-get install sops"},
			{Stream: ports.StreamStdout, Text: "[install] done"},
		},
		exitCode: 0,
	}
	var out bytes.Buffer
	res, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probes,
		Assume: true,
		Out:    &out,
	})
	if err != nil {
		t.Fatalf("assume-yes: unexpected error %v; out:\n%s", err, out.String())
	}
	if !res.InstallRan {
		t.Errorf("InstallRan should be true when missing tools detected")
	}
	if runner.calls != 1 {
		t.Errorf("ScriptRunner should be invoked exactly once; got %d", runner.calls)
	}
	if runner.lastName != ports.ScriptInstallPrereqs {
		t.Errorf("wrong script invoked: %q", runner.lastName)
	}
	if len(res.MissingBefore) != 1 || res.MissingBefore[0] != "sops" {
		t.Errorf("MissingBefore = %v; want [sops]", res.MissingBefore)
	}
	if len(res.MissingAfter) != 0 {
		t.Errorf("MissingAfter should be empty on happy path; got %v", res.MissingAfter)
	}
	// Log stream propagates to caller.
	if !strings.Contains(out.String(), "apt-get install sops") {
		t.Errorf("script stdout not streamed to Out; got:\n%s", out.String())
	}
}

// TestInstallPrereqs_ConsentRequired_NoAssumeNoPrompt — missing
// tools + no consent → engine refuses, returns the specific
// sentinel + names the missing tools in the wrap. ScriptRunner
// MUST NOT be invoked.
func TestInstallPrereqs_ConsentRequired_NoAssumeNoPrompt(t *testing.T) {
	runner := &fakePrereqRunner{}
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probeSet(map[string]ports.Result{
			"sops": missingResult,
			"gh":   missingResult,
		}),
	})
	if err == nil {
		t.Fatal("expected consent-required error")
	}
	if !errors.Is(err, ErrInstallPrereqsConsentRequired) {
		t.Errorf("expected ErrInstallPrereqsConsentRequired, got %v", err)
	}
	if runner.calls != 0 {
		t.Errorf("ScriptRunner MUST NOT run on consent refusal; calls=%d", runner.calls)
	}
	// Error names the missing tools so operators know what --yes would install.
	if !strings.Contains(err.Error(), "gh") || !strings.Contains(err.Error(), "sops") {
		t.Errorf("error should list missing tools, got %v", err)
	}
}

// TestInstallPrereqs_ConsentViaPromptTrue_Runs — the interactive
// path: Prompt returns true when the operator hits Enter.
func TestInstallPrereqs_ConsentViaPromptTrue_Runs(t *testing.T) {
	runner := &fakePrereqRunner{
		lines:    []ports.Line{{Stream: ports.StreamStdout, Text: "installing"}},
		exitCode: 0,
	}
	call := 0
	probes := []ports.Probe{
		&flippingProbe{name: "sops", first: missingResult, later: presentResult, callp: &call},
	}
	promptCalled := false
	res, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probes,
		Prompt: func(missing []string) bool {
			promptCalled = true
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !promptCalled {
		t.Error("Prompt was not called")
	}
	if !res.InstallRan {
		t.Error("InstallRan should be true after prompt returned true")
	}
}

// TestInstallPrereqs_ConsentViaPromptFalse_Refuses — Prompt
// returning false MUST refuse without invoking the ScriptRunner.
func TestInstallPrereqs_ConsentViaPromptFalse_Refuses(t *testing.T) {
	runner := &fakePrereqRunner{}
	probes := probeSet(map[string]ports.Result{"sops": missingResult})
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probes,
		Prompt: func(_ []string) bool { return false },
	})
	if !errors.Is(err, ErrInstallPrereqsConsentRequired) {
		t.Errorf("expected ErrInstallPrereqsConsentRequired, got %v", err)
	}
	if runner.calls != 0 {
		t.Errorf("ScriptRunner MUST NOT run on prompt=false; calls=%d", runner.calls)
	}
}

// TestInstallPrereqs_ScriptExitNonZero_Refuses — the install script
// itself failing must surface as an error (with the exit code
// visible in the message).
func TestInstallPrereqs_ScriptExitNonZero_Refuses(t *testing.T) {
	runner := &fakePrereqRunner{
		lines:    []ports.Line{{Stream: ports.StreamStdout, Text: "E: apt-get failed"}},
		exitCode: 42,
	}
	probes := probeSet(map[string]ports.Result{"sops": missingResult})
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: probes,
		Assume: true,
	})
	if err == nil {
		t.Fatal("expected script-exit error")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("expected exit code in error, got %v", err)
	}
}

// TestInstallPrereqs_ScriptSucceedsButToolsStillMissing_Refuses —
// partial-success detection: script exit 0 but re-probe still
// finds the tool missing. Surface via the specific sentinel so
// operators can triage.
func TestInstallPrereqs_ScriptSucceedsButToolsStillMissing_Refuses(t *testing.T) {
	// Probe stays "missing" on the second call — script claimed
	// success but the tool didn't actually install.
	stuckProbe := &stubProbe{name: "gh", result: missingResult}
	runner := &fakePrereqRunner{
		lines:    []ports.Line{{Stream: ports.StreamStdout, Text: "install claimed OK"}},
		exitCode: 0,
	}
	res, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner,
		Probes: []ports.Probe{stuckProbe},
		Assume: true,
	})
	if err == nil {
		t.Fatal("expected incomplete-install error")
	}
	if !errors.Is(err, ErrInstallPrereqsIncomplete) {
		t.Errorf("expected ErrInstallPrereqsIncomplete, got %v", err)
	}
	if len(res.MissingAfter) != 1 || res.MissingAfter[0] != "gh" {
		t.Errorf("MissingAfter = %v; want [gh]", res.MissingAfter)
	}
}

// TestInstallPrereqs_NilRunner_AllPresent_NoOp — reviewer P2: the
// all-present path MUST NOT require a Runner. An operator verifying
// their laptop is clean shouldn't need a fleet checkout just to
// hear "nothing missing".
func TestInstallPrereqs_NilRunner_AllPresent_NoOp(t *testing.T) {
	res, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Assume: true,
		Probes: probeSet(map[string]ports.Result{
			"kubectl": presentResult,
			"sops":    presentResult,
		}),
		// Runner is nil — MUST still succeed on the no-op path.
	})
	if err != nil {
		t.Fatalf("all-present with nil Runner should be no-op, got %v", err)
	}
	if res.InstallRan {
		t.Error("InstallRan should be false on no-op")
	}
}

// TestInstallPrereqs_MissingDependency_NilRunner_WithMissing —
// missing tools + nil Runner surfaces the wiring bug WITH the
// missing set in the wrap so the operator sees which tools would
// have been installed.
func TestInstallPrereqs_MissingDependency_NilRunner_WithMissing(t *testing.T) {
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Assume: true,
		Probes: probeSet(map[string]ports.Result{
			"sops": missingResult,
		}),
	})
	if !errors.Is(err, ErrInstallPrereqsMissingDependency) {
		t.Errorf("expected ErrInstallPrereqsMissingDependency, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "sops") {
		t.Errorf("expected missing tool name in wrap, got %v", err)
	}
}

// TestDetectMissingTools_SortsAlphabetically — stable output
// across re-runs matters for operator-facing lists + snapshot
// tests downstream.
func TestDetectMissingTools_SortsAlphabetically(t *testing.T) {
	probes := []ports.Probe{
		&stubProbe{name: "sops", result: missingResult},
		&stubProbe{name: "kubectl", result: presentResult},
		&stubProbe{name: "gh", result: missingResult},
		&stubProbe{name: "age", result: missingResult},
	}
	got := detectMissingTools(context.Background(), probes)
	want := []string{"age", "gh", "sops"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("index %d: got %q, want %q", i, got[i], name)
		}
	}
}

// ---------- helpers ----------

// flippingProbe returns `first` on the initial Run call and `later`
// on every subsequent call. Used to model "install landed so the
// tool now probes as present" between the pre- and post-install
// detectMissingTools passes.
type flippingProbe struct {
	name         string
	first, later ports.Result
	callp        *int
}

func (p *flippingProbe) Name() string { return p.name }
func (p *flippingProbe) Run(_ context.Context) ports.Result {
	*p.callp++
	if *p.callp == 1 {
		return p.first
	}
	return p.later
}

// Caller-level regressions for the ports.Drain migration.
//
// Testing the contract in ports.Drain alone is not enough: this caller
// could be reverted to its old hand-rolled loop -- which defaulted the
// exit code to 0 and parsed with fmt.Sscanf -- and every existing spec
// here would stay green, because the fake always appended a well-formed
// exit record. These drive the two shapes that loop got wrong.
//
// Both must fail the install. The post-install re-probe narrows the
// blast radius but does not cover this: it answers "is the tool present
// now", not "did we ever learn how the installer ended". A script killed
// after installing one of two tools re-probes clean for the one it
// managed to install.
func TestInstallPrereqs_TruncatedStream_FailsInstall(t *testing.T) {
	call := 0
	probes := []ports.Probe{
		&flippingProbe{name: "sops", first: missingResult, later: presentResult, callp: &call},
		&stubProbe{name: "kubectl", result: presentResult},
	}
	runner := &fakePrereqRunner{
		lines:    []ports.Line{{Stream: ports.StreamStdout, Text: "[install] apt-get install sops"}},
		dropExit: true, // killed mid-run: no terminal exit record
	}
	var out bytes.Buffer
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner, Probes: probes, Assume: true, Out: &out,
	})
	if err == nil {
		t.Fatal("a stream with no exit record must fail the install, not be synthesized as exit 0")
	}
	if !errors.Is(err, ports.ErrStreamTruncated) {
		t.Errorf("want ports.ErrStreamTruncated, got %v", err)
	}
}

func TestInstallPrereqs_MalformedExitCode_FailsInstall(t *testing.T) {
	call := 0
	probes := []ports.Probe{
		&flippingProbe{name: "sops", first: missingResult, later: presentResult, callp: &call},
		&stubProbe{name: "kubectl", result: presentResult},
	}
	// fmt.Sscanf("%d") stopped at the first non-digit and reported
	// success, so this parsed as a clean exit 0.
	runner := &fakePrereqRunner{exitText: "0garbage"}
	var out bytes.Buffer
	_, err := InstallPrereqs(context.Background(), InstallPrereqsOptions{
		Runner: runner, Probes: probes, Assume: true, Out: &out,
	})
	if err == nil {
		t.Fatal(`exit record "0garbage" must fail the install, not parse as a clean 0`)
	}
	if !errors.Is(err, ports.ErrStreamBadExitCode) {
		t.Errorf("want ports.ErrStreamBadExitCode, got %v", err)
	}
}
