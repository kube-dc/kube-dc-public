// M4-T05 — auto-create the fleet's remote repository via a provider
// CLI (`gh` for GitHub, `glab` for GitLab) when the operator opts
// into `--fleet-mode=new-repo`.
//
// Runs as the FIRST mutating step in the `runApplyEngine` sequence
// so the remote exists by the time Apply's commit+push phase fires.
// Sits BEFORE:
//
//   - Apply's clean-tree gate + Scaffold (those work against the
//     LOCAL fleet checkout; a missing REMOTE would only surface at
//     git.Push time, which is after Scaffold has committed local
//     state — a rollback there is more expensive).
//   - `bootstrap/flux-install.sh` (needs a live remote to point Flux
//     at).
//
// **Multi-provider** — dispatches on `Provider`:
//   - `github` (default): shells out to `gh repo create <owner>/<name>
//     --private`, honours `GH_TOKEN`, sniffs GitHub's "already exists"
//     stderr wording (multiple variants; case-insensitive).
//   - `gitlab`: shells out to `glab repo create <owner>/<name>
//     --private`, honours `GITLAB_TOKEN`, sniffs GitLab's own
//     already-exists wordings ("has already been taken", "path is
//     already taken", plus the generic "already exists" that glab
//     sometimes emits).
//
// Both providers use the same subcommand shape (`<cli> repo create
// <owner>/<name> --private`) because gh and glab converged on it —
// which keeps the dispatch table small. Future providers (Gitea?
// Bitbucket?) would slot in the same providerConfig struct.
//
// **Not called in dry-run mode** — same discipline as the DNS + NFD
// gates: dry-run has no side effects, and creating a remote repo
// is definitely a side effect. The plan render still notes the
// create WILL happen at apply time so the operator isn't surprised.
//
// **Idempotent** — the create attempt swallows the provider's
// already-exists response as success. This handles two legitimate
// cases:
//
//   1. The operator ran `init --apply` once, it failed after this
//      step (e.g. Scaffold refused), and they're re-running. The
//      remote exists from the prior attempt; no reason to fail.
//
//   2. The operator manually created the repo before running init
//      (e.g. their org policy requires humans to create repos, and
//      they want the CLI to skip the create step but not require
//      passing `--no-create-repo`).
//
// **Never called on `--no-create-repo`** — that flag is the escape
// hatch for operators who manage repo creation externally (SSO'd
// terraform module, org-level GitHub App, whatever). The engine
// respects it verbatim; the flag's presence in the plan input hash
// means dry-run/apply-plan drift catches operators who set it in
// one run but not the other.

package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Provider identifies which remote-repo hosting service to talk to.
// The zero value (empty string) is treated as ProviderGitHub for
// backward compatibility with pre-multi-provider callers, but any
// OTHER unrecognised value fails loudly via ErrCreateRepoUnknownProvider
// (see resolveProvider) — a silent fall-through would mask typos
// like --provider=gitub. Callers should set Provider explicitly;
// the zero-value tolerance is only a backward-compat safety net.
type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderGitLab Provider = "gitlab"
)

// providerConfig captures per-provider CLI + token + idempotency
// idioms. Kept in a lookup table so adding a new provider (Gitea,
// Bitbucket) is a single-entry change rather than a new engine.
type providerConfig struct {
	// displayName is what the operator sees in log lines
	// ("GitHub" / "GitLab"). Never used programmatically.
	displayName string

	// cli is the executable name the engine expects on $PATH
	// (`gh` / `glab`).
	cli string

	// tokenEnvVar is the env variable the CLI honours for
	// non-interactive auth (`GH_TOKEN` / `GITLAB_TOKEN`).
	tokenEnvVar string

	// scrubEnvVars is the list of env-var PREFIXES stripped from
	// the base env before invoking the CLI. Includes the provider's
	// own primary + common aliases (`GITHUB_TOKEN=` alongside
	// `GH_TOKEN=`; `GL_TOKEN=` alongside `GITLAB_TOKEN=`).
	scrubEnvVars []string

	// alreadyExistsMarkers are lowercase substrings the engine
	// looks for in stderr to decide "create failed but the repo is
	// already there → success". Multiple markers per provider
	// because both gh and glab drift their error wording across
	// versions.
	alreadyExistsMarkers []string
}

var providerConfigs = map[Provider]providerConfig{
	ProviderGitHub: {
		displayName: "GitHub",
		cli:         "gh",
		tokenEnvVar: "GH_TOKEN",
		// GITHUB_TOKEN is the older name gh honours; scrub both so a
		// stale shell token can't overrule the resolved value.
		scrubEnvVars:         []string{"GH_TOKEN=", "GITHUB_TOKEN="},
		alreadyExistsMarkers: []string{"already exists"},
	},
	ProviderGitLab: {
		displayName: "GitLab",
		cli:         "glab",
		tokenEnvVar: "GITLAB_TOKEN",
		// GL_TOKEN is a glab-specific alias; strip alongside the
		// canonical GITLAB_TOKEN so token discipline mirrors GitHub.
		scrubEnvVars: []string{"GITLAB_TOKEN=", "GL_TOKEN="},
		// GitLab's uniqueness errors:
		//   - "has already been taken"  (Rails validator wording;
		//     shows up on both name + path fields)
		//   - "path is already taken"   (variant when the SLUG
		//     collides but the NAME differs)
		//   - "already exists"          (bare glab wording when it
		//     surfaces the raw API error verbatim)
		alreadyExistsMarkers: []string{
			"has already been taken",
			"path is already taken",
			"already exists",
		},
	},
}

