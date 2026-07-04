package openbao

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakePGBao is a minimal ports.OpenBaoClient stub for
// policy-generation tests — only GetAnnotation is meaningful; the
// other methods satisfy the interface and are never called.
type fakePGBao struct {
	annotations   map[string]string
	getErr        error
	getCalls      int
	lastGetKey    string
}

func (f *fakePGBao) GetAnnotation(_ context.Context, _, key string) (string, error) {
	f.getCalls++
	f.lastGetKey = key
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.annotations[key], nil
}

// Interface stubs.
func (f *fakePGBao) PodList(_ context.Context) ([]string, error)                     { return nil, nil }
func (f *fakePGBao) Status(_ context.Context, _ string) (ports.BaoStatus, error)     { return ports.BaoStatus{}, nil }
func (f *fakePGBao) Unseal(_ context.Context, _ string, _ []byte) error              { return nil }
func (f *fakePGBao) RaftJoin(_ context.Context, _, _ string) error                   { return nil }
func (f *fakePGBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error)      { return nil, nil }
func (f *fakePGBao) RevokeSelf(_ context.Context, _ []byte) error                    { return nil }
func (f *fakePGBao) ApplyPolicy(_ context.Context, _ []byte, _, _ string) error       { return nil }
func (f *fakePGBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error    { return nil }
func (f *fakePGBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	return nil
}
func (f *fakePGBao) WriteAuthRole(_ context.Context, _ []byte, _, _ string, _ map[string]string) error {
	return nil
}
func (f *fakePGBao) SetAnnotation(_ context.Context, _, _, _ string) error                 { return nil }
func (f *fakePGBao) SetAnnotations(_ context.Context, _ string, _ map[string]string) error { return nil }

// TestReadPolicyGenerationInstalled_Absent — canonical "legacy
// install / never stamped" case. Empty annotation returns (0, nil)
// so callers can render drift against expected >= 1.
func TestReadPolicyGenerationInstalled_Absent(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{}}
	n, err := ReadPolicyGenerationInstalled(context.Background(), bao)
	if err != nil {
		t.Fatalf("absent annotation: unexpected error %v", err)
	}
	if n != 0 {
		t.Errorf("absent → want 0, got %d", n)
	}
	// Verify we asked for the right annotation key.
	if bao.lastGetKey != AnnotationPolicyGeneration {
		t.Errorf("wrong key queried: %s", bao.lastGetKey)
	}
}

// TestReadPolicyGenerationInstalled_Present — canonical happy path.
func TestReadPolicyGenerationInstalled_Present(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{
		AnnotationPolicyGeneration: "3",
	}}
	n, err := ReadPolicyGenerationInstalled(context.Background(), bao)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if n != 3 {
		t.Errorf("want 3, got %d", n)
	}
}

// TestReadPolicyGenerationInstalled_MalformedRefuses — non-integer
// stamps must surface an error so callers can distinguish "absent"
// from "corrupt stamp" (the recovery differs — corrupt = manual
// inspection; absent = run setup-controller-auth).
func TestReadPolicyGenerationInstalled_MalformedRefuses(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{
		AnnotationPolicyGeneration: "not-a-number",
	}}
	n, err := ReadPolicyGenerationInstalled(context.Background(), bao)
	if err == nil {
		t.Fatal("malformed annotation must error")
	}
	if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("expected the malformed value in the error, got %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 on error, got %d", n)
	}
}

// TestReadPolicyGenerationInstalled_NegativeRefuses — negative
// stamps are a corruption signal (legacy stamp munged by a bad
// script). Refuse.
func TestReadPolicyGenerationInstalled_NegativeRefuses(t *testing.T) {
	bao := &fakePGBao{annotations: map[string]string{
		AnnotationPolicyGeneration: "-1",
	}}
	_, err := ReadPolicyGenerationInstalled(context.Background(), bao)
	if err == nil {
		t.Fatal("negative value must error")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("expected 'negative' in error, got %v", err)
	}
}

// TestReadPolicyGenerationInstalled_AdapterErrorPropagates —
// apiserver 500 / RBAC denial surfaces to the caller with the
// annotation key in the wrapped message.
func TestReadPolicyGenerationInstalled_AdapterErrorPropagates(t *testing.T) {
	bao := &fakePGBao{getErr: errors.New("simulated 403")}
	_, err := ReadPolicyGenerationInstalled(context.Background(), bao)
	if err == nil {
		t.Fatal("adapter error must propagate")
	}
	if !strings.Contains(err.Error(), "simulated 403") {
		t.Errorf("expected adapter error surfaced, got %v", err)
	}
	if !strings.Contains(err.Error(), AnnotationPolicyGeneration) {
		t.Errorf("expected annotation key in wrapped error, got %v", err)
	}
}

