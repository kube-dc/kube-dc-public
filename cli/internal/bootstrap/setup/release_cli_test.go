package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCLIArtifactBytesBindsRunningBinary(t *testing.T) {
	s, dir := fixture(t)
	binary := filepath.Join(dir, "kube-dc")
	data := []byte("release-candidate-cli")
	if err := os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	record := completeReleaseRecord(s)
	record.Artifacts.CLI.SHA256 = hex.EncodeToString(hash[:])
	writeReleaseTestRecord(t, s, record)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := CheckCLIArtifactBytes(c, report, binary)
	if err != nil || checked.State != "recorded" || !checked.RecordComplete || checked.ReadyToApply || hasReleaseBlocker(checked, "artifacts.cliBytes") {
		t.Fatalf("matching CLI bytes rejected or authorized apply: %+v, %v", checked, err)
	}
	if err := os.WriteFile(binary, []byte("different-cli"), 0700); err != nil {
		t.Fatal(err)
	}
	checked, err = CheckCLIArtifactBytes(c, report, binary)
	if err != nil || checked.State != "blocked" || !checked.RecordComplete || !hasReleaseBlocker(checked, "artifacts.cliBytes") {
		t.Fatalf("changed CLI bytes accepted: %+v, %v", checked, err)
	}
	if strings.Contains(checked.Checks[len(checked.Checks)-1].Detail, binary) {
		t.Fatal("release report exposed local CLI path")
	}
	checked, err = CheckCLIArtifactBytes(c, report, filepath.Join(dir, "missing"))
	if err != nil || !hasReleaseBlocker(checked, "artifacts.cliBytes") {
		t.Fatalf("unreadable CLI accepted: %+v, %v", checked, err)
	}
	record.ReleaseID = "changed"
	writeReleaseTestRecord(t, s, record)
	if _, err := CheckCLIArtifactBytes(c, report, binary); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("release record drift accepted: %v", err)
	}
}
