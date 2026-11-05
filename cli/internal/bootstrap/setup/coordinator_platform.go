package setup

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PlatformExecution binds the existing engine to explicit child access and
// exact prepared bytes. Only the coordinator constructs a mutation reporter.
type PlatformExecution struct {
	Options     clusterinit.InitOptions
	Environment map[string]string
	Prepared    clusterinit.PreparedScaffold
	Session     *bootstrap.Session
	Client      kubernetes.Interface
	Kubeconfig  string
	Reporter    clusterinit.StepReporter
	Out         io.Writer
}

// PlatformEngine uses the current Apply/finalization drivers. Observe must
// perform read-only, stage-specific live checks; callback success is insufficient.
type PlatformEngine interface {
	Run(context.Context, PlatformExecution) error
	Observe(context.Context, PlatformExecution, clusterinit.StepID) (PlatformObservation, error)
}

func requiredPlatformChecks(id clusterinit.StepID) []string {
	switch id {
	case clusterinit.StepIngressNodes:
		return []string{"ingress-nodes"}
	case clusterinit.StepScaffold:
		return []string{"prepared-publication"}
	case clusterinit.StepCommitPush:
		return []string{"git-publication"}
	case clusterinit.StepFluxInstall:
		return []string{"flux-bootstrap"}
	case clusterinit.StepBreakGlass:
		return []string{"break-glass"}
	case clusterinit.StepReconcile:
		return []string{"platform-reconcile"}
	case clusterinit.StepOpenBao:
		return []string{"openbao-health", "openbao-escrow"}
	case clusterinit.StepKeycloakOIDC:
		return []string{"keycloak-clients"}
	case clusterinit.StepOIDCCutover:
		return []string{"oidc-cutover"}
	case clusterinit.StepManagedServices:
		return []string{"managed-services", "backup-admission"}
	case clusterinit.StepGPUInventory, clusterinit.StepGPUOperator, clusterinit.StepGPUHAMi, clusterinit.StepGPUProduct:
		return []string{string(id)}
	case clusterinit.StepPlatformAccept:
		return []string{"platform-acceptance"}
	}
	return nil
}

func checkPlatformObservation(id clusterinit.StepID, observation PlatformObservation) error {
	if err := validatePlatformObservation(observation); err != nil {
		return err
	}
	required := requiredPlatformChecks(id)
	if len(required) == 0 {
		return fmt.Errorf("stage has no platform verifier")
	}
	for _, want := range required {
		found := false
		for _, c := range observation.Checks {
			found = found || c.ID == want
		}
		if !found {
			return fmt.Errorf("platform stage has missing postconditions")
		}
	}
	return nil
}

func (c *SessionCoordinator) platformOwnership(ctx context.Context, r SessionRecord, hostCheck bool) error {
	if c.closed || c.sealed || r.Ownership == nil || r.Ownership.Phase != "held" || r.Ownership.RunID != c.runID {
		return fmt.Errorf("platform mutation requires this invocation's held ownership; reopened runs are inspection-only")
	}
	if hostCheck {
		if err := checkStartedHostClaims(ctx, c.compiled.Spec.Hosts, r, c.runID, c.claims); err != nil {
			return err
		}
		if err := c.checkPlatformNodes(ctx, r); err != nil {
			return err
		}
	}
	status := inspectSessionCluster(ctx, c.writer.directory, r, c.compiled, c.runID, c.childClient())
	if status.State != "held" || !status.CurrentRun || !status.MatchesProof {
		return fmt.Errorf("platform cluster ownership is not confirmed")
	}
	return ctx.Err()
}

