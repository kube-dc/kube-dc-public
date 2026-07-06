package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// validAtlantisArgs returns the canonical CLI flags for the
// atlantis bootstrap shape (M4-T01 acceptance). Used as the
// table baseline so each test case mutates one arg at a time.
//
// The 5 --set values are the cloud+public-vlan preset's required
// keys (M4-T04) populated from the atlantis README's CGNAT +
// public-IP topology. Tests that exercise "missing required key"
// paths filter --set entries out as needed.
//
// Note: --repo is a persistent flag on the parent `bootstrap`
// command, not on `init` — `runInitCmd` injects it via the helper's
// fleetRepo pointer rather than via the args slice.
func validAtlantisArgs() []string {
	return []string{
		"--preset=cloud+public-vlan",
		"--mode=install",
		"--name=atlantis",
		"--domain=kdc.atlantis.example.com",
		"--node-external-ip=203.0.113.52",
		"--email=ops@atlantis.example.com",
		"--fleet-mode=existing-fleet",
		// M4-T05 P2 close: owner/repo required for any apply-path
		// fleet mode (Validate check catches missing values when
		// dry-run is filtered off — tests that assert the missing
		// case filter these back out).
		"--github-owner=kube-dc",
		"--github-repo=kube-dc-fleet",
		// Mode is required with no default (OS-1). Baseline stays
		// `disabled` (no companion flags needed); rook-* modes are
		// live (OS-2) — mode-specific tests override explicitly.
		"--object-storage-mode=disabled",
		// cloud+public-vlan preset required keys.
		"--set=EXT_NET_VLAN_ID=1103",
		"--set=EXT_NET_INTERFACE=bond0",
		"--set=EXT_PUBLIC_VLAN_ID=1100",
		"--set=EXT_PUBLIC_CIDR=203.0.113.48/29",
		"--set=EXT_PUBLIC_GATEWAY=203.0.113.49",
		"--dry-run",
		"--no-tty",
	}
}

// runInitCmd executes `bootstrap init` with a fresh tmpdir fleet
// pre-provisioned: `.sops.yaml` with a known recipient + a matching
// age key file. This is the canonical happy-path shape for the
// atlantis-style tests; M4-T09 agekey enrollment check passes
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
// value for the atlantis-style happy-path tests.
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
	body, err := runInitCmd(t, validAtlantisArgs())
	if err != nil {
		t.Fatalf("dry-run with valid args should succeed, got %v\nout:\n%s", err, body)
	}
	// Assert the canonical plan-render shape — header, file list,
	// script list, mutations, hashes, footer. Anchored on tokens
	// that are stable across T04/T10/T13 refinements.
	for _, want := range []string{
		"DRY RUN",
		"atlantis",
		"Files to write",
		"clusters/atlantis/cluster-config.env",
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
	args := append(validAtlantisArgs(), "--plan-file="+planPath)
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
	if loaded.ClusterName != "atlantis" {
		t.Errorf("loaded ClusterName = %q, want atlantis", loaded.ClusterName)
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
	if _, err := runInitCmdWithRepo(t, repo, append(validAtlantisArgs(), "--plan-file="+planPath, "--allow-dns-not-ready")); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}

	// Same substantive flag set (including --allow-dns-not-ready) so
	// the InputHash matches.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
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
	//   1. Dry-run with atlantis flags → write plan
	//   2. Apply-plan with a DIFFERENT --set value
	//   3. VerifyApplyPlanInput must reject before runApplyEngine
	//      is called (engine markers should NOT appear in output)
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if _, err := runInitCmdWithRepo(t, repo, append(validAtlantisArgs(), "--plan-file="+planPath)); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}

	// Drift: bump the EXT_NET_VLAN_ID from 1103 to 999.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
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
	prep := append(validAtlantisArgs(), "--plan-file="+planPath, "--allow-no-kubevirt-eligible")
	if _, err := runInitCmdWithRepo(t, repo, prep); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}
	// Apply the plan WITHOUT the flag — should surface ErrPlanInputDrift.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
	args = append(args, "--apply-plan="+planPath, "--yes", "--allow-dns-not-ready")
	_, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatal("input drift on --allow-no-kubevirt-eligible must be rejected, got nil err")
	}
	if !errors.Is(err, clusterinit.ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift, got %v", err)
	}
}

// C2 (adopt fail-closed BEFORE mutation): with --mode=adopt against a
// cluster whose live components (cert-manager CRD, per the mock) aren't
// reflected in a fleet overlay, the adopt gate must refuse in
// runApplyEngine BEFORE clusterinit.Apply touches the fleet/cluster —
// same fail-fast contract as the DNS gate. Proves the gate is wired
// into the apply path, not just unit-tested in isolation.
func TestBootstrapInit_Adopt_GateBlocksBeforeMutation(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud") // mock K8s reports the cert-manager CRD
	repo := setupValidFleet(t)        // empty clusters/ → "atlantis" has no overlay
	// Relax every earlier apply gate so we actually REACH the adopt gate:
	// no dry-run (apply path), prereqs skipped, DNS downgraded, no push.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
	args = filterFlag(args, "--mode")
	args = append(args,
		"--mode=adopt", "--yes",
		"--allow-dns-not-ready", "--no-install-prereqs", "--no-push",
	)
	body, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatalf("adopt gate should block (cert-manager detected, no overlay); out:\n%s", body)
	}
	// cert-manager is detected but "atlantis" has no overlay → the
	// no-overlay boundary error (not the pin-versions nudge).
	if !strings.Contains(err.Error(), "no fleet overlay") {
		t.Errorf("expected the no-overlay boundary error, got: %v", err)
	}
	// Engine mutation markers must NOT appear — the gate ran BEFORE
	// runApplyEngine handed off to clusterinit.Apply.
	for _, forbidden := range []string{"[scaffold]", "flux-install.sh"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("engine mutated despite adopt-gate failure (contains %q):\n%s", forbidden, body)
		}
	}
}

