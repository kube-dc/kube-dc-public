package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

type verifiedHandoffEvidence struct {
	Node rke2.VerifiedNode
	Path string
}
type sessionStageEvidence struct {
	SessionID     string            `json:"sessionId"`
	InputHash     string            `json:"inputHash"`
	ReviewHash    string            `json:"reviewHash"`
	ReleaseSHA256 string            `json:"releaseSHA256"`
	Stage         string            `json:"stage"`
	Attempt       int               `json:"attempt"`
	HostID        string            `json:"hostId"`
	Node          rke2.VerifiedNode `json:"node"`
	ChildPath     string            `json:"childPath,omitempty"`
	ChildSHA256   string            `json:"childSHA256,omitempty"`
	ObservedAt    time.Time         `json:"observedAt"`
}

// saveStageEvidence publishes bounded non-secret proof before completion. The
// hash in the session identifies this private report, not discarded memory.
func (w *SessionWriter) saveStageEvidence(stage string, attempt int, host Host, node rke2.VerifiedNode, childPath string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return "", err
	}
	last, ok := lastSessionEvent(w.record, stage)
	if !ok || last.State != "running" || last.Attempt != attempt {
		return "", fmt.Errorf("stage evidence has no running attempt")
	}
	if node.Name != host.ID || node.Role != host.Role || node.InternalIP != host.ManagementAddress || node.UID == "" || len(node.UID) > 128 {
		return "", fmt.Errorf("stage proof differs from verified host")
	}
	target := SessionTarget{}
	for _, t := range w.record.Targets {
		if t.ID == host.ID {
			target = t
		}
	}
	if target.ID == "" {
		return "", fmt.Errorf("stage proof target is not reviewed")
	}
	if childPath != "" {
		if stage != "fetch-kubeconfig" || filepath.Clean(childPath) != filepath.Join(w.directory, "child-bootstrap.kubeconfig") {
			return "", fmt.Errorf("stage proof child path changed")
		}
		info, err := w.root.Lstat("child-bootstrap.kubeconfig")
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", fmt.Errorf("stage proof needs a private child config")
		}
	}
	if err := checkStageProofTarget(w.record, stage, host.ID, node, childPath); err != nil {
		return "", err
	}
	proof := sessionStageEvidence{SessionID: w.record.ID, InputHash: w.record.InputHash, ReviewHash: w.record.ReviewHash, ReleaseSHA256: w.record.ReleaseSHA256, Stage: stage, Attempt: attempt, HostID: host.ID, Node: node, ChildPath: childPath, ObservedAt: time.Now().UTC()}
	if childPath != "" {
		hash, err := privateChildFileHash(w.root)
		if err != nil {
			return "", err
		}
		proof.ChildSHA256 = hash
	}
	body, err := json.Marshal(proof)
	if err != nil || len(body) > 64<<10 {
		return "", fmt.Errorf("stage proof exceeds limit")
	}
	body = append(body, '\n')
	h := sha256.Sum256(body)
	hash := hex.EncodeToString(h[:])
	name := "evidence-" + hash + ".json"
	file, err := w.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		w.poisoned = true
		return "", fmt.Errorf("create stage proof failed")
	}
	_, writeErr := file.Write(body)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		w.poisoned = true
		return "", fmt.Errorf("persist stage proof failed")
	}
	if err := w.syncDirectory(w.root); err != nil {
		w.poisoned = true
		return "", fmt.Errorf("sync stage proof failed")
	}
	return hash, nil
}

// Coordinator completions are distinguished from externally supplied generic
// inspection hashes. Their private proof must remain available and bound.
func (w *SessionWriter) completeVerified(stage string, attempt int, hash string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	event := SessionEvent{Stage: stage, Attempt: attempt, State: "succeeded", Source: "coordinator", At: time.Now().UTC(), EvidenceSHA256: hash}
	if err := validateStoredStageProof(w.root, w.record, event); err != nil {
		return err
	}
	return w.finish(stage, attempt, "succeeded", "coordinator", hash)
}
func validateStoredStageEvidence(root *os.Root, record SessionRecord) error {
	for _, event := range record.Events {
		if event.Source == "coordinator" {
			if err := validateStoredStageProof(root, record, event); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateStoredStageProof(root *os.Root, record SessionRecord, event SessionEvent) error {
	if !validSessionHash(event.EvidenceSHA256) {
		return fmt.Errorf("invalid stored proof hash")
	}
	name := "evidence-" + event.EvidenceSHA256 + ".json"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return fmt.Errorf("verified stage needs its bounded private proof")
	}
	f, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open stored proof failed")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("stored proof changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return fmt.Errorf("stored proof exceeds read limit")
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != event.EvidenceSHA256 {
		return fmt.Errorf("stored proof hash changed")
	}
	var proof sessionStageEvidence
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&proof); err != nil {
		return fmt.Errorf("invalid stored proof")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("stored proof has trailing data")
	}
	if proof.SessionID != record.ID || proof.InputHash != record.InputHash || proof.ReviewHash != record.ReviewHash || proof.ReleaseSHA256 != record.ReleaseSHA256 || proof.Stage != event.Stage || proof.Attempt != event.Attempt || proof.ObservedAt.IsZero() || proof.ObservedAt.After(event.At) {
		return fmt.Errorf("stored proof binding changed")
	}
	if err := checkStageProofTarget(record, proof.Stage, proof.HostID, proof.Node, proof.ChildPath); err != nil {
		return err
	}
	if proof.ChildPath != "" {
		directory, err := filepath.Abs(root.Name())
		if err != nil || proof.ChildPath != filepath.Join(directory, "child-bootstrap.kubeconfig") {
			return fmt.Errorf("stored proof child directory changed")
		}
		hash, err := privateChildFileHash(root)
		if err != nil || hash != proof.ChildSHA256 {
			return fmt.Errorf("stored proof child config changed or is unavailable")
		}
	} else if proof.ChildSHA256 != "" {
		return fmt.Errorf("node proof has unexpected child bytes")
	}
	return nil
}
func checkStageProofTarget(record SessionRecord, stage, hostID string, node rke2.VerifiedNode, childPath string) error {
	var planned SessionStage
	for _, s := range record.Stages {
		if s.ID == stage {
			planned = s
		}
	}
	target := false
	for _, h := range record.Targets {
		target = target || h.ID == hostID
	}
	if !target || node.Name != hostID || node.UID == "" || len(node.UID) > 128 || net.ParseIP(node.InternalIP) == nil || (node.Role != "server" && node.Role != "agent") {
		return fmt.Errorf("stored proof node identity is invalid")
	}
	if stage == "fetch-kubeconfig" {
		if childPath == "" || !filepath.IsAbs(childPath) || filepath.Base(childPath) != "child-bootstrap.kubeconfig" || node.Role != "server" {
			return fmt.Errorf("child stage proof has no explicit bootstrap config")
		}
	} else if (stage != "rke2-first-server" && !strings.HasPrefix(stage, "rke2-join-")) || planned.Target != hostID || childPath != "" || (stage == "rke2-first-server" && node.Role != "server") {
		return fmt.Errorf("stored proof stage target changed")
	}
	return nil
}

func privateChildFileHash(root *os.Root) (string, error) {
	info, err := root.Lstat("child-bootstrap.kubeconfig")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxChildKubeconfigBytes {
		return "", fmt.Errorf("child proof needs a bounded private config")
	}
	file, err := root.Open("child-bootstrap.kubeconfig")
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("child config changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxChildKubeconfigBytes+1))
	if err != nil || len(body) > maxChildKubeconfigBytes {
		return "", fmt.Errorf("child config exceeds limit")
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:]), nil
}
