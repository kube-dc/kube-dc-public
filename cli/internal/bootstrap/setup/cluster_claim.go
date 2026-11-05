package setup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coordinationclient "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

const clusterClaimStage = "claim-child-cluster"
const clusterClaimName = "kube-dc-installer"
const clusterClaimNamespace = "kube-system"
const clusterClaimPrefix = "kube-dc.com/setup-"

// ClusterClaim is non-secret evidence for one exact Lease instance. It is
// cooperative exclusion, not server-enforced fencing of arbitrary API writes.
// A fresh invocation cannot recover authority from this saved identity.
type ClusterClaim struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	SessionID       string `json:"sessionId"`
	RunID           string `json:"runId"`
	InputHash       string `json:"inputHash"`
	ReviewHash      string `json:"reviewHash"`
	ReleaseSHA256   string `json:"releaseSHA256"`
	PrimaryNodeUID  string `json:"primaryNodeUid"`
	HandoffID       string `json:"handoffId,omitempty"`
	SourceRunID     string `json:"sourceRunId,omitempty"`
	HandoffEpoch    int64  `json:"handoffEpoch,omitempty"`
}

type ClusterClaimStatus struct {
	State          string        `json:"state"`
	CurrentRun     bool          `json:"currentRun"`
	MatchesSession bool          `json:"matchesSession"`
	MatchesProof   bool          `json:"matchesProof"`
	Detail         string        `json:"detail"`
	Claim          *ClusterClaim `json:"claim,omitempty"`
}

type childClusterClientFactory func(context.Context, Compiled, string, rke2.VerifiedNode) (kubernetes.Interface, error)

func verifiedChildClusterClient(ctx context.Context, c Compiled, path string, node rke2.VerifiedNode) (kubernetes.Interface, error) {
	config, err := VerifyDirectChildBootstrapAPI(ctx, c, path, node)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func expectedClusterClaim(record SessionRecord, run string, node rke2.VerifiedNode) ClusterClaim {
	return ClusterClaim{Namespace: clusterClaimNamespace, Name: clusterClaimName, SessionID: record.ID, RunID: run,
		InputHash: record.InputHash, ReviewHash: record.ReviewHash, ReleaseSHA256: record.ReleaseSHA256, PrimaryNodeUID: node.UID}
}

func claimIdentityText(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) == -1
}

func validateClusterClaim(c ClusterClaim, instance bool) error {
	if c.Namespace != clusterClaimNamespace || c.Name != clusterClaimName || !claimIdentityText(c.PrimaryNodeUID) {
		return fmt.Errorf("invalid cluster claim target")
	}
	for _, hash := range []string{c.SessionID, c.RunID, c.InputHash, c.ReviewHash, c.ReleaseSHA256} {
		if !validSessionHash(hash) {
			return fmt.Errorf("invalid cluster claim owner")
		}
	}
	if instance && (!claimIdentityText(c.UID) || !claimIdentityText(c.ResourceVersion)) {
		return fmt.Errorf("cluster claim has no API instance identity")
	}
	if (c.HandoffID != "" || c.SourceRunID != "") && (!validSessionHash(c.HandoffID) || !validSessionHash(c.SourceRunID)) {
		return fmt.Errorf("invalid cluster handoff identity")
	}
	if c.HandoffEpoch < 0 || c.HandoffEpoch > 65535 || (c.HandoffID == "" && c.HandoffEpoch != 0) {
		return fmt.Errorf("invalid cluster handoff epoch")
	}
	return nil
}

func clusterClaimAnnotations(c ClusterClaim) map[string]string {
	return map[string]string{
		clusterClaimPrefix + "schema": "1", clusterClaimPrefix + "session": c.SessionID,
		clusterClaimPrefix + "run": c.RunID, clusterClaimPrefix + "input": c.InputHash,
		clusterClaimPrefix + "review": c.ReviewHash, clusterClaimPrefix + "release": c.ReleaseSHA256,
		clusterClaimPrefix + "primary-node": c.PrimaryNodeUID,
	}
}

