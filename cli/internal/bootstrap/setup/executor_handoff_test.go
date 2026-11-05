package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func handoffFixture(t *testing.T) (*SessionCoordinator, *fixtureClaims, *fake.Clientset) {
	t.Helper()
	c, _, _ := platformFixture(t)
	f := c.claims.(*fixtureClaims)
	for id, claim := range f.remote {
		claim.ExecutorProtocol = "systemd-v1"
		claim.ExecutorUnit = "kube-dc-setup-11111111-1111-1111-1111-111111111111.service"
		f.remote[id] = claim
	}
	c.handoffTargets = func(context.Context) error { return nil }
	client, err := c.childClient()(context.Background(), c.compiled, filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig"), firstServerEvidence("verified-primary-uid"))
	if err != nil {
		t.Fatal(err)
	}
	api := client.(*fake.Clientset)
	// client-go's tracker does not implement the API's resourceVersion CAS.
	// This reactor provides the real exclusion primitive for competing runs.
	var mu sync.Mutex
	version := 0
	api.PrependReactor("update", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		next := a.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease).DeepCopy()
		obj, err := api.Tracker().Get(coordinationv1.SchemeGroupVersion.WithResource("leases"), next.Namespace, next.Name)
		if err != nil {
			return true, nil, err
		}
		old := obj.(*coordinationv1.Lease)
		if next.ResourceVersion != old.ResourceVersion || next.UID != old.UID {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, next.Name, errors.New("stale version"))
		}
		version++
		next.ResourceVersion = fmt.Sprintf("opaque-handoff-%d", version)
		err = api.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), next, next.Namespace)
		return true, next, err
	})
	c.handoffHost = handoffFixtureControl(f)
	return c, f, api
}

func handoffFixtureControl(f *fixtureClaims) func(context.Context, Host, HostClaim, string) (HostClaim, error) {
	return func(_ context.Context, h Host, want HostClaim, action string) (HostClaim, error) {
		got := f.remote[h.ID]
		if got.MachineID != want.MachineID || got.BootID != want.BootID || got.BootID != got.ObservedBootID || got.ExecutorProtocol != "systemd-v1" {
			return HostClaim{}, errors.New("host changed")
		}
		switch action {
		case "park":
			if got.RunID != want.SourceRunID || (got.State != "started" && got.State != "handoff") {
				return got, errors.New("not source")
			}
			if got.State == "handoff" && got.HandoffID != want.HandoffID {
				return got, errors.New("different handoff")
			}
			got.State, got.HandoffID, got.SourceRunID = "handoff", want.HandoffID, want.SourceRunID
		case "transfer":
			if got.State != "handoff" || got.HandoffID != want.HandoffID || got.SourceRunID != want.SourceRunID {
				return got, errors.New("not pending")
			}
			if want.HandoffEpoch < got.HandoffEpoch || (want.HandoffEpoch == got.HandoffEpoch && want.RunID != got.RunID) {
				return got, errors.New("stale handoff epoch")
			}
			got.RunID = want.RunID
			got.HandoffEpoch = want.HandoffEpoch
		case "activate":
			if got.State != "handoff" || got.HandoffID != want.HandoffID || got.SourceRunID != want.SourceRunID || got.RunID != want.RunID {
				return got, errors.New("not recipient")
			}
			got.State, got.HandoffID, got.SourceRunID = "started", "", ""
			got.HandoffEpoch = 0
		default:
			return got, errors.New("unsupported")
		}
		f.remote[h.ID] = got
		return got, nil
	}
}

