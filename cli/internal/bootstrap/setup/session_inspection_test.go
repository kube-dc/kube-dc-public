package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type historicalClaimSSH struct {
	ports.CappedSSHHostKeyClient
	claim HostClaim
	pin   string
	reads int
}

func (s *historicalClaimSSH) RunCappedWithHostKey(context.Context, ports.SSHHost, string, int) ([]byte, ports.SSHHostKeyEvidence, error) {
	s.reads++
	c := s.claim
	body := fmt.Sprintf("KDC_CLAIM_V1\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%d\n%s\n%d\n%s\n", c.MachineID, c.BootID, c.SessionID, c.RunID, c.InputHash, c.ReviewHash, c.ReleaseSHA256, c.ExpiresTick, c.State, c.ObservedTick, c.ObservedBootID)
	return []byte(body), ports.SSHHostKeyEvidence{FingerprintSHA256: s.pin}, nil
}

func TestHistoricalSessionRemainsInspectableWithoutMutationAuthority(t *testing.T) {
	c, hosts := coordinatorFixture(t)
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	record, _ := c.writer.Snapshot()
	preview, err := BuildPreview(c.compiled)
	if err != nil {
		t.Fatal(err)
	}
	filtered := []StagePreview{}
	record.Stages = nil
	for _, s := range preview.Stages {
		if s.ID != clusterClaimStage && s.ID != string(clusterinit.StepPlatformAccept) {
			if s.ID == string(clusterinit.StepReconcile) {
				s.PotentialWrite = false
			}
			filtered = append(filtered, s)
			record.Stages = append(record.Stages, SessionStage{s.ID, s.Target, s.PotentialWrite})
		}
	}
	preview.Stages, preview.PlanHash = filtered, ""
	body, _ := json.Marshal(preview)
	digest := sha256.Sum256(body)
	record.PlanHash = hex.EncodeToString(digest[:])
	if err := c.writer.publish(record); err != nil {
		t.Fatal(err)
	}
	if err := c.writer.Close(); err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.compiled.Spec.Hosts)
	ssh := &historicalClaimSSH{claim: hosts.remote[primary.ID], pin: primary.HostKeySHA256}
	status, err := InspectSessionTargets(context.Background(), c.writer.directory, ssh)
	if err != nil || ssh.reads != 1 || status.Targets[0].State != "reserved" || status.ReadyToApply || status.Targets[0].CurrentRun {
		t.Fatalf("historical inspection failed or restored authority: %+v %v", status, err)
	}
	if _, _, err := loadSessionInputs(record); err == nil {
		t.Fatal("historical inspection permitted mutation matching")
	}
	for _, kind := range []string{"plan", "stage", "target", "input", "review"} {
		t.Run(kind, func(t *testing.T) {
			copyBody, _ := json.Marshal(record)
			var edited SessionRecord
			_ = json.Unmarshal(copyBody, &edited)
			switch kind {
			case "plan":
				edited.PlanHash = strings.Repeat("0", 64)
			case "stage":
				edited.Stages[0].PotentialWrite = !edited.Stages[0].PotentialWrite
			case "target":
				edited.Targets[0].MachineID = strings.Repeat("b", 32)
			case "input":
				edited.InputHash = strings.Repeat("0", 64)
			case "review":
				edited.ReviewHash = strings.Repeat("0", 64)
			}
			if err := matchSessionInspectionRecord(edited, c.compiled, c.review); err == nil {
				t.Fatal("unrecognized historical binding accepted")
			}
		})
	}
}
