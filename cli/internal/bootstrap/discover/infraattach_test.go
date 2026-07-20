package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeSubnetReader struct {
	cidr string
	err  error
}

func (f fakeSubnetReader) GetResourceFieldFirst(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
	return f.cidr, f.err
}

func TestInfraAttachProbe_ReportsPresentSubnet(t *testing.T) {
	r := NewInfraAttachProbe(fakeSubnetReader{cidr: "100.66.0.0/16"}, "").Run(context.Background())
	if r.Status != ports.StatusInstalled || r.Severity != ports.SeverityInfo {
		t.Fatalf("status=%v severity=%v", r.Status, r.Severity)
	}
	if !strings.Contains(r.Detail, "100.66.0.0/16") {
		t.Fatalf("detail should name the CIDR: %q", r.Detail)
	}
}

// The failure this exists to catch: configured for dual-homing, subnet never
// applied. The cluster looks healthy and silently never dual-homes.
func TestInfraAttachProbe_AbsentSubnetIsAnActionableWarning(t *testing.T) {
	r := NewInfraAttachProbe(fakeSubnetReader{cidr: ""}, "").Run(context.Background())
	if r.Status != ports.StatusMissing || r.Severity != ports.SeverityWarn {
		t.Fatalf("status=%v severity=%v", r.Status, r.Severity)
	}
	for _, want := range []string{"not active", "tenant-net-v2"} {
		if !strings.Contains(r.Detail, want) {
			t.Fatalf("detail missing %q: %q", want, r.Detail)
		}
	}
}

// A read failure must not escalate: doctor should not fail its whole run
// because one custom resource could not be read.
func TestInfraAttachProbe_ReadErrorDoesNotBlock(t *testing.T) {
	r := NewInfraAttachProbe(fakeSubnetReader{err: errors.New("boom")}, "").Run(context.Background())
	if r.Severity == ports.SeverityBlocker {
		t.Fatal("a CR read failure must not be a blocker")
	}
}

func TestInfraAttachProbe_NilClientIsSafe(t *testing.T) {
	if r := NewInfraAttachProbe(nil, "").Run(context.Background()); r.Severity == ports.SeverityBlocker {
		t.Fatal("nil client must not blocker")
	}
}