func nextHandoffCoordinator(t *testing.T, old *SessionCoordinator, f *fixtureClaims) *SessionCoordinator {
	t.Helper()
	if err := old.CloseEvidence(); err != nil {
		t.Fatal(err)
	}
	w, err := OpenSession(old.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Match(old.compiled, old.review); err != nil {
		t.Fatal(err)
	}
	run, err := randomHandoffIdentity()
	if err != nil {
		t.Fatal(err)
	}
	next := &SessionCoordinator{compiled: old.compiled, review: old.review, writer: w, runID: run, sealed: true, realClaims: old.realClaims, claims: f, clusterClient: old.clusterClient, platformSession: old.platformSession, platformArtifacts: old.platformArtifacts, handoffTargets: old.handoffTargets, handoffHost: handoffFixtureControl(f)}
	f.coordinator = next
	t.Cleanup(func() { _ = next.CloseEvidence() })
	return next
}

func TestExecutorHandoffTransfersFreshAuthorityAndPreservesProofs(t *testing.T) {
	ctx := context.Background()
	c, f, _ := handoffFixture(t)
	before, _ := c.writer.Snapshot()
	oldRun := c.runID
	staleReview, err := c.RefreshPlatformReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.PrepareExecutorHandoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !c.sealed || h.Phase != "preparing" {
		t.Fatal("predecessor not sealed")
	}
	if _, err := c.Acquire(ctx, true); err == nil {
		t.Fatal("sealed predecessor acquired targets")
	}
	if _, err := c.Release(ctx); err == nil {
		t.Fatal("sealed predecessor reported a release of started handoff claims")
	}
	if _, err := c.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("predecessor accepted its own handoff")
	}
	for _, claim := range f.remote {
		if claim.State != "handoff" {
			t.Fatal("claim released instead of parked")
		}
	}
	next := nextHandoffCoordinator(t, c, f)
	status, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if next.runID == oldRun || !status.Cluster.CurrentRun || !status.Cluster.MatchesProof || status.Handoff.Phase != "accepted" {
		t.Fatalf("no fresh authority: %+v", status)
	}
	after, err := ReadSession(next.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	if before.ReviewHash != after.ReviewHash || !reflect.DeepEqual(before.Events, after.Events) {
		t.Fatal("handoff rewrote original proof history")
	}
	if _, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("consumed certificate replayed")
	}
	if _, err := next.RunHosts(ctx, ReadinessReport{ReadyToApply: true}); err == nil {
		t.Fatal("transferred session replayed host effects")
	}
	ready := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, ReadyToApply: true, State: "ready", InputHash: after.InputHash, ReleaseSHA256: after.ReleaseSHA256, PlanHash: after.PlanHash}
	engine := &platformFixtureEngine{}
	if err := next.RunPlatform(ctx, ready, staleReview.Hash, engine, nil, nil); err == nil || engine.effects != 0 {
		t.Fatal("predecessor review authorized successor effects")
	}
	review, err := next.RefreshPlatformReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review.Cluster.RunID != next.runID {
		t.Fatal("refreshed review retained predecessor Lease")
	}
	if err := next.RunPlatform(ctx, ready, review.Hash, engine, nil, nil); err != nil {
		t.Fatal(err)
	}
	if engine.effects != 2 {
		t.Fatal("successor did not run only platform effects")
	}
	// A normal reopen after acceptance does not recover the accepted run.
	reopened := nextHandoffCoordinator(t, next, f)
	if _, err := reopened.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("reopen bypassed consumed handoff")
	}
	if err := reopened.RunPlatform(ctx, ready, review.Hash, engine, nil, nil); err == nil {
		t.Fatal("reopen restored saved authority")
	}
}

