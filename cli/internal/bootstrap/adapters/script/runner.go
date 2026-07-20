// Package script is the real ports.ScriptRunner adapter. It executes
// fleet-repo bash scripts via os/exec and streams their output as
// typed Line messages. This is the v1 backend for every fleet-script-
// driven step in `init` and `openbao` — per agent rule 5 of the
// installer plan.
//
// **Sentinel-bracketed payload capture (agent rule 7):** when a script
// emits a sentinel-delimited block (canonically `openbao-init.sh`
// wrapping the 5 Shamir shares + root token between
// `KUBE_DC_INIT_JSON_BEGIN` and `KUBE_DC_INIT_JSON_END` markers), the
// runner diverts the enclosed bytes to a registered callback. The raw
// payload NEVER enters the channel that drives the log file or TUI
// viewport — the only thing emitted to the regular stream is a
// placeholder. The capture buffer is a `[]byte` (not a string) so
// callers wiring it to `secrets.Buffer` can scrub the underlying
// bytes; this adapter zeroes its internal buffer after the callback
// returns regardless. M5-T01 wires the callback to a secrets.Buffer.
//
// **Process lifecycle (M0-T06 review-pass):**
//   - StdoutPipe + StderrPipe are read by dedicated goroutines.
//   - The caller's context cancel propagates as SIGTERM via
//     `cmd.Cancel`; the runtime then enforces a 5s grace via
//     `cmd.WaitDelay` before SIGKILL. This is the ONLY cancellation
//     owner — supervise just observes `cmd.Wait()` returning and
//     reads `ctx.Err()` to decide whether to emit exit 130. The
//     earlier hand-rolled "select on ctx.Done() then signal" path
//     raced against `exec.CommandContext`'s default kill behaviour.
//   - The output channel is closed exactly once, after the process
//     exits AND both stdout/stderr drain goroutines complete.
//   - The last emitted Line is always StreamExit with the decimal
//     exit code (or a synthetic "130" for ctx cancellation, "1" for
//     a sentinel-callback failure).
//
// **Script registry:** v1 ships a built-in map from ScriptKind →
// (root, path) so scripts that live in the kube-dc repo (`hack/`)
// don't have to be vendored into the fleet repo. The kubeDCRepo arg
// to New() may be empty — only scripts whose registry entry uses
// RootKubeDC need it, and the only such script in v1 is
// openbao-setup-controller-auth.sh. M3-T03 will rebuild this as a
// richer registry with required-env validation.
package script

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// killGrace is the window between SIGTERM (sent by cmd.Cancel) and
// SIGKILL (enforced by cmd.WaitDelay). 5 seconds matches the contract
// in ports/script.go and gives bash traps time to run cleanup.
const killGrace = 5 * time.Second

// channelBuffer is the capacity of the per-Run Line channel. Sized
// generously so a momentarily slow consumer (the TUI redrawing) doesn't
// stall the script's stdout drain; if it does fill up, we block the
// drain rather than dropping lines (script output is authoritative).
const channelBuffer = 64

// ScriptRoot picks which on-disk root the registry path resolves
// against. The fleet repo holds operator-supplied bootstrap scripts;
// the kube-dc repo holds developer-side helpers like
// hack/openbao-setup-controller-auth.sh that the production install
// drives but doesn't vendor.
type ScriptRoot int

const (
	// RootFleet resolves against the fleet repo (kube-dc-fleet).
	RootFleet ScriptRoot = iota
	// RootKubeDC resolves against the kube-dc repo (this monorepo).
	// Used by openbao-setup-controller-auth.sh and any future
	// developer-tooling scripts the production CLI drives.
	RootKubeDC
)

// scriptLocation pairs the root and the path-within-root for a single
// ScriptKind. Adding a script means adding a ScriptKind in
// `ports/script.go` AND a row here.
type scriptLocation struct {
	root ScriptRoot
	path string
}

