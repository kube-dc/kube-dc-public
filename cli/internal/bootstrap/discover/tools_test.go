package discover

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeExec builds a fixed-output execHook. The map key is the
// argv-as-string (e.g. "kubectl version --client --output=json"); the
// value is the (stdout, stderr, err) triple to return. Any unmatched
// command returns exec.ErrNotFound to mimic a missing binary.
func fakeExec(responses map[string]execResp) execHook {
	return func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		key := name
		if len(args) > 0 {
			key = name + " " + strings.Join(args, " ")
		}
		r, ok := responses[key]
		if !ok {
			return nil, nil, exec.ErrNotFound
		}
		return []byte(r.stdout), []byte(r.stderr), r.err
	}
}

type execResp struct {
	stdout string
	stderr string
	err    error
}

// happyPath table — every probe's canonical successful run.
func TestToolProbes_HappyPath(t *testing.T) {
	cases := []struct {
		name    string
		factory func(execHook) *ToolProbe
		resp    map[string]execResp
		wantVer string
	}{
		{
			name:    "kubectl",
			factory: newKubectlProbe,
			resp: map[string]execResp{
				"kubectl version --client --output=json": {stdout: `{"clientVersion":{"gitVersion":"v1.32.1+rke2r1"}}`},
			},
			wantVer: "v1.32.1+rke2r1",
		},
		{
			name:    "flux",
			factory: newFluxProbe,
			resp: map[string]execResp{
				"flux --version": {stdout: "flux version v2.4.0\n"},
			},
			wantVer: "v2.4.0",
		},
		{
			name:    "sops",
			factory: newSOPSProbe,
			resp: map[string]execResp{
				"sops --version": {stdout: "sops 3.10.2 (latest)\n"},
			},
			wantVer: "3.10.2",
		},
		{
			name:    "age",
			factory: newAgeProbe,
			resp: map[string]execResp{
				"age --version": {stdout: "v1.2.1\n"},
			},
			wantVer: "v1.2.1",
		},
		{
			name:    "git",
			factory: newGitProbe,
			resp: map[string]execResp{
				"git --version": {stdout: "git version 2.43.0\n"},
			},
			wantVer: "2.43.0",
		},
		{
			name:    "ssh",
			factory: newSSHProbe,
			resp: map[string]execResp{
				// ssh writes version to STDERR, not stdout.
				"ssh -V": {stderr: "OpenSSH_9.6p1 Debian-1+b1, OpenSSL 3.0.13"},
			},
			wantVer: "OpenSSH_9.6p1",
		},
		{
			name:    "bao",
			factory: newBaoProbe,
			resp: map[string]execResp{
				"bao --version": {stdout: "OpenBao v2.5.3 ('abc123')\n"},
			},
			wantVer: "v2.5.3",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.factory(fakeExec(c.resp))
			r := p.Run(context.Background())
			if r.Status != ports.StatusInstalled {
				t.Errorf("%s status=%v want installed", c.name, r.Status)
			}
			if !strings.Contains(r.Version, c.wantVer) && r.Version != c.wantVer {
				t.Errorf("%s version=%q want substr %q", c.name, r.Version, c.wantVer)
			}
			if r.Severity != ports.SeverityInfo {
				t.Errorf("%s severity=%v want Info", c.name, r.Severity)
			}
		})
	}
}

func TestToolProbes_MissingBinary(t *testing.T) {
	// fakeExec with no responses returns exec.ErrNotFound for every
	// command. Every probe should surface StatusMissing + Blocker.
	probes := []ports.Probe{
		newKubectlProbe(fakeExec(nil)),
		newFluxProbe(fakeExec(nil)),
		newSOPSProbe(fakeExec(nil)),
		newAgeProbe(fakeExec(nil)),
		newGitProbe(fakeExec(nil)),
		newGHProbe(fakeExec(nil)),
		newSSHProbe(fakeExec(nil)),
		newBaoProbe(fakeExec(nil)),
	}
	for _, p := range probes {
		r := p.Run(context.Background())
		if r.Status != ports.StatusMissing {
			t.Errorf("%s missing-binary status=%v want missing", p.Name(), r.Status)
		}
		if r.Severity != ports.SeverityBlocker {
			t.Errorf("%s missing-binary severity=%v want Blocker", p.Name(), r.Severity)
		}
		if !strings.Contains(r.FixHint.Text, "install-prerequisites") {
			t.Errorf("%s FixHint missing prereq pointer: %q", p.Name(), r.FixHint.Text)
		}
	}
}

