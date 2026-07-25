package keycloak

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeRunner struct {
	lines []ports.Line
	err   error
}

func (r *fakeRunner) Run(_ context.Context, _ ports.ScriptKind, _ map[string]string, _ ...string) (<-chan ports.Line, error) {
	if r.err != nil {
		return nil, r.err
	}
	ch := make(chan ports.Line, len(r.lines))
	for _, line := range r.lines {
		ch <- line
	}
	close(ch)
	return ch, nil
}

func (r *fakeRunner) WithSentinelCallback(ports.SentinelCallback) ports.ScriptRunner { return r }

type recordingGit struct {
	ports.GitClient
	pushes    int
	dir       string
	token     string
	err       error
	headCalls int
	heads     []string
}

func (g *recordingGit) Push(_ context.Context, dir, token string) error {
	g.pushes++
	g.dir = dir
	g.token = token
	return g.err
}

func (g *recordingGit) Head(_ context.Context, _ string) (string, error) {
	g.headCalls++
	if g.headCalls <= len(g.heads) {
		return g.heads[g.headCalls-1], nil
	}
	if g.headCalls == 1 {
		return "before", nil
	}
	return "after", nil
}

func successfulRunner() *fakeRunner {
	return &fakeRunner{lines: []ports.Line{
		{Stream: ports.StreamStdout, Text: "configured"},
		{Stream: ports.StreamExit, Text: "0"},
	}}
}

func TestInitPushesScriptCommit(t *testing.T) {
	git := &recordingGit{}
	var out bytes.Buffer
	err := Init(context.Background(), InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      successfulRunner(),
		Git:         git,
		GitHubToken: "sensitive-token",
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if git.pushes != 1 || git.dir != "/fleet" || git.token != "sensitive-token" {
		t.Fatalf("Push calls = %d, dir=%q token=%q", git.pushes, git.dir, git.token)
	}
	if strings.Contains(out.String(), "sensitive-token") {
		t.Fatal("GitHub token leaked to output")
	}
	if !strings.Contains(out.String(), "committed and pushed") {
		t.Fatalf("completion did not confirm durable push:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "flux-system --with-source") {
		t.Fatalf("next steps do not reconcile root cluster-config first:\n%s", out.String())
	}
}

func TestInitAlreadyConvergedDoesNotPush(t *testing.T) {
	git := &recordingGit{heads: []string{"same", "same"}}
	var out bytes.Buffer
	err := Init(context.Background(), InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      successfulRunner(),
		Git:         git,
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if git.pushes != 0 {
		t.Fatalf("Push called %d times without a new script commit", git.pushes)
	}
	if !strings.Contains(out.String(), "no new commit to push") {
		t.Fatalf("no-op completion missing:\n%s", out.String())
	}
}

func TestInitNoPushLeavesCommitLocal(t *testing.T) {
	git := &recordingGit{}
	var out bytes.Buffer
	err := Init(context.Background(), InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      successfulRunner(),
		Git:         git,
		NoPush:      true,
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if git.pushes != 0 {
		t.Fatalf("Push called %d times with --no-push", git.pushes)
	}
	if !strings.Contains(out.String(), "committed locally (--no-push)") {
		t.Fatalf("local-only completion missing:\n%s", out.String())
	}
}

func TestInitPushFailurePreservesActionableError(t *testing.T) {
	git := &recordingGit{err: errors.New("remote rejected")}
	err := Init(context.Background(), InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      successfulRunner(),
		Git:         git,
	})
	if err == nil || !strings.Contains(err.Error(), "local commit preserved") || !strings.Contains(err.Error(), "remote rejected") {
		t.Fatalf("Init() error = %v, want preserved local-commit push error", err)
	}
}

func TestInitScriptFailureNeverPushes(t *testing.T) {
	git := &recordingGit{}
	runner := &fakeRunner{lines: []ports.Line{{Stream: ports.StreamExit, Text: "1"}}}
	err := Init(context.Background(), InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      runner,
		Git:         git,
	})
	if err == nil || !strings.Contains(err.Error(), "exit=1") {
		t.Fatalf("Init() error = %v, want script exit", err)
	}
	if git.pushes != 0 {
		t.Fatalf("Push called %d times after script failure", git.pushes)
	}
}

func TestValidateRequiresGit(t *testing.T) {
	err := validate(InitOptions{ClusterName: "dc1", FleetRepo: "/fleet", Runner: successfulRunner()})
	if err == nil || !strings.Contains(err.Error(), "Git") {
		t.Fatalf("validate() error = %v, want missing Git", err)
	}
}

func TestValidateNoPushDoesNotRequireGit(t *testing.T) {
	err := validate(InitOptions{
		ClusterName: "dc1",
		FleetRepo:   "/fleet",
		Runner:      successfulRunner(),
		NoPush:      true,
	})
	if err != nil {
		t.Fatalf("validate() error = %v with --no-push", err)
	}
}
