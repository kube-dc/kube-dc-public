package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGhRunner records every CLI invocation + returns a canned
// (stderr, err) pair. `err` non-nil simulates a non-zero exit;
// `stderr` is what the CLI would have printed to fd 2 (used by the
// already-exists sniff). Name kept as fakeGhRunner for continuity
// with the pre-multi-provider tests; also used for GitLab
// invocations (records `lastCli` so gitlab tests can assert on
// which binary was called).
type fakeGhRunner struct {
	// canned response
	stderrOut []byte
	err       error

	// captured
	calls    int
	lastCli  string
	lastArgs []string
	lastEnv  []string
}

func (f *fakeGhRunner) Run(_ context.Context, cli string, env []string, args ...string) ([]byte, error) {
	f.calls++
	f.lastCli = cli
	f.lastArgs = append([]string(nil), args...)
	f.lastEnv = append([]string(nil), env...)
	return f.stderrOut, f.err
}

// swapEnviron pins osEnviron to a canned slice so token-scrub tests
// don't depend on the developer's shell state. Auto-restores via
// t.Cleanup.
func swapEnviron(t *testing.T, env []string) {
	t.Helper()
	prev := osEnviron
	osEnviron = func() []string { return env }
	t.Cleanup(func() { osEnviron = prev })
}

// TestCreateGitHubRepo_HappyPath_CreatesPrivateRepo — canonical fresh
// case: gh exits 0; we log the create + return nil.
func TestCreateGitHubRepo_HappyPath_CreatesPrivateRepo(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{} // exits 0 by default
	var out bytes.Buffer
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "acme-corp",
		Name:   "kdc-fleet",
		Token:  "ghp_fake-token",
		Out:    &out,
		runner: fake,
	})
	if err != nil {
		t.Fatalf("happy path: unexpected error %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("gh should be called exactly once, got %d", fake.calls)
	}
	// Provider dispatch: default (empty Provider) → gh.
	if fake.lastCli != "gh" {
		t.Errorf("default provider should call `gh`, got %q", fake.lastCli)
	}
	// Command shape: `gh repo create acme-corp/kdc-fleet --private`.
	wantArgs := []string{"repo", "create", "acme-corp/kdc-fleet", "--private"}
	if !equalStringsCR(fake.lastArgs, wantArgs) {
		t.Errorf("wrong gh args: got %v, want %v", fake.lastArgs, wantArgs)
	}
	// Token propagated via env.
	sawToken := false
	for _, kv := range fake.lastEnv {
		if kv == "GH_TOKEN=ghp_fake-token" {
			sawToken = true
			break
		}
	}
	if !sawToken {
		t.Errorf("GH_TOKEN not propagated via env; env=%v", fake.lastEnv)
	}
	// Log line names the target + the private flag.
	if !strings.Contains(out.String(), "acme-corp/kdc-fleet") {
		t.Errorf("log should name the target repo; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "created") {
		t.Errorf("log should confirm creation; out=%q", out.String())
	}
}

// TestCreateGitHubRepo_AlreadyExists_TreatedAsSuccess — GitHub 422
// on repeat-create. The stderr contains "already exists"; engine
// swallows it and returns nil (idempotency contract).
func TestCreateGitHubRepo_AlreadyExists_TreatedAsSuccess(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{
		stderrOut: []byte("GraphQL: Name already exists on this account (createRepository)\n"),
		err:       errors.New("exit status 1"),
	}
	var out bytes.Buffer
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "acme-corp",
		Name:   "kdc-fleet",
		Out:    &out,
		runner: fake,
	})
	if err != nil {
		t.Fatalf("already-exists should be success, got %v", err)
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("log should mention already-exists; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "treating as success") {
		t.Errorf("log should surface the idempotency decision; out=%q", out.String())
	}
}

// TestCreateGitHubRepo_AlreadyExistsCaseInsensitive — GitHub's
// error wording varies across gh versions (some lowercase, some
// title-case). Sniff must be robust.
func TestCreateGitHubRepo_AlreadyExistsCaseInsensitive(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	for _, msg := range []string{
		"HTTP 422: Repository name ALREADY EXISTS on this account",
		"Already Exists — pass -f to update",
		"name already exists",
	} {
		t.Run(msg, func(t *testing.T) {
			fake := &fakeGhRunner{
				stderrOut: []byte(msg),
				err:       errors.New("exit status 1"),
			}
			err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
				Owner:  "a", Name: "b", runner: fake,
			})
			if err != nil {
				t.Errorf("expected already-exists sniff to match %q, got %v", msg, err)
			}
		})
	}
}

