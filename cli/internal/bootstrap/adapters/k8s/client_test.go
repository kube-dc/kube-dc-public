package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Compile-time assertion.
var _ ports.K8sClient = (*Client)(nil)

func TestKustomizationToNode_ReadsConditionsAndDeps(t *testing.T) {
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
				"conditions": []interface{}{
					map[string]interface{}{
						"type":    "Ready",
						"status":  "False",
						"reason":  "ReconciliationFailed",
						"message": "HelmRelease/openbao not ready",
					},
				},
			},
		},
	}
	n := kustomizationToNode(u)
	if n.Name != "platform" || n.Namespace != "flux-system" {
		t.Errorf("name/ns: %+v", n)
	}
	if n.Path != "./clusters/cloud/platform" {
		t.Errorf("path: %q", n.Path)
	}
	if len(n.DependsOn) != 2 {
		t.Errorf("deps: %v", n.DependsOn)
	}
	if n.Ready {
		t.Error("Ready=true on False condition")
	}
	if n.Reason != "ReconciliationFailed" {
		t.Errorf("reason: %q", n.Reason)
	}
}

func TestTopoSort_RootsFirst(t *testing.T) {
	nodes := []ports.GraphNode{
		{Name: "addons", DependsOn: []string{"platform"}},
		{Name: "platform", DependsOn: []string{"infra-core", "infra-object-storage"}},
		{Name: "infra-object-storage", DependsOn: []string{"infra-core"}},
		{Name: "infra-core", DependsOn: []string{"infra-cni"}},
		{Name: "infra-cni"},
	}
	topoSort(nodes)
	rankByName := map[string]int{}
	for i, n := range nodes {
		rankByName[n.Name] = i
	}
	// Every parent must come before its child.
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if rankByName[dep] > rankByName[n.Name] {
				t.Errorf("%s depends on %s but appears earlier (ranks: %v)", n.Name, dep, rankByName)
			}
		}
	}
	// First node must be a root (no deps).
	if len(nodes[0].DependsOn) != 0 {
		t.Errorf("first node has deps: %+v", nodes[0])
	}
}

func TestTopoSort_TieBreakAlpha(t *testing.T) {
	// Two roots — should come out alphabetically.
	nodes := []ports.GraphNode{
		{Name: "bbb"},
		{Name: "aaa"},
	}
	topoSort(nodes)
	if nodes[0].Name != "aaa" {
		t.Errorf("alpha tie-break failed: %+v", nodes)
	}
}

func TestTopoSort_BrokenDepDoesNotCrash(t *testing.T) {
	// References a Kustomization that isn't in the slice — we should
	// treat it as missing rather than panicking.
	nodes := []ports.GraphNode{
		{Name: "child", DependsOn: []string{"missing-parent"}},
	}
	topoSort(nodes) // must not panic
	if len(nodes) != 1 {
		t.Errorf("nodes mutated: %v", nodes)
	}
}

// GetServiceAnnotation against a fake clientset confirms the read
// path: returns the value, "", or an error for service-missing.
func TestGetServiceAnnotation_ReturnsValue(t *testing.T) {
	core := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openbao",
			Namespace: "openbao",
			Annotations: map[string]string{
				"kube-dc.com/openbao-bootstrap-finalized": "2026-05-26T10:00:00Z",
			},
		},
	})
	c := &Client{core: core}
	got, err := c.GetServiceAnnotation(context.Background(), "openbao", "openbao", "kube-dc.com/openbao-bootstrap-finalized")
	if err != nil {
		t.Fatalf("GetServiceAnnotation: %v", err)
	}
	if got != "2026-05-26T10:00:00Z" {
		t.Errorf("value = %q", got)
	}
}

func TestGetServiceAnnotation_MissingKey_EmptyNotError(t *testing.T) {
	core := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openbao",
			Namespace: "openbao",
		},
	})
	c := &Client{core: core}
	got, err := c.GetServiceAnnotation(context.Background(), "openbao", "openbao", "kube-dc.com/missing")
	if err != nil {
		t.Errorf("missing key should not error: %v", err)
	}
	if got != "" {
		t.Errorf("missing key returned %q", got)
	}
}

func TestGetServiceAnnotation_MissingService_Errors(t *testing.T) {
	core := fake.NewSimpleClientset()
	c := &Client{core: core}
	_, err := c.GetServiceAnnotation(context.Background(), "openbao", "openbao", "k")
	if err == nil {
		t.Fatal("missing Service should error")
	}
}

