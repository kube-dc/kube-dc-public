package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

const maxPlatformProofBytes = 4 << 20

type PlatformBinding struct {
	SessionID     string `json:"sessionId"`
	InputHash     string `json:"inputHash"`
	ReviewHash    string `json:"reviewHash"`
	ReleaseSHA256 string `json:"releaseSHA256"`
	PlanHash      string `json:"planHash"`
}

type PlatformReview struct {
	SchemaVersion int              `json:"schemaVersion"`
	Binding       PlatformBinding  `json:"binding"`
	CreatedAt     time.Time        `json:"createdAt"`
	PriorReceipt  string           `json:"priorReceipt,omitempty"`
	ChildSHA256   string           `json:"childSHA256"`
	Cluster       ClusterClaim     `json:"cluster"`
	Snapshot      PlatformSnapshot `json:"snapshot"`
	Effects       []StagePreview   `json:"effects"`
	Hash          string           `json:"hash"`
}

// EffectCheck stores only a stable check ID and a non-secret result hash.
// Live responses, resource bodies, credentials, and error text are excluded.
type EffectCheck struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type PlatformObservation struct {
	Verified bool          `json:"verified"`
	Checks   []EffectCheck `json:"checks"`
}

type PlatformReceipt struct {
	Binding      PlatformBinding     `json:"binding"`
	Stage        string              `json:"stage"`
	Attempt      int                 `json:"attempt"`
	ReviewSHA256 string              `json:"reviewSHA256"`
	PriorReceipt string              `json:"priorReceipt,omitempty"`
	ObservedAt   time.Time           `json:"observedAt"`
	Snapshot     PlatformSnapshot    `json:"snapshot"`
	Observation  PlatformObservation `json:"observation"`
}

