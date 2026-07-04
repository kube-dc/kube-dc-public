package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// validCloudacropolisArgs returns the canonical CLI flags for the
// cloudacropolis bootstrap shape (M4-T01 acceptance). Used as the
// table baseline so each test case mutates one arg at a time.
//
// The 5 --set values are the cloud+public-vlan preset's required
// keys (M4-T04) populated from the cloudacropolis README's CGNAT +
// public-IP topology. Tests that exercise "missing required key"
// paths filter --set entries out as needed.
//
// Note: --repo is a persistent flag on the parent `bootstrap`
// command, not on `init` — `runInitCmd` injects it via the helper's
// fleetRepo pointer rather than via the args slice.
func validCloudacropolisArgs() []string {
	return []string{
		"--preset=cloud+public-vlan",
		"--mode=install",
		"--name=cloudacropolis",
		"--domain=kdc.acropolis.example.com",
		"--node-external-ip=217.117.26.52",
		"--email=ops@acropolis.example.com",
		"--fleet-mode=existing-fleet",
		"--rook-mode=rook-ceph-multi-node",
		// cloud+public-vlan preset required keys.
		"--set=EXT_NET_VLAN_ID=1103",
		"--set=EXT_NET_INTERFACE=bond0",
		"--set=EXT_PUBLIC_VLAN_ID=1100",
		"--set=EXT_PUBLIC_CIDR=217.117.26.48/29",
		"--set=EXT_PUBLIC_GATEWAY=217.117.26.49",
		"--dry-run",
		"--no-tty",
	}
}

// runInitCmd executes `bootstrap init` with a fresh tmpdir fleet
// pre-provisioned: `.sops.yaml` with a known recipient + a matching
// age key file. This is the canonical happy-path shape for the
// cloudacropolis-style tests; M4-T09 agekey enrollment check passes
// against it.
//
// Tests that need a different repo shape (no `.sops.yaml`, no key,
// a sibling already populated) use `runInitCmdWithRepo` with a
// custom path.
func runInitCmd(t *testing.T, args []string) (string, error) {
	t.Helper()
	repo := setupValidFleet(t)
	return runInitCmdWithRepo(t, repo, args)
}

// runInitCmdWithRepo lets callers control the persistent --repo
// pointer that the parent `bootstrap` cmd would normally set.
// Passing "" exercises the "no repo configured" branch of validation
// (existing-fleet's required-repo check).
//
// Caller-owned repo paths: M4-T09 agekey enrollment will fail if
// the path doesn't have a `.sops.yaml` matching the operator's
// age key — tests that exercise enrollment-failure branches set
// up the fleet shape deliberately.
func runInitCmdWithRepo(t *testing.T, repo string, args []string) (string, error) {
	t.Helper()
	// Redirect the consent-marker dir + age key default lookup
	// under tmp so dry-run runs don't pollute the operator's home
	// and the agekey precedence chain consults our test fixtures
	// only (not the operator's real ~/.config/sops/age/keys.txt).
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SOPS_AGE_KEY_FILE", "") // disable env-var fallback
	var out bytes.Buffer
	r := repo
	cmd := bootstrapInitCmd(&r)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// fixtureAgePubkey + fixtureAgeKeyBody are matched values lifted
// from the sops adapter's client_test.go so both packages exercise
// the same canonical fixtures. The pubkey corresponds to the
// secret in fixtureAgeKeyBody (canonical age-keygen output shape).
const fixtureAgePubkey = "age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu"

const fixtureAgeKeyBody = `# created: 2025-12-15T10:00:00Z
# public key: age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu
AGE-SECRET-KEY-1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
`

// setupValidFleet provisions a minimal fleet repo at tmp with the
// canonical shape M4-T09 + M4-T13 expect:
//
//   - `.sops.yaml` with `fixtureAgePubkey` as the only recipient
//   - `age.key` at the repo root with the matching secret + pubkey
//
// Returns the tmpdir path. Used by runInitCmd as the default --repo
// value for the cloudacropolis-style happy-path tests.
func setupValidFleet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// .sops.yaml — comma-separated recipient shape matching the live
	// kube-dc-fleet config.
	sopsConfig := []byte(
		"# Test fleet SOPS config\n" +
			"creation_rules:\n" +
			"  - path_regex: '\\.enc\\.yaml$'\n" +
			"    age: '" + fixtureAgePubkey + "'\n",
	)
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), sopsConfig, 0o644); err != nil {
		t.Fatalf("write .sops.yaml: %v", err)
	}
	// age.key file at fleet root — the slot-2 candidate in
	// resolveAgeKeyPath's precedence chain.
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte(fixtureAgeKeyBody), 0o600); err != nil {
		t.Fatalf("write age.key: %v", err)
	}
	// Empty clusters/ directory so discover.ListClusters returns no
	// priors without erroring on a missing dir.
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o755); err != nil {
		t.Fatalf("mkdir clusters: %v", err)
	}
	return dir
}

