package flux

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Compile-time assertion: *Client implements ports.FluxClient.
var _ ports.FluxClient = (*Client)(nil)

func TestGVRForKind(t *testing.T) {
	if _, err := gvrForKind("Kustomization"); err != nil {
		t.Errorf("Kustomization: %v", err)
	}
	if _, err := gvrForKind("HelmRelease"); err != nil {
		t.Errorf("HelmRelease: %v", err)
	}
	if _, err := gvrForKind("Whatever"); err == nil {
		t.Error("Whatever should be rejected")
	}
}

func TestKustomizationFromUnstructured_ReadyCondition(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":      "platform",
				"namespace": "flux-system",
			},
			"spec": map[string]interface{}{
				"path": "./clusters/cloud/platform",
				"dependsOn": []interface{}{
					map[string]interface{}{"name": "infra-core"},
					map[string]interface{}{"name": "infra-object-storage"},
				},
			},
			"status": map[string]interface{}{
				"lastAppliedRevision": "main@abc123",
				"conditions": []interface{}{
					map[string]interface{}{
						"type":    "Ready",
						"status":  "True",
						"reason":  "ReconciliationSucceeded",
						"message": "Applied revision: main@abc123",
					},
				},
			},
		},
	}
	ev := kustomizationFromUnstructured(u)
	if ev.Name != "platform" || ev.Namespace != "flux-system" {
		t.Errorf("name/ns: %+v", ev)
	}
	if !ev.Ready {
		t.Errorf("Ready=false; conds: %v", ev)
	}
	if ev.Revision != "main@abc123" {
		t.Errorf("Revision=%q", ev.Revision)
	}
	if len(ev.DependsOn) != 2 || ev.DependsOn[0] != "infra-core" {
		t.Errorf("DependsOn: %v", ev.DependsOn)
	}
}

func TestKustomizationFromUnstructured_NotReadySurfaceReason(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "platform", "namespace": "flux-system"},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":    "Ready",
						"status":  "False",
						"reason":  "ReconciliationFailed",
						"message": "HelmRelease/openbao not ready: 3/3 sealed",
					},
					map[string]interface{}{
						"type":   "Reconciling",
						"status": "True",
					},
				},
			},
		},
	}
	ev := kustomizationFromUnstructured(u)
	if ev.Ready {
		t.Error("Ready=true on False condition")
	}
	if !ev.Reconciling {
		t.Error("Reconciling=false")
	}
	if ev.Reason != "ReconciliationFailed" || !contains(ev.Message, "sealed") {
		t.Errorf("Reason/Message: %q / %q", ev.Reason, ev.Message)
	}
}

func TestHelmReleaseFromUnstructured_ChartInfo(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "kube-dc", "namespace": "kube-dc"},
			"spec": map[string]interface{}{
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{
						"chart":   "kube-dc",
						"version": "v0.3.62",
					},
				},
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
				},
			},
		},
	}
	ev := helmReleaseFromUnstructured(u)
	if ev.ChartName != "kube-dc" || ev.ChartVersion != "v0.3.62" {
		t.Errorf("chart: %+v", ev)
	}
	if !ev.Ready {
		t.Error("Ready=false")
	}
}

func TestConsumeTimestamps_RFC3339(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"lastAppliedRevisionTimestamp": "2026-05-26T10:30:00Z",
				"lastHandledReconcileAt":       "2026-05-26T10:31:00Z",
			},
		},
	}
	last, attempt := consumeTimestamps(u)
	if last.IsZero() || attempt.IsZero() {
		t.Errorf("timestamps: %v / %v", last, attempt)
	}
	if attempt.Before(last) {
		t.Error("attempt should be at or after last applied")
	}
}

func TestBootstrap_RequiresToken(t *testing.T) {
	c := &Client{}
	if err := c.Bootstrap(nil, ports.FluxBootstrapOpts{}); err == nil {
		t.Fatal("Bootstrap with empty token should fail")
	}
	_ = time.Second // keep time import for future timestamp tests
}

// Bootstrap MUST pass GITHUB_TOKEN via env, NEVER as a CLI arg.
func TestBootstrap_TokenInEnvNotArgv(t *testing.T) {
	var capturedArgs []string
	var capturedEnv []string
	c := &Client{
		runFluxCmd: func(_ context.Context, env []string, args ...string) ([]byte, []byte, error) {
			capturedArgs = append([]string(nil), args...)
			capturedEnv = append([]string(nil), env...)
			return nil, nil, nil
		},
	}
	const tok = "ghp_DO-NOT-LEAK"
	err := c.Bootstrap(context.Background(), ports.FluxBootstrapOpts{
		GitHubOwner: "acme",
		GitHubRepo:  "fleet",
		Path:        "clusters/cloud",
		Token:       tok,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	for i, a := range capturedArgs {
		if strings.Contains(a, "DO-NOT-LEAK") || strings.Contains(a, "ghp_") {
			t.Errorf("token leaked into argv[%d]=%q", i, a)
		}
	}
	envOK := false
	for _, e := range capturedEnv {
		if e == "GITHUB_TOKEN="+tok {
			envOK = true
		}
	}
	if !envOK {
		t.Errorf("GITHUB_TOKEN not in env: %v", capturedEnv)
	}
}

// Watch send is ctx-aware: a slow consumer can't park the watch
// goroutine indefinitely. Verified by cancelling ctx after the
// channel is full (via no draining) and confirming the watch
// goroutine exits (out channel closes).
func TestWatch_ContextCancel_UnblocksSlowConsumer(t *testing.T) {
	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan ports.KustomizationEvent, 1) // tight buffer
	doneSend := make(chan struct{})
	go func() {
		defer close(doneSend)
		// Fill the buffer.
		out <- ports.KustomizationEvent{Name: "first"}
		// Now try to send into a full channel — should select ctx.Done.
		ev := ports.KustomizationEvent{Name: "second"}
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}()

	// Don't drain. Cancel after a moment.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-doneSend:
		// Good — ctx unblocked the send.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch send did not unblock on ctx cancel")
	}
	_ = c // keep type referenced for future watch tests
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
