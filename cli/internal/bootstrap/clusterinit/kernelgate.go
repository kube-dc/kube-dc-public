package clusterinit

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// NodeKernelLister is the optional K8s capability the kernel gate needs.
type NodeKernelLister interface {
	NodeKernelVersions(ctx context.Context) (map[string]string, error)
}

// CephKernelGateOptions configures CheckCephKernels.
type CephKernelGateOptions struct {
	// RequireEvidence makes unknown or partial node inventories blocking.
	RequireEvidence bool
	ExpectedNodes   []string
	// Nodes is usually the session's K8s client; a client without
	// NodeKernelLister makes the gate stand down.
	Nodes any
	Mode  RookMode
	// OverlayPath is clusters/<name>/object-storage/kustomization.yaml. An
	// existing overlay without AES256K keeps its qualified key policy.
	OverlayPath string
	Out         io.Writer
}

// CheckCephKernels refuses a new Rook Ceph overlay when any node runs a
// kernel older than 7.0. New overlays select AES256K CephX keys, which every
// RBD/CephFS client kernel must support; the Fleet host-compatibility
// DaemonSet otherwise stays unready and blocks the storage layer for good
// (Ubuntu 24.04 GA kernel 6.8, found in a 2026-10-31 installation rehearsal).
func CheckCephKernels(ctx context.Context, opts CephKernelGateOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.Mode != RookCephLocal && opts.Mode != RookCephMultiNode && opts.Mode != RookCephPVC {
		return nil
	}
	if body, err := os.ReadFile(opts.OverlayPath); err == nil && !strings.Contains(string(body), "aes256k") {
		return nil
	}
	lister, ok := opts.Nodes.(NodeKernelLister)
	if !ok || lister == nil {
		if opts.RequireEvidence {
			return fmt.Errorf("Ceph kernel compatibility evidence is unavailable")
		}
		return nil
	}
	kernels, err := lister.NodeKernelVersions(ctx)
	if err != nil || len(kernels) == 0 {
		if opts.RequireEvidence {
			return fmt.Errorf("Ceph kernel compatibility evidence is unavailable")
		}
		fmt.Fprintf(out, "[apply] Ceph kernel gate: skipped (node kernels unavailable)\n")
		return nil
	}
	if opts.RequireEvidence {
		if len(kernels) != len(opts.ExpectedNodes) {
			return fmt.Errorf("Ceph kernel inventory differs from reviewed hosts")
		}
		for _, node := range opts.ExpectedNodes {
			if kernels[node] == "" {
				return fmt.Errorf("Ceph kernel evidence missing for %s", node)
			}
		}
	}
	var old []string
	for node, kernel := range kernels {
		if !kernelAtLeast(kernel, 7, 0) || strings.Contains(kernel, "-rc") {
			old = append(old, node+" ("+kernel+")")
		}
	}
	if len(old) == 0 {
		fmt.Fprintf(out, "[apply] Ceph kernel gate: PASS — %d node(s) run Linux 7.0 or later\n", len(kernels))
		return nil
	}
	sort.Strings(old)
	return fmt.Errorf("Ceph storage needs Linux 7.0 or later on every node (AES256K CephX keys); "+
		"older or release-candidate kernels: %s (FIPS hosts need 7.2). On Ubuntu 24.04 install the HWE kernel (linux-generic-hwe-24.04) and reboot each node, "+
		"then re-run init", strings.Join(old, ", "))
}

// kernelAtLeast compares the leading major.minor of a kernel release string.
// An unparseable version counts as too old.
func kernelAtLeast(release string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(release), ".", 3)
	if len(parts) < 2 {
		return false
	}
	leading := func(s string) (int, bool) {
		end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
		if end < 0 {
			end = len(s)
		}
		n, err := strconv.Atoi(s[:end])
		return n, err == nil
	}
	ma, ok1 := leading(parts[0])
	mi, ok2 := leading(parts[1])
	if !ok1 || !ok2 {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}
