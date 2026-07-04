package clusterinit

import (
	"context"
	"errors"
	"fmt"
)

// M4-T03 — auto mode detection.
//
// Per installer-prd §4.1.1, `kube-dc bootstrap init` picks one of
// three branches automatically by probing the current kubeconfig's
// cluster, with `--mode=<branch>` available as an explicit override.
//
// | Probe state                                                          | Branch    |
// |----------------------------------------------------------------------|-----------|
// | K8s reachable, no `flux-system`, no kube-dc workloads                | install   |
// | K8s reachable, `flux-system` present, no kube-dc-manager             | adopt     |
// | K8s reachable, `flux-system` + kube-dc-manager both present          | resume    |
//
// A fourth row in the PRD ("K8s API unreachable, RKE2 not installed
// on the host") routes to a v2-only branch; v1 errors loudly so the
// operator runs `bootstrap/rke2/install-server.sh` from the fleet
// repo first.
//
// **Two-layer split**: the pure decision function `DetectMode` is in
// this file and unit-tested with a small table. The runtime probe
// (`ModeProber`) is an interface so the cobra layer can plug in a
// real client-go-backed implementation while tests use a fake.

// ModeProbeInputs captures the three facts the mode detector needs.
// All booleans default to false (zero value); a missing input is
// always treated as "absent" which routes to the most-conservative
// branch (install).
type ModeProbeInputs struct {
	// K8sReachable is true when the kubeconfig's apiserver responded
	// to a basic `GET /api/v1/namespaces` (or equivalent) within the
	// probe timeout.
	K8sReachable bool

	// FluxSystemPresent is true when a `flux-system` namespace exists
	// in the cluster. Flux is the prerequisite for both adopt and
	// resume — its absence proves we're on a fresh kube-cluster.
	FluxSystemPresent bool

	// KubeDCManagerPresent is true when the kube-dc-manager workload
	// exists. v1 looks for either:
	//   - a Deployment named "kube-dc-manager" in the "kube-dc" ns, OR
	//   - a HelmRelease named "kube-dc" in the "kube-dc" ns.
	// Either is a strong signal kube-dc is installed; both being
	// false means we're pre-install or mid-adopt.
	KubeDCManagerPresent bool
}

// ErrK8sUnreachable is returned by DetectMode when the probe says
// the API server is unreachable. Auto-detection cannot proceed; the
// cobra layer surfaces this as a clear "pass --mode= explicitly or
// fix kubeconfig" message.
var ErrK8sUnreachable = errors.New("init: auto-detection failed — Kubernetes API unreachable; check kubeconfig or pass --mode= explicitly")

// DetectMode is the pure decision function. Given a ModeProbeInputs,
// it returns the canonical Mode + a one-line reason suitable for
// rendering in the plan header.
//
// Returns ErrK8sUnreachable when K8sReachable is false — auto-detect
// cannot pick a branch without a working apiserver.
func DetectMode(in ModeProbeInputs) (Mode, string, error) {
	if !in.K8sReachable {
		return "", "", ErrK8sUnreachable
	}
	switch {
	case !in.FluxSystemPresent:
		return ModeInstall, "no flux-system namespace — fresh Kubernetes, will install kube-dc from scratch", nil
	case !in.KubeDCManagerPresent:
		return ModeAdopt, "flux-system present but no kube-dc-manager — adopting an existing Flux-managed cluster", nil
	default:
		return ModeResume, "flux-system + kube-dc-manager both present — resuming a previous install", nil
	}
}

// ModeProber is the runtime contract the cobra layer depends on. The
// real implementation (`RealModeProber`, wired in the cobra package)
// queries the cluster via client-go; tests substitute fakes that
// return canned ModeProbeInputs.
//
// `Probe` accepts a context so the caller can apply a deadline; the
// timeout for the canonical "is K8s reachable" check is the caller's
// concern (the doctor pattern uses 5s).
type ModeProber interface {
	Probe(ctx context.Context) (ModeProbeInputs, error)
}

// ResolveMode is the orchestration entry point. When `o.Mode ==
// ModeAuto`, it runs the prober + DetectMode and substitutes the
// resolved value into `o.Mode`. For any other value, it's a no-op
// (the explicit operator override always wins, per
// installer-prd §4.1.1).
//
// On success, returns the resolved Mode + a human-readable reason
// suitable for the plan-render header ("Auto-detected: install (no
// flux-system namespace — …)"). On failure (prober error or K8s
// unreachable), returns the wrapping error so the cobra layer can
// surface it.
func ResolveMode(ctx context.Context, o *InitOptions, prober ModeProber) (Mode, string, error) {
	if o == nil {
		return "", "", fmt.Errorf("ResolveMode: nil options")
	}
	if o.Mode != ModeAuto {
		// Explicit override — no probe needed. Return the value as
		// the "resolved" mode so callers can uniformly log the
		// decision.
		return o.Mode, fmt.Sprintf("explicit --mode=%s (no auto-detect needed)", o.Mode), nil
	}
	if prober == nil {
		return "", "", fmt.Errorf("ResolveMode: --mode=auto requires a prober but none was provided")
	}
	in, err := prober.Probe(ctx)
	if err != nil {
		return "", "", fmt.Errorf("--mode=auto probe failed: %w", err)
	}
	mode, reason, err := DetectMode(in)
	if err != nil {
		return "", "", err
	}
	o.Mode = mode
	return mode, reason, nil
}
