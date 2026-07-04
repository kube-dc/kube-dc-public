// Per-ScriptKind validation contract (M3-T03).
//
// M3-T01 shipped a ScriptKind → (root, path) registry. This file
// layers a second registry on top: ScriptKind → required env keys +
// accepted positional arg shape. Validation runs in defaultCmdFactory
// BEFORE exec.Cmd builds, so a misconfigured call surfaces as a
// typed error from `ports` rather than a bash-side fail-deep-inside-
// the-script.
//
// **Why this matters for M4-T10 and M4-T12** (the scaffold + apply
// engine slices): they sequence multiple ScriptRunner.Run calls
// (add-cluster → flux-install → setup-keycloak-oidc) and need to
// know AT PLAN TIME which env knobs the operator must supply. Without
// per-kind validation, a missing GITHUB_TOKEN would only manifest
// half-way through `kube-dc bootstrap init` — after the
// add-cluster.sh commit had already landed locally. With it, the
// preflight (Resolve) tells the operator everything they need before
// any disk-mutating step runs.
//
// **Host-env passthrough satisfies required keys.** mergeEnv layers
// the operator's host KUBECONFIG, PATH, etc. onto every Run call's
// env. validateContract honours that: a script that requires
// KUBECONFIG is satisfied either by an explicit Run env entry OR by
// the operator's shell env (the same effective env the script sees).
// That keeps the validation in lockstep with what the script
// actually runs against, avoiding a "validator says missing /
// script sees it" inconsistency.

package script

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// scriptContract describes the operator-visible interface of a
// fleet/hack script: required env keys, optional env (documented but
// not validated), and positional-arg bounds. The argDesc string is
// the doc-style usage line surfaced via Resolve for plan preflight
// and `--help` output.
type scriptContract struct {
	// requiredEnv lists keys that MUST be present in the effective
	// env (operator Run env merged with host-env passthrough).
	// Sorted alphabetically — Resolve returns the slice as-is so
	// downstream callers (plan preview, doctor preflight) render a
	// deterministic order.
	requiredEnv []string

	// optionalEnv lists keys the script honours but doesn't require.
	// Not validated; published via Resolve for documentation.
	optionalEnv []string

	// minArgs / maxArgs bound the positional arg count. maxArgs=-1
	// means unbounded.
	minArgs int
	maxArgs int

	// argDesc is a doc-style description of the positional args
	// (matches the script's own `Usage:` header where one exists).
	// Empty string ⇒ the script takes no positional args.
	argDesc string
}

