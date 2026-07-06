package clusterinit

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
)

// Item 5 — `init --mode=adopt` consumes the resolved adopt plan.
//
// When the operator runs `kube-dc bootstrap init --mode=adopt`, kube-dc
// is being layered onto a cluster that ALREADY runs some of the
// components it would otherwise install (cert-manager, kube-ovn,
// kubevirt, …). The fleet's Kustomizations run prune:false + force:true,
// so Flux ADOPTS each running Helm release in place rather than deleting
// it — but the FIRST reconcile will still UPGRADE/RESTART a component if
// cluster-config.env pins a version different from the one running.
//
// The safe workflow is: `bootstrap adopt <cluster> --pin-versions` FIRST
// (pins cluster-config.env to the live versions), THEN `init`. This gate
// enforces that ordering rather than silently re-detecting and guessing:
// it re-runs the SAME adopt.PinVersions engine and FAILS CLOSED when any
// pre-existing component would drift (a pending pin) or is undetected. It
// is strictly read-only — no mutation, no overlay surgery.

// AdoptGateOptions configures the adopt-safety preflight.
type AdoptGateOptions struct {
	// Inspector is the live-cluster reader (session.K8s). Nil → soft-skip
	// (e.g. a mock apply with no cluster): behavior is unchanged.
	Inspector adopt.Inspector
	// Env is the effective cluster-config.env the apply will write —
	// the on-disk file with any `--set KEY=VALUE` overrides layered on
	// top. Nil → soft-skip.
	Env adopt.EnvReader
	// Allow downgrades a hard failure to a warning (--allow-unpinned-adopt).
	Allow bool
	// ClusterName is only used to render the exact remediation command.
	ClusterName string
	Out         io.Writer
}

// CheckAdoptPinned verifies that every pre-existing component kube-dc
// would install is version-pinned to its LIVE version in
// cluster-config.env, so Flux's first reconcile adopts it in place
// without an upgrade/restart. It re-runs adopt.PinVersions (the same
// engine as `bootstrap adopt --pin-versions`) with NO escape options —
// at init time the operator is expected to have already resolved every
// component. Outcomes:
//
//   - all pre-existing components already pinned (or none present) → pass
//     (prints a one-line adopt-safe note when any were adopted).
//   - one or more would drift (pending pin) or are undetected → FAIL
//     CLOSED with the `bootstrap adopt --pin-versions` remediation,
//     unless opts.Allow (then a warning + proceed).
//
// Soft-skips (returns nil) when Inspector or Env is absent — the gate
// only bites when it has a live cluster AND the effective env to compare.
func CheckAdoptPinned(ctx context.Context, opts AdoptGateOptions) error {
	if opts.Inspector == nil || opts.Env == nil {
		return nil
	}
	res, err := adopt.PinVersions(ctx, opts.Inspector, opts.Env, adopt.PinOptions{})
	if err != nil {
		return fmt.Errorf("adopt preflight: %w", err)
	}

	// Nothing would drift and nothing is undetected → adopt-safe.
	if len(res.Pins) == 0 && !res.HasUnresolved() {
		if n := len(res.AlreadyPinned); n > 0 {
			fmt.Fprintf(opts.Out, "[adopt] %d pre-existing component(s) already pinned to live versions — safe to adopt in place.\n", n)
		}
		return nil
	}

	// Build the detailed list (printed line-by-line) + a compact
	// names-only list (for the returned error, so it isn't a wall of
	// text that repeats what was just printed). Drifting pins first
	// (would upgrade/restart), then undetected (can't verify → unsafe).
	var problems, names []string
	for _, p := range res.Pins {
		cur := p.Current
		if cur == "" {
			cur = "(unset)"
		}
		problems = append(problems, fmt.Sprintf("%s: %s=%s but live is %s", p.Component, p.VersionKey, cur, p.Live))
		names = append(names, p.Component)
	}
	for _, u := range res.Undetected {
		problems = append(problems, u+": live version not readable and not pinned")
		names = append(names, u)
	}

	target := opts.ClusterName
	if target == "" {
		target = "<cluster>"
	}
	fmt.Fprintf(opts.Out, "[adopt] %d pre-existing component(s) are NOT pinned to their live versions:\n", len(problems))
	for _, p := range problems {
		fmt.Fprintf(opts.Out, "        - %s\n", p)
	}
	fmt.Fprintf(opts.Out, "  Flux's first reconcile would upgrade/restart these. Pin them first:\n")
	fmt.Fprintf(opts.Out, "      kube-dc bootstrap adopt %s --kubeconfig <target> --pin-versions --yes\n", target)

	if opts.Allow {
		fmt.Fprintln(opts.Out, "[adopt] --allow-unpinned-adopt set — proceeding anyway (RISKY: expect upgrades/restarts on first reconcile).")
		return nil
	}
	return fmt.Errorf("init --mode=adopt: %d pre-existing component(s) not version-pinned (%s) — run `bootstrap adopt %s --pin-versions` first (see list above), or pass --allow-unpinned-adopt to proceed anyway",
		len(names), strings.Join(names, ", "), target)
}
