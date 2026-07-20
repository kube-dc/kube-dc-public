package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeGit is a lightweight mock-stand-in for ports.GitClient — only
// the methods Apply consumes. Records call order so tests can
// assert the canonical sequence (Head → Diff → CommitAndPush →
// ResetHard on failure). The mock package's GitClient is richer but
// it's scoped to scenario fixtures; for Apply's unit tests we want
// per-test control over each method's return value.
type fakeGit struct {
	mu sync.Mutex

	preSHA string
	diff   ports.Diff

	commitSHA  string
	commitErr  error
	commitCall int

	pushed       bool
	pushErr      error
	pushAtCommit bool // commit succeeded but push failed

	resetCalls []string // SHAs passed to ResetHard
	resetErr   error
}

func (g *fakeGit) Clone(_ context.Context, _, _, _ string) error { return nil }
func (g *fakeGit) Init(_ context.Context, _, _ string) error     { return nil }
func (g *fakeGit) Pull(_ context.Context, _, _ string) error     { return nil }
func (g *fakeGit) CreateRepo(_ context.Context, _, _ string, _ bool, _ string) error {
	return nil
}

func (g *fakeGit) Head(_ context.Context, _ string) (string, error) {
	return g.preSHA, nil
}

func (g *fakeGit) Diff(_ context.Context, _ string) (ports.Diff, error) {
	return g.diff, nil
}

func (g *fakeGit) CommitAndPush(_ context.Context, _, _, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commitCall++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	if g.pushErr != nil {
		// The real adapter returns (commitSHA, pushErr) when the
		// local commit succeeded but the push failed — exercises
		// the rollback-via-pre-commit-SHA path.
		g.pushAtCommit = true
		return g.commitSHA, g.pushErr
	}
	g.pushed = true
	return g.commitSHA, nil
}

func (g *fakeGit) Push(_ context.Context, _, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pushErr != nil {
		g.pushAtCommit = true
		return g.pushErr
	}
	g.pushed = true
	return nil
}

func (g *fakeGit) Commit(_ context.Context, _, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commitCall++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	return g.commitSHA, nil
}

func (g *fakeGit) ResetHard(_ context.Context, _, ref string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetCalls = append(g.resetCalls, ref)
	return g.resetErr
}