func (c *SessionCoordinator) checkPlatformNodes(ctx context.Context, r SessionRecord) error {
	handoff, err := verifiedSessionHandoff(c.writer.root, r)
	if err != nil {
		return err
	}
	client, err := c.childClient()(ctx, c.compiled, handoff.ChildPath, handoff.Node)
	if err != nil {
		return err
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != len(c.compiled.Spec.Hosts) {
		return fmt.Errorf("live node inventory differs from reviewed hosts")
	}
	for _, host := range c.compiled.Spec.Hosts {
		stage := "rke2-join-" + host.ID
		if host.ID == primaryServer(c.compiled.Spec.Hosts).ID {
			stage = "rke2-first-server"
		}
		event, ok := lastSessionEvent(r, stage)
		if !ok || event.Source != "coordinator" || event.State != "succeeded" {
			return fmt.Errorf("platform host has no verified node proof")
		}
		proof, err := loadStoredStageProof(c.writer.root, r, event)
		if err != nil {
			return err
		}
		found := false
		for _, node := range nodes.Items {
			if node.Name == host.ID && string(node.UID) == proof.Node.UID && hasInternalIP(node.Status.Addresses, host.ManagementAddress) && hasControlPlaneRole(node.Labels) == (host.Role == "server") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("live node UID, role, or address differs from the installed host proof")
		}
	}
	return ctx.Err()
}

func (c *SessionCoordinator) platformExecution(ctx context.Context, r SessionRecord) (PlatformExecution, error) {
	var execution PlatformExecution
	handoff, err := verifiedSessionHandoff(c.writer.root, r)
	if err != nil {
		return execution, err
	}
	client, err := c.childClient()(ctx, c.compiled, handoff.ChildPath, handoff.Node)
	if err != nil {
		return execution, err
	}
	var session *bootstrap.Session
	if c.platformSession != nil {
		session, err = c.platformSession(ctx, handoff.ChildPath)
	} else {
		session, err = OpenVerifiedChildSession(ctx, c.compiled, handoff.ChildPath, handoff.Node)
	}
	if err != nil {
		return execution, err
	}
	if transport, ok := session.SSH.(ports.CappedSSHHostKeyClient); ok {
		session.SSH = &reviewedReadSSH{base: transport, hosts: c.compiled.Spec.Hosts}
	}
	environment := make(map[string]string, len(c.review.Environment))
	for k, v := range c.review.Environment {
		environment[k] = v
	}
	return PlatformExecution{Options: c.Inputs(), Environment: environment, Prepared: c.review.Prepared, Session: session, Client: client, Kubeconfig: handoff.ChildPath, Out: io.Discard}, nil
}

// RunPlatform remains behind qualified readiness. It serializes all effects,
// admits only a fresh retained review, and cancels after any uncertain outcome.
func (c *SessionCoordinator) RunPlatform(ctx context.Context, readiness ReadinessReport, reviewHash string, engine PlatformEngine, out io.Writer, reporter clusterinit.StepReporter) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.realClaims == nil || engine == nil || readiness.SchemaVersion != ReadinessSchemaVersion || !readiness.ReadyToApply || readiness.State != "ready" || readiness.BlockedCount != 0 || len(readiness.Unresolved) != 0 || readiness.InputHash != c.compiled.InputHash || readiness.ReleaseSHA256 != c.compiled.ReleaseSHA256 {
		return fmt.Errorf("platform execution requires complete qualified readiness and real owned targets")
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		return err
	}
	if r.ExecutorUncertain || c.executorUncertain.Load() {
		return fmt.Errorf("executor outcome is uncertain; evidence reconciliation cannot restore mutation authority")
	}
	if readiness.PlanHash != r.PlanHash || len(SummarizeSession(r).NeedsInspection) != 0 {
		return fmt.Errorf("reconcile uncertain stage evidence before platform execution")
	}
	if err := c.platformOwnership(ctx, r, true); err != nil {
		return err
	}
	var review PlatformReview
	reference := ""
	for _, hash := range r.PlatformReviews {
		var candidate PlatformReview
		if err := readPlatformArtifact(c.writer.root, hash, &candidate); err != nil {
			return err
		}
		if candidate.Hash == reviewHash {
			review, reference = candidate, hash
		}
	}
	if reference == "" || time.Since(review.CreatedAt) < 0 || time.Since(review.CreatedAt) > maxHostInventoryAge {
		return fmt.Errorf("refresh and review platform effects before execution")
	}
	if review.Binding != platformBinding(r) || review.Hash != platformReviewHash(review) {
		return fmt.Errorf("platform execution review changed")
	}
	if review.Cluster.RunID != c.runID {
		return fmt.Errorf("refresh execution review for the accepted workstation owner")
	}
	prior := ""
	if latest, ok := latestPlatformReceipt(r); ok {
		prior = latest.EvidenceSHA256
	}
	if review.PriorReceipt != prior {
		return fmt.Errorf("platform progress changed after review; refresh execution review before continuing")
	}
	if err := clusterinit.VerifyPreparedScaffold(c.review.Prepared); err != nil {
		return err
	}
	if c.platformArtifacts != nil {
		err = c.platformArtifacts(ctx)
	} else {
		_, err = VerifyArtifactCache(ctx, c.compiled, c.review.Artifacts.CacheDirectory, c.cliPath)
	}
	if err != nil {
		return fmt.Errorf("reviewed runtime artifacts are not confirmed: %w", err)
	}
	current, err := SnapshotPlatform(ctx, c.compiled.Init.Repo)
	if err != nil || !reflect.DeepEqual(current, review.Snapshot) {
		return fmt.Errorf("platform state changed after review")
	}
	execution, err := c.platformExecution(ctx, r)
	if err != nil {
		return err
	}
	defer execution.Session.Close()
	if err := bindPlatformGit(ctx, c.compiled.Init, execution.Session); err != nil {
		return err
	}
	if reporter == nil {
		reporter = clusterinit.NopReporter{}
	}
	if out == nil {
		out = io.Discard
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	gate := &platformExecutor{coordinator: c, ctx: runCtx, cancel: cancel, engine: engine, execution: execution, reporter: reporter, review: review, reference: reference, snapshot: review.Snapshot, prior: review.PriorReceipt}
	runCtx = ports.WithMutationGuard(runCtx, gate.CheckExecution)
	gate.ctx = runCtx
	execution.Reporter, execution.Out = gate, out
	gate.execution = execution
	guardPlatformSession(execution.Session, gate, c.realClaims)
	// A returned callback is not proof that a remote payload stopped. The
	// independent executor fence survives positive evidence reconciliation.
	err = engine.Run(runCtx, execution)
	gate.mu.Lock()
	failure := gate.failure
	gate.mu.Unlock()
	if failure != nil {
		return failure
	}
	if err != nil {
		return err
	}
	completed, err := c.writer.Snapshot()
	if err != nil {
		return err
	}
	for _, stage := range review.Effects {
		if c.compiled.Init.NoPush && stage.ID == string(clusterinit.StepIngressNodes) {
			continue
		}
		event, ok := lastSessionEvent(completed, stage.ID)
		if !ok || event.State != "succeeded" || (event.Source != "platform" && event.Source != "platform-inspection") {
			return fmt.Errorf("platform execution has an unverified required stage: %s", stage.ID)
		}
	}
	return nil
}

func bindPlatformGit(ctx context.Context, o clusterinit.InitOptions, s *bootstrap.Session) error {
	if o.NoPush {
		return nil
	}
	binder, ok := s.Git.(ports.GitProviderBinder)
	if !ok {
		return fmt.Errorf("platform execution requires a provider-bound Git client")
	}
	host, err := clusterinit.ResolveRepositoryProviderHost(ctx, o.Provider, o.ProviderHost, o.Repo)
	if err != nil {
		return err
	}
	s.Git, err = binder.ForProvider(ctx, o.Repo, host)
	return err
}

type platformExecutor struct {
	coordinator *SessionCoordinator
	ctx         context.Context
	cancel      context.CancelFunc
	engine      PlatformEngine
	execution   PlatformExecution
	reporter    clusterinit.StepReporter
	review      PlatformReview
	reference   string
	snapshot    PlatformSnapshot
	prior       string
	mu          sync.Mutex
	stageMu     sync.Mutex
	active      clusterinit.StepID
	failure     error
}

func (g *platformExecutor) Plan(steps []clusterinit.Step)             { g.reporter.Plan(steps) }
func (g *platformExecutor) Start(id clusterinit.StepID)               { g.reporter.Start(id) }
func (g *platformExecutor) Done(id clusterinit.StepID, err error)     { g.reporter.Done(id, err) }
func (g *platformExecutor) Skip(id clusterinit.StepID, reason string) { g.reporter.Skip(id, reason) }

func (g *platformExecutor) CheckExecution(ctx context.Context) error {
	g.mu.Lock()
	active, failed := g.active, g.failure
	g.mu.Unlock()
	if failed != nil || active == "" || g.ctx.Err() != nil || ctx.Err() != nil || g.coordinator.executorUncertain.Load() {
		return fmt.Errorf("no active admitted platform effect")
	}
	r, err := g.coordinator.writer.Snapshot()
	if err != nil {
		return err
	}
	return g.coordinator.platformOwnership(ctx, r, false)
}

func (g *platformExecutor) ExecuteStep(id clusterinit.StepID, run func() error) (runErr error) {
	if !g.stageMu.TryLock() {
		return g.stop(fmt.Errorf("concurrent platform stages are not permitted"))
	}
	defer g.stageMu.Unlock()
	g.mu.Lock()
	if g.failure != nil || g.active != "" || g.ctx.Err() != nil {
		g.mu.Unlock()
		return fmt.Errorf("platform execution stopped; inspect its previous outcome")
	}
	g.mu.Unlock()
	c := g.coordinator
	r, err := c.writer.Snapshot()
	if err != nil {
		return g.stop(err)
	}
	planned := false
	for _, s := range r.Stages {
		planned = planned || s.ID == string(id)
	}
	if !planned || len(requiredPlatformChecks(id)) == 0 {
		return g.stop(fmt.Errorf("unreviewed platform stage"))
	}
	if err := c.platformOwnership(g.ctx, r, true); err != nil {
		return g.stop(err)
	}
	if previous, ok := lastSessionEvent(r, string(id)); ok {
		if previous.State != "succeeded" || (previous.Source != "platform" && previous.Source != "platform-inspection") {
			return g.stop(fmt.Errorf("inspect the previous platform attempt before retry"))
		}
		g.reporter.Skip(id, "verified receipt retained")
		return nil
	}
	current, err := SnapshotPlatform(g.ctx, c.compiled.Init.Repo)
	if err != nil || !reflect.DeepEqual(current, g.snapshot) {
		return g.stop(fmt.Errorf("Fleet changed outside the previous accepted effect"))
	}
	if err := checkPlatformStageInputs(id, c.review.Prepared, g.snapshot); err != nil {
		return g.stop(err)
	}
	if id == clusterinit.StepScaffold || id == clusterinit.StepFluxInstall {
		expected := make([]string, 0, len(c.compiled.Spec.Hosts))
		for _, host := range c.compiled.Spec.Hosts {
			expected = append(expected, host.ID)
		}
		if err := clusterinit.CheckCephKernels(g.ctx, clusterinit.CephKernelGateOptions{Nodes: platformKernelInventory{client: g.execution.Client}, Mode: c.compiled.Init.RookMode, OverlayPath: filepath.Join(c.review.Prepared.Directory, "clusters", c.compiled.Spec.Name, "object-storage", "kustomization.yaml"), Out: g.execution.Out, RequireEvidence: true, ExpectedNodes: expected}); err != nil {
			return g.stop(err)
		}
	}
	if id == clusterinit.StepFluxInstall {
		storage, err := InspectStorageIdentities(g.ctx, c.compiled, c.realClaims.ssh)
		if err != nil {
			return g.stop(err)
		}
		if !reflect.DeepEqual(storage, c.review.Storage) {
			return g.stop(fmt.Errorf("selected storage identity changed before Flux provisioning"))
		}
	}
	for _, host := range c.compiled.Spec.Hosts {
		if _, err := c.claims.Renew(g.ctx, host); err != nil {
			return g.stop(err)
		}
	}
	attempt, err := c.writer.Begin(string(id))
	if err != nil {
		return g.stop(err)
	}
	g.mu.Lock()
	g.active = id
	g.mu.Unlock()
	g.reporter.Start(id)
	defer func() {
		g.reporter.Done(id, runErr)
		g.mu.Lock()
		g.active = ""
		g.mu.Unlock()
		if runErr != nil {
			runErr = joinCoordinatorErrors(runErr, c.writer.Uncertain(string(id), attempt))
			g.stop(runErr)
		}
	}()
	if err := g.CheckExecution(g.ctx); err != nil {
		return err
	}
	if err := run(); err != nil {
		return fmt.Errorf("platform stage %s stopped; inspect its outcome", id)
	}
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if err := g.CheckExecution(g.ctx); err != nil {
		return err
	}
	after, err := SnapshotPlatform(g.ctx, c.compiled.Init.Repo)
	if err != nil {
		return err
	}
	if err := checkPlatformDelta(g.ctx, g.snapshot, after, id, c.review.Prepared); err != nil {
		return err
	}
	observation, err := g.engine.Observe(g.ctx, g.execution, id)
	if err != nil {
		return fmt.Errorf("platform stage %s postcondition is not confirmed", id)
	}
	if err := checkPlatformObservation(id, observation); err != nil {
		return err
	}
	if err := g.CheckExecution(g.ctx); err != nil {
		return err
	}
	confirmed, err := SnapshotPlatform(g.ctx, c.compiled.Init.Repo)
	if err != nil || !reflect.DeepEqual(confirmed, after) {
		return fmt.Errorf("Fleet changed while verifying the platform effect")
	}
	proof := PlatformReceipt{Binding: platformBinding(r), Stage: string(id), Attempt: attempt, ReviewSHA256: g.reference, PriorReceipt: g.prior, ObservedAt: time.Now().UTC(), Snapshot: after, Observation: observation}
	hash, err := c.writer.savePlatformArtifact(proof)
	if err != nil {
		return err
	}
	if err := c.writer.completePlatform(string(id), attempt, hash, false); err != nil {
		return err
	}
	g.snapshot, g.prior = after, hash
	return nil
}

func (g *platformExecutor) stop(err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure == nil {
		g.failure = err
		g.cancel()
	}
	return err
}

func (g *platformExecutor) executorLost() {
	g.coordinator.executorUncertain.Store(true)
	w := g.coordinator.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	next := w.record
	next.ExecutorUncertain = true
	if err := w.publish(next); err != nil {
		w.poisoned = true
	}
}

func (w *SessionWriter) completePlatform(stage string, attempt int, hash string, inspection bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	source := "platform"
	if inspection {
		source = "platform-inspection"
	}
	e := SessionEvent{Stage: stage, Attempt: attempt, State: "succeeded", Source: source, At: time.Now().UTC(), EvidenceSHA256: hash}
	proof, err := loadPlatformReceipt(w.root, w.record, e)
	if err != nil {
		return err
	}
	prior := ""
	if latest, ok := latestPlatformReceipt(w.record); ok {
		prior = latest.EvidenceSHA256
	}
	if proof.PriorReceipt != prior {
		return fmt.Errorf("platform receipt predecessor changed before completion")
	}
	return w.finish(stage, attempt, "succeeded", source, hash)
}
