package gpuupgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validQualification() Qualification {
	return Qualification{
		APIVersion: APIVersion, Kind: Kind, ID: "v100-kernel-canary-1", State: "qualified",
		ApprovedBy: "platform-review", SourceRevision: strings.Repeat("a", 40),
		Hardware: []string{"10de:1db6/10de:124a"},
		Target:   Target{Kernel: "6.8.0-135-generic", RKE2: "v1.36.1+rke2r1", Driver: "580.130.00", GPUOperator: "v26.3.4", DCGMExporter: "4.4.1-4.6.0-ubuntu22.04"},
		Canary:   Canary{CompletedAt: "2026-07-14T12:00:00Z", AllocationPassed: true, MonitoringPassed: true, RollbackPassed: true, Evidence: "change-record-42"},
	}
}

func validRequest() Request {
	return Request{
		Current: Target{Kernel: "6.8.0-134-generic", RKE2: "v1.35.3+rke2r3", Driver: "580.126.20", GPUOperator: "v26.3.3", DCGMExporter: "4.4.1-4.6.0-ubuntu22.04"},
		Target:  validQualification().Target, Hardware: []string{"10DE:1DB6/10DE:124A"},
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}
}

func TestCheckAllowsExactRecentQualifiedTuple(t *testing.T) {
	got, err := Check(validQualification(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.QualificationID != "v100-kernel-canary-1" || got.ApprovedBy != "platform-review" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestCheckFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Qualification, *Request)
		want string
	}{
		{"unknown target", func(_ *Qualification, r *Request) { r.Target.Driver = "999.0" }, "not the qualified value"},
		{"unknown exporter", func(_ *Qualification, r *Request) { r.Target.DCGMExporter = "4.5.3-4.8.2" }, "dcgmExporter target"},
		{"hardware mismatch", func(_ *Qualification, r *Request) { r.Hardware = []string{"10de:1db4/10de:1212"} }, "hardware tuple mismatch"},
		{"minor skip", func(q *Qualification, r *Request) { q.Target.RKE2 = "v1.37.1+rke2r1"; r.Target.RKE2 = q.Target.RKE2 }, "cannot skip"},
		{"downgrade", func(q *Qualification, r *Request) { q.Target.RKE2 = "v1.34.9+rke2r1"; r.Target.RKE2 = q.Target.RKE2 }, "downgrade"},
		{"failed allocation", func(q *Qualification, _ *Request) { q.Canary.AllocationPassed = false }, "must all pass"},
		{"stale evidence", func(q *Qualification, _ *Request) { q.Canary.CompletedAt = "2026-05-01T00:00:00Z" }, "older than"},
		{"unapproved", func(q *Qualification, _ *Request) { q.ApprovedBy = "" }, "approvedBy"},
		{"no change", func(q *Qualification, r *Request) { q.Target = r.Current; r.Target = r.Current }, "at least one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, req := validQualification(), validRequest()
			tt.edit(&q, &req)
			_, err := Check(q, req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q blocker, got %v", tt.want, err)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	for _, body := range []string{
		"apiVersion: kube-dc.io/v1alpha1\nkind: GPUUpgradeQualification\nunknown: true\n",
		"apiVersion: kube-dc.io/v1alpha1\nkind: GPUUpgradeQualification\n---\nid: second\n",
	} {
		path := filepath.Join(t.TempDir(), "qualification.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("expected fail-closed decode for %q", body)
		}
	}
}
