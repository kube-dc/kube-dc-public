package script

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// shCmd builds an exec.Cmd that runs `script` via /bin/sh -c. Used by
// tests instead of writing throwaway .sh files; keeps each test self-
// contained. Sets Setpgid so cancellation tests can group-kill the
// whole tree (sh + grandchildren) — orphaned children holding stdout
// open is otherwise a real source of test hangs.
func shCmd(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// newTestRunner builds a Runner whose cmdFactory always returns the
// supplied *exec.Cmd (one-shot — re-using the Runner across multiple
// Run calls is intentionally unsupported in tests; build a fresh
// Runner per case).
func newTestRunner(t *testing.T, factory func(ctx context.Context, name ports.ScriptKind, env map[string]string, args []string) (*exec.Cmd, error)) *Runner {
	t.Helper()
	r := New("", "", nil)
	r.cmdFactory = factory
	return r
}

// collectLines drains the channel until it closes and returns the
// accumulated lines. Caps the wait at `timeout` so a hung test fails
// fast instead of triggering the global test timeout.
func collectLines(t *testing.T, ch <-chan ports.Line, timeout time.Duration) []ports.Line {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	var lines []ports.Line
	for {
		select {
		case ln, ok := <-ch:
			if !ok {
				return lines
			}
			lines = append(lines, ln)
		case <-deadline.C:
			t.Fatalf("timed out after %v; collected %d lines: %v", timeout, len(lines), lines)
			return nil
		}
	}
}

// lastExit returns the final StreamExit line. Tests assert on its
// Text (the decimal exit code).
func lastExit(t *testing.T, lines []ports.Line) ports.Line {
	t.Helper()
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Stream == ports.StreamExit {
			return lines[i]
		}
	}
	t.Fatalf("no StreamExit line; lines=%v", lines)
	return ports.Line{}
}

// streamCount filters by stream.
func streamCount(lines []ports.Line, s ports.LineStream) int {
	n := 0
	for _, ln := range lines {
		if ln.Stream == s {
			n++
		}
	}
	return n
}

func TestRun_StdoutAndStderr_Demuxed(t *testing.T) {
	r := newTestRunner(t, func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		return shCmd(ctx, `echo "out-1"; echo "err-1" >&2; echo "out-2"; exit 0`), nil
	})

	ch, err := r.Run(context.Background(), ports.ScriptInstallPrereqs, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 5*time.Second)

	if got, want := streamCount(lines, ports.StreamStdout), 2; got != want {
		t.Errorf("stdout lines: got %d want %d (%v)", got, want, lines)
	}
	if got, want := streamCount(lines, ports.StreamStderr), 1; got != want {
		t.Errorf("stderr lines: got %d want %d (%v)", got, want, lines)
	}
	if ex := lastExit(t, lines); ex.Text != "0" {
		t.Errorf("exit code = %q want 0", ex.Text)
	}
}

func TestRun_NonZeroExitCodeSurfaced(t *testing.T) {
	r := newTestRunner(t, func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		return shCmd(ctx, `echo "before-fail"; exit 42`), nil
	})

	ch, err := r.Run(context.Background(), ports.ScriptInstallPrereqs, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 5*time.Second)
	if ex := lastExit(t, lines); ex.Text != "42" {
		t.Errorf("exit code = %q want 42", ex.Text)
	}
}