func TestExecutorHandoffRejectsUnprovenQuiescenceAndChangedState(t *testing.T) {
	for _, kind := range []string{"legacy", "running", "uncertain", "platform-attempt", "worktree", "artifact", "storage", "node", "lease"} {
		t.Run(kind, func(t *testing.T) {
			c, f, client := handoffFixture(t)
			switch kind {
			case "legacy", "running":
				for id, claim := range f.remote {
					if kind == "legacy" {
						claim.ExecutorProtocol = ""
					} else {
						claim.ExecutorProtocol = "systemd-running"
					}
					f.remote[id] = claim
				}
			case "uncertain":
				c.executorUncertain.Store(true)
			case "platform-attempt":
				a, err := c.writer.Begin("scaffold")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.writer.Uncertain("scaffold", a); err != nil {
					t.Fatal(err)
				}
				// Even a generic positive inspection must not erase the attempt.
				if err := c.writer.Resolve("scaffold", a, true, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			case "worktree":
				if err := os.WriteFile(filepath.Join(c.compiled.Init.Repo, "unexpected"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "artifact":
				c.platformArtifacts = func(context.Context) error { return errors.New("changed binary") }
			case "storage":
				c.handoffTargets = func(context.Context) error { return errors.New("changed disk") }
			case "node":
				if err := client.CoreV1().Nodes().Delete(context.Background(), primaryServer(c.compiled.Spec.Hosts).ID, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "lease":
				if err := client.CoordinationV1().Leases(clusterClaimNamespace).Delete(context.Background(), clusterClaimName, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.PrepareExecutorHandoff(context.Background()); err == nil {
				t.Fatal("unproven handoff accepted")
			}
			r, _ := c.writer.Snapshot()
			if len(r.Handoffs) != 0 {
				t.Fatal("failed eligibility created transferable authority")
			}
		})
	}
}

func TestExecutorHandoffPendingTransferCanContinueWithoutPayloadAuthority(t *testing.T) {
	for _, failure := range []string{"park-host", "park-lease", "transfer-host", "claim-lease"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			c, f, client := handoffFixture(t)
			base := c.handoffHost
			if failure == "park-host" {
				c.handoffHost = func(ctx context.Context, h Host, claim HostClaim, action string) (HostClaim, error) {
					if action == "park" {
						return HostClaim{}, errors.New("lost source")
					}
					return base(ctx, h, claim, action)
				}
			}
			if failure == "park-lease" {
				once := true
				client.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
					if once {
						once = false
						return true, nil, errors.New("lost CAS")
					}
					return false, nil, nil
				})
			}
			h, err := c.PrepareExecutorHandoff(ctx)
			if (failure == "park-host" || failure == "park-lease") != (err != nil) {
				t.Fatalf("unexpected prepare outcome: %v", err)
			}
			r, _ := c.writer.Snapshot()
			if !c.sealed || r.Ownership.Phase != "handoff" {
				t.Fatal("partial preparation restored old authority")
			}
			next := nextHandoffCoordinator(t, c, f)
			if failure == "transfer-host" {
				base := next.handoffHost
				next.handoffHost = func(ctx context.Context, h Host, claim HostClaim, action string) (HostClaim, error) {
					got, err := base(ctx, h, claim, action)
					if action == "transfer" && err == nil {
						return got, errors.New("lost transfer response")
					}
					return got, err
				}
			}
			if failure == "claim-lease" {
				count := 0
				client.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
					count++
					if count == 1 {
						return true, nil, errors.New("lost CAS")
					}
					return false, nil, nil
				})
			}
			if failure == "transfer-host" || failure == "claim-lease" {
				if _, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
					t.Fatal("injected ambiguity ignored")
				}
				if next.sealed != true {
					t.Fatal("partial recipient obtained authority")
				}
				next = nextHandoffCoordinator(t, next, f)
			}
			if _, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutorHandoffActivationAmbiguityNeverPermitsReplay(t *testing.T) {
	ctx := context.Background()
	c, f, _ := handoffFixture(t)
	h, err := c.PrepareExecutorHandoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := nextHandoffCoordinator(t, c, f)
	base := next.handoffHost
	next.handoffHost = func(ctx context.Context, host Host, claim HostClaim, action string) (HostClaim, error) {
		got, err := base(ctx, host, claim, action)
		if action == "activate" && err == nil {
			return got, errors.New("lost active response")
		}
		return got, err
	}
	if _, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("activation ambiguity ignored")
	}
	if !next.sealed {
		t.Fatal("ambiguous activation enabled payloads")
	}
	again := nextHandoffCoordinator(t, next, f)
	if _, err := again.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("active controls replayed a consumed pending transfer")
	}
}

func TestExecutorHandoffRecipientAndIdentityChangesBlockAcceptance(t *testing.T) {
	for _, kind := range []string{"missing-key", "wrong-key", "key-symlink", "host-boot", "lease-uid", "worktree", "storage"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c, f, client := handoffFixture(t)
			h, err := c.PrepareExecutorHandoff(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next := nextHandoffCoordinator(t, c, f)
			path := filepath.Join(next.writer.directory, "handoff-"+h.CertificateSHA256+".key")
			switch kind {
			case "missing-key":
				err = os.Remove(path)
			case "wrong-key":
				err = os.WriteFile(path, []byte(strings.Repeat("0", 64)), 0600)
			case "key-symlink":
				if err = os.Rename(path, path+"-old"); err == nil {
					err = os.Symlink(path+"-old", path)
				}
			case "host-boot":
				for id, claim := range f.remote {
					claim.ObservedBootID = "22222222-2222-2222-2222-222222222222"
					f.remote[id] = claim
				}
			case "lease-uid":
				lease, e := client.CoordinationV1().Leases(clusterClaimNamespace).Get(ctx, clusterClaimName, metav1.GetOptions{})
				if e != nil {
					t.Fatal(e)
				}
				lease.UID = "replacement"
				err = client.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), lease, clusterClaimNamespace)
			case "worktree":
				err = os.WriteFile(filepath.Join(next.compiled.Init.Repo, "unreviewed"), []byte("changed"), 0600)
			case "storage":
				next.handoffTargets = func(context.Context) error { return errors.New("changed disk") }
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := next.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil || !next.sealed {
				t.Fatal("changed recipient or target enabled handoff")
			}
		})
	}
}