// resolveProvider maps the caller's Provider value (possibly empty)
// to a providerConfig. Empty → GitHub for backward compatibility
// with the pre-multi-provider callers. Unknown non-empty values
// return an error rather than silently defaulting — a typoed
// `--provider=gitub` would otherwise create GitHub repos when the
// operator meant something else.
func resolveProvider(p Provider) (providerConfig, error) {
	if p == "" {
		return providerConfigs[ProviderGitHub], nil
	}
	cfg, ok := providerConfigs[p]
	if !ok {
		return providerConfig{}, fmt.Errorf("%w: unknown provider %q (want %q or %q)",
			ErrCreateRepoUnknownProvider, string(p),
			string(ProviderGitHub), string(ProviderGitLab))
	}
	return cfg, nil
}

// CreateRepoOptions is the parameter bundle for CreateRemoteRepo.
type CreateRepoOptions struct {
	// Provider selects the hosting service. Empty defaults to
	// GitHub (backward compat with pre-multi-provider callers);
	// unrecognised non-empty values fail via
	// ErrCreateRepoUnknownProvider (no silent fall-through).
	Provider Provider

	// Owner is the account, org, or group namespace that will own
	// the repo. Populated from --github-owner on InitOptions (the
	// flag name is provider-neutral in spirit even though it's
	// spelled github-* for legacy reasons).
	Owner string

	// Name is the repo name (short — not owner/name). Populated
	// from --github-repo on InitOptions.
	Name string

	// Token is the PAT the provider CLI will use to authenticate
	// the create call. Empty means fall back to the CLI's own
	// credential store (`gh auth login` / `glab auth login`). Never
	// logged.
	Token string

	// Out is the operator-facing log writer. Nil is safe (io.Discard).
	Out io.Writer

	// runner is a test seam. Nil in production = use exec.Command
	// directly against the system CLI. Tests inject a fake that
	// records call args + returns canned exit codes.
	runner cliRunner
}

// cliRunner is a test-seam adapter over provider-CLI invocations.
// Real impl is exec.CommandContext; fake in tests records calls
// without forking. Same signature for gh and glab because their
// invocation shapes are congruent.
type cliRunner interface {
	Run(ctx context.Context, cli string, env []string, args ...string) (stderr []byte, err error)
}

// realCliRunner is the production cliRunner. Shells out to the
// provider's CLI in the operator's $PATH.
type realCliRunner struct{}

func (realCliRunner) Run(ctx context.Context, cli string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.Bytes(), err
}

// --- Errors ---

// ErrCreateRepoMissingDependency surfaces on nil runner / empty
// owner / empty name. Caller wiring bug — cobra populates these
// unconditionally on the new-repo path.
var ErrCreateRepoMissingDependency = errors.New("init: create-repo missing required dependency")

// ErrCreateRepoUnknownProvider surfaces when Provider is set to an
// unrecognised value (typo, future provider that the operator's
// binary doesn't know about yet).
var ErrCreateRepoUnknownProvider = errors.New("init: create-repo unknown provider")

// ErrCreateRepoCLIMissing wraps `<cli> not found` (exec.LookPath
// failure). The wrapped message names the specific CLI (`gh` /
// `glab`) so operators aren't left guessing which tool to install.
// Distinct error so operators without the CLI see the installer +
// gate mismatch rather than a raw exec error.
var ErrCreateRepoCLIMissing = errors.New("init: provider CLI not found on $PATH")

// ErrCreateRepoFailed wraps a non-idempotent `<cli> repo create`
// failure (auth denied, org disallows repo creation, etc). The
// wrapped error carries the CLI stderr for operator triage.
var ErrCreateRepoFailed = errors.New("init: `repo create` failed")

// --- Engine ---