func TestRun_ContextCancel_TerminatesAndEmits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newTestRunner(t, func(c context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		// A long-running sleep that should be SIGTERM'd by the runner
		// when we cancel ctx below. Setpgid (via shCmd) + group-kill
		// (via cmd.Cancel) ensures sleep dies with sh.
		cmd := shCmd(c, `echo "starting"; sleep 30; echo "should-not-print"`)
		cmd.Cancel = func() error {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second
		return cmd, nil
	})

	ch, err := r.Run(ctx, ports.ScriptInstallPrereqs, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Wait for the first line so we know the process is alive, then
	// cancel.
	first, ok := <-ch
	if !ok || first.Text != "starting" {
		t.Fatalf("expected 'starting', got %v ok=%v", first, ok)
	}
	cancel()

	lines := []ports.Line{first}
	lines = append(lines, collectLines(t, ch, 10*time.Second)...)

	ex := lastExit(t, lines)
	if ex.Text != "130" {
		t.Errorf("exit code on cancel = %q want 130", ex.Text)
	}
	// The "should-not-print" line MUST NOT appear.
	for _, ln := range lines {
		if strings.Contains(ln.Text, "should-not-print") {
			t.Errorf("post-cancel line leaked: %v", ln)
		}
	}
}

// Sentinel-bracketed payload — happy path. The raw payload MUST NOT
// reach the channel; a placeholder MUST take its place; the callback
// MUST receive the enclosed bytes.
func TestRun_SentinelHappyPath(t *testing.T) {
	var captured atomic.Pointer[[]byte]
	cb := func(name ports.ScriptKind, marker string, payload []byte) error {
		cp := append([]byte(nil), payload...)
		captured.Store(&cp)
		return nil
	}

	r := New("", "", cb)
	r.cmdFactory = func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		// Emit a normal line, then a sentinel block with the canonical
		// openbao-init.sh shape, then another normal line.
		script := `
echo "before"
echo "KUBE_DC_INIT_JSON_BEGIN"
echo '{"unseal_keys_b64":["share1","share2"],"root_token":"s.rootABC"}'
echo "KUBE_DC_INIT_JSON_END"
echo "after"
exit 0
`
		return shCmd(ctx, script), nil
	}

	ch, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 5*time.Second)

	for _, ln := range lines {
		if strings.Contains(ln.Text, "s.rootABC") || strings.Contains(ln.Text, "share1") {
			t.Errorf("raw payload leaked into stream: %q", ln.Text)
		}
	}

	// Placeholder line must exist.
	found := false
	for _, ln := range lines {
		if strings.Contains(ln.Text, "openbao-init.sh") && strings.Contains(ln.Text, "payload captured") {
			found = true
		}
	}
	if !found {
		t.Errorf("placeholder line not emitted: %v", lines)
	}

	// Callback got the payload.
	cap := captured.Load()
	if cap == nil {
		t.Fatal("callback never received payload")
	}
	if !strings.Contains(string(*cap), "s.rootABC") {
		t.Errorf("payload missing token: %q", *cap)
	}
	if !strings.Contains(string(*cap), "share1") {
		t.Errorf("payload missing shares: %q", *cap)
	}

	if ex := lastExit(t, lines); ex.Text != "0" {
		t.Errorf("exit code = %q want 0", ex.Text)
	}
}

// Sentinel callback failure — runner must kill the script, emit a
// stderr line + non-zero exit, and NEVER emit the raw payload.
func TestRun_SentinelCallbackFailure_KillsAndExitsNonZero(t *testing.T) {
	cb := func(name ports.ScriptKind, marker string, payload []byte) error {
		return errors.New("simulated capture failure")
	}

	r := New("", "", cb)
	r.cmdFactory = func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		// After END, we'd "continue" — but the runner should kill the
		// process before "should-not-print" reaches us. We add a sleep
		// to make the race deterministic.
		script := `
echo "before"
echo "KUBE_DC_INIT_JSON_BEGIN"
echo "secret-payload-DO-NOT-LEAK"
echo "KUBE_DC_INIT_JSON_END"
sleep 5
echo "should-not-print"
exit 0
`
		return shCmd(ctx, script), nil
	}

	ch, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 10*time.Second)

	for _, ln := range lines {
		if strings.Contains(ln.Text, "secret-payload-DO-NOT-LEAK") {
			t.Errorf("raw payload leaked despite callback failure: %v", ln)
		}
		if strings.Contains(ln.Text, "should-not-print") {
			t.Errorf("post-kill line emitted: %v", ln)
		}
	}

	ex := lastExit(t, lines)
	if ex.Text == "0" {
		t.Errorf("exit code = %q want non-zero on callback failure", ex.Text)
	}
}

// Sentinel with nil callback — payload is dropped, placeholder still
// emitted, process completes normally.
func TestRun_SentinelNilCallback_DropsSilently(t *testing.T) {
	r := New("", "", nil)
	r.cmdFactory = func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		script := `
echo "KUBE_DC_INIT_JSON_BEGIN"
echo "secret"
echo "KUBE_DC_INIT_JSON_END"
exit 0
`
		return shCmd(ctx, script), nil
	}

	ch, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 5*time.Second)

	for _, ln := range lines {
		if strings.Contains(ln.Text, "secret") && !strings.Contains(ln.Text, "captured") {
			t.Errorf("payload leaked with nil cb: %v", ln)
		}
	}

	if ex := lastExit(t, lines); ex.Text != "0" {
		t.Errorf("exit = %q want 0", ex.Text)
	}
}

func TestRun_UnknownScript_ReturnsErrUnknownScript(t *testing.T) {
	r := New("/tmp/fleet-that-doesnt-matter", "", nil)
	_, err := r.Run(context.Background(), ports.ScriptKind("totally-unregistered.sh"), nil)
	if !errors.Is(err, ports.ErrUnknownScript) {
		t.Fatalf("want ErrUnknownScript, got %v", err)
	}
}

