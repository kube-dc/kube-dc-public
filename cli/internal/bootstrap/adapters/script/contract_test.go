package script

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Every entry in scriptPaths MUST have a matching entry in
// scriptContracts. Catches "added a ScriptKind + path but forgot the
// contract" — the moment defaultCmdFactory tries to validate an
// unregistered kind it would fall through to the defensive
// ErrUnknownScript path, masking the registration drift.
func TestContracts_CoversAllExportedKinds(t *testing.T) {
	for k := range scriptPaths {
		if _, ok := scriptContracts[k]; !ok {
			t.Errorf("scriptContracts missing entry for %s", k)
		}
	}
}

// hostEnvPassthrough must stay byte-aligned with mergeEnv's allowlist
// — validateContract counts the host-env keys towards required-env
// satisfaction, so the two slices drifting silently corrupts the
// contract. Guards the agent-rule-4 boundary (an adapter-private slice
// can't be enforced at the ports layer).
func TestHostEnvPassthrough_AlignsWithMergeEnvAllowlist(t *testing.T) {
	want := []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM", "SSH_AUTH_SOCK", "KUBECONFIG"}
	if len(hostEnvPassthrough) != len(want) {
		t.Fatalf("hostEnvPassthrough size=%d want %d", len(hostEnvPassthrough), len(want))
	}
	for _, k := range want {
		if _, ok := hostEnvPassthrough[k]; !ok {
			t.Errorf("hostEnvPassthrough missing %q (drifted from mergeEnv allowlist)", k)
		}
	}
}

// Required-env validation: when a script requires KUBECONFIG and the
// operator supplies it via the call env, the contract is satisfied
// without any host-env presence.
func TestValidateContract_RequiredEnv_ExplicitSatisfies(t *testing.T) {
	t.Setenv("KUBECONFIG", "") // forbid host-env from satisfying

	err := validateContract(
		ports.ScriptSetupKeycloakOIDC,
		map[string]string{"KUBECONFIG": "/path/to/kc"},
		[]string{"cluster-a"},
	)
	if err != nil {
		t.Errorf("explicit env should satisfy; got: %v", err)
	}
}

// Required-env validation: host-env passthrough satisfies a required
// key when the operator's shell exports it. Mirrors the runtime
// behaviour where mergeEnv layers KUBECONFIG into the script's env.
func TestValidateContract_RequiredEnv_HostEnvSatisfies(t *testing.T) {
	t.Setenv("KUBECONFIG", "/operator/shell/kubeconfig")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC,
		nil,
		[]string{"cluster-a"},
	)
	if err != nil {
		t.Errorf("host env should satisfy; got: %v", err)
	}
}

// Required-env validation: missing key surfaces ErrMissingRequiredEnv
// with the key name in the message + actionable remediation hint.
func TestValidateContract_RequiredEnv_Missing(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	err := validateContract(
		ports.ScriptFluxInstall,
		map[string]string{"KUBECONFIG": "/k"}, // GITHUB_TOKEN missing
		[]string{"cluster-a"},
	)
	if err == nil {
		t.Fatal("expected ErrMissingRequiredEnv")
	}
	if !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Errorf("err not ErrMissingRequiredEnv: %v", err)
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("error should name missing key (GITHUB_TOKEN): %v", err)
	}
	if !strings.Contains(err.Error(), "--set KEY=VALUE") {
		t.Errorf("error should include remediation hint: %v", err)
	}
}

// Multiple missing keys are listed alphabetically — deterministic
// output is important for cobra-side regression assertions + operator
// scanability.
func TestValidateContract_RequiredEnv_MissingListedAlphabetically(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	err := validateContract(
		ports.ScriptFluxInstall,
		nil,
		[]string{"cluster-a"},
	)
	if err == nil {
		t.Fatal("expected ErrMissingRequiredEnv")
	}
	// fail with "GITHUB_TOKEN, KUBECONFIG" — alphabetical.
	if !strings.Contains(err.Error(), "GITHUB_TOKEN, KUBECONFIG") {
		t.Errorf("missing keys should be alphabetical: %v", err)
	}
}

// Arg-count: too-few positional args surfaces ErrInvalidArgCount with
// the usage hint.
func TestValidateContract_ArgCount_TooFew(t *testing.T) {
	t.Setenv("KUBECONFIG", "/k")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC, nil, nil, // needs 1, got 0
	)
	if err == nil {
		t.Fatal("expected ErrInvalidArgCount")
	}
	if !errors.Is(err, ports.ErrInvalidArgCount) {
		t.Errorf("err not ErrInvalidArgCount: %v", err)
	}
	if !strings.Contains(err.Error(), "<cluster-name>") {
		t.Errorf("error should embed usage shape: %v", err)
	}
}

