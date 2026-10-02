package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigHandoff receives the admin config in memory. A coordinator must
// persist it to a protected, explicit path and build the child cluster session.
// The host engine has no default path and never writes to ~/.kube/config.
type KubeconfigHandoff func(*clientcmdapi.Config) error

// RKE2HostOperations adapts the existing RKE2 engines to the guarded host
// phase. Constructing it does not authorize a write; RunHostPhase must first
// receive independently qualified readiness and a host ownership claim.
type RKE2HostOperations struct {
	compiled        Compiled
	ssh             ports.CappedSSHHostKeyClient
	kubeconfig      KubeconfigHandoff
	trustedCA       *rke2.TrustedCAMaterial
	podCIDR         string
	serviceCIDR     string
	clusterDNS      string
	verifyAttempts  int
	verifyDelay     time.Duration // test override only
	mu              sync.Mutex
	permits         map[string]hostWritePermit
	activeWrites    map[string]bool
	rechecks        map[string]uint64
	firstServerNode rke2.VerifiedNode
	firstVerified   bool
	firstGeneration uint64
	safetyReview    *SafetyReview
	cliPath         string
	claimCheck      func(context.Context, Host) error
	verifiedNodes   map[string]rke2.VerifiedNode
}

type hostWritePermit struct {
	host     Host
	reviewed HostObservation
	issuedAt time.Time
}

const maxHostWritePermitAge = 30 * time.Second

func NewRKE2HostOperations(c Compiled, ssh ports.CappedSSHHostKeyClient, handoff KubeconfigHandoff) (*RKE2HostOperations, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	if c.Spec.Target.Intent != clusterinit.ModeInstall || ssh == nil || handoff == nil {
		return nil, fmt.Errorf("RKE2 host engine needs install intent, bounded SSH, and a kubeconfig handoff")
	}
	for _, host := range c.Spec.Hosts {
		if host.HostKeySHA256 == "" || net.ParseIP(host.ManagementAddress) == nil {
			return nil, fmt.Errorf("host %s needs a pinned SSH key and selected management IP", host.ID)
		}
	}
	env, err := clusterinit.EnvMapFor(c.Init.Preset, c.Init.Sets)
	if err != nil {
		return nil, fmt.Errorf("resolve RKE2 network configuration: %w", err)
	}
	for _, key := range []string{"POD_CIDR", "SVC_CIDR", "CLUSTER_DNS"} {
		if env[key] == "" {
			return nil, fmt.Errorf("RKE2 network configuration has no %s", key)
		}
	}
	ca, err := rke2.LoadTrustedCAForNodes(c.Init.TrustedCABundle)
	if err != nil {
		return nil, err
	}
	if (ca == nil && c.Init.TrustedCAFingerprint != "") || (ca != nil && ca.Fingerprint != c.Init.TrustedCAFingerprint) {
		return nil, fmt.Errorf("trusted CA bundle changed since setup review")
	}
	return &RKE2HostOperations{compiled: c, ssh: ssh, kubeconfig: handoff, trustedCA: ca,
		podCIDR: env["POD_CIDR"], serviceCIDR: env["SVC_CIDR"], clusterDNS: env["CLUSTER_DNS"],
		permits:      make(map[string]hostWritePermit, len(c.Spec.Hosts)),
		activeWrites: make(map[string]bool, len(c.Spec.Hosts)),
		rechecks:     make(map[string]uint64, len(c.Spec.Hosts))}, nil
}

