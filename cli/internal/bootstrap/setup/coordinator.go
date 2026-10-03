package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type claimOperations interface {
	Acquire(context.Context, Host, bool) (HostClaim, error)
	Inspect(context.Context, Host) (HostClaim, error)
	Check(context.Context, Host) (HostClaim, error)
	Renew(context.Context, Host) (HostClaim, error)
	Release(context.Context, Host) (HostClaim, error)
}

type TargetClaimStatus struct {
	HostID           string     `json:"hostId"`
	State            string     `json:"state"`
	CurrentRun       bool       `json:"currentRun"`
	BootChanged      bool       `json:"bootChanged"`
	SecondsRemaining int64      `json:"secondsRemaining"`
	Claim            *HostClaim `json:"claim,omitempty"`
}
type CoordinatorStatus struct {
	Session      SessionSummary      `json:"session"`
	Targets      []TargetClaimStatus `json:"targets"`
	ReadyToApply bool                `json:"readyToApply"`
}

// SessionCoordinator shares the session and ownership lifecycle between CLI
// and the existing New Cluster panel. It cannot promote a saved review to
// readiness. Constructors and Status do not acquire any remote target.
type SessionCoordinator struct {
	mu             sync.Mutex
	compiled       Compiled
	review         SafetyReview
	writer         *SessionWriter
	claims         claimOperations
	realClaims     *HostClaims
	runID, cliPath string
	recheck        func(context.Context) error
	closed         bool
}