// scriptPaths is the v1 built-in registry.
var scriptPaths = map[ports.ScriptKind]scriptLocation{
	ports.ScriptInstallServer:              {RootFleet, "bootstrap/rke2/install-server.sh"},
	ports.ScriptInstallAgent:               {RootFleet, "bootstrap/rke2/install-agent.sh"},
	ports.ScriptGenerateAgeKey:             {RootFleet, "bootstrap/generate-age-key.sh"},
	ports.ScriptAddCluster:                 {RootFleet, "bootstrap/add-cluster.sh"},
	ports.ScriptFluxInstall:                {RootFleet, "bootstrap/flux-install.sh"},
	ports.ScriptOpenBaoInit:                {RootFleet, "bootstrap/openbao-init.sh"},
	ports.ScriptSetupKeycloakOIDC:          {RootFleet, "bootstrap/setup-keycloak-oidc.sh"},
	ports.ScriptOpenBaoSetupControllerAuth: {RootKubeDC, "hack/openbao-setup-controller-auth.sh"},
	ports.ScriptAddEngineer:                {RootFleet, "bootstrap/add-engineer.sh"},
	ports.ScriptInstallPrereqs:             {RootFleet, "scripts/install-prerequisites.sh"},
	ports.ScriptDumpClusterState:           {RootFleet, "scripts/dump-cluster-state.sh"},
}

// sentinelMarkers maps ScriptKind to the (BEGIN, END) marker pair the
// runner watches for in stdout. A nil entry means "no sentinel for
// this script" — the only sentinel-emitting script in v1 is
// openbao-init.sh.
var sentinelMarkers = map[ports.ScriptKind][2]string{
	ports.ScriptOpenBaoInit: {"KUBE_DC_INIT_JSON_BEGIN", "KUBE_DC_INIT_JSON_END"},
}

// Runner implements ports.ScriptRunner.
type Runner struct {
	fleetRepo  string
	kubeDCRepo string // optional; required only for ScriptRoot=RootKubeDC entries
	cb         ports.SentinelCallback

	// cmdFactory builds the *exec.Cmd to invoke. Production builds it
	// from the script path resolved via scriptPaths; tests override
	// with /bin/sh -c '...' stand-ins for hermetic coverage.
	cmdFactory func(ctx context.Context, name ports.ScriptKind, env map[string]string, args []string) (*exec.Cmd, error)
}

// New returns a Runner that resolves scripts relative to the supplied
// roots and routes any sentinel-captured payload through cb. cb may be
// nil — in that case captured payloads are silently dropped (matches
// the "never emit raw" contract; the placeholder line still goes out
// so the operator sees something happened).
//
// kubeDCRepo may be empty. It's only required for scripts whose
// registry entry uses RootKubeDC (currently just
// openbao-setup-controller-auth.sh).
func New(fleetRepo, kubeDCRepo string, cb ports.SentinelCallback) *Runner {
	r := &Runner{
		fleetRepo:  fleetRepo,
		kubeDCRepo: kubeDCRepo,
		cb:         cb,
	}
	r.cmdFactory = r.defaultCmdFactory
	return r
}

// WithSentinelCallback returns a copy of r with the callback replaced.
// M5-T01 uses this to attach the share-capture callback for the single
// openbao-init.sh invocation while leaving the surrounding session
// unchanged. Returns the ports.ScriptRunner interface so callers
// holding the port type can chain without a type assertion.
func (r *Runner) WithSentinelCallback(cb ports.SentinelCallback) ports.ScriptRunner {
	cp := *r
	cp.cb = cb
	return &cp
}

// Run implements ports.ScriptRunner.Run.
func (r *Runner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	cmd, err := r.cmdFactory(ctx, name, env, args)
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("script: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("script: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("script: start %s: %w", name, err)
	}

	out := make(chan ports.Line, channelBuffer)
	go r.supervise(ctx, name, cmd, stdout, stderr, out)
	return out, nil
}