func TestToolProbes_BelowMinVersion(t *testing.T) {
	// kubectl v1.27 < required 1.28 → Partial + Warn
	p := newKubectlProbe(fakeExec(map[string]execResp{
		"kubectl version --client --output=json": {stdout: `{"clientVersion":{"gitVersion":"v1.27.5"}}`},
	}))
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial {
		t.Errorf("status=%v want partial", r.Status)
	}
	if r.Severity != ports.SeverityWarn {
		t.Errorf("severity=%v want Warn", r.Severity)
	}
	if !strings.Contains(r.Detail, "< required") {
		t.Errorf("detail should call out version floor: %q", r.Detail)
	}
}

func TestToolProbes_VersionEqualToMin_OK(t *testing.T) {
	// Exact floor (kubectl 1.28.0) passes — version comparator uses
	// <, not <=.
	p := newKubectlProbe(fakeExec(map[string]execResp{
		"kubectl version --client --output=json": {stdout: `{"clientVersion":{"gitVersion":"v1.28.0"}}`},
	}))
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed", r.Status)
	}
}

func TestGHProbe_AuthStatus_NoScopes_Partial(t *testing.T) {
	p := newGHProbe(fakeExec(map[string]execResp{
		"gh --version": {stdout: "gh version 2.51.0 (2024-04-15)\n"},
		// `gh auth status` output, gist-only scope (missing repo + workflow).
		"gh auth status": {stderr: `github.com
  ✓ Logged in to github.com as voa
  ✓ Token scopes: 'gist'
`},
	}))
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial {
		t.Errorf("status=%v want partial (missing scopes)", r.Status)
	}
	if !strings.Contains(r.Detail, "repo") || !strings.Contains(r.Detail, "workflow") {
		t.Errorf("Detail should call out missing scopes: %q", r.Detail)
	}
	if !strings.Contains(r.FixHint.Text, "gh auth refresh") {
		t.Errorf("FixHint should point at gh auth refresh: %q", r.FixHint.Text)
	}
}

func TestGHProbe_AuthStatus_AllScopes_OK(t *testing.T) {
	p := newGHProbe(fakeExec(map[string]execResp{
		"gh --version": {stdout: "gh version 2.51.0 (2024-04-15)\n"},
		"gh auth status": {stderr: `github.com
  ✓ Logged in to github.com as voa
  ✓ Token scopes: 'gist', 'repo', 'workflow'
`},
	}))
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("status=%v want installed (all scopes present)", r.Status)
	}
}