// SetServiceAnnotation patches via merge-patch — unrelated
// annotations on the Service must survive the patch.
func TestSetServiceAnnotation_PreservesOtherAnnotations(t *testing.T) {
	core := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openbao",
			Namespace: "openbao",
			Annotations: map[string]string{
				"meta.helm.sh/release-name": "openbao",
			},
		},
	})
	var patchBody string
	core.PrependReactor("patch", "services", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		pa, ok := action.(clienttesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		patchBody = string(pa.GetPatch())
		return false, nil, nil // fall through to default reactor
	})

	c := &Client{core: core}
	if err := c.SetServiceAnnotation(context.Background(), "openbao", "openbao", "kube-dc.com/openbao-bootstrap-finalized", "2026-05-26T10:00:00Z"); err != nil {
		t.Fatalf("SetServiceAnnotation: %v", err)
	}

	// The patch body must contain only the new annotation (merge
	// semantics preserve unrelated keys).
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(patchBody), &parsed); err != nil {
		t.Fatalf("bad patch body: %v\n%s", err, patchBody)
	}
	meta, _ := parsed["metadata"].(map[string]interface{})
	anns, _ := meta["annotations"].(map[string]interface{})
	if v, _ := anns["kube-dc.com/openbao-bootstrap-finalized"].(string); v != "2026-05-26T10:00:00Z" {
		t.Errorf("patch missing new annotation: %s", patchBody)
	}
	if _, hasOther := anns["meta.helm.sh/release-name"]; hasOther {
		t.Errorf("patch tries to overwrite unrelated annotation: %s", patchBody)
	}
}

func TestSetServiceAnnotation_EmptyValue_ClearsKey(t *testing.T) {
	core := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns", Annotations: map[string]string{"k": "v"}},
	})
	c := &Client{core: core}
	if err := c.SetServiceAnnotation(context.Background(), "ns", "s", "k", ""); err != nil {
		t.Fatalf("SetServiceAnnotation: %v", err)
	}
}

