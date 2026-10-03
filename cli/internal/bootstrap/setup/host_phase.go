package setup

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

const maxWorkerJoins = 4

// HostPhaseOperations keeps join tokens and installation details inside the
// host engine. RecheckHost must compare the live machine, SSH key, selected
// network, RKE2 state, and any selected disk with the reviewed observation.
// It must fail before the corresponding install method writes to that host.
// VerifyFirstServer must check the RKE2 service and API without waiting for
// Node Ready before the CNI exists. VerifyJoinedServer must confirm membership
// in that API, and VerifyWorker must confirm agent registration.
type HostPhaseOperations interface {
	RecheckHost(context.Context, Host, HostObservation) error
	InstallFirstServer(context.Context, Host) error
	VerifyFirstServer(context.Context, Host) error
	JoinServer(context.Context, Host, Host) error
	VerifyJoinedServer(context.Context, Host) error
	JoinWorker(context.Context, Host, Host) error
	VerifyWorker(context.Context, Host) error
	FetchKubeconfig(context.Context, Host) error
}

// HostPhaseResult contains no join token or kubeconfig contents. A failed
// stage stays incomplete, even if its install method already changed a host.
type HostPhaseResult struct {
	State     string   `json:"state"`
	Completed []string `json:"completed"`
	Failed    string   `json:"failed,omitempty"`
}