func TestBootstrapInit_DryRun_HappyPath(t *testing.T) {
	body, err := runInitCmd(t, validCloudacropolisArgs())
	if err != nil {
		t.Fatalf("dry-run with valid args should succeed, got %v\nout:\n%s", err, body)
	}
	// Assert the canonical plan-render shape — header, file list,
	// script list, mutations, hashes, footer. Anchored on tokens
	// that are stable across T04/T10/T13 refinements.
	for _, want := range []string{
		"DRY RUN",
		"cloudacropolis",
		"Files to write",
		"clusters/cloudacropolis/cluster-config.env",
		"Scripts to run",
		"bootstrap/flux-install.sh",
		"Cluster mutations",
		"InputHash:",
		"PlanHash:",
		"No mutations performed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q\nFULL:\n%s", want, body)
		}
	}
}

func TestBootstrapInit_DryRun_PlanFileWritten(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	args := append(validCloudacropolisArgs(), "--plan-file="+planPath)
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("dry-run with --plan-file should succeed, got %v\nout:\n%s", err, body)
	}
	if !strings.Contains(body, "Plan written to "+planPath) {
		t.Errorf("missing plan-write confirmation line\nFULL:\n%s", body)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan file not written: %v", err)
	}
	// Round-trip: LoadPlan must verify the on-disk hash.
	loaded, err := clusterinit.LoadPlan(planPath)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if loaded.ClusterName != "cloudacropolis" {
		t.Errorf("loaded ClusterName = %q, want cloudacropolis", loaded.ClusterName)
	}
}

func TestBootstrapInit_ApplyPlan_ReachesEngineAfterVerify(t *testing.T) {
	// M4-T12 wired --apply-plan to the engine. The gate ordering
	// (Validate → LoadPlan → VerifyApplyPlanInput → Apply) is
	// preserved; this test confirms cobra dispatches to the engine
	// once VerifyApplyPlanInput passes. The engine itself fails at
	// the git step since the test tmpdir isn't a real repo — same
	// principle as TestBootstrapInit_DefaultApply_ReachesEngine.
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	// Include --allow-dns-not-ready on the dry-run so it's captured
	// in the plan's InputHash; the apply-plan call must repeat it or
	// VerifyApplyPlanInput fires with ErrPlanInputDrift. The test's
	// intent is proving cobra REACHES the apply engine, not that
	// the DNS gate works (that's dnsgate_test.go). The gate itself
	// is bypassed by the flag; the apply engine then fails at the
	// git step since the tmpdir isn't a real repo.
	if _, err := runInitCmdWithRepo(t, repo, append(validCloudacropolisArgs(), "--plan-file="+planPath, "--allow-dns-not-ready")); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}

	// Same substantive flag set (including --allow-dns-not-ready) so
	// the InputHash matches.
	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = append(args, "--apply-plan="+planPath, "--yes", "--allow-dns-not-ready")
	body, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatalf("apply-plan should fail without a real git repo; out:\n%s", body)
	}
	// Engine entry markers: cobra ran LoadPlan + VerifyApplyPlanInput,
	// printed the verified-inputs line, then dispatched to runApplyEngine.
	for _, want := range []string{
		"APPLY-PLAN",
		"Inputs verified",
		"applying plan verbatim",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("apply-plan output missing %q\nFULL:\n%s", want, body)
		}
	}
	// Error path must NOT mention the gone "engine not yet
	// implemented" — confirms the engine actually ran.
	if strings.Contains(err.Error(), "engine not yet implemented") {
		t.Errorf("apply-plan still hitting the stub error; got %v", err)
	}
}

func TestBootstrapInit_ApplyPlan_DriftBlocksEngine(t *testing.T) {
	// Reviewer's T12-P2 carry-forward: prove that input drift
	// prevents the engine (Scaffold/Apply) from running. The flow:
	//   1. Dry-run with cloudacropolis flags → write plan
	//   2. Apply-plan with a DIFFERENT --set value
	//   3. VerifyApplyPlanInput must reject before runApplyEngine
	//      is called (engine markers should NOT appear in output)
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if _, err := runInitCmdWithRepo(t, repo, append(validCloudacropolisArgs(), "--plan-file="+planPath)); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}

	// Drift: bump the EXT_NET_VLAN_ID from 1103 to 999.
	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = filterFlag(args, "--set=EXT_NET_VLAN_ID=1103")
	args = append(args, "--set=EXT_NET_VLAN_ID=999", "--apply-plan="+planPath, "--yes")
	body, err := runInitCmdWithRepo(t, repo, args)
	if !errors.Is(err, clusterinit.ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift, got %v\nout:\n%s", err, body)
	}
	// Engine markers must NOT appear — drift caught BEFORE Apply
	// ran, so Scaffold/commit/push never executed.
	for _, forbidden := range []string{
		"applying plan verbatim",
		"[apply]",
		"[scaffold]",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("engine ran despite drift (contains %q):\n%s", forbidden, body)
		}
	}
}