// Arg-count: too-many positional args surfaces ErrInvalidArgCount.
func TestValidateContract_ArgCount_TooMany(t *testing.T) {
	t.Setenv("KUBECONFIG", "/k")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC, nil, []string{"a", "b"}, // max 1
	)
	if err == nil {
		t.Fatal("expected ErrInvalidArgCount")
	}
	if !errors.Is(err, ports.ErrInvalidArgCount) {
		t.Errorf("err not ErrInvalidArgCount: %v", err)
	}
}

// Arg-count: scripts that accept 0 positional args (e.g.
// install-prerequisites.sh) reject any args supplied. This catches
// "I passed cluster-name to the wrong script" calls.
func TestValidateContract_ArgCount_ZeroArgsScriptRejectsArgs(t *testing.T) {
	err := validateContract(
		ports.ScriptInstallPrereqs, nil, []string{"oops"},
	)
	if err == nil || !errors.Is(err, ports.ErrInvalidArgCount) {
		t.Errorf("expected ErrInvalidArgCount, got: %v", err)
	}
}

// Arg-count: zero-args, zero-required scripts pass.
func TestValidateContract_ArgCount_ZeroArgsScriptHappyPath(t *testing.T) {
	if err := validateContract(ports.ScriptInstallPrereqs, nil, nil); err != nil {
		t.Errorf("happy path should pass: %v", err)
	}
}

// Defensive: an unregistered ScriptKind passed to validateContract
// returns the canonical ErrUnknownScript sentinel (matches the parent
// defaultCmdFactory's lookup error).
func TestValidateContract_UnknownKind(t *testing.T) {
	err := validateContract(ports.ScriptKind("bogus.sh"), nil, nil)
	if err == nil || !errors.Is(err, ports.ErrUnknownScript) {
		t.Errorf("expected ErrUnknownScript, got: %v", err)
	}
}

// Resolve(): happy path returns the full contract + path for a known
// kind. Sorted RequiredEnv / OptionalEnv guards downstream rendering
// determinism.
func TestResolve_HappyPath(t *testing.T) {
	info, err := Resolve(ports.ScriptFluxInstall)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Kind != ports.ScriptFluxInstall {
		t.Errorf("Kind=%s want %s", info.Kind, ports.ScriptFluxInstall)
	}
	if info.Path != "bootstrap/flux-install.sh" {
		t.Errorf("Path=%q want bootstrap/flux-install.sh", info.Path)
	}
	if info.Root != RootFleet {
		t.Errorf("Root=%d want RootFleet", info.Root)
	}
	if !sort.StringsAreSorted(info.RequiredEnv) {
		t.Errorf("RequiredEnv not sorted: %v", info.RequiredEnv)
	}
	if !sort.StringsAreSorted(info.OptionalEnv) {
		t.Errorf("OptionalEnv not sorted: %v", info.OptionalEnv)
	}
	wantReq := []string{"GITHUB_TOKEN", "KUBECONFIG"}
	if !equalSlices(info.RequiredEnv, wantReq) {
		t.Errorf("RequiredEnv=%v want %v", info.RequiredEnv, wantReq)
	}
}

// Resolve(): unknown kind returns ErrUnknownScript.
func TestResolve_UnknownKind(t *testing.T) {
	_, err := Resolve(ports.ScriptKind("bogus.sh"))
	if err == nil || !errors.Is(err, ports.ErrUnknownScript) {
		t.Errorf("expected ErrUnknownScript, got: %v", err)
	}
}

// Resolve(): RootKubeDC kinds are surfaced as such (so plan / doctor
// can warn "this script lives in the kube-dc monorepo, not the
// fleet").
func TestResolve_RootKubeDC(t *testing.T) {
	info, err := Resolve(ports.ScriptOpenBaoSetupControllerAuth)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Root != RootKubeDC {
		t.Errorf("Root=%d want RootKubeDC", info.Root)
	}
	if info.Path != "hack/openbao-setup-controller-auth.sh" {
		t.Errorf("Path=%q want hack/openbao-setup-controller-auth.sh", info.Path)
	}
}

// Resolve(): the returned slices are COPIES so callers can't mutate
// the registry by sorting/appending to RequiredEnv. Mutation defence
// for a shared package-level map.
func TestResolve_ReturnedSlicesAreCopies(t *testing.T) {
	info, _ := Resolve(ports.ScriptFluxInstall)
	if len(info.RequiredEnv) > 0 {
		info.RequiredEnv[0] = "MUTATED"
	}
	again, _ := Resolve(ports.ScriptFluxInstall)
	if again.RequiredEnv[0] == "MUTATED" {
		t.Errorf("Resolve leaked a mutable reference to the registry")
	}
}