// applyFleet builds a tmp fleet repo (mirroring setupValidFleet for
// scaffold tests) plus seed metadata to support Apply's full
// pipeline.
func applyFleet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// applyRunner builds a fake ScriptRunner that emits successful
// add-cluster.sh + flux-install.sh exit lines. The onRun callback
// writes the scaffold "output" files so Scaffold's post-process
// step has something to consume.
func applyRunner(t *testing.T, repo string) *fakeScriptRunner {
	t.Helper()
	envBody := "CLUSTER_NAME=atlantis\nDOMAIN=kdc.atlantis.example.com\n"
	infraBody := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-cni
spec:
  interval: 10m
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
spec:
  interval: 10m
`
	encryptedSecrets := `stringData:
    KEYCLOAK_ADMIN_PASSWORD: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
sops:
    mac: ENC[AES256_GCM,data:mac,iv:miv]
`
	return &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			// Skip onRun for flux-install (it doesn't write files;
			// the script-runner contract is one onRun per call,
			// shared across both invocations — we detect by
			// checking whether the cluster overlay already exists).
			if _, err := os.Stat(clusterDir); err == nil {
				return nil // second call (flux-install) — no-op
			}
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte(envBody), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "infrastructure.yaml"), []byte(infraBody), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(encryptedSecrets), 0o644)
		},
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> Creating cluster overlay", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}
}

func atlantisApplyOpts(t *testing.T, repo string, runner ports.ScriptRunner, git ports.GitClient) ApplyOptions {
	t.Helper()
	return ApplyOptions{
		Plan: &Plan{
			ClusterName: "atlantis",
			Domain:      "kdc.atlantis.example.com",
			Preset:      PresetCloudPublicVLAN,
		},
		FleetRepo:      repo,
		NodeExternalIP: "203.0.113.52",
		Sets: map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "10.0.0.0/24",
			"EXT_PUBLIC_GATEWAY": "10.0.0.1",
		},
		NodeNICs:    map[string]string{"HOST5-A": "enp1s0"},
		Runner:      runner,
		Git:         git,
		GitHubToken: "fake-token",
	}
}

// M4-T05 GitLab close: runFluxInstall MUST propagate provider +
// owner/repo/token env to the fleet script so `flux-install.sh`
// can dispatch `flux bootstrap gitlab` vs `flux bootstrap github`.
// Default (empty Provider) → KUBE_DC_PROVIDER=github + GITHUB_TOKEN.
func TestApply_FluxInstall_GitHubEnv_Propagated(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{preSHA: "abc123"}
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.Provider = "" // default → github
	opts.GitHubOwner = "acme"
	opts.GitHubRepo = "fleet"
	opts.GitHubToken = "ghp_TEST123"
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Find the flux-install call in the runner's log — it's the
	// call to ScriptFluxInstall.
	var fluxCall *fakeScriptCall
	for i := range runner.calls {
		if runner.calls[i].Kind == ports.ScriptFluxInstall {
			fluxCall = &runner.calls[i]
			break
		}
	}
	if fluxCall == nil {
		t.Fatalf("no flux-install call in runner log")
	}
	if got := fluxCall.Env["KUBE_DC_PROVIDER"]; got != "github" {
		t.Errorf("KUBE_DC_PROVIDER = %q, want github", got)
	}
	if got := fluxCall.Env["GITHUB_OWNER"]; got != "acme" {
		t.Errorf("GITHUB_OWNER = %q, want acme", got)
	}
	if got := fluxCall.Env["GITHUB_REPO"]; got != "fleet" {
		t.Errorf("GITHUB_REPO = %q, want fleet", got)
	}
	if got := fluxCall.Env["GITHUB_TOKEN"]; got != "ghp_TEST123" {
		t.Errorf("GITHUB_TOKEN = %q, want ghp_TEST123", got)
	}
	if _, gitlab := fluxCall.Env["GITLAB_TOKEN"]; gitlab {
		t.Errorf("GITLAB_TOKEN should NOT be set on github provider; env=%v", fluxCall.Env)
	}
}

// GitLab provider path: KUBE_DC_PROVIDER=gitlab + GITLAB_TOKEN (not
// GITHUB_TOKEN). Same owner/repo env-var names (github-* naming is
// carried as-is for backward compat).
func TestApply_FluxInstall_GitLabEnv_Propagated(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{preSHA: "abc123"}
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.Provider = ProviderGitLab
	opts.GitHubOwner = "acme-group"
	opts.GitHubRepo = "fleet"
	opts.GitHubToken = "glpat_TEST456"
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var fluxCall *fakeScriptCall
	for i := range runner.calls {
		if runner.calls[i].Kind == ports.ScriptFluxInstall {
			fluxCall = &runner.calls[i]
			break
		}
	}
	if fluxCall == nil {
		t.Fatalf("no flux-install call in runner log")
	}
	if got := fluxCall.Env["KUBE_DC_PROVIDER"]; got != "gitlab" {
		t.Errorf("KUBE_DC_PROVIDER = %q, want gitlab", got)
	}
	if got := fluxCall.Env["GITLAB_TOKEN"]; got != "glpat_TEST456" {
		t.Errorf("GITLAB_TOKEN = %q, want glpat_TEST456", got)
	}
	if _, gh := fluxCall.Env["GITHUB_TOKEN"]; gh {
		t.Errorf("GITHUB_TOKEN should NOT be set on gitlab provider; env=%v", fluxCall.Env)
	}
	if got := fluxCall.Env["GITHUB_OWNER"]; got != "acme-group" {
		t.Errorf("GITHUB_OWNER = %q, want acme-group (backward-compat naming)", got)
	}
}

func TestApply_HappyPath(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{
		preSHA:    "abc123",
		commitSHA: "def456",
	}

	var out bytes.Buffer
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.Out = &out
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply: %v\nout:\n%s", err, out.String())
	}

	// Sequence: head → diff → scaffold → commitAndPush → flux-install
	if git.commitCall != 1 {
		t.Errorf("commit call count = %d, want 1", git.commitCall)
	}
	if !git.pushed {
		t.Error("push didn't happen on happy path")
	}
	if len(git.resetCalls) != 0 {
		t.Errorf("happy path triggered rollback: %v", git.resetCalls)
	}
	// Both scripts ran (add-cluster.sh + flux-install.sh).
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 script calls (add-cluster + flux-install), got %d", len(runner.calls))
	}
	if runner.calls[0].Kind != ports.ScriptAddCluster {
		t.Errorf("first script = %v, want ScriptAddCluster", runner.calls[0].Kind)
	}
	if runner.calls[1].Kind != ports.ScriptFluxInstall {
		t.Errorf("second script = %v, want ScriptFluxInstall", runner.calls[1].Kind)
	}
	// flux-install args.
	wantFlux := []string{"atlantis", "--new-cluster"}
	if len(runner.calls[1].Args) != len(wantFlux) {
		t.Errorf("flux-install args = %v, want %v", runner.calls[1].Args, wantFlux)
	}
	// Operator-facing log mentions commit + completion.
	for _, want := range []string{
		"commit=def456",
		"complete — Flux is reconciling cluster \"atlantis\"",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestApply_PushFailure_RollsBackToPreSHA(t *testing.T) {
	// Critical: push fails → ResetHard MUST be called with the SHA
	// captured BEFORE the commit attempt (memory contract: never
	// HEAD~1). The fakeGit records ResetHard ref args; we assert
	// the value matches the preSHA we seeded.
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	pushErr := errors.New("simulated push failure: network unreachable")
	git := &fakeGit{
		preSHA:    "PRECOMMIT-SHA-WE-CAPTURED",
		commitSHA: "POSTCOMMIT-SHA-WE-DISCARD",
		pushErr:   pushErr,
	}

	var out bytes.Buffer
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.Out = &out
	err := Apply(context.Background(), opts)
	if err == nil {
		t.Fatal("expected push failure to surface as error")
	}
	if !errors.Is(err, pushErr) {
		t.Fatalf("expected wrap of push error, got %v", err)
	}
	// Exactly one ResetHard call, with the captured pre-commit SHA.
	if len(git.resetCalls) != 1 {
		t.Fatalf("expected 1 ResetHard call, got %d: %v", len(git.resetCalls), git.resetCalls)
	}
	if git.resetCalls[0] != "PRECOMMIT-SHA-WE-CAPTURED" {
		t.Fatalf("ResetHard called with %q, want PRECOMMIT-SHA-WE-CAPTURED (never HEAD~1)",
			git.resetCalls[0])
	}
	// flux-install must NOT run after push failure.
	for _, c := range runner.calls {
		if c.Kind == ports.ScriptFluxInstall {
			t.Errorf("flux-install ran despite push failure: %v", c)
		}
	}
	// Operator-facing log mentions rollback.
	if !strings.Contains(out.String(), "rolling back") {
		t.Errorf("output should mention rollback:\n%s", out.String())
	}
}

func TestApply_PushAndRollbackBothFail_Wraps(t *testing.T) {
	// Defensive: when push fails AND rollback fails, both errors
	// must surface so the operator knows manual recovery is needed.
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	pushErr := errors.New("push failed")
	resetErr := errors.New("reset failed too")
	git := &fakeGit{
		preSHA:    "pre",
		commitSHA: "post",
		pushErr:   pushErr,
		resetErr:  resetErr,
	}
	opts := atlantisApplyOpts(t, repo, runner, git)
	err := Apply(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "manual recovery needed") {
		t.Errorf("error should signal manual-recovery: %v", err)
	}
	// Both push and rollback errors should appear in the message.
	if !strings.Contains(err.Error(), "push failed") || !strings.Contains(err.Error(), "reset failed too") {
		t.Errorf("error should mention both failures: %v", err)
	}
}

func TestApply_DirtyTree_Refused(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{
		preSHA: "abc",
		diff: ports.Diff{
			Files: []ports.FileDiff{
				{Path: "infrastructure/cni/values.yaml", Status: "M"},
				{Path: "addons/metallb/some-untracked", Status: "??"},
			},
		},
	}
	opts := atlantisApplyOpts(t, repo, runner, git)
	err := Apply(context.Background(), opts)
	if !errors.Is(err, ErrDirtyWorkingTree) {
		t.Fatalf("expected ErrDirtyWorkingTree, got %v", err)
	}
	// Dirty paths surface in the error message.
	for _, want := range []string{"infrastructure/cni/values.yaml", "addons/metallb/some-untracked"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list dirty path %q: %v", want, err)
		}
	}
	// Scaffold + commit must NOT have run.
	if len(runner.calls) != 0 {
		t.Errorf("Scaffold ran despite dirty tree: %v", runner.calls)
	}
	if git.commitCall != 0 {
		t.Error("commit ran despite dirty tree")
	}
}

func TestApply_NoPush_SkipsFluxInstall(t *testing.T) {
	// --no-push: local commit lands but Flux can't reconcile from
	// an unpushed commit, so flux-install is skipped. Operator sees
	// an explicit "skipping flux-install" log.
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{
		preSHA:    "abc",
		commitSHA: "def",
	}
	var out bytes.Buffer
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.NoPush = true
	opts.Out = &out
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply --no-push: %v", err)
	}
	if !strings.Contains(out.String(), "skipping flux-install") {
		t.Errorf("output should mention skipped flux-install:\n%s", out.String())
	}
	// Only one script (add-cluster.sh); flux-install must NOT run.
	if len(runner.calls) != 1 {
		t.Errorf("--no-push should run only add-cluster.sh, got %d calls", len(runner.calls))
	}
}

func TestApply_FluxInstallFailure_NoRollback(t *testing.T) {
	// flux-install failure happens AFTER successful push — the
	// remote already has the commit. ResetHard would only roll
	// back locally, not the published state. The contract is
	// "surface the error, operator fixes forward" (no auto-revert).
	repo := applyFleet(t)
	// Runner: add-cluster.sh succeeds; flux-install.sh fails.
	envBody := "CLUSTER_NAME=test\n"
	infraBody := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-core
spec: {}
`
	encryptedSecrets := `stringData:
    K: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
sops:
    mac: ENC[AES256_GCM,data:mac,iv:miv]
`
	addClusterDone := false
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		// onRun fires for BOTH script invocations; only do the
		// scaffold writes on the first one.
		onRun: func(clusterDir string) error {
			if addClusterDone {
				return nil // flux-install — no file writes
			}
			addClusterDone = true
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte(envBody), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "infrastructure.yaml"), []byte(infraBody), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(encryptedSecrets), 0o644)
		},
		// First call (add-cluster.sh) returns exit 0 + completion.
		// Second call (flux-install.sh) will use whatever runner.lines
		// holds — we provide a non-zero exit. Since fakeScriptRunner
		// uses the same `lines` for every call, the trick is to
		// make the lines slice produce success-then-failure. A
		// simpler approach: swap lines mid-flight via runErr.
		// Cleanest: emit a non-zero exit on every call, accepting
		// that add-cluster.sh "fails" — that would short-circuit
		// before flux-install. So instead, we use ONE script call
		// total + assert flux-install's "AFTER successful commit"
		// branch by making runner.runErr trip on the second call.
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> step", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}
	// Capture call count to flip the second invocation to exit 1.
	origRunner := runner
	wrappedRunner := &flippingScriptRunner{wrapped: origRunner, failOnCall: 2}
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}

	var out bytes.Buffer
	opts := atlantisApplyOpts(t, repo, wrappedRunner, git)
	opts.Runner = wrappedRunner
	opts.Out = &out
	err := Apply(context.Background(), opts)
	if !errors.Is(err, ErrApplyFluxInstallFailed) {
		t.Fatalf("expected ErrApplyFluxInstallFailed, got %v\nout:\n%s", err, out.String())
	}
	// Critical: NO rollback after successful push (remote already
	// has the commit; local rollback would be misleading).
	if len(git.resetCalls) != 0 {
		t.Errorf("flux-install failure must NOT trigger rollback (remote has commit); got %v",
			git.resetCalls)
	}
	if !git.pushed {
		t.Error("expected push to have succeeded before flux-install fired")
	}
}

