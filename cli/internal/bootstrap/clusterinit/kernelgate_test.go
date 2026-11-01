package clusterinit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type kernels map[string]string

func (k kernels) NodeKernelVersions(context.Context) (map[string]string, error) { return k, nil }

func TestKernelAtLeast(t *testing.T) {
	for release, want := range map[string]bool{
		"7.0.0-42-generic": true, "7.1.3": true, "8.0": true,
		"6.8.0-60-generic": false, "6.17.1": false, "": false, "garbage": false,
	} {
		if got := kernelAtLeast(release, 7, 0); got != want {
			t.Errorf("kernelAtLeast(%q) = %v, want %v", release, got, want)
		}
	}
}

func TestCheckCephKernels(t *testing.T) {
	ctx := context.Background()
	old := kernels{"n1": "7.0.0-42-generic", "n2": "6.8.0-60-generic"}
	err := CheckCephKernels(ctx, CephKernelGateOptions{Nodes: old, Mode: RookCephMultiNode})
	if err == nil || !strings.Contains(err.Error(), "n2 (6.8.0-60-generic)") || strings.Contains(err.Error(), "n1") {
		t.Fatalf("err = %v", err)
	}
	if err := CheckCephKernels(ctx, CephKernelGateOptions{Nodes: kernels{"n1": "7.0.0"}, Mode: RookCephMultiNode}); err != nil {
		t.Fatal(err)
	}
	// Modes without Rook, and clients without the capability, stand down.
	if err := CheckCephKernels(ctx, CephKernelGateOptions{Nodes: old, Mode: RookMode("external-s3")}); err != nil {
		t.Fatal(err)
	}
	if err := CheckCephKernels(ctx, CephKernelGateOptions{Nodes: struct{}{}, Mode: RookCephMultiNode}); err != nil {
		t.Fatal(err)
	}
	// An existing overlay that kept its earlier key policy is not re-gated.
	overlay := filepath.Join(t.TempDir(), "kustomization.yaml")
	if err := os.WriteFile(overlay, []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckCephKernels(ctx, CephKernelGateOptions{Nodes: old, Mode: RookCephMultiNode, OverlayPath: overlay}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCephKernelsRejectsReleaseCandidates(t *testing.T) {
	if err := CheckCephKernels(context.Background(), CephKernelGateOptions{Nodes: kernels{"n1": "7.1.0-rc3"}, Mode: RookCephMultiNode}); err == nil {
		t.Fatal("a release-candidate kernel passed")
	}
}
