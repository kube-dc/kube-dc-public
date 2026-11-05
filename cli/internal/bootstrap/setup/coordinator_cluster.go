package setup

import (
	"context"
	"fmt"
	"os"
)

func verifiedSessionHandoff(root *os.Root, record SessionRecord) (sessionStageEvidence, error) {
	event, ok := lastSessionEvent(record, "fetch-kubeconfig")
	if !ok || event.State != "succeeded" || event.Source != "coordinator" {
		return sessionStageEvidence{}, fmt.Errorf("cluster reservation requires durable verified child handoff evidence")
	}
	return loadStoredStageProof(root, record, event)
}

// reserveChildCluster runs only under the coordinator mutex, after the host
// sequencer has persisted the verified kubeconfig stage. It keeps every host
// claim and never promotes readiness. Any failed attempt remains uncertain.
func (c *SessionCoordinator) reserveChildCluster(ctx context.Context) (runErr error) {
	record, err := c.writer.Snapshot()
	if err != nil {
		return err
	}
	if record.Ownership == nil || record.Ownership.Phase != "held" || record.Ownership.RunID != c.runID || len(SummarizeSession(record).NeedsInspection) != 0 {
		return fmt.Errorf("cluster reservation requires this run's complete host ownership and reconciled stages")
	}
	handoff, err := verifiedSessionHandoff(c.writer.root, record)
	if err != nil {
		return err
	}
	// All nodes have admitted host effects. Check every retained owner before
	// contacting the child API; a primary-only check can hide a lost join claim.
	if err := checkStartedHostClaims(ctx, c.compiled.Spec.Hosts, record, c.runID, c.claims); err != nil {
		return err
	}
	client, err := c.childClient()(ctx, c.compiled, handoff.ChildPath, handoff.Node)
	if err != nil {
		return fmt.Errorf("cluster reservation could not verify the child API")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	attempt, err := c.writer.Begin(clusterClaimStage)
	if err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			runErr = joinCoordinatorErrors(runErr, c.writer.Uncertain(clusterClaimStage, attempt))
		}
	}()
	claim, err := acquireClusterClaim(ctx, client.CoordinationV1().Leases(clusterClaimNamespace), expectedClusterClaim(record, c.runID, handoff.Node))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hash, err := c.writer.saveVerifiedEvidence(clusterClaimStage, attempt, primaryServer(c.compiled.Spec.Hosts), handoff.Node, handoff.ChildPath, &claim)
	if err != nil {
		return err
	}
	return c.writer.completeVerified(clusterClaimStage, attempt, hash)
}

func checkStartedHostClaims(ctx context.Context, hosts []Host, record SessionRecord, run string, claims claimOperations) error {
	for _, host := range hosts {
		target := SessionTarget{}
		for _, t := range record.Targets {
			if t.ID == host.ID {
				target = t
			}
		}
		claim, err := claims.Check(ctx, host)
		if err != nil || target.ID == "" || claim.HostID != target.ID || claim.MachineID != target.MachineID || claim.State != "started" || claim.RunID != run || claim.SessionID != record.ID ||
			claim.InputHash != record.InputHash || claim.ReviewHash != record.ReviewHash || claim.ReleaseSHA256 != record.ReleaseSHA256 || claim.BootID != claim.ObservedBootID || claim.ExpiresTick <= claim.ObservedTick {
			return fmt.Errorf("host %s ownership is not confirmed; retain claims and inspect", host.ID)
		}
	}
	return ctx.Err()
}

func (c *SessionCoordinator) childClient() childClusterClientFactory {
	if c.clusterClient != nil {
		return c.clusterClient
	}
	return verifiedChildClusterClient
}

// inspectSessionCluster uses only a hash-checked handoff and explicit direct
// child access. It can observe an interrupted Create without restoring authority.
func inspectSessionCluster(ctx context.Context, directory string, record SessionRecord, compiled Compiled, run string, factory childClusterClientFactory) ClusterClaimStatus {
	result := ClusterClaimStatus{State: "not-recorded", Detail: "No verified child handoff is recorded."}
	event, exists := lastSessionEvent(record, "fetch-kubeconfig")
	if !exists || event.State != "succeeded" || event.Source != "coordinator" {
		return result
	}
	result.State, result.Detail = "unknown", "The protected child handoff or API could not be verified. Retain claims and inspect."
	root, err := privateSessionRoot(directory)
	if err != nil {
		return result
	}
	defer root.Close()
	handoff, err := verifiedSessionHandoff(root, record)
	if err != nil {
		return result
	}
	var proof *ClusterClaim
	if event, ok := lastSessionEvent(record, clusterClaimStage); ok && event.State == "succeeded" && event.Source == "coordinator" {
		stored, err := loadStoredStageProof(root, record, event)
		if err != nil {
			return result
		}
		proof = stored.Cluster
	}
	for _, h := range record.Handoffs {
		if h.Phase == "accepted" {
			p := h.Cluster
			proof = &p
		}
	}
	client, err := factory(ctx, compiled, handoff.ChildPath, handoff.Node)
	if err != nil {
		return result
	}
	expectedRun := ""
	if record.Ownership != nil {
		expectedRun = record.Ownership.RunID
	}
	expected := expectedClusterClaim(record, expectedRun, handoff.Node)
	return inspectClusterClaim(ctx, client.CoordinationV1().Leases(clusterClaimNamespace), expected, proof, run)
}
