package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

type fixtureClaims struct {
	coordinator              *SessionCoordinator
	remote                   map[string]HostClaim
	writes                   int
	failAcquire, failRelease bool
	beforeWrite              func()
}

func (f *fixtureClaims) Acquire(_ context.Context, h Host, takeover bool) (HostClaim, error) {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	f.writes++
	c := f.coordinator
	r, _ := c.writer.Snapshot()
	observed, _ := reviewedHost(c.review.Hosts, h.ID)
	claim := HostClaim{HostID: h.ID, MachineID: observed.Facts.MachineID, BootID: "11111111-1111-1111-1111-111111111111", ObservedBootID: "11111111-1111-1111-1111-111111111111", SessionID: r.ID, RunID: c.runID, InputHash: r.InputHash, ReviewHash: r.ReviewHash, ReleaseSHA256: r.ReleaseSHA256, State: "reserved", ExpiresTick: 1801, ObservedTick: 1}
	if old, ok := f.remote[h.ID]; ok && old.State != "released" && !(takeover && old.State == "reserved" && old.ExpiresTick <= old.ObservedTick) {
		return HostClaim{}, fmt.Errorf("claimed")
	}
	f.remote[h.ID] = claim
	if f.failAcquire {
		return HostClaim{}, errors.New("lost response")
	}
	return claim, nil
}
func (f *fixtureClaims) Inspect(_ context.Context, h Host) (HostClaim, error) {
	c, ok := f.remote[h.ID]
	if !ok {
		return c, errors.New("unreadable")
	}
	return c, nil
}
func (f *fixtureClaims) Check(ctx context.Context, h Host) (HostClaim, error) {
	c, err := f.Inspect(ctx, h)
	if err == nil && (c.RunID != f.coordinator.runID || c.ExpiresTick <= c.ObservedTick || c.State == "released") {
		err = errors.New("not owned")
	}
	return c, err
}
func (f *fixtureClaims) Renew(ctx context.Context, h Host) (HostClaim, error) { return f.Check(ctx, h) }
func (f *fixtureClaims) Release(ctx context.Context, h Host) (HostClaim, error) {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	f.writes++
	c, err := f.Inspect(ctx, h)
	if err != nil || c.State != "reserved" || c.RunID != f.coordinator.runID {
		return c, errors.New("not reserved")
	}
	c.State = "released"
	f.remote[h.ID] = c
	if f.failRelease {
		return HostClaim{}, errors.New("lost release response")
	}
	return c, nil
}
func coordinatorFixture(t *testing.T) (*SessionCoordinator, *fixtureClaims) {
	t.Helper()
	w, c, review := sessionFixture(t)
	coordinator := &SessionCoordinator{compiled: c, review: review, writer: w, runID: strings.Repeat("a", 64), recheck: func(context.Context) error { return nil }}
	claims := &fixtureClaims{coordinator: coordinator, remote: map[string]HostClaim{}}
	coordinator.claims = claims
	return coordinator, claims
}
func TestCoordinatorPersistsIntentBeforeClaimsAndRelease(t *testing.T) {
	c, f := coordinatorFixture(t)
	f.beforeWrite = func() {
		record, err := c.writer.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if record.Ownership.Phase != "acquiring" && record.Ownership.Phase != "releasing" {
			t.Fatalf("write without durable intent: %+v", record.Ownership)
		}
	}
	status, err := c.Acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReadyToApply || status.Session.OwnershipPhase != "held" || len(status.Targets) == 0 || !status.Targets[0].CurrentRun {
		t.Fatalf("%+v", status)
	}
	firstRun := c.runID
	status, err = c.Release(context.Background())
	if err != nil || status.Session.OwnershipPhase != "released" {
		t.Fatalf("%+v %v", status, err)
	}
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if c.runID == firstRun {
		t.Fatal("run identity reused")
	}
}
func TestCoordinatorStorageFailurePreventsRemoteClaims(t *testing.T) {
	c, f := coordinatorFixture(t)
	c.writer.syncDirectory = func(*os.Root) error { return errors.New("sync failure") }
	if _, err := c.Acquire(context.Background(), false); err == nil {
		t.Fatal("storage failure ignored")
	}
	if f.writes != 0 {
		t.Fatal("claimed after failed journal")
	}
}
func TestCoordinatorAmbiguousAcquireRetainsOwnership(t *testing.T) {
	c, f := coordinatorFixture(t)
	f.failAcquire = true
	if _, err := c.Acquire(context.Background(), false); err == nil {
		t.Fatal("ambiguous acquisition accepted")
	}
	record, _ := c.writer.Snapshot()
	if record.Ownership.Phase != "uncertain" || len(f.remote) == 0 {
		t.Fatal(record.Ownership)
	}
	if err := c.Close(); err == nil {
		t.Fatal("uncertain close implied cleanup")
	}
	for _, claim := range f.remote {
		if claim.State != "reserved" {
			t.Fatal("uncertain claim released")
		}
	}
}
func TestCoordinatorStartedClaimCannotReleaseOrTakeOver(t *testing.T) {
	c, f := coordinatorFixture(t)
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for id, g := range f.remote {
		g.State = "started"
		g.ExpiresTick = 0
		f.remote[id] = g
	}
	before := f.writes
	if _, err := c.Release(context.Background()); err == nil {
		t.Fatal("started released")
	}
	if f.writes != before {
		t.Fatal("release wrote before all-target inspection")
	}
	record, _ := c.writer.Snapshot()
	if record.Ownership.Phase != "uncertain" {
		t.Fatal(record.Ownership)
	}
	if _, err := c.Acquire(context.Background(), true); err == nil {
		t.Fatal("started takeover accepted")
	}
}
func TestCoordinatorReopenDoesNotRestoreRunAuthority(t *testing.T) {
	c, _ := coordinatorFixture(t)
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	directory := c.writer.directory
	if err := c.writer.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenSession(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	record, err := writer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record.Ownership.Phase != "uncertain" || SummarizeSession(record).State != "inspection-required" {
		t.Fatalf("%+v", record.Ownership)
	}
	if err := writer.Match(c.compiled, c.review); err != nil {
		t.Fatal(err)
	}
	if err := writer.ownership(strings.Repeat("b", 64), "releasing", nil, false); err == nil {
		t.Fatal("new run restored old cleanup authority")
	}
}
func TestCoordinatorRefusesBlockedReadinessWithoutEffects(t *testing.T) {
	c, f := coordinatorFixture(t)
	before := f.writes
	result, err := c.RunHosts(context.Background(), ReadinessReport{ReadyToApply: false})
	if err == nil || result.State != "blocked" || f.writes != before {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestJournalHostOperationsRequirePostconditionAndRecordCancellation(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			c, f := coordinatorFixture(t)
			if _, err := c.Acquire(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			engine := &fakeHostPhaseOperations{}
			ops := &journalHostOperations{base: engine, writer: c.writer, claims: f, pending: map[string]int{}, evidence: func(h Host) (rke2.VerifiedNode, error) {
				return rke2.VerifiedNode{Name: h.ID, UID: "node-uid", Role: h.Role, InternalIP: h.ManagementAddress}, nil
			}}
			h := primaryServer(c.compiled.Spec.Hosts)
			if err := ops.InstallFirstServer(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			record, _ := c.writer.Snapshot()
			event, _ := lastSessionEvent(record, "rke2-first-server")
			if event.State != "running" {
				t.Fatal(event)
			}
			if success {
				if err := ops.VerifyFirstServer(context.Background(), h); err != nil {
					t.Fatal(err)
				}
			}
			if err := ops.stopPending(); err != nil {
				t.Fatal(err)
			}
			record, _ = c.writer.Snapshot()
			event, _ = lastSessionEvent(record, "rke2-first-server")
			expected := "uncertain"
			if success {
				expected = "succeeded"
			}
			if event.State != expected {
				t.Fatal(event)
			}
		})
	}
}

func TestSessionOpenNormalizesDirectoryForProtectedChildPaths(t *testing.T) {
	c, _ := coordinatorFixture(t)
	directory := c.writer.directory
	if err := c.writer.Close(); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, directory)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := OpenSession(relative)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if writer.directory != directory || !filepath.IsAbs(writer.directory) {
		t.Fatal(writer.directory)
	}
}

func TestOwnershipJournalReservesRecoveryAtLimit(t *testing.T) {
	c, f := coordinatorFixture(t)
	for i := 0; i < maxOwnershipEvents/4; i++ {
		if _, err := c.Acquire(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	before := f.writes
	if _, err := c.Acquire(context.Background(), false); err == nil {
		t.Fatal("full ownership journal accepted another attempt")
	}
	if f.writes != before {
		t.Fatal("remote write after event cap")
	}
}
func TestCoordinatorLostReleaseResponseRemainsUncertain(t *testing.T) {
	c, f := coordinatorFixture(t)
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.failRelease = true
	if _, err := c.Release(context.Background()); err == nil {
		t.Fatal("lost release response accepted")
	}
	record, err := c.writer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record.Ownership.Phase != "uncertain" {
		t.Fatal(record.Ownership)
	}
	if _, err := c.Acquire(context.Background(), false); err == nil {
		t.Fatal("ambiguous release retried implicitly")
	}
}
func TestJournalCannotStartEffectWithoutMatchingStageOrStorage(t *testing.T) {
	for _, failure := range []string{"claim", "journal", "verify"} {
		t.Run(failure, func(t *testing.T) {
			c, f := coordinatorFixture(t)
			if _, err := c.Acquire(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			h := primaryServer(c.compiled.Spec.Hosts)
			engine := &fakeHostPhaseOperations{}
			ops := &journalHostOperations{base: engine, writer: c.writer, claims: f, pending: map[string]int{}, evidence: func(h Host) (rke2.VerifiedNode, error) {
				return rke2.VerifiedNode{Name: h.ID, UID: "uid", Role: h.Role, InternalIP: h.ManagementAddress}, nil
			}}
			switch failure {
			case "claim":
				g := f.remote[h.ID]
				g.RunID = strings.Repeat("b", 64)
				f.remote[h.ID] = g
			case "journal":
				c.writer.syncDirectory = func(*os.Root) error { return errors.New("storage") }
			case "verify":
				engine.fail = "verify-first:" + h.ID
			}
			err := ops.InstallFirstServer(context.Background(), h)
			if failure != "verify" {
				if err == nil || len(engine.events) != 0 {
					t.Fatal("effect ran after failed guard")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ops.VerifyFirstServer(context.Background(), h); err == nil {
				t.Fatal("failed postcondition accepted")
			}
			if err := ops.stopPending(); err != nil {
				t.Fatal(err)
			}
			r, _ := c.writer.Snapshot()
			last, _ := lastSessionEvent(r, "rke2-first-server")
			if last.State != "uncertain" {
				t.Fatal(last)
			}
		})
	}
}
