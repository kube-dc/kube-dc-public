package openbao

// M5-T04 — `kube-dc bootstrap openbao status`.
//
// Read-only projection of OpenBao's on-cluster state. Answers the
// question every operator asks first when something's off — "what
// state is bao in?" — without needing shares, root tokens, or a
// SOPS-decrypt step. Fully non-mutating; safe to run any time.
//
// Data sourced entirely through OpenBaoClient (M0-T04 port):
//   - PodList  → the openbao-N StatefulSet members
//   - Status   → per-pod `bao status` (Initialized, Sealed, Version,
//                HAMode, RaftIndex, ActiveNodeID)
//   - GetAnnotation → svc/openbao's two operational markers
//                (bootstrap-finalized + controller-auth-installed)
//
// Not needed here:
//   - SOPS (we don't decrypt anything)
//   - --repo (we don't touch the fleet overlay)
// but the parent `bootstrap openbao` subcommand still passes the
// persistent --repo flag; the cobra wrapper takes ClusterName as a
// display label and uses --repo only for a soft "cluster exists in
// the fleet" verify — no functional dependency.
//
// Exit-code contract at the cobra layer:
//   0 — every pod unsealed AND both markers present
//   1 — any pod sealed OR one/both markers missing (recoverable
//       operator state — the whole reason `status` exists is to
//       triage into `openbao unseal` / `openbao setup-controller-auth`)
//   2 — structural failure (no pods enumerable; apiserver unreachable;
//       nil dependency)

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// StatusOptions is the parameter bundle for Status. ClusterName is a
// display label only (see file header). OpenBao is the live port
// (real adapter or scenario mock). Out captures the operator log.
type StatusOptions struct {
	ClusterName string
	OpenBao     ports.OpenBaoClient
	Out         io.Writer
}

// StatusResult is the structured summary the engine returns. The
// cobra layer maps this into the operator-facing text; other
// consumers (future TUI / status API) can render however they like.
// Pods is ordered as OpenBaoClient.PodList returned it (deterministic
// per adapter — real adapter sorts by name).
type StatusResult struct {
	// ClusterName is the display label the caller supplied.
	ClusterName string

	// Pods holds one entry per OpenBao pod, in the order PodList
	// returned. Empty when no OpenBao pods exist.
	Pods []ports.BaoStatus

	// BootstrapFinalized is the value of the
	// kube-dc.com/openbao-bootstrap-finalized annotation on
	// svc/openbao. Empty when the annotation is absent — a signal
	// that init hasn't been run (or Phase B bailed).
	BootstrapFinalized string

	// ControllerAuthInstalled is the value of the
	// kube-dc.com/openbao-controller-auth-installed annotation on
	// svc/openbao. Empty when the annotation is absent — a signal
	// that Phase C hasn't been run (init bailed after B, or the
	// cluster is a pre-M5-T08 legacy install).
	ControllerAuthInstalled string

	// PolicyGenerationInstalled is the value of the M5-T07
	// kube-dc.com/openbao-policy-generation annotation. Zero when
	// the annotation is absent — either a legacy install pre-M5-T07
	// or setup-controller-auth has never run. Compare against
	// PolicyGenerationExpected via HasPolicyGenerationDrift.
	PolicyGenerationInstalled int

	// PolicyGenerationExpected is the compile-time PolicyGeneration
	// constant this binary knows how to install. Snapshotted at
	// Status() call time so consumers rendering the result don't
	// have to import the openbao package to compare.
	PolicyGenerationExpected int
}

// AnySealed returns true when at least one pod reports Sealed=true.
// Convenience helper for the cobra layer's exit-code decision — kept
// on the result rather than duplicated at every call site.
func (r StatusResult) AnySealed() bool {
	for _, p := range r.Pods {
		if p.Sealed {
			return true
		}
	}
	return false
}

// AnyUninitialized returns true when any pod reports
// Initialized=false. On a healthy multi-pod HA install this is 0;
// non-zero signals either the OrderedReady race (fresh followers
// haven't joined the leader's Raft cluster yet — recover via
// `openbao unseal` which auto-raft-joins) or a wholly-uninitialised
// cluster (no `bootstrap openbao init` yet). Use `AllUninitialized`
// to distinguish the two — the recovery command differs.
func (r StatusResult) AnyUninitialized() bool {
	for _, p := range r.Pods {
		if !p.Initialized {
			return true
		}
	}
	return false
}

// AllUninitialized returns true iff every pod reports
// Initialized=false — the never-initialized-cluster state. Distinct
// from AnyUninitialized so the triage renderer can distinguish
// "cluster has never seen `openbao init`" (all-uninit → run init)
// from "OrderedReady race — followers spun up before the leader
// finished raft-join" (some-uninit + some-init → run unseal, which
// auto-raft-joins). Returns false when Pods is empty — an empty
// slice is a structural failure, not a never-init state.
func (r StatusResult) AllUninitialized() bool {
	if len(r.Pods) == 0 {
		return false
	}
	for _, p := range r.Pods {
		if p.Initialized {
			return false
		}
	}
	return true
}

