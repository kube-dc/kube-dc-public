package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// Reviewer P2: resolveGitHubToken had zero tests despite the
// provider-aware dispatch justifying the (now-restored) GitLab
// retire attempt. Table tests here lock the branch behaviour so
// the provider surface can be trusted going forward.

// TestResolveGitHubToken_FlagWins — explicit `--github-token`
// overrides everything; CLI subshell is never invoked.
func TestResolveGitHubToken_FlagWins(t *testing.T) {
	var out bytes.Buffer
	execFired := false
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{GitHubToken: "explicit-flag-token"},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			execFired = true
			return nil, nil
		},
	)
	if got != "explicit-flag-token" {
		t.Errorf("flag wins → got %q", got)
	}
	if execFired {
		t.Errorf("CLI subshell must NOT run when --github-token is supplied")
	}
	if !strings.Contains(out.String(), "using --github-token flag value") {
		t.Errorf("expected 'using --github-token flag value' log; got:\n%s", out.String())
	}
}

// TestResolveGitHubToken_GitHubDefault — empty flag + default
// (github) provider → invokes `gh auth token`; returns the
// trimmed stdout.
func TestResolveGitHubToken_GitHubDefault(t *testing.T) {
	var out bytes.Buffer
	var gotBin string
	var gotArgs []string
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			gotBin = bin
			gotArgs = append([]string(nil), args...)
			return []byte("ghp_ABCDEFGH\n"), nil // note trailing \n → trimmed
		},
	)
	if got != "ghp_ABCDEFGH" {
		t.Errorf("github token = %q, want ghp_ABCDEFGH (trimmed)", got)
	}
	if gotBin != "gh" {
		t.Errorf("default provider should invoke `gh`, got %q", gotBin)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "auth" || gotArgs[1] != "token" {
		t.Errorf("gh args = %v, want [auth token]", gotArgs)
	}
	if !strings.Contains(out.String(), "gh auth token") {
		t.Errorf("expected 'gh auth token' in log")
	}
	// Token MUST NOT appear in the log.
	if strings.Contains(out.String(), "ghp_ABCDEFGH") {
		t.Errorf("token leaked into log; out=%q", out.String())
	}
}

// TestResolveGitHubToken_GitHubExplicit — explicit --provider=github
// takes the same path as empty (both default to github).
func TestResolveGitHubToken_GitHubExplicit(t *testing.T) {
	var out bytes.Buffer
	var gotBin string
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{Provider: clusterinit.ProviderGitHub},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			gotBin = bin
			return []byte("gh_token_value"), nil
		},
	)
	if got != "gh_token_value" {
		t.Errorf("got %q", got)
	}
	if gotBin != "gh" {
		t.Errorf("explicit github should invoke `gh`, got %q", gotBin)
	}
}

// TestResolveGitHubToken_GitLab_MultilineTokenLine — glab's
// `auth status --show-token` emits a multi-line status block with
// `Token: <value>` on one line. Resolver must extract just the
// value.
func TestResolveGitHubToken_GitLab_MultilineTokenLine(t *testing.T) {
	glabStatus := `gitlab.com
  ✓ Logged in to gitlab.com as opsuser (from ~/.config/glab-cli/config.yml)
  ✓ Git operations for gitlab.com configured to use https protocol.
  ✓ REST API Endpoint: https://gitlab.com/api/v4/
  ✓ GraphQL Endpoint: https://gitlab.com/api/graphql
  Token: glpat_ABCXYZ123
`
	var out bytes.Buffer
	var gotBin string
	var gotArgs []string
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{Provider: clusterinit.ProviderGitLab},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			gotBin = bin
			gotArgs = append([]string(nil), args...)
			return []byte(glabStatus), nil
		},
	)
	if got != "glpat_ABCXYZ123" {
		t.Errorf("gitlab token = %q, want glpat_ABCXYZ123", got)
	}
	if gotBin != "glab" {
		t.Errorf("gitlab provider should invoke `glab`, got %q", gotBin)
	}
	wantArgs := []string{"auth", "status", "--show-token"}
	for i, want := range wantArgs {
		if i >= len(gotArgs) || gotArgs[i] != want {
			t.Errorf("glab args = %v, want %v", gotArgs, wantArgs)
			break
		}
	}
	// No token leaks into the log.
	if strings.Contains(out.String(), "glpat_ABCXYZ123") {
		t.Errorf("token leaked into log; out=%q", out.String())
	}
}

