package setup

import (
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestBuildReadinessCombinesBlockersAndOpenGates(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	release, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	hosts := HostInventory{InputHash: c.InputHash, State: "blocked", Hosts: []HostObservation{{
		ID: "server-1", State: "blocked", Checks: []HostCheck{{ID: "host-key", Status: "blocked", Detail: "fingerprint mismatch", NextAction: "Check the host key."}},
	}}, Unresolved: []string{"operator-confirmed-host-key-fingerprints"}}
	tools := []ToolFact{{Name: "kubectl", Status: "installed", Reason: "cluster checks"}, {Name: "helm", Status: "missing", Reason: "install charts", NextAction: "Install Helm."}}
	got, err := BuildReadiness(c, plan, release, tools, &hosts)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "blocked" || got.ReadyToApply || !got.HostsChecked || got.BlockedCount < 3 || got.Findings[0].Status != "blocked" || got.PlanHash != plan.PlanHash {
		t.Fatalf("incorrect readiness reduction: %+v", got)
	}
	if !contains(got.Unresolved, "exact-effects-diff") || !contains(got.Unresolved, "operator-confirmed-host-key-fingerprints") {
		t.Fatalf("open gates were lost: %+v", got.Unresolved)
	}
	if got.Findings[0].ID == "tool.kubectl" {
		t.Fatal("passing tool sorted before blockers")
	}
}

func TestBuildReadinessDoesNotTreatSkippedHostsAsReady(t *testing.T) {
	s, _ := fixture(t)
	writeReleaseTestRecord(t, s, completeReleaseRecord(s))
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	release, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildReadiness(c, plan, release, []ToolFact{{Name: "helm", Status: "installed", Reason: "install charts"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "action-required" || got.ReadyToApply || got.HostsChecked || !contains(got.Unresolved, "host-inventory-not-run") || got.BlockedCount != 0 {
		t.Fatalf("skipped hosts claimed readiness: %+v", got)
	}
	plan.InputHash = strings.Repeat("f", 64)
	if _, err := BuildReadiness(c, plan, release, nil, nil); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mixed plan accepted: %v", err)
	}
	plan.InputHash = c.InputHash
	release.ReleaseSHA256 = strings.Repeat("f", 64)
	if _, err := BuildReadiness(c, plan, release, nil, nil); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mixed release accepted: %v", err)
	}
	release.ReleaseSHA256 = c.ReleaseSHA256
	hosts := HostInventory{InputHash: strings.Repeat("f", 64)}
	if _, err := BuildReadiness(c, plan, release, nil, &hosts); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mixed host inventory accepted: %v", err)
	}
}

func TestBuildReadinessPreservesDiskScopeIdentityAndEvidenceTime(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	release, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	hosts := HostInventory{InputHash: c.InputHash, Hosts: []HostObservation{{ID: "server-1", Checks: []HostCheck{{
		ID: "selected-disk", Scope: "disk", Resource: "/dev/disk/by-id/example-data", Status: "blocked",
		Detail: "disk has a holder", ObservedAt: now,
	}}}}}
	report, err := BuildReadiness(c, plan, release, []ToolFact{}, &hosts)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.ID == "host.server-1.selected-disk" {
			if finding.Scope != "disk" || finding.Resource != "/dev/disk/by-id/example-data" || !finding.ObservedAt.Equal(now) {
				t.Fatalf("disk identity or evidence time lost: %+v", finding)
			}
			return
		}
	}
	t.Fatal("disk finding missing")
}

func TestHostInventoryBindingBlocksMissingChangedAndStaleHosts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	selected := []Host{{ID: "server-1", Role: "server", SSHAlias: "ubuntu@server-1", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{ID: "worker-1", Role: "agent", SSHAlias: "ubuntu@worker-1"}}
	valid := HostInventory{SchemaVersion: HostInventorySchemaVersion, State: "observed", Hosts: []HostObservation{
		{ID: "server-1", Role: "server", SSHAlias: "ubuntu@server-1", State: "observed", ObservedAt: now,
			Facts: HostFacts{HostKey: &ports.SSHHostKeyEvidence{FingerprintSHA256: selected[0].HostKeySHA256}}},
		{ID: "worker-1", Role: "agent", SSHAlias: "ubuntu@worker-1", State: "observed", ObservedAt: now},
	}}
	if got := checkHostInventoryBinding(selected, valid, now); len(got) != 0 {
		t.Fatalf("fresh matching inventory rejected: %+v", got)
	}
	blocked := valid
	blocked.State = "blocked"
	blocked.Hosts = append([]HostObservation(nil), valid.Hosts...)
	blocked.Hosts[0].State = "blocked"
	blocked.Hosts[0].Checks = []HostCheck{{ID: "selected-disk", Status: "blocked", Detail: "disk has a holder", ObservedAt: now}}
	if got := checkHostInventoryBinding(selected, blocked, now); len(got) != 0 {
		t.Fatalf("completed inventory with a real disk blocker was misclassified: %+v", got)
	}
	tests := []struct {
		name string
		edit func(*HostInventory)
		want string
	}{
		{"missing", func(r *HostInventory) { r.Hosts = r.Hosts[:1] }, "hosts.binding.missing-worker-1"},
		{"duplicate", func(r *HostInventory) { r.Hosts = append(r.Hosts, r.Hosts[0]) }, "hosts.binding.duplicate-server-1"},
		{"changed-target", func(r *HostInventory) { r.Hosts[0].SSHAlias = "ubuntu@other" }, "hosts.binding.target-server-1"},
		{"changed-key", func(r *HostInventory) { r.Hosts[0].Facts.HostKey = nil }, "hosts.binding.host-key-server-1"},
		{"expired", func(r *HostInventory) { r.Hosts[0].ObservedAt = now.Add(-maxHostInventoryAge - time.Second) }, "hosts.binding.age-server-1"},
		{"future", func(r *HostInventory) { r.Hosts[0].ObservedAt = now.Add(2 * time.Minute) }, "hosts.binding.age-server-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := valid
			copy.Hosts = append([]HostObservation(nil), valid.Hosts...)
			tt.edit(&copy)
			findings := checkHostInventoryBinding(selected, copy, now)
			if !hasFindingID(findings, tt.want) {
				t.Fatalf("missing %s: %+v", tt.want, findings)
			}
		})
	}
}

func TestBuildReadinessBlocksPartialHostInventory(t *testing.T) {
	s, _ := fixture(t)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	release, err := InspectRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	hosts := HostInventory{SchemaVersion: HostInventorySchemaVersion, State: "observed", InputHash: c.InputHash}
	got, err := BuildReadiness(c, plan, release, []ToolFact{}, &hosts)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlockedCount == 0 || !contains(got.Unresolved, "host-inventory-invalid-or-stale") || !hasFindingID(got.Findings, "hosts.binding.missing-server-1") {
		t.Fatalf("partial host inventory did not block readiness: %+v", got)
	}
}

func hasFindingID(findings []ReadinessFinding, want string) bool {
	for _, finding := range findings {
		if finding.ID == want {
			return true
		}
	}
	return false
}
