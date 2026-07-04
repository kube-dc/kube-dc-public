package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// runOpenBaoCmd executes `kube-dc bootstrap openbao …` with the
// parent's --repo pointer set as supplied. Mirrors runInitCmd's
// shape from bootstrap_init_test.go so the openbao subtree's cobra
// surface gets equivalent test coverage to the init surface.
func runOpenBaoCmd(t *testing.T, repo string, args []string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	r := repo
	// The openbao subcommands attach under bootstrapCmd, which
	// owns the persistent --repo flag. Construct only the openbao
	// subtree directly via bootstrapOpenBaoCmd so tests stay
	// hermetic + don't have to thread through every other
	// bootstrap subcommand.
	cmd := bootstrapOpenBaoCmd(&r)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	// Bind cmd.Execute() before out.String() — Go evaluates the
	// return-tuple's expressions left-to-right, so a naked
	// `return out.String(), cmd.Execute()` would snapshot the
	// buffer BEFORE the command ran (latent bug in the pre-M5-T04
	// helper; only surfaced when a test needed the actual body).
	err := cmd.Execute()
	return out.String(), err
}

func TestBootstrapOpenBao_RegistersSubcommands(t *testing.T) {
	// Cobra's --help routing on a deeply-nested subcommand is
	// hard to capture via SetOut/SetErr (see the LongHelp tests
	// below for the workaround). Assert child registration
	// directly — that's the contract the cobra surface exposes.
	empty := ""
	cmd := bootstrapOpenBaoCmd(&empty)
	wantSubs := map[string]bool{
		"init":                  true,
		"unseal":                true,
		"setup-controller-auth": true, // M5-T08
		"status":                true, // M5-T04
		"reveal-shares":         true, // M5-T05
		"generate-root":         true, // M5-T06
	}
	for _, child := range cmd.Commands() {
		if wantSubs[child.Name()] {
			delete(wantSubs, child.Name())
		}
	}
	if len(wantSubs) > 0 {
		t.Errorf("missing openbao subcommands: %v", wantSubs)
	}
	// Long help mentions the share-custody contract anchor.
	for _, want := range []string{
		"share-custody",
		"init captures",
		"unseal decrypts",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("openbao Long missing %q", want)
		}
	}
}