// CreateRemoteRepo idempotently creates `<owner>/<name>` on the
// selected provider via its CLI. Returns nil when:
//
//   - The repo was created successfully (fresh case), OR
//   - The repo already existed (provider's already-exists response).
//
// Returns a wrapped error otherwise — auth failures, org policy
// denials, CLI missing, network errors, etc. The wrapped error
// carries the CLI stderr so the operator sees actionable output.
//
// Never mutates any local state (no clone, no local git init). The
// caller is responsible for the local `--repo` path being an
// initialized git repo with the remote already configured by the
// time Apply's phases 4-5 (commit + push) fire.
func CreateRemoteRepo(ctx context.Context, opts CreateRepoOptions) error {
	if err := validateCreateRepoOptions(opts); err != nil {
		return err
	}
	cfg, err := resolveProvider(opts.Provider)
	if err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	runner := opts.runner
	if runner == nil {
		runner = realCliRunner{}
	}

	// Fast fail if the CLI isn't installed — surface the specific
	// error before we shell out with a maybe-useless PATH.
	if _, err := exec.LookPath(cfg.cli); err != nil {
		if opts.runner == nil {
			// Only fire this branch in production. Test seam always
			// supplies a runner, and injecting exec.LookPath into
			// the seam would over-engineer this.
			return fmt.Errorf("%w: `%s` (install it or pass --no-create-repo): %v",
				ErrCreateRepoCLIMissing, cfg.cli, err)
		}
	}

	target := fmt.Sprintf("%s/%s", opts.Owner, opts.Name)
	fmt.Fprintf(out, "[apply] create-repo: ensuring %s exists on %s (%s)\n",
		target, cfg.displayName, cfg.cli)

	// Idempotent create — a bare `<cli> repo create` fails loudly if
	// the repo exists. We could pre-check with a view call, but
	// that's two subprocess spawns for the common case. Instead try
	// create, and treat the provider's already-exists response as
	// success. Stderr sniffing is the standard idiom for both gh
	// and glab — exit codes are generic, the messages are stable.
	env := envWithProviderToken(cfg, opts.Token)
	args := []string{"repo", "create", target, "--private"}
	stderr, err := runner.Run(ctx, cfg.cli, env, args...)
	if err == nil {
		fmt.Fprintf(out, "[apply] create-repo: created %s on %s (private)\n",
			target, cfg.displayName)
		return nil
	}
	if isAlreadyExistsFor(cfg, stderr) {
		fmt.Fprintf(out, "[apply] create-repo: %s already exists on %s — treating as success\n",
			target, cfg.displayName)
		return nil
	}
	// Real failure — wrap with stderr so the operator sees the CLI's
	// own error message (auth denied / rate-limited / org policy /
	// invalid name).
	return fmt.Errorf("%w: %s (%s): %v (stderr: %s)",
		ErrCreateRepoFailed, target, cfg.displayName, err,
		strings.TrimSpace(string(stderr)))
}

// CreateGitHubRepo is a compatibility alias — pre-multi-provider
// callers referenced this name. Delegates to CreateRemoteRepo with
// Provider=GitHub. Prefer CreateRemoteRepo + Provider in new code.
func CreateGitHubRepo(ctx context.Context, opts CreateRepoOptions) error {
	if opts.Provider == "" {
		opts.Provider = ProviderGitHub
	}
	return CreateRemoteRepo(ctx, opts)
}

// validateCreateRepoOptions is the same defensive shape as every
// other clusterinit engine — caller-wiring-bug guard.
func validateCreateRepoOptions(opts CreateRepoOptions) error {
	if opts.Owner == "" {
		return fmt.Errorf("%w: Owner", ErrCreateRepoMissingDependency)
	}
	if opts.Name == "" {
		return fmt.Errorf("%w: Name", ErrCreateRepoMissingDependency)
	}
	return nil
}

// envWithProviderToken returns osEnviron() minus any provider-scoped
// stale tokens, plus <cfg.tokenEnvVar>=<token> when non-empty. Same
// discipline for all providers: the resolved --github-token wins
// over whatever the operator's shell had set.
func envWithProviderToken(cfg providerConfig, token string) []string {
	base := scrubProviderTokenEnv(cfg)
	if token == "" {
		return base
	}
	return append(base, cfg.tokenEnvVar+"="+token)
}

// scrubProviderTokenEnv strips every prefix in cfg.scrubEnvVars
// from the base env so a per-call token has authority.
func scrubProviderTokenEnv(cfg providerConfig) []string {
	var out []string
	base := osEnviron()
next:
	for _, kv := range base {
		for _, prefix := range cfg.scrubEnvVars {
			if strings.HasPrefix(kv, prefix) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// osEnviron is a package-local seam over os.Environ so tests can
// control the base environment without leaking real host state
// into the generated env slice. Tests override this via
// swapEnviron() in create_repo_test.go; production defers to
// os.Environ.
var osEnviron = os.Environ

// isAlreadyExistsFor sniffs stderr for any of the provider's
// already-exists markers. Substring match on lowercased stderr —
// both gh and glab include the phrase in a human-readable line,
// and casing varies across versions.
//
// A false positive here would mask a genuine create failure as
// success. But the markers are all provider-specific error phrases
// unlikely to appear in unrelated failure text — the mitigating
// factor for the multi-marker sniff is that a follow-up push
// failure would surface within the same command if the repo
// really wasn't there.
func isAlreadyExistsFor(cfg providerConfig, stderr []byte) bool {
	lowered := bytes.ToLower(stderr)
	for _, marker := range cfg.alreadyExistsMarkers {
		if bytes.Contains(lowered, []byte(marker)) {
			return true
		}
	}
	return false
}

// isAlreadyExists is the pre-multi-provider entry point kept as a
// GitHub-only convenience for the existing test coverage. Prefer
// isAlreadyExistsFor(cfg, stderr) in new code.
func isAlreadyExists(stderr []byte) bool {
	return isAlreadyExistsFor(providerConfigs[ProviderGitHub], stderr)
}
