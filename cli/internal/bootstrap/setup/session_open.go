package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func loadSessionInputs(record SessionRecord) (Compiled, SafetyReview, error) {
	return loadSessionInputsMatching(record, matchSessionRecord)
}

func loadSessionInputsMatching(record SessionRecord, match func(SessionRecord, Compiled, SafetyReview) error) (Compiled, SafetyReview, error) {
	var c Compiled
	var review SafetyReview
	spec, err := Load(record.SpecFile)
	if err != nil {
		return c, review, err
	}
	c, err = Compile(spec)
	if err != nil {
		return c, review, err
	}
	review, err = LoadSafetyReview(record.ReviewFile)
	if err != nil {
		return c, review, err
	}
	if err := match(record, c, review); err != nil {
		return c, review, err
	}
	return c, review, nil
}
func matchSessionRecord(record SessionRecord, c Compiled, review SafetyReview) error {
	stages, hash, err := sessionStages(c)
	if err != nil {
		return err
	}
	return matchSessionPlan(record, c, review, stages, hash)
}

func matchSessionPlan(record SessionRecord, c Compiled, review SafetyReview, stages []SessionStage, hash string) error {
	targets, err := sessionTargets(c, review)
	if err != nil {
		return err
	}
	if record.InputHash != c.InputHash || record.ReleaseSHA256 != c.ReleaseSHA256 || record.ReviewHash != review.Hash || record.PlanHash != hash || !reflect.DeepEqual(stages, record.Stages) || !reflect.DeepEqual(targets, record.Targets) {
		return fmt.Errorf("session differs from compiled inputs or reviewed targets")
	}
	return nil
}

// Prior records without the additive reservation stage stay inspectable. Only
// this known historical plan is accepted, with every original binding intact.
// Writer/open/claim paths continue to require the exact current plan.
func matchSessionInspectionRecord(record SessionRecord, c Compiled, review SafetyReview) error {
	if err := matchSessionRecord(record, c, review); err == nil {
		return nil
	}
	preview, err := BuildPreview(c)
	if err != nil {
		return err
	}
	for _, removeClaim := range []bool{false, true} {
		prior := preview
		stages := []SessionStage{}
		filtered := []StagePreview{}
		for _, stage := range preview.Stages {
			if stage.ID == string(clusterinit.StepPlatformAccept) || (removeClaim && stage.ID == clusterClaimStage) {
				continue
			}
			if stage.ID == string(clusterinit.StepReconcile) {
				stage.PotentialWrite = false
			}
			filtered = append(filtered, stage)
			stages = append(stages, SessionStage{stage.ID, stage.Target, stage.PotentialWrite})
		}
		prior.Stages, prior.PlanHash = filtered, ""
		body, err := json.Marshal(prior)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(body)
		if err := matchSessionPlan(record, c, review, stages, hex.EncodeToString(hash[:])); err == nil {
			return nil
		}
	}
	return fmt.Errorf("session differs from current or known historical inspection plans")
}

func OpenSessionCoordinator(directory string, ssh ports.GuardedPutSSHClient, cliPath string) (*SessionCoordinator, error) {
	record, err := ReadSession(directory)
	if err != nil {
		return nil, err
	}
	c, review, err := loadSessionInputs(record)
	if err != nil {
		return nil, err
	}
	writer, err := OpenSession(directory)
	if err != nil {
		return nil, err
	}
	coordinator, err := NewSessionCoordinator(c, review, writer, ssh, cliPath)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	return coordinator, nil
}

// InspectSessionTargets reads both local and remote state without opening a
// local writer or reclaiming an old run's authority. Unreadable targets remain
// unknown; a failed SSH read is never proof that a claim is absent.
func InspectSessionTargets(ctx context.Context, directory string, ssh ports.CappedSSHHostKeyClient) (CoordinatorStatus, error) {
	result := CoordinatorStatus{}
	if ssh == nil {
		return result, fmt.Errorf("target inspection requires verified SSH")
	}
	record, err := ReadSession(directory)
	if err != nil {
		return result, err
	}
	c, review, err := loadSessionInputsMatching(record, matchSessionInspectionRecord)
	if err != nil {
		return result, err
	}
	result.Session = SummarizeSession(record)
	result.ExecutorUncertain = record.ExecutorUncertain
	if len(record.Handoffs) != 0 {
		h := record.Handoffs[len(record.Handoffs)-1]
		result.Handoff = &h
	}
	for _, host := range c.Spec.Hosts {
		target := TargetClaimStatus{HostID: host.ID, State: "unknown"}
		observed, ok := reviewedHost(review.Hosts, host.ID)
		if !ok {
			return result, fmt.Errorf("target has no reviewed observation")
		}
		expected := HostClaim{HostID: host.ID, MachineID: observed.Facts.MachineID, SessionID: record.ID, RunID: record.ID, InputHash: c.InputHash, ReviewHash: review.Hash, ReleaseSHA256: c.ReleaseSHA256}
		command, err := hostClaimCommand(expected, "inspect", false, "", hostClaimBase, 0)
		if err != nil {
			return result, err
		}
		endpoint := sshHost(host.SSHAlias)
		endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
		readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		body, key, err := ssh.RunCappedWithHostKey(readCtx, endpoint, command, 4096)
		cancel()
		if err == nil && key.FingerprintSHA256 == host.HostKeySHA256 {
			claim, err := parseHostClaim(body, host.ID)
			if err == nil && claim.MachineID == expected.MachineID {
				target.Claim = &claim
				target.State = claim.State
				target.SecondsRemaining = max(int64(0), claim.ExpiresTick-claim.ObservedTick)
				target.BootChanged = claim.BootID != claim.ObservedBootID
				if target.BootChanged {
					target.SecondsRemaining = 0
				}
			}
		}
		result.Targets = append(result.Targets, target)
	}
	result.Cluster = inspectSessionCluster(ctx, directory, record, c, "", verifiedChildClusterClient)
	return result, ctx.Err()
}
