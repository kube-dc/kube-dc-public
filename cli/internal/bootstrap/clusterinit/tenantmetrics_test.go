package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTenantMetrics_Disabled_NoOp(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/monitoring-writepath")
	if err := WriteTenantMetrics(fleet, "c1", false, nil); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fleet, "clusters", "c1", "monitoring-writepath.yaml")); !os.IsNotExist(err) {
		t.Fatalf("disabled must write nothing")
	}
}

func TestWriteTenantMetrics_WiresOverlay(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/monitoring-writepath")
	if err := WriteTenantMetrics(fleet, "c1", true, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")

	b, err := os.ReadFile(filepath.Join(clusterDir, "monitoring-writepath.yaml"))
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	for _, want := range []string{
		"kind: Kustomization",
		"name: monitoring-writepath",
		"path: ./platform/monitoring-writepath",
		"- name: platform", // dependsOn
		// codex fixes for a fresh-owned stateless layer:
		"prune: true",
		"force: false",
		"healthChecks:",
		"name: cortex-tenant",
		"name: alloy-metrics",
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("overlay missing %q:\n%s", want, b)
		}
	}
	// The stateful-adoption footgun must NOT be present on this fresh layer.
	if strings.Contains(string(b), "force: true") {
		t.Fatalf("fresh-owned layer must not use force: true:\n%s", b)
	}

	kust, err := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kust), "  - monitoring-writepath.yaml") {
		t.Fatalf("kustomization not wired:\n%s", kust)
	}

	// Idempotent: a second call must not duplicate the resource entry.
	if err := WriteTenantMetrics(fleet, "c1", true, nil); err != nil {
		t.Fatalf("second write: %v", err)
	}
	kust2, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if n := strings.Count(string(kust2), "- monitoring-writepath.yaml"); n != 1 {
		t.Fatalf("resource entry duplicated (%d):\n%s", n, kust2)
	}
}

func TestWriteTenantMetrics_AbsentBundleSkips(t *testing.T) {
	// Older starter without the bundle: skip with a warning, do not fail, and
	// do not wire a resource the cluster can't render.
	fleet := tempFleet(t, "c1") // no platform/monitoring-writepath
	var log strings.Builder
	if err := WriteTenantMetrics(fleet, "c1", true, &log); err != nil {
		t.Fatalf("absent bundle must not fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fleet, "clusters", "c1", "monitoring-writepath.yaml")); !os.IsNotExist(err) {
		t.Fatalf("absent bundle must not write the overlay")
	}
	kust, _ := os.ReadFile(filepath.Join(fleet, "clusters", "c1", "kustomization.yaml"))
	if strings.Contains(string(kust), "monitoring-writepath.yaml") {
		t.Fatalf("absent bundle must not wire the resource:\n%s", kust)
	}
	if !strings.Contains(log.String(), "skipped") {
		t.Fatalf("skip must be logged, got: %s", log.String())
	}
}
