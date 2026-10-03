package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeHostPhaseOperations struct {
	mu              sync.Mutex
	events          []string
	fail            string
	activeWorkers   int
	maxWorkers      int
	workerJoinDelay time.Duration
	cancelOn        string
	cancel          context.CancelFunc
}

func (f *fakeHostPhaseOperations) step(id string) error {
	f.mu.Lock()
	f.events = append(f.events, id)
	f.mu.Unlock()
	if f.cancelOn == id && f.cancel != nil {
		f.cancel()
	}
	if f.fail == id {
		return errors.New("synthetic failure")
	}
	return nil
}

func (f *fakeHostPhaseOperations) RecheckHost(_ context.Context, host Host, observed HostObservation) error {
	if observed.ID != host.ID || observed.Facts.MachineID == "" || observed.Facts.HostKey == nil {
		return fmt.Errorf("wrong reviewed identity for %s", host.ID)
	}
	return f.step("recheck:" + host.ID)
}
func (f *fakeHostPhaseOperations) InstallFirstServer(_ context.Context, host Host) error {
	return f.step("first:" + host.ID)
}
func (f *fakeHostPhaseOperations) VerifyFirstServer(_ context.Context, host Host) error {
	return f.step("verify-first:" + host.ID)
}
func (f *fakeHostPhaseOperations) JoinServer(_ context.Context, primary, host Host) error {
	return f.step("server:" + primary.ID + "->" + host.ID)
}
func (f *fakeHostPhaseOperations) VerifyJoinedServer(_ context.Context, host Host) error {
	return f.step("verify-server:" + host.ID)
}
func (f *fakeHostPhaseOperations) JoinWorker(ctx context.Context, primary, host Host) error {
	f.mu.Lock()
	f.activeWorkers++
	if f.activeWorkers > f.maxWorkers {
		f.maxWorkers = f.activeWorkers
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.activeWorkers--; f.mu.Unlock() }()
	if err := f.step("worker:" + primary.ID + "->" + host.ID); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(f.workerJoinDelay):
		return nil
	}
}
func (f *fakeHostPhaseOperations) VerifyWorker(_ context.Context, host Host) error {
	return f.step("verify-worker:" + host.ID)
}
func (f *fakeHostPhaseOperations) FetchKubeconfig(_ context.Context, host Host) error {
	return f.step("fetch:" + host.ID)
}

func hostPhaseFixture(t *testing.T, workers int) (Compiled, Preview, ReadinessReport, HostInventory) {
	return hostPhaseFixtureWithDisk(t, workers, "")
}