// TestReadPolicyGenerationInstalled_NilBao_Refuses — programmer
// error catch.
func TestReadPolicyGenerationInstalled_NilBao_Refuses(t *testing.T) {
	_, err := ReadPolicyGenerationInstalled(context.Background(), nil)
	if err == nil {
		t.Fatal("nil OpenBaoClient must error")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected 'nil' in error, got %v", err)
	}
}

// TestStatusResult_HasPolicyGenerationDrift_TruthTable — the
// helper's contract: expected > installed → drift. Exercise
// forward-drift (expected 2, installed 1), legacy-drift (expected
// 1, installed 0), match (expected 1, installed 1), and reverse
// (expected 1, installed 2 — operator ran a NEWER binary against
// this cluster earlier).
func TestStatusResult_HasPolicyGenerationDrift_TruthTable(t *testing.T) {
	cases := []struct {
		name             string
		expected         int
		installed        int
		wantDrift        bool
	}{
		{"legacy-absent", 1, 0, true},
		{"forward-drift", 2, 1, true},
		{"match-1-1", 1, 1, false},
		{"match-2-2", 2, 2, false},
		{"reverse-drift-1-2", 1, 2, false},
		{"reverse-drift-1-3", 1, 3, false},
		{"double-zero", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := StatusResult{
				PolicyGenerationExpected:  c.expected,
				PolicyGenerationInstalled: c.installed,
			}
			if got := r.HasPolicyGenerationDrift(); got != c.wantDrift {
				t.Errorf("HasPolicyGenerationDrift(exp=%d, inst=%d) = %v, want %v",
					c.expected, c.installed, got, c.wantDrift)
			}
		})
	}
}

// TestStatusResult_FullyReady_IgnoresDrift — policy-generation
// drift is a soft signal; a cluster with drift but otherwise
// green stays FullyReady=true (contract: CLI never blocks on drift).
func TestStatusResult_FullyReady_IgnoresDrift(t *testing.T) {
	// Baseline: green cluster.
	r := StatusResult{
		Pods: []ports.BaoStatus{
			{Initialized: true, Sealed: false},
			{Initialized: true, Sealed: false},
		},
		BootstrapFinalized:        "some-time",
		ControllerAuthInstalled:   "some-time",
		PolicyGenerationExpected:  5,
		PolicyGenerationInstalled: 3, // <- drift
	}
	if !r.FullyReady() {
		t.Errorf("FullyReady should be true even with policy-generation drift (soft signal)")
	}
	if !r.HasPolicyGenerationDrift() {
		t.Errorf("baseline should have drift")
	}
}

// TestStatus_PopulatesPolicyGenerationFields — end-to-end: the
// Status() engine reads the annotation via
// ReadPolicyGenerationInstalled and stamps both fields on the
// result. Uses a fakeStatusBao (already exists in status_test.go)
// extended with a policy-generation annotation.
func TestStatus_PopulatesPolicyGenerationFields(t *testing.T) {
	// Reuse the canonical ready 3-pod baseline, add PG annotation.
	bao := canonicalReadyBao()
	bao.annotations[AnnotationPolicyGeneration] = strconv.Itoa(PolicyGeneration)

	r, err := Status(context.Background(), StatusOptions{
		ClusterName: "cs/zrh",
		OpenBao:     bao,
	})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if r.PolicyGenerationExpected != PolicyGeneration {
		t.Errorf("expected field mismatch: %d vs %d", r.PolicyGenerationExpected, PolicyGeneration)
	}
	if r.PolicyGenerationInstalled != PolicyGeneration {
		t.Errorf("installed field mismatch: %d vs %d", r.PolicyGenerationInstalled, PolicyGeneration)
	}
	if r.HasPolicyGenerationDrift() {
		t.Errorf("matched generation must not report drift")
	}
	if !r.FullyReady() {
		t.Errorf("green cluster + matched generation → FullyReady")
	}
}

// TestStatus_LegacyPolicyGeneration_ReportsDrift — same shape, but
// the annotation is absent. Status() surfaces drift and
// FullyReady remains true (soft signal).
func TestStatus_LegacyPolicyGeneration_ReportsDrift(t *testing.T) {
	bao := canonicalReadyBao()
	// annotation absent
	r, err := Status(context.Background(), StatusOptions{
		ClusterName: "cs/zrh",
		OpenBao:     bao,
	})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if r.PolicyGenerationInstalled != 0 {
		t.Errorf("legacy install → installed=0, got %d", r.PolicyGenerationInstalled)
	}
	if !r.HasPolicyGenerationDrift() {
		t.Errorf("legacy install must report drift (expected=%d, installed=0)", r.PolicyGenerationExpected)
	}
	if !r.FullyReady() {
		t.Errorf("green pods + both markers + only PG drift → FullyReady=true (soft signal)")
	}
}
