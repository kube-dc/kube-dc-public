package keycloak

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// envRecordingRunner captures the env passed to Run so the tests can prove
// secrets travel via env (and only env) into the script invocation.
type envRecordingRunner struct {
	fakeRunner
	gotEnv  map[string]string
	gotKind ports.ScriptKind
}

func (r *envRecordingRunner) Run(ctx context.Context, kind ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	r.gotEnv, r.gotKind = env, kind
	return r.fakeRunner.Run(ctx, kind, env, args...)
}

func TestSSORealm_PassesEnvAndPushesOnNewCommit(t *testing.T) {
	runner := &envRecordingRunner{fakeRunner: *successfulRunner()}
	git := &recordingGit{heads: []string{"before", "after"}}
	var out bytes.Buffer
	err := SSORealm(context.Background(), SSOOptions{
		ClusterName: "atlantis",
		FleetRepo:   "/fleet",
		Runner:      runner,
		Git:         git,
		Out:         &out,
		Env: map[string]string{
			"SMTP_USER":     "u",
			"SMTP_PASSWORD": "p",
			"SMTP_HOST":     "smtp.example.com",
		},
	})
	if err != nil {
		t.Fatalf("SSORealm: %v", err)
	}
	if runner.gotKind != ports.ScriptSetupSSORealm {
		t.Errorf("ran %q, want %q", runner.gotKind, ports.ScriptSetupSSORealm)
	}
	if runner.gotEnv["SMTP_PASSWORD"] != "p" || runner.gotEnv["SMTP_HOST"] != "smtp.example.com" {
		t.Errorf("env not passed through: %v", runner.gotEnv)
	}
	if git.pushes != 1 {
		t.Errorf("pushes = %d, want 1 (HEAD moved)", git.pushes)
	}
	if !strings.Contains(out.String(), "sso realm complete") {
		t.Errorf("missing completion line in output: %s", out.String())
	}
}

func TestSSORealm_ConvergedSkipsPush(t *testing.T) {
	runner := &envRecordingRunner{fakeRunner: *successfulRunner()}
	git := &recordingGit{heads: []string{"same", "same"}}
	err := SSORealm(context.Background(), SSOOptions{
		ClusterName: "atlantis", FleetRepo: "/fleet",
		Runner: runner, Git: git,
	})
	if err != nil {
		t.Fatalf("SSORealm: %v", err)
	}
	if git.pushes != 0 {
		t.Errorf("pushes = %d, want 0 (no new commit)", git.pushes)
	}
}

func TestSSORealm_NonZeroExitFails(t *testing.T) {
	runner := &envRecordingRunner{fakeRunner: fakeRunner{lines: []ports.Line{
		{Stream: ports.StreamStdout, Text: "boom"},
		{Stream: ports.StreamExit, Text: "1"},
	}}}
	git := &recordingGit{}
	err := SSORealm(context.Background(), SSOOptions{
		ClusterName: "atlantis", FleetRepo: "/fleet",
		Runner: runner, Git: git, NoPush: true,
	})
	if err == nil || !strings.Contains(err.Error(), "exit=1") {
		t.Fatalf("want exit=1 error, got %v", err)
	}
	if git.pushes != 0 {
		t.Errorf("must not push on failure")
	}
}

func TestSSORealm_ValidatesDependencies(t *testing.T) {
	err := SSORealm(context.Background(), SSOOptions{FleetRepo: "/fleet"})
	if err == nil {
		t.Fatal("want missing-dependency error")
	}
}