func (o *RKE2HostOperations) pinnedHost(host Host) ports.SSHHost {
	endpoint := sshHost(host.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
	return endpoint
}

func (o *RKE2HostOperations) selectedHost(host Host, role string) bool {
	if host.Role != role {
		return false
	}
	for _, selected := range o.compiled.Spec.Hosts {
		if selected == host {
			return true
		}
	}
	return false
}

func (o *RKE2HostOperations) selectedPrimary(host Host) bool {
	return o.selectedHost(host, "server") && host == primaryServer(o.compiled.Spec.Hosts)
}

func (o *RKE2HostOperations) RecheckHost(ctx context.Context, host Host, reviewed HostObservation) error {
	if !o.selectedHost(host, host.Role) {
		return fmt.Errorf("host differs from the reviewed setup")
	}
	o.mu.Lock()
	if o.activeWrites[host.ID] {
		o.mu.Unlock()
		return fmt.Errorf("host %s already has an active installation write", host.ID)
	}
	delete(o.verifiedNodes, host.ID)
	o.rechecks[host.ID]++
	generation := o.rechecks[host.ID]
	delete(o.permits, host.ID)
	o.mu.Unlock()
	if err := RecheckFreshHost(ctx, o.compiled, host, reviewed, o.ssh); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rechecks[host.ID] != generation {
		return fmt.Errorf("host %s recheck was superseded", host.ID)
	}
	o.permits[host.ID] = hostWritePermit{host: host, reviewed: reviewed, issuedAt: time.Now()}
	return nil
}

// takePermit makes a successful host observation one-use. A join takes its
// permit before it reads credentials, then rechecks immediately before writes.
func (o *RKE2HostOperations) takePermit(host Host) (hostWritePermit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.activeWrites[host.ID] {
		return hostWritePermit{}, fmt.Errorf("host %s already has an active installation write", host.ID)
	}
	permit, ok := o.permits[host.ID]
	delete(o.permits, host.ID)
	age := time.Since(permit.issuedAt)
	if !ok || permit.host != host || age < 0 || age > maxHostWritePermitAge {
		return hostWritePermit{}, fmt.Errorf("host %s needs a fresh one-use pre-write recheck", host.ID)
	}
	o.activeWrites[host.ID] = true
	return permit, nil
}

func (o *RKE2HostOperations) finishWrite(host Host) {
	o.mu.Lock()
	delete(o.activeWrites, host.ID)
	o.mu.Unlock()
}

func (o *RKE2HostOperations) recheckPermit(ctx context.Context, host Host, permit hostWritePermit) error {
	if age := time.Since(permit.issuedAt); age < 0 || age > maxHostWritePermitAge {
		return fmt.Errorf("host %s pre-write recheck expired", host.ID)
	}
	if o.safetyReview != nil {
		if err := RecheckReviewFiles(ctx, o.compiled, *o.safetyReview, o.cliPath); err != nil {
			return err
		}
		if o.claimCheck != nil {
			if err := o.claimCheck(ctx, host); err != nil {
				return err
			}
		}
		return RecheckSafetyHost(ctx, o.compiled, *o.safetyReview, host, o.ssh)
	}
	return RecheckFreshHost(ctx, o.compiled, host, permit.reviewed, o.ssh)
}

func (o *RKE2HostOperations) serverOptions(host Host) rke2.InstallOptions {
	return rke2.InstallOptions{
		SSH: o.ssh, Host: o.pinnedHost(host), NodeName: host.ID, Domain: o.compiled.Init.Domain,
		PodCIDR: o.podCIDR, ServiceCIDR: o.serviceCIDR, ClusterDNS: o.clusterDNS,
		NodeIP: host.ManagementAddress, RKE2Version: o.compiled.Spec.Release.RKE2Version,
		TrustedCA: o.trustedCA, Out: io.Discard, Artifacts: o.artifactsFor(host), BeforeConfigure: o.beforeConfigure(host),
	}
}

func (o *RKE2HostOperations) InstallFirstServer(ctx context.Context, host Host) error {
	if !o.selectedPrimary(host) {
		return fmt.Errorf("first server differs from the reviewed setup")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	permit, err := o.takePermit(host)
	if err != nil {
		return err
	}
	defer o.finishWrite(host)
	if err := o.recheckPermit(ctx, host, permit); err != nil {
		return err
	}
	return rke2.Install(ctx, o.serverOptions(host))
}

func (o *RKE2HostOperations) VerifyFirstServer(ctx context.Context, host Host) error {
	if !o.selectedPrimary(host) {
		return fmt.Errorf("first server differs from the reviewed setup")
	}
	o.mu.Lock()
	o.firstGeneration++
	generation := o.firstGeneration
	o.firstServerNode = rke2.VerifiedNode{}
	o.firstVerified = false
	o.mu.Unlock()
	node, err := o.verify(ctx, host, host, "server")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.firstGeneration != generation {
		return fmt.Errorf("first-server verification was superseded")
	}
	o.firstServerNode = node
	o.firstVerified = true
	if o.verifiedNodes == nil {
		o.verifiedNodes = map[string]rke2.VerifiedNode{}
	}
	o.verifiedNodes[host.ID] = node
	return nil
}

// FirstServerEvidence is available only after successful pinned service and
// API verification. The child API handoff must match its exact Node UID.
func (o *RKE2HostOperations) FirstServerEvidence() (rke2.VerifiedNode, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.firstVerified {
		return rke2.VerifiedNode{}, fmt.Errorf("first server has no verified node identity")
	}
	return o.firstServerNode, nil
}

func (o *RKE2HostOperations) JoinServer(ctx context.Context, primary, host Host) error {
	if !o.selectedPrimary(primary) || !o.selectedHost(host, "server") || host.ID == primary.ID {
		return fmt.Errorf("server join differs from the reviewed setup")
	}
	if err := rke2.CheckNodeAbsent(ctx, o.ssh, o.pinnedHost(primary), host.ID); err != nil {
		return err
	}
	permit, err := o.takePermit(host)
	if err != nil {
		return err
	}
	defer o.finishWrite(host)
	token, cpIP, err := rke2.ResolveJoinCredentials(ctx, o.ssh, o.pinnedHost(primary), "", primary.ManagementAddress, io.Discard)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.recheckPermit(ctx, host, permit); err != nil {
		return err
	}
	opts := o.serverOptions(host)
	opts.JoinToken, opts.JoinServer = token, cpIP
	return rke2.Install(ctx, opts)
}

func (o *RKE2HostOperations) VerifyJoinedServer(ctx context.Context, host Host) error {
	if !o.selectedHost(host, "server") || o.selectedPrimary(host) {
		return fmt.Errorf("joined server differs from the reviewed setup")
	}
	node, err := o.verify(ctx, primaryServer(o.compiled.Spec.Hosts), host, "server")
	return o.retainNode(ctx, host, node, err)
}

func (o *RKE2HostOperations) JoinWorker(ctx context.Context, primary, host Host) error {
	if !o.selectedPrimary(primary) || !o.selectedHost(host, "agent") {
		return fmt.Errorf("worker join differs from the reviewed setup")
	}
	if err := rke2.CheckNodeAbsent(ctx, o.ssh, o.pinnedHost(primary), host.ID); err != nil {
		return err
	}
	permit, err := o.takePermit(host)
	if err != nil {
		return err
	}
	defer o.finishWrite(host)
	if err := ctx.Err(); err != nil {
		return err
	}
	token, cpIP, err := rke2.ResolveJoinCredentials(ctx, o.ssh, o.pinnedHost(primary), "", primary.ManagementAddress, io.Discard)
	if err != nil {
		return err
	}
	if err := o.recheckPermit(ctx, host, permit); err != nil {
		return err
	}
	return rke2.JoinWorker(ctx, rke2.JoinWorkerOptions{
		SSH: o.ssh, Worker: o.pinnedHost(host), WorkerName: host.ID, WorkerIP: host.ManagementAddress,
		ControlPlane: o.pinnedHost(primary), JoinToken: token, CPHost: cpIP,
		RKE2Version: o.compiled.Spec.Release.RKE2Version, TrustedCA: o.trustedCA, Out: io.Discard, Artifacts: o.artifactsFor(host), BeforeConfigure: o.beforeConfigure(host),
	})
}

func (o *RKE2HostOperations) VerifyWorker(ctx context.Context, host Host) error {
	if !o.selectedHost(host, "agent") {
		return fmt.Errorf("worker differs from the reviewed setup")
	}
	node, err := o.verify(ctx, primaryServer(o.compiled.Spec.Hosts), host, "agent")
	return o.retainNode(ctx, host, node, err)
}

func (o *RKE2HostOperations) verify(ctx context.Context, primary, host Host, role string) (rke2.VerifiedNode, error) {
	return rke2.VerifyRegisteredNode(ctx, rke2.VerifyNodeOptions{
		SSH: o.ssh, APIHost: o.pinnedHost(primary), Node: o.pinnedHost(host),
		NodeName: host.ID, Role: role, InternalIP: host.ManagementAddress,
		Attempts: o.verifyAttempts, RetryDelay: o.verifyDelay,
	})
}

func (o *RKE2HostOperations) FetchKubeconfig(ctx context.Context, primary Host) error {
	if !o.selectedPrimary(primary) {
		return fmt.Errorf("kubeconfig source is not the reviewed first server")
	}
	cfg, err := clusterinit.FetchKubeconfig(ctx, clusterinit.FetchKubeconfigOptions{
		SSH: o.ssh, Host: o.pinnedHost(primary), ClusterName: o.compiled.Spec.Name,
		Domain: o.compiled.Init.Domain, Out: io.Discard,
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return o.kubeconfig(cfg)
}

// NewReviewedRKE2HostOperations binds consumers to an immutable copy of a
// saved review. RunHostPhase must still require readiness and a target claim.
func NewReviewedRKE2HostOperations(ctx context.Context, c Compiled, review SafetyReview, ssh ports.CappedSSHHostKeyClient, handoff KubeconfigHandoff, cliPath string) (*RKE2HostOperations, error) {
	if err := RecheckSafetyReview(ctx, c, review, ssh, cliPath); err != nil {
		return nil, err
	}
	ops, err := NewRKE2HostOperations(c, ssh, handoff)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(review)
	if err != nil {
		return nil, err
	}
	var copy SafetyReview
	if err := json.Unmarshal(body, &copy); err != nil {
		return nil, err
	}
	ops.safetyReview = &copy
	ops.cliPath = cliPath
	return ops, nil
}
func (o *RKE2HostOperations) artifactsFor(host Host) *rke2.ArtifactSet {
	if o.safetyReview == nil {
		return nil
	}
	observed, _ := reviewedHost(o.safetyReview.Hosts, host.ID)
	set := o.safetyReview.Artifacts.RKE2[architectureName(observed.Facts.Architecture)]
	return &set
}

func (o *RKE2HostOperations) beforeConfigure(host Host) func(context.Context) error {
	if o.safetyReview == nil {
		return nil
	}
	return func(ctx context.Context) error {
		if err := RecheckReviewFiles(ctx, o.compiled, *o.safetyReview, o.cliPath); err != nil {
			return err
		}
		if o.claimCheck != nil {
			if err := o.claimCheck(ctx, host); err != nil {
				return err
			}
		}
		return RecheckSafetyHost(ctx, o.compiled, *o.safetyReview, host, o.ssh)
	}
}

func (o *RKE2HostOperations) retainNode(ctx context.Context, host Host, node rke2.VerifiedNode, err error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.verifiedNodes, host.ID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.verifiedNodes == nil {
		o.verifiedNodes = map[string]rke2.VerifiedNode{}
	}
	o.verifiedNodes[host.ID] = node
	return nil
}
func (o *RKE2HostOperations) verifiedNode(host Host) (rke2.VerifiedNode, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	node, ok := o.verifiedNodes[host.ID]
	if !ok {
		return node, fmt.Errorf("host has no verified node evidence")
	}
	return node, nil
}
