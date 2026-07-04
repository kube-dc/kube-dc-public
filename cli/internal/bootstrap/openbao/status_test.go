package openbao

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeStatusBao is a minimal ports.OpenBaoClient tailored for the
// Status engine — only the 3 methods Status calls (PodList, Status,
// GetAnnotation) have live behaviour; everything else is a no-op so
// we satisfy the interface. Recording the calls lets us assert
// which pods + which annotation keys the engine hit.
type fakeStatusBao struct {
	pods           []string
	perPod         map[string]ports.BaoStatus // pod → canned status
	annotations    map[string]string          // key → value
	podListErr     error
	perPodErr      map[string]error // pod → status error
	annotationErr  map[string]error // key → get error
	statusCallLog  []string
	annotCallLog   []string
	podListCalls   int
	annotBatchArgs map[string]map[string]string
}

func (f *fakeStatusBao) PodList(_ context.Context) ([]string, error) {
	f.podListCalls++
	return f.pods, f.podListErr
}

func (f *fakeStatusBao) Status(_ context.Context, pod string) (ports.BaoStatus, error) {
	f.statusCallLog = append(f.statusCallLog, pod)
	if err, ok := f.perPodErr[pod]; ok && err != nil {
		return ports.BaoStatus{}, err
	}
	return f.perPod[pod], nil
}

func (f *fakeStatusBao) GetAnnotation(_ context.Context, _, key string) (string, error) {
	f.annotCallLog = append(f.annotCallLog, key)
	if err, ok := f.annotationErr[key]; ok && err != nil {
		return "", err
	}
	return f.annotations[key], nil
}

