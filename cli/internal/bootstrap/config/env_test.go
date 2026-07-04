package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-config.env")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestSet_ReplacesExistingValueInPlace(t *testing.T) {
	// M4-T10's post-process step depends on Set updating an
	// existing key without disturbing surrounding comments / blank
	// lines / key order.
	path := writeFixture(t, `# Cluster: atlantis
CLUSTER_NAME=atlantis

# Network
EXT_NET_VLAN_ID=CHANGEME
EXT_NET_INTERFACE=CHANGEME
POD_CIDR=10.100.0.0/16
`)
	env, err := LoadEnv(path)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	env.Set("EXT_NET_VLAN_ID", "1103")
	env.Set("EXT_NET_INTERFACE", "bond0")
	if err := env.Write(""); err != nil {
		t.Fatalf("Write: %v", err)
	}

	body, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := []string{
		"# Cluster: atlantis",
		"CLUSTER_NAME=atlantis",
		"",
		"# Network",
		"EXT_NET_VLAN_ID=1103",
		"EXT_NET_INTERFACE=bond0",
		"POD_CIDR=10.100.0.0/16",
	}
	if len(lines) != len(want) {
		t.Fatalf("line count = %d, want %d\nGOT:\n%s", len(lines), len(want), string(body))
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

func TestSet_AppendsNewKey(t *testing.T) {
	path := writeFixture(t, `CLUSTER_NAME=atlantis
POD_CIDR=10.100.0.0/16
`)
	env, _ := LoadEnv(path)
	env.Set("KUBE_DC_VERSION", "v0.3.63")
	if err := env.Write(""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "KUBE_DC_VERSION=v0.3.63") {
		t.Errorf("new key missing:\n%s", body)
	}
	// Existing keys preserved.
	if !strings.Contains(string(body), "CLUSTER_NAME=atlantis") {
		t.Errorf("existing key wiped:\n%s", body)
	}
	if !strings.Contains(string(body), "POD_CIDR=10.100.0.0/16") {
		t.Errorf("existing key wiped:\n%s", body)
	}
}

func TestSet_RoundTripStable(t *testing.T) {
	// Setting a key to its current value followed by Write should
	// produce byte-identical output (no churn). This is the
	// determinism guarantee operators rely on for clean diff
	// reviews.
	original := `CLUSTER_NAME=atlantis
POD_CIDR=10.100.0.0/16
`
	path := writeFixture(t, original)
	env, _ := LoadEnv(path)
	env.Set("CLUSTER_NAME", "atlantis") // no-op value change
	if err := env.Write(""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != original {
		t.Errorf("round-trip non-stable:\nGOT:\n%q\nWANT:\n%q", string(body), original)
	}
}

func TestWrite_AtomicViaUniqueTemp(t *testing.T) {
	// Atomic-write contract: no `.tmp` residue after success.
	path := writeFixture(t, "CLUSTER_NAME=atlantis\n")
	env, _ := LoadEnv(path)
	env.Set("CLUSTER_NAME", "renamed")
	if err := env.Write(""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q leaked", e.Name())
		}
	}
}

func TestWrite_PathFallback(t *testing.T) {
	// Write("") falls back to e.Path. Write(p) writes to p and
	// updates e.Path for future no-arg writes.
	path := writeFixture(t, "K=v\n")
	env, _ := LoadEnv(path)
	if err := env.Write(""); err != nil {
		t.Fatalf("Write(\"\"): %v", err)
	}
	// New target.
	dir := t.TempDir()
	target := filepath.Join(dir, "moved.env")
	env.Set("K", "v2")
	if err := env.Write(target); err != nil {
		t.Fatalf("Write(target): %v", err)
	}
	if env.Path != target {
		t.Errorf("Path not updated: %q vs %q", env.Path, target)
	}
}

func TestAppendComment_AndBlank(t *testing.T) {
	path := writeFixture(t, "K=v\n")
	env, _ := LoadEnv(path)
	env.AppendBlank()
	env.AppendComment("# --- inherited from sibling cloud ---")
	env.Set("KUBE_DC_VERSION", "v0.3.63")
	if err := env.Write(""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	for _, want := range []string{
		"K=v",
		"# --- inherited from sibling cloud ---",
		"KUBE_DC_VERSION=v0.3.63",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
