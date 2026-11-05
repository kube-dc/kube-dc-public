package setup

import (
	"context"
	"fmt"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

type PlatformRecoveryFinding struct {
	Stage   string `json:"stage"`
	Attempt int    `json:"attempt"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}
type PlatformRecoveryReport struct {
	SessionID         string                    `json:"sessionId"`
	Findings          []PlatformRecoveryFinding `json:"findings"`
	MutationAuthority bool                      `json:"mutationAuthority"`
}

// ReconcilePlatform performs read-only postcondition checks and optionally
// retains positive evidence. It never retries an effect, resets Git, releases
// claims, or restores a previous executor's run identity.
func (c *SessionCoordinator) ReconcilePlatform(ctx context.Context, engine PlatformEngine, recordEvidence bool) (PlatformRecoveryReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := PlatformRecoveryReport{Findings: []PlatformRecoveryFinding{}}
	if c.closed || engine == nil {
		return result, fmt.Errorf("platform inspection requires an open coordinator and live driver")
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		return result, err
	}
	result.SessionID = r.ID
	execution, err := c.platformExecution(ctx, r)
	if err != nil {
		return result, err
	}
	defer execution.Session.Close()
	status := inspectSessionCluster(ctx, c.writer.directory, r, c.compiled, "", c.childClient())
	if status.State != "held" || !status.MatchesProof {
		return result, fmt.Errorf("recovery requires the same verified cluster reservation")
	}
	for _, event := range SummarizeSession(r).NeedsInspection {
		id := clusterinit.StepID(event.Stage)
		if len(requiredPlatformChecks(id)) == 0 {
			result.Findings = append(result.Findings, PlatformRecoveryFinding{Stage: event.Stage, Attempt: event.Attempt, State: "unresolved", Detail: "This stage needs a host-specific recovery check. Retain claims; no retry is authorized."})
			continue
		}
		finding := PlatformRecoveryFinding{Stage: event.Stage, Attempt: event.Attempt, State: "unresolved", Detail: "Live state is incomplete, changed, or unavailable. No retry is authorized."}
		// A running stage may still have an executor. OpenSession converts
		// abandoned attempts to uncertain; same-process calls must have returned.
		if event.State != "uncertain" {
			result.Findings = append(result.Findings, finding)
			continue
		}
		var review PlatformReview
		ref := ""
		for _, hash := range r.PlatformReviews {
			var candidate PlatformReview
			if err := readPlatformArtifact(c.writer.root, hash, &candidate); err != nil {
				return result, err
			}
			if !candidate.CreatedAt.After(event.At) {
				review, ref = candidate, hash
			}
		}
		if ref == "" {
			result.Findings = append(result.Findings, finding)
			continue
		}
		before := review.Snapshot
		prior := review.PriorReceipt
		if previous, ok := latestPlatformReceipt(r); ok {
			p, err := loadPlatformReceipt(c.writer.root, r, previous)
			if err != nil {
				return result, err
			}
			before, prior = p.Snapshot, previous.EvidenceSHA256
		}
		after, err := SnapshotPlatform(ctx, c.compiled.Init.Repo)
		if err != nil || checkPlatformDelta(ctx, before, after, id, c.review.Prepared) != nil {
			result.Findings = append(result.Findings, finding)
			continue
		}
		observation, err := engine.Observe(ctx, execution, id)
		if err != nil || checkPlatformObservation(id, observation) != nil {
			result.Findings = append(result.Findings, finding)
			continue
		}
		finding.State, finding.Detail = "verified", "Live postconditions pass. This observation does not restore a previous run's mutation authority."
		if recordEvidence {
			proof := PlatformReceipt{Binding: platformBinding(r), Stage: event.Stage, Attempt: event.Attempt, ReviewSHA256: ref, PriorReceipt: prior, ObservedAt: time.Now().UTC(), Snapshot: after, Observation: observation}
			hash, err := c.writer.savePlatformArtifact(proof)
			if err != nil {
				return result, err
			}
			if err := c.writer.completePlatform(event.Stage, event.Attempt, hash, true); err != nil {
				return result, err
			}
			finding.State = "recorded"
			r, err = c.writer.Snapshot()
			if err != nil {
				return result, err
			}
		}
		result.Findings = append(result.Findings, finding)
	}
	return result, ctx.Err()
}

// CloseEvidence closes a read/reconcile writer without releasing remote claims.
func (c *SessionCoordinator) CloseEvidence() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.writer.Close()
}