// defaultCmdFactory resolves the ScriptKind to a script path and
// builds an *exec.Cmd with the supplied env + args. Returns
// ports.ErrUnknownScript when the kind isn't registered.
//
// Configures `cmd.Cancel` to send SIGTERM and `cmd.WaitDelay` to the
// 5s grace constant so the runtime owns cancellation atomically
// rather than racing against a hand-rolled signal/kill in supervise.
func (r *Runner) defaultCmdFactory(ctx context.Context, name ports.ScriptKind, env map[string]string, args []string) (*exec.Cmd, error) {
	loc, ok := scriptPaths[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ports.ErrUnknownScript, name)
	}

	root, err := r.rootPath(loc.root)
	if err != nil {
		return nil, fmt.Errorf("script %s: %w", name, err)
	}

	// M3-T03: validate the per-kind contract (required env keys,
	// positional-arg bounds) BEFORE the exec.Cmd builds. Refusing
	// here surfaces the canonical ports.ErrMissingRequiredEnv /
	// ErrInvalidArgCount sentinel to engine-package callers rather
	// than a bash-side "missing GITHUB_TOKEN" message half-way
	// through a script. Validation runs against the call env merged
	// with the host-env passthrough allowlist, matching the effective
	// env the script will actually see.
	//
	// Ordered AFTER the repo resolution above so a misconfigured CLI
	// (no fleet repo / no kube-dc repo) surfaces its structural error
	// first — operators fix that one knob and the contract validation
	// only matters once the repos resolve.
	if err := validateContract(name, env, args); err != nil {
		return nil, err
	}

	path := filepath.Join(root, loc.path)

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = mergeEnv(env)
	cmd.Dir = root
	// Process-group setup + ctx-cancel signaling are POSIX-only
	// (Setpgid / syscall.Kill broke the windows cross-compile in the
	// v0.5.0 GoReleaser run) — platform seam in procgroup_{unix,windows}.go.
	configureProcessGroup(cmd)
	cmd.WaitDelay = killGrace
	return cmd, nil
}

// rootPath resolves a ScriptRoot to its on-disk path or surfaces a
// clear "not configured" error.
func (r *Runner) rootPath(root ScriptRoot) (string, error) {
	switch root {
	case RootFleet:
		if r.fleetRepo == "" {
			return "", fmt.Errorf("no fleet repo configured (run with --repo or set KUBE_DC_FLEET)")
		}
		return r.fleetRepo, nil
	case RootKubeDC:
		if r.kubeDCRepo == "" {
			return "", fmt.Errorf("no kube-dc repo configured (this script lives in the kube-dc monorepo's hack/ — supply --kube-dc-repo or set KUBE_DC_REPO)")
		}
		return r.kubeDCRepo, nil
	default:
		return "", fmt.Errorf("unknown script root %d (internal bug)", root)
	}
}

