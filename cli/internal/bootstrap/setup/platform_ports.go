package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	bao "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func guardPlatformSession(s *bootstrap.Session, g *platformExecutor, claims *HostClaims) {
	s.Git = &platformGit{GitClient: s.Git, gate: g}
	s.Scripts = &platformScripts{ScriptRunner: s.Scripts, gate: g}
	s.K8s = &platformK8s{K8sClient: s.K8s, gate: g}
	if s.Scenario == "" {
		s.OpenBao = bao.New(s.K8s)
	}
	s.SOPS = &platformSOPS{SOPSClient: s.SOPS, gate: g}
	s.SSH = &platformSSH{CappedSSHHostKeyClient: claims.GuardedSSH(), claims: claims, gate: g}
}

type platformGit struct {
	ports.GitClient
	gate *platformExecutor
}

func (g *platformGit) ForProvider(ctx context.Context, dir, host string) (ports.GitClient, error) {
	binder, ok := g.GitClient.(ports.GitProviderBinder)
	if !ok {
		return nil, fmt.Errorf("Git adapter cannot bind provider")
	}
	bound, err := binder.ForProvider(ctx, dir, host)
	if err != nil {
		return nil, err
	}
	return &platformGit{bound, g.gate}, nil
}
func (g *platformGit) write(ctx context.Context, dir string) error {
	if dir != g.gate.execution.Options.Repo {
		return fmt.Errorf("unreviewed Git directory")
	}
	return g.gate.CheckExecution(ctx)
}
func (g *platformGit) Commit(ctx context.Context, dir, msg string) (string, error) {
	if err := g.write(ctx, dir); err != nil {
		return "", err
	}
	return g.GitClient.Commit(ctx, dir, msg)
}
func (g *platformGit) CommitAndPush(ctx context.Context, dir, msg, token string) (string, error) {
	if g.gate.execution.Options.NoPush {
		return "", fmt.Errorf("reviewed execution disables remote publication")
	}
	if err := g.write(ctx, dir); err != nil {
		return "", err
	}
	return g.GitClient.CommitAndPush(ctx, dir, msg, token)
}
func (g *platformGit) Push(ctx context.Context, dir, token string) error {
	if g.gate.execution.Options.NoPush {
		return fmt.Errorf("reviewed execution disables remote publication")
	}
	if err := g.write(ctx, dir); err != nil {
		return err
	}
	return g.GitClient.Push(ctx, dir, token)
}
func (g *platformGit) ResetHard(context.Context, string, string) error {
	return fmt.Errorf("guarded Git failure retains local and remote evidence; inspect before recovery")
}
func (g *platformGit) Pull(context.Context, string, string) error {
	return fmt.Errorf("guarded execution cannot pull over reviewed state")
}
func (g *platformGit) Clone(context.Context, string, string, string) error {
	return fmt.Errorf("guarded execution cannot clone an unreviewed repository")
}
func (g *platformGit) Init(context.Context, string, string) error {
	return fmt.Errorf("guarded execution requires an existing reviewed repository")
}
func (g *platformGit) CreateRepo(context.Context, string, string, bool, string) error {
	return fmt.Errorf("provider repository creation requires a separate review")
}

type platformScripts struct {
	ports.ScriptRunner
	gate *platformExecutor
}

func (s *platformScripts) WithSentinelCallback(cb ports.SentinelCallback) ports.ScriptRunner {
	return &platformScripts{s.ScriptRunner.WithSentinelCallback(cb), s.gate}
}
func (s *platformScripts) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	if err := s.gate.CheckExecution(ctx); err != nil {
		return nil, err
	}
	s.gate.mu.Lock()
	active := s.gate.active
	s.gate.mu.Unlock()
	allowed := (active == clusterinit.StepFluxInstall && name == ports.ScriptFluxInstall) || (active == clusterinit.StepOpenBao && name == ports.ScriptOpenBaoInit) || (active == clusterinit.StepKeycloakOIDC && name == ports.ScriptSetupKeycloakOIDC)
	if !allowed {
		return nil, fmt.Errorf("script is outside the admitted stage")
	}
	lines, err := s.ScriptRunner.Run(ctx, name, env, args...)
	if err != nil {
		return nil, err
	}
	out := make(chan ports.Line, 64)
	go func() {
		defer close(out)
		exit := false
		for line := range lines {
			if line.Stream == ports.StreamExit {
				exit = true
			}
			select {
			case out <- line:
			case <-ctx.Done():
			}
		}
		if !exit || ctx.Err() != nil {
			s.gate.executorLost()
		}
	}()
	return out, nil
}

type platformK8s struct {
	ports.K8sClient
	gate *platformExecutor
}