// C2/C3 edge: init --mode=adopt against a reachable cluster with NO
// detected components AND no fleet overlay must still fail closed before
// mutation — foreign-cluster import isn't automated. Regression guard for
// the ordering bug where "nothing to pin → pass" ran before the
// OverlayMissing boundary. Uses the "fresh" scenario (reachable cluster,
// no CRDs) with an empty test fleet (no clusters/atlantis overlay).
func TestBootstrapInit_Adopt_NoOverlayNoComponentsFailsClosed(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "fresh") // reachable cluster, ListCRDs → none
	repo := setupValidFleet(t)        // empty clusters/ → "atlantis" has no overlay
	args := filterFlag(validAtlantisArgs(), "--dry-run")
	args = filterFlag(args, "--mode")
	args = append(args,
		"--mode=adopt", "--yes",
		"--allow-dns-not-ready", "--allow-no-kubevirt-eligible",
		"--no-install-prereqs", "--no-push",
	)
	body, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatalf("adopt on a no-overlay cluster with no components must fail closed; out:\n%s", body)
	}
	if !strings.Contains(err.Error(), "no fleet overlay") {
		t.Errorf("expected the no-overlay boundary error, got: %v", err)
	}
	for _, forbidden := range []string{"[scaffold]", "flux-install.sh"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("engine mutated despite the no-overlay gate (contains %q):\n%s", forbidden, body)
		}
	}
}

// C5 (adopt-gate plan-hash safety): --allow-unpinned-adopt is a
// substantive input, so dry-running WITH it then apply-plan WITHOUT it
// (or vice versa) must trip ErrPlanInputDrift — an operator can't review
// a plan that would refuse an unpinned cluster and then apply with the
// bypass silently flipped. Mirrors the AllowNoKubevirtEligible case.
func TestBootstrapInit_ApplyPlan_RefusesOnAllowUnpinnedAdoptDrift(t *testing.T) {
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	// Dry-run WITH the bypass set.
	prep := append(validAtlantisArgs(), "--plan-file="+planPath, "--allow-unpinned-adopt")
	if _, err := runInitCmdWithRepo(t, repo, prep); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}
	// Apply the plan WITHOUT the bypass — should surface ErrPlanInputDrift.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
	args = append(args, "--apply-plan="+planPath, "--yes", "--allow-dns-not-ready")
	_, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatal("input drift on --allow-unpinned-adopt must be rejected, got nil err")
	}
	if !errors.Is(err, clusterinit.ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift, got %v", err)
	}
}

