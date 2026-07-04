package bootstrap

import (
	"errors"
	"os"
	"testing"
)

func TestNewSession_NoMockEnv_NoKubeconfig_ReturnsErrRealAdaptersNotReady(t *testing.T) {
	// Defensive: ensure env is clean of both the mock toggle and the
	// kubeconfig resolution chain.
	mockPrev := os.Getenv("KUBE_DC_MOCK")
	kcPrev := os.Getenv("KUBECONFIG")
	homePrev := os.Getenv("HOME")
	t.Cleanup(func() {
		_ = os.Setenv("KUBE_DC_MOCK", mockPrev)
		_ = os.Setenv("KUBECONFIG", kcPrev)
		_ = os.Setenv("HOME", homePrev)
	})
	_ = os.Unsetenv("KUBE_DC_MOCK")
	// Point KUBECONFIG at a nonexistent path AND HOME at a tmp dir
	// so the standard resolution chain (KUBECONFIG -> ~/.kube/config
	// -> in-cluster) has no valid kubeconfig to land on.
	_ = os.Setenv("KUBECONFIG", "/nonexistent/path/to/kubeconfig")
	_ = os.Setenv("HOME", t.TempDir())

	_, err := NewSession(Options{Kubeconfig: "/nonexistent/path/to/kubeconfig"})
	if err == nil {
		t.Fatal("NewSession with no kubeconfig should fail")
	}
	if !errors.Is(err, ErrRealAdaptersNotReady) {
		t.Errorf("NewSession with no kubeconfig: err=%v, want chain to include ErrRealAdaptersNotReady", err)
	}
}

func TestNewSession_MockEnv_LoadsScenario(t *testing.T) {
	prev := os.Getenv("KUBE_DC_MOCK")
	t.Cleanup(func() { _ = os.Setenv("KUBE_DC_MOCK", prev) })
	_ = os.Setenv("KUBE_DC_MOCK", "fresh")

	s, err := NewSession(Options{})
	if err != nil {
		t.Fatalf("NewSession with KUBE_DC_MOCK=fresh: %v", err)
	}
	defer s.Close()

	if s.Scenario != "fresh" {
		t.Errorf("Scenario=%q, want fresh", s.Scenario)
	}
	if s.K8s == nil || s.OpenBao == nil || s.Scripts == nil {
		t.Error("session ports incomplete")
	}
}

func TestNewSession_MockEnv_UnknownScenarioReturnsError(t *testing.T) {
	prev := os.Getenv("KUBE_DC_MOCK")
	t.Cleanup(func() { _ = os.Setenv("KUBE_DC_MOCK", prev) })
	_ = os.Setenv("KUBE_DC_MOCK", "not-a-real-scenario")

	_, err := NewSession(Options{})
	if err == nil {
		t.Fatal("NewSession with bogus scenario returned nil error")
	}
}

func TestListMockScenarios_IncludesShipped(t *testing.T) {
	got, err := ListMockScenarios()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"fresh": false, "cloud": false, "openbao-sealed": false}
	for _, name := range got {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("ListMockScenarios missing %q (got %v)", name, got)
		}
	}
}
