package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// RunHosts is the shared, fenced host-phase entry. The public readiness reducer
// never authorizes this path yet; there is no CLI flag that bypasses that gate.
// It deliberately retains started host claims for the future cluster handoff.
func (c *SessionCoordinator) RunHosts(ctx context.Context, readiness ReadinessReport) (HostPhaseResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	blocked := HostPhaseResult{State: "blocked", Completed: []string{}}
	if c.closed || c.realClaims == nil || !readiness.ReadyToApply {
		return blocked, fmt.Errorf("host execution requires qualified readiness and real owned targets")
	}
	record, err := c.writer.Snapshot()
	if err != nil {
		return blocked, err
	}
	if record.Ownership == nil || record.Ownership.RunID != c.runID || record.Ownership.Phase != "held" || len(SummarizeSession(record).NeedsInspection) != 0 {
		return blocked, fmt.Errorf("host execution requires this run's ownership and reconciled stage evidence")
	}
	plan, err := BuildPreview(c.compiled)
	if err != nil {
		return blocked, err
	}
	publicPath := filepath.Join(c.writer.directory, "child-public.kubeconfig")
	bootstrapPath := filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig")
	handoff, err := NewExclusiveKubeconfigHandoff(c.compiled, publicPath)
	if err != nil {
		return blocked, err
	}
	var engine *RKE2HostOperations
	wrappedHandoff := func(cfg *clientcmdapi.Config) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		primary := primaryServer(c.compiled.Spec.Hosts)
		if _, err := c.claims.Check(ctx, primary); err != nil {
			return err
		}
		if err := handoff(cfg); err != nil {
			return err
		}
		node, err := engine.FirstServerEvidence()
		if err != nil {
			return err
		}
		return PublishVerifiedChildBootstrapKubeconfig(ctx, c.compiled, publicPath, bootstrapPath, node)
	}
	engine, err = NewReviewedRKE2HostOperations(ctx, c.compiled, c.review, c.realClaims.GuardedSSH(), wrappedHandoff, c.cliPath)
	if err != nil {
		return blocked, err
	}
	engine.claimCheck = func(ctx context.Context, h Host) error { _, err := c.claims.Check(ctx, h); return err }
	ops := &journalHostOperations{base: engine, writer: c.writer, claims: c.claims, pending: map[string]int{}, evidence: engine.verifiedNode, handoffEvidence: func() (verifiedHandoffEvidence, error) {
		node, err := engine.FirstServerEvidence()
		if err != nil {
			return verifiedHandoffEvidence{}, err
		}
		if _, err := VerifyDirectChildBootstrapAPI(ctx, c.compiled, bootstrapPath, node); err != nil {
			return verifiedHandoffEvidence{}, err
		}
		return verifiedHandoffEvidence{node, bootstrapPath}, nil
	}}
	result, runErr := RunHostPhase(ctx, c.compiled, plan, readiness, c.review.Hosts, ops)
	// Includes cancellation between mutation and verification, not only errors
	// inside an operation. Every unmatched Begin remains uncertain.
	journalErr := ops.stopPending()
	if runErr != nil {
		return result, joinCoordinatorErrors(runErr, journalErr)
	}
	if journalErr != nil {
		result.State = "action-required"
		return result, journalErr
	}
	return result, nil
}

type journalHostOperations struct {
	base            HostPhaseOperations
	writer          *SessionWriter
	claims          claimOperations
	mu              sync.Mutex
	pending         map[string]int
	evidence        func(Host) (rke2.VerifiedNode, error)
	handoffEvidence func() (verifiedHandoffEvidence, error)
}

func (o *journalHostOperations) RecheckHost(ctx context.Context, h Host, review HostObservation) error {
	if err := o.base.RecheckHost(ctx, h, review); err != nil {
		return err
	}
	if _, err := o.claims.Check(ctx, h); err != nil {
		return err
	}
	_, err := o.claims.Renew(ctx, h)
	return err
}
func (o *journalHostOperations) begin(ctx context.Context, stage string, h Host, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := o.claims.Check(ctx, h); err != nil {
		return err
	}
	attempt, err := o.writer.Begin(stage)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.pending[stage] = attempt
	o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}
func (o *journalHostOperations) verified(ctx context.Context, stage string, h Host, verify func() error) error {
	if err := verify(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := o.claims.Check(ctx, h); err != nil {
		return err
	}
	node, err := o.evidence(h)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	attempt, ok := o.pending[stage]
	if !ok {
		return fmt.Errorf("verification has no journaled attempt")
	}
	hash, err := o.writer.saveStageEvidence(stage, attempt, h, node, "")
	if err != nil {
		return err
	}
	if err := o.writer.completeVerified(stage, attempt, hash); err != nil {
		return err
	}
	delete(o.pending, stage)
	return nil
}
func (o *journalHostOperations) InstallFirstServer(ctx context.Context, h Host) error {
	return o.begin(ctx, "rke2-first-server", h, func() error { return o.base.InstallFirstServer(ctx, h) })
}
func (o *journalHostOperations) VerifyFirstServer(ctx context.Context, h Host) error {
	return o.verified(ctx, "rke2-first-server", h, func() error { return o.base.VerifyFirstServer(ctx, h) })
}
func (o *journalHostOperations) JoinServer(ctx context.Context, p, h Host) error {
	return o.begin(ctx, "rke2-join-"+h.ID, h, func() error { return o.base.JoinServer(ctx, p, h) })
}
func (o *journalHostOperations) VerifyJoinedServer(ctx context.Context, h Host) error {
	return o.verified(ctx, "rke2-join-"+h.ID, h, func() error { return o.base.VerifyJoinedServer(ctx, h) })
}
func (o *journalHostOperations) JoinWorker(ctx context.Context, p, h Host) error {
	return o.begin(ctx, "rke2-join-"+h.ID, h, func() error { return o.base.JoinWorker(ctx, p, h) })
}
func (o *journalHostOperations) VerifyWorker(ctx context.Context, h Host) error {
	return o.verified(ctx, "rke2-join-"+h.ID, h, func() error { return o.base.VerifyWorker(ctx, h) })
}
func (o *journalHostOperations) FetchKubeconfig(ctx context.Context, h Host) error {
	stage := "fetch-kubeconfig"
	if err := o.begin(ctx, stage, h, func() error { return o.base.FetchKubeconfig(ctx, h) }); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := o.claims.Check(ctx, h); err != nil {
		return err
	}
	evidence, err := o.handoffEvidence()
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	hash, err := o.writer.saveStageEvidence(stage, o.pending[stage], h, evidence.Node, evidence.Path)
	if err != nil {
		return err
	}
	if err := o.writer.completeVerified(stage, o.pending[stage], hash); err != nil {
		return err
	}
	delete(o.pending, stage)
	return nil
}
func (o *journalHostOperations) stopPending() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var first error
	for stage, attempt := range o.pending {
		if err := o.writer.Uncertain(stage, attempt); err != nil && first == nil {
			first = err
		}
		delete(o.pending, stage)
	}
	return first
}