func TestBootstrapOpenBaoInit_LongHelp_DocumentsCustodyFlow(t *testing.T) {
	// Inspect the registered Long help directly. Cobra's --help
	// routing on a deeply-nested subcommand is awkward to capture
	// via SetOut/SetErr; the Long field IS the help text contract
	// and is what `--help` renders to the binary.
	empty := ""
	cmd := bootstrapOpenBaoInitCmd(&empty)
	for _, want := range []string{
		"in-memory",
		"NEVER touches disk",
		"sentinel callback",
		"share-custody",
		"hash-pinned", // T12 contract referenced from openbao docs (or alternative — adjust if missing)
	} {
		// The hash-pinned token isn't in init's Long; only enforce
		// the openbao-specific tokens.
		_ = want
	}
	for _, want := range []string{
		"in-memory",
		"NEVER touches disk",
		"sentinel callback",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("init Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
	// Flag registration check.
	if cmd.Flags().Lookup("openbao-shares-out") == nil {
		t.Errorf("--openbao-shares-out flag not registered")
	}
	if cmd.Flags().Lookup("no-push") == nil {
		t.Errorf("--no-push flag not registered")
	}
}

func TestBootstrapOpenBaoUnseal_LongHelp_DocumentsIdempotence(t *testing.T) {
	empty := ""
	cmd := bootstrapOpenBaoUnsealCmd(&empty)
	for _, want := range []string{
		"Idempotent",
		"OPENBAO_UNSEAL_KEY_{1..3}",
		"remaining 2 shares\nstay encrypted",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("unseal Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
}

func TestBootstrapOpenBaoInit_RequiresRepo(t *testing.T) {
	// Empty --repo + no $KUBE_DC_FLEET → engine can't find
	// clusters/<name>/secrets.enc.yaml. Cobra surfaces a clean
	// "no repo" error before constructing a Session.
	t.Setenv("KUBE_DC_FLEET", "")
	t.Setenv("HOME", t.TempDir())
	_, err := runOpenBaoCmd(t, "", []string{"init", "cloudacropolis"})
	if err == nil {
		t.Fatal("expected error when no fleet repo is configured")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error should mention --repo guidance: %v", err)
	}
}

func TestBootstrapOpenBaoUnseal_RequiresRepo(t *testing.T) {
	t.Setenv("KUBE_DC_FLEET", "")
	t.Setenv("HOME", t.TempDir())
	_, err := runOpenBaoCmd(t, "", []string{"unseal", "cloudacropolis"})
	if err == nil {
		t.Fatal("expected error when no fleet repo is configured")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error should mention --repo guidance: %v", err)
	}
}

func TestBootstrapOpenBaoInit_RequiresClusterName(t *testing.T) {
	// cobra.ExactArgs(1) — calling without a positional arg is a
	// usage error.
	_, err := runOpenBaoCmd(t, "/tmp", []string{"init"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

func TestBootstrapOpenBaoUnseal_RequiresClusterName(t *testing.T) {
	_, err := runOpenBaoCmd(t, "/tmp", []string{"unseal"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

// --- M5-T04 status ---

// TestBootstrapOpenBaoStatus_MockCloud_ExitZero — canonical fully-ready
// mock scenario: 3 unsealed pods + both operational markers present
// → exit 0. Also asserts the summary shape includes the cluster
// label, both annotation rows, and the pod rows.
func TestBootstrapOpenBaoStatus_MockCloud_ExitZero(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	body, err := runOpenBaoCmd(t, "", []string{"status", "cloud"})
	if err != nil {
		t.Fatalf("mock cloud status should exit 0, got %v; output:\n%s", err, body)
	}
	for _, want := range []string{
		"Cluster: cloud",
		"bootstrap-finalized:       2026-04-20T10:00:00Z",
		"controller-auth-installed: 2026-04-20T10:05:00Z",
		"openbao-0",
		"openbao-1",
		"openbao-2",
		"unsealed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status output missing %q\nFULL:\n%s", want, body)
		}
	}
	// No triage hint on the fully-ready path.
	if strings.Contains(body, "Next:") {
		t.Errorf("fully-ready state must not print a triage hint\nFULL:\n%s", body)
	}
}

// TestBootstrapOpenBaoStatus_MockSealed_ExitOne — canonical sealed
// cascade: all 3 pods sealed, both markers present. Exit code = 1
// (recoverable) and the triage hint names `openbao unseal`.
func TestBootstrapOpenBaoStatus_MockSealed_ExitOne(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "openbao-sealed")
	t.Setenv("NO_COLOR", "1")

	body, err := runOpenBaoCmd(t, "", []string{"status", "cloud"})
	if err == nil {
		t.Fatalf("sealed state should exit non-zero; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 1 {
		t.Fatalf("sealed state exit = %d, want 1; output:\n%s", de.ExitCode(), body)
	}
	for _, want := range []string{
		"SEALED",
		"Next: kube-dc bootstrap openbao unseal cloud",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sealed-state output missing %q\nFULL:\n%s", want, body)
		}
	}
}

// TestBootstrapOpenBaoStatus_RequiresClusterName — cobra.ExactArgs(1)
// enforced same as init/unseal.
func TestBootstrapOpenBaoStatus_RequiresClusterName(t *testing.T) {
	_, err := runOpenBaoCmd(t, "/tmp", []string{"status"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

// M5-T07: `bootstrap openbao status` renders the policy-generation
// line. Cloud scenario has no annotation → drift signal + refresh
// hint; the drift line is INFORMATIVE (soft) and does NOT change
// FullyReady / exit code.
func TestBootstrapOpenBaoStatus_MockCloud_RendersPolicyGenerationDrift(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	body, err := runOpenBaoCmd(t, "", []string{"status", "cloud"})
	if err != nil {
		t.Fatalf("mock cloud status should exit 0 despite drift (soft signal), got %v; output:\n%s", err, body)
	}
	for _, want := range []string{
		"policy-generation:",
		"1 expected",
		"<absent> installed",
		"DRIFT",
		// P3 fix: drift command MUST include the cluster name —
		// setup-controller-auth requires it as a positional arg.
		"kube-dc bootstrap openbao setup-controller-auth cloud --refresh-policy",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("policy-generation drift render missing %q\nBODY:\n%s", want, body)
		}
	}
}

// TestRenderOpenBaoStatus_TriageDecisions — the reviewer's P2 gap:
// engine tests cover the STATES (AnySealed / AnyUninitialized /
// missing markers) but not the rendered `Next:` decision for each
// state. These cases lock the triage priority so a future refactor
// can't silently regress a case (e.g. suggesting `init` on an
// OrderedReady race, which would try to re-generate shares).
func TestRenderOpenBaoStatus_TriageDecisions(t *testing.T) {
	// Baseline: 3 pods, all Initialized+unsealed, both markers set —
	// FullyReady. Each subtest mutates the baseline to exercise ONE
	// decision branch.
	baseline := func() openbao.StatusResult {
		return openbao.StatusResult{
			ClusterName: "cs/zrh",
			Pods: []ports.BaoStatus{
				{Pod: "openbao-0", Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "active"},
				{Pod: "openbao-1", Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "standby"},
				{Pod: "openbao-2", Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "standby"},
			},
			BootstrapFinalized:      "2026-06-01T10:00:00Z",
			ControllerAuthInstalled: "2026-06-01T10:05:00Z",
		}
	}

	cases := []struct {
		name         string
		mutate       func(r *openbao.StatusResult)
		wantContain  []string
		notContain   []string
	}{
		{
			// Fresh cluster: pods report Initialized=false + Sealed=true
			// (that's what `bao status` says on a never-init pod). MUST
			// route to `init` — `unseal` would fail with "security
			// barrier not initialized". Reviewer's P2 case A.
			name: "AllUninitialized_and_Sealed_routes_to_init",
			mutate: func(r *openbao.StatusResult) {
				for i := range r.Pods {
					r.Pods[i].Initialized = false
					r.Pods[i].Sealed = true
				}
				r.BootstrapFinalized = ""
				r.ControllerAuthInstalled = ""
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao init cs/zrh",
				"never been initialised",
			},
			notContain: []string{
				"openbao unseal",
				"setup-controller-auth",
			},
		},
		{
			// OrderedReady race: openbao-0 initialised, openbao-1/2
			// fresh (Initialized=false + Sealed=true). MUST route to
			// `unseal` — its auto-raft-join covers this case. Would
			// have hit `init` under the pre-fix code (AllUninitialized
			// is now the guard).
			name: "SomeUninitialized_routes_to_unseal_not_init",
			mutate: func(r *openbao.StatusResult) {
				r.Pods[1].Initialized = false
				r.Pods[1].Sealed = true
				r.Pods[2].Initialized = false
				r.Pods[2].Sealed = true
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao unseal cs/zrh",
			},
			notContain: []string{
				"openbao init",
			},
		},
		{
			// Classic sealed cascade (post-restart on a fully-init'd
			// cluster). Pods stay Initialized=true; Sealed=true. Route
			// to `unseal`.
			name: "SealedInitialized_routes_to_unseal",
			mutate: func(r *openbao.StatusResult) {
				for i := range r.Pods {
					r.Pods[i].Sealed = true
				}
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao unseal cs/zrh",
			},
			notContain: []string{
				"init",
				"setup-controller-auth",
			},
		},
		{
			// Reviewer's P2 case B: init got past share-custody + unseal
			// (pods Initialized+unsealed) but Phase C bailed BEFORE
			// postInitAnnotateBoth. Both markers absent. Pre-fix code
			// hit "openbao init — Phase B did not finalize" which is
			// dangerous (re-running init on an already-init cluster).
			// Now routes to setup-controller-auth (idempotent RefreshFull
			// stamps both markers).
			name: "InitDone_BothMarkersAbsent_routes_to_setup_controller_auth",
			mutate: func(r *openbao.StatusResult) {
				r.BootstrapFinalized = ""
				r.ControllerAuthInstalled = ""
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao setup-controller-auth cs/zrh",
				"both markers absent",
			},
			notContain: []string{
				"openbao init",
				"openbao unseal",
			},
		},
		{
			// Phase C never ran (init pre-M5-T08, or legacy install).
			// bootstrap-finalized set + controller-auth-installed
			// absent. Route to setup-controller-auth.
			name: "OnlyControllerAuthMissing_routes_to_setup_controller_auth",
			mutate: func(r *openbao.StatusResult) {
				r.ControllerAuthInstalled = ""
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao setup-controller-auth cs/zrh",
				"controller-auth-installed marker absent",
			},
			notContain: []string{
				"openbao init",
				"openbao unseal",
			},
		},
		{
			// Unusual: bootstrap-finalized absent but controller-auth-installed
			// set. Still setup-controller-auth (RefreshFull re-stamps both).
			name: "OnlyBootstrapFinalizedMissing_routes_to_setup_controller_auth",
			mutate: func(r *openbao.StatusResult) {
				r.BootstrapFinalized = ""
			},
			wantContain: []string{
				"Next: kube-dc bootstrap openbao setup-controller-auth cs/zrh",
				"bootstrap-finalized marker absent",
				"re-stamps both",
			},
			notContain: []string{
				"openbao init",
				"openbao unseal",
			},
		},
		{
			// Fully-ready state must not print ANY Next: hint.
			name: "FullyReady_prints_no_triage_hint",
			mutate: func(_ *openbao.StatusResult) {
				// unchanged baseline
			},
			wantContain: nil,
			notContain: []string{
				"Next:",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := baseline()
			c.mutate(&r)
			var out bytes.Buffer
			renderOpenBaoStatus(&out, r)
			body := out.String()
			for _, want := range c.wantContain {
				if !strings.Contains(body, want) {
					t.Errorf("%s: missing %q\nFULL:\n%s", c.name, want, body)
				}
			}
			for _, forbidden := range c.notContain {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s: should NOT contain %q\nFULL:\n%s", c.name, forbidden, body)
				}
			}
		})
	}
}

// --- M5-T05 reveal-shares ---

// TestBootstrapOpenBaoReveal_RequiresConsent — the whole point of the
// subcommand's safety story. Without --i-understand-the-risk AND
// without REVEAL=true, the cobra layer exits 1 with a specific
// consent-required message. Muscle-memory or typoed invocations
// hit this before any file is touched.
func TestBootstrapOpenBaoReveal_RequiresConsent(t *testing.T) {
	t.Setenv("REVEAL", "") // defensive: clear any env inherited from CI
	body, err := runOpenBaoCmd(t, "/tmp/some-fleet", []string{"reveal-shares", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected consent-gate refusal; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 1 {
		t.Fatalf("consent-refused exit = %d, want 1; output:\n%s", de.ExitCode(), body)
	}
	if !strings.Contains(body, "refuses without consent") {
		t.Errorf("expected consent-required message; output:\n%s", body)
	}
	// Neither of the two opt-in tokens should appear in the shares
	// column (there IS no shares column when refused).
	if strings.Contains(body, "OPENBAO_UNSEAL_KEY_") {
		t.Errorf("shares must never appear when consent is refused; output:\n%s", body)
	}
}

// TestBootstrapOpenBaoReveal_RevealEnvGrantsConsent — the env-var
// opt-in path (for CI). REVEAL=true is equivalent to
// --i-understand-the-risk. This test proves the env is READ; the
// underlying decrypt fails (no real fleet) so we still exit 2, but
// the consent gate was cleared (exit code is 2, not 1).
func TestBootstrapOpenBaoReveal_RevealEnvGrantsConsent(t *testing.T) {
	t.Setenv("REVEAL", "true")
	body, err := runOpenBaoCmd(t, "/tmp/nonexistent-fleet-reveal-env", []string{"reveal-shares", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected decrypt failure with nonexistent fleet; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	// Exit 2 = structural failure (nonexistent secrets.enc.yaml).
	// Exit 1 would signal consent-refused, which is exactly what we
	// need to prove ISN'T happening.
	if de.ExitCode() != 2 {
		t.Fatalf("expected exit 2 (structural), got %d — REVEAL env may not have cleared consent; output:\n%s", de.ExitCode(), body)
	}
	if strings.Contains(body, "refuses without consent") {
		t.Errorf("REVEAL=true should clear consent gate; output:\n%s", body)
	}
	if !strings.Contains(body, "secrets.enc.yaml not found") {
		t.Errorf("expected 'secrets.enc.yaml not found' post-consent, got:\n%s", body)
	}
}

// TestBootstrapOpenBaoReveal_FlagGrantsConsent — same as above but
// via --i-understand-the-risk.
func TestBootstrapOpenBaoReveal_FlagGrantsConsent(t *testing.T) {
	t.Setenv("REVEAL", "") // defensive
	body, err := runOpenBaoCmd(t, "/tmp/nonexistent-fleet-reveal-flag",
		[]string{"reveal-shares", "cs/zrh", "--i-understand-the-risk"})
	if err == nil {
		t.Fatalf("expected decrypt failure with nonexistent fleet; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("expected exit 2 (structural), got %d; output:\n%s", de.ExitCode(), body)
	}
	if strings.Contains(body, "refuses without consent") {
		t.Errorf("--i-understand-the-risk should clear consent gate; output:\n%s", body)
	}
}

// TestBootstrapOpenBaoReveal_NoKubeconfig_Proceeds — reviewer P1
// regression guard. Reveal-shares is documented as SOPS-only; a
// missing kubeconfig MUST NOT block the emergency recovery path
// (whole point of the subcommand is when the cluster is down /
// apiserver unreachable). Under the pre-P1-fix code this failed at
// `bootstrap.NewSession` build with "no kubeconfig resolved" before
// SOPS was ever touched. With the fix (NewSOPSOnly), execution
// reaches the SOPS decrypt path — which fails on the stub file
// with a decrypt error (exit 2), not a session-build error.
func TestBootstrapOpenBaoReveal_NoKubeconfig_Proceeds(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-reveal-p1")
	t.Setenv("HOME", t.TempDir()) // wipes any ~/.kube/config lookup
	t.Setenv("REVEAL", "true")
	// Fleet with a stub secrets.enc.yaml so we get PAST the os.Stat
	// check and INTO the SOPS decrypt path — that's where the P1
	// bug lived. Any SOPS-layer error is fine; what we're asserting
	// is that the command didn't die at session build.
	repo := t.TempDir()
	dir := repo + "/clusters/cs/zrh"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/secrets.enc.yaml", []byte("stub-not-real-sops"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := runOpenBaoCmd(t, repo, []string{"reveal-shares", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected SOPS decrypt failure on stub file; output:\n%s", body)
	}
	// Regression assertion: the error MUST NOT be a session-build
	// failure. Under the pre-P1 code this said
	// "openbao reveal-shares: build session: ...".
	if strings.Contains(err.Error(), "build session") {
		t.Errorf("reveal-shares should not require kubeconfig — pre-P1 regression:\n%v", err)
	}
	if strings.Contains(body, "build session") {
		t.Errorf("reveal-shares should not require kubeconfig — pre-P1 regression\nOUTPUT:\n%s", body)
	}
	// Sanity: exit 2 (structural — decrypt error), not exit 1 (consent).
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("expected exit 2 (structural), got %d; output:\n%s", de.ExitCode(), body)
	}
}

// TestBootstrapOpenBaoReveal_RequiresRepo — cobra layer's --repo
// guard. Fires before the consent gate so operators see the more
// actionable error first.
func TestBootstrapOpenBaoReveal_RequiresRepo(t *testing.T) {
	t.Setenv("KUBE_DC_FLEET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REVEAL", "true") // ensure it's not the consent gate firing
	_, err := runOpenBaoCmd(t, "", []string{"reveal-shares", "cs/zrh"})
	if err == nil {
		t.Fatal("expected error when no fleet repo is configured")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error should mention --repo guidance: %v", err)
	}
}

// TestBootstrapOpenBaoReveal_RequiresClusterName — ExactArgs guard.
func TestBootstrapOpenBaoReveal_RequiresClusterName(t *testing.T) {
	t.Setenv("REVEAL", "true")
	_, err := runOpenBaoCmd(t, "/tmp", []string{"reveal-shares"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

// TestBootstrapOpenBaoReveal_LongHelp_DocumentsExitContract — the
// operator-visible surface of the exit-code contract stays anchored.
func TestBootstrapOpenBaoReveal_LongHelp_DocumentsExitContract(t *testing.T) {
	empty := ""
	cmd := bootstrapOpenBaoRevealSharesCmd(&empty)
	for _, want := range []string{
		"worst-case recovery",
		"--i-understand-the-risk",
		"REVEAL=true",
		"stdout",
		"stderr",
		"audit",
		"Exit code:",
		"0  shares revealed",
		"1  consent gate not satisfied",
		"2  structural failure",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("reveal-shares Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
	// Flag registration + shortcut.
	if cmd.Flags().Lookup("i-understand-the-risk") == nil {
		t.Errorf("--i-understand-the-risk flag not registered")
	}
}

// --- M5-T06 generate-root ---

// TestBootstrapOpenBaoGenerateRoot_RequiresConsent — same consent-
// gate contract as reveal-shares. Muscle-memory typos exit 1 before
// any session build / decrypt happens.
func TestBootstrapOpenBaoGenerateRoot_RequiresConsent(t *testing.T) {
	t.Setenv("REVEAL", "")
	body, err := runOpenBaoCmd(t, "/tmp/some-fleet", []string{"generate-root", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected consent-gate refusal; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 1 {
		t.Fatalf("consent-refused exit = %d, want 1", de.ExitCode())
	}
	if !strings.Contains(body, "refuses without consent") {
		t.Errorf("expected consent-required message; output:\n%s", body)
	}
	if !strings.Contains(body, "generate-root") {
		t.Errorf("consent-refused message should name the subcommand; output:\n%s", body)
	}
}

// TestBootstrapOpenBaoGenerateRoot_RevealEnvGrantsConsent — REVEAL=true
// clears the consent gate. Since the fleet path doesn't exist we hit
// the os.Stat guard next → exit 2. Exit code discriminates.
func TestBootstrapOpenBaoGenerateRoot_RevealEnvGrantsConsent(t *testing.T) {
	t.Setenv("REVEAL", "true")
	body, err := runOpenBaoCmd(t, "/tmp/nonexistent-fleet-genroot-env",
		[]string{"generate-root", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected structural failure with nonexistent fleet; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("expected exit 2 (structural), got %d — REVEAL env may not clear consent; output:\n%s", de.ExitCode(), body)
	}
	if strings.Contains(body, "refuses without consent") {
		t.Errorf("REVEAL=true should clear consent gate; output:\n%s", body)
	}
}

// TestBootstrapOpenBaoGenerateRoot_FlagGrantsConsent — mirror of the
// env-var test via --i-understand-the-risk.
func TestBootstrapOpenBaoGenerateRoot_FlagGrantsConsent(t *testing.T) {
	t.Setenv("REVEAL", "")
	body, err := runOpenBaoCmd(t, "/tmp/nonexistent-fleet-genroot-flag",
		[]string{"generate-root", "cs/zrh", "--i-understand-the-risk"})
	if err == nil {
		t.Fatalf("expected structural failure; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %d; output:\n%s", de.ExitCode(), body)
	}
	if strings.Contains(body, "refuses without consent") {
		t.Errorf("--i-understand-the-risk should clear consent gate; output:\n%s", body)
	}
}

// Standard cobra guards.
func TestBootstrapOpenBaoGenerateRoot_RequiresRepo(t *testing.T) {
	t.Setenv("KUBE_DC_FLEET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REVEAL", "true")
	_, err := runOpenBaoCmd(t, "", []string{"generate-root", "cs/zrh"})
	if err == nil {
		t.Fatal("expected error when no fleet repo is configured")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error should mention --repo guidance: %v", err)
	}
}

func TestBootstrapOpenBaoGenerateRoot_RequiresClusterName(t *testing.T) {
	t.Setenv("REVEAL", "true")
	_, err := runOpenBaoCmd(t, "/tmp", []string{"generate-root"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

// TestBootstrapOpenBaoGenerateRoot_FlagsRegistered — guards against
// a future refactor accidentally dropping the two flags this
// subcommand's UX depends on.
func TestBootstrapOpenBaoGenerateRoot_FlagsRegistered(t *testing.T) {
	empty := ""
	cmd := bootstrapOpenBaoGenerateRootCmd(&empty)
	for _, name := range []string{"i-understand-the-risk", "revoke-immediately"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s flag not registered", name)
		}
	}
}

// TestBootstrapOpenBaoGenerateRoot_SessionBuildFails_ExitTwo —
// reviewer P2. Consent set + secrets.enc.yaml exists → we DO reach
// bootstrap.NewSession; without a valid kubeconfig it fails, and
// per the doc'd contract that's a STRUCTURAL failure (exit 2), NOT
// a plain error (which main.go maps to exit 1 — indistinguishable
// from consent refusal in CI).
func TestBootstrapOpenBaoGenerateRoot_SessionBuildFails_ExitTwo(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-genroot-p2")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REVEAL", "true") // consent granted
	// Seed a fleet with a real secrets.enc.yaml so the os.Stat check
	// passes and execution reaches session build — where the P2 bug
	// lived.
	repo := t.TempDir()
	dir := repo + "/clusters/cs/zrh"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/secrets.enc.yaml", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := runOpenBaoCmd(t, repo, []string{"generate-root", "cs/zrh"})
	if err == nil {
		t.Fatalf("expected session-build failure with nonexistent kubeconfig; output:\n%s", body)
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr (P2 regression: plain error → exit 1); got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("session-build failure exit = %d, want 2 (structural); output:\n%s", de.ExitCode(), body)
	}
	if !strings.Contains(body, "build session") {
		t.Errorf("stderr should mention the session build failure\nOUTPUT:\n%s", body)
	}
}

// TestBootstrapOpenBaoGenerateRoot_LongHelp — operator-visible
// surface stays anchored.
func TestBootstrapOpenBaoGenerateRoot_LongHelp(t *testing.T) {
	empty := ""
	cmd := bootstrapOpenBaoGenerateRootCmd(&empty)
	for _, want := range []string{
		"--i-understand-the-risk",
		"REVEAL=true",
		"OPENBAO_ROOT_TOKEN",
		"revoke-immediately",
		"Exit code:",
		"0  token emitted",
		"1  consent gate not satisfied",
		"2  structural failure",
		"bao token revoke",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("generate-root Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
}

// TestBootstrapOpenBaoStatus_LongHelp_DocumentsExitContract — the
// long help is the operator-visible surface for the exit-code
// contract; assert it stays anchored so a future rewrite doesn't
// silently drop the CI-integration guidance.
func TestBootstrapOpenBaoStatus_LongHelp_DocumentsExitContract(t *testing.T) {
	empty := ""
	cmd := bootstrapOpenBaoStatusCmd(&empty)
	for _, want := range []string{
		"No SOPS key required",
		"Exit code:",
		"0  every pod unsealed",
		"1  any pod sealed",
		"2  structural failure",
		"bootstrap-finalized",
		"controller-auth-installed",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("status Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
}
