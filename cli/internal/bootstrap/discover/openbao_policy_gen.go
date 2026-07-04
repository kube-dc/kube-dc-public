// Package discover: M5-T07 — openbao policy-generation drift probe.
//
// Thin adapter over `openbao.ReadPolicyGenerationInstalled` +
// `openbao.PolicyGeneration` that fits the ports.Probe shape so
// the M1 doctor can surface drift alongside its other cluster
// probes. The engine + render live in the openbao package (see
// openbao/policy_generation.go); this file is only about hooking
// the drift signal into the doctor's category machinery.
//
// **Severity ladder** — CLI never blocks on drift (M5-T07 plan):
//
//   - StatusInstalled + SeverityInfo — installed == expected.
//     Detail names both numbers so operators see "on generation N"
//     even without drift.
//
//   - StatusPartial   + SeverityInfo — installed > 0 but expected >
//     installed. Classic post-chart-upgrade drift; FixHint points
//     at `bootstrap openbao setup-controller-auth --refresh-policy`.
//
//   - StatusMissing   + SeverityInfo — installed == 0 (annotation
//     absent). Either legacy install pre-M5-T07 or setup-controller-
//     auth has never run. FixHint suggests the same refresh-policy
//     command (which will run + stamp the marker).
//
//   - StatusMissing   + SeverityWarn — adapter/parse error. Warn
//     because a hard read failure means we CAN'T characterise
//     drift; the operator should investigate rather than assume
//     "everything's fine".
//
// **Nil-safe** — NewPolicyGenerationProbe accepts nil OpenBaoClient
// so the doctor's fresh-laptop / no-kubeconfig path doesn't crash;
// Run then reports StatusMissing + SeverityInfo with a "not
// configured" detail (mirrors NFDProbe's nil-source handling).

package discover

import (
	"context"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// PolicyGenerationProbe wraps the openbao package's drift-detection
// engine as a ports.Probe. Constructed with an OpenBaoClient so the
// adapter dependency is explicit (matches NFDProbe's shape).
type PolicyGenerationProbe struct {
	bao ports.OpenBaoClient
}

// NewPolicyGenerationProbe constructs a probe. `bao` may be nil for
// call sites wired before the session is built; Run then reports
// StatusMissing + SeverityInfo so the doctor render doesn't crash.
func NewPolicyGenerationProbe(bao ports.OpenBaoClient) *PolicyGenerationProbe {
	return &PolicyGenerationProbe{bao: bao}
}

// Compile-time assertion.
var _ ports.Probe = (*PolicyGenerationProbe)(nil)

func (p *PolicyGenerationProbe) Name() string { return "openbao-policy-generation" }

// Run reads the installed generation from svc/openbao and compares
// with the compile-time openbao.PolicyGeneration constant. Never
// mutates cluster state.
func (p *PolicyGenerationProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	expected := openbao.PolicyGeneration
	if p.bao == nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   fmt.Sprintf("expected=%d installed=<unknown> (no OpenBaoClient wired — pre-session probe)", expected),
		}
	}
	installed, err := openbao.ReadPolicyGenerationInstalled(ctx, p.bao)
	if err != nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("read failed: %v", err),
			FixHint: ports.FixHint{
				Text: "inspect svc/openbao annotations manually: kubectl -n openbao get svc openbao -o yaml | grep openbao-policy-generation",
			},
		}
	}
	// Absent = installed 0. Treat as drift-from-legacy explicitly so
	// operators see clearer guidance than the generic "N-1 → N" case.
	if installed == 0 {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   fmt.Sprintf("expected=%d installed=<absent> (legacy install pre-M5-T07 or setup never run)", expected),
			FixHint: ports.FixHint{
				Text: "run: kube-dc bootstrap openbao setup-controller-auth <cluster> --refresh-policy",
			},
		}
	}
	if expected > installed {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityInfo,
			Detail:   fmt.Sprintf("expected=%d installed=%d — DRIFT: chart bumped controller-auth policies since last run", expected, installed),
			FixHint: ports.FixHint{
				Text: "run: kube-dc bootstrap openbao setup-controller-auth <cluster> --refresh-policy",
			},
		}
	}
	// expected == installed (or expected < installed, in which case
	// the operator's binary is older than what the cluster last
	// ran — StatusInstalled is still correct; the newer chart's
	// policies are in place, our stamp check just doesn't fire drift).
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   fmt.Sprintf("expected=%d installed=%d — up to date", expected, installed),
	}
}
