// M6-T05 — NFD-derived KubeVirt-eligibility gate for
// `bootstrap init --apply`.
//
// Sits AFTER the M4-T08 DNS gate and before the engine's Apply so
// that when the target cluster demonstrably cannot host VMs (NFD is
// running, composites are applied, and yet zero nodes carry
// kube-dc.com/kubevirt-eligible=true) we refuse to hand the operator
// an install that will silently fail every VM schedule. Kube-DC's
// whole product surface (Organizations → Projects → managed-K8s
// clusters → developer VMs) assumes at least one node can boot a
// KubeVirt VirtualMachineInstance; landing the platform on a cluster
// with none is an install-time misconfiguration worth catching
// before the reconcilers churn on it.
//
// **Not run when NFD is absent**: on a fresh-RKE2 install where
// infra-core hasn't landed yet, NFD's DaemonSet doesn't exist and
// NFDDetect reports NFDInstalled=false. That's the expected state
// pre-Apply — Apply is what INSTALLS NFD (via the M6-T01
// HelmRelease) — so we skip the gate rather than fire it. The
// signal we care about is "NFD says the hardware can't host VMs",
// not "NFD hasn't been deployed yet."
//
// **Not run when the target cluster is unreachable**: NFDDetect
// error → soft skip (same rationale — the DNS gate already caught
// pre-Apply plumbing issues; a K8sClient error here at Apply-time is
// usually the target cluster not being up yet).
//
// **Escape hatch**: `--allow-no-kubevirt-eligible`. Operator takes
// on the promise "I know this cluster can't run VMs today; I'm
// installing kube-dc for the multi-tenancy / GitOps surface without
// VMs, or I'll enable nested virt on the underlying hypervisor
// later." Part of the input hash so `--apply-plan` from a dry-run
// that had the flag matches an apply run that also has it.
//
// Reader is discover.NFDDetect (M1-T04). This gate is a THIN wrapper
// around that reader + a policy decision — the reader itself is
// re-used by M6-T03 (doctor row) and M6-T04 (status Nodes section)
// so cross-consumer behaviour stays consistent.

package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// ErrNoKubeVirtEligibleNodes is returned when NFD is installed, has
// been fully labeled (composites present or raw-fallback derivation
// succeeded), yet reports zero kubevirt-eligible nodes AND the
// operator did not opt out via --allow-no-kubevirt-eligible.
var ErrNoKubeVirtEligibleNodes = errors.New("init: no KubeVirt-eligible nodes")

// NFDGateOptions is the parameter bundle for CheckKubeVirtEligibility.
// K8s is a NodeLabelsProvider — ports.K8sClient satisfies it at
// runtime; using the smaller interface keeps tests hermetic (fake
// gets 3 lines instead of the full K8sClient surface). Nil on the
// fresh-RKE2 pre-Apply path where the cluster isn't up yet — gate
// soft-skips. Out is the operator log; nil is safe (io.Discard).
type NFDGateOptions struct {
	K8s                     discover.NodeLabelsProvider
	AllowNoKubevirtEligible bool
	Out                     io.Writer
}

// CheckKubeVirtEligibility resolves the target cluster's NFD state
// via the M1-T04 reader and applies the policy:
//
//   - K8s nil → soft-skip (pre-Apply fresh cluster).
//   - NFDDetect error → soft-skip (target cluster not reachable yet).
//   - NFDInstalled=false → soft-skip (Apply will install it).
//   - KubeVirtEligibleCount == 0 → BLOCK (unless AllowNoKubevirtEligible).
//   - KubeVirtEligibleCount >= 1 → PASS, log the count + source.
//
// Never mutates cluster state. Safe to run multiple times.
func CheckKubeVirtEligibility(ctx context.Context, opts NFDGateOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.K8s == nil {
		fmt.Fprintln(out, "[apply] NFD gate: skipped (no K8sClient — target cluster not observable yet)")
		return nil
	}
	r, err := discover.NFDDetect(ctx, opts.K8s)
	if err != nil {
		fmt.Fprintf(out, "[apply] NFD gate: skipped (NFD probe failed: %v)\n", err)
		return nil
	}
	if !r.NFDInstalled {
		fmt.Fprintln(out, "[apply] NFD gate: skipped (NFD not installed yet — Apply will install it via M6-T01)")
		return nil
	}
	source := "M6-T02 composites"
	if !r.CompositesInstalled {
		source = "raw NFD labels (composites absent — install M6-T02 for authoritative labels)"
	}
	if r.KubeVirtEligibleCount() == 0 {
		if opts.AllowNoKubevirtEligible {
			fmt.Fprintf(out,
				"[apply] NFD gate: 0 KubeVirt-eligible nodes across %d nodes (source: %s)\n",
				r.TotalNodes, source)
			fmt.Fprintln(out, "[apply]   --allow-no-kubevirt-eligible set; proceeding.")
			fmt.Fprintln(out, "[apply]   VM workloads (KubeVirt VMIs / managed-K8s worker VMs) will fail to schedule until at least one node exposes /dev/kvm.")
			return nil
		}
		return fmt.Errorf(
			"%w: 0 of %d nodes have kube-dc.com/kubevirt-eligible=true (source: %s); enable nested virt on the underlying hypervisor and load the `kvm` kernel module on each node, or pass --allow-no-kubevirt-eligible to proceed anyway",
			ErrNoKubeVirtEligibleNodes, r.TotalNodes, source)
	}
	fmt.Fprintf(out,
		"[apply] NFD gate: PASS — %d/%d nodes KubeVirt-eligible (source: %s)\n",
		r.KubeVirtEligibleCount(), r.TotalNodes, source)
	return nil
}