// TestCreateGitHubRepo_AuthError_Propagates — gh 401 (bad token /
// no auth) MUST NOT be swallowed. Engine surfaces
// ErrCreateRepoFailed with stderr in the wrap so the operator sees
// what gh actually said.
func TestCreateGitHubRepo_AuthError_Propagates(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{
		stderrOut: []byte("HTTP 401: Bad credentials\n"),
		err:       errors.New("exit status 1"),
	}
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		Name:   "b",
		Token:  "bad",
		runner: fake,
	})
	if err == nil {
		t.Fatal("expected auth failure, got nil")
	}
	if !errors.Is(err, ErrCreateRepoFailed) {
		t.Errorf("expected ErrCreateRepoFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in wrapped error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("expected stderr in wrapped error, got %v", err)
	}
}

// TestCreateGitHubRepo_NonAlreadyExistsFailure_Propagates — a
// generic failure without the already-exists marker must NOT be
// swallowed. Guards against a future regression where a broken
// sniff string starts eating real errors.
func TestCreateGitHubRepo_NonAlreadyExistsFailure_Propagates(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{
		stderrOut: []byte("network unreachable\n"),
		err:       errors.New("exit status 1"),
	}
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		Name:   "b",
		runner: fake,
	})
	if err == nil {
		t.Fatal("expected propagated failure, got nil")
	}
	if !errors.Is(err, ErrCreateRepoFailed) {
		t.Errorf("expected ErrCreateRepoFailed, got %v", err)
	}
}

// TestCreateGitHubRepo_EnvScrubsPreExistingGHToken — the operator's
// shell may carry a stale GH_TOKEN that WOULD authenticate as the
// wrong identity. Engine scrubs it and lets the resolved --github-
// token win.
func TestCreateGitHubRepo_EnvScrubsPreExistingGHToken(t *testing.T) {
	// Simulate the operator's shell state.
	swapEnviron(t, []string{
		"PATH=/usr/bin",
		"GH_TOKEN=SHELL-STALE-TOKEN",
		"GITHUB_TOKEN=SHELL-STALE-TOKEN-2",
	})
	fake := &fakeGhRunner{}
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		Name:   "b",
		Token:  "RESOLVED-TOKEN",
		runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stale tokens MUST be absent; the resolved token wins.
	for _, kv := range fake.lastEnv {
		if strings.Contains(kv, "SHELL-STALE-TOKEN") {
			t.Errorf("stale shell token leaked into gh env: %s\nfull env: %v", kv, fake.lastEnv)
		}
	}
	// Resolved token IS present.
	sawResolved := false
	for _, kv := range fake.lastEnv {
		if kv == "GH_TOKEN=RESOLVED-TOKEN" {
			sawResolved = true
			break
		}
	}
	if !sawResolved {
		t.Errorf("resolved token missing from env: %v", fake.lastEnv)
	}
}

// TestCreateGitHubRepo_EmptyToken_ScrubsShellToken — when the
// resolved token is empty (operator relies on `gh auth login`),
// stale shell GH_TOKEN is still scrubbed. gh's credential store
// takes over.
func TestCreateGitHubRepo_EmptyToken_ScrubsShellToken(t *testing.T) {
	swapEnviron(t, []string{
		"PATH=/usr/bin",
		"GH_TOKEN=SHELL-STALE-TOKEN",
	})
	fake := &fakeGhRunner{}
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		Name:   "b",
		Token:  "",
		runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range fake.lastEnv {
		if strings.Contains(kv, "SHELL-STALE-TOKEN") {
			t.Errorf("stale shell token leaked: %s", kv)
		}
		if strings.HasPrefix(kv, "GH_TOKEN=") {
			t.Errorf("empty-token path should NOT set GH_TOKEN; got %s", kv)
		}
	}
}