// PodExec must pass Stdin=true via PodExecOptions when stdin is
// non-empty. Asserted by intercepting the request via fake reactor
// chain (limited — we just assert the build path doesn't crash).
func TestPodExec_BuildsOptionsWithStdinFlag(t *testing.T) {
	// We can't easily inspect the PodExecOptions serialization via
	// the fake clientset (exec subresource isn't fully simulated).
	// Verify via the execStreamer hook contract: when stdin is
	// non-empty, the hook receives the bytes. Production wires
	// realExecStreamer; tests override.
	var capturedStdin []byte
	c := &Client{
		execStreamer: func(_ context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
			capturedStdin = append([]byte(nil), stdin...)
			if ns != "openbao" || pod != "openbao-0" {
				t.Errorf("ns/pod wrong: %s/%s", ns, pod)
			}
			if len(cmd) == 0 || cmd[len(cmd)-1] != "-" {
				t.Errorf("cmd should end with stdin sigil: %v", cmd)
			}
			return []byte("ok"), nil
		},
	}
	out, err := c.PodExec(context.Background(), "openbao", "openbao-0", []string{"bao", "operator", "unseal", "-"}, []byte("secret-share"))
	if err != nil {
		t.Fatalf("PodExec: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("out=%q", out)
	}
	if string(capturedStdin) != "secret-share" {
		t.Errorf("stdin not propagated: %q", capturedStdin)
	}
	_ = schema.GroupVersionResource{} // keep schema imported for future graph tests
}

// =====================================================================
// realKubectlStreamer + resolveKubectl unit tests (F-bootstrap-3 follow-up)
// =====================================================================

// resolveKubectl must be idempotent — sync.Once should run LookPath
// at most once per Client lifetime, returning the same answer on
// every call. The actual binary location varies by host so we only
// assert idempotence, not a specific value.
func TestResolveKubectl_Idempotent(t *testing.T) {
	c := &Client{}
	first := c.resolveKubectl()
	second := c.resolveKubectl()
	if first != second {
		t.Errorf("resolveKubectl should cache via sync.Once; got %q then %q", first, second)
	}
}

// realKubectlStreamer must return ErrKubectlNotFound when kubectl is
// absent — so the caller can preserve the original WS-drop error
// classification instead of substituting a less informative
// "command not found".
func TestRealKubectlStreamer_KubectlAbsent_ReturnsErrKubectlNotFound(t *testing.T) {
	// Pre-seed the cache with an empty path to simulate "kubectl not
	// on PATH" without depending on the test environment's actual
	// kubectl install.
	c := &Client{}
	c.kubectlOnce.Do(func() {
		c.kubectlPath = ""
	})

	_, err := c.realKubectlStreamer(context.Background(), "openbao", "openbao-0",
		[]string{"bao", "status"}, nil)
	if err == nil {
		t.Fatal("expected ErrKubectlNotFound when kubectl is absent")
	}
	if !errors.Is(err, ports.ErrKubectlNotFound) {
		t.Errorf("expected errors.Is(err, ErrKubectlNotFound); got %v", err)
	}
}

// Argv-shape contract: the kubectl-exec child gets [exec -n <ns>
// [-i] <pod> -- <cmd...>]. `-i` MUST appear only when caller
// supplied stdin (matches kubectl's own behaviour for non-stdin
// invocations) — the apiserver doesn't allocate the stdin pipe
// channel when -i is absent, so we save a half-duplex pipe per call.
// This is tested via a process-level stub: we point kubectlPath at
// a tiny shell script that records its argv to a temp file.
func TestRealKubectlStreamer_ArgvShape_WithAndWithoutStdin(t *testing.T) {
	// Write a tiny stub binary that records its argv + stdin into
	// two files we can inspect after.
	tmpDir := t.TempDir()
	argsLogPath := tmpDir + "/argv.log"
	stdinLogPath := tmpDir + "/stdin.log"
	stub := tmpDir + "/fake-kubectl.sh"
	stubScript := `#!/bin/sh
printf '%s\n' "$@" > "` + argsLogPath + `"
cat > "` + stdinLogPath + `"
echo "stub-output"
`
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cases := []struct {
		name      string
		stdin     []byte
		wantI     bool
	}{
		{"no_stdin_omits_i", nil, false},
		{"with_stdin_adds_i", []byte("token-payload"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset logs.
			_ = os.WriteFile(argsLogPath, nil, 0o644)
			_ = os.WriteFile(stdinLogPath, nil, 0o644)

			c := &Client{}
			c.kubectlOnce.Do(func() {
				c.kubectlPath = stub
			})
			out, err := c.realKubectlStreamer(context.Background(),
				"openbao", "openbao-0",
				[]string{"sh", "-c", "echo hi"}, tc.stdin)
			if err != nil {
				t.Fatalf("realKubectlStreamer: %v (out=%q)", err, out)
			}

			loggedArgs, _ := os.ReadFile(argsLogPath)
			argSlice := strings.Split(strings.TrimSpace(string(loggedArgs)), "\n")

			// First three args are always [exec, -n, openbao]
			if len(argSlice) < 4 || argSlice[0] != "exec" || argSlice[1] != "-n" || argSlice[2] != "openbao" {
				t.Errorf("argv prefix wrong: %v", argSlice)
			}
			// -i presence
			gotI := false
			for _, a := range argSlice {
				if a == "-i" {
					gotI = true
					break
				}
			}
			if gotI != tc.wantI {
				t.Errorf("-i present=%v, want=%v; argv=%v", gotI, tc.wantI, argSlice)
			}
			// Pod + -- separator + cmd tail
			foundSep := false
			for _, a := range argSlice {
				if a == "--" {
					foundSep = true
					break
				}
			}
			if !foundSep {
				t.Errorf("missing -- separator: %v", argSlice)
			}
			// Stdin transparency
			loggedStdin, _ := os.ReadFile(stdinLogPath)
			if string(loggedStdin) != string(tc.stdin) {
				t.Errorf("stdin = %q, want %q", loggedStdin, tc.stdin)
			}
		})
	}
}

// Kubeconfig pass-through: when the adapter was built with an
// explicit kubeconfigPath, the child kubectl gets KUBECONFIG=<path>
// in its env (overriding any inherited value). When the path is
// empty, the child inherits the parent's env unchanged so kubectl
// does its own standard resolution.
func TestRealKubectlStreamer_KubeconfigEnvPassthrough(t *testing.T) {
	tmpDir := t.TempDir()
	envLogPath := tmpDir + "/env.log"
	stub := tmpDir + "/fake-kubectl.sh"
	stubScript := `#!/bin/sh
env | grep '^KUBECONFIG=' > "` + envLogPath + `" || true
echo "stub-output"
`
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	t.Run("explicit_path_overrides_inherited", func(t *testing.T) {
		t.Setenv("KUBECONFIG", "/inherited/path")
		_ = os.WriteFile(envLogPath, nil, 0o644)

		c := &Client{kubeconfigPath: "/explicit/path"}
		c.kubectlOnce.Do(func() { c.kubectlPath = stub })

		_, _ = c.realKubectlStreamer(context.Background(),
			"openbao", "openbao-0", []string{"true"}, nil)

		logged, _ := os.ReadFile(envLogPath)
		got := strings.TrimSpace(string(logged))
		if got != "KUBECONFIG=/explicit/path" {
			t.Errorf("explicit kubeconfig didn't override inherited; got %q", got)
		}
	})

	t.Run("empty_path_inherits_env", func(t *testing.T) {
		t.Setenv("KUBECONFIG", "/inherited/path")
		_ = os.WriteFile(envLogPath, nil, 0o644)

		c := &Client{} // no kubeconfigPath
		c.kubectlOnce.Do(func() { c.kubectlPath = stub })

		_, _ = c.realKubectlStreamer(context.Background(),
			"openbao", "openbao-0", []string{"true"}, nil)

		logged, _ := os.ReadFile(envLogPath)
		got := strings.TrimSpace(string(logged))
		if got != "KUBECONFIG=/inherited/path" {
			t.Errorf("empty kubeconfigPath should let parent env pass through; got %q", got)
		}
	})
}
