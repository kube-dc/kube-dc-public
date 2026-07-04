// Package discover: M1-T04 — Node Feature Discovery (NFD) label
// consumer.
//
// NFD ships two Kubernetes components (see
// github.com/kubernetes-sigs/node-feature-discovery):
//
//   - nfd-worker (DaemonSet) probes each node's CPU flags, PCI/USB
//     devices, kernel modules, memory topology, and any operator-
//     authored NodeFeatureRules; emits results to the master.
//   - nfd-master (Deployment) aggregates worker reports + applies
//     NodeFeatureRule CRs, then writes labels onto the Node object.
//
// **Raw NFD labels** are the built-in feature.node.kubernetes.io/*
// set (e.g. cpu-cpuid.VMX, cpu-cpuid.SVM, kernel-module.kvm,
// pci-<vendor>.present). These land automatically on every cluster
// that installs NFD, regardless of any customisation.
//
// **Composite labels** are what M6-T02 will author via
// NodeFeatureRule CRs — three kube-dc-specific rollups that answer
// the questions kube-dc actually cares about:
//
//   - kube-dc.com/kubevirt-eligible=true
//     (nested-KVM capable: VMX or SVM present AND kvm module loaded)
//   - kube-dc.com/gpu.nvidia=true
//     (any NVIDIA PCI 03xx device visible on the node)
//   - kube-dc.com/gpu.amd=true
//     (any AMD PCI 03xx device visible on the node)
//
// This file exposes:
//
//   - `NFDResult` — the structured, machine-consumable summary that
//     M6-T04 status renderer and M6-T05 init preset validator will
//     use (they need counts + node lists, not just a doctor row).
//   - `NFDDetect(ctx, k8s)` — the pure detection function that
//     produces an NFDResult. Isolated so preset validation and
//     status can call it directly without instantiating the doctor
//     probe machinery.
//   - `NFDProbe` — the `ports.Probe` wrapper the M6-T03 doctor
//     consumer will slot into `assembleProbes` (as a
//     `CategoryPhysical` / `CategoryVerifiesSuggests` entry).
//
// **Composites-preferred / raw-fallback rule** — when the
// M6-T02 composites exist on the cluster, NFDDetect uses them
// verbatim (single source of truth; matches what an operator would
// see on `kubectl get nodes --show-labels`). When they DON'T exist
// but NFD itself is running, NFDDetect derives best-effort counts
// from the raw labels so an operator running vanilla NFD without
// our NodeFeatureRules still gets a useful report. The result
// carries a `CompositesInstalled` flag so consumers can surface
// "install M6-T02 for authoritative labels" when they want to.
//
// **Not deployed anywhere** — this file is the READER; the WRITER
// (NFD HelmRelease) is M6-T01 and the rule set is M6-T02, both
// unshipped as of this slice. On a cluster without NFD, NFDDetect
// returns NFDResult{NFDInstalled: false} + a probe result of
// StatusMissing with a FixHint pointing at "install NFD via
// M6-T01's fleet manifest". That's the right shape for M6-T03/T04
// consumers to render "Nodes: NFD not installed" without hard-
// failing.

package discover

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// NFDResult is the structured payload consumers use. Node lists (not
// just counts) so error messages can name the specific nodes — e.g.
// "cloud-vlan preset requires ≥3 kubevirt-eligible nodes; found 2:
// [srv1-kub1, srv2-kub1]".
type NFDResult struct {
	// NFDInstalled is true iff at least one Node carries any
	// feature.node.kubernetes.io/* label. Absence is a strong signal
	// that NFD's DaemonSet isn't running — nfd-worker labels appear
	// within seconds of a node coming up under NFD.
	NFDInstalled bool

	// CompositesInstalled is true iff at least one Node carries any
	// kube-dc.com/* label matching the M6-T02 composite set. When
	// false but NFDInstalled is true, the raw-fallback derivation
	// path is in play; consumers should surface a "install M6-T02
	// for authoritative labels" hint.
	CompositesInstalled bool

	// TotalNodes is the count of nodes NodeLabels returned. Present
	// even when NFDInstalled is false so a caller can still say
	// "3 nodes, no NFD".
	TotalNodes int

	// KubeVirtEligibleNodes lists the node names capable of running
	// VMs. Sorted alphabetically for stable rendering.
	KubeVirtEligibleNodes []string

	// NvidiaGPUNodes lists nodes with an NVIDIA GPU present.
	// Sorted alphabetically.
	NvidiaGPUNodes []string

	// AMDGPUNodes lists nodes with an AMD GPU present. Sorted
	// alphabetically.
	AMDGPUNodes []string
}