func (k *platformK8s) SetNodeLabel(ctx context.Context, node, key, value string) error {
	if err := k.gate.CheckExecution(ctx); err != nil {
		return err
	}
	return k.K8sClient.SetNodeLabel(ctx, node, key, value)
}
func (k *platformK8s) PodExec(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if err := k.gate.CheckExecution(ctx); err != nil {
		return nil, err
	}
	body, err := k.K8sClient.PodExec(ctx, ns, pod, cmd, stdin)
	if err != nil && !readOnlyBaoCommand(cmd) {
		k.gate.executorLost()
	}
	return body, err
}
func (k *platformK8s) PodExecViaKubectl(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if err := k.gate.CheckExecution(ctx); err != nil {
		return nil, err
	}
	body, err := k.K8sClient.PodExecViaKubectl(ctx, ns, pod, cmd, stdin)
	if err != nil && !readOnlyBaoCommand(cmd) {
		k.gate.executorLost()
	}
	return body, err
}
func (k *platformK8s) SetServiceAnnotation(ctx context.Context, ns, svc, key, value string) error {
	if err := k.gate.CheckExecution(ctx); err != nil {
		return err
	}
	return k.K8sClient.SetServiceAnnotation(ctx, ns, svc, key, value)
}
func (k *platformK8s) SetServiceAnnotations(ctx context.Context, ns, svc string, kv map[string]string) error {
	if err := k.gate.CheckExecution(ctx); err != nil {
		return err
	}
	return k.K8sClient.SetServiceAnnotations(ctx, ns, svc, kv)
}

type platformSOPS struct {
	ports.SOPSClient
	gate *platformExecutor
}

func (s *platformSOPS) check(ctx context.Context, path string) error {
	if err := s.gate.CheckExecution(ctx); err != nil {
		return err
	}
	rel, err := filepath.Rel(s.gate.execution.Options.Repo, path)
	if err != nil {
		return err
	}
	s.gate.mu.Lock()
	active := s.gate.active
	s.gate.mu.Unlock()
	if !platformPathAllowed(active, s.gate.execution.Options.Name, filepath.ToSlash(rel), s.gate.execution.Prepared) {
		return fmt.Errorf("SOPS destination is outside the reviewed stage")
	}
	return nil
}
func (s *platformSOPS) Encrypt(ctx context.Context, path string) error {
	if err := s.check(ctx, path); err != nil {
		return err
	}
	return s.SOPSClient.Encrypt(ctx, path)
}
func (s *platformSOPS) SetStringData(ctx context.Context, path, key string, value []byte) error {
	if err := s.check(ctx, path); err != nil {
		return err
	}
	return s.SOPSClient.SetStringData(ctx, path, key, value)
}

type platformSSH struct {
	ports.CappedSSHHostKeyClient
	claims *HostClaims
	gate   *platformExecutor
}

func (s *platformSSH) target(host ports.SSHHost) (ports.SSHHost, error) {
	for _, h := range s.claims.compiled.Spec.Hosts {
		_, endpoint, err := s.claims.expected(h)
		if err != nil {
			continue
		}
		plain := endpoint
		plain.ExpectedHostKeySHA256 = ""
		given := host
		given.ExpectedHostKeySHA256 = ""
		if given == plain && (host.ExpectedHostKeySHA256 == "" || host.ExpectedHostKeySHA256 == endpoint.ExpectedHostKeySHA256) {
			return endpoint, nil
		}
	}
	return ports.SSHHost{}, fmt.Errorf("SSH endpoint is outside reviewed targets")
}
func (s *platformSSH) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	if err := s.gate.CheckExecution(ctx); err != nil {
		return nil, err
	}
	host, err := s.target(host)
	if err != nil {
		return nil, err
	}
	body, err := s.CappedSSHHostKeyClient.Run(ctx, host, cmd)
	if err != nil {
		s.gate.executorLost()
	}
	return body, err
}
func (s *platformSSH) Put(ctx context.Context, host ports.SSHHost, path string, body []byte, mode uint32) error {
	if err := s.gate.CheckExecution(ctx); err != nil {
		return err
	}
	host, err := s.target(host)
	if err != nil {
		return err
	}
	err = s.CappedSSHHostKeyClient.Put(ctx, host, path, body, mode)
	if err != nil {
		s.gate.executorLost()
	}
	return err
}

func readOnlyBaoCommand(cmd []string) bool {
	joined := strings.Join(cmd, "\x00")
	return joined == "true" || joined == "bao\x00status\x00-format=json" || joined == "/bin/sh\x00-c\x00bao status -format=json || [ \"$?\" -eq 2 ]" || joined == "bao\x00operator\x00generate-root\x00-status\x00-format=json"
}
func (s *platformSSH) Fetch(ctx context.Context, host ports.SSHHost, path string) ([]byte, error) {
	host, err := s.target(host)
	if err != nil {
		return nil, err
	}
	return s.CappedSSHHostKeyClient.Fetch(ctx, host, path)
}
