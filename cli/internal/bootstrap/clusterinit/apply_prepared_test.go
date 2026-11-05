package clusterinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type preparedTestExecutor struct {
	NopReporter
	stages []StepID
	deny   StepID
}

func (e *preparedTestExecutor) ExecuteStep(id StepID, run func() error) error {
	e.stages = append(e.stages, id)
	if id == e.deny {
		return errors.New("denied before effect")
	}
	return run()
}
func (e *preparedTestExecutor) CheckExecution(context.Context) error { return nil }

type preparedTestGit struct{ *fakeGit }

func (g *preparedTestGit) ForProvider(context.Context, string, string) (ports.GitClient, error) {
	return g, nil
}

type preparedTestK8s struct {
	ports.K8sClient
	labels map[string]map[string]string
}

func (k *preparedTestK8s) NodeLabels(context.Context) (map[string]map[string]string, error) {
	return k.labels, nil
}
func (k *preparedTestK8s) SetNodeLabel(_ context.Context, node, key, value string) error {
	k.labels[node][key] = value
	return nil
}

func TestPreparedApplyUsesReviewedBytesAndAbortableStages(t *testing.T) {
	for _, kind := range []string{"complete", "denied", "push-error"} {
		t.Run(kind, func(t *testing.T) {
			p, fleet := preparedFixture(t)
			runner := &fakeScriptRunner{}
			git := &preparedTestGit{&fakeGit{preSHA: "before", commitSHA: "ours", diff: ports.Diff{Files: []ports.FileDiff{{Path: p.Files[0].Path, Status: "A"}}}}}
			executor := &preparedTestExecutor{}
			if kind == "denied" {
				executor.deny = StepScaffold
			}
			if kind == "push-error" {
				git.pushErr = errors.New("response lost after remote accepted commit")
			}
			plan := &Plan{ClusterName: p.Cluster, Domain: "example.test", Preset: PresetInternalOnly, IngressNodes: []string{"server-1"}, IngressNodeLabel: "kube-dc.com/ingress"}
			err := Apply(context.Background(), ApplyOptions{Plan: plan, FleetRepo: fleet, NodeExternalIP: "192.0.2.10", KubeconfigPath: "/private/child", Runner: runner, Git: git, K8s: &preparedTestK8s{labels: map[string]map[string]string{"server-1": {}}}, NoPush: kind != "push-error", Reporter: executor, Prepared: &p})
			if kind == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				if err := VerifyPreparedPublication(p); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failed admission or push reported success")
			}
			if kind == "denied" {
				if _, err := os.Stat(filepath.Join(fleet, "clusters", p.Cluster)); !os.IsNotExist(err) {
					t.Fatal("denied scaffold wrote files")
				}
				if git.commitCall != 0 {
					t.Fatal("continued after denied stage")
				}
			}
			if kind == "push-error" && (!git.pushAtCommit || len(git.resetCalls) != 0) {
				t.Fatalf("push not reached or evidence reset: pushed=%v resets=%v error=%v", git.pushAtCommit, git.resetCalls, err)
			}
			if len(runner.calls) != 0 {
				t.Fatal("prepared apply reran the generator or continued into Flux after failure")
			}
		})
	}
}