// KubeVirtEligibleCount / NvidiaGPUCount / AMDGPUCount are shortcuts
// for consumers that want counts without slicing the string lists.
// (Preset validation frequently just needs "≥ N"; keeping the count
// helpers separates the "did you meet the count?" from "which nodes
// did?" concerns.)
func (r NFDResult) KubeVirtEligibleCount() int { return len(r.KubeVirtEligibleNodes) }
func (r NFDResult) NvidiaGPUCount() int        { return len(r.NvidiaGPUNodes) }
func (r NFDResult) AMDGPUCount() int           { return len(r.AMDGPUNodes) }

// Composite label keys — the M6-T02 rule set will emit these. Kept
// as constants so a future rename is grep-able across consumers.
const (
	compositeKubeVirtEligible = "kube-dc.com/kubevirt-eligible"
	compositeGPUNvidia        = "kube-dc.com/gpu.nvidia"
	compositeGPUAMD           = "kube-dc.com/gpu.amd"
)

// Raw NFD label prefix — any node carrying a label with this prefix
// signals NFD is running.
const rawNFDPrefix = "feature.node.kubernetes.io/"

// Raw NFD label keys that participate in the kubevirt-eligible
// derivation when composites aren't installed.
//
// The KVM check requires TWO conditions to hold on a node:
//
//   1. Hardware virtualization is supported by the CPU:
//        Intel VT-x → cpu-cpuid.VMX
//        AMD-V      → cpu-cpuid.SVM
//      NFD sets these labels from /proc/cpuinfo flags.
//
//   2. The Linux `kvm` kernel module is loaded:
//        NFD may label this as `kernel-module.kvm` or
//        `kernel-loadedmodule.kvm` depending on which feature
//        source is enabled (loadedmodule is more recent). We
//        accept either alias so this fallback isn't fragile to
//        NFD's own version drift.
//
// **Not checked here** (deferred to the M6-T02 composite):
//   - The arch-specific submodule (`kvm_intel` for Intel,
//     `kvm_amd` for AMD) — in practice `modprobe kvm-intel`
//     cascade-loads the parent `kvm`, so if `kvm` is present the
//     submodule usually is too.
//   - /dev/kvm accessibility (kernel labels can't reflect device
//     permissions — a strict M6-T02 rule can `nsenter` and probe
//     the device node).
//   - Nested-virt-enabled state on the parent hypervisor when the
//     node itself is a VM.
//
// So the raw fallback's rule is:
//
//     kubevirt-eligible = (VMX ∨ SVM) ∧ (kvm-module ∨ kvm-loadedmodule)
//
// **Over-count risk**: VMX+kvm true, but kvm_intel not actually
// loaded (rare — implies an operator ran `modprobe kvm` by hand
// without the arch sub); KubeVirt VMI schedule would fail at
// runtime. M6-T02's composite catches this; raw doesn't.
//
// **Under-count risk**: none — every VM-capable node in the wild
// has both flags set.
const (
	rawCPUIDVMX      = "feature.node.kubernetes.io/cpu-cpuid.VMX"
	rawCPUIDSVM      = "feature.node.kubernetes.io/cpu-cpuid.SVM"
	rawKernelModKVM  = "feature.node.kubernetes.io/kernel-module.kvm"
	rawKernelModKVM2 = "feature.node.kubernetes.io/kernel-loadedmodule.kvm"
)