func fingerprintJSON(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func platformBinding(r SessionRecord) PlatformBinding {
	return PlatformBinding{r.ID, r.InputHash, r.ReviewHash, r.ReleaseSHA256, r.PlanHash}
}
func platformReviewHash(r PlatformReview) string { r.Hash = ""; return fingerprintJSON(r) }

func safePlatformPath(name string) bool {
	return name != "" && name != "." && !strings.HasPrefix(name, "/") && !strings.Contains(name, "\\") && !strings.ContainsAny(name, "\r\n\x00") && !strings.Contains("/"+name+"/", "/../") && !strings.Contains("/"+name+"/", "/./")
}

func readPlatformArtifact(root *os.Root, hash string, dest any) error {
	if !validSessionHash(hash) {
		return fmt.Errorf("invalid platform evidence hash")
	}
	name := "platform-" + hash + ".json"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxPlatformProofBytes {
		return fmt.Errorf("platform evidence must remain bounded and private")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("platform evidence changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxPlatformProofBytes+1))
	if err != nil || len(body) > maxPlatformProofBytes {
		return fmt.Errorf("platform evidence exceeds its limit")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("platform evidence hash changed")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dest); err != nil {
		return fmt.Errorf("invalid platform evidence JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("platform evidence has trailing data")
	}
	return nil
}

func (w *SessionWriter) savePlatformArtifact(value any) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return "", err
	}
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > maxPlatformProofBytes {
		return "", fmt.Errorf("platform evidence exceeds its limit")
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	name := "platform-" + hash + ".json"
	f, err := w.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(body)
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		w.poisoned = true
		return "", fmt.Errorf("platform proof could not be persisted")
	}
	if err := w.syncDirectory(w.root); err != nil {
		w.poisoned = true
		return "", fmt.Errorf("platform proof directory sync failed")
	}
	return hash, nil
}

func validatePlatformObservation(o PlatformObservation) error {
	if !o.Verified || len(o.Checks) == 0 || len(o.Checks) > 256 {
		return fmt.Errorf("platform effect has no verified live postconditions")
	}
	seen := map[string]bool{}
	for _, c := range o.Checks {
		if !safePlatformPath(c.ID) || len(c.ID) > 128 || seen[c.ID] || !validSessionHash(c.SHA256) {
			return fmt.Errorf("invalid platform postcondition")
		}
		seen[c.ID] = true
	}
	return nil
}

func loadPlatformReceipt(root *os.Root, r SessionRecord, e SessionEvent) (PlatformReceipt, error) {
	var proof PlatformReceipt
	if err := readPlatformArtifact(root, e.EvidenceSHA256, &proof); err != nil {
		return proof, err
	}
	if proof.Binding != platformBinding(r) || proof.Stage != e.Stage || proof.Attempt != e.Attempt || proof.ObservedAt.Before(r.CreatedAt) || proof.ObservedAt.After(e.At) {
		return proof, fmt.Errorf("platform receipt binding changed")
	}
	if err := validatePlatformObservation(proof.Observation); err != nil {
		return proof, err
	}
	known := false
	for _, hash := range r.PlatformReviews {
		known = known || hash == proof.ReviewSHA256
	}
	if !known {
		return proof, fmt.Errorf("platform receipt has no retained review")
	}
	return proof, nil
}

func latestPlatformReceipt(r SessionRecord) (SessionEvent, bool) {
	for i := len(r.Events) - 1; i >= 0; i-- {
		e := r.Events[i]
		if e.Source == "platform" || e.Source == "platform-inspection" {
			return e, true
		}
	}
	return SessionEvent{}, false
}

func validatePlatformEvidence(root *os.Root, r SessionRecord) error {
	if len(r.PlatformReviews) > 64 {
		return fmt.Errorf("too many platform reviews")
	}
	seen := map[string]bool{}
	for _, hash := range r.PlatformReviews {
		var review PlatformReview
		if seen[hash] {
			return fmt.Errorf("duplicate platform review")
		}
		seen[hash] = true
		if err := readPlatformArtifact(root, hash, &review); err != nil {
			return err
		}
		if review.SchemaVersion != 1 || review.Binding != platformBinding(r) || review.Hash != platformReviewHash(review) || review.CreatedAt.Before(r.CreatedAt) || review.CreatedAt.After(time.Now().Add(time.Minute)) || !validSessionHash(review.ChildSHA256) || validateClusterClaim(review.Cluster, true) != nil {
			return fmt.Errorf("platform review binding changed")
		}
	}
	prior := ""
	for _, e := range r.Events {
		if e.Source != "platform" && e.Source != "platform-inspection" {
			continue
		}
		p, err := loadPlatformReceipt(root, r, e)
		if err != nil {
			return err
		}
		if p.PriorReceipt != prior {
			return fmt.Errorf("platform receipt chain changed")
		}
		prior = e.EvidenceSHA256
	}
	return nil
}

func (w *SessionWriter) retainPlatformReview(review PlatformReview) (string, error) {
	hash, err := w.savePlatformArtifact(review)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.record.PlatformReviews) >= 64 {
		return "", fmt.Errorf("platform review limit reached")
	}
	next := w.record
	next.PlatformReviews = append(append([]string{}, next.PlatformReviews...), hash)
	return hash, w.publish(next)
}

// RefreshPlatformReview preserves the original safety review and all earlier
// proof hashes. Only the latest accepted receipt may explain changed state.
func (c *SessionCoordinator) RefreshPlatformReview(ctx context.Context) (PlatformReview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result PlatformReview
	if c.closed {
		return result, fmt.Errorf("session coordinator is closed")
	}
	r, err := c.writer.Snapshot()
	if err != nil {
		return result, err
	}
	if len(SummarizeSession(r).NeedsInspection) != 0 {
		return result, fmt.Errorf("inspect uncertain effects before refreshing execution review")
	}
	handoff, err := verifiedSessionHandoff(c.writer.root, r)
	if err != nil {
		return result, err
	}
	e, ok := lastSessionEvent(r, clusterClaimStage)
	if !ok || e.State != "succeeded" || e.Source != "coordinator" {
		return result, fmt.Errorf("verified cluster reservation is required")
	}
	cluster, err := loadStoredStageProof(c.writer.root, r, e)
	if err != nil {
		return result, err
	}
	status := inspectSessionCluster(ctx, c.writer.directory, r, c.compiled, "", c.childClient())
	if status.State != "held" || !status.MatchesProof {
		return result, fmt.Errorf("cluster reservation no longer matches")
	}
	cluster.Cluster = status.Claim
	if err := clusterinit.VerifyPreparedScaffold(c.review.Prepared); err != nil {
		return result, err
	}
	snapshot, err := SnapshotPlatform(ctx, c.compiled.Init.Repo)
	if err != nil {
		return result, err
	}
	prior := ""
	if event, ok := latestPlatformReceipt(r); ok {
		receipt, err := loadPlatformReceipt(c.writer.root, r, event)
		if err != nil {
			return result, err
		}
		if !reflect.DeepEqual(snapshot, receipt.Snapshot) {
			return result, fmt.Errorf("Fleet changed outside the last verified effect; inspect before review")
		}
		prior = event.EvidenceSHA256
	} else {
		if snapshot.Git != c.review.Git {
			return result, fmt.Errorf("Fleet differs from the original reviewed revision")
		}
		if err := RecheckGitBinding(ctx, c.review.Git); err != nil {
			return result, err
		}
		current, err := clusterinit.SnapshotScaffoldSource(c.compiled.Init.Repo, c.review.Prepared.SiteComponents...)
		if err != nil || !reflect.DeepEqual(current, c.review.Prepared.SourceFiles) {
			return result, fmt.Errorf("Fleet source changed before execution review")
		}
	}
	result = PlatformReview{SchemaVersion: 1, Binding: platformBinding(r), CreatedAt: time.Now().UTC(), PriorReceipt: prior, ChildSHA256: handoff.ChildSHA256, Cluster: *cluster.Cluster, Snapshot: snapshot}
	preview, err := BuildPreview(c.compiled)
	if err != nil {
		return PlatformReview{}, err
	}
	for _, stage := range preview.Stages {
		if len(requiredPlatformChecks(clusterinit.StepID(stage.ID))) > 0 {
			result.Effects = append(result.Effects, stage)
		}
	}
	result.Hash = platformReviewHash(result)
	_, err = c.writer.retainPlatformReview(result)
	return result, err
}
