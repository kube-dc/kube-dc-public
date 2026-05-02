package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// newTestManager builds a Manager rooted in a tmp file so the test
// never touches the operator's actual ~/.kube/config.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	return &Manager{path: path}, path
}

func writeConfig(t *testing.T, path string, c *Config) {
	t.Helper()
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestRemoveContext_DeletesOnlyNamedContext is the regression for the
// bug operator @voa hit: selecting one row in the context manager and
// pressing `d` used to wipe every context that pointed at the same
// server. Now it must delete exactly one.
func TestRemoveContext_DeletesOnlyNamedContext(t *testing.T) {
	mgr, path := newTestManager(t)

	// Mirror operator's actual layout: 5 tenant contexts + 1 admin,
	// all on the same stage server, plus one unrelated context.
	writeConfig(t, path, &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "kube-dc/stage.kube-dc.com/shalb/dev",
		Clusters: []NamedCluster{
			{Name: "kube-dc-stage.kube-dc.com-shalb", Cluster: Cluster{Server: "https://kube-api.stage.kube-dc.com:6443"}},
			{Name: "kube-dc-stage-admin", Cluster: Cluster{Server: "https://kube-api.stage.kube-dc.com:6443"}},
			{Name: "cloud-tunnel", Cluster: Cluster{Server: "https://localhost:6443"}},
		},
		Users: []NamedUser{
			{Name: "kube-dc@stage.kube-dc.com/shalb", User: User{}},
			{Name: "kube-dc-admin@stage", User: User{}},
		},
		Contexts: []NamedContext{
			{Name: "kube-dc/stage.kube-dc.com/shalb/dev", Context: Context{Cluster: "kube-dc-stage.kube-dc.com-shalb", User: "kube-dc@stage.kube-dc.com/shalb", Namespace: "shalb-dev"}},
			{Name: "kube-dc/stage.kube-dc.com/shalb/demo", Context: Context{Cluster: "kube-dc-stage.kube-dc.com-shalb", User: "kube-dc@stage.kube-dc.com/shalb", Namespace: "shalb-demo"}},
			{Name: "kube-dc/stage.kube-dc.com/shalb/envoy", Context: Context{Cluster: "kube-dc-stage.kube-dc.com-shalb", User: "kube-dc@stage.kube-dc.com/shalb", Namespace: "shalb-envoy"}},
			{Name: "kube-dc/stage/admin", Context: Context{Cluster: "kube-dc-stage-admin", User: "kube-dc-admin@stage"}},
			{Name: "cloud-tunnel", Context: Context{Cluster: "cloud-tunnel", User: ""}},
		},
	})

	if err := mgr.RemoveContext("kube-dc/stage/admin"); err != nil {
		t.Fatalf("RemoveContext: %v", err)
	}

	got, err := mgr.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The 3 tenant contexts MUST survive. This is the load-bearing
	// assertion — failing it means we regressed back to the bug.
	keepNames := map[string]bool{
		"kube-dc/stage.kube-dc.com/shalb/dev":   true,
		"kube-dc/stage.kube-dc.com/shalb/demo":  true,
		"kube-dc/stage.kube-dc.com/shalb/envoy": true,
		"cloud-tunnel":                          true,
	}
	gotNames := make(map[string]bool, len(got.Contexts))
	for _, c := range got.Contexts {
		gotNames[c.Name] = true
	}
	for n := range keepNames {
		if !gotNames[n] {
			t.Errorf("context %q was deleted, must have survived", n)
		}
	}
	if gotNames["kube-dc/stage/admin"] {
		t.Error("kube-dc/stage/admin was NOT deleted")
	}

	// Cluster GC: kube-dc-stage-admin had only the admin context;
	// it must be gone. kube-dc-stage.kube-dc.com-shalb still has 3
	// contexts pointing at it; it must remain.
	clusterNames := map[string]bool{}
	for _, cl := range got.Clusters {
		clusterNames[cl.Name] = true
	}
	if clusterNames["kube-dc-stage-admin"] {
		t.Error("orphan cluster kube-dc-stage-admin should have been GC'd")
	}
	if !clusterNames["kube-dc-stage.kube-dc.com-shalb"] {
		t.Error("shared cluster kube-dc-stage.kube-dc.com-shalb was deleted — should have stayed (3 contexts still reference it)")
	}

	// User GC: same rule.
	userNames := map[string]bool{}
	for _, u := range got.Users {
		userNames[u.Name] = true
	}
	if userNames["kube-dc-admin@stage"] {
		t.Error("orphan user kube-dc-admin@stage should have been GC'd")
	}
	if !userNames["kube-dc@stage.kube-dc.com/shalb"] {
		t.Error("shared user kube-dc@stage.kube-dc.com/shalb was deleted — should have stayed")
	}
}