// TestCreateGitHubRepo_MissingOwner + Name — programmer-error catch.
func TestCreateGitHubRepo_MissingOwner(t *testing.T) {
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Name:   "b",
		runner: &fakeGhRunner{},
	})
	if !errors.Is(err, ErrCreateRepoMissingDependency) {
		t.Errorf("expected ErrCreateRepoMissingDependency, got %v", err)
	}
}
func TestCreateGitHubRepo_MissingName(t *testing.T) {
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		runner: &fakeGhRunner{},
	})
	if !errors.Is(err, ErrCreateRepoMissingDependency) {
		t.Errorf("expected ErrCreateRepoMissingDependency, got %v", err)
	}
}

// TestCreateGitHubRepo_NilOut_NoCrash — nil Out falls back to
// io.Discard so library callers don't need to wire a writer just
// to exercise the engine.
func TestCreateGitHubRepo_NilOut_NoCrash(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{}
	err := CreateGitHubRepo(context.Background(), CreateRepoOptions{
		Owner:  "a",
		Name:   "b",
		Out:    nil,
		runner: fake,
	})
	if err != nil {
		t.Errorf("nil Out should not error, got %v", err)
	}
}

// --- GitLab provider ---

// TestCreateRemoteRepo_GitLab_HappyPath_CallsGlab — provider=gitlab
// routes to `glab` (not `gh`), passes the same subcommand shape,
// and propagates the token via GITLAB_TOKEN (not GH_TOKEN).
func TestCreateRemoteRepo_GitLab_HappyPath_CallsGlab(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{}
	var out bytes.Buffer
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
		Provider: ProviderGitLab,
		Owner:    "acme-group",
		Name:     "kdc-fleet",
		Token:    "glpat_fake-token",
		Out:      &out,
		runner:   fake,
	})
	if err != nil {
		t.Fatalf("gitlab happy path: unexpected error %v", err)
	}
	if fake.lastCli != "glab" {
		t.Errorf("gitlab provider should call `glab`, got %q", fake.lastCli)
	}
	wantArgs := []string{"repo", "create", "acme-group/kdc-fleet", "--private"}
	if !equalStringsCR(fake.lastArgs, wantArgs) {
		t.Errorf("wrong glab args: got %v, want %v", fake.lastArgs, wantArgs)
	}
	sawToken := false
	for _, kv := range fake.lastEnv {
		if kv == "GITLAB_TOKEN=glpat_fake-token" {
			sawToken = true
		}
		if strings.HasPrefix(kv, "GH_TOKEN=") {
			t.Errorf("GitLab provider must NOT set GH_TOKEN; env=%v", fake.lastEnv)
		}
	}
	if !sawToken {
		t.Errorf("GITLAB_TOKEN not propagated via env; env=%v", fake.lastEnv)
	}
	if !strings.Contains(out.String(), "GitLab") {
		t.Errorf("log should name the provider; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "glab") {
		t.Errorf("log should name the CLI; out=%q", out.String())
	}
}

// TestCreateRemoteRepo_GitLab_AlreadyExistsWordings — GitLab has
// multiple already-exists error phrasings depending on which
// validator fires. Sniff must swallow every one of them as
// idempotency success.
func TestCreateRemoteRepo_GitLab_AlreadyExistsWordings(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	for _, msg := range []string{
		// Rails validator, common on POST /projects when name collides.
		"name has already been taken",
		// Slug validator when path differs from name but still collides.
		"path is already taken",
		// Bare glab surface of the raw API error.
		"HTTP 422: project already exists",
		// Case-insensitive.
		"HAS ALREADY BEEN TAKEN",
	} {
		t.Run(msg, func(t *testing.T) {
			fake := &fakeGhRunner{
				stderrOut: []byte(msg),
				err:       errors.New("exit status 1"),
			}
			err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
				Provider: ProviderGitLab,
				Owner:    "g",
				Name:     "p",
				runner:   fake,
			})
			if err != nil {
				t.Errorf("expected gitlab already-exists sniff to match %q, got %v", msg, err)
			}
		})
	}
}

