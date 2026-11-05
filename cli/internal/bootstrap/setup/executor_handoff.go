package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxSessionHandoffs = 16

// SessionHandoff retains the predecessor and the accepted successor. Preparing
// is terminal for the predecessor: it cannot regain installation authority.
type SessionHandoff struct {
	CertificateSHA256 string           `json:"certificateSHA256"`
	SourceRunID       string           `json:"sourceRunId"`
	DestinationRunID  string           `json:"destinationRunId,omitempty"`
	Phase             string           `json:"phase"`
	Cluster           ClusterClaim     `json:"cluster"`
	Claims            []HostClaim      `json:"claims"`
	Attempts          []HandoffAttempt `json:"attempts,omitempty"`
}

// HandoffAttempt records recipient control-plane intent before remote writes.
type HandoffAttempt struct {
	RunID string    `json:"runId"`
	At    time.Time `json:"at"`
}

// ExecutorHandoff binds a sealed host boundary to private recipient material.
// Its saved hash identifies remote parked controls; the file grants no authority.
type ExecutorHandoff struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Binding         PlatformBinding  `json:"binding"`
	Nonce           string           `json:"nonce"`
	RecipientSHA256 string           `json:"recipientSHA256"`
	SourceRunID     string           `json:"sourceRunId"`
	CreatedAt       time.Time        `json:"createdAt"`
	ChildSHA256     string           `json:"childSHA256"`
	Snapshot        PlatformSnapshot `json:"snapshot"`
	Cluster         ClusterClaim     `json:"cluster"`
	Hosts           []HostClaim      `json:"hosts"`
}

func randomHandoffIdentity() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

// hostBoundary rejects the complete platform attempt history. A successful
// postcondition cannot establish quiescence of a prior uncontained executor.
func hostBoundary(r SessionRecord) error {
	if r.ExecutorUncertain || len(SummarizeSession(r).NeedsInspection) != 0 {
		return fmt.Errorf("executor or stage outcome is uncertain; retain inspection-only access")
	}
	for _, event := range r.Events {
		if len(requiredPlatformChecks(clusterinit.StepID(event.Stage))) != 0 {
			return fmt.Errorf("workstation handoff supports the verified host boundary before any platform attempt")
		}
	}
	return nil
}

func (c *SessionCoordinator) handoffInputs(ctx context.Context, r SessionRecord, snapshot PlatformSnapshot) error {
	if err := hostBoundary(r); err != nil {
		return err
	}
	if err := clusterinit.VerifyPreparedScaffold(c.review.Prepared); err != nil {
		return err
	}
	var err error
	if c.platformArtifacts != nil {
		err = c.platformArtifacts(ctx)
	} else {
		_, err = VerifyArtifactCache(ctx, c.compiled, c.review.Artifacts.CacheDirectory, c.cliPath)
	}
	if err != nil {
		return fmt.Errorf("handoff runtime artifacts changed")
	}
	current, err := SnapshotPlatform(ctx, c.compiled.Init.Repo)
	if err != nil || !reflect.DeepEqual(current, snapshot) {
		return fmt.Errorf("handoff Fleet bytes or Git identity changed")
	}
	if c.handoffTargets != nil {
		return c.handoffTargets(ctx)
	}
	if c.realClaims == nil || c.realClaims.ssh == nil {
		return fmt.Errorf("handoff needs pinned storage inspection")
	}
	storage, err := InspectStorageIdentities(ctx, c.compiled, c.realClaims.ssh)
	if err != nil || !reflect.DeepEqual(storage, c.review.Storage) {
		return fmt.Errorf("handoff raw storage identity changed")
	}
	primary := primaryServer(c.compiled.Spec.Hosts)
	_, apiHost, err := c.realClaims.expected(primary)
	if err != nil {
		return err
	}
	for _, host := range c.compiled.Spec.Hosts {
		_, endpoint, err := c.realClaims.expected(host)
		if err != nil {
			return err
		}
		node, err := rke2.VerifyRegisteredNode(ctx, rke2.VerifyNodeOptions{SSH: c.realClaims.ssh, Node: endpoint, APIHost: apiHost, NodeName: host.ID, Role: host.Role, InternalIP: host.ManagementAddress, Attempts: 1})
		if err != nil {
			return fmt.Errorf("handoff RKE2 service or registration is unconfirmed on %s", host.ID)
		}
		stage := "rke2-join-" + host.ID
		if host.ID == primary.ID {
			stage = "rke2-first-server"
		}
		event, ok := lastSessionEvent(r, stage)
		if !ok {
			return fmt.Errorf("handoff host has no node proof")
		}
		proof, err := loadStoredStageProof(c.writer.root, r, event)
		if err != nil || node != proof.Node {
			return fmt.Errorf("handoff host registration differs from its retained proof")
		}
	}
	return ctx.Err()
}