func TestRun_NoFleetRepo_Rejects(t *testing.T) {
	r := New("", "", nil) // empty fleetRepo, default cmdFactory
	_, err := r.Run(context.Background(), ports.ScriptInstallPrereqs, nil)
	if err == nil {
		t.Fatal("expected error on missing fleet repo")
	}
	if !strings.Contains(err.Error(), "fleet repo") {
		t.Errorf("error should mention fleet repo: %v", err)
	}
}

// Built-in registry resolves a known kind to a path under fleetRepo.
// Verifies path composition; we don't actually execute the script
// (no file exists). The Run call should still launch exec.Cmd, get
// ENOENT, and surface a typed error.
func TestRun_DefaultFactory_PathComposition(t *testing.T) {
	r := New("/non/existent/fleet", "", nil)
	_, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err == nil {
		t.Fatal("expected error launching nonexistent script")
	}
	// The error message should reference the script kind so the
	// operator can correlate.
	if !strings.Contains(err.Error(), "openbao-init.sh") {
		t.Errorf("error didn't mention script name: %v", err)
	}
}

func TestWithSentinelCallback_Override(t *testing.T) {
	original := func(ports.ScriptKind, string, []byte) error { return errors.New("original") }
	override := func(ports.ScriptKind, string, []byte) error { return nil }

	r := New("/tmp/fleet", "", original)
	// WithSentinelCallback returns the ports.ScriptRunner interface
	// (M5-T01 — callers stay adapter-agnostic). Cast back to
	// *Runner to inspect the cb field for this whitebox test.
	r2Iface := r.WithSentinelCallback(override)
	r2, ok := r2Iface.(*Runner)
	if !ok {
		t.Fatalf("WithSentinelCallback returned %T, want *Runner", r2Iface)
	}
	if r.cb == nil {
		t.Error("original runner lost its cb")
	}
	if r2.cb == nil {
		t.Fatal("override didn't take")
	}
	// They MUST be distinct funcs.
	if errOriginal := r.cb(ports.ScriptOpenBaoInit, "M", nil); errOriginal == nil {
		t.Error("original cb appears swapped")
	}
	if errOverride := r2.cb(ports.ScriptOpenBaoInit, "M", nil); errOverride != nil {
		t.Errorf("override cb appears wrong: %v", errOverride)
	}
}

func TestExitCodeFrom(t *testing.T) {
	if got := exitCodeFrom(nil); got != 0 {
		t.Errorf("nil → %d want 0", got)
	}
	// Real *exec.ExitError via /bin/false.
	err := exec.Command("/bin/false").Run()
	if got := exitCodeFrom(err); got != 1 {
		t.Errorf("/bin/false → %d want 1", got)
	}
	// Non-ExitError.
	if got := exitCodeFrom(errors.New("other")); got != 1 {
		t.Errorf("non-exit error → %d want 1", got)
	}
}

