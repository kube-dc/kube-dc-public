package discover

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakePGBao is a minimal ports.OpenBaoClient — only GetAnnotation is
// meaningful. Kept in this package (not shared with openbao's own
// fakePGBao) to avoid a cross-package test-helper dependency.
type fakePGBao struct {
	annotations map[string]string
	getErr      error
}

func (f *fakePGBao) GetAnnotation(_ context.Context, _, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.annotations[key], nil
}
func (f *fakePGBao) PodList(_ context.Context) ([]string, error)                 { return nil, nil }
func (f *fakePGBao) Status(_ context.Context, _ string) (ports.BaoStatus, error) { return ports.BaoStatus{}, nil }
func (f *fakePGBao) Unseal(_ context.Context, _ string, _ []byte) error          { return nil }
func (f *fakePGBao) RaftJoin(_ context.Context, _, _ string) error               { return nil }
func (f *fakePGBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error)  { return nil, nil }
func (f *fakePGBao) RevokeSelf(_ context.Context, _ []byte) error                { return nil }
func (f *fakePGBao) ApplyPolicy(_ context.Context, _ []byte, _, _ string) error   { return nil }
func (f *fakePGBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error {
	return nil
}
func (f *fakePGBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	return nil
}
func (f *fakePGBao) WriteAuthRole(_ context.Context, _ []byte, _, _ string, _ map[string]string) error {
	return nil
}
func (f *fakePGBao) SetAnnotation(_ context.Context, _, _, _ string) error                 { return nil }
func (f *fakePGBao) SetAnnotations(_ context.Context, _ string, _ map[string]string) error { return nil }

// TestPolicyGenerationProbe_Installed — annotation matches compile-time.
func TestPolicyGenerationProbe_Installed(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{
		openbao.AnnotationPolicyGeneration: strconv.Itoa(openbao.PolicyGeneration),
	}}
	p := NewPolicyGenerationProbe(bao)
	r := p.Run(context.Background())
	if r.Status != ports.StatusInstalled {
		t.Errorf("want StatusInstalled, got %v", r.Status)
	}
	if r.Severity != ports.SeverityInfo {
		t.Errorf("want SeverityInfo, got %v", r.Severity)
	}
	if !strings.Contains(r.Detail, "up to date") {
		t.Errorf("expected 'up to date' in Detail, got %q", r.Detail)
	}
}

// TestPolicyGenerationProbe_LegacyAbsent — annotation missing
// (installed=0). Distinct message from a bumped-since-run case.
func TestPolicyGenerationProbe_LegacyAbsent(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{}}
	p := NewPolicyGenerationProbe(bao)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("want StatusMissing, got %v", r.Status)
	}
	if r.Severity != ports.SeverityInfo {
		t.Errorf("want SeverityInfo (soft), got %v", r.Severity)
	}
	if !strings.Contains(r.Detail, "legacy install") {
		t.Errorf("expected 'legacy install' in Detail, got %q", r.Detail)
	}
	if !strings.Contains(r.FixHint.Text, "refresh-policy") {
		t.Errorf("expected refresh-policy in FixHint, got %q", r.FixHint.Text)
	}
}

// TestPolicyGenerationProbe_ForwardDrift — installed > 0 but
// expected > installed. Chart-upgrade drift.
func TestPolicyGenerationProbe_ForwardDrift(t *testing.T) {
	// Only meaningful when compile-time PolicyGeneration >= 2 —
	// simulate by pretending the installed value is one behind.
	// We stash a value = expected-1 (which becomes 0 when expected=1;
	// that lands in the legacy branch, so we bump by 2 to force
	// forward-drift semantics even at generation 1). Since the
	// probe compares vs the compile-time constant, we need
	// installed > 0 AND < expected — that means we need to
	// dynamically choose. When PolicyGeneration == 1, forward-drift
	// isn't reachable in this test (would need installed=0 → legacy).
	// Guard with skip.
	if openbao.PolicyGeneration < 2 {
		t.Skipf("skipping forward-drift test at PolicyGeneration=%d — needs >= 2 to have a non-legacy prior value", openbao.PolicyGeneration)
	}
	bao := &fakePGBao{annotations: map[string]string{
		openbao.AnnotationPolicyGeneration: strconv.Itoa(openbao.PolicyGeneration - 1),
	}}
	p := NewPolicyGenerationProbe(bao)
	r := p.Run(context.Background())
	if r.Status != ports.StatusPartial {
		t.Errorf("want StatusPartial, got %v", r.Status)
	}
	if r.Severity != ports.SeverityInfo {
		t.Errorf("want SeverityInfo, got %v", r.Severity)
	}
	if !strings.Contains(r.Detail, "DRIFT") {
		t.Errorf("expected 'DRIFT' in Detail, got %q", r.Detail)
	}
	if !strings.Contains(r.FixHint.Text, "refresh-policy") {
		t.Errorf("expected refresh-policy in FixHint, got %q", r.FixHint.Text)
	}
}

// TestPolicyGenerationProbe_AdapterError_Warns — a hard read
// failure surfaces as Warn (severity higher than Info) so the
// operator investigates rather than silently accepting "no drift".
func TestPolicyGenerationProbe_AdapterError_Warns(t *testing.T) {
	bao := &fakePGBao{getErr: errors.New("apiserver 403")}
	p := NewPolicyGenerationProbe(bao)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("want StatusMissing on adapter error, got %v", r.Status)
	}
	if r.Severity != ports.SeverityWarn {
		t.Errorf("want SeverityWarn (not Info — adapter failure means we CAN'T characterise drift), got %v", r.Severity)
	}
	if !strings.Contains(r.Detail, "apiserver 403") {
		t.Errorf("expected wrapped adapter error in Detail, got %q", r.Detail)
	}
}

// TestPolicyGenerationProbe_NilBao — pre-session call sites.
// Reports StatusMissing/Info with a "not configured" detail so
// the doctor render doesn't crash.
func TestPolicyGenerationProbe_NilBao(t *testing.T) {
	p := NewPolicyGenerationProbe(nil)
	r := p.Run(context.Background())
	if r.Status != ports.StatusMissing {
		t.Errorf("want StatusMissing, got %v", r.Status)
	}
	if r.Severity != ports.SeverityInfo {
		t.Errorf("want SeverityInfo, got %v", r.Severity)
	}
	if !strings.Contains(r.Detail, "no OpenBaoClient") {
		t.Errorf("expected 'no OpenBaoClient' in Detail, got %q", r.Detail)
	}
}

// TestPolicyGenerationProbe_Name — stable name identifier — doctor
// output tests match against this exact string.
func TestPolicyGenerationProbe_Name(t *testing.T) {
	p := NewPolicyGenerationProbe(nil)
	if got := p.Name(); got != "openbao-policy-generation" {
		t.Errorf("Name() = %q, want %q", got, "openbao-policy-generation")
	}
}

// TestRealFactory_ClusterOpenBao — nil-safe + returns exactly the
// policy-generation probe when wired.
func TestRealFactory_ClusterOpenBao(t *testing.T) {
	got := RealFactory{}.ClusterOpenBao(nil)
	if got != nil {
		t.Errorf("ClusterOpenBao(nil) should return nil, got %d probes", len(got))
	}
	bao := &fakePGBao{}
	probes := RealFactory{}.ClusterOpenBao(bao)
	if len(probes) != 1 {
		t.Fatalf("ClusterOpenBao(bao) should return exactly 1 probe, got %d", len(probes))
	}
	if probes[0].Name() != "openbao-policy-generation" {
		t.Errorf("wrong probe returned: %s", probes[0].Name())
	}
}