// PrepareExecutorHandoff permanently seals the owning invocation at the
// verified host-to-platform boundary. It never stops an uncertain executor.
// A private recipient key is retained in the session and never printed.
func (c *SessionCoordinator) PrepareExecutorHandoff(ctx context.Context) (SessionHandoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result SessionHandoff
	if c.closed || c.sealed || c.executorUncertain.Load() {
		return result, fmt.Errorf("handoff requires an unsealed, quiescent owning invocation")
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		return result, err
	}
	if len(r.Handoffs) >= maxSessionHandoffs || len(r.OwnershipEvents)+4 > maxOwnershipEvents {
		return result, fmt.Errorf("handoff journal capacity exhausted")
	}
	if err := hostBoundary(r); err != nil {
		return result, err
	}
	if err := c.platformOwnership(ctx, r, true); err != nil {
		return result, err
	}
	handoff, err := verifiedSessionHandoff(c.writer.root, r)
	if err != nil {
		return result, err
	}
	status := inspectSessionCluster(ctx, c.writer.directory, r, c.compiled, c.runID, c.childClient())
	if !status.CurrentRun || status.Claim == nil {
		return result, fmt.Errorf("handoff requires the exact owned cluster Lease")
	}
	snapshot, err := SnapshotPlatform(ctx, c.compiled.Init.Repo)
	if err != nil {
		return result, err
	}
	if err := RecheckGitBinding(ctx, c.review.Git); err != nil {
		return result, err
	}
	if err := c.handoffInputs(ctx, r, snapshot); err != nil {
		return result, err
	}
	hosts := []HostClaim{}
	for _, h := range c.compiled.Spec.Hosts {
		claim, err := c.claims.Check(ctx, h)
		if err != nil || claim.ExecutorProtocol != "systemd-v1" || claim.RunID != c.runID || claim.State != "started" {
			return result, fmt.Errorf("host %s lacks complete contained executor evidence", h.ID)
		}
		hosts = append(hosts, claim)
	}
	key, err := randomHandoffIdentity()
	if err != nil {
		return result, err
	}
	nonce, err := randomHandoffIdentity()
	if err != nil {
		return result, err
	}
	recipient := sha256.Sum256([]byte(key))
	cert := ExecutorHandoff{SchemaVersion: 1, Binding: platformBinding(r), Nonce: nonce, RecipientSHA256: hex.EncodeToString(recipient[:]), SourceRunID: c.runID, CreatedAt: time.Now().UTC(), ChildSHA256: handoff.ChildSHA256, Snapshot: snapshot, Cluster: *status.Claim, Hosts: hosts}
	ref, err := c.writer.savePlatformArtifact(cert)
	if err != nil {
		return result, err
	}
	if err := c.writer.saveHandoffKey(ref, key); err != nil {
		return result, err
	}
	result = SessionHandoff{CertificateSHA256: ref, SourceRunID: c.runID, Phase: "preparing", Cluster: cert.Cluster, Claims: hosts}
	// No mutation is admitted after this point, including when publication or
	// a remote transition fails. A copied intent can only finish the transfer.
	c.sealed = true
	if err := c.writer.prepareHandoff(result); err != nil {
		return result, err
	}
	if err := c.parkHandoff(ctx, cert, ref); err != nil {
		return result, err
	}
	return result, nil
}

func (w *SessionWriter) saveHandoffKey(ref, key string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	f, err := w.root.OpenFile("handoff-"+ref+".key", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create private handoff recipient key failed")
	}
	_, writeErr := f.WriteString(key)
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		w.poisoned = true
		return fmt.Errorf("persist handoff recipient key failed")
	}
	if err := w.syncDirectory(w.root); err != nil {
		w.poisoned = true
		return fmt.Errorf("sync handoff recipient key failed")
	}
	return nil
}