func NewSessionCoordinator(c Compiled, review SafetyReview, writer *SessionWriter, ssh ports.GuardedPutSSHClient, cliPath string) (*SessionCoordinator, error) {
	if writer == nil || ssh == nil {
		return nil, fmt.Errorf("session coordinator requires a writer and guarded SSH")
	}
	if err := writer.Match(c, review); err != nil {
		return nil, err
	}
	record, err := writer.Snapshot()
	if err != nil {
		return nil, err
	}
	// Inspection remains available after the five-minute review expires.
	// Acquisition and execution separately require fresh full safety checks.
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	if review.Hash != safetyReviewHash(review) || review.InputHash != c.InputHash || review.ReleaseSHA256 != c.ReleaseSHA256 || review.ReadyToApply {
		return nil, fmt.Errorf("session review binding changed")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	run := hex.EncodeToString(nonce[:])
	claims := &HostClaims{compiled: c, review: review, sessionID: record.ID, runID: run, ssh: ssh}
	for _, h := range c.Spec.Hosts {
		if _, _, err := claims.expected(h); err != nil {
			return nil, err
		}
	}
	return &SessionCoordinator{compiled: c, review: review, writer: writer, claims: claims, realClaims: claims, runID: run, cliPath: cliPath, recheck: func(ctx context.Context) error { return RecheckSafetyReview(ctx, c, review, ssh, cliPath) }}, nil
}

// Inputs returns a copy for presentation. Editing it does not change the
// coordinator's sealed specification or authorize the legacy Apply pipeline.
func (c *SessionCoordinator) Inputs() clusterinit.InitOptions {
	body, _ := json.Marshal(c.compiled.Init)
	var options clusterinit.InitOptions
	_ = json.Unmarshal(body, &options)
	return options
}
func (c *SessionCoordinator) Status(ctx context.Context) (CoordinatorStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status(ctx)
}
func (c *SessionCoordinator) status(ctx context.Context) (CoordinatorStatus, error) {
	var result CoordinatorStatus
	if c.closed {
		return result, fmt.Errorf("session coordinator is closed")
	}
	record, err := c.writer.Snapshot()
	if err != nil {
		return result, err
	}
	result.Session = SummarizeSession(record)
	for _, h := range c.compiled.Spec.Hosts {
		status := TargetClaimStatus{HostID: h.ID, State: "unknown"}
		claim, err := c.claims.Inspect(ctx, h)
		if err == nil {
			status.State = claim.State
			status.BootChanged = claim.BootID != claim.ObservedBootID
			status.CurrentRun = !status.BootChanged && claim.RunID == c.runID && claim.SessionID == record.ID
			status.SecondsRemaining = max(int64(0), claim.ExpiresTick-claim.ObservedTick)
			if status.BootChanged {
				status.SecondsRemaining = 0
			}
			status.Claim = &claim
		}
		result.Targets = append(result.Targets, status)
	}
	return result, ctx.Err()
}
func (c *SessionCoordinator) Acquire(ctx context.Context, takeover bool) (CoordinatorStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return CoordinatorStatus{}, fmt.Errorf("session coordinator is closed")
	}
	record, err := c.writer.Snapshot()
	if err != nil {
		return CoordinatorStatus{}, err
	}
	if len(SummarizeSession(record).NeedsInspection) != 0 {
		return CoordinatorStatus{}, fmt.Errorf("inspect uncertain installation stages before claiming targets")
	}
	if err := c.recheck(ctx); err != nil {
		return CoordinatorStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return CoordinatorStatus{}, err
	}
	if record.Ownership != nil && record.Ownership.Phase != "released" && !(record.Ownership.Phase == "uncertain" && takeover) {
		return CoordinatorStatus{}, fmt.Errorf("inspect or release the current ownership before claiming targets")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return CoordinatorStatus{}, err
	}
	c.runID = hex.EncodeToString(nonce[:])
	if c.realClaims != nil {
		next := *c.realClaims
		next.runID = c.runID
		c.realClaims = &next
		c.claims = c.realClaims
	}
	// A fresh invocation identity is never recovered from the local record.
	if err := c.writer.ownership(c.runID, "acquiring", nil, takeover); err != nil {
		return CoordinatorStatus{}, err
	}
	grants := []HostClaim{}
	for _, h := range c.compiled.Spec.Hosts {
		grant, err := c.claims.Acquire(ctx, h, takeover)
		if err != nil {
			saveErr := c.writer.ownership(c.runID, "uncertain", grants, false)
			return CoordinatorStatus{}, fmt.Errorf("claim acquisition stopped; inspect all targets before retry: %w", joinCoordinatorErrors(err, saveErr))
		}
		grants = append(grants, grant)
	}
	if err := c.writer.ownership(c.runID, "held", grants, false); err != nil {
		return CoordinatorStatus{}, err
	}
	return c.status(ctx)
}
func joinCoordinatorErrors(operation, save error) error {
	if save != nil {
		return fmt.Errorf("%v; ownership evidence could not be saved: %w", operation, save)
	}
	return operation
}
func (c *SessionCoordinator) Release(ctx context.Context) (CoordinatorStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.release(ctx); err != nil {
		return CoordinatorStatus{}, err
	}
	return c.status(ctx)
}
func (c *SessionCoordinator) release(ctx context.Context) error {
	if c.closed {
		return fmt.Errorf("session coordinator is closed")
	}
	record, err := c.writer.Snapshot()
	if err != nil {
		return err
	}
	if record.Ownership == nil || record.Ownership.Phase == "released" {
		return nil
	}
	if record.Ownership.RunID != c.runID || record.Ownership.Phase != "held" {
		return fmt.Errorf("ownership is uncertain or belongs to a previous run; retain claims and inspect targets")
	}
	// Inspect every host before releasing any. Expiry is not absence evidence.
	grants := []HostClaim{}
	for _, h := range c.compiled.Spec.Hosts {
		g, err := c.claims.Inspect(ctx, h)
		if err != nil || g.State != "reserved" || g.RunID != c.runID || g.SessionID != record.ID || g.InputHash != record.InputHash || g.ReviewHash != record.ReviewHash || g.ReleaseSHA256 != record.ReleaseSHA256 {
			saveErr := c.writer.ownership(c.runID, "uncertain", record.Ownership.Claims, false)
			return joinCoordinatorErrors(fmt.Errorf("host %s may have effects; retain claims and inspect", h.ID), saveErr)
		}
		grants = append(grants, g)
	}
	if err := c.writer.ownership(c.runID, "releasing", grants, false); err != nil {
		return err
	}
	released := []HostClaim{}
	for _, h := range c.compiled.Spec.Hosts {
		g, err := c.claims.Release(ctx, h)
		if err != nil {
			return joinCoordinatorErrors(err, c.writer.ownership(c.runID, "uncertain", released, false))
		}
		released = append(released, g)
	}
	return c.writer.ownership(c.runID, "released", released, false)
}

// Close releases only reservations from this invocation that have admitted no
// effects. It waits for active operations; ambiguous or started targets remain
// claimed. The caller must cancel its active work before closing.
func (c *SessionCoordinator) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := c.release(ctx)
	c.closed = true
	closeErr := c.writer.Close()
	if err != nil {
		return joinCoordinatorErrors(err, closeErr)
	}
	return closeErr
}