// M6-T05: --allow-no-kubevirt-eligible participates in the input
// hash so a dry-run made with the flag doesn't apply-plan into an
// apply run without it (and vice versa). Same shape as the existing
// input-drift tests for --node-external-ip.
func TestBootstrapInit_ApplyPlan_RefusesOnAllowNoKubevirtEligibleDrift(t *testing.T) {
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	// Dry-run with the flag set.
	prep := append(validCloudacropolisArgs(), "--plan-file="+planPath, "--allow-no-kubevirt-eligible")
	if _, err := runInitCmdWithRepo(t, repo, prep); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}
	// Apply the plan WITHOUT the flag — should surface ErrPlanInputDrift.
	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = append(args, "--apply-plan="+planPath, "--yes", "--allow-dns-not-ready")
	_, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatal("input drift on --allow-no-kubevirt-eligible must be rejected, got nil err")
	}
	if !errors.Is(err, clusterinit.ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift, got %v", err)
	}
}

// M6-T05: the flag registers on the cobra command surface. Guards
// against accidental removal from the flag registration block.
func TestBootstrapInit_AllowNoKubevirtEligibleFlagRegistered(t *testing.T) {
	empty := ""
	cmd := bootstrapInitCmd(&empty)
	if cmd.Flag("allow-no-kubevirt-eligible") == nil {
		t.Fatal("--allow-no-kubevirt-eligible flag missing from `bootstrap init` — regression")
	}
}

func TestBootstrapInit_ApplyPlan_RefusesOnInputDrift(t *testing.T) {
	// Same --repo across both runs — only the substantive inputs
	// drift (the test's intent).
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if _, err := runInitCmdWithRepo(t, repo, append(validCloudacropolisArgs(), "--plan-file="+planPath)); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}
	// Mutate the inputs between dry-run and apply — different
	// --node-external-ip. VerifyApplyPlanInput must catch this and
	// surface ErrPlanInputDrift.
	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = filterFlag(args, "--node-external-ip=217.117.26.52")
	args = append(args, "--node-external-ip=217.117.26.53", "--apply-plan="+planPath, "--yes")
	_, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatal("input drift must be rejected, got nil err")
	}
	if !errors.Is(err, clusterinit.ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift, got %v", err)
	}
}

func TestBootstrapInit_DefaultApply_ReachesEngine(t *testing.T) {
	// M4-T12 wired the apply engine. Default-flow (no --dry-run +
	// --yes) now actually invokes Apply, which fails in test env
	// at the first git operation since the tmpdir fleet isn't a
	// real git repo. The point of this test is to confirm cobra
	// dispatches to runInitDefaultApply and reaches the engine —
	// not to test Apply itself (that's covered by the engine-
	// level fakeGit tests). --allow-dns-not-ready bypasses the
	// M4-T08 DNS gate so the test isn't dependent on real DNS
	// resolution of the placeholder cloudacropolis domain. The
	// M6-T05 NFD gate soft-skips on nil K8sClient (test env has
	// no kubeconfig) so it never fires here.
	args := validCloudacropolisArgs()
	args = append(filterFlag(args, "--dry-run"), "--yes", "--allow-dns-not-ready")
	body, err := runInitCmd(t, args)
	if err == nil {
		t.Fatalf("apply path should fail without a real git repo; out:\n%s", body)
	}
	// Engine entry markers — these prove cobra reached the apply
	// engine (vs the prior initEngineNotReadyErr stub).
	for _, want := range []string{
		"=== kube-dc bootstrap init — APPLY ===",
		"Built plan for cluster",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("apply output missing %q (engine not entered?):\n%s", want, body)
		}
	}
	// Error wraps the underlying git failure (no real repo).
	if !strings.Contains(err.Error(), "apply:") {
		t.Errorf("error should be wrapped from apply engine, got %v", err)
	}
}