// TestCreateRemoteRepo_GitLab_EnvScrubsShellTokens — the operator's
// shell may carry a stale GITLAB_TOKEN AND a stale GL_TOKEN.
// Engine scrubs both prefixes; the resolved --github-token (spelled
// generically at the flag layer; carried in Token) becomes the
// canonical GITLAB_TOKEN.
func TestCreateRemoteRepo_GitLab_EnvScrubsShellTokens(t *testing.T) {
	swapEnviron(t, []string{
		"PATH=/usr/bin",
		"GITLAB_TOKEN=SHELL-STALE-GLTOK",
		"GL_TOKEN=SHELL-STALE-GL",
		// GH_TOKEN must NOT be scrubbed by the gitlab provider — it
		// belongs to another CLI. Verifies the scrub is provider-scoped.
		"GH_TOKEN=UNRELATED-GH-TOKEN",
	})
	fake := &fakeGhRunner{}
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
		Provider: ProviderGitLab,
		Owner:    "g", Name: "p",
		Token:  "RESOLVED-GLPAT",
		runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stale GitLab tokens gone.
	for _, kv := range fake.lastEnv {
		if strings.Contains(kv, "SHELL-STALE-GLTOK") ||
			strings.Contains(kv, "SHELL-STALE-GL") {
			t.Errorf("stale GitLab token leaked into env: %s\nfull env: %v", kv, fake.lastEnv)
		}
	}
	// Resolved token IS present.
	sawResolved := false
	for _, kv := range fake.lastEnv {
		if kv == "GITLAB_TOKEN=RESOLVED-GLPAT" {
			sawResolved = true
		}
	}
	if !sawResolved {
		t.Errorf("resolved GitLab token missing from env: %v", fake.lastEnv)
	}
	// GH_TOKEN preserved (belongs to a different provider — not
	// this provider's concern to strip).
	sawGH := false
	for _, kv := range fake.lastEnv {
		if kv == "GH_TOKEN=UNRELATED-GH-TOKEN" {
			sawGH = true
		}
	}
	if !sawGH {
		t.Errorf("gitlab scrub over-reached and stripped GH_TOKEN: env=%v", fake.lastEnv)
	}
}

// TestCreateRemoteRepo_GitHub_ScrubDoesNotStripGitLab — symmetric
// invariant: the github provider's scrub MUST NOT strip GITLAB_TOKEN
// or GL_TOKEN. Operators may legitimately have both configured for
// different fleets.
func TestCreateRemoteRepo_GitHub_ScrubDoesNotStripGitLab(t *testing.T) {
	swapEnviron(t, []string{
		"PATH=/usr/bin",
		"GITLAB_TOKEN=OTHER-PROVIDER-TOK",
		"GL_TOKEN=OTHER-PROVIDER-TOK-2",
	})
	fake := &fakeGhRunner{}
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
		Provider: ProviderGitHub,
		Owner:    "a", Name: "b",
		Token:  "RESOLVED-GHTOK",
		runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	sawGitLab := 0
	for _, kv := range fake.lastEnv {
		if strings.Contains(kv, "OTHER-PROVIDER-TOK") {
			sawGitLab++
		}
	}
	if sawGitLab != 2 {
		t.Errorf("github scrub over-reached and stripped %d/2 GitLab tokens: env=%v",
			2-sawGitLab, fake.lastEnv)
	}
}

// TestCreateRemoteRepo_UnknownProvider_Refuses — typo catch. A
// silent default to GitHub would create repos on the wrong service.
func TestCreateRemoteRepo_UnknownProvider_Refuses(t *testing.T) {
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
		Provider: Provider("gitub"), // typo
		Owner:    "a", Name: "b",
		runner: &fakeGhRunner{},
	})
	if err == nil {
		t.Fatal("unknown provider must error")
	}
	if !errors.Is(err, ErrCreateRepoUnknownProvider) {
		t.Errorf("expected ErrCreateRepoUnknownProvider, got %v", err)
	}
	// Error message mentions the valid choices so operator can fix.
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("error should list valid providers, got %v", err)
	}
	if !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("error should list valid providers, got %v", err)
	}
}