// mergeEnv builds the env slice exec.Cmd expects. We do NOT inherit
// the caller's process env wholesale — only a minimal allowlist
// (PATH, HOME, USER, LANG, LC_ALL, TERM, SSH_AUTH_SOCK, KUBECONFIG)
// is preserved, then operator-supplied entries are layered on top.
// That keeps secret-leaning host env (CI tokens, SHELL aliases) out
// of the script's context.
//
// KUBECONFIG is on the allowlist (added 2026-05-26) because every
// kubectl/flux invocation inside fleet/bootstrap/*.sh depends on
// the operator's KUBECONFIG choice. Without passthrough, scripts
// silently fell through to ~/.kube/config — which could point at a
// completely different cluster. The atlantis install hit this:
// flux-install.sh probed the production cloud cluster instead of
// the new atlantis cluster. No mutation landed (flux refused
// on a path mismatch) but the misroute was severe enough to make
// KUBECONFIG a host-env citizen on par with HOME.
//
// In v1 we pass through everything operator-supplied; M3-T03 will add
// per-kind required-env validation.
func mergeEnv(env map[string]string) []string {
	out := make([]string, 0, len(env)+9)
	for _, k := range []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM", "SSH_AUTH_SOCK", "KUBECONFIG"} {
		if v := lookupEnv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// supervise runs in its own goroutine; closes `out` once the process
// has fully terminated AND both drain goroutines have exited.
//
// **Ordering invariant**: drainers run to completion BEFORE cmd.Wait
// is called. Per os/exec docs ("incorrect to call Wait before all
// reads from the pipe have completed"), concurrent-Wait races
// against the bufio.Scanner and the runtime's pipe-close cleanup —
// in stress tests the scanner sees "file already closed" mid-stream
// and drops legitimate output lines. The orphan-child case (bash
// + `sleep & wait $!` in a trap) is handled by Setpgid + group-kill
// in defaultCmdFactory: cancelling the context signals the entire
// process group, so the sleep dies alongside bash and the pipe
// closes naturally for the drainer.
//
// Sequence:
//
//	start drainers
//	wait for either: (a) drainers all finish, (b) mid-stream sentinel error
//	if (b): kill process so the other drainer can EOF
//	wait for drainers to finish flushing
//	call cmd.Wait()  ← safe; nothing is still reading
//
// EOF-detected sentinel failures (unterminated BEGIN/END at process
// exit) are promoted from sentinelErr's buffered slot AFTER wg.Wait
// returns; without this they'd race against drainersDone.
func (r *Runner) supervise(ctx context.Context, name ports.ScriptKind, cmd *exec.Cmd, stdout, stderr io.ReadCloser, out chan<- ports.Line) {
	defer close(out)

	// stderr drainer: pipe lines straight through.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		drain(stderr, ports.StreamStderr, out)
	}()

	// stdout drainer: wraps in a sentinel filter when the script has
	// a registered marker pair.
	var sentinelErr chan error
	if _, hasSentinel := sentinelMarkers[name]; hasSentinel {
		sentinelErr = make(chan error, 1)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if sentinelErr == nil {
			drain(stdout, ports.StreamStdout, out)
			return
		}
		markers := sentinelMarkers[name]
		drainWithSentinel(stdout, name, markers[0], markers[1], r.cb, out, sentinelErr)
	}()

	drainersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(drainersDone)
	}()

	// Phase 1: wait for either a mid-stream sentinel failure (kill
	// the process so the stderr drainer can EOF) or drainers to
	// finish naturally.
	var (
		earlySentErr    error
		sentinelHandled bool
	)
	select {
	case earlySentErr = <-sentinelErr:
		_ = cmd.Process.Kill()
		<-drainersDone
		sentinelHandled = true
	case <-drainersDone:
		// Drainers finished naturally.
	}

	// Phase 2: drainers have finished reading. Safe to call Wait now —
	// no race against pipe close.
	waitErr := cmd.Wait()

	// Phase 3: pick the authoritative outcome. Sentinel errors take
	// precedence over the script's exit code (a clean exit-0 paired
	// with an unterminated BEGIN block IS a capture failure). Any
	// late signal (post-EOF, e.g. unterminated block) is still
	// buffered in sentinelErr after wg.Wait.
	sentErr := earlySentErr
	if !sentinelHandled && sentinelErr != nil {
		select {
		case sentErr = <-sentinelErr:
		default:
		}
	}

	var exitCode int
	switch {
	case sentErr != nil:
		out <- ports.Line{
			Stream: ports.StreamStderr,
			Text:   fmt.Sprintf("script: sentinel capture failed: %v", sentErr),
			Time:   time.Now(),
		}
		exitCode = 1
	default:
		exitCode = exitCodeFrom(waitErr)
		if ctx.Err() != nil {
			// ctx cancellation propagated through cmd.Cancel; surface
			// the conventional 128+SIGINT exit code regardless of what
			// the killed process actually returned.
			exitCode = 130
		}
	}

	out <- ports.Line{
		Stream: ports.StreamExit,
		Text:   strconv.Itoa(exitCode),
		Time:   time.Now(),
	}
}