// TestRemoveContext_ClearsCurrentContext verifies that deleting the
// active context resets current-context. (Keeping it would point at a
// non-existent entry, breaking kubectl until the operator switches.)
func TestRemoveContext_ClearsCurrentContext(t *testing.T) {
	mgr, path := newTestManager(t)
	writeConfig(t, path, &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "kube-dc/stage/admin",
		Clusters:       []NamedCluster{{Name: "kube-dc-stage-admin", Cluster: Cluster{Server: "https://x:6443"}}},
		Users:          []NamedUser{{Name: "kube-dc-admin@stage"}},
		Contexts: []NamedContext{
			{Name: "kube-dc/stage/admin", Context: Context{Cluster: "kube-dc-stage-admin", User: "kube-dc-admin@stage"}},
			{Name: "other", Context: Context{Cluster: "kube-dc-stage-admin", User: "kube-dc-admin@stage"}},
		},
	})
	if err := mgr.RemoveContext("kube-dc/stage/admin"); err != nil {
		t.Fatalf("RemoveContext: %v", err)
	}
	got, _ := mgr.Load()
	if got.CurrentContext != "" {
		t.Errorf("CurrentContext = %q, want \"\" (cleared)", got.CurrentContext)
	}
}

// TestRemoveContext_Idempotent makes the operator's "press d twice"
// experience tolerable: the second invocation is a no-op.
func TestRemoveContext_Idempotent(t *testing.T) {
	mgr, path := newTestManager(t)
	writeConfig(t, path, &Config{
		APIVersion: "v1",
		Kind:       "Config",
		Contexts:   []NamedContext{{Name: "x"}},
	})
	if err := mgr.RemoveContext("x"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := mgr.RemoveContext("x"); err != nil {
		t.Errorf("second delete (already gone): %v — want nil (idempotent)", err)
	}
	if err := mgr.RemoveContext("never-existed"); err != nil {
		t.Errorf("delete of missing context: %v — want nil (idempotent)", err)
	}
}

// TestRemoveContext_ExternalRowIsSafe — the operator must be able to
// `d` an EXTERNAL row (e.g. cloud-tunnel) without taking down sibling
// kube-dc contexts. This is the path enabled by switching from
// RemoveKubeDCContexts(server) to RemoveContext(name).
func TestRemoveContext_ExternalRowIsSafe(t *testing.T) {
	mgr, path := newTestManager(t)
	writeConfig(t, path, &Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: []NamedCluster{
			{Name: "cloud-tunnel-cluster", Cluster: Cluster{Server: "https://localhost:6443"}},
			{Name: "kube-dc-cloud-admin", Cluster: Cluster{Server: "https://kube-api.kube-dc.cloud:6443"}},
		},
		Users: []NamedUser{
			{Name: "cloud-tunnel-user"},
			{Name: "kube-dc-admin@cloud"},
		},
		Contexts: []NamedContext{
			{Name: "cloud-tunnel", Context: Context{Cluster: "cloud-tunnel-cluster", User: "cloud-tunnel-user"}},
			{Name: "kube-dc/cloud/admin", Context: Context{Cluster: "kube-dc-cloud-admin", User: "kube-dc-admin@cloud"}},
		},
	})
	if err := mgr.RemoveContext("cloud-tunnel"); err != nil {
		t.Fatalf("RemoveContext: %v", err)
	}
	got, _ := mgr.Load()
	gotCtx := map[string]bool{}
	for _, c := range got.Contexts {
		gotCtx[c.Name] = true
	}
	if gotCtx["cloud-tunnel"] {
		t.Error("cloud-tunnel still present")
	}
	if !gotCtx["kube-dc/cloud/admin"] {
		t.Error("kube-dc/cloud/admin lost — RemoveContext leaked into a sibling")
	}
}