// End-to-end via defaultCmdFactory: a Run with no env + no args
// against a script that requires both surfaces the contract error,
// not a generic exec error.
func TestDefaultCmdFactory_ContractValidationFires(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	r := New("/fleet", "", nil)
	_, err := r.defaultCmdFactory(nil, ports.ScriptFluxInstall, nil, nil)
	if err == nil {
		t.Fatal("expected contract error")
	}
	if !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Errorf("expected ErrMissingRequiredEnv, got: %v", err)
	}
}

// === M3-T03 review-pass regression tests ===
//
// Three reviewer-surfaced findings fixed in the contract.go update
// that pairs with this file. Each gets a focused regression test so
// the bug shape is captured before fix and stays captured after.

// Reviewer P1 #1: empty required env values must be treated as
// MISSING, not "set but blank". Shell scripts test `[[ -z "$KEY" ]]`
// or `[ -n "${KEY:-}" ]`, NOT key presence — letting a `KEY=""` env
// satisfy validateContract leaves the typed-preflight guarantee
// useless because the script still fails at runtime.
func TestValidateContract_RequiredEnv_EmptyValueCountsAsMissing(t *testing.T) {
	t.Setenv("KUBECONFIG", "") // forbid host-env from satisfying

	err := validateContract(
		ports.ScriptFluxInstall,
		map[string]string{
			"GITHUB_TOKEN": "", // explicit empty — should NOT satisfy
			"KUBECONFIG":   "/k",
		},
		[]string{"cluster-a"},
	)
	if err == nil {
		t.Fatal("expected ErrMissingRequiredEnv for explicit empty value")
	}
	if !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Errorf("err not ErrMissingRequiredEnv: %v", err)
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("error should still name the empty-valued key: %v", err)
	}
}

// Same shape but covering host-env passthrough: an empty operator
// shell env for KUBECONFIG must NOT satisfy a required-KUBECONFIG
// script. (`lookupEnv` returns "" for unset OR empty; the loop's
// `lookupEnv(k) != ""` guard is what makes that work.)
func TestValidateContract_RequiredEnv_EmptyHostEnvDoesNotSatisfy(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC,
		nil,
		[]string{"cluster-a"},
	)
	if err == nil || !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Errorf("expected ErrMissingRequiredEnv when KUBECONFIG is empty in host env, got: %v", err)
	}
}

// Reviewer P1 (review-pass round 2): explicit empty in the caller env
// must OVERRIDE a non-empty host env, because mergeEnv writes host
// entries first then layers operator entries on top — `KEY=""` from
// the caller wins in the script's effective environment. The earlier
// "if env empty, fall through to host" path was wrong: it passed
// preflight while the script still saw KEY="" at runtime.
//
// Regression for:
//   - host KUBECONFIG=/valid
//   - call env KUBECONFIG=""
//   - expected: ErrMissingRequiredEnv (not satisfied by host)
func TestValidateContract_RequiredEnv_ExplicitEmptyOverridesHostEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "/operator/shell/valid-kubeconfig")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC,
		map[string]string{"KUBECONFIG": ""}, // caller explicitly empty
		[]string{"cluster-a"},
	)
	if err == nil {
		t.Fatal("expected ErrMissingRequiredEnv (explicit empty must NOT be satisfied by host fallback)")
	}
	if !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Errorf("err not ErrMissingRequiredEnv: %v", err)
	}
	if !strings.Contains(err.Error(), "KUBECONFIG") {
		t.Errorf("error should name the empty-overridden key: %v", err)
	}
}

// Same shape but absent (not empty) — host fallback DOES satisfy when
// the key is missing from env entirely. Guards the "absent vs.
// explicit empty" distinction this fix turns on.
func TestValidateContract_RequiredEnv_AbsentKeyAcceptsHostFallback(t *testing.T) {
	t.Setenv("KUBECONFIG", "/operator/shell/valid-kubeconfig")
	err := validateContract(
		ports.ScriptSetupKeycloakOIDC,
		map[string]string{}, // KUBECONFIG absent (not empty)
		[]string{"cluster-a"},
	)
	if err != nil {
		t.Errorf("absent KUBECONFIG should fall through to host env: %v", err)
	}
}