func TestApply_ScaffoldFailure_CleansUpPartialClusterDir(t *testing.T) {
	// M4-T12 review-pass — P2: when Scaffold fails after writing
	// partial files (e.g. plaintext secrets.enc.yaml on the SOPS
	// fallback path), Apply must clean up the cluster dir so the
	// operator's working tree doesn't carry leftover state — in
	// particular potential plaintext credentials.
	repo := applyFleet(t)
	plaintextSecrets := `apiVersion: v1
stringData:
    KEYCLOAK_ADMIN_PASSWORD: LITERAL-PLAINTEXT-SHOULD-BE-CLEANED-UP
`
	// Runner writes plaintext (script's sops-fallback path). Scaffold's
	// verifySOPSEncrypted will reject, returning ErrScaffoldSecretsNotEncrypted.
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte("CLUSTER_NAME=x\n"), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(plaintextSecrets), 0o644)
		},
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> step", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	var out bytes.Buffer
	opts := atlantisApplyOpts(t, repo, runner, git)
	opts.Out = &out

	err := Apply(context.Background(), opts)
	if !errors.Is(err, ErrScaffoldSecretsNotEncrypted) {
		t.Fatalf("expected ErrScaffoldSecretsNotEncrypted, got %v", err)
	}

	// Critical: the cluster directory must have been cleaned up so
	// the plaintext secret can't be accidentally git-added by the
	// operator.
	clusterDir := filepath.Join(repo, "clusters", opts.Plan.ClusterName)
	if _, err := os.Stat(clusterDir); !errors.Is(err, os.ErrNotExist) {
		// List what's left so a regression is debuggable.
		entries, _ := os.ReadDir(clusterDir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("partial scaffold survived cleanup at %s: %v (stat err=%v)", clusterDir, names, err)
	}
	if !strings.Contains(out.String(), "cleaned up partial scaffold") {
		t.Errorf("output should mention cleanup:\n%s", out.String())
	}
	// Commit/push must NOT have run.
	if git.commitCall != 0 {
		t.Errorf("commit ran after Scaffold failure")
	}
}