// drain reads `r` line-by-line and emits each as a Line on `out`. The
// final partial line (no trailing newline) is also emitted.
func drain(r io.Reader, stream ports.LineStream, out chan<- ports.Line) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*64), 1024*1024) // up to 1 MiB per line
	for sc.Scan() {
		out <- ports.Line{Stream: stream, Text: sc.Text(), Time: time.Now()}
	}
}

// drainWithSentinel reads stdout looking for a BEGIN/END marker pair.
// Lines outside the markers pass through to `out`. Lines inside are
// buffered into a `[]byte` payload that is handed to `cb` at END time
// and never emitted. A placeholder line is emitted in place of the
// captured block. The buffer is zeroed after the callback returns (or
// on the bail-out paths) regardless of whether cb scrubbed too — the
// adapter doesn't trust callers to do it.
//
// Failure paths that signal sentinelErrCh and return:
//
//   - sc.Err() non-nil (scanner I/O or line-too-long failure)
//   - inside == true at scanner EOF (BEGIN seen but no END before
//     pipe closed — a malformed script or a process killed mid-block)
//   - callback returns non-nil
//
// In every failure case the raw payload is zeroed before the function
// returns and never reaches the regular stream channel.
func drainWithSentinel(r io.Reader, name ports.ScriptKind, begin, end string, cb ports.SentinelCallback, out chan<- ports.Line, sentinelErrCh chan<- error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*64), 1024*1024)

	var (
		inside  bool
		payload []byte
	)
	defer func() {
		zeroBytes(payload)
	}()

	signal := func(err error) {
		select {
		case sentinelErrCh <- err:
		default:
		}
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case !inside && strings.Contains(line, begin):
			inside = true
			payload = payload[:0]
		case inside && strings.Contains(line, end):
			inside = false
			placeholderBytes := len(payload)
			if cb != nil {
				if err := cb(name, begin, payload); err != nil {
					signal(err)
					return
				}
			}
			out <- ports.Line{
				Stream: ports.StreamStdout,
				Text:   fmt.Sprintf("[%s payload captured — %d bytes]", name, placeholderBytes),
				Time:   time.Now(),
			}
			zeroBytes(payload)
			payload = payload[:0]
		case inside:
			payload = append(payload, line...)
			payload = append(payload, '\n')
		default:
			out <- ports.Line{Stream: ports.StreamStdout, Text: line, Time: time.Now()}
		}
	}

	// EOF or error. "Use of closed file" is benign — it just means
	// cmd.Wait did its job and closed our end of the pipe (see the
	// supervise type-doc on why concurrent Wait is what we want). We
	// still want to check `inside` because an unterminated block at
	// that point is genuinely incomplete capture, regardless of
	// whether the pipe closed via clean EOF or runtime cleanup.
	if err := sc.Err(); err != nil && !scannerErrIsBenign(err) {
		signal(fmt.Errorf("sentinel reader: %w", err))
		return
	}
	if inside {
		signal(fmt.Errorf("unterminated sentinel block: saw %s but never %s", begin, end))
		return
	}
}

// scannerErrIsBenign reports whether a bufio.Scanner error came from
// the os/exec runtime closing our pipe (the "file already closed"
// shape), versus a real I/O failure. Closed-pipe errors during
// sentinel scanning are equivalent to a clean EOF — we still run the
// inside-flag check to detect unterminated blocks.
func scannerErrIsBenign(err error) bool {
	if err == nil {
		return true
	}
	// os.ErrClosed is the canonical signal; some platforms wrap it.
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "file already closed") ||
		strings.Contains(msg, "use of closed file")
}

// zeroBytes overwrites every byte of b with 0. Used by
// drainWithSentinel to scrub captured payloads after the callback
// returns. Safe on nil / empty slices.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// exitCodeFrom extracts the integer exit code from cmd.Wait()'s error.
// nil error → 0; *exec.ExitError → its ExitCode(); anything else → 1
// (treat as "process failed for non-exit reason").
func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// lookupEnv resolves a host env key. Indirected through a package var
// so tests can replace it with a deterministic stub instead of relying
// on the test harness's actual env. Production reads via os.Getenv.
var lookupEnv = os.Getenv