// TestResolveGitHubToken_GitLab_BareTokenFallback — some glab
// versions or piped output might emit a bare token. The fallback
// path returns the trimmed body when no "Token:" line is found
// AND the body is single-line.
func TestResolveGitHubToken_GitLab_BareTokenFallback(t *testing.T) {
	var out bytes.Buffer
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{Provider: clusterinit.ProviderGitLab},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			return []byte("glpat_BARETOKEN\n"), nil
		},
	)
	if got != "glpat_BARETOKEN" {
		t.Errorf("bare-token fallback = %q, want glpat_BARETOKEN", got)
	}
}

// TestResolveGitHubToken_ExecError_Empty — subshell errors return
// empty string; caller (Apply) surfaces the push-auth error later
// if needed. Log line MUST NOT include the token (it's empty
// anyway, but we assert the "failed" wording).
func TestResolveGitHubToken_ExecError_Empty(t *testing.T) {
	var out bytes.Buffer
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			return nil, errors.New("exit status 1")
		},
	)
	if got != "" {
		t.Errorf("exec error should return empty; got %q", got)
	}
	if !strings.Contains(out.String(), "no token resolved") {
		t.Errorf("expected 'no token resolved' in log; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "gh auth token failed") {
		t.Errorf("expected 'gh auth token failed' in log; got:\n%s", out.String())
	}
}

// TestResolveGitHubToken_EmptyOutput_Empty — subshell exit 0 but
// no stdout (unusual `gh` state; treat as no-token).
func TestResolveGitHubToken_EmptyOutput_Empty(t *testing.T) {
	var out bytes.Buffer
	got := resolveGitHubTokenWithExec(
		&clusterinit.InitOptions{},
		&out,
		func(bin string, args ...string) ([]byte, error) {
			return []byte(""), nil
		},
	)
	if got != "" {
		t.Errorf("empty output should return empty; got %q", got)
	}
	if !strings.Contains(out.String(), "returned empty") {
		t.Errorf("expected 'returned empty' in log; got:\n%s", out.String())
	}
}

// TestResolveGitHubToken_TokenNeverLogged_TruthTable — the
// security-critical invariant: across every success + failure
// branch, the resolved token MUST NOT appear in `out`. Iterate a
// small set of canned tokens through the different paths and
// assert.
func TestResolveGitHubToken_TokenNeverLogged_TruthTable(t *testing.T) {
	cases := []struct {
		name     string
		opts     *clusterinit.InitOptions
		execOut  string
		execErr  error
	}{
		{"flag-token", &clusterinit.InitOptions{GitHubToken: "SENSITIVE-FLAG-TOK"}, "", nil},
		{"github-stdout", &clusterinit.InitOptions{}, "SENSITIVE-GH-TOK\n", nil},
		{"gitlab-status", &clusterinit.InitOptions{Provider: clusterinit.ProviderGitLab}, "Token: SENSITIVE-GL-TOK\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			resolveGitHubTokenWithExec(c.opts, &out, func(bin string, args ...string) ([]byte, error) {
				return []byte(c.execOut), c.execErr
			})
			// Every SENSITIVE-* value MUST NOT appear in the log.
			for _, canary := range []string{"SENSITIVE-FLAG-TOK", "SENSITIVE-GH-TOK", "SENSITIVE-GL-TOK"} {
				if strings.Contains(out.String(), canary) {
					t.Errorf("token %q leaked into log; out=%q", canary, out.String())
				}
			}
		})
	}
}

// TestExtractGlabToken_TruthTable — the parser tolerates the two
// wordings glab has been observed to emit (multi-line status +
// bare token) and refuses on ambiguous multi-line non-status
// output.
func TestExtractGlabToken_TruthTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"bare-single-line", "glpat_ABC", "glpat_ABC"},
		{"canonical-status", "gitlab.com\n  Token: glpat_XYZ\n", "glpat_XYZ"},
		{"status-indented", "  Token: glpat_INDENT", "glpat_INDENT"},
		{"multiline-no-token-line", "line1\nline2\nno token here", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractGlabToken(c.in)
			if got != c.want {
				t.Errorf("extractGlabToken(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