// scriptContracts is the per-kind registry. Every ScriptKind that
// appears in scriptPaths MUST appear here too — TestContracts_
// CoversAllExportedKinds in contract_test.go is the static guard.
//
// Sources for each entry:
//   - install-server.sh / install-agent.sh: header docs in
//     kube-dc-fleet/bootstrap/rke2/install-{server,agent}.sh.
//   - generate-age-key.sh / add-cluster.sh / add-engineer.sh /
//     install-prerequisites.sh / dump-cluster-state.sh: their
//     own `Usage:` headers + body env lookups.
//   - flux-install.sh: header "Requires: KUBECONFIG, provider PAT".
//     KUBECONFIG is universally required; the token env var is
//     provider-conditional (GITHUB_TOKEN for github, GITLAB_TOKEN
//     for gitlab). Both are OPTIONAL in the contract table so a
//     KUBE_DC_PROVIDER=gitlab preflight isn't refused for missing
//     GITHUB_TOKEN; the fleet script's own auth branch enforces
//     the right one is present based on KUBE_DC_PROVIDER.
//   - openbao-init.sh: needs KUBECONFIG to exec into the openbao
//     pod; NAMESPACE/POD/KEY_SHARES/KEY_THRESHOLD are knobs with
//     sensible defaults.
//   - setup-keycloak-oidc.sh: needs KUBECONFIG to read the
//     keycloak admin password Secret from cluster-secrets /
//     keycloak Secret; admin password isn't an operator env knob
//     (it's discovered).
//   - openbao-setup-controller-auth.sh: same — needs KUBECONFIG.
//     REFRESH_POLICY is an optional knob for M5-T03 in-place
//     rewrites.
var scriptContracts = map[ports.ScriptKind]scriptContract{
	ports.ScriptInstallServer: {
		optionalEnv: []string{
			"RKE2_VERSION", "NODE_NAME", "NODE_IP", "EXTERNAL_IP",
			"DOMAIN", "CLUSTER_DOMAIN", "POD_CIDR", "SERVICE_CIDR", "CLUSTER_DNS",
		},
		minArgs: 0,
		maxArgs: 2,
		argDesc: "[<join-token> <server-ip>]  (omit for first server)",
	},
	ports.ScriptInstallAgent: {
		optionalEnv: []string{"RKE2_VERSION", "NODE_NAME", "CP_PORT"},
		minArgs:     2,
		maxArgs:     3,
		argDesc:     "<token> <server-ip> [<node-ip>]",
	},
	ports.ScriptGenerateAgeKey: {
		minArgs: 0,
		maxArgs: 0,
	},
	ports.ScriptAddCluster: {
		minArgs: 3,
		maxArgs: 4,
		argDesc: "<name> <domain> <node-external-ip> [<kubeconfig-path>]",
	},
	ports.ScriptFluxInstall: {
		// Only KUBECONFIG is universally required. Token env is
		// provider-conditional (GITHUB_TOKEN for github, GITLAB_TOKEN
		// for gitlab); the fleet script's own auth branch enforces
		// the right one is set based on KUBE_DC_PROVIDER. Keeping
		// both as OPTIONAL here so a preflight against a
		// KUBE_DC_PROVIDER=gitlab run doesn't spuriously refuse for
		// missing GITHUB_TOKEN.
		requiredEnv: []string{"KUBECONFIG"},
		optionalEnv: []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "KUBE_DC_PROVIDER", "GITHUB_OWNER", "GITHUB_REPO"},
		minArgs:     1,
		maxArgs:     2,
		argDesc:     "<cluster-name> [--new-cluster]",
	},
	ports.ScriptOpenBaoInit: {
		requiredEnv: []string{"KUBECONFIG"},
		optionalEnv: []string{"NAMESPACE", "POD", "KEY_SHARES", "KEY_THRESHOLD"},
		minArgs:     0,
		maxArgs:     0,
	},
	ports.ScriptSetupKeycloakOIDC: {
		requiredEnv: []string{"KUBECONFIG"},
		minArgs:     1,
		maxArgs:     1,
		argDesc:     "<cluster-name>",
	},
	ports.ScriptOpenBaoSetupControllerAuth: {
		// hack/openbao-setup-controller-auth.sh takes ZERO positional
		// args. CLUSTER + DOMAIN are env-only (the script's body uses
		// `${CLUSTER:?...}` / `${DOMAIN:?...}` to bail out without
		// either). KUBECONFIG is also required — explicit `[ -n
		// "${KUBECONFIG:-}" ]` guard in the script body. The earlier
		// contract listed a `<cluster-name>` positional arg; that
		// was wrong and the M3-T03 review caught it.
		requiredEnv: []string{"CLUSTER", "DOMAIN", "KUBECONFIG"},
		optionalEnv: []string{
			"KUBE_DC_NS", "MANAGER_SA", "ROLE_NAME", "POLICY_NAME",
			"DB_MANAGER_NS", "DB_MANAGER_SA", "DB_MANAGER_ROLE_NAME", "DB_MANAGER_POLICY_NAME",
			"SHARE_THRESHOLD", "FLEET_DIR", "REFRESH_POLICY", "SOPS_FILE", "OPENBAO_NS",
		},
		minArgs: 0,
		maxArgs: 0,
	},
	ports.ScriptAddEngineer: {
		// add-engineer.sh has two call shapes:
		//   1. `<name> <age-public-key>`  — add a new recipient and
		//      re-encrypt all secrets (the canonical CLI-driven
		//      shape, surfaced as a FixHint by the doctor probe).
		//   2. `--reencrypt`              — re-encrypt all secrets
		//      against the current .sops.yaml recipients (operator-
		//      manual, used after editing .sops.yaml by hand).
		// minArgs=1 admits the --reencrypt mode without blocking it;
		// maxArgs=2 keeps the canonical mode. The two shapes have
		// disjoint first-arg values ("--reencrypt" vs. an engineer
		// name), so the contract can't reject one without rejecting
		// the other unless we add a more expressive arg-pattern type
		// (deferred — the CLI doesn't drive this script Run() today;
		// the contract exists for preflight existence + Resolve).
		minArgs: 1,
		maxArgs: 2,
		argDesc: "<name> <age-public-key>  |  --reencrypt",
	},
	ports.ScriptInstallPrereqs: {
		minArgs: 0,
		maxArgs: 0,
	},
	ports.ScriptDumpClusterState: {
		requiredEnv: []string{"KUBECONFIG"},
		minArgs:     0,
		maxArgs:     1,
		argDesc:     "[<cluster-name>]",
	},
}

// hostEnvPassthrough is the set of host-env keys mergeEnv layers onto
// every Run call. Kept in sync with the slice in mergeEnv (a separate
// test asserts the two stay aligned). Required-env validation honours
// this set: a script that requires KUBECONFIG is satisfied if the
// operator's shell env supplies it, even if the engine didn't pass it
// explicitly in the env map.
var hostEnvPassthrough = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LANG": {}, "LC_ALL": {},
	"TERM": {}, "SSH_AUTH_SOCK": {}, "KUBECONFIG": {},
}