func TestApply_MissingDependencies_Errors(t *testing.T) {
	repo := applyFleet(t)
	runner := applyRunner(t, repo)
	git := &fakeGit{preSHA: "x", commitSHA: "y"}
	base := atlantisApplyOpts(t, repo, runner, git)

	cases := []struct {
		name   string
		mutate func(*ApplyOptions)
		wantIn string
	}{
		{"nil Plan", func(o *ApplyOptions) { o.Plan = nil }, "Plan"},
		{"empty FleetRepo", func(o *ApplyOptions) { o.FleetRepo = "" }, "FleetRepo"},
		{"empty NodeExternalIP", func(o *ApplyOptions) { o.NodeExternalIP = "" }, "NodeExternalIP"},
		{"nil Runner", func(o *ApplyOptions) { o.Runner = nil }, "Runner"},
		{"nil Git", func(o *ApplyOptions) { o.Git = nil }, "Git"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			err := Apply(context.Background(), o)
			if !errors.Is(err, ErrApplyMissingDependency) {
				t.Fatalf("expected ErrApplyMissingDependency, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error should name missing field %q: %v", tc.wantIn, err)
			}
		})
	}
}

// flippingScriptRunner wraps a fakeScriptRunner + makes the Nth
// call's exit code 1 instead of 0. Used to simulate flux-install's
// failure on the second invocation (after add-cluster.sh succeeds).
type flippingScriptRunner struct {
	wrapped    *fakeScriptRunner
	failOnCall int

	calls int
}