// GPU vendor IDs as they appear in NFD's raw pci-*.present labels.
// NFD's config decides the exact shape: default is
// `pci-<class>_<vendor>.present`, but common tenant configs also
// enable `pci-<vendor>.present` (any class). We match on substring
// so both shapes are recognised.
const (
	pciVendorNvidia = "10de" // NVIDIA Corp.
	pciVendorAMD    = "1002" // Advanced Micro Devices, Inc.
)

// NodeLabelsProvider is the minimal interface NFDDetect needs. Kept
// separate from ports.K8sClient so callers wiring the pure detector
// don't have to synthesise the full K8sClient surface (tests
// especially benefit — the fake gets 3 lines instead of 20).
// ports.K8sClient satisfies this interface at runtime.
type NodeLabelsProvider interface {
	NodeLabels(ctx context.Context) (map[string]map[string]string, error)
}

// NFDDetect is the pure detection entrypoint. Given a NodeLabels
// source, returns the structured NFDResult. Never mutates cluster
// state; safe to call from any consumer (doctor, status, init
// preset validator). Returns a wrapped error only when NodeLabels
// itself fails — an empty node list is a valid state (fresh
// cluster; NFDResult.TotalNodes == 0 tells the story).
func NFDDetect(ctx context.Context, src NodeLabelsProvider) (NFDResult, error) {
	if src == nil {
		return NFDResult{}, fmt.Errorf("nfd: NodeLabelsProvider is nil")
	}
	labels, err := src.NodeLabels(ctx)
	if err != nil {
		return NFDResult{}, fmt.Errorf("nfd: NodeLabels: %w", err)
	}
	r := NFDResult{TotalNodes: len(labels)}

	// First pass: detect NFD + composites installation state so we
	// know whether to use composites or fall back to raw labels.
	//
	// Composites imply NFD — they can only be computed by an
	// NFD-authored NodeFeatureRule, so a node carrying a kube-dc.com
	// composite label must be under NFD's management even if the
	// raw feature.node.kubernetes.io/* labels aren't visible in a
	// scoped read (test fixtures, filtered RBAC, etc.).
	for _, node := range labels {
		for k := range node {
			if strings.HasPrefix(k, rawNFDPrefix) {
				r.NFDInstalled = true
			}
			if k == compositeKubeVirtEligible || k == compositeGPUNvidia || k == compositeGPUAMD {
				r.CompositesInstalled = true
				r.NFDInstalled = true // composites can only exist under NFD
			}
		}
		if r.NFDInstalled && r.CompositesInstalled {
			break // no need to keep scanning
		}
	}

	// Second pass: fill the per-node capability lists. Deterministic
	// alphabetical order — matters for stable snapshot tests and
	// consumer-facing rendering (M6-T03 doctor row, M6-T05 preset
	// error messages).
	nodeNames := make([]string, 0, len(labels))
	for name := range labels {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)

	for _, name := range nodeNames {
		node := labels[name]
		if isKubeVirtEligible(node, r.CompositesInstalled) {
			r.KubeVirtEligibleNodes = append(r.KubeVirtEligibleNodes, name)
		}
		if hasVendorGPU(node, r.CompositesInstalled, compositeGPUNvidia, pciVendorNvidia) {
			r.NvidiaGPUNodes = append(r.NvidiaGPUNodes, name)
		}
		if hasVendorGPU(node, r.CompositesInstalled, compositeGPUAMD, pciVendorAMD) {
			r.AMDGPUNodes = append(r.AMDGPUNodes, name)
		}
	}
	return r, nil
}

// isKubeVirtEligible resolves a single node's kubevirt eligibility.
// Composites-preferred: if M6-T02's rule is deployed, its verdict is
// authoritative. Fallback: raw VMX-or-SVM + kvm-module (best-effort;
// may over-count vs a strict M6-T02 rule that also checks e.g.
// /dev/kvm accessibility — that's why the composite exists).
func isKubeVirtEligible(node map[string]string, compositesInstalled bool) bool {
	if compositesInstalled {
		return node[compositeKubeVirtEligible] == "true"
	}
	hasVirt := node[rawCPUIDVMX] == "true" || node[rawCPUIDSVM] == "true"
	hasKVM := node[rawKernelModKVM] == "true" || node[rawKernelModKVM2] == "true"
	return hasVirt && hasKVM
}