func hostPhaseFixtureWithDisk(t *testing.T, workers int, disk string) (Compiled, Preview, ReadinessReport, HostInventory) {
	t.Helper()
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].Primary = true
	s.Hosts[0].HostKeySHA256 = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if disk != "" {
		s.Hosts[0].Disk = disk
		if err := os.WriteFile(s.Platform.ConfigFile, []byte(baseConfig("demo")+"OBJECT_STORAGE_MODE=rook-ceph-local\nCEPH_LOCAL_OSD_NODE=server-1\nCEPH_LOCAL_OSD_DEVICE="+strings.TrimPrefix(disk, "/dev/")+"\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Hosts = append(s.Hosts, Host{ID: "server-2", Role: "server", SSHAlias: "admin@server-2", ManagementAddress: "192.0.2.11", HostKeySHA256: s.Hosts[0].HostKeySHA256})
	for i := range workers {
		s.Hosts = append(s.Hosts, Host{ID: fmt.Sprintf("worker-%d", i), Role: "agent", SSHAlias: fmt.Sprintf("admin@worker-%d", i),
			ManagementAddress: fmt.Sprintf("192.0.2.%d", i+20), HostKeySHA256: s.Hosts[0].HostKeySHA256})
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	readiness := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, State: "ready", ReadyToApply: true,
		InputHash: c.InputHash, PlanHash: plan.PlanHash, ReleaseSHA256: c.ReleaseSHA256}
	inventory := HostInventory{SchemaVersion: HostInventorySchemaVersion, State: "observed", InputHash: c.InputHash,
		Checks: []HostCheck{{ID: "kvm-capacity", Status: "pass", ObservedAt: time.Now().UTC()}}}
	now := time.Now().UTC()
	checkIDs := []string{"machine-identity", "host-key", "operating-system", "architecture", "cpu", "memory", "privilege",
		"free-space", "clock", "kvm-device", "tcp-ports", "network", "rke2-server", "rke2-agent", "rke2-files"}
	for i, host := range s.Hosts {
		checks := make([]HostCheck, 0, len(checkIDs))
		for _, id := range checkIDs {
			status := "pass"
			if id == "free-space" {
				status = "unknown"
				readiness.PendingCount++
				readiness.Findings = append(readiness.Findings, ReadinessFinding{ID: "host." + host.ID + ".free-space", Status: status})
			}
			checks = append(checks, HostCheck{ID: id, Status: status, ObservedAt: now})
		}
		checks = append(checks, HostCheck{ID: "profile-free-space", Status: "pass", ObservedAt: now})
		facts := HostFacts{MachineID: fmt.Sprintf("%032x", i+1), RKE2Files: "absent", RKE2ServerLoad: "not-found", RKE2AgentLoad: "not-found", KVMDevice: "present", FreeBytes: 100 << 30,
			HostKey: &ports.SSHHostKeyEvidence{Address: host.SSHAlias, Algorithm: "ssh-ed25519", FingerprintSHA256: host.HostKeySHA256}}
		if host.Disk != "" {
			facts.SelectedDisk = &DiskFact{SelectedPath: host.Disk}
			checks = append(checks, HostCheck{ID: "selected-disk", Status: "not-applicable", ObservedAt: now})
		}
		inventory.Hosts = append(inventory.Hosts, HostObservation{ID: host.ID, Role: host.Role, SSHAlias: host.SSHAlias, State: "observed",
			ObservedAt: now, Checks: checks, Facts: facts})
	}
	return c, plan, readiness, inventory
}

func TestRunHostPhaseOrdersServersBeforeBoundedWorkers(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 6)
	ops := &fakeHostPhaseOperations{workerJoinDelay: 15 * time.Millisecond}
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, ops)
	if err != nil || result.State != "complete" || len(result.Completed) != 9 {
		t.Fatalf("host phase did not complete: %+v, %v", result, err)
	}
	wantCompleted := []string{"rke2-first-server", "rke2-join-server-2", "rke2-join-worker-0", "rke2-join-worker-1", "rke2-join-worker-2", "rke2-join-worker-3", "rke2-join-worker-4", "rke2-join-worker-5", "fetch-kubeconfig"}
	if !reflect.DeepEqual(result.Completed, wantCompleted) {
		t.Fatalf("completion order differs from reviewed plan: %v", result.Completed)
	}
	if ops.maxWorkers < 2 || ops.maxWorkers > maxWorkerJoins {
		t.Fatalf("worker concurrency = %d", ops.maxWorkers)
	}
	events := ops.events
	index := map[string]int{}
	for i, event := range events {
		index[event] = i
	}
	ordered := []string{"recheck:server-1", "first:server-1", "verify-first:server-1", "recheck:server-2", "server:server-1->server-2", "verify-server:server-2"}
	for i := 1; i < len(ordered); i++ {
		if index[ordered[i-1]] >= index[ordered[i]] {
			t.Fatalf("server operation order is wrong: %v", events)
		}
	}
	for i := range 6 {
		id := fmt.Sprintf("worker-%d", i)
		if index["verify-server:server-2"] >= index["recheck:"+id] || index["recheck:"+id] >= index["worker:server-1->"+id] || index["worker:server-1->"+id] >= index["verify-worker:"+id] || index["verify-worker:"+id] >= index["fetch:server-1"] {
			t.Fatalf("worker %s was not checked, joined, and verified in order: %v", id, events)
		}
	}
}

func TestRunHostPhaseRejectsOpenOrStaleReadinessBeforeWrites(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 1)
	for _, edit := range []struct {
		name string
		fn   func(*ReadinessReport, *HostInventory)
	}{
		{"not-approved", func(r *ReadinessReport, _ *HostInventory) { r.ReadyToApply = false }},
		{"open-check", func(r *ReadinessReport, _ *HostInventory) { r.Unresolved = []string{"live-target-identity"} }},
		{"stale-host", func(_ *ReadinessReport, h *HostInventory) {
			h.Hosts[0].ObservedAt = time.Now().Add(-maxHostInventoryAge - time.Minute)
		}},
		{"missing-key", func(_ *ReadinessReport, h *HostInventory) { h.Hosts[0].Facts.HostKey = nil }},
		{"missing-profile-floor", func(_ *ReadinessReport, h *HostInventory) {
			h.Hosts[0].Checks[len(h.Hosts[0].Checks)-1].Status = "blocked"
		}},
		{"changed-plan", func(r *ReadinessReport, _ *HostInventory) { r.PlanHash = strings.Repeat("f", 64) }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			r := readiness
			h := inventory
			h.Hosts = append([]HostObservation(nil), inventory.Hosts...)
			edit.fn(&r, &h)
			ops := &fakeHostPhaseOperations{}
			result, err := RunHostPhase(context.Background(), c, plan, r, h, ops)
			if err == nil || result.State != "blocked" || len(ops.events) != 0 {
				t.Fatalf("invalid preflight reached host operations: %+v, %v, %v", result, err, ops.events)
			}
		})
	}
}