// TestBootstrapInit_DefaultApply_DNSGateBlocksWithoutOptOut proves
// the M4-T08 gate wires end-to-end through cobra: without
// --allow-dns-not-ready, an apply against a placeholder domain
// fails with ErrDNSGateBlocked BEFORE reaching the apply engine.
// Complementary regression to the "reaches engine" test above.
func TestBootstrapInit_DefaultApply_DNSGateBlocksWithoutOptOut(t *testing.T) {
	args := validCloudacropolisArgs()
	args = append(filterFlag(args, "--dry-run"), "--yes")
	// Deliberately NO --allow-dns-not-ready — the gate should fire.
	body, err := runInitCmd(t, args)
	if err == nil {
		t.Fatalf("expected DNS gate to block; out:\n%s", body)
	}
	if !errors.Is(err, clusterinit.ErrDNSGateBlocked) {
		t.Errorf("expected ErrDNSGateBlocked, got: %v", err)
	}
	// Engine entry markers must NOT appear — the gate ran BEFORE
	// runApplyEngine handed off to clusterinit.Apply. This is the
	// whole point of the gate: fail fast, don't touch fleet or
	// cluster state.
	for _, forbidden := range []string{
		"[apply] running flux-install.sh",
		"[scaffold]",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("engine ran despite DNS gate failure (contains %q):\n%s", forbidden, body)
		}
	}
	// Operator-facing output includes the record-set + escape-hatch
	// hint so the error is actionable.
	for _, want := range []string{
		"DNS records are not in place",
		"--allow-dns-not-ready",
	} {
		if !strings.Contains(body+err.Error(), want) {
			t.Errorf("expected %q in the operator's output/error, got:\nbody: %s\nerr: %v", want, body, err)
		}
	}
}

func TestBootstrapInit_ApplyPlan_NotImplemented(t *testing.T) {
	// Apply-plan with a non-existent file errors at LoadPlan time;
	// the error chain mentions load failure, not the engine.
	args := validCloudacropolisArgs()
	args = filterFlag(args, "--dry-run")
	args = append(args, "--apply-plan=/tmp/does-not-exist-fake-plan.json")
	body, err := runInitCmd(t, args)
	if err == nil {
		t.Fatalf("apply-plan against missing file should error; out:\n%s", body)
	}
	if !strings.Contains(err.Error(), "load plan") {
		t.Errorf("error should reference load failure; got %v", err)
	}
	if !strings.Contains(body, "APPLY-PLAN") {
		t.Errorf("output should announce apply-plan flow; got:\n%s", body)
	}
}

func TestBootstrapInit_FlagConflicts(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantSub string
		wantErr error
	}{
		{
			name: "dry-run + apply-plan rejected",
			args: append(validCloudacropolisArgs(), "--apply-plan=/tmp/p.json"),
			// Validate's "mutually exclusive" error wraps ErrValidation.
			wantSub: "mutually exclusive",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name: "fleet-mode=new-repo without github-owner",
			args: replaceFlag(filterFlag(validCloudacropolisArgs(), "--fleet-mode=existing-fleet"),
				"", "--fleet-mode=new-repo"),
			wantSub: "github-owner",
			wantErr: clusterinit.ErrFleetModeNewRepo,
		},
		{
			name: "no-tty without --yes/--apply-plan/--dry-run",
			args: replaceFlag(filterFlag(validCloudacropolisArgs(), "--dry-run"), "", ""),
			// Just --no-tty and no satisfier — Validate refuses.
			wantSub: "--no-tty requires",
			wantErr: clusterinit.ErrApplyGate,
		},
		{
			name:    "bad preset",
			args:    replaceFlag(validCloudacropolisArgs(), "--preset=cloud+public-vlan", "--preset=bogus"),
			wantSub: "--preset",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad mode",
			args:    replaceFlag(validCloudacropolisArgs(), "--mode=install", "--mode=upgrade"),
			wantSub: "--mode",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad domain",
			args:    replaceFlag(validCloudacropolisArgs(), "--domain=kdc.acropolis.example.com", "--domain=localhost"),
			wantSub: "--domain",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad IP",
			args:    replaceFlag(validCloudacropolisArgs(), "--node-external-ip=217.117.26.52", "--node-external-ip=not-an-ip"),
			wantSub: "not a valid IP",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "lowercase set rejected",
			args:    append(validCloudacropolisArgs(), "--set=domain=foo.example.com"),
			wantSub: "SCREAMING_SNAKE_CASE",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "unknown addon rejected",
			args:    append(validCloudacropolisArgs(), "--addon=cilium"),
			wantSub: "not in registry",
			wantErr: clusterinit.ErrValidation,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := runInitCmd(t, tc.args)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantSub)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected errors.Is(%v), got %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestBootstrapInit_RepeatableFlags(t *testing.T) {
	// Baseline already carries the 5 --set required keys for
	// cloud+public-vlan (M4-T04). This test layers additional
	// non-required --set + --node-nic + --addon entries to
	// exercise the repeatable-flag handling.
	args := append(validCloudacropolisArgs(),
		"--set=PROM_STORAGE=50Gi",
		"--set=PROM_RETENTION=730d",
		"--node-nic=SRV5-Kub1=enp1s0",
		"--node-nic=SRV6-Kub1=enp1s0",
		"--addon=metallb",
		"--addon=velero",
	)
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("repeatable flags should validate, got %v\nout:\n%s", err, body)
	}
	// The plan render shows customInterfaces-rendering step when
	// --node-nic is set, addons in the file list, and the count of
	// nic mappings.
	for _, want := range []string{
		"2 --node-nic mapping",
		"addons overlay (metallb, velero)",
		"customInterfaces",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plan render missing %q\nFULL:\n%s", want, body)
		}
	}
}