// validateContract refuses calls that don't satisfy the per-kind
// contract. Called from defaultCmdFactory BEFORE the exec.Cmd builds
// so the operator gets a typed ports.ErrMissingRequiredEnv /
// ErrInvalidArgCount rather than a script-internal fail.
//
// An unknown ScriptKind here panics in development (registry
// inconsistency caught by TestContracts_CoversAllExportedKinds) but
// returns ErrUnknownScript in release — defensive coverage for a
// future commit that adds a path without a contract entry.
func validateContract(name ports.ScriptKind, env map[string]string, args []string) error {
	c, ok := scriptContracts[name]
	if !ok {
		return fmt.Errorf("%w: missing contract for %s", ports.ErrUnknownScript, name)
	}

	// Empty string counts as "missing" because the scripts test
	// non-empty values, not key presence — e.g. flux-install.sh does
	// `[[ -z "${GITHUB_TOKEN:-}" ]]` and
	// openbao-setup-controller-auth.sh does
	// `[ -n "${KUBECONFIG:-}" ]`. Treating `KEY=""` as set would let
	// validateContract pass while the script still fails at runtime,
	// defeating the M3-T03 typed-preflight guarantee.
	//
	// **Empty in `env` blocks host fallback.** mergeEnv writes
	// host-env entries FIRST then layers operator-supplied `env`
	// entries on top — `KEY=""` from the caller therefore OVERRIDES a
	// non-empty host KEY in the script's effective environment. So
	// when env[k] is explicitly set, its emptiness/non-emptiness is
	// authoritative; the host-env fallback only applies when env[k]
	// is absent. Without this guard, a call like
	//   validateContract(K, map[string]string{"KUBECONFIG": ""}, …)
	// against a shell with `KUBECONFIG=/valid` would pass preflight
	// but the script would see KUBECONFIG="" and bail.
	var missing []string
	for _, k := range c.requiredEnv {
		if v, ok := env[k]; ok {
			if v != "" {
				continue
			}
			// Explicit empty in the caller env — host fallback can't
			// save this because mergeEnv would let the explicit empty
			// win.
			missing = append(missing, k)
			continue
		}
		if _, allow := hostEnvPassthrough[k]; allow {
			if lookupEnv(k) != "" {
				continue
			}
		}
		missing = append(missing, k)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"%w: %s requires %s (pass via --set KEY=VALUE or export in shell env)",
			ports.ErrMissingRequiredEnv, name, strings.Join(missing, ", "),
		)
	}

	usage := c.argDesc
	if usage == "" {
		usage = "(no positional args)"
	}
	if len(args) < c.minArgs {
		return fmt.Errorf(
			"%w: %s needs at least %d positional arg(s), got %d; usage: %s %s",
			ports.ErrInvalidArgCount, name, c.minArgs, len(args), name, usage,
		)
	}
	if c.maxArgs >= 0 && len(args) > c.maxArgs {
		return fmt.Errorf(
			"%w: %s accepts at most %d positional arg(s), got %d; usage: %s %s",
			ports.ErrInvalidArgCount, name, c.maxArgs, len(args), name, usage,
		)
	}
	return nil
}

// ScriptInfo is the public projection of an entry in the M3-T03
// registry. Plan / doctor / `--help` consumers call Resolve(kind) to
// surface required env + accepted args at preflight time, before any
// Run-level validation fires.
type ScriptInfo struct {
	// Kind is the registered identifier.
	Kind ports.ScriptKind

	// Root is the script's repo root (RootFleet / RootKubeDC).
	Root ScriptRoot

	// Path is the script's path relative to Root (e.g.
	// "bootstrap/setup-keycloak-oidc.sh").
	Path string

	// RequiredEnv lists env keys validateContract enforces. Sorted
	// alphabetically for stable rendering.
	RequiredEnv []string

	// OptionalEnv lists documented but unvalidated knobs.
	OptionalEnv []string

	// MinArgs / MaxArgs match the contract bounds.
	MinArgs int
	MaxArgs int

	// ArgDesc is the doc-style positional-arg usage line.
	ArgDesc string
}

// Resolve returns the public ScriptInfo for a registered ScriptKind.
// Used by:
//   - M4 plan preview to render required env per planned script
//     ("will run flux-install.sh which requires GITHUB_TOKEN").
//   - M1 doctor preflight to surface missing knobs alongside missing
//     binaries before init runs.
//   - cobra `--help` for `kube-dc bootstrap` subcommands that
//     forward to one specific script.
//
// Returns ports.ErrUnknownScript if the kind isn't registered (e.g.
// the caller is on an older CLI than the engine slice that introduced
// the kind).
func Resolve(kind ports.ScriptKind) (ScriptInfo, error) {
	loc, ok := scriptPaths[kind]
	if !ok {
		return ScriptInfo{}, fmt.Errorf("%w: %s", ports.ErrUnknownScript, kind)
	}
	c, ok := scriptContracts[kind]
	if !ok {
		return ScriptInfo{}, fmt.Errorf("%w: missing contract for %s", ports.ErrUnknownScript, kind)
	}
	info := ScriptInfo{
		Kind:        kind,
		Root:        loc.root,
		Path:        loc.path,
		RequiredEnv: append([]string(nil), c.requiredEnv...),
		OptionalEnv: append([]string(nil), c.optionalEnv...),
		MinArgs:     c.minArgs,
		MaxArgs:     c.maxArgs,
		ArgDesc:     c.argDesc,
	}
	sort.Strings(info.RequiredEnv)
	sort.Strings(info.OptionalEnv)
	return info, nil
}
