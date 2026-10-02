package setup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func completeReleaseRecord(s Spec) InstallerReleaseRecord {
	digest := strings.Repeat("a", 64)
	return InstallerReleaseRecord{
		SchemaVersion: ReleaseRecordSchemaVersion,
		Kind:          "kube-dc-installer-release", ReleaseID: "test-release",
		Artifacts: ReleaseArtifacts{
			CLI:        ReleaseFile{Version: "v1", SHA256: digest},
			StarterRef: s.Release.StarterRef, RKE2Version: s.Release.RKE2Version,
			PlatformChart: ReleaseFile{Version: "v1", SHA256: digest},
			BackendImage:  "example.test/backend@sha256:" + digest,
			FrontendImage: "example.test/frontend@sha256:" + digest,
			AdminImage:    "example.test/admin@sha256:" + digest,
			ThemeArchives: map[string]string{"kube-dc-theme.jar": digest, "kube-dc-theme-provider.jar": digest},
		},
		Profiles: []ReleaseProfile{{
			ID: s.Profile, SupportedOS: []string{"ubuntu:24.04"}, Architectures: []string{"x86_64"},
			MinServers: 1, MaxServers: 1, MinAgents: 0, MaxAgents: 0,
			MinCPUPerHost: 4, MinMemoryKiBPerHost: 8 << 20, MinFreeBytesPerHost: 80 << 30,
			NetworkPresets: []string{"internal-only"}, ObjectStorageModes: []string{"disabled"},
			RequiredTools: []string{"kubectl", "helm"}, RequiredCapabilities: []string{"containers"},
			AcceptanceChecks: []string{"platform-ready"}, Availability: "Single host; no host failover.",
			Qualification: ReleaseQualification{RunID: "synthetic-test", Result: "passed", EvidenceSHA256: digest},
		}},
	}
}

func writeReleaseTestRecord(t *testing.T, s Spec, record InstallerReleaseRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Release.RecordFile, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func hasReleaseBlocker(report ReleaseReport, id string) bool {
	for _, check := range report.Checks {
		if check.ID == id && check.Status == "blocked" {
			return true
		}
	}
	return false
}

func TestInspectReleaseRejectsUIOnlyRecord(t *testing.T) {
	s, _ := fixture(t)
	if err := os.WriteFile(s.Release.RecordFile, []byte(`{"ui":{"version":"v1"},"backendImage":"example.test/backend@sha256:a"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || report.RecordComplete || report.ReadyToApply || !hasReleaseBlocker(report, "kind") || !hasReleaseBlocker(report, "artifacts.cli") || !hasReleaseBlocker(report, "profiles.selected") {
		t.Fatalf("UI rollout record accepted: %+v", report)
	}
}

func TestInspectReleaseChecksBindingsAndNeverAuthorizesApply(t *testing.T) {
	s, _ := fixture(t)
	record := completeReleaseRecord(s)
	writeReleaseTestRecord(t, s, record)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "recorded" || !report.RecordComplete || report.ReadyToApply || len(report.Unresolved) == 0 {
		t.Fatalf("complete local record result = %+v", report)
	}
	record.Artifacts.StarterRef = "oci://example.test/other@sha256:" + strings.Repeat("b", 64)
	record.Profiles[0].RequiredCapabilities = []string{"managed-services"}
	writeReleaseTestRecord(t, s, record)
	c, err = Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err = InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReleaseBlocker(report, "artifacts.starterRef") || !hasReleaseBlocker(report, "profile.capabilities") {
		t.Fatalf("binding mismatch accepted: %+v", report)
	}
}

func TestInspectReleaseRejectsMalformedImageReferences(t *testing.T) {
	for _, bad := range []string{
		"example.test/backend:old:new@sha256:" + strings.Repeat("a", 64),
		"example.test/backend//child@sha256:" + strings.Repeat("a", 64),
		"example.test/backend:old@sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(bad[:strings.Index(bad, "@")], func(t *testing.T) {
			s, _ := fixture(t)
			record := completeReleaseRecord(s)
			record.Artifacts.BackendImage = bad
			writeReleaseTestRecord(t, s, record)
			c, err := Compile(s)
			if err != nil {
				t.Fatal(err)
			}
			report, err := InspectRelease(c)
			if err != nil {
				t.Fatal(err)
			}
			if report.RecordComplete || !hasReleaseBlocker(report, "artifacts.backendImage") {
				t.Fatalf("malformed image accepted: %+v", report)
			}
		})
	}
}

func TestInspectReleaseRejectsDriftAndUnknownFields(t *testing.T) {
	s, _ := fixture(t)
	record := completeReleaseRecord(s)
	writeReleaseTestRecord(t, s, record)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	record.ReleaseID = "changed"
	writeReleaseTestRecord(t, s, record)
	if _, err := InspectRelease(c); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("record drift accepted: %v", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
	if err := os.WriteFile(s.Release.RecordFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReleaseBlocker(report, "schema-fields") {
		t.Fatalf("unknown release field accepted: %+v", report)
	}
}

func TestCompileCapsReleaseRecord(t *testing.T) {
	s, _ := fixture(t)
	data := append([]byte(`{"unused":"`), []byte(strings.Repeat("x", maxReleaseRecordBytes))...)
	data = append(data, []byte(`"}`)...)
	if err := os.WriteFile(s.Release.RecordFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized release record accepted: %v", err)
	}
}

func TestReleaseProfileChecksObservedHostFloors(t *testing.T) {
	s, _ := fixture(t)
	writeReleaseTestRecord(t, s, completeReleaseRecord(s))
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	inventory := HostInventory{State: "observed", InputHash: c.InputHash, Hosts: []HostObservation{{
		ID: "server-1", State: "observed",
		Facts: HostFacts{OSID: "ubuntu", OSVersion: "24.04", Architecture: "x86_64", CPUs: 8, MemoryKiB: 16 << 20, FreeBytes: 100 << 30},
	}}}
	got, err := applyReleaseHostPolicy(c, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "observed" || len(got.Hosts[0].Checks) != 5 {
		t.Fatalf("supported host was blocked: %+v", got)
	}
	for _, check := range got.Hosts[0].Checks {
		if check.Status != "pass" {
			t.Fatalf("supported host failed %s: %+v", check.ID, check)
		}
	}
	inventory.Hosts[0].Facts.OSVersion = "22.04"
	inventory.Hosts[0].Facts.CPUs = 2
	inventory.Hosts[0].Facts.FreeBytes = 20 << 30
	got, err = applyReleaseHostPolicy(c, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "blocked" || got.Hosts[0].State != "blocked" || got.ReadyToInstall {
		t.Fatalf("undersized host accepted: %+v", got)
	}
	blocked := map[string]bool{}
	for _, check := range got.Hosts[0].Checks {
		if check.Status == "blocked" {
			blocked[check.ID] = true
		}
	}
	for _, id := range []string{"profile-os", "profile-cpu", "profile-free-space"} {
		if !blocked[id] {
			t.Fatalf("missing blocker %s: %+v", id, got.Hosts[0].Checks)
		}
	}
}