// TestNormalizeProviderForHash — the P3 regression guard for the
// apply-plan input-hash. Empty and explicit `github` MUST collapse
// to the same normalized value so their hashes match; `gitlab`
// (and any future non-default provider) surfaces verbatim so
// gitlab-plan / github-apply drift IS caught.
func TestNormalizeProviderForHash(t *testing.T) {
	cases := []struct {
		in, want Provider
	}{
		{"", ""},                            // default → default
		{ProviderGitHub, ""},                // explicit default → default
		{ProviderGitLab, ProviderGitLab},    // non-default → verbatim
		{Provider("bitbucket"), Provider("bitbucket")}, // future / typo → verbatim (Validate() catches upstream)
	}
	for _, c := range cases {
		got := normalizeProviderForHash(c.in)
		if got != c.want {
			t.Errorf("normalizeProviderForHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildPlan_ProviderHashStability — end-to-end: an operator
// who omits --provider and one who passes --provider=github MUST
// end up with identical InputHash. Otherwise the dry-run → apply
// bridge trips ErrPlanInputDrift on a semantically-empty change.
func TestBuildPlan_ProviderHashStability(t *testing.T) {
	baselineOpts := func() InitOptions {
		return InitOptions{
			Preset:         PresetInternalOnly,
			Mode:           ModeInstall,
			Name:           "t1",
			Domain:         "t1.example.com",
			NodeExternalIP: "1.2.3.4",
			Email:          "ops@t1",
			FleetMode:      FleetNewRepo,
			GitHubOwner:    "acme",
			GitHubRepo:     "fleet",
			RookMode:       RookCephMultiNode,
			Sets: map[string]string{
				"EXT_NET_VLAN_ID":  "1",
				"EXT_NET_INTERFACE": "e",
			},
		}
	}

	optEmpty := baselineOpts()   // Provider unset
	optGitHub := baselineOpts()
	optGitHub.Provider = ProviderGitHub // explicit default

	planEmpty, err := BuildPlan(&optEmpty, FleetState{})
	if err != nil {
		t.Fatalf("empty plan: %v", err)
	}
	planGH, err := BuildPlan(&optGitHub, FleetState{})
	if err != nil {
		t.Fatalf("github plan: %v", err)
	}
	if planEmpty.InputHash != planGH.InputHash {
		t.Errorf("empty Provider vs explicit `github` produced different hashes — apply-plan drift regression:\n  empty  = %s\n  github = %s",
			planEmpty.InputHash, planGH.InputHash)
	}

	optGitLab := baselineOpts()
	optGitLab.Provider = ProviderGitLab
	planGL, err := BuildPlan(&optGitLab, FleetState{})
	if err != nil {
		t.Fatalf("gitlab plan: %v", err)
	}
	// Non-default provider MUST hash differently — otherwise a
	// gitlab-plan → github-apply mismatch would slip through.
	if planEmpty.InputHash == planGL.InputHash {
		t.Errorf("gitlab hash MUST differ from default; both = %s (drift-detection regression)",
			planGL.InputHash)
	}
}

// TestCreateRemoteRepo_EmptyProvider_DefaultsToGitHub — backward
// compat with pre-multi-provider callers who didn't set Provider.
func TestCreateRemoteRepo_EmptyProvider_DefaultsToGitHub(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin"})
	fake := &fakeGhRunner{}
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{
		// Provider unset
		Owner:  "a", Name: "b",
		runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastCli != "gh" {
		t.Errorf("empty provider should default to gh, got %q", fake.lastCli)
	}
}

// TestIsAlreadyExists_TruthTable — direct unit coverage of the
// sniff so a future stderr-format change surfaces here, not as a
// dropped idempotency contract.
func TestIsAlreadyExists_TruthTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty", "", false},
		{"unrelated", "network unreachable", false},
		{"graphql-shape", "GraphQL: Name already exists on this account", true},
		{"http-422-shape", "HTTP 422: Name already exists", true},
		{"uppercase", "NAME ALREADY EXISTS", true},
		// Sniff requires the exact space-separated substring
		// "already exists"; underscore-joined variants (e.g.
		// hypothetical machine-readable error codes) do NOT match.
		// The false-positive risk documented in the engine godoc
		// is specifically about GitHub's human-readable error text
		// changing its wording in a way that keeps the phrase but
		// changes context — a low-probability regression.
		{"underscore-noise-does-not-match", "not_already_exists_here", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAlreadyExists([]byte(c.body)); got != c.want {
				t.Errorf("isAlreadyExists(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

// equalStringsCR — suffixed to avoid a package-level collision
// with equalStrings in a sibling test file.
func equalStringsCR(a, b []string) bool {
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