func parseClusterClaim(lease *coordinationv1.Lease) (ClusterClaim, error) {
	if lease == nil || lease.DeletionTimestamp != nil || lease.Spec.HolderIdentity == nil ||
		lease.Labels["app.kubernetes.io/managed-by"] != "kube-dc-installer" || lease.Annotations[clusterClaimPrefix+"schema"] != "1" {
		return ClusterClaim{}, fmt.Errorf("cluster claim is not an active installer reservation")
	}
	a := lease.Annotations
	c := ClusterClaim{Namespace: lease.Namespace, Name: lease.Name, UID: string(lease.UID), ResourceVersion: lease.ResourceVersion,
		SessionID: a[clusterClaimPrefix+"session"], RunID: a[clusterClaimPrefix+"run"], InputHash: a[clusterClaimPrefix+"input"],
		ReviewHash: a[clusterClaimPrefix+"review"], ReleaseSHA256: a[clusterClaimPrefix+"release"], PrimaryNodeUID: a[clusterClaimPrefix+"primary-node"]}
	c.HandoffID, c.SourceRunID = a[clusterClaimPrefix+"handoff"], a[clusterClaimPrefix+"handoff-source"]
	if epoch, ok := a[clusterClaimPrefix+"handoff-epoch"]; ok {
		var err error
		c.HandoffEpoch, err = strconv.ParseInt(epoch, 10, 64)
		if err != nil {
			return c, fmt.Errorf("invalid cluster handoff epoch")
		}
	}
	if err := validateClusterClaim(c, true); err != nil {
		return c, err
	}
	if *lease.Spec.HolderIdentity != c.RunID {
		return c, fmt.Errorf("cluster claim holder differs from owner")
	}
	return c, nil
}

func sameClusterClaimOwner(a, b ClusterClaim) bool {
	a.UID, a.ResourceVersion, b.UID, b.ResourceVersion = "", "", "", ""
	return a == b
}

// acquireClusterClaim performs exactly one Create. Existing, expired, malformed,
// or ambiguously created reservations require inspection; none are overwritten.
// No duration is set: only a future reviewed recovery path can release this
// reservation. Renew timestamps are informational and never grant takeover.
func acquireClusterClaim(ctx context.Context, leases coordinationclient.LeaseInterface, expected ClusterClaim) (ClusterClaim, error) {
	if err := validateClusterClaim(expected, false); err != nil {
		return ClusterClaim{}, err
	}
	if err := ctx.Err(); err != nil {
		return ClusterClaim{}, err
	}
	now := metav1.NewMicroTime(time.Now().UTC())
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: expected.Name, Namespace: expected.Namespace,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "kube-dc-installer"}, Annotations: clusterClaimAnnotations(expected)},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &expected.RunID, AcquireTime: &now, RenewTime: &now}}
	created, err := leases.Create(ctx, lease, metav1.CreateOptions{})
	if err != nil {
		return ClusterClaim{}, fmt.Errorf("cluster reservation was not confirmed; inspect the Lease before retry")
	}
	claim, err := parseClusterClaim(created)
	if err != nil || !sameClusterClaimOwner(claim, expected) {
		return ClusterClaim{}, fmt.Errorf("cluster reservation response changed; inspect the Lease before retry")
	}
	if err := ctx.Err(); err != nil {
		return ClusterClaim{}, err
	}
	observed, err := leases.Get(ctx, clusterClaimName, metav1.GetOptions{})
	if err != nil {
		return ClusterClaim{}, fmt.Errorf("cluster reservation readback failed; inspect before retry")
	}
	checked, err := parseClusterClaim(observed)
	if err != nil || checked.UID != claim.UID || !sameClusterClaimOwner(checked, expected) {
		return ClusterClaim{}, fmt.Errorf("cluster reservation identity changed during readback")
	}
	return checked, ctx.Err()
}

func inspectClusterClaim(ctx context.Context, leases coordinationclient.LeaseInterface, expected ClusterClaim, proof *ClusterClaim, run string) ClusterClaimStatus {
	result := ClusterClaimStatus{State: "unknown", Detail: "Cluster ownership could not be read. Inspect API access and the Lease."}
	lease, err := leases.Get(ctx, clusterClaimName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		result.State, result.Detail = "absent", "The verified child API has no installer Lease. Saved effects still require inspection."
		return result
	}
	if err != nil {
		return result
	}
	claim, err := parseClusterClaim(lease)
	if err != nil {
		result.State, result.Detail = "conflict", "The installer Lease has an unknown format or owner. Retain host claims and inspect."
		return result
	}
	result.Claim = &claim
	if claim.HandoffID != "" {
		result.State, result.Detail = "handoff", "Executor handoff is pending. No installation mutation is authorized."
		return result
	}
	result.MatchesSession = sameClusterClaimOwner(claim, expected)
	result.MatchesProof = proof != nil && proof.UID == claim.UID && sameClusterClaimOwner(claim, *proof)
	if !result.MatchesSession || (proof != nil && !result.MatchesProof) {
		result.State, result.Detail = "conflict", "The installer Lease differs from the saved session or Lease instance. Retain claims and inspect."
		return result
	}
	result.State = "held"
	result.CurrentRun = result.MatchesProof && run != "" && claim.RunID == run
	result.Detail = "Cluster reservation observed. Host claims remain protected. Installation stays blocked."
	return result
}