func TestMergeEnv_AllowlistedHostKeys(t *testing.T) {
	// Restore lookupEnv when done.
	orig := lookupEnv
	t.Cleanup(func() { lookupEnv = orig })

	lookupEnv = func(k string) string {
		switch k {
		case "PATH":
			return "/usr/bin"
		case "HOME":
			return "/home/test"
		case "KUBECONFIG":
			// Regression atlantis 2026-05-26: without this
			// passthrough, flux-install.sh probed the wrong cluster.
			return "/home/test/.kube/atlantis_config"
		case "SHELL":
			return "/bin/zsh" // should NOT be in output (not in allowlist)
		case "GITHUB_TOKEN":
			return "ghp_xxx" // should NOT be in output
		default:
			return ""
		}
	}

	out := mergeEnv(map[string]string{"OPERATOR_VAR": "value"})

	mustHave := []string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"KUBECONFIG=/home/test/.kube/atlantis_config",
		"OPERATOR_VAR=value",
	}
	for _, want := range mustHave {
		if !contains(out, want) {
			t.Errorf("env missing %q in %v", want, out)
		}
	}
	mustNotHave := []string{"SHELL=", "GITHUB_TOKEN="}
	for _, bad := range mustNotHave {
		for _, e := range out {
			if strings.HasPrefix(e, bad) {
				t.Errorf("env leaked non-allowlisted host key %q", e)
			}
		}
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// Ensure the package-level scriptPaths registry covers every
// ScriptKind exported by ports — guards against adding a new kind and
// forgetting the path registration.
func TestScriptPaths_CoversAllExportedKinds(t *testing.T) {
	all := []ports.ScriptKind{
		ports.ScriptInstallServer,
		ports.ScriptInstallAgent,
		ports.ScriptGenerateAgeKey,
		ports.ScriptAddCluster,
		ports.ScriptFluxInstall,
		ports.ScriptOpenBaoInit,
		ports.ScriptSetupKeycloakOIDC,
		ports.ScriptOpenBaoSetupControllerAuth,
		ports.ScriptAddEngineer,
		ports.ScriptInstallPrereqs,
		ports.ScriptDumpClusterState,
	}
	for _, k := range all {
		if _, ok := scriptPaths[k]; !ok {
			t.Errorf("scriptPaths missing %s", k)
		}
	}
}

// Smoke test that the path resolves under the expected fleet layout.
func TestDefaultCmdFactory_PathLayout(t *testing.T) {
	r := New("/fleet", "", nil)
	cmd, err := r.defaultCmdFactory(context.Background(), ports.ScriptOpenBaoInit, nil, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	want := filepath.Join("/fleet", "bootstrap", "openbao-init.sh")
	if cmd.Path != want {
		t.Errorf("Path=%q want %q", cmd.Path, want)
	}
}

// --- review-pass tests for 2902fc62 / a5df1470 (M0-T06 batch 1) ---

// P1 cancellation contract: SIGTERM (via cmd.Cancel) must reach the
// process BEFORE SIGKILL, giving bash traps the grace period. Without
// the cmd.Cancel/WaitDelay fix, exec.CommandContext would send
// SIGKILL immediately and the trap would never run.
//
// The script uses `sleep & wait $!` — bash defers TRAP actions until
// the current foreground command returns, but `wait` is signal-
// responsive, so the trap runs the moment SIGTERM arrives. A bare
// `sleep 30` blocks the trap and would fail this test (verified by
// removing the `&`/`wait` and watching the trap output disappear).
func TestRun_ContextCancel_DeliversSIGTERMBeforeKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newTestRunner(t, func(c context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		script := `
trap 'echo GOT-TERM-CLEANUP-OK; exit 99' TERM
echo running
sleep 30 &
wait $!
echo should-not-print
`
		cmd := exec.CommandContext(c, "/bin/bash", "-c", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Group-kill on cancel so the trap runs AND the background
		// sleep dies in the same group — orphaned sleep would
		// otherwise keep stdout open and hang the drainer.
		cmd.Cancel = func() error {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second
		return cmd, nil
	})

	ch, err := r.Run(ctx, ports.ScriptInstallPrereqs, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := <-ch
	if first.Text != "running" {
		t.Fatalf("unexpected first line: %v", first)
	}
	cancel()

	lines := []ports.Line{first}
	lines = append(lines, collectLines(t, ch, 10*time.Second)...)

	sawTrap := false
	for _, ln := range lines {
		if strings.Contains(ln.Text, "GOT-TERM-CLEANUP-OK") {
			sawTrap = true
		}
		if strings.Contains(ln.Text, "should-not-print") {
			t.Errorf("post-cancel line leaked: %v", ln)
		}
	}
	if !sawTrap {
		t.Errorf("trap output missing — SIGTERM likely lost to SIGKILL race; lines=%v", lines)
	}
	if ex := lastExit(t, lines); ex.Text != "130" {
		t.Errorf("exit code on cancel = %q want 130", ex.Text)
	}
}

// P1 dual-root: openbao-setup-controller-auth.sh lives in the kube-dc
// repo's hack/, NOT in fleet. The registry must resolve it against
// the kubeDCRepo root.
func TestDefaultCmdFactory_OpenBaoSetupControllerAuth_ResolvesAgainstKubeDCRepo(t *testing.T) {
	r := New("/fleet", "/kube-dc", nil)
	// Satisfy the M3-T03 contract: CLUSTER + DOMAIN env are required
	// (script body has `${CLUSTER:?...}` / `${DOMAIN:?...}` guards);
	// KUBECONFIG is also required and passes via the host-env
	// passthrough allowlist (mergeEnv) or via t.Setenv below to keep
	// the test hermetic. The script takes zero positional args.
	t.Setenv("KUBECONFIG", "/dev/null/kube-dc-m3t03-test")
	env := map[string]string{"CLUSTER": "test", "DOMAIN": "test.example"}
	cmd, err := r.defaultCmdFactory(context.Background(), ports.ScriptOpenBaoSetupControllerAuth, env, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	want := filepath.Join("/kube-dc", "hack", "openbao-setup-controller-auth.sh")
	if cmd.Path != want {
		t.Errorf("Path=%q want %q", cmd.Path, want)
	}
	if cmd.Dir != "/kube-dc" {
		t.Errorf("Dir=%q want /kube-dc", cmd.Dir)
	}
}

// P1 dual-root: invoking a RootKubeDC script without configuring
// kubeDCRepo must produce a clear "configure --kube-dc-repo" error,
// not a confusing fleet-path-resolution failure.
func TestDefaultCmdFactory_RootKubeDC_NoRepo_ClearError(t *testing.T) {
	r := New("/fleet", "", nil)
	_, err := r.defaultCmdFactory(context.Background(), ports.ScriptOpenBaoSetupControllerAuth, nil, nil)
	if err == nil {
		t.Fatal("expected error when kube-dc repo unconfigured for a RootKubeDC script")
	}
	if !strings.Contains(err.Error(), "kube-dc repo") {
		t.Errorf("error should reference kube-dc repo: %v", err)
	}
}

// Fleet-root scripts still work when kubeDCRepo is empty (only
// affected scripts should require kubeDCRepo).
func TestDefaultCmdFactory_RootFleet_NoKubeDCRepo_Works(t *testing.T) {
	r := New("/fleet", "", nil)
	cmd, err := r.defaultCmdFactory(context.Background(), ports.ScriptInstallPrereqs, nil, nil)
	if err != nil {
		t.Fatalf("RootFleet should work without kubeDCRepo: %v", err)
	}
	if cmd.Path != filepath.Join("/fleet", "scripts", "install-prerequisites.sh") {
		t.Errorf("Path=%q", cmd.Path)
	}
}

// P2 sentinel scrub: the callback receives the payload as []byte. The
// runner zeroes its internal buffer after the callback returns —
// verify by capturing the slice pointer and asserting the underlying
// memory is zeroed by the time we observe the next stream line.
func TestRun_SentinelPayload_BufferZeroedAfterCallback(t *testing.T) {
	type captured struct {
		// Capture the slice itself so we can inspect the backing array
		// AFTER the runner's drainWithSentinel has zeroed it. Note:
		// this is a deliberate aliasing pattern — see the contract
		// comment on drainWithSentinel.
		slice []byte
	}
	var cap captured
	cb := func(name ports.ScriptKind, marker string, payload []byte) error {
		cap.slice = payload // alias, no copy — observe scrub after return
		return nil
	}

	r := New("", "", cb)
	r.cmdFactory = func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		script := `
echo "KUBE_DC_INIT_JSON_BEGIN"
echo "SECRET-PAYLOAD-MUST-BE-ZEROED"
echo "KUBE_DC_INIT_JSON_END"
exit 0
`
		return shCmd(ctx, script), nil
	}

	ch, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	collectLines(t, ch, 5*time.Second)

	if cap.slice == nil {
		t.Fatal("callback never fired")
	}
	// After the channel closed, the runner has zeroed its buffer. The
	// aliased slice should therefore observe zero bytes (or, if the
	// runner expanded the slice via append into a new backing array,
	// we'd still see the original bytes — that's the failure mode this
	// test guards against).
	for i, b := range cap.slice {
		if b != 0 {
			t.Errorf("byte %d not zeroed: 0x%02x (slice=%q)", i, b, cap.slice)
			break
		}
	}
}

// P2 sentinel parse failures: an unterminated BEGIN block must
// signal capture failure with a clear "unterminated sentinel" error
// AND result in a non-zero exit.
func TestRun_SentinelUnterminatedBlock_SignalsFailure(t *testing.T) {
	r := New("", "", func(ports.ScriptKind, string, []byte) error { return nil })
	r.cmdFactory = func(ctx context.Context, _ ports.ScriptKind, _ map[string]string, _ []string) (*exec.Cmd, error) {
		// Script starts a sentinel block then exits without END.
		// Pipe closes mid-block → drainWithSentinel must flag it.
		script := `
echo "before"
echo "KUBE_DC_INIT_JSON_BEGIN"
echo "incomplete payload"
exit 0
`
		return shCmd(ctx, script), nil
	}

	ch, err := r.Run(context.Background(), ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := collectLines(t, ch, 5*time.Second)

	// Should NOT see the payload line in the stream.
	for _, ln := range lines {
		if strings.Contains(ln.Text, "incomplete payload") {
			t.Errorf("payload leaked despite unterminated block: %v", ln)
		}
	}

	// Should see a stderr line surfacing the parse failure.
	sawErr := false
	for _, ln := range lines {
		if ln.Stream == ports.StreamStderr && strings.Contains(ln.Text, "unterminated sentinel") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("missing stderr line for unterminated sentinel; lines=%v", lines)
	}

	// Should exit non-zero.
	if ex := lastExit(t, lines); ex.Text == "0" {
		t.Errorf("exit=%q want non-zero on unterminated sentinel", ex.Text)
	}
}