// Reviewer P1 #2: openbao-setup-controller-auth.sh requires CLUSTER +
// DOMAIN env (script body has `${CLUSTER:?...}` / `${DOMAIN:?...}`
// guards) and takes ZERO positional args. The earlier contract listed
// `<cluster-name>` as a positional arg and missed CLUSTER/DOMAIN
// entirely — this would let a caller make a contract-valid call that
// still failed at runtime.
func TestResolve_OpenBaoSetupControllerAuth_ContractMatchesScriptBody(t *testing.T) {
	info, err := Resolve(ports.ScriptOpenBaoSetupControllerAuth)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantReq := []string{"CLUSTER", "DOMAIN", "KUBECONFIG"}
	if !equalSlices(info.RequiredEnv, wantReq) {
		t.Errorf("RequiredEnv=%v want %v", info.RequiredEnv, wantReq)
	}
	if info.MinArgs != 0 || info.MaxArgs != 0 {
		t.Errorf("MinArgs/MaxArgs=%d/%d want 0/0 (script body takes no positional args)", info.MinArgs, info.MaxArgs)
	}
	if info.ArgDesc != "" {
		t.Errorf("ArgDesc=%q want empty (no positional args)", info.ArgDesc)
	}
	// Sanity-check optional env enumeration covers the
	// REFRESH_POLICY knob (M5-T03 in-place rewrites) — the most
	// load-bearing of the optional set.
	hasRefresh := false
	for _, k := range info.OptionalEnv {
		if k == "REFRESH_POLICY" {
			hasRefresh = true
			break
		}
	}
	if !hasRefresh {
		t.Errorf("OptionalEnv missing REFRESH_POLICY (M5-T03 knob): %v", info.OptionalEnv)
	}
}

// validateContract path for the same fix: calling with only KUBECONFIG
// (no CLUSTER/DOMAIN) surfaces ErrMissingRequiredEnv with both keys.
func TestValidateContract_OpenBaoSetupControllerAuth_RequiresClusterAndDomain(t *testing.T) {
	t.Setenv("KUBECONFIG", "/k")
	err := validateContract(
		ports.ScriptOpenBaoSetupControllerAuth,
		nil,
		nil,
	)
	if err == nil || !errors.Is(err, ports.ErrMissingRequiredEnv) {
		t.Fatalf("expected ErrMissingRequiredEnv, got: %v", err)
	}
	if !strings.Contains(err.Error(), "CLUSTER") || !strings.Contains(err.Error(), "DOMAIN") {
		t.Errorf("error should name both missing keys: %v", err)
	}
}

// Same script: passing a positional arg now (with the fixed contract)
// surfaces ErrInvalidArgCount — the script takes zero positional
// args, the earlier `<cluster-name>` shape was wrong.
func TestValidateContract_OpenBaoSetupControllerAuth_RejectsPositionalArg(t *testing.T) {
	t.Setenv("KUBECONFIG", "/k")
	env := map[string]string{"CLUSTER": "c", "DOMAIN": "d"}
	err := validateContract(
		ports.ScriptOpenBaoSetupControllerAuth,
		env,
		[]string{"some-cluster"},
	)
	if err == nil || !errors.Is(err, ports.ErrInvalidArgCount) {
		t.Errorf("expected ErrInvalidArgCount, got: %v", err)
	}
}

// Reviewer P2: add-engineer.sh supports BOTH `<name> <pubkey>` (the
// canonical FixHint shape) AND `--reencrypt` (operator-manual mode
// re-encrypting all secrets against the current .sops.yaml). The
// earlier contract pinned minArgs=2, blocking --reencrypt with
// ErrInvalidArgCount even though the script accepts it. With the
// broadened contract (1..2 args), --reencrypt now passes preflight.
//
// Note: this contract doesn't distinguish the two shapes — passing
// e.g. `[]string{"alice"}` would also pass, even though that's not a
// real script mode. Deferred until / unless a more expressive
// arg-pattern type lands; the CLI doesn't drive ScriptAddEngineer.Run
// today, this contract exists primarily for Resolve/preflight.
func TestValidateContract_AddEngineer_AllowsReencryptMode(t *testing.T) {
	err := validateContract(
		ports.ScriptAddEngineer,
		nil,
		[]string{"--reencrypt"},
	)
	if err != nil {
		t.Errorf("--reencrypt should satisfy add-engineer contract: %v", err)
	}
}

// Canonical mode still works.
func TestValidateContract_AddEngineer_CanonicalModeWorks(t *testing.T) {
	err := validateContract(
		ports.ScriptAddEngineer,
		nil,
		[]string{"alice", "age1xxxxxx"},
	)
	if err != nil {
		t.Errorf("canonical 2-arg shape should satisfy: %v", err)
	}
}

// Resolve surfaces the broadened usage shape so plan / help text
// makes both modes discoverable.
func TestResolve_AddEngineer_UsageMentionsReencrypt(t *testing.T) {
	info, err := Resolve(ports.ScriptAddEngineer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(info.ArgDesc, "--reencrypt") {
		t.Errorf("ArgDesc should mention --reencrypt: %q", info.ArgDesc)
	}
}

// equalSlices is a tiny helper used by TestResolve_HappyPath. Avoids
// pulling in reflect.DeepEqual for a 2-element string compare.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