func TestBootstrapInit_GitHubTokenNeverInOutput(t *testing.T) {
	// CRITICAL: --github-token's value must never appear in any
	// output channel. T02's plan render doesn't surface GitHub
	// fields at all (a strictly stronger no-leak posture than T01's
	// "token=<set>" placeholder).
	//
	// Test against the dry-run path which is the canonical place
	// the operator would see token data echoed back. The default
	// (engine-not-ready) flow is also expected to be redaction-safe
	// — the writeOptionsSummary helper renders `token=<set>` —
	// but with --fleet-mode=new-repo, the M4-T09 agekey gate now
	// fires before that codepath (greenfield-generate is not yet
	// implemented). The relevant security invariant is "value
	// never leaks regardless of which gate trips"; both paths
	// satisfy it.
	const fakeToken = "ghp_FAKE_TOKEN_DO_NOT_LEAK_AAAAAA"

	t.Run("dry-run path (existing-fleet)", func(t *testing.T) {
		args := append(validCloudacropolisArgs(), "--github-token="+fakeToken)
		body, err := runInitCmd(t, args)
		if err != nil {
			t.Fatalf("dry-run should pass, got %v\nout:\n%s", err, body)
		}
		if strings.Contains(body, "FAKE_TOKEN_DO_NOT_LEAK") {
			t.Fatalf("--github-token value leaked into dry-run output:\n%s", body)
		}
	})

	t.Run("new-repo dry-run path", func(t *testing.T) {
		// Even with new-repo (which currently returns
		// ErrAgeKeyGenerateNotImplemented at apply-time), the
		// dry-run path downgrades that to a warning so the plan
		// renders. The token must not appear anywhere in the
		// resulting output.
		args := append(filterFlag(validCloudacropolisArgs(), "--fleet-mode=existing-fleet"),
			"--fleet-mode=new-repo",
			"--github-owner=kube-dc",
			"--github-repo=kube-dc-fleet",
			"--github-token="+fakeToken,
		)
		body, _ := runInitCmd(t, args)
		if strings.Contains(body, "FAKE_TOKEN_DO_NOT_LEAK") {
			t.Fatalf("--github-token value leaked into new-repo output:\n%s", body)
		}
	})
}

func TestBootstrapInit_DomainCollision_Rejected(t *testing.T) {
	// M4-T13: --domain matching an existing sibling must fail
	// loudly with ErrDomainCollision. The cobra wiring runs the
	// check before BuildPlan so the error surfaces clean.
	//
	// The live ~/projects/kube-dc-fleet has cloud at kube-dc.cloud
	// — reusing that domain must be rejected.
	args := replaceFlag(validCloudacropolisArgs(),
		"--domain=kdc.acropolis.example.com",
		"--domain=kube-dc.cloud") // collides with cluster cloud
	_, err := runInitCmdWithRepo(t, "/home/voa/projects/kube-dc-fleet", args)
	if err == nil {
		t.Fatal("expected ErrDomainCollision when --domain reuses a sibling's value")
	}
	if !errors.Is(err, clusterinit.ErrDomainCollision) {
		t.Fatalf("expected ErrDomainCollision, got %v", err)
	}
	if !strings.Contains(err.Error(), "cloud") {
		t.Errorf("error should name the colliding sibling: %v", err)
	}
}

func TestBootstrapInit_InheritsFromFleetSiblings(t *testing.T) {
	// M4-T13: an existing-fleet dry-run against the live fleet must
	// surface the InheritedDefaults block in the plan render. We
	// assert on the SHAPE (presence of version-suffix keys) rather
	// than exact values so the test stays robust as the live cloud
	// cluster's pins drift.
	body, err := runInitCmdWithRepo(t, "/home/voa/projects/kube-dc-fleet", validCloudacropolisArgs())
	if err != nil {
		t.Fatalf("dry-run should succeed; got %v\nout:\n%s", err, body)
	}
	for _, want := range []string{
		"Template (most-recently-modified sibling):",
		"Inherited defaults",
		"KUBE_DC_VERSION=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inheritance render missing %q\nFULL:\n%s", want, body)
		}
	}
}

func TestBootstrapInit_AgeKey_EnrolledPasses(t *testing.T) {
	// M4-T09 happy path: the fixture fleet has the operator's
	// pubkey in .sops.yaml and a matching age.key at the repo
	// root. The dry-run prints "[sops] enrolled (pubkey=… via …)"
	// and proceeds to the plan.
	body, err := runInitCmd(t, validCloudacropolisArgs())
	if err != nil {
		t.Fatalf("enrolled dry-run should succeed, got %v\nout:\n%s", err, body)
	}
	if !strings.Contains(body, "[sops] enrolled") {
		t.Errorf("output missing enrollment confirmation\nFULL:\n%s", body)
	}
	if !strings.Contains(body, fixtureAgePubkey) {
		t.Errorf("output should mention resolved pubkey %q\nFULL:\n%s", fixtureAgePubkey, body)
	}
}