// hasVendorGPU resolves a node's GPU-by-vendor. Composites-preferred:
// if the composite label is set to "true", return true. Fallback:
// substring-match against any pci-* label containing the vendor ID.
// The substring match handles both NFD-default `pci-<class>_<vendor>`
// shape and the any-class `pci-<vendor>` shape without operator
// intervention.
func hasVendorGPU(node map[string]string, compositesInstalled bool, compositeKey, vendorID string) bool {
	if compositesInstalled {
		return node[compositeKey] == "true"
	}
	for k, v := range node {
		if !strings.HasPrefix(k, rawNFDPrefix+"pci-") {
			continue
		}
		if v != "true" {
			continue
		}
		if strings.Contains(k, vendorID) {
			return true
		}
	}
	return false
}

// ---------- NFDProbe ----------

// NFDProbe wraps NFDDetect as a ports.Probe so doctor / status can
// treat it uniformly with the M1-T01..T03 probe fleet. Consumers
// pull the structured NFDResult via NFDDetect directly when they
// need programmatic access (M6-T05 preset validator); the probe
// wrapper is for the doctor-row shape (M6-T03).
type NFDProbe struct {
	k8s NodeLabelsProvider
}

// NewNFDProbe constructs a probe. k8s may be nil for constructors
// that are wired before the session lands (doctor's mock path); Run
// returns a "not configured" warning in that case.
func NewNFDProbe(k8s NodeLabelsProvider) *NFDProbe {
	return &NFDProbe{k8s: k8s}
}

// Compile-time assertion.
var _ ports.Probe = (*NFDProbe)(nil)

func (p *NFDProbe) Name() string { return "nfd" }

// Run produces a doctor-row summary of the NFD state. Severity
// ladder:
//
//   - StatusMissing + SeverityWarn — no NFD on the cluster. Warn
//     rather than Blocker because M6 is optional in v1 (installer
//     works without NFD; init just gives up on preset validation's
//     node-count checks).
//   - StatusPartial + SeverityInfo — NFD installed but M6-T02
//     composites absent. Info, not Warn, because the raw fallback
//     path still produces usable output for M6-T03..T05.
//   - StatusInstalled + SeverityInfo — NFD + composites both live.
//     Detail names the counts.
func (p *NFDProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	if p.k8s == nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "NodeLabelsProvider not configured (internal wiring bug)",
		}
	}
	r, err := NFDDetect(ctx, p.k8s)
	if err != nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("NFD probe: %v", err),
		}
	}
	if !r.NFDInstalled {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("NFD not installed (%d nodes)", r.TotalNodes),
			FixHint: ports.FixHint{
				Text: "Install NFD via M6-T01's HelmRelease (fleet: infrastructure/core/node-features/). See kube-dc-fleet/docs/prd/installer-agentic-tracker.md §M6.",
			},
		}
	}
	if !r.CompositesInstalled {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityInfo,
			Detail: fmt.Sprintf(
				"NFD running; M6-T02 composites absent (using raw fallback: %d kubevirt-eligible, %d NVIDIA GPU, %d AMD GPU across %d nodes)",
				r.KubeVirtEligibleCount(), r.NvidiaGPUCount(), r.AMDGPUCount(), r.TotalNodes,
			),
			FixHint: ports.FixHint{
				Text: "Apply the M6-T02 NodeFeatureRule set for authoritative composite labels.",
			},
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail: fmt.Sprintf(
			"NFD + composites: %d kubevirt-eligible, %d NVIDIA GPU, %d AMD GPU across %d nodes",
			r.KubeVirtEligibleCount(), r.NvidiaGPUCount(), r.AMDGPUCount(), r.TotalNodes,
		),
	}
}