// WithSentinelCallback satisfies the ports.ScriptRunner interface.
// flippingScriptRunner wraps fakeScriptRunner so we delegate;
// neither exercises sentinel capture in these tests.
func (f *flippingScriptRunner) WithSentinelCallback(_ ports.SentinelCallback) ports.ScriptRunner {
	return f
}

func (f *flippingScriptRunner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	f.calls++
	if f.calls != f.failOnCall {
		return f.wrapped.Run(ctx, name, env, args...)
	}
	// Build a failing-exit stream for the targeted call.
	out := make(chan ports.Line, 2)
	out <- ports.Line{Stream: ports.StreamStderr, Text: fmt.Sprintf("ERROR: simulated %s failure", name), Time: time.Now()}
	out <- ports.Line{Stream: ports.StreamExit, Text: "1", Time: time.Now()}
	close(out)
	// Record the call against the wrapped runner so tests can
	// inspect the args downstream.
	f.wrapped.calls = append(f.wrapped.calls, fakeScriptCall{Kind: name, Env: env, Args: args})
	return out, nil
}

// TestApply_Resume_PushesThenFluxInstall proves the resume path (existing
// overlay → ErrScaffoldTargetExists): rather than fail, Apply pushes HEAD
// — a clean working tree does NOT prove the committed overlay is on the
// remote branch Flux reconciles — and then runs flux-install. Regression
// guard for the P1 resume-correctness fix.
func TestApply_Resume_PushesThenFluxInstall(t *testing.T) {
	repo := applyFleet(t)
	// Pre-create the overlay so Scaffold's preflight returns
	// ErrScaffoldTargetExists and Apply takes the resume branch.
	overlay := filepath.Join(repo, "clusters", "atlantis")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatal(err)
	}
	// Scaffold's "already initialised" marker is cluster-config.env —
	// write it so the preflight returns ErrScaffoldTargetExists (resume).
	if err := os.WriteFile(filepath.Join(overlay, "cluster-config.env"), []byte("CLUSTER_NAME=atlantis\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := applyRunner(t, repo)
	git := &fakeGit{preSHA: "abc123", commitSHA: "def456"}

	if err := Apply(context.Background(), atlantisApplyOpts(t, repo, runner, git)); err != nil {
		t.Fatalf("resume apply: %v", err)
	}
	if !git.pushed {
		t.Fatal("resume must Push HEAD before flux-install (a clean tree doesn't prove the overlay is pushed)")
	}
	if git.commitCall != 0 {
		t.Fatalf("resume created %d new commit(s); unchanged GPU manifests would trigger an unnecessary Flux apply", git.commitCall)
	}
	var fluxRan bool
	for _, c := range runner.calls {
		if c.Kind == ports.ScriptAddCluster {
			t.Fatal("resume reran add-cluster/scaffold; healthy GPU operands must remain untouched")
		}
		if c.Kind == ports.ScriptFluxInstall {
			fluxRan = true
		}
	}
	if !fluxRan {
		t.Fatal("resume must run flux-install after pushing HEAD")
	}
}

// TestApply_Resume_PushFailure_BlocksFluxInstall proves a resume push
// failure is fatal — we must not point flux-install at a remote we can't
// confirm contains the overlay.
func TestApply_Resume_PushFailure_BlocksFluxInstall(t *testing.T) {
	repo := applyFleet(t)
	overlay := filepath.Join(repo, "clusters", "atlantis")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatal(err)
	}
	// Scaffold's "already initialised" marker is cluster-config.env —
	// write it so the preflight returns ErrScaffoldTargetExists (resume).
	if err := os.WriteFile(filepath.Join(overlay, "cluster-config.env"), []byte("CLUSTER_NAME=atlantis\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := applyRunner(t, repo)
	pushErr := errors.New("simulated resume push failure")
	git := &fakeGit{preSHA: "abc123", pushErr: pushErr}

	err := Apply(context.Background(), atlantisApplyOpts(t, repo, runner, git))
	if !errors.Is(err, pushErr) {
		t.Fatalf("want resume push error, got %v", err)
	}
	for _, c := range runner.calls {
		if c.Kind == ports.ScriptFluxInstall {
			t.Fatal("flux-install ran despite resume push failure")
		}
	}
}