func TestBootstrapInit_AgeKey_NotEnrolled_Refuses(t *testing.T) {
	// M4-T09 sad path: operator's age key exists but the pubkey
	// is NOT in .sops.yaml. CheckAgeKeyEnrollment must refuse
	// with the typed error + the add-engineer.sh remediation.
	dir := t.TempDir()
	// .sops.yaml lists a different recipient than the operator's
	// key.
	otherPubkey := "age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm"
	sopsConfig := []byte("creation_rules:\n  - path_regex: '\\.enc\\.yaml$'\n    age: '" + otherPubkey + "'\n")
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), sopsConfig, 0o644); err != nil {
		t.Fatalf("write .sops.yaml: %v", err)
	}
	// Our operator's key points at fixtureAgePubkey — not in the
	// recipients list above.
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte(fixtureAgeKeyBody), 0o600); err != nil {
		t.Fatalf("write age.key: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o755); err != nil {
		t.Fatalf("mkdir clusters: %v", err)
	}

	// Strip --dry-run so the agekey gate fires as a hard error
	// (dry-run downgrades it to a warning).
	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = append(args, "--yes")
	_, err := runInitCmdWithRepo(t, dir, args)
	if err == nil {
		t.Fatal("expected ErrAgeKeyNotEnrolled")
	}
	if !errors.Is(err, clusterinit.ErrAgeKeyNotEnrolled) {
		t.Fatalf("expected ErrAgeKeyNotEnrolled, got %v", err)
	}
	// Remediation must be operator-actionable.
	for _, want := range []string{
		"bootstrap/add-engineer.sh",
		"--reencrypt",
		"git push",
		fixtureAgePubkey,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q\nFULL:\n%s", want, err.Error())
		}
	}
}

func TestBootstrapInit_AgeKey_DryRunDowngradesMissingKey(t *testing.T) {
	// M4-T09 dry-run downgrade: when the agekey can't be resolved
	// (no key file anywhere in the precedence chain), the dry-run
	// proceeds with a warning so operators can still review a plan
	// without being a keyholder yet.
	dir := t.TempDir()
	// .sops.yaml present but NO age key file anywhere — operator
	// hasn't enrolled yet.
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"),
		[]byte("creation_rules:\n  - age: '"+fixtureAgePubkey+"'\n"), 0o644); err != nil {
		t.Fatalf("write .sops.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o755); err != nil {
		t.Fatalf("mkdir clusters: %v", err)
	}

	body, err := runInitCmdWithRepo(t, dir, validCloudacropolisArgs())
	if err != nil {
		t.Fatalf("dry-run with missing key should warn not error, got %v\nout:\n%s", err, body)
	}
	if !strings.Contains(body, "WARNING") {
		t.Errorf("dry-run should downgrade missing-key to a WARNING line\nFULL:\n%s", body)
	}
	if !strings.Contains(body, "DRY RUN") {
		t.Errorf("dry-run should still render the plan; got:\n%s", body)
	}
}

func TestBootstrapInit_AgeKey_FleetKeyPermissionError_Surfaces(t *testing.T) {
	// Review-pass P3: when <fleet>/age.key exists but is
	// unreadable, resolveAgeKeyPath must surface the stat error
	// instead of silently skipping to a different slot — otherwise
	// the operator lands on the wrong key + gets a confusing
	// "not enrolled" diagnosis. Test by making the fleet age.key
	// unreadable via chmod 000.
	dir := setupValidFleet(t)
	keyPath := filepath.Join(dir, "age.key")
	if err := os.Chmod(keyPath, 0o000); err != nil {
		t.Skipf("can't chmod 000 (running as root?): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyPath, 0o600) })

	args := filterFlag(validCloudacropolisArgs(), "--dry-run")
	args = append(args, "--yes")
	_, err := runInitCmdWithRepo(t, dir, args)
	if err == nil {
		t.Fatal("expected permission error on unreadable fleet age.key")
	}
	// Accept any of: stat error surfaced from resolveAgeKeyPath,
	// pubkey-derive error from the sops adapter. The point is the
	// error mentions the file path so the operator can diagnose.
	if !strings.Contains(err.Error(), "age.key") {
		t.Errorf("permission error should mention the age.key path; got %v", err)
	}
}

func TestBootstrapInit_PresetEmptyRequiredValue_Rejected(t *testing.T) {
	// Review-pass P1/P2: an empty value for a required key reaches
	// the cobra layer via --set=KEY= (key present, value empty).
	// ValidatePresetValues must surface ErrPresetInvalidValue so
	// T10 doesn't write the empty value to cluster-config.env.
	args := append(filterFlag(validCloudacropolisArgs(), "--set=EXT_PUBLIC_CIDR=217.117.26.48/29"),
		"--set=EXT_PUBLIC_CIDR=")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("empty --set=EXT_PUBLIC_CIDR= must be rejected")
	}
	if !errors.Is(err, clusterinit.ErrPresetInvalidValue) {
		t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
	}
	if !strings.Contains(err.Error(), "EXT_PUBLIC_CIDR") {
		t.Errorf("error should name the offending key; got %v", err)
	}
}

func TestBootstrapInit_NodeNICValidation_RejectsShellMetachar(t *testing.T) {
	// M4-T11 review-pass — P2: --node-nic iface values reach the
	// ProviderNetwork patch on disk via M4-T11. They must pass the
	// same NIC-name sanity check as EXT_NET_INTERFACE. Cobra
	// surface relays the typed validation error.
	args := append(validCloudacropolisArgs(),
		"--node-nic=SRV5-Kub1=enp1s0;rm")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("--node-nic with shell metachar must be rejected")
	}
	if !errors.Is(err, clusterinit.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported character") {
		t.Errorf("error should explain NIC charset; got %v", err)
	}
}