// C3/C5: --allow-unpinned-adopt registers on the cobra command surface.
func TestBootstrapInit_AllowUnpinnedAdoptFlagRegistered(t *testing.T) {
	empty := ""
	cmd := bootstrapInitCmd(&empty)
	if cmd.Flag("allow-unpinned-adopt") == nil {
		t.Fatal("--allow-unpinned-adopt flag missing from `bootstrap init` — regression")
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

// Reviewer P1 regression guard (dry-run mutation): `--dry-run`
// promises "no mutations", but the M4-T09 greenfield-generate
// auto-run WAS running `bootstrap/generate-age-key.sh` before the
// dry-run dispatch — creating `<fleet>/age.key` on disk during a
// plan preview. This test uses a fleet WITH the generate script
// available (so the auto-run WOULD fire without the guard) and
// asserts `age.key` does NOT appear post-dry-run.
func TestBootstrapInit_DryRun_NewRepo_DoesNotGenerateAgeKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SOPS_AGE_KEY_FILE", "")

	// Fresh fleet with a NO age.key + a script that WOULD create
	// one if the auto-run fired. We stub the script by dropping a
	// no-op shell into bootstrap/generate-age-key.sh; if the
	// auto-run mistakenly runs, the script would touch `age.key`.
	// Since our dry-run guard fires FIRST, the script never runs
	// and age.key never appears.
	repo := t.TempDir()
	if err := os.MkdirAll(repo+"/bootstrap", 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a real (executable) shell script that WOULD create
	// age.key + .sops.yaml. If the dry-run guard is broken, this
	// gets invoked and the assertion below fails.
	script := "#!/bin/sh\ntouch age.key\n"
	if err := os.WriteFile(repo+"/bootstrap/generate-age-key.sh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--preset=internal-only",
		"--mode=install",
		"--name=fresh",
		"--domain=fresh.example.com",
		"--node-external-ip=1.2.3.4",
		"--email=ops@fresh",
		"--fleet-mode=new-repo",
		"--github-owner=acme",
		"--github-repo=fleet",
		"--rook-mode=rook-ceph-multi-node",
		"--set=EXT_NET_VLAN_ID=1",
		"--set=EXT_NET_INTERFACE=e",
		"--dry-run",
		"--no-tty",
	}
	_, err := runInitCmdWithRepo(t, repo, args)
	// Dry-run may still error out (age-key gate returns
	// ErrAgeKeyDryRunSkip → RunE downgrades to WARNING → but
	// downstream may fail on preset OR the plan may render — the
	// test's point is NOT to assert exit code, only mutation).
	_ = err

	// **Critical invariant**: `<fleet>/age.key` MUST NOT exist
	// after a dry-run. If the guard is broken, the shell script
	// above would have created it.
	if _, statErr := os.Stat(repo + "/age.key"); !os.IsNotExist(statErr) {
		t.Errorf("dry-run created <fleet>/age.key — mutation regression (P1)")
	}
}

// M4-T05 GitLab boundary RETIRED (properly this time): the
// coordinated `kube-dc-fleet` PR made `flux-install.sh`
// provider-aware, so the CLI-side fail-closed is no longer
// needed. This test guards the OPPOSITE contract: --provider=gitlab
// with --yes should NOT trip the retired sentinel.
//
// Downstream still fails in a test env (no real cluster / fleet
// scripts), but the failure must be structural, not the retired
// sentinel. A regression back to fail-closed would trip this
// test's `errors.Is` guard.
func TestBootstrapInit_DefaultApply_GitLab_Proceeds(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GH_TOKEN", "irrelevant")

	args := []string{
		"--preset=cloud+public-vlan",
		"--mode=install",
		"--name=gl-test",
		"--domain=gl.example.com",
		"--node-external-ip=1.2.3.4",
		"--email=ops@gl.example.com",
		"--fleet-mode=new-repo",
		"--provider=gitlab",
		"--github-owner=acme-group",
		"--github-repo=kdc-fleet",
		"--github-token=irrelevant",
		"--rook-mode=rook-ceph-multi-node",
		"--set=EXT_NET_VLAN_ID=1103",
		"--set=EXT_NET_INTERFACE=bond0",
		"--set=EXT_PUBLIC_VLAN_ID=1100",
		"--set=EXT_PUBLIC_CIDR=203.0.113.48/29",
		"--set=EXT_PUBLIC_GATEWAY=203.0.113.49",
		"--yes",
		"--no-tty",
		"--allow-dns-not-ready",
	}
	_, err := runInitCmd(t, args)
	// A downstream error is expected (test env has no real cluster
	// / fleet); we care only that the pre-retire fail-closed text
	// isn't surfacing (that would mean the boundary got reinstated
	// somewhere). Text-level guard because the ErrGitLabApplyNotImplemented
	// sentinel is deleted — assert on the pre-fix message shape.
	if err != nil && strings.Contains(err.Error(), "not yet supported on the apply path") {
		t.Fatalf("GitLab boundary retired but still firing (regression): %v", err)
	}
}

// M4-T05 dry-run stays unaffected by the GitLab boundary — the
// operator can preview a GitLab-shaped plan without triggering
// the fail-closed sentinel (no side effects → no boundary).
func TestBootstrapInit_DryRun_GitLab_Allowed(t *testing.T) {
	args := []string{
		"--preset=cloud+public-vlan",
		"--mode=install",
		"--name=gl-preview",
		"--domain=gl.example.com",
		"--node-external-ip=1.2.3.4",
		"--email=ops@gl.example.com",
		"--fleet-mode=new-repo",
		"--provider=gitlab",
		"--github-owner=acme-group",
		"--github-repo=kdc-fleet",
		"--object-storage-mode=disabled", // baseline: no companions needed; rook modes live (OS-2)
		"--set=EXT_NET_VLAN_ID=1103",
		"--set=EXT_NET_INTERFACE=bond0",
		"--set=EXT_PUBLIC_VLAN_ID=1100",
		"--set=EXT_PUBLIC_CIDR=203.0.113.48/29",
		"--set=EXT_PUBLIC_GATEWAY=203.0.113.49",
		"--dry-run",
		"--no-tty",
	}
	body, err := runInitCmd(t, args)
	if err != nil {
		// The M4-T09 age-key gate downgrades to a WARNING on
		// dry-run for new-repo, so we should get a clean plan back.
		t.Fatalf("dry-run + gitlab should succeed, got %v; output:\n%s", err, body)
	}
	// The GitLab boundary MUST NOT fire on dry-run.
	if strings.Contains(body, "not yet supported on the apply path") {
		t.Errorf("dry-run should skip the gitlab boundary; output:\n%s", body)
	}
	// A dry-run plan render happened.
	if !strings.Contains(body, "DRY RUN") {
		t.Errorf("expected dry-run banner; output:\n%s", body)
	}
}

// NOTE (M4-T05): the greenfield age-key gate now auto-runs
// `bootstrap/generate-age-key.sh` on new-repo mode (M4-T09 close,
// SHA `1ace3c9d`), so runApplyEngine IS reachable in a test env
// that seeds the fleet-starter shape. Full end-to-end coverage
// (create-repo → scaffold → commit+push → flux-install with a
// stubbed ScriptRunner) is a nice-to-have but out of scope; the
// CreateGitHubRepo engine already has thorough unit coverage in
// create_repo_test.go (happy path / already-exists / auth
// failure / token scrub / missing-deps).

func TestBootstrapInit_ApplyPlan_RefusesOnInputDrift(t *testing.T) {
	// Same --repo across both runs — only the substantive inputs
	// drift (the test's intent).
	// Same --repo across both runs — only the substantive inputs
	// drift (the test's intent).
	repo := setupValidFleet(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if _, err := runInitCmdWithRepo(t, repo, append(validAtlantisArgs(), "--plan-file="+planPath)); err != nil {
		t.Fatalf("dry-run prep: %v", err)
	}
	// Mutate the inputs between dry-run and apply — different
	// --node-external-ip. VerifyApplyPlanInput must catch this and
	// surface ErrPlanInputDrift.
	args := filterFlag(validAtlantisArgs(), "--dry-run")
	args = filterFlag(args, "--node-external-ip=203.0.113.52")
	args = append(args, "--node-external-ip=203.0.113.53", "--apply-plan="+planPath, "--yes")
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
	// resolution of the placeholder atlantis domain. The
	// M6-T05 NFD gate soft-skips on nil K8sClient (test env has
	// no kubeconfig) so it never fires here.
	args := validAtlantisArgs()
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
	args := validAtlantisArgs()
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
	args := validAtlantisArgs()
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
			args: append(validAtlantisArgs(), "--apply-plan=/tmp/p.json"),
			// Validate's "mutually exclusive" error wraps ErrValidation.
			wantSub: "mutually exclusive",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name: "fleet-mode=new-repo without github-owner",
			// Filter out the baseline's --github-owner + --github-repo
			// (added by validAtlantisArgs post the M4-T05 P2
			// close) so we reach the actual "missing owner" case.
			args: replaceFlag(
				filterFlag(
					filterFlag(
						filterFlag(validAtlantisArgs(), "--fleet-mode=existing-fleet"),
						"--github-owner=kube-dc"),
					"--github-repo=kube-dc-fleet"),
				"", "--fleet-mode=new-repo"),
			wantSub: "github-owner",
			wantErr: clusterinit.ErrFleetModeNewRepo,
		},
		{
			name: "no-tty without --yes/--apply-plan/--dry-run",
			args: replaceFlag(filterFlag(validAtlantisArgs(), "--dry-run"), "", ""),
			// Just --no-tty and no satisfier — Validate refuses.
			wantSub: "--no-tty requires",
			wantErr: clusterinit.ErrApplyGate,
		},
		{
			name:    "bad preset",
			args:    replaceFlag(validAtlantisArgs(), "--preset=cloud+public-vlan", "--preset=bogus"),
			wantSub: "--preset",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad mode",
			args:    replaceFlag(validAtlantisArgs(), "--mode=install", "--mode=upgrade"),
			wantSub: "--mode",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad domain",
			args:    replaceFlag(validAtlantisArgs(), "--domain=kdc.atlantis.example.com", "--domain=localhost"),
			wantSub: "--domain",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "bad IP",
			args:    replaceFlag(validAtlantisArgs(), "--node-external-ip=203.0.113.52", "--node-external-ip=not-an-ip"),
			wantSub: "not a valid IP",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "lowercase set rejected",
			args:    append(validAtlantisArgs(), "--set=domain=foo.example.com"),
			wantSub: "SCREAMING_SNAKE_CASE",
			wantErr: clusterinit.ErrValidation,
		},
		{
			name:    "unknown addon rejected",
			args:    append(validAtlantisArgs(), "--addon=cilium"),
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
	// non-required --set + --node-nic entries to exercise the
	// repeatable-flag handling. NOTE: --addon is deliberately absent —
	// it now fails closed (see TestBootstrapInit_Addon_FailsClosed);
	// its repeatable-parsing is covered by the unknown-addon case in
	// FlagConflicts.
	args := append(validAtlantisArgs(),
		"--set=PROM_STORAGE=50Gi",
		"--set=PROM_RETENTION=730d",
		"--node-nic=HOST5-A=enp1s0",
		"--node-nic=HOST6-A=enp1s0",
	)
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("repeatable flags should validate, got %v\nout:\n%s", err, body)
	}
	// The plan render shows the customInterfaces-rendering step + the
	// count of nic mappings when --node-nic is repeated. NOTE: the
	// plan no longer claims an "addons overlay" file — add-cluster.sh
	// scaffolds no addon files and nothing in the apply path consumes
	// o.Addons, so listing an addons file was a dry-run fidelity lie
	// (E2E shakedown 2026-07-04).
	for _, want := range []string{
		"2 --node-nic mapping",
		"customInterfaces",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plan render missing %q\nFULL:\n%s", want, body)
		}
	}
	// Guard the fix: the plan must NOT resurrect the phantom overlay
	// files the scaffold never writes.
	for _, absent := range []string{
		"addons overlay",
		"infra-object-storage",
		"object-storage overlay",
	} {
		if strings.Contains(body, absent) {
			t.Errorf("plan render leaked phantom file %q (add-cluster.sh does not scaffold it)\nFULL:\n%s", absent, body)
		}
	}
}

func TestBootstrapInit_Addon_FailsClosed(t *testing.T) {
	// E2E shakedown 2026-07-04: --addon validated + hashed + rendered
	// but scaffolded nothing (a no-op). It now fails closed. A VALID
	// addon must reach the cobra surface as ErrAddonsNotImplemented
	// (wrapped by nothing — returned directly from Validate) so the
	// operator gets the actionable manual-path message. This fires even
	// under --dry-run (a plan preview with phantom addons would be the
	// same lie).
	args := append(validAtlantisArgs(), "--addon=metallb")
	_, err := runInitCmd(t, args)
	if err == nil {
		t.Fatal("expected --addon to fail closed, got nil")
	}
	if !errors.Is(err, clusterinit.ErrAddonsNotImplemented) {
		t.Fatalf("expected ErrAddonsNotImplemented, got %v", err)
	}
	if !strings.Contains(err.Error(), "addons.yaml") {
		t.Errorf("error should point at the manual addons.yaml path, got %q", err.Error())
	}
}

// --- OS-1 object-storage mode surface -------------------------------

// filterArgs drops any arg with the given prefix from a copy of args.
func filterArgs(args []string, prefix string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func TestBootstrapInit_ObjectStorageMode_Required(t *testing.T) {
	// No mode at all → typed required error. No sibling in the
	// default fixture fleet → no hint suffix.
	args := filterArgs(validAtlantisArgs(), "--object-storage-mode")
	_, err := runInitCmd(t, args)
	if !errors.Is(err, clusterinit.ErrObjectStorageModeRequired) {
		t.Fatalf("expected ErrObjectStorageModeRequired, got %v", err)
	}
	if strings.Contains(err.Error(), "template sibling") {
		t.Errorf("no-sibling fleet should not produce a hint, got %q", err.Error())
	}
}

func TestBootstrapInit_ObjectStorageMode_RequiredWithSiblingHint(t *testing.T) {
	// Existing-fleet with a template sibling running rook-ceph-pvc →
	// the required error carries the hint (design call 2026-07-04:
	// hint, never inherit).
	repo := setupValidFleet(t)
	sib := filepath.Join(repo, "clusters", "eudc1")
	if err := os.MkdirAll(filepath.Join(sib, "object-storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sib, "cluster-config.env"),
		[]byte("CLUSTER_NAME=eudc1\nDOMAIN=kdc.eu-dc1.example.com\nKUBE_DC_VERSION=v0.3.90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := "apiVersion: kustomize.config.k8s.io/v1beta1\n" +
		"kind: Kustomization\n" +
		"resources:\n" +
		"  - ../../../infrastructure/object-storage/modes/rook-ceph-pvc\n" +
		"  - ../../../infrastructure/object-storage/bucket-provisioning\n"
	if err := os.WriteFile(filepath.Join(sib, "object-storage", "kustomization.yaml"),
		[]byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}

	args := filterArgs(validAtlantisArgs(), "--object-storage-mode")
	_, err := runInitCmdWithRepo(t, repo, args)
	if !errors.Is(err, clusterinit.ErrObjectStorageModeRequired) {
		t.Fatalf("expected ErrObjectStorageModeRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "template sibling eudc1 uses rook-ceph-pvc") {
		t.Errorf("expected sibling hint in error, got %q", err.Error())
	}
}

func TestBootstrapInit_ObjectStorageMode_DeprecatedAliasWorks(t *testing.T) {
	// --rook-mode still binds for one release (pflag prints the
	// deprecation notice); disabled via the alias must behave exactly
	// like the canonical flag.
	args := append(filterArgs(validAtlantisArgs(), "--object-storage-mode"),
		"--rook-mode=disabled")
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("alias should work, got %v\nout:\n%s", err, body)
	}
	if !strings.Contains(body, "DRY RUN") {
		t.Errorf("expected plan render via alias\nFULL:\n%s", body)
	}
}

func TestBootstrapInit_ObjectStorageMode_AliasConflict(t *testing.T) {
	// Both flags with DIFFERENT values → hard error (silently
	// preferring either would surprise).
	args := append(validAtlantisArgs(), "--rook-mode=rook-ceph-local")
	_, err := runInitCmd(t, args)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestBootstrapInit_ObjectStorageMode_RookDryRunRendersWiring(t *testing.T) {
	// OS-2: a complete rook-ceph-local config passes Validate and the
	// dry-run plan predicts the full object-storage wiring — the
	// overlay + Flux layer files, the extended platform/kustomization
	// descriptions, and the in-process writer step. (Replaces the
	// OS-1 FailsClosedUntilOS2 test — the scaffold writer shipped.)
	args := append(filterArgs(validAtlantisArgs(), "--object-storage-mode"),
		"--object-storage-mode=rook-ceph-local",
		"--rook-osd-node=host6-a",
		"--rook-osd-size-gb=500",
	)
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("rook-ceph-local dry-run should succeed post-OS-2, got %v\nout:\n%s", err, body)
	}
	for _, want := range []string{
		"object-storage/kustomization.yaml",
		"infra-object-storage.yaml",
		"modes/rook-ceph-local + bucket-provisioning + exposure",
		"healthCheck: CephCluster Ready",
		"dependsOn: infra-core + infra-object-storage",
		"wire object-storage (mode=rook-ceph-local)",
		"object-storage keys",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rook dry-run render missing %q\nFULL:\n%s", want, body)
		}
	}
	// No warnings at all for a real mode.
	if strings.Contains(body, "== WARNINGS ==") {
		t.Errorf("rook mode should not carry any warning block\nFULL:\n%s", body)
	}
}

func TestBootstrapInit_ObjectStorageMode_NoExposureRendered(t *testing.T) {
	// --no-s3-exposure drops the exposure layer from the predicted
	// overlay composition.
	args := append(filterArgs(validAtlantisArgs(), "--object-storage-mode"),
		"--object-storage-mode=rook-ceph-pvc",
		"--ceph-storage-class=fast-ssd",
		"--no-s3-exposure",
	)
	body, err := runInitCmd(t, args)
	if err != nil {
		t.Fatalf("pvc dry-run should succeed, got %v", err)
	}
	if !strings.Contains(body, "modes/rook-ceph-pvc + bucket-provisioning") {
		t.Errorf("overlay composition missing\nFULL:\n%s", body)
	}
	if strings.Contains(body, "+ exposure") {
		t.Errorf("--no-s3-exposure must drop the exposure layer\nFULL:\n%s", body)
	}
}

func TestBootstrapInit_ObjectStorageMode_StubFailsClosed(t *testing.T) {
	args := append(filterArgs(validAtlantisArgs(), "--object-storage-mode"),
		"--object-storage-mode=external-s3")
	_, err := runInitCmd(t, args)
	if !errors.Is(err, clusterinit.ErrObjectStorageModeStub) {
		t.Fatalf("expected ErrObjectStorageModeStub, got %v", err)
	}
}

func TestBootstrapInit_ObjectStorageMode_DisabledWarnsInPlan(t *testing.T) {
	// The baseline args carry --object-storage-mode=disabled; the
	// dry-run plan MUST state the consequence prominently.
	body, err := runInitCmd(t, validAtlantisArgs())
	if err != nil {
		t.Fatalf("disabled dry-run should succeed, got %v", err)
	}
	for _, want := range []string{"== WARNINGS ==", "Mimir + Loki are SUSPENDED", "UNPROTECTED"} {
		if !strings.Contains(body, want) {
			t.Errorf("disabled plan render missing %q\nFULL:\n%s", want, body)
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
		args := append(validAtlantisArgs(), "--github-token="+fakeToken)
		body, err := runInitCmd(t, args)
		if err != nil {
			t.Fatalf("dry-run should pass, got %v\nout:\n%s", err, body)
		}
		if strings.Contains(body, "FAKE_TOKEN_DO_NOT_LEAK") {
			t.Fatalf("--github-token value leaked into dry-run output:\n%s", body)
		}
	})

	t.Run("new-repo dry-run path", func(t *testing.T) {
		// new-repo dry-run: the greenfield age-key gate's
		// mutations-allowed=false branch (M4-T09 close) surfaces
		// ErrAgeKeyDryRunSkip when `<fleet>/age.key` is missing,
		// which RunE downgrades to a WARNING so the plan
		// renders. The token must not appear anywhere in the
		// resulting output.
		args := append(filterFlag(validAtlantisArgs(), "--fleet-mode=existing-fleet"),
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

// setupFleetWithSibling extends setupValidFleet with one synthetic
// sibling cluster carrying a cluster-config.env. Hermetic replacement
// for the former live-fleet-dependent tests (2026-07-04 sweep: no
// operator paths or real cluster values in this package).
func setupFleetWithSibling(t *testing.T, sibling, envBody string) string {
	t.Helper()
	repo := setupValidFleet(t)
	dir := filepath.Join(repo, "clusters", sibling)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte(envBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestBootstrapInit_DomainCollision_Rejected(t *testing.T) {
	// M4-T13: --domain matching an existing sibling must fail
	// loudly with ErrDomainCollision. The cobra wiring runs the
	// check before BuildPlan so the error surfaces clean. Hermetic:
	// synthetic sibling "prod1" owns the domain we try to reuse.
	repo := setupFleetWithSibling(t, "prod1",
		"CLUSTER_NAME=prod1\nDOMAIN=prod1.example.com\nKUBE_DC_VERSION=v0.0.1\n")
	args := replaceFlag(validAtlantisArgs(),
		"--domain=kdc.atlantis.example.com",
		"--domain=prod1.example.com") // collides with sibling prod1
	_, err := runInitCmdWithRepo(t, repo, args)
	if err == nil {
		t.Fatal("expected ErrDomainCollision when --domain reuses a sibling's value")
	}
	if !errors.Is(err, clusterinit.ErrDomainCollision) {
		t.Fatalf("expected ErrDomainCollision, got %v", err)
	}
	if !strings.Contains(err.Error(), "prod1") {
		t.Errorf("error should name the colliding sibling: %v", err)
	}
}

func TestBootstrapInit_InheritsFromFleetSiblings(t *testing.T) {
	// M4-T13: an existing-fleet dry-run must surface the
	// InheritedDefaults block in the plan render. Hermetic: a
	// synthetic sibling carries the version pin we expect to see
	// inherited (formerly ran against the operator's live fleet —
	// same coverage, no local-checkout dependency).
	repo := setupFleetWithSibling(t, "prod1",
		"CLUSTER_NAME=prod1\nDOMAIN=prod1.example.com\nKUBE_DC_VERSION=v9.9.9\n")
	body, err := runInitCmdWithRepo(t, repo, validAtlantisArgs())
	if err != nil {
		t.Fatalf("dry-run should succeed; got %v\nout:\n%s", err, body)
	}
	for _, want := range []string{
		"Template (most-recently-modified sibling): prod1",
		"Inherited defaults",
		"KUBE_DC_VERSION=v9.9.9",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inheritance render missing %q\nFULL:\n%s", want, body)
		}
	}
}

func TestDiscoverFleetState_PopulatesSOPSRecipients(t *testing.T) {
	// E2E shakedown 2026-07-04 regression: discoverFleetState walked
	// siblings + inherited version pins but never read <repo>/.sops.yaml,
	// so FleetState.SOPSRecipients stayed 0 and the plan's "sops encrypt"
	// step always rendered the misleading "single recipient (operator's
	// local age key)" note even when the fleet had a multi-recipient
	// block. Assert the count now reflects reality.
	threeRecipients := "creation_rules:\n" +
		"  - path_regex: '\\.enc\\.yaml$'\n" +
		"    age: 'age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu," +
		"age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm," +
		"age16nk3t6chcrjntd76s3an32hx3p2y5cup7vnkywny0uy9gn0tkcuqxzav8s'\n"

	cases := []struct {
		name string
		sops string // .sops.yaml body; "" means don't write the file
		want int
	}{
		{"three recipients", threeRecipients, 3},
		{"no sops file", "", 0},
		{"recipient-less sops", "creation_rules: []\n", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// A sibling cluster so ListClusters is non-empty and the
			// existing-fleet path runs end-to-end.
			repo := writeFleetFixture(t, map[string]string{"cloud": "KUBE_DC_VERSION=v0.3.90\n"})
			if tc.sops != "" {
				if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(tc.sops), 0o644); err != nil {
					t.Fatalf("write .sops.yaml: %v", err)
				}
			}
			o := &clusterinit.InitOptions{
				FleetMode: clusterinit.FleetExistingFleet,
				Repo:      repo,
				Name:      "new-cluster",
			}
			st := discoverFleetState(o)
			if st.SOPSRecipients != tc.want {
				t.Errorf("SOPSRecipients = %d, want %d", st.SOPSRecipients, tc.want)
			}
		})
	}
}

func TestBootstrapInit_AgeKey_EnrolledPasses(t *testing.T) {
	// M4-T09 happy path: the fixture fleet has the operator's
	// pubkey in .sops.yaml and a matching age.key at the repo
	// root. The dry-run prints "[sops] enrolled (pubkey=… via …)"
	// and proceeds to the plan.
	body, err := runInitCmd(t, validAtlantisArgs())
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
	args := filterFlag(validAtlantisArgs(), "--dry-run")
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

	body, err := runInitCmdWithRepo(t, dir, validAtlantisArgs())
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

	args := filterFlag(validAtlantisArgs(), "--dry-run")
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
	args := append(filterFlag(validAtlantisArgs(), "--set=EXT_PUBLIC_CIDR=203.0.113.48/29"),
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
	args := append(validAtlantisArgs(),
		"--node-nic=HOST5-A=enp1s0;rm")
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
	args := append(filterFlag(validAtlantisArgs(), "--set=EXT_NET_VLAN_ID=1103"),
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
	args := filterFlag(validAtlantisArgs(), "--set=EXT_PUBLIC_VLAN_ID=1100")
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
	args := replaceFlag(validAtlantisArgs(), "--mode=install", "--mode=auto")
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
	_, err := runInitCmdWithRepo(t, "", validAtlantisArgs())
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

// Reviewer P2 (T6): the wizard fires ONLY on a truly bare invocation —
// any init-local flag (a partial CLI attempt) must fall through to
// flag validation instead of a form that would overwrite the given
// flags. The parent's --repo is context, not an init flag, and stays
// exempt.
func TestBareInitInvocation_Gate(t *testing.T) {
	build := func(args []string) *cobra.Command {
		repo := ""
		parent := &cobra.Command{Use: "bootstrap"}
		parent.PersistentFlags().StringVar(&repo, "repo", "", "")
		c := bootstrapInitCmd(&repo)
		parent.AddCommand(c)
		parent.SetArgs(append([]string{"init"}, args...))
		parent.SetOut(io.Discard)
		parent.SetErr(io.Discard)
		_ = parent.Execute() // errors fine — we only inspect flag state
		return c
	}
	if !bareInitInvocation(build(nil)) {
		t.Error("no flags → bare")
	}
	if !bareInitInvocation(build([]string{"--repo=/tmp"})) {
		t.Error("--repo only → still bare (context flag)")
	}
	if bareInitInvocation(build([]string{"--provider=gitlab", "--fleet-mode=new-repo", "--no-tty", "--dry-run"})) {
		t.Error("partial invocation must NOT be bare")
	}
	if bareInitInvocation(build([]string{"--name=x", "--no-tty", "--dry-run"})) {
		t.Error("--name set must NOT be bare")
	}
}

// Review P1 (E2E finding 5 follow-up): origin must be ENSURED for every
// new-repo apply — added when missing, CORRECTED when pointing at a
// stale template remote, untouched when already canonical.
func TestEnsureOriginRemote(t *testing.T) {
	mk := func(t *testing.T) string {
		d := t.TempDir()
		if out, err := execGit(t, d, "init", "-q"); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		return d
	}
	var buf bytes.Buffer

	// missing → added
	repo := mk(t)
	if err := ensureOriginRemote(t.Context(), &buf, repo, "", "kube-dc", "e2e-fleet"); err != nil {
		t.Fatal(err)
	}
	if out, _ := execGit(t, repo, "remote", "get-url", "origin"); !strings.Contains(out, "github.com/kube-dc/e2e-fleet.git") {
		t.Errorf("origin not added: %s", out)
	}

	// wrong → corrected
	repo = mk(t)
	if _, err := execGit(t, repo, "remote", "add", "origin", "https://github.com/other/starter.git"); err != nil {
		t.Fatal(err)
	}
	if err := ensureOriginRemote(t.Context(), &buf, repo, "gitlab", "acme", "fleet"); err != nil {
		t.Fatal(err)
	}
	if out, _ := execGit(t, repo, "remote", "get-url", "origin"); !strings.Contains(out, "gitlab.com/acme/fleet.git") {
		t.Errorf("stale origin not corrected: %s", out)
	}
	if !strings.Contains(buf.String(), "CORRECTED") {
		t.Error("correction must be visible in output")
	}

	// matching (incl. token-decorated via insteadOf shapes) → no-op
	repo = mk(t)
	if _, err := execGit(t, repo, "remote", "add", "origin", "https://x-token@github.com/kube-dc/e2e-fleet.git"); err != nil {
		t.Fatal(err)
	}
	if err := ensureOriginRemote(t.Context(), &buf, repo, "", "kube-dc", "e2e-fleet"); err != nil {
		t.Fatal(err)
	}
	if out, _ := execGit(t, repo, "remote", "get-url", "origin"); !strings.Contains(out, "x-token@") {
		t.Errorf("matching decorated origin must be left alone: %s", out)
	}

	// Review P1 (2026-07-05): a github.com remote for the SAME owner/repo
	// must NOT satisfy a gitlab-provider apply — the old substring check
	// ignored the host and left the push aimed at GitHub.
	repo = mk(t)
	if _, err := execGit(t, repo, "remote", "add", "origin", "https://github.com/acme/fleet.git"); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := ensureOriginRemote(t.Context(), &buf, repo, "gitlab", "acme", "fleet"); err != nil {
		t.Fatal(err)
	}
	if out, _ := execGit(t, repo, "remote", "get-url", "origin"); !strings.Contains(out, "gitlab.com/acme/fleet.git") {
		t.Errorf("wrong-HOST origin (github vs gitlab) not corrected: %s", out)
	}

	// Review P1: a path PREFIX must not false-positive — `/acme/fleet`
	// is a different repo from `/acme/fleet-starter`.
	repo = mk(t)
	if _, err := execGit(t, repo, "remote", "add", "origin", "https://github.com/acme/fleet-starter.git"); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := ensureOriginRemote(t.Context(), &buf, repo, "", "acme", "fleet"); err != nil {
		t.Fatal(err)
	}
	if out, _ := execGit(t, repo, "remote", "get-url", "origin"); !strings.Contains(out, "github.com/acme/fleet.git") || strings.Contains(out, "fleet-starter") {
		t.Errorf("prefix false-positive: fleet-starter not corrected to fleet: %s", out)
	}
}

func TestRemoteIdentity(t *testing.T) {
	cases := []struct {
		raw, host, path string
		ok              bool
	}{
		{"https://github.com/acme/fleet.git", "github.com", "acme/fleet", true},
		{"https://x-access-token:TOKEN@github.com/acme/fleet.git", "github.com", "acme/fleet", true},
		{"https://gitlab.example.com:8443/acme/fleet.git", "gitlab.example.com", "acme/fleet", true},
		{"git@github.com:acme/fleet.git", "github.com", "acme/fleet", true},
		{"ssh://git@github.com/acme/fleet.git", "github.com", "acme/fleet", true},
		{"https://github.com/acme/fleet-starter", "github.com", "acme/fleet-starter", true},
		{"gitlab.com:group/sub/repo.git", "gitlab.com", "group/sub/repo", true},
		{"", "", "", false},
		{"not-a-url", "", "", false},
	}
	for _, c := range cases {
		h, p, ok := remoteIdentity(c.raw)
		if ok != c.ok || h != c.host || p != c.path {
			t.Errorf("remoteIdentity(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.raw, h, p, ok, c.host, c.path, c.ok)
		}
	}

	// remoteMatches is host-exact + path-exact, host case-insensitive.
	if !remoteMatches("https://GitHub.com/acme/fleet.git", "github.com", "acme", "fleet") {
		t.Error("host comparison must be case-insensitive")
	}
	if remoteMatches("https://github.com/acme/fleet-starter.git", "github.com", "acme", "fleet") {
		t.Error("path prefix must not match")
	}
	if remoteMatches("https://github.com/acme/fleet.git", "gitlab.com", "acme", "fleet") {
		t.Error("cross-host must not match")
	}
}

func execGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