func TestExecutorHandoffDelayedLoserCannotStrandAcceptedRecipient(t *testing.T) {
	ctx := context.Background()
	source, remote, _ := handoffFixture(t)
	h, err := source.PrepareExecutorHandoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loser := nextHandoffCoordinator(t, source, remote)
	// Separate mutexes and fresh run IDs model two workstation processes. The
	// shared writer is a test seam; remote CAS/epochs must provide exclusion.
	run, err := randomHandoffIdentity()
	if err != nil {
		t.Fatal(err)
	}
	winner := &SessionCoordinator{compiled: loser.compiled, review: loser.review, writer: loser.writer, runID: run, sealed: true, realClaims: loser.realClaims, clusterClient: loser.clusterClient, platformSession: loser.platformSession, platformArtifacts: loser.platformArtifacts, handoffTargets: loser.handoffTargets}
	winnerClaims := &fixtureClaims{coordinator: winner, remote: remote.remote}
	winner.claims = winnerClaims
	winner.handoffHost = handoffFixtureControl(winnerClaims)
	base := loser.handoffHost
	interleaved := false
	delayedRejectedWhileParked := false
	loser.handoffHost = func(ctx context.Context, host Host, claim HostClaim, action string) (HostClaim, error) {
		if action == "transfer" && !interleaved {
			interleaved = true
			winnerBase := winner.handoffHost
			winner.handoffHost = func(ctx context.Context, h Host, want HostClaim, operation string) (HostClaim, error) {
				if operation == "activate" && !delayedRejectedWhileParked {
					if remote.remote[h.ID].State != "handoff" {
						t.Fatal("race window no longer has parked hosts")
					}
					if _, err := base(ctx, host, claim, "transfer"); err == nil {
						t.Fatal("stale contender moved winner's parked host after Lease activation")
					}
					delayedRejectedWhileParked = true
				}
				return winnerBase(ctx, h, want, operation)
			}
			if _, err := winner.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err != nil {
				t.Fatalf("winner acceptance: %v", err)
			}
		}
		return base(ctx, host, claim, action)
	}
	if _, err := loser.AcceptExecutorHandoff(ctx, h.CertificateSHA256); err == nil {
		t.Fatal("losing recipient acquired payload authority")
	}
	if !interleaved || !delayedRejectedWhileParked || !loser.sealed || winner.sealed {
		t.Fatal("recipient selection did not remain exclusive")
	}
	status, err := winner.Status(ctx)
	if err != nil || !status.Cluster.CurrentRun {
		t.Fatalf("winner stranded: %+v %v", status, err)
	}
}