func TestBootstrapInit_PresetSemanticValidation_BadVLAN(t *testing.T) {
	// Bad VLAN ID surfaces in the cobra layer too — guard against
	// a future refactor that drops ValidatePresetValues from the
	// RunE pre-flight. Uses 9999 (above the 4094 max); VLAN 0 is
	// now accepted as "untagged" (CloudSigma-style provider
	// networks where the carrier NIC IS the VLAN), so the
	// error-message check uses "0..4094" — the current accepted
	// range documented by validateVLANID.
	args := append(filterFlag(validCloudacropolisArgs(), "--set=EXT_NET_VLAN_ID=1103"),
		"--set=EXT_NET_VLAN_ID=9999")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("out-of-range VLAN must be rejected")
	}
	if !errors.Is(err, clusterinit.ErrPresetInvalidValue) {
		t.Fatalf("expected ErrPresetInvalidValue, got %v", err)
	}
	if !strings.Contains(err.Error(), "0..4094") {
		t.Errorf("error should explain VLAN range; got %v", err)
	}
}

func TestBootstrapInit_PresetMissingRequiredKey(t *testing.T) {
	// M4-T04 wired preset required-key validation. Dropping a
	// required --set entry for the chosen preset must surface
	// ErrPresetMissingRequired with the missing key name + the
	// preset name in the error message.
	args := filterFlag(validCloudacropolisArgs(), "--set=EXT_PUBLIC_VLAN_ID=1100")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("expected ErrPresetMissingRequired when --set=EXT_PUBLIC_VLAN_ID dropped")
	}
	if !errors.Is(err, clusterinit.ErrPresetMissingRequired) {
		t.Fatalf("expected ErrPresetMissingRequired, got %v", err)
	}
	for _, want := range []string{
		"EXT_PUBLIC_VLAN_ID",
		"cloud+public-vlan",
		"--set KEY=VALUE",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q (operator guidance)", err.Error(), want)
		}
	}
}

func TestBootstrapInit_ModeAuto_NoKubeconfigErrorsClearly(t *testing.T) {
	// M4-T03 wired the auto-detector. `--mode=auto` without a
	// reachable kubeconfig must error with a clear message
	// directing the operator at the remediation options (fix
	// kubeconfig OR pass --mode= explicitly).
	//
	// Hermetic kubeconfig isolation: point KUBECONFIG at a
	// guaranteed-nonexistent file so client-go cannot fall back to
	// any of the standard precedence paths (env=this-nonexistent,
	// --kubeconfig=unset, ~/.kube/config under tmpdir HOME).
	bogus := filepath.Join(t.TempDir(), "absolutely-not-a-real-kubeconfig")
	t.Setenv("KUBECONFIG", bogus)
	args := replaceFlag(validCloudacropolisArgs(), "--mode=install", "--mode=auto")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("--mode=auto without kubeconfig should error")
	}
	body := err.Error()
	// Two acceptable error surfaces:
	//   (a) prober constructor fails to load any kubeconfig
	//   (b) prober loads a config but the apiserver is unreachable
	// Both must direct the operator at the same remediation; assert
	// the universally-present cues.
	for _, want := range []string{"--mode="} {
		if !strings.Contains(body, want) {
			t.Errorf("error %q missing %q (operator-direction)", body, want)
		}
	}
	// AT LEAST ONE of these phrases must appear so operators know
	// what to fix.
	directions := []string{
		"--mode=install|adopt|resume",
		"check kubeconfig",
		"load kubeconfig",
	}
	matched := false
	for _, d := range directions {
		if strings.Contains(body, d) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("error %q missing any of %v (no remediation guidance)", body, directions)
	}
}