// Interface satisfaction stubs — none of these should be called by Status.
func (f *fakeStatusBao) Unseal(_ context.Context, _ string, _ []byte) error      { return nil }
func (f *fakeStatusBao) RaftJoin(_ context.Context, _, _ string) error           { return nil }
func (f *fakeStatusBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeStatusBao) RevokeSelf(_ context.Context, _ []byte) error                 { return nil }
func (f *fakeStatusBao) ApplyPolicy(_ context.Context, _ []byte, _, _ string) error    { return nil }
func (f *fakeStatusBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error { return nil }
func (f *fakeStatusBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	return nil
}
func (f *fakeStatusBao) WriteAuthRole(_ context.Context, _ []byte, _, _ string, _ map[string]string) error {
	return nil
}
func (f *fakeStatusBao) SetAnnotation(_ context.Context, _, _, _ string) error { return nil }
func (f *fakeStatusBao) SetAnnotations(_ context.Context, svc string, kv map[string]string) error {
	if f.annotBatchArgs == nil {
		f.annotBatchArgs = map[string]map[string]string{}
	}
	f.annotBatchArgs[svc] = kv
	return nil
}

// canonicalReadyBao returns a fully-ready 3-pod HA cluster —
// baseline for happy-path tests.
func canonicalReadyBao() *fakeStatusBao {
	return &fakeStatusBao{
		pods: []string{"openbao-0", "openbao-1", "openbao-2"},
		perPod: map[string]ports.BaoStatus{
			"openbao-0": {Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "active", RaftIndex: 1234},
			"openbao-1": {Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "standby"},
			"openbao-2": {Initialized: true, Sealed: false, Version: "2.5.3", HAMode: "standby"},
		},
		annotations: map[string]string{
			AnnotationBootstrapFinalized:      "2026-06-01T10:00:00Z",
			AnnotationControllerAuthInstalled: "2026-06-01T10:05:00Z",
		},
	}
}

// TestStatus_HappyPath_FullyReady — canonical green state. Every pod
// unsealed + Initialized, both markers present. FullyReady is true;
// no error.
func TestStatus_HappyPath_FullyReady(t *testing.T) {
	bao := canonicalReadyBao()
	var out bytes.Buffer
	r, err := Status(context.Background(), StatusOptions{
		ClusterName: "atlantis",
		OpenBao:     bao,
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("happy path: unexpected error %v", err)
	}
	if !r.FullyReady() {
		t.Errorf("expected FullyReady=true, got %+v", r)
	}
	if r.AnySealed() {
		t.Errorf("no pod should be sealed")
	}
	if r.AnyUninitialized() {
		t.Errorf("no pod should be uninitialized")
	}
	if r.ClusterName != "atlantis" {
		t.Errorf("ClusterName = %q, want atlantis", r.ClusterName)
	}
	if bao.podListCalls != 1 {
		t.Errorf("PodList should be called exactly once, got %d", bao.podListCalls)
	}
	if len(bao.statusCallLog) != 3 {
		t.Errorf("Status should be called per pod (3), got %d", len(bao.statusCallLog))
	}
	// Both annotation keys read.
	seen := map[string]bool{}
	for _, k := range bao.annotCallLog {
		seen[k] = true
	}
	if !seen[AnnotationBootstrapFinalized] || !seen[AnnotationControllerAuthInstalled] {
		t.Errorf("expected both markers read, got %v", bao.annotCallLog)
	}
}

// TestStatus_SealedPod_NotFullyReady — the canonical sealed-cascade
// state (post-restart). AnySealed=true, FullyReady=false.
func TestStatus_SealedPod_NotFullyReady(t *testing.T) {
	bao := canonicalReadyBao()
	bao.perPod["openbao-1"] = ports.BaoStatus{Initialized: true, Sealed: true, Version: "2.5.3"}
	r, err := Status(context.Background(), StatusOptions{
		ClusterName: "cloud",
		OpenBao:     bao,
	})
	if err != nil {
		t.Fatalf("sealed-pod: unexpected error %v", err)
	}
	if !r.AnySealed() {
		t.Errorf("expected AnySealed=true")
	}
	if r.FullyReady() {
		t.Errorf("expected FullyReady=false with a sealed pod")
	}
}

// TestStatus_UninitializedPod_NotFullyReady — represents the
// OrderedReady race (openbao-1/2 came up before joining the leader's
// raft cluster). AnyUninitialized=true, FullyReady=false.
func TestStatus_UninitializedPod_NotFullyReady(t *testing.T) {
	bao := canonicalReadyBao()
	bao.perPod["openbao-2"] = ports.BaoStatus{Initialized: false, Sealed: true}
	r, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err != nil {
		t.Fatalf("uninit: unexpected error %v", err)
	}
	if !r.AnyUninitialized() {
		t.Errorf("expected AnyUninitialized=true")
	}
	if r.FullyReady() {
		t.Errorf("expected FullyReady=false")
	}
}

// TestStatus_MissingBootstrapFinalized_NotFullyReady — init Phase B
// didn't stamp the annotation (bailed after custody, before annotate).
func TestStatus_MissingBootstrapFinalized_NotFullyReady(t *testing.T) {
	bao := canonicalReadyBao()
	delete(bao.annotations, AnnotationBootstrapFinalized)
	r, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err != nil {
		t.Fatalf("missing-finalized: unexpected error %v", err)
	}
	if r.FullyReady() {
		t.Errorf("expected FullyReady=false when bootstrap-finalized missing")
	}
	if r.BootstrapFinalized != "" {
		t.Errorf("expected empty BootstrapFinalized, got %q", r.BootstrapFinalized)
	}
}

// TestStatus_MissingControllerAuthInstalled_NotFullyReady — Phase C
// didn't run (init pre-M5-T08, or legacy install).
func TestStatus_MissingControllerAuthInstalled_NotFullyReady(t *testing.T) {
	bao := canonicalReadyBao()
	delete(bao.annotations, AnnotationControllerAuthInstalled)
	r, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err != nil {
		t.Fatalf("missing-cai: unexpected error %v", err)
	}
	if r.FullyReady() {
		t.Errorf("expected FullyReady=false when controller-auth-installed missing")
	}
}

// TestStatus_NoPodsFound_ReturnsErr — structural failure. Empty
// PodList → typed ErrStatusNoPodsFound; result zero-value discarded.
func TestStatus_NoPodsFound_ReturnsErr(t *testing.T) {
	bao := &fakeStatusBao{pods: []string{}}
	r, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err == nil {
		t.Fatal("expected ErrStatusNoPodsFound, got nil")
	}
	if !errors.Is(err, ErrStatusNoPodsFound) {
		t.Errorf("expected ErrStatusNoPodsFound, got %v", err)
	}
	if r.FullyReady() {
		t.Errorf("empty result should not be FullyReady")
	}
}

// TestStatus_NilOpenBao_ReturnsErr — defensive; nil adapter is a
// caller wiring bug. Typed ErrStatusMissingDependency so cobra layer
// can distinguish from adapter errors.
func TestStatus_NilOpenBao_ReturnsErr(t *testing.T) {
	_, err := Status(context.Background(), StatusOptions{})
	if err == nil {
		t.Fatal("expected ErrStatusMissingDependency, got nil")
	}
	if !errors.Is(err, ErrStatusMissingDependency) {
		t.Errorf("expected ErrStatusMissingDependency, got %v", err)
	}
}

// TestStatus_PodListError_Propagates — apiserver-level failure
// (RBAC, kubectl-exec timeout) reaches the caller unwrapped-with-context.
func TestStatus_PodListError_Propagates(t *testing.T) {
	bao := canonicalReadyBao()
	bao.podListErr = errors.New("apiserver 500")
	_, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err == nil {
		t.Fatal("expected propagated error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("apiserver 500")) {
		t.Errorf("expected propagated 'apiserver 500', got %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("list pods")) {
		t.Errorf("expected 'list pods' context, got %v", err)
	}
}

// TestStatus_StatusError_Propagates — mid-loop status failure aborts
// the walk with the failing pod's name in the wrapped error.
func TestStatus_StatusError_Propagates(t *testing.T) {
	bao := canonicalReadyBao()
	bao.perPodErr = map[string]error{"openbao-1": errors.New("exec timeout")}
	_, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err == nil {
		t.Fatal("expected propagated error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("openbao-1")) {
		t.Errorf("expected failing pod name in error, got %v", err)
	}
}

// TestStatus_AnnotationError_Propagates — a hard failure reading an
// annotation (403 / service gone) is structural; the engine must
// surface it rather than silently render an empty marker (which
// would look identical to the "annotation absent" recoverable state).
func TestStatus_AnnotationError_Propagates(t *testing.T) {
	bao := canonicalReadyBao()
	bao.annotationErr = map[string]error{AnnotationBootstrapFinalized: errors.New("forbidden")}
	_, err := Status(context.Background(), StatusOptions{OpenBao: bao})
	if err == nil {
		t.Fatal("expected propagated annotation error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("forbidden")) {
		t.Errorf("expected 'forbidden' in error, got %v", err)
	}
}

// TestStatus_NilOut_NoCrash — nil Out falls back to io.Discard.
func TestStatus_NilOut_NoCrash(t *testing.T) {
	bao := canonicalReadyBao()
	_, err := Status(context.Background(), StatusOptions{OpenBao: bao, Out: nil})
	if err != nil {
		t.Fatalf("nil Out should not error, got %v", err)
	}
}

// TestStatusResult_FullyReady_EmptyPods — FullyReady must NOT return
// true when Pods is empty (defensive: an empty result with markers
// present should still be a "not ready" state, not a false positive).
func TestStatusResult_FullyReady_EmptyPods(t *testing.T) {
	r := StatusResult{
		BootstrapFinalized:      "some-time",
		ControllerAuthInstalled: "some-time",
	}
	if r.FullyReady() {
		t.Error("FullyReady should be false with 0 pods even if markers set")
	}
}

// TestStatusResult_AllUninitialized_TruthTable — the reviewer-flagged
// helper. Distinguishes "cluster never init'd" (all-uninit → run
// `openbao init`) from "OrderedReady race" (some-uninit → run
// `openbao unseal` which auto-raft-joins). Empty Pods returns false
// (that's a structural failure, not a never-init state).
func TestStatusResult_AllUninitialized_TruthTable(t *testing.T) {
	cases := []struct {
		name string
		pods []ports.BaoStatus
		want bool
	}{
		{"empty", nil, false},
		{"single-init", []ports.BaoStatus{{Initialized: true}}, false},
		{"single-uninit", []ports.BaoStatus{{Initialized: false}}, true},
		{"all-init", []ports.BaoStatus{{Initialized: true}, {Initialized: true}, {Initialized: true}}, false},
		{"all-uninit", []ports.BaoStatus{{Initialized: false}, {Initialized: false}, {Initialized: false}}, true},
		{"mixed-leader-init", []ports.BaoStatus{{Initialized: true}, {Initialized: false}}, false},
		{"mixed-follower-init", []ports.BaoStatus{{Initialized: false}, {Initialized: true}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := StatusResult{Pods: c.pods}
			if got := r.AllUninitialized(); got != c.want {
				t.Errorf("AllUninitialized() = %v, want %v (pods=%+v)", got, c.want, c.pods)
			}
		})
	}
}
