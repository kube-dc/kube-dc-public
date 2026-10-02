package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveReadinessReportUsesPrivateAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reports", "latest.json")
	report := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, State: "blocked", InputHash: "input", PlanHash: "plan",
		Findings: []ReadinessFinding{{ID: "host.server-1.host-key", Status: "blocked", Detail: "key mismatch"}}}
	if err := SaveReadinessReport(path, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("report mode = %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("new report directory mode = %v, %v", info, err)
	}
	report.Findings[0].Detail = "fixed"
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveReadinessReport(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ReadinessReport
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Findings[0].Detail != "fixed" {
		t.Fatalf("replacement report invalid: %+v, %v", decoded, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("replacement mode = %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary report remained: %v, %v", entries, err)
	}
}

func TestSaveReadinessReportRejectsSymlinkAndIncompleteReport(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "report.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	report := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, InputHash: "input", PlanHash: "plan"}
	if err := SaveReadinessReport(link, report); err == nil {
		t.Fatal("symlink report destination accepted")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	if err := SaveReadinessReport(filepath.Join(dir, "incomplete.json"), ReadinessReport{}); err == nil {
		t.Fatal("incomplete report accepted")
	}
}
