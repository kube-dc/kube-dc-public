package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	gitadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/git"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type platformFixtureEngine struct {
	before               func(PlatformExecution, clusterinit.StepID)
	afterPublication     func(PlatformExecution)
	failObserve          bool
	failAfterPublication bool
	effects              int
}

func (e *platformFixtureEngine) Run(ctx context.Context, x PlatformExecution) error {
	for _, id := range []clusterinit.StepID{clusterinit.StepScaffold, clusterinit.StepCommitPush} {
		if err := clusterinit.ExecuteStep(x.Reporter, id, func() error {
			e.effects++
			if e.before != nil {
				e.before(x, id)
			}
			if id == clusterinit.StepScaffold {
				checker := x.Reporter.(clusterinit.ExecutionChecker)
				if err := clusterinit.PublishPreparedScaffold(ctx, x.Prepared, x.Options.Repo, checker.CheckExecution); err != nil {
					return err
				}
				if e.afterPublication != nil {
					e.afterPublication(x)
				}
				if e.failAfterPublication {
					return errors.New("lost response")
				}
				return nil
			}
			_, err := x.Session.Git.Commit(ctx, x.Options.Repo, "reviewed fixture publication")
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
func (e *platformFixtureEngine) Observe(ctx context.Context, x PlatformExecution, id clusterinit.StepID) (PlatformObservation, error) {
	if e.failObserve {
		return PlatformObservation{}, errors.New("live condition unknown")
	}
	if id == clusterinit.StepScaffold {
		if err := clusterinit.VerifyPreparedPublication(x.Prepared); err != nil {
			return PlatformObservation{}, err
		}
	}
	if id == clusterinit.StepCommitPush {
		diff, err := x.Session.Git.Diff(ctx, x.Options.Repo)
		if err != nil || len(diff.Files) != 0 {
			return PlatformObservation{}, errors.New("uncommitted publication")
		}
	}
	checks := []EffectCheck{}
	for _, name := range requiredPlatformChecks(id) {
		checks = append(checks, EffectCheck{ID: name, SHA256: strings.Repeat("a", 64)})
	}
	return PlatformObservation{Verified: true, Checks: checks}, nil
}

func platformFixture(t *testing.T) (*SessionCoordinator, ReadinessReport, *platformFixtureEngine) {
	t.Helper()
	c, _, client := clusterReservationFixture(t)
	if err := c.reserveChildCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.compiled.Spec.Hosts)
	attempt, err := c.writer.Begin("rke2-first-server")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := c.writer.saveStageEvidence("rke2-first-server", attempt, primary, rke2.VerifiedNode{Name: primary.ID, Role: primary.Role, UID: "verified-primary-uid", InternalIP: primary.ManagementAddress}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writer.completeVerified("rke2-first-server", attempt, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Nodes().Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: primary.ID, UID: "verified-primary-uid", Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KernelVersion: "7.0.1"}, Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: primary.ManagementAddress}}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.realClaims = &HostClaims{compiled: c.compiled, review: c.review, runID: c.runID}
	c.platformArtifacts = func(context.Context) error { return nil }
	c.platformSession = func(context.Context, string) (*bootstrap.Session, error) {
		return &bootstrap.Session{Scenario: "fixture", Git: gitadapter.New()}, nil
	}
	r, _ := c.writer.Snapshot()
	ready := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, ReadyToApply: true, State: "ready", InputHash: r.InputHash, ReleaseSHA256: r.ReleaseSHA256, PlanHash: r.PlanHash}
	return c, ready, &platformFixtureEngine{}
}

func TestPlatformReviewAndReceiptsPreserveOriginalProofs(t *testing.T) {
	c, ready, engine := platformFixture(t)
	original, _ := c.writer.Snapshot()
	baseHash := c.review.Hash
	review, err := c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Effects) == 0 || review.Binding.ReviewHash != baseHash {
		t.Fatal("execution review lost original binding")
	}
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err != nil {
		t.Fatal(err)
	}
	r, err := ReadSession(c.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	if c.review.Hash != baseHash || r.ReviewHash != original.ReviewHash || r.Events[0].EvidenceSHA256 != original.Events[0].EvidenceSHA256 {
		t.Fatal("refresh rewrote historical safety/host proof")
	}
	for _, stage := range []string{"scaffold", "commit-push"} {
		event, _ := lastSessionEvent(r, stage)
		if event.State != "succeeded" || event.Source != "platform" {
			t.Fatal(event)
		}
	}
	refreshed, err := c.RefreshPlatformReview(context.Background())
	if err != nil || refreshed.PriorReceipt == "" || refreshed.Snapshot.Git.Head == review.Snapshot.Git.Head {
		t.Fatalf("review did not follow owned changes: %+v %v", refreshed, err)
	}
	before := engine.effects
	if err := c.RunPlatform(context.Background(), ready, refreshed.Hash, engine, nil, nil); err != nil || engine.effects != before {
		t.Fatalf("completed effects replayed: %v", err)
	}
}

func TestPlatformUnknownPostconditionRequiresLiveReconciliation(t *testing.T) {
	c, ready, engine := platformFixture(t)
	review, err := c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	engine.failObserve = true
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil {
		t.Fatal("callback success replaced live postcondition")
	}
	r, _ := c.writer.Snapshot()
	event, _ := lastSessionEvent(r, "scaffold")
	if event.State != "uncertain" || engine.effects != 1 {
		t.Fatal("unknown stage allowed later writes")
	}
	if _, err := c.RefreshPlatformReview(context.Background()); err == nil {
		t.Fatal("uncertain effect hidden by review refresh")
	}
	report, err := c.ReconcilePlatform(context.Background(), engine, true)
	if err != nil || report.Findings[0].State != "unresolved" {
		t.Fatalf("unknown inspection: %+v %v", report, err)
	}
	engine.failObserve = false
	report, err = c.ReconcilePlatform(context.Background(), engine, false)
	if err != nil || report.Findings[0].State != "verified" || report.MutationAuthority {
		t.Fatalf("%+v %v", report, err)
	}
	r, _ = c.writer.Snapshot()
	event, _ = lastSessionEvent(r, "scaffold")
	if event.State != "uncertain" {
		t.Fatal("read-only inspection changed journal")
	}
	report, err = c.ReconcilePlatform(context.Background(), engine, true)
	if err != nil || report.Findings[0].State != "recorded" {
		t.Fatalf("%+v %v", report, err)
	}
	review, err = c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err != nil || engine.effects != 2 {
		t.Fatalf("same-run positive recovery did not continue only remaining work: %v", err)
	}
}

func TestPlatformBlocksDriftAndLostOwnershipBeforeEffects(t *testing.T) {
	for _, kind := range []string{"worktree", "origin", "lease", "unqualified", "review", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			c, ready, engine := platformFixture(t)
			review, err := c.RefreshPlatformReview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "worktree":
				if err := os.WriteFile(filepath.Join(c.compiled.Init.Repo, "operator.txt"), []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "origin":
				gitTest(t, c.compiled.Init.Repo, "remote", "set-url", "origin", "/missing/repository")
			case "lease":
				client, err := c.childClient()(context.Background(), c.compiled, filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig"), firstServerEvidence("verified-primary-uid"))
				if err != nil {
					t.Fatal(err)
				}
				lease, err := client.CoordinationV1().Leases(clusterClaimNamespace).Get(context.Background(), clusterClaimName, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				lease.UID = "replacement"
				if _, err := client.CoordinationV1().Leases(clusterClaimNamespace).Update(context.Background(), lease, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "unqualified":
				ready.ReadyToApply = false
			case "review":
				review.Hash = strings.Repeat("0", 64)
			case "artifact":
				c.platformArtifacts = func(context.Context) error { return errors.New("changed CLI") }
			}
			if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil || engine.effects != 0 {
				t.Fatal("effect ran after failed admission")
			}
		})
	}
}

func TestPlatformProofFailureAndExecutorFenceSurviveRecovery(t *testing.T) {
	for _, kind := range []string{"proof-sync", "executor", "receipt-loss"} {
		t.Run(kind, func(t *testing.T) {
			c, ready, engine := platformFixture(t)
			review, err := c.RefreshPlatformReview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			engine.before = func(x PlatformExecution, id clusterinit.StepID) {
				if id == clusterinit.StepScaffold {
					switch kind {
					case "proof-sync":
						c.writer.syncDirectory = func(*os.Root) error { return errors.New("disk full") }
					}
				}
			}
			if kind == "executor" {
				engine.afterPublication = func(x PlatformExecution) { x.Reporter.(*platformExecutor).executorLost() }
			}
			err = c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil)
			if kind != "receipt-loss" && err == nil {
				t.Fatal("failure did not stop writes")
			}
			if kind == "receipt-loss" {
				r, _ := c.writer.Snapshot()
				event, _ := latestPlatformReceipt(r)
				if err := os.Remove(filepath.Join(c.writer.directory, "platform-"+event.EvidenceSHA256+".json")); err != nil {
					t.Fatal(err)
				}
				if _, err := c.writer.Snapshot(); err == nil {
					t.Fatal("missing receipt accepted")
				}
				return
			}
			if kind == "executor" {
				if !c.executorUncertain.Load() {
					t.Fatal("ambiguous executor fence missing")
				}
				report, err := c.ReconcilePlatform(context.Background(), engine, true)
				if err != nil || len(report.Findings) != 1 || report.Findings[0].State != "recorded" {
					t.Fatalf("positive inspection failed: %+v %v", report, err)
				}
				// Even a positive journal resolution cannot restore authority.
				c.executorUncertain.Store(false) // Simulate a fresh object reading the durable fence.
				r, _ := c.writer.Snapshot()
				if !r.ExecutorUncertain {
					t.Fatal("executor fence was not persisted")
				}
				if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil {
					t.Fatal("journal proof restored an ambiguous executor")
				}
			}
		})
	}
}

func TestPlatformReopenReconcilesEvidenceWithoutRestoringAuthority(t *testing.T) {
	c, ready, engine := platformFixture(t)
	review, err := c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	engine.failAfterPublication = true
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil {
		t.Fatal("lost response reported success")
	}
	if err := c.writer.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := OpenSession(c.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Match(c.compiled, c.review); err != nil {
		t.Fatal(err)
	}
	c.writer, c.runID = w, strings.Repeat("e", 64)
	report, err := c.ReconcilePlatform(context.Background(), engine, true)
	if err != nil || report.MutationAuthority || report.Findings[0].State != "recorded" {
		t.Fatalf("%+v %v", report, err)
	}
	review, err = c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := engine.effects
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil || engine.effects != before {
		t.Fatal("reopened review restored prior executor authority")
	}
}

func TestPlatformRejectsUnexpectedMutationAndPreservesGitOnPushFailure(t *testing.T) {
	c, ready, engine := platformFixture(t)
	review, err := c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	engine.before = func(x PlatformExecution, id clusterinit.StepID) {
		if id == clusterinit.StepScaffold {
			if err := x.Session.Git.ResetHard(context.Background(), x.Options.Repo, c.review.Git.Head); err == nil {
				t.Fatal("guarded path permitted reset")
			}
			if err := os.WriteFile(filepath.Join(x.Options.Repo, "foreign.txt"), []byte("operator edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil {
		t.Fatal("foreign mutation accepted")
	}
	if _, err := os.Stat(filepath.Join(c.compiled.Init.Repo, "foreign.txt")); err != nil {
		t.Fatal("operator work was removed")
	}
	r, _ := c.writer.Snapshot()
	event, _ := lastSessionEvent(r, "scaffold")
	if event.State != "uncertain" {
		t.Fatal(event)
	}
}

func TestPlatformBindsSharedFinalizationGitBeforeCredentialUse(t *testing.T) {
	directory, _ := gitBindingFixture(t)
	gitTest(t, directory, "remote", "set-url", "origin", "https://github.com/reviewed/fleet.git")
	s := &bootstrap.Session{Git: gitadapter.New()}
	o := clusterinit.InitOptions{Repo: directory, Provider: clusterinit.ProviderGitHub}
	if err := bindPlatformGit(context.Background(), o, s); err != nil {
		t.Fatal(err)
	}
	before, err := s.Git.Head(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "finalization.txt"), []byte("owned change"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, directory, "config", "remote.origin.pushurl", "https://wrong.example/stolen/fleet.git")
	if _, err := s.Git.CommitAndPush(context.Background(), directory, "finalization", "private-token"); err == nil {
		t.Fatal("finalization lost its provider binding")
	}
	after, err := s.Git.Head(context.Background(), directory)
	if err != nil || after != before {
		t.Fatal("binding failure created a local commit")
	}
}

var _ ports.GitProviderBinder = (*platformGit)(nil)

func TestPlatformRejectsStaleReviewAfterAPIOnlyProgress(t *testing.T) {
	c, ready, engine := platformFixture(t)
	review, err := c.RefreshPlatformReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := c.writer.Begin(string(clusterinit.StepIngressNodes))
	if err != nil {
		t.Fatal(err)
	}
	proof := PlatformReceipt{Binding: platformBinding(r), Stage: string(clusterinit.StepIngressNodes), Attempt: attempt, ReviewSHA256: r.PlatformReviews[0], ObservedAt: time.Now().UTC(), Snapshot: review.Snapshot, Observation: PlatformObservation{Verified: true, Checks: []EffectCheck{{ID: "ingress-nodes", SHA256: strings.Repeat("a", 64)}}}}
	hash, err := c.writer.savePlatformArtifact(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writer.completePlatform(proof.Stage, attempt, hash, false); err != nil {
		t.Fatal(err)
	}
	if err := c.RunPlatform(context.Background(), ready, review.Hash, engine, nil, nil); err == nil || engine.effects != 0 {
		t.Fatal("old review authorized effects after unchanged-Git progress")
	}
	if _, err := ReadSession(c.writer.directory); err != nil {
		t.Fatal("rejected review poisoned existing evidence")
	}
	refreshed, err := c.RefreshPlatformReview(context.Background())
	if err != nil || refreshed.PriorReceipt != hash {
		t.Fatalf("new review lost predecessor: %v", err)
	}
}