func TestBootstrapInit_ExistingFleetRequiresRepo(t *testing.T) {
	// Review-pass — P2/P3: existing-fleet without --repo would
	// produce a misleading plan with no priors. Refuse at Validate
	// time so the cobra layer surfaces a clean error.
	_, err := runInitCmdWithRepo(t, "", validCloudacropolisArgs())
	if err == nil {
		t.Fatal("existing-fleet without --repo should be rejected")
	}
	if !errors.Is(err, clusterinit.ErrFleetModeExistingRepo) {
		t.Fatalf("expected ErrFleetModeExistingRepo, got %v", err)
	}
}

func TestBootstrapInit_FlagSurfaceComplete(t *testing.T) {
	// Snapshot test for the flag surface: every flag listed in
	// installer-agentic-implementation-plan §9.M4-T01 must be
	// registered. assertRequiredFlagsRegistered panics on missing
	// flags at construction time; this test catches that panic by
	// reconstructing the cobra command and asserting it doesn't
	// panic. (If a future change drops a flag, the panic surfaces
	// here as a test failure rather than at runtime.)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("bootstrap init flag registration panicked: %v", r)
		}
	}()
	empty := ""
	_ = bootstrapInitCmd(&empty)
}

func TestBootstrapInit_HelpStable(t *testing.T) {
	// Sanity-check help output mentions the three operating modes
	// (M4 init engine status + dry-run + apply-plan) and the CI
	// apply gate. This is a thin stand-in for the snapshot test in
	// the plan's acceptance — full snapshot wait for M4-T02 plan
	// output to stabilise first.
	body, err := runInitCmd(t, []string{"--help"})
	if err != nil {
		t.Fatalf("help should not error, got %v", err)
	}
	for _, want := range []string{
		"--dry-run",
		"--apply-plan",
		"--no-tty",
		"cluster-config.env",
		"SCREAMING_SNAKE_CASE",
		// Post-M4-T12: the old "engine slices in development"
		// banner was removed once the apply path went live.
		// Assert on canonical surface elements that survive any
		// future text edits to the Long string.
		"Flux-reconciled",
		"hash-pinned",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

// --- helpers ---

// filterFlag returns args with every entry that starts with `prefix`
// (matching either "--flag" or "--flag=value") removed.
//
// Repeatable flags (--set, --node-nic, --addon): when `prefix`
// contains an `=`, only entries with the same key portion are
// removed. So `filterFlag(args, "--set=KEY=value")` strips just
// the `--set=KEY=...` entry, not every `--set`. Without this,
// the simple flag-name strip wipes every `--set` repetition and
// trips downstream validation on missing required keys.
func filterFlag(args []string, prefix string) []string {
	out := make([]string, 0, len(args))
	// Parse the prefix shape:
	//   "--flag"             → strip by flag name only
	//   "--flag=value"       → strip by flag name only (legacy
	//                           helpers don't pass repeatable keys)
	//   "--set=KEY=val"      → strip only the --set entry matching KEY
	flagName := prefix
	if i := strings.Index(prefix, "="); i > 0 {
		flagName = prefix[:i]
	}
	// Detect repeatable-flag key targeting: prefix has TWO `=` signs.
	targetKey := ""
	if isRepeatableFlag(flagName) {
		if first := strings.Index(prefix, "="); first > 0 {
			tail := prefix[first+1:]
			if eq := strings.Index(tail, "="); eq > 0 {
				targetKey = tail[:eq]
			}
		}
	}

	for _, a := range args {
		if a == flagName {
			continue
		}
		if strings.HasPrefix(a, flagName+"=") {
			if targetKey == "" {
				continue // strip every repetition (non-repeatable path)
			}
			// Repeatable-flag key targeting: only strip the entry
			// whose value's KEY-portion matches.
			value := a[len(flagName)+1:]
			if eq := strings.Index(value, "="); eq > 0 {
				if value[:eq] == targetKey {
					continue
				}
			}
		}
		out = append(out, a)
	}
	return out
}

// isRepeatableFlag reports whether a flag name is one of the
// repeatable KEY=VALUE flags so filterFlag can switch into key-
// targeting mode.
func isRepeatableFlag(name string) bool {
	switch name {
	case "--set", "--node-nic":
		return true
	}
	return false
}

// replaceFlag swaps the first arg matching `from` with `to`. If `from`
// is empty (no match), the function just appends `to`.
func replaceFlag(args []string, from, to string) []string {
	if from == "" {
		if to == "" {
			return args
		}
		return append(args, to)
	}
	out := make([]string, len(args))
	copy(out, args)
	flagName := from
	if i := strings.Index(from, "="); i > 0 {
		flagName = from[:i]
	}
	for i, a := range out {
		if a == flagName || strings.HasPrefix(a, flagName+"=") {
			out[i] = to
			return out
		}
	}
	return append(out, to)
}