func TestGHProbe_NotAuthenticated_Partial(t *testing.T) {
	p := newGHProbe(fakeExec(map[string]execResp{
		"gh --version":   {stdout: "gh version 2.51.0 (2024-04-15)\n"},
		"gh auth status": {stderr: "You are not logged into any GitHub hosts.", err: errors.New("exit status 1")},
	}))
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial {
		t.Errorf("status=%v want partial", r.Status)
	}
	if !strings.Contains(r.FixHint.Text, "gh auth login") {
		t.Errorf("FixHint should point at gh auth login: %q", r.FixHint.Text)
	}
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in   string
		want Semver
		ok   bool
	}{
		{"v1.32.1+rke2r1", Semver{1, 32, 1}, true},
		{"v2.4.0", Semver{2, 4, 0}, true},
		{"1.0.0", Semver{1, 0, 0}, true},
		{"v0.36.1", Semver{0, 36, 1}, true},
		{"sops 3.10.2 (latest)", Semver{3, 10, 2}, true},
		{"no version here", Semver{}, false},
	}
	for _, c := range cases {
		got, err := ParseSemver(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseSemver(%q): unexpected err %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ParseSemver(%q): expected error", c.in)
		}
		if c.ok && got != c.want {
			t.Errorf("ParseSemver(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b Semver
		want bool
	}{
		{Semver{1, 0, 0}, Semver{2, 0, 0}, true},
		{Semver{1, 2, 3}, Semver{1, 2, 4}, true},
		{Semver{1, 2, 3}, Semver{1, 2, 3}, false},
		{Semver{2, 0, 0}, Semver{1, 99, 99}, false},
	}
	for _, c := range cases {
		if got := c.a.Less(c.b); got != c.want {
			t.Errorf("less(%v, %v)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestParseOpenSSHVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Semver
	}{
		{"OpenSSH_9.6p1", Semver{9, 6, 1}},
		{"OpenSSH_8.9", Semver{8, 9, 0}},
		{"OpenSSH_9.6p1 Debian-1+b1, OpenSSL 3.0.13", Semver{9, 6, 1}},
	}
	for _, c := range cases {
		got, err := parseOpenSSHVersion(c.in)
		if err != nil {
			t.Errorf("err on %q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseOpenSSHVersion(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestParseGHScopes(t *testing.T) {
	body := `github.com
  ✓ Logged in to github.com as voa
  ✓ Token scopes: 'gist', 'repo', 'workflow'
`
	got := parseGHScopes(body)
	want := []string{"gist", "repo", "workflow"}
	if !equalStrings(got, want) {
		t.Errorf("parseGHScopes=%v want %v", got, want)
	}
}

func TestRequiredGHScopes(t *testing.T) {
	missing := RequiredGHScopes([]string{"gist"})
	if !equalStrings(missing, []string{"repo", "workflow"}) {
		t.Errorf("missing=%v want [repo workflow]", missing)
	}
	if got := RequiredGHScopes([]string{"repo", "workflow", "gist"}); len(got) != 0 {
		t.Errorf("all-present missing=%v want []", got)
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(exec.ErrNotFound) {
		t.Error("exec.ErrNotFound should match")
	}
	if !isNotFound(errors.New(`exec: "foo": executable file not found in $PATH`)) {
		t.Error("text match should hit")
	}
	if isNotFound(errors.New("exit status 1")) {
		t.Error("false positive on exit status")
	}
	if isNotFound(nil) {
		t.Error("nil should be false")
	}
}

func equalStrings(a, b []string) bool {
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

func TestAllToolProbes_IncludesEightTools(t *testing.T) {
	got := AllToolProbes()
	if len(got) != 8 {
		t.Errorf("AllToolProbes returned %d, want 8", len(got))
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name()] = true
	}
	for _, want := range []string{"kubectl", "flux", "sops", "age", "git", "gh", "ssh", "bao"} {
		if !names[want] {
			t.Errorf("missing probe %q", want)
		}
	}
}

// ports.Probe contract: probes MUST return immediately on ctx.Done().
// A cancelled ctx at Run entry MUST NOT fire any exec call.
func TestToolProbe_CancelledContext_NoExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	p := newKubectlProbe(func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		calls++
		return nil, nil, nil
	})
	r := p.Run(ctx)
	if calls != 0 {
		t.Errorf("exec fired %d times despite cancelled ctx", calls)
	}
	if !strings.Contains(r.Detail, "cancelled") {
		t.Errorf("Detail should surface cancellation: %q", r.Detail)
	}
}

// gh's two-stage flow (version + auth status) must also re-check
// ctx between exec calls so a cancellation between them doesn't
// fire the second exec.
func TestGHProbe_CancelBetweenExecs_NoSecondExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p := newGHProbe(func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			cancel() // fire ctx cancel after the version exec
			return []byte("gh version 2.51.0 (2024-04-15)\n"), nil, nil
		}
		// Second exec (auth status) should NEVER run.
		t.Errorf("second exec ran despite cancelled ctx: %v", args)
		return nil, nil, nil
	})
	r := p.Run(ctx)
	if calls != 1 {
		t.Errorf("expected 1 exec, got %d", calls)
	}
	if !strings.Contains(r.Detail, "cancelled") {
		t.Errorf("Detail should surface cancellation: %q", r.Detail)
	}
}
