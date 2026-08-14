package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClusterConfig lays down clusters/<name>/cluster-config.env with the given
// body and returns the fleet-repo root.
func writeClusterConfig(t *testing.T, cluster, body string) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", cluster)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func readArrival(t *testing.T, repo, cluster string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "clusters", cluster, "cluster-config.env"))
	if err != nil {
		t.Fatal(err)
	}
	return envValue(string(b), "KUBE_API_ARRIVAL_IP")
}

// none/1:1-NAT: add-cluster.sh already wrote the node's own IP — leave it alone.
func TestResolveKubeAPIArrivalIP_NoneLayerUntouched(t *testing.T) {
	repo := writeClusterConfig(t, "c1",
		"INGRESS_ADDRESS_LAYER=none\nKUBE_API_ARRIVAL_IP=192.0.2.11\nMETALLB_FLOATING_IP=CHANGEME\n")
	if err := ResolveKubeAPIArrivalIP(repo, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if got := readArrival(t, repo, "c1"); got != "192.0.2.11" {
		t.Errorf("arrival IP = %q, want 192.0.2.11 (untouched)", got)
	}
}

// MetalLB layer: the scaffold left CHANGEME; resolve it to the announced VIP.
func TestResolveKubeAPIArrivalIP_ResolvesVIP(t *testing.T) {
	repo := writeClusterConfig(t, "c1",
		"INGRESS_ADDRESS_LAYER=metallb-l2\nKUBE_API_ARRIVAL_IP=CHANGEME\nMETALLB_FLOATING_IP=198.51.100.7\n")
	if err := ResolveKubeAPIArrivalIP(repo, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if got := readArrival(t, repo, "c1"); got != "198.51.100.7" {
		t.Errorf("arrival IP = %q, want 198.51.100.7 (resolved from METALLB_FLOATING_IP)", got)
	}
}

// VIP still unset: leave the placeholder and warn (render gate rejects it later).
func TestResolveKubeAPIArrivalIP_UnresolvedWarns(t *testing.T) {
	repo := writeClusterConfig(t, "c1",
		"INGRESS_ADDRESS_LAYER=metallb-l2\nKUBE_API_ARRIVAL_IP=CHANGEME\nMETALLB_FLOATING_IP=CHANGEME\n")
	var sb strings.Builder
	if err := ResolveKubeAPIArrivalIP(repo, "c1", &sb); err != nil {
		t.Fatal(err)
	}
	if got := readArrival(t, repo, "c1"); got != "CHANGEME" {
		t.Errorf("arrival IP = %q, want CHANGEME (left for the operator)", got)
	}
	if !strings.Contains(sb.String(), "DARK") {
		t.Errorf("expected a DARK warning, got: %q", sb.String())
	}
}

// none-layer with a bad arrival IP must NOT borrow a stale valid METALLB_FLOATING_IP
// (the VIP is unannounced on this layer) — warn and leave it (codex 2026-08-11).
func TestResolveKubeAPIArrivalIP_NoneLayerDoesNotBorrowVIP(t *testing.T) {
	repo := writeClusterConfig(t, "c1",
		"INGRESS_ADDRESS_LAYER=none\nKUBE_API_ARRIVAL_IP=CHANGEME\nMETALLB_FLOATING_IP=198.51.100.7\n")
	var sb strings.Builder
	if err := ResolveKubeAPIArrivalIP(repo, "c1", &sb); err != nil {
		t.Fatal(err)
	}
	if got := readArrival(t, repo, "c1"); got != "CHANGEME" {
		t.Errorf("arrival IP = %q, want CHANGEME (must NOT borrow the unannounced VIP on a none layer)", got)
	}
	if !strings.Contains(sb.String(), "DARK") {
		t.Errorf("expected a DARK warning, got: %q", sb.String())
	}
}

// Missing cluster-config.env (mock harness / older starter) is a no-op, not an error.
func TestResolveKubeAPIArrivalIP_MissingFileNoop(t *testing.T) {
	if err := ResolveKubeAPIArrivalIP(t.TempDir(), "c1", nil); err != nil {
		t.Errorf("missing cluster-config.env should be a no-op, got %v", err)
	}
}