func (w *SessionWriter) prepareHandoff(h SessionHandoff) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if !w.matched || w.record.Ownership == nil || w.record.Ownership.RunID != h.SourceRunID || w.record.Ownership.Phase != "held" {
		return fmt.Errorf("handoff intent requires the current owner")
	}
	next := w.record
	next.Handoffs = append(append([]SessionHandoff{}, next.Handoffs...), h)
	next.Ownership = &SessionOwnership{RunID: h.SourceRunID, Phase: "handoff", Claims: h.Claims}
	next.OwnershipEvents = append(append([]OwnershipEvent{}, next.OwnershipEvents...), OwnershipEvent{RunID: h.SourceRunID, Phase: "handoff", At: time.Now().UTC(), EvidenceSHA256: fingerprintOwnership(next.Ownership)})
	if err := validateHandoffEvidence(w.root, next); err != nil {
		return err
	}
	return w.publish(next)
}

func (c *SessionCoordinator) controlHandoffHost(ctx context.Context, h Host, claim HostClaim, action string) (HostClaim, error) {
	if c.handoffHost != nil {
		return c.handoffHost(ctx, h, claim, action)
	}
	if c.realClaims == nil || c.realClaims.ssh == nil {
		return HostClaim{}, fmt.Errorf("handoff requires pinned guarded SSH")
	}
	_, endpoint, err := c.realClaims.expected(h)
	if err != nil {
		return HostClaim{}, err
	}
	command, err := hostClaimCommand(claim, action, false, "", hostClaimBase, 0)
	if err != nil {
		return HostClaim{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body, key, err := c.realClaims.ssh.RunCappedWithHostKey(ctx, endpoint, command, 4096)
	if err != nil || key.FingerprintSHA256 != h.HostKeySHA256 {
		return HostClaim{}, fmt.Errorf("host %s handoff %s is unconfirmed; retain claims", h.ID, action)
	}
	got, err := parseHostClaim(body, h.ID)
	if err != nil {
		return got, err
	}
	want := claim
	want.State = "handoff"
	if action == "activate" {
		want.State, want.HandoffID, want.SourceRunID = "started", "", ""
		want.HandoffEpoch = 0
	}
	if !sameHandoffHost(got, want) {
		return got, fmt.Errorf("host handoff response changed")
	}
	return got, ctx.Err()
}

func sameHandoffHost(a, b HostClaim) bool {
	a.ExpiresTick, a.ObservedTick, b.ExpiresTick, b.ObservedTick = 0, 0, 0, 0
	return a == b
}

func (c *SessionCoordinator) parkHandoff(ctx context.Context, cert ExecutorHandoff, ref string) error {
	for i, h := range c.compiled.Spec.Hosts {
		claim := cert.Hosts[i]
		claim.HandoffID, claim.SourceRunID = ref, cert.SourceRunID
		got, err := c.claims.Inspect(ctx, h)
		if err != nil {
			return fmt.Errorf("read host handoff failed")
		}
		if got.State == "handoff" {
			claim.State, claim.RunID, claim.HandoffEpoch = "handoff", got.RunID, got.HandoffEpoch
			if !sameHandoffHost(got, claim) {
				return fmt.Errorf("parked host identity changed")
			}
			continue
		}
		if _, err := c.controlHandoffHost(ctx, h, claim, "park"); err != nil {
			return err
		}
	}
	_, err := c.changeHandoffLease(ctx, cert, ref, "park", "")
	return err
}

// AcceptExecutorHandoff uses a fresh invocation identity. It can complete
// pending control-plane work for the designated recipient. It never restores
// a saved run or treats an expired claim, absent unit or lost SSH as quiescence.
func (c *SessionCoordinator) AcceptExecutorHandoff(ctx context.Context, ref string) (CoordinatorStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !validSessionHash(ref) {
		return CoordinatorStatus{}, fmt.Errorf("an open session and exact handoff certificate are required")
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		return CoordinatorStatus{}, err
	}
	if r.Ownership == nil || r.Ownership.Phase != "handoff" || len(r.Handoffs) == 0 {
		return CoordinatorStatus{}, fmt.Errorf("session has no prepared executor handoff; reopened execution stays inspection-only")
	}
	h := r.Handoffs[len(r.Handoffs)-1]
	if h.Phase != "preparing" || h.CertificateSHA256 != ref || c.runID == h.SourceRunID {
		return CoordinatorStatus{}, fmt.Errorf("handoff requires its designated recipient in a fresh invocation")
	}
	var cert ExecutorHandoff
	if err := readPlatformArtifact(c.writer.root, ref, &cert); err != nil {
		return CoordinatorStatus{}, err
	}
	if err := checkHandoffCertificate(c.writer.root, r, h, cert); err != nil {
		return CoordinatorStatus{}, err
	}
	if c.executorUncertain.Load() {
		return CoordinatorStatus{}, fmt.Errorf("executor outcome is uncertain")
	}
	if err := c.checkHandoffKey(ref, cert); err != nil {
		return CoordinatorStatus{}, err
	}
	if err := c.handoffInputs(ctx, r, cert.Snapshot); err != nil {
		return CoordinatorStatus{}, err
	}
	if err := c.writer.beginHandoffAttempt(ref, c.runID); err != nil {
		return CoordinatorStatus{}, err
	}
	// Idempotent parking can finish an interrupted preparation. It never
	// re-enables the predecessor and cannot modify an activated host/Lease.
	if err := c.parkHandoff(ctx, cert, ref); err != nil {
		return CoordinatorStatus{}, err
	}
	pending, err := c.changeHandoffLease(ctx, cert, ref, "claim", "")
	if err != nil {
		return CoordinatorStatus{}, err
	}
	claims := []HostClaim{}
	for i, host := range c.compiled.Spec.Hosts {
		claim := cert.Hosts[i]
		claim.RunID, claim.SourceRunID, claim.HandoffID = c.runID, cert.SourceRunID, ref
		claim.HandoffEpoch = pending.HandoffEpoch
		got, err := c.controlHandoffHost(ctx, host, claim, "transfer")
		if err != nil {
			return CoordinatorStatus{}, err
		}
		claims = append(claims, got)
	}
	if err := c.handoffInputs(ctx, r, cert.Snapshot); err != nil {
		return CoordinatorStatus{}, err
	}
	if err := c.checkPlatformNodes(ctx, r); err != nil {
		return CoordinatorStatus{}, err
	}
	// Re-read every host before committing the Lease. Other candidates can
	// only move pending controls; none can run a parked payload.
	for i, host := range c.compiled.Spec.Hosts {
		got, err := c.claims.Inspect(ctx, host)
		if err != nil || !sameHandoffHost(got, claims[i]) {
			return CoordinatorStatus{}, fmt.Errorf("pending host handoff changed before activation")
		}
	}
	active, err := c.changeHandoffLease(ctx, cert, ref, "activate", pending.ResourceVersion)
	if err != nil {
		return CoordinatorStatus{}, err
	}
	for i, host := range c.compiled.Spec.Hosts {
		got, err := c.controlHandoffHost(ctx, host, claims[i], "activate")
		if err != nil {
			return CoordinatorStatus{}, err
		}
		claims[i] = got
	}
	h.Phase, h.DestinationRunID, h.Cluster, h.Claims = "accepted", c.runID, active, claims
	if err := c.writer.acceptHandoff(h); err != nil {
		return CoordinatorStatus{}, err
	}
	if c.realClaims != nil {
		next := *c.realClaims
		next.runID = c.runID
		c.realClaims = &next
		if _, ok := c.claims.(*HostClaims); ok {
			c.claims = c.realClaims
		}
	}
	c.sealed = false
	r, err = c.writer.Snapshot()
	if err != nil {
		c.sealed = true
		return CoordinatorStatus{}, err
	}
	if err := c.platformOwnership(ctx, r, true); err != nil {
		c.sealed = true
		return CoordinatorStatus{}, err
	}
	return c.status(ctx)
}

func (c *SessionCoordinator) checkHandoffKey(ref string, cert ExecutorHandoff) error {
	name := "handoff-" + ref + ".key"
	info, err := c.writer.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 64 {
		return fmt.Errorf("handoff requires its private recipient key")
	}
	f, err := c.writer.root.Open(name)
	if err != nil {
		return fmt.Errorf("open handoff recipient key failed")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("handoff recipient key changed")
	}
	var key [64]byte
	n, err := f.Read(key[:])
	if err != nil || n != 64 || !validSessionHash(string(key[:])) {
		return fmt.Errorf("invalid handoff recipient key")
	}
	hash := sha256.Sum256(key[:])
	clear(key[:])
	if hex.EncodeToString(hash[:]) != cert.RecipientSHA256 {
		return fmt.Errorf("handoff recipient does not match")
	}
	return nil
}

func (w *SessionWriter) acceptHandoff(h SessionHandoff) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if !w.matched || w.record.Ownership == nil || w.record.Ownership.Phase != "handoff" || len(w.record.Handoffs) == 0 {
		return fmt.Errorf("handoff has no sealed predecessor")
	}
	previous := w.record.Handoffs[len(w.record.Handoffs)-1]
	if previous.Phase != "preparing" || previous.CertificateSHA256 != h.CertificateSHA256 || previous.SourceRunID != h.SourceRunID {
		return fmt.Errorf("handoff predecessor changed")
	}
	h.Attempts = append([]HandoffAttempt{}, previous.Attempts...)
	next := w.record
	next.Handoffs = append([]SessionHandoff{}, next.Handoffs...)
	next.Handoffs[len(next.Handoffs)-1] = h
	next.Ownership = &SessionOwnership{RunID: h.DestinationRunID, Phase: "held", Claims: h.Claims}
	next.OwnershipEvents = append(append([]OwnershipEvent{}, next.OwnershipEvents...), OwnershipEvent{RunID: h.DestinationRunID, Phase: "held", At: time.Now().UTC(), EvidenceSHA256: fingerprintOwnership(next.Ownership)})
	if err := validateHandoffEvidence(w.root, next); err != nil {
		return err
	}
	return w.publish(next)
}

func (w *SessionWriter) beginHandoffAttempt(ref, run string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if !w.matched || !validSessionHash(run) || len(w.record.Handoffs) == 0 {
		return fmt.Errorf("handoff attempt has no matched intent")
	}
	index := len(w.record.Handoffs) - 1
	h := w.record.Handoffs[index]
	if h.Phase != "preparing" || h.CertificateSHA256 != ref || h.SourceRunID == run {
		return fmt.Errorf("handoff recipient intent changed")
	}
	for _, a := range h.Attempts {
		if a.RunID == run {
			return nil
		}
	}
	if len(h.Attempts) >= 64 {
		return fmt.Errorf("handoff attempt capacity exhausted")
	}
	h.Attempts = append(append([]HandoffAttempt{}, h.Attempts...), HandoffAttempt{RunID: run, At: time.Now().UTC()})
	next := w.record
	next.Handoffs = append([]SessionHandoff{}, next.Handoffs...)
	next.Handoffs[index] = h
	if err := validateHandoffEvidence(w.root, next); err != nil {
		return err
	}
	return w.publish(next)
}

func transferredOwner(r SessionRecord, source, destination string) bool {
	for _, h := range r.Handoffs {
		if h.Phase == "accepted" && h.SourceRunID == source && h.DestinationRunID == destination {
			return true
		}
	}
	return false
}
func transferredDestination(r SessionRecord, destination string) bool {
	for _, h := range r.Handoffs {
		if h.Phase == "accepted" && h.DestinationRunID == destination {
			return true
		}
	}
	return false
}

func validateHandoffEvidence(root *os.Root, r SessionRecord) error {
	if len(r.Handoffs) > maxSessionHandoffs {
		return fmt.Errorf("handoff history exceeds limit")
	}
	previous := ""
	for i, h := range r.Handoffs {
		if !validSessionHash(h.SourceRunID) || !validSessionHash(h.CertificateSHA256) || (previous != "" && h.SourceRunID != previous) {
			return fmt.Errorf("invalid handoff ancestry")
		}
		var cert ExecutorHandoff
		if err := readPlatformArtifact(root, h.CertificateSHA256, &cert); err != nil {
			return err
		}
		if err := checkHandoffCertificate(root, r, h, cert); err != nil {
			return err
		}
		if len(h.Attempts) > 64 {
			return fmt.Errorf("handoff attempts exceed limit")
		}
		seen := map[string]bool{}
		at := cert.CreatedAt
		for _, a := range h.Attempts {
			if !validSessionHash(a.RunID) || a.RunID == h.SourceRunID || seen[a.RunID] || a.At.Before(at) || a.At.After(time.Now().Add(time.Minute)) {
				return fmt.Errorf("invalid handoff attempt history")
			}
			seen[a.RunID] = true
			at = a.At
		}
		if h.Phase == "accepted" {
			if !seen[h.DestinationRunID] {
				return fmt.Errorf("accepted handoff has no durable recipient intent")
			}
			if !validSessionHash(h.DestinationRunID) || h.DestinationRunID == h.SourceRunID || h.Cluster.RunID != h.DestinationRunID || h.Cluster.UID != cert.Cluster.UID || h.Cluster.HandoffID != "" || len(h.Claims) != len(cert.Hosts) {
				return fmt.Errorf("invalid accepted handoff")
			}
			want := cert.Cluster
			want.RunID = h.DestinationRunID
			if !sameClusterClaimOwner(want, h.Cluster) || validateClusterClaim(h.Cluster, true) != nil {
				return fmt.Errorf("accepted handoff cluster changed")
			}
			for i, got := range h.Claims {
				want := cert.Hosts[i]
				want.RunID = h.DestinationRunID
				if !sameHandoffHost(got, want) {
					return fmt.Errorf("accepted handoff host changed")
				}
			}
			previous = h.DestinationRunID
		} else if h.Phase != "preparing" || i != len(r.Handoffs)-1 || h.DestinationRunID != "" || r.Ownership == nil || r.Ownership.Phase != "handoff" || r.Ownership.RunID != h.SourceRunID || !reflect.DeepEqual(h.Cluster, cert.Cluster) || !reflect.DeepEqual(h.Claims, cert.Hosts) {
			return fmt.Errorf("invalid pending handoff")
		}
	}
	return nil
}

func checkHandoffCertificate(root *os.Root, r SessionRecord, h SessionHandoff, cert ExecutorHandoff) error {
	if cert.SchemaVersion != 1 || cert.Binding != platformBinding(r) || cert.SourceRunID != h.SourceRunID || !validSessionHash(cert.Nonce) || !validSessionHash(cert.RecipientSHA256) || cert.CreatedAt.Before(r.CreatedAt) || cert.CreatedAt.After(time.Now().Add(time.Minute)) || len(cert.Hosts) != len(r.Targets) || cert.Cluster.RunID != cert.SourceRunID || validateClusterClaim(cert.Cluster, true) != nil || cert.Cluster.HandoffID != "" {
		return fmt.Errorf("invalid executor handoff certificate")
	}
	child, err := verifiedSessionHandoff(root, r)
	if err != nil || child.ChildSHA256 != cert.ChildSHA256 || child.Node.UID != cert.Cluster.PrimaryNodeUID {
		return fmt.Errorf("handoff child proof changed")
	}
	if !sameClusterClaimOwner(cert.Cluster, expectedClusterClaim(r, cert.SourceRunID, child.Node)) {
		return fmt.Errorf("handoff cluster binding changed")
	}
	event, ok := lastSessionEvent(r, clusterClaimStage)
	if !ok {
		return fmt.Errorf("handoff has no original cluster reservation")
	}
	proof, err := loadStoredStageProof(root, r, event)
	if err != nil || proof.Cluster == nil || proof.Cluster.UID != cert.Cluster.UID {
		return fmt.Errorf("handoff cluster instance differs from original proof")
	}
	if event.At.After(cert.CreatedAt) {
		return fmt.Errorf("handoff predates the original cluster proof")
	}
	for _, e := range r.Events {
		if !e.At.After(cert.CreatedAt) && len(requiredPlatformChecks(clusterinit.StepID(e.Stage))) != 0 {
			return fmt.Errorf("handoff followed an uncontained platform attempt")
		}
	}
	for i, claim := range cert.Hosts {
		t := r.Targets[i]
		if claim.HostID != t.ID || claim.MachineID != t.MachineID || claim.RunID != cert.SourceRunID || claim.SessionID != r.ID || claim.InputHash != r.InputHash || claim.ReviewHash != r.ReviewHash || claim.ReleaseSHA256 != r.ReleaseSHA256 || claim.State != "started" || claim.ExecutorProtocol != "systemd-v1" || claim.ExecutorUnit == "" || claim.BootID != claim.ObservedBootID || !claimBootPattern.MatchString(claim.BootID) || claim.ExpiresTick <= claim.ObservedTick || claim.HandoffID != "" || claim.SourceRunID != "" {
			return fmt.Errorf("invalid handoff host proof")
		}
	}
	return nil
}

// changeHandoffLease uses the API's resourceVersion CAS and never deletes or
// replaces the Lease. Pending claimants have control-plane authority only.
func (c *SessionCoordinator) changeHandoffLease(ctx context.Context, cert ExecutorHandoff, ref, action, version string) (ClusterClaim, error) {
	r, err := c.writer.Snapshot()
	if err != nil {
		return ClusterClaim{}, err
	}
	child, err := verifiedSessionHandoff(c.writer.root, r)
	if err != nil {
		return ClusterClaim{}, err
	}
	client, err := c.childClient()(ctx, c.compiled, child.ChildPath, child.Node)
	if err != nil {
		return ClusterClaim{}, err
	}
	leases := client.CoordinationV1().Leases(clusterClaimNamespace)
	lease, err := leases.Get(ctx, clusterClaimName, metav1.GetOptions{})
	if err != nil {
		return ClusterClaim{}, fmt.Errorf("read handoff Lease failed")
	}
	got, err := parseClusterClaim(lease)
	if err != nil || got.UID != cert.Cluster.UID {
		return ClusterClaim{}, fmt.Errorf("handoff Lease instance changed")
	}
	want := cert.Cluster
	if got.HandoffID != "" {
		want.RunID, want.HandoffID, want.SourceRunID, want.HandoffEpoch = got.RunID, ref, cert.SourceRunID, got.HandoffEpoch
	}
	if !sameClusterClaimOwner(got, want) {
		return ClusterClaim{}, fmt.Errorf("handoff Lease owner changed")
	}
	if action == "park" {
		if got.HandoffID != "" {
			return got, nil
		}
		if got.RunID != cert.SourceRunID {
			return got, fmt.Errorf("handoff Lease was already consumed")
		}
	} else if got.HandoffID != ref || got.SourceRunID != cert.SourceRunID {
		return got, fmt.Errorf("Lease has no matching pending handoff")
	}
	if action == "activate" && (got.RunID != c.runID || got.ResourceVersion != version) {
		return got, fmt.Errorf("pending handoff claimant changed")
	}
	if action == "claim" {
		if got.HandoffEpoch >= 65535 {
			return got, fmt.Errorf("handoff claim epoch exhausted")
		}
		lease.Spec.HolderIdentity = &c.runID
		lease.Annotations[clusterClaimPrefix+"run"] = c.runID
		lease.Annotations[clusterClaimPrefix+"handoff-epoch"] = strconv.FormatInt(got.HandoffEpoch+1, 10)
	}
	if action == "activate" {
		delete(lease.Annotations, clusterClaimPrefix+"handoff")
		delete(lease.Annotations, clusterClaimPrefix+"handoff-source")
		delete(lease.Annotations, clusterClaimPrefix+"handoff-epoch")
	} else {
		lease.Annotations[clusterClaimPrefix+"handoff"], lease.Annotations[clusterClaimPrefix+"handoff-source"] = ref, cert.SourceRunID
	}
	updated, err := leases.Update(ctx, lease, metav1.UpdateOptions{})
	if err != nil {
		return ClusterClaim{}, fmt.Errorf("handoff Lease CAS was not confirmed; inspect before continuing")
	}
	result, err := parseClusterClaim(updated)
	if err != nil {
		return result, err
	}
	observed, err := leases.Get(ctx, clusterClaimName, metav1.GetOptions{})
	if err != nil {
		return result, fmt.Errorf("handoff Lease readback failed")
	}
	checked, err := parseClusterClaim(observed)
	if err != nil || checked.UID != result.UID || !sameClusterClaimOwner(checked, result) {
		return checked, fmt.Errorf("handoff Lease changed during readback")
	}
	return checked, ctx.Err()
}