func TestRunHostPhaseStopsBeforeMutationOnRecheckFailure(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 1)
	ops := &fakeHostPhaseOperations{fail: "recheck:server-2"}
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, ops)
	if err == nil || result.State != "action-required" || result.Failed != "rke2-join-server-2" || len(result.Completed) != 1 {
		t.Fatalf("recheck failure was not actionable: %+v, %v", result, err)
	}
	for _, event := range ops.events {
		if strings.HasPrefix(event, "server:server-1->server-2") || strings.HasPrefix(event, "worker:") || strings.HasPrefix(event, "fetch:") {
			t.Fatalf("mutation continued after recheck failure: %v", ops.events)
		}
	}
}

func TestRunHostPhaseWorkerFailureNeverFetchesKubeconfig(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 5)
	ops := &fakeHostPhaseOperations{fail: "worker:server-1->worker-0", workerJoinDelay: 20 * time.Millisecond}
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, ops)
	if err == nil || result.State != "action-required" || result.Failed != "rke2-join-worker-0" || len(result.Completed) == 0 {
		t.Fatalf("worker failure claimed success: %+v, %v", result, err)
	}
	for _, event := range ops.events {
		if strings.HasPrefix(event, "fetch:") {
			t.Fatalf("fetched kubeconfig after worker failure: %v", ops.events)
		}
	}
}

func TestRunHostPhaseCancellationAfterRecheckStopsBeforeInstall(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &fakeHostPhaseOperations{cancelOn: "recheck:server-1", cancel: cancel}
	result, err := RunHostPhase(ctx, c, plan, readiness, inventory, ops)
	if !errors.Is(err, context.Canceled) || result.State != "stopped" || result.Failed != "rke2-first-server" || len(result.Completed) != 0 {
		t.Fatalf("canceled recheck was not stopped: %+v, %v", result, err)
	}
	if len(ops.events) != 1 || ops.events[0] != "recheck:server-1" {
		t.Fatalf("host mutation followed canceled recheck: %v", ops.events)
	}
}

func TestRunHostPhaseCancellationAfterFetchDoesNotComplete(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &fakeHostPhaseOperations{cancelOn: "fetch:server-1", cancel: cancel}
	result, err := RunHostPhase(ctx, c, plan, readiness, inventory, ops)
	if !errors.Is(err, context.Canceled) || result.State != "stopped" || result.Failed != "fetch-kubeconfig" {
		t.Fatalf("canceled fetch claimed completion: %+v, %v", result, err)
	}
	for _, stage := range result.Completed {
		if stage == "fetch-kubeconfig" {
			t.Fatalf("canceled fetch was marked complete: %+v", result)
		}
	}
}

func TestRunHostPhaseAcceptsLoopBackedDiskWithoutRawIdentity(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixtureWithDisk(t, 0, "/dev/loop0")
	ops := &fakeHostPhaseOperations{}
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, ops)
	if err != nil || result.State != "complete" {
		t.Fatalf("loop-backed disk required a raw disk identity: %+v, %v", result, err)
	}
}

func TestRunHostPhaseAcceptsNonKVMHostWhenClusterHasKVM(t *testing.T) {
	c, plan, readiness, inventory := hostPhaseFixture(t, 1)
	inventory.Hosts[0].Facts.KVMDevice = "absent"
	for i := range inventory.Hosts[0].Checks {
		if inventory.Hosts[0].Checks[i].ID == "kvm-device" {
			inventory.Hosts[0].Checks[i].Status = "unknown"
		}
	}
	readiness.PendingCount++
	readiness.Findings = append(readiness.Findings, ReadinessFinding{ID: "host.server-1.kvm-device", Status: "unknown"})
	ops := &fakeHostPhaseOperations{}
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, ops)
	if err != nil || result.State != "complete" {
		t.Fatalf("non-KVM host blocked a KVM-capable cluster: %+v, %v", result, err)
	}
}