// RunHostPhase executes the ordered RKE2 portion of a future guided run. It
// cannot be called from today's setup command: the current readiness reducer
// never grants ReadyToApply. The eventual coordinator must supply a qualified,
// exact, fresh readiness result and an engine with live pre-write rechecks.
func RunHostPhase(ctx context.Context, c Compiled, plan Preview, readiness ReadinessReport, inventory HostInventory, operations HostPhaseOperations) (HostPhaseResult, error) {
	result := HostPhaseResult{State: "blocked", Completed: []string{}}
	if err := checkCompiled(c); err != nil {
		return result, err
	}
	if operations == nil || c.Spec.Target.Intent != clusterinit.ModeInstall {
		return result, fmt.Errorf("host phase requires a fresh-install target and host operations")
	}
	if plan.InputHash != c.InputHash || plan.ReleaseSHA256 != c.ReleaseSHA256 || plan.PlanHash == "" ||
		readiness.InputHash != c.InputHash || readiness.ReleaseSHA256 != c.ReleaseSHA256 || readiness.PlanHash != plan.PlanHash ||
		inventory.InputHash != c.InputHash {
		return result, fmt.Errorf("host phase inputs changed; run setup checks again")
	}
	currentPlan, err := BuildPreview(c)
	if err != nil || plan.PlanHash != currentPlan.PlanHash {
		return result, fmt.Errorf("host phase plan changed; review the setup plan again")
	}
	if readiness.SchemaVersion != ReadinessSchemaVersion || !readiness.ReadyToApply || readiness.State != "ready" || readiness.BlockedCount != 0 || len(readiness.Unresolved) != 0 ||
		len(inventory.Unresolved) != 0 || len(checkHostInventoryBinding(c.Spec.Hosts, inventory, time.Now().UTC())) != 0 {
		return result, fmt.Errorf("host phase requires complete, fresh, qualified readiness")
	}
	kvmRequired := !c.Init.NoKubeVirt && !c.Init.AllowNoKubevirtEligible
	kvmPresent, kvmCapacityPassed := false, false
	for _, host := range inventory.Hosts {
		kvmPresent = kvmPresent || host.Facts.KVMDevice == "present"
	}
	for _, check := range inventory.Checks {
		if check.ID == "kvm-capacity" && check.Status == "pass" {
			kvmCapacityPassed = true
		}
	}
	allowedPending := 0
	for _, finding := range readiness.Findings {
		if finding.Status == "unknown" && qualifiedFreeSpaceFinding(finding.ID, inventory.Hosts) {
			allowedPending++
			continue
		}
		if finding.Status == "unknown" && kvmRequired && kvmPresent && kvmCapacityPassed {
			allowed := false
			for _, host := range inventory.Hosts {
				if finding.ID == "host."+host.ID+".kvm-device" && host.Facts.KVMDevice == "absent" {
					allowed = true
					break
				}
			}
			if allowed {
				allowedPending++
				continue
			}
		}
		if finding.Status != "pass" && finding.Status != "not-applicable" {
			return result, fmt.Errorf("host phase readiness has an unresolved %s finding", finding.ID)
		}
	}
	if readiness.PendingCount != allowedPending {
		return result, fmt.Errorf("host phase readiness has unresolved pending checks")
	}
	observed := make(map[string]HostObservation, len(inventory.Hosts))
	selected := make(map[string]Host, len(c.Spec.Hosts))
	for _, host := range c.Spec.Hosts {
		selected[host.ID] = host
	}
	for _, host := range inventory.Hosts {
		if host.Facts.MachineID == "" || host.Facts.HostKey == nil || host.Facts.HostKey.Algorithm == "" || host.Facts.HostKey.Address == "" {
			return result, fmt.Errorf("host %s has no verified identity", host.ID)
		}
		if host.Facts.RKE2Files != "absent" || host.Facts.RKE2ServerLoad != "not-found" || host.Facts.RKE2AgentLoad != "not-found" {
			return result, fmt.Errorf("host %s is not a fresh RKE2 target", host.ID)
		}
		required := map[string]bool{"machine-identity": true, "host-key": true, "operating-system": true, "architecture": true,
			"cpu": true, "memory": true, "privilege": true, "free-space": true, "clock": true, "tcp-ports": true,
			"network": true, "rke2-server": true, "rke2-agent": true, "rke2-files": true}
		if kvmRequired {
			required["kvm-device"] = true
		}
		if selected[host.ID].Disk != "" {
			if host.Facts.SelectedDisk == nil || host.Facts.SelectedDisk.SelectedPath != selected[host.ID].Disk {
				return result, fmt.Errorf("host %s selected disk differs from the reviewed disk", host.ID)
			}
			required["selected-disk"] = true
			if !isLoopSelectedDevice(selected[host.ID].Disk) {
				if host.Facts.SelectedDisk.StableID == "" {
					return result, fmt.Errorf("host %s raw disk has no stable identity", host.ID)
				}
				required["selected-disk-identity"] = true
			}
		}
		for _, check := range host.Checks {
			kvmAbsentOnThisHost := check.ID == "kvm-device" && kvmRequired && check.Status == "unknown" && host.Facts.KVMDevice == "absent"
			freeSpaceQualified := check.ID == "free-space" && check.Status == "unknown" && qualifiedFreeSpace(host)
			if check.Status != "pass" && check.Status != "not-applicable" && !kvmAbsentOnThisHost && !freeSpaceQualified {
				return result, fmt.Errorf("host %s has an unresolved %s check", host.ID, check.ID)
			}
			if check.ObservedAt.IsZero() || time.Since(check.ObservedAt) > maxHostInventoryAge || check.ObservedAt.After(time.Now().Add(time.Minute)) {
				return result, fmt.Errorf("host %s has stale %s evidence", host.ID, check.ID)
			}
			delete(required, check.ID)
		}
		if host.Facts.KVMDevice == "present" {
			kvmPresent = true
		}
		if len(required) != 0 {
			return result, fmt.Errorf("host %s is missing required readiness checks", host.ID)
		}
		observed[host.ID] = host
	}
	for _, check := range inventory.Checks {
		if check.Status != "pass" && check.Status != "not-applicable" {
			return result, fmt.Errorf("host inventory has an unresolved %s check", check.ID)
		}
	}
	if kvmRequired && (!kvmPresent || !kvmCapacityPassed) {
		return result, fmt.Errorf("host phase needs confirmed cluster KVM capacity")
	}
	primary, servers, workers := orderedHostPhase(c.Spec.Hosts)
	result.State = "running"
	fail := func(stage string, err error) (HostPhaseResult, error) {
		result.State, result.Failed = "action-required", stage
		if ctx.Err() != nil {
			result.State = "stopped"
		}
		return result, fmt.Errorf("%s: %w", stage, err)
	}
	if err := ctx.Err(); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := operations.RecheckHost(ctx, primary, observed[primary.ID]); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := operations.InstallFirstServer(ctx, primary); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := operations.VerifyFirstServer(ctx, primary); err != nil {
		return fail("rke2-first-server", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("rke2-first-server", err)
	}
	result.Completed = append(result.Completed, "rke2-first-server")
	for _, host := range servers {
		stage := "rke2-join-" + host.ID
		if err := ctx.Err(); err != nil {
			return fail(stage, err)
		}
		if err := operations.RecheckHost(ctx, host, observed[host.ID]); err != nil {
			return fail(stage, err)
		}
		if err := ctx.Err(); err != nil {
			return fail(stage, err)
		}
		if err := operations.JoinServer(ctx, primary, host); err != nil {
			return fail(stage, err)
		}
		if err := ctx.Err(); err != nil {
			return fail(stage, err)
		}
		if err := operations.VerifyJoinedServer(ctx, host); err != nil {
			return fail(stage, err)
		}
		if err := ctx.Err(); err != nil {
			return fail(stage, err)
		}
		result.Completed = append(result.Completed, stage)
	}
	if len(workers) != 0 {
		workerCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		jobs := make(chan Host, len(workers))
		for _, host := range workers {
			jobs <- host
		}
		close(jobs)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		failedStage := ""
		completedWorkers := make(map[string]bool, len(workers))
		count := maxWorkerJoins
		if len(workers) < count {
			count = len(workers)
		}
		for range count {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for host := range jobs {
					if workerCtx.Err() != nil {
						return
					}
					stage := "rke2-join-" + host.ID
					err := operations.RecheckHost(workerCtx, host, observed[host.ID])
					if err == nil {
						err = workerCtx.Err()
					}
					if err == nil {
						err = operations.JoinWorker(workerCtx, primary, host)
					}
					if err == nil {
						err = workerCtx.Err()
					}
					if err == nil {
						err = operations.VerifyWorker(workerCtx, host)
					}
					if err == nil {
						err = workerCtx.Err()
					}
					mu.Lock()
					if err != nil && firstErr == nil {
						firstErr, failedStage = err, stage
						cancel()
					} else if err == nil {
						completedWorkers[host.ID] = true
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		for _, host := range workers {
			if completedWorkers[host.ID] {
				result.Completed = append(result.Completed, "rke2-join-"+host.ID)
			}
		}
		if firstErr != nil {
			return fail(failedStage, firstErr)
		}
		if err := ctx.Err(); err != nil {
			return fail("rke2-join-workers", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail("fetch-kubeconfig", err)
	}
	if err := operations.FetchKubeconfig(ctx, primary); err != nil {
		return fail("fetch-kubeconfig", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("fetch-kubeconfig", err)
	}
	result.Completed = append(result.Completed, "fetch-kubeconfig")
	result.State = "complete"
	return result, nil
}

// The raw free-space probe deliberately reports unknown until the selected
// release profile checks its floor. Keep both observations when deciding if
// the host can enter the RKE2 phase.
func qualifiedFreeSpace(host HostObservation) bool {
	if host.Facts.FreeBytes == 0 {
		return false
	}
	for _, check := range host.Checks {
		if check.ID == "profile-free-space" && check.Status == "pass" {
			return true
		}
	}
	return false
}

func qualifiedFreeSpaceFinding(id string, hosts []HostObservation) bool {
	for _, host := range hosts {
		if id == "host."+host.ID+".free-space" && qualifiedFreeSpace(host) {
			return true
		}
	}
	return false
}

func orderedHostPhase(hosts []Host) (Host, []Host, []Host) {
	primary := primaryServer(hosts)
	var servers, workers []Host
	for _, host := range hosts {
		if host.ID == primary.ID {
			continue
		}
		if host.Role == "server" {
			servers = append(servers, host)
		} else {
			workers = append(workers, host)
		}
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	return primary, servers, workers
}