// FullyReady returns true iff every pod is Initialized + unsealed
// AND both annotation markers are present. The canonical "green"
// state the cobra layer maps to exit 0.
//
// **Policy-generation drift is NOT included in FullyReady** — the
// M5-T07 plan says the CLI never blocks on drift; the operator
// decides when to run refresh-policy. So a cluster with drift but
// otherwise green stays exit 0; the drift shows up as an
// operator-visible advisory line in the status renderer + doctor
// probe, but doesn't change the CI gate contract.
func (r StatusResult) FullyReady() bool {
	if len(r.Pods) == 0 {
		return false
	}
	if r.AnySealed() || r.AnyUninitialized() {
		return false
	}
	return r.BootstrapFinalized != "" && r.ControllerAuthInstalled != ""
}

// HasPolicyGenerationDrift returns true iff the compile-time
// PolicyGeneration is ahead of the annotation-stamped value —
// signals the operator should run `openbao setup-controller-auth
// --refresh-policy` to close the gap. Zero installed with expected
// >= 1 counts as drift (legacy install pre-M5-T07); the render + doctor
// probe describe that case with slightly different language than
// the "you're on N-1" case but both take the same recovery action.
func (r StatusResult) HasPolicyGenerationDrift() bool {
	return r.PolicyGenerationExpected > r.PolicyGenerationInstalled
}

// --- Errors ---

// ErrStatusMissingDependency surfaces on nil OpenBao adapter.
var ErrStatusMissingDependency = errors.New("openbao status: missing required dependency")

// ErrStatusNoPodsFound signals PodList returned an empty slice — a
// structural failure (no HelmRelease reconciled? RBAC filtered?),
// not a recoverable operator state.
var ErrStatusNoPodsFound = errors.New("openbao status: no OpenBao pods found (HelmRelease not yet Ready or RBAC-filtered?)")

// --- Engine ---

// Status walks the openbao StatefulSet + the svc/openbao annotations
// and returns a structured StatusResult. Never mutates cluster
// state. The cobra layer decides exit code from the returned result
// (nil result means the returned error is authoritative).
//
// Errors surfaced:
//   - ErrStatusMissingDependency — programmer error (nil adapter).
//   - ErrStatusNoPodsFound — no openbao pods enumerable.
//   - Any adapter-level error from PodList / Status / GetAnnotation
//     is wrapped and returned verbatim so operators see the real
//     apiserver / kubectl-exec failure.
func Status(ctx context.Context, opts StatusOptions) (StatusResult, error) {
	if opts.OpenBao == nil {
		return StatusResult{}, fmt.Errorf("%w: OpenBao", ErrStatusMissingDependency)
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	res := StatusResult{ClusterName: opts.ClusterName}

	pods, err := opts.OpenBao.PodList(ctx)
	if err != nil {
		return StatusResult{}, fmt.Errorf("openbao status: list pods: %w", err)
	}
	if len(pods) == 0 {
		return StatusResult{}, ErrStatusNoPodsFound
	}

	res.Pods = make([]ports.BaoStatus, 0, len(pods))
	for _, pod := range pods {
		st, err := opts.OpenBao.Status(ctx, pod)
		if err != nil {
			return StatusResult{}, fmt.Errorf("openbao status: %s: %w", pod, err)
		}
		st.Pod = pod
		res.Pods = append(res.Pods, st)
	}

	// Annotations are best-effort — an adapter error here shouldn't
	// hide the pod state we already collected. But a HARD read
	// failure (apiserver 403, service missing) means the cluster is
	// in a state where the status output would be misleading, so
	// surface it. The "annotation absent" case (empty value + nil
	// error) is the normal signal path.
	bf, err := opts.OpenBao.GetAnnotation(ctx, "openbao", AnnotationBootstrapFinalized)
	if err != nil {
		return StatusResult{}, fmt.Errorf("openbao status: read %s: %w", AnnotationBootstrapFinalized, err)
	}
	res.BootstrapFinalized = bf

	cai, err := opts.OpenBao.GetAnnotation(ctx, "openbao", AnnotationControllerAuthInstalled)
	if err != nil {
		return StatusResult{}, fmt.Errorf("openbao status: read %s: %w", AnnotationControllerAuthInstalled, err)
	}
	res.ControllerAuthInstalled = cai

	// M5-T07 policy-generation drift signal. Adapter/parse errors
	// surface (same posture as the other annotations — a hard read
	// failure would make the drift-vs-current comparison misleading);
	// a MISSING annotation returns (0, nil) from
	// ReadPolicyGenerationInstalled, which correctly represents
	// "legacy install / never stamped" and lets the render display
	// drift against expected >= 1.
	installed, err := ReadPolicyGenerationInstalled(ctx, opts.OpenBao)
	if err != nil {
		return StatusResult{}, fmt.Errorf("openbao status: %w", err)
	}
	res.PolicyGenerationInstalled = installed
	res.PolicyGenerationExpected = PolicyGeneration

	return res, nil
}
