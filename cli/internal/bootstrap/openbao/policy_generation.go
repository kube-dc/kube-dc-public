package openbao

// M5-T07 — policy-generation compile-time marker (B-001 resolution).
//
// The controller-tier policies + roles (ManagerPolicy, DBManagerPolicy,
// their k8s-host auth-role bindings) evolve across kube-dc releases —
// new HCL paths for new engines, rotated SA names, changed TTLs. When
// a chart upgrade extends the policy set, EXISTING clusters carry the
// OLDER policies until the operator re-runs `openbao setup-controller-
// auth --refresh-policy`.
//
// Detecting that drift needs a version cookie the CLI can compare
// against a marker stamped on the cluster:
//
//   - `PolicyGeneration` — compile-time constant, bumped in the
//     SAME PR that extends ManagerPolicyHCL / DBManagerPolicyHCL /
//     ManagerRoleParams / DBManagerRoleParams. Every kube-dc binary
//     carries the generation number it knows how to install.
//
//   - `AnnotationPolicyGeneration` — Kubernetes annotation on
//     svc/openbao. Stamped by SetupControllerAuth after every
//     successful run (Full OR RefreshPolicy — the whole point of
//     Refresh is to close this drift, so it MUST bump the stamp).
//
//   - `ReadPolicyGenerationInstalled(ctx, bao)` — the read side.
//     Absent annotation → 0 (represents "legacy install pre-M5-T07");
//     malformed value → 0 with a wrapped error so callers can
//     distinguish "never set" from "adapter/parse failed".
//
// Drift is `PolicyGeneration > installed`. Consumers:
//
//   - `bootstrap openbao status` (M5-T04) renders "Policy generation:
//     N (installed: M) — DRIFT: run refresh-policy" under drift, "up
//     to date" otherwise.
//   - M1 doctor's `PolicyGenerationProbe` (this file's `NewProbe`)
//     surfaces the same signal in the doctor's Verifies+Suggests
//     section with the `refresh-policy` FixHint.
//
// **CLI never blocks on drift** — the operator decides when to run
// refresh-policy. Drift is StatusPartial + SeverityInfo, not Blocker.
// Rationale: a chart-published PolicyGeneration bump can arrive on
// an operator's laptop days before the cluster's next planned
// maintenance window; refusing to run any other subcommand until
// they refresh would be the wrong ergonomic.
//
// **Bump policy** — increment PolicyGeneration by 1 in the SAME PR
// that changes any of: ManagerPolicyHCL, DBManagerPolicyHCL,
// ManagerRoleParams(), DBManagerRoleParams(), ManagerSAName,
// DBManagerSAName, ManagerSAns, DBManagerSAns, or the KubernetesAuth
// config-write behaviour. Skipping the bump means clusters that ran
// the PRIOR generation will silently carry the OLD policies with
// no drift signal.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// PolicyGeneration is the compile-time generation number this
// binary knows how to install. Bump alongside any change to the
// controller-tier policies/roles (see file header).
//
// **Generation history**:
//   1 — Initial cookie. Corresponds to the M5-T08 policy set:
//       ManagerPolicyHCL + DBManagerPolicyHCL + their k8s-host
//       roles + the standard TokenTTL/TokenMaxTTL. First stamped
//       by clusters running kube-dc CLI at M5-T07 landing (2026-07-03).
const PolicyGeneration = 1

// AnnotationPolicyGeneration stores the last-stamped generation
// number on svc/openbao. Distinct from AnnotationControllerAuthInstalled
// (which stores an RFC3339 timestamp — WHEN setup ran) so the two
// signals can evolve independently. A cluster where setup ran but
// the CLI bumped PolicyGeneration since then carries a truthful
// installed-timestamp but a stale installed-generation.
const AnnotationPolicyGeneration = "kube-dc.com/openbao-policy-generation"

// ReadPolicyGenerationInstalled reads the currently-stamped
// generation off svc/openbao. Returns (0, nil) when the annotation
// is absent — the canonical "legacy install pre-M5-T07" signal;
// callers interpret 0 vs the compile-time PolicyGeneration to
// detect drift.
//
// Returns (0, err) for adapter failures (apiserver unreachable,
// RBAC denied) OR malformed values (non-integer, negative,
// overflowing int). The distinction between "absent" and "parse
// failed" lets consumers render different guidance — absent is
// "run setup-controller-auth (or --refresh-policy)"; malformed is
// "manually inspect svc/openbao annotations".
func ReadPolicyGenerationInstalled(ctx context.Context, bao ports.OpenBaoClient) (int, error) {
	if bao == nil {
		return 0, fmt.Errorf("openbao: ReadPolicyGenerationInstalled: OpenBaoClient is nil")
	}
	raw, err := bao.GetAnnotation(ctx, "openbao", AnnotationPolicyGeneration)
	if err != nil {
		return 0, fmt.Errorf("openbao: read %s: %w", AnnotationPolicyGeneration, err)
	}
	if raw == "" {
		// Absent — legacy install / never stamped.
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("openbao: parse %s=%q: %w", AnnotationPolicyGeneration, raw, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("openbao: negative generation %d in %s — legacy stamp corruption?", n, AnnotationPolicyGeneration)
	}
	return n, nil
}
