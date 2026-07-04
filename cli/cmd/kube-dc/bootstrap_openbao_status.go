package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapOpenBaoStatusCmd registers `kube-dc bootstrap openbao
// status <cluster-name>` (M5-T04). Read-only projection of every
// operational signal the operator asks for first — "what state is
// bao in?" — without needing shares, root tokens, or a SOPS decrypt.
//
// Cluster-name is a display label; the actual K8s calls target
// whatever kubeconfig the session resolved. If --repo is set, we do
// a soft "cluster exists in the fleet" check so a typo in the label
// surfaces before the operator wonders why the printed name doesn't
// match a fleet overlay.
//
// Exit code (via doctorExitCodeErr, matches the doctor + status
// conventions):
//
//	0 — every pod unsealed + both markers present
//	1 — any pod sealed OR one/both markers missing (recoverable —
//	    triage into `openbao unseal` / `openbao setup-controller-auth`)
//	2 — structural failure (no pods, adapter error)
func bootstrapOpenBaoStatusCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <cluster-name>",
		Short: "Read-only projection of OpenBao pods + operational markers (M5-T04)",
		Long: `Reports every OpenBao pod's Initialized/Sealed/Version/HA-mode
plus the svc/openbao operational markers:
  - kube-dc.com/openbao-bootstrap-finalized       (init Phase B done)
  - kube-dc.com/openbao-controller-auth-installed (Phase C / M5-T08 done)

No SOPS key required. No cluster mutation. Safe to run anytime.

Exit code:
  0  every pod unsealed AND both markers present
  1  any pod sealed OR one/both markers missing
  2  structural failure (no pods enumerable, apiserver unreachable)`,
		Example: `  # Cloud cluster, live view
  kube-dc bootstrap openbao status cloud --repo ~/projects/kube-dc-fleet

  # CI gate: exit 1 signals "run unseal / setup-controller-auth"
  kube-dc bootstrap openbao status cs/zrh --repo ./fleet || case $? in
    1) echo "run: kube-dc bootstrap openbao unseal cs/zrh";;
    2) echo "cluster unreachable";;
  esac`,
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}

			// (1) Soft fleet-overlay check when --repo is set. Absence
			// here isn't fatal — the operator may legitimately be
			// running status against a cluster they haven't cloned the
			// fleet for. But when --repo IS set AND the cluster dir is
			// missing, that's almost always a typo in the label; warn.
			if repo != "" {
				overlay := filepath.Join(repo, "clusters", clusterName)
				if _, err := os.Stat(overlay); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(),
						"[openbao] note: no fleet overlay at %s (label mismatch, or fleet not cloned locally)\n",
						overlay)
				}
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("openbao status: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}

			out := cmd.OutOrStdout()
			res, err := openbao.Status(cmd.Context(), openbao.StatusOptions{
				ClusterName: clusterName,
				OpenBao:     session.OpenBao,
				Out:         out,
			})
			if err != nil {
				// Structural failure → exit 2 via typed wrapper. The
				// engine's ErrStatusMissingDependency + adapter errors
				// all belong here — the operator can't recover with a
				// sub-ceremony; the cluster/apiserver itself is broken.
				fmt.Fprintf(out, "openbao status: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}

			renderOpenBaoStatus(out, res)

			if res.FullyReady() {
				return nil
			}
			// Recoverable non-ready → exit 1 with a triage hint.
			return &doctorExitCodeErr{code: 1}
		},
	}
	return cmd
}

// renderOpenBaoStatus writes the operator-facing summary. Same
// section style as `bootstrap status` deep view + doctor: leading
// summary lines, then a per-pod row block with severity glyph +
// key attributes.
func renderOpenBaoStatus(out io.Writer, r openbao.StatusResult) {
	fmt.Fprintf(out, "Cluster: %s\n", r.ClusterName)
	fmt.Fprintln(out, "Service annotations (svc/openbao):")
	fmt.Fprintf(out, "  bootstrap-finalized:       %s\n", displayAnnotation(r.BootstrapFinalized))
	fmt.Fprintf(out, "  controller-auth-installed: %s\n", displayAnnotation(r.ControllerAuthInstalled))
	// M5-T07 policy-generation display. `installed=0` renders as
	// `<absent>` so operators eyeballing the line see the drift
	// unmistakably; non-zero renders the number so drift shows as a
	// simple lower value.
	installedStr := "<absent>"
	if r.PolicyGenerationInstalled > 0 {
		installedStr = fmt.Sprintf("%d", r.PolicyGenerationInstalled)
	}
	if r.HasPolicyGenerationDrift() {
		// Include the cluster name — `setup-controller-auth` requires
		// it as a positional arg, so a bare `setup-controller-auth
		// --refresh-policy` would fail cobra.ExactArgs and leave the
		// operator hunting for the missing bit.
		fmt.Fprintf(out, "  policy-generation:         %d expected / %s installed  ── DRIFT: run `kube-dc bootstrap openbao setup-controller-auth %s --refresh-policy`\n",
			r.PolicyGenerationExpected, installedStr, r.ClusterName)
	} else {
		fmt.Fprintf(out, "  policy-generation:         %d expected / %s installed  ── up to date\n",
			r.PolicyGenerationExpected, installedStr)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Pods:")
	for _, p := range r.Pods {
		glyph, tag := podGlyph(p)
		fmt.Fprintf(out, "  %s  %-10s  %s  %s\n", glyph, p.Pod, tag, podExtras(p))
	}
	fmt.Fprintln(out)

	// Triage hints — order matters. Reviewer-flagged edges:
	//
	//   1. `init` is ONLY the right answer when the cluster has never
	//      been through init at all (every pod Initialized=false).
	//      A cluster where some pods are init'd and some aren't is
	//      the OrderedReady race — `unseal` auto-raft-joins followers
	//      and is the correct recovery, NOT `init` (which would try
	//      to re-run share generation and either fail loudly or —
	//      worse — succeed with new shares that don't match the
	//      committed ones in secrets.enc.yaml).
	//
	//   2. `unseal` guards on Sealed OR the partial-init OrderedReady
	//      race (AnyUninitialized after all-uninit is ruled out). A
	//      sealed all-uninitialised cluster (Initialized=false +
	//      Sealed=true, which is what `bao status` reports on a fresh
	//      never-init pod) MUST take the init branch above — bao can't
	//      be unsealed if there's no shamir barrier yet.
	//
	//   3. `init` is the WRONG hint for the Phase-C-failure recovery
	//      case (pods Initialized+unsealed but markers absent). Re-
	//      running init on an initialised cluster is dangerous — it
	//      would fail on the ErrAlreadyInitialised guard when the
	//      bootstrap-finalized annotation IS set, and would try to
	//      re-generate shares when it ISN'T. `setup-controller-auth`
	//      re-runs Phase C idempotently and stamps both markers as
	//      part of RefreshFull mode, which is exactly the recovery
	//      path init's failure message points at.
	switch {
	case r.AllUninitialized():
		fmt.Fprintf(out, "Next: kube-dc bootstrap openbao init %s   # cluster has never been initialised\n", r.ClusterName)
	case r.AnySealed() || r.AnyUninitialized():
		// Both cases recover via unseal: the sealed cascade is unseal's
		// canonical purpose, and the OrderedReady race (mix of init'd
		// and non-init'd pods) recovers through unseal's raft-auto-join.
		fmt.Fprintf(out, "Next: kube-dc bootstrap openbao unseal %s\n", r.ClusterName)
	case r.BootstrapFinalized == "" || r.ControllerAuthInstalled == "":
		// Any missing marker on an otherwise-ready cluster maps to
		// setup-controller-auth. RefreshFull stamps BOTH markers, so
		// this recovers both the "Phase B never annotated" (unusual)
		// and the "Phase C bailed before annotate" (the common
		// partial-init recovery) states in one command.
		reason := ""
		switch {
		case r.BootstrapFinalized == "" && r.ControllerAuthInstalled == "":
			reason = "   # both markers absent — init's Phase C likely bailed before annotate"
		case r.ControllerAuthInstalled == "":
			reason = "   # controller-auth-installed marker absent"
		default:
			reason = "   # bootstrap-finalized marker absent (unusual — re-stamps both)"
		}
		fmt.Fprintf(out, "Next: kube-dc bootstrap openbao setup-controller-auth %s%s\n", r.ClusterName, reason)
	default:
		// FullyReady case — no triage hint needed; the caller returns
		// nil and prints nothing further.
	}
}

// displayAnnotation renders empty annotations as `<absent>` so
// operators see the missing state at a glance rather than a
// suspicious empty column.
func displayAnnotation(v string) string {
	if v == "" {
		return "<absent>"
	}
	return v
}

// podGlyph maps a pod's state to a compact severity marker + tag.
// Sealed is Blocker (needs unseal to make progress). Uninitialised
// on a non-leader is Warn (raft-join race, unseal auto-recovers).
// Uninitialised leader is Blocker (init hasn't run). Ready pods get
// a plain check.
func podGlyph(p ports.BaoStatus) (string, string) {
	switch {
	case !p.Initialized:
		return "✗", "UNINITIALIZED"
	case p.Sealed:
		return "✗", "SEALED"
	default:
		return "✓", "unsealed"
	}
}

// podExtras renders the compact version + HA-mode tail (e.g.
// "v2.5.3 ha=active raft-index=1234"). Empty HAMode (single-node
// installs) is skipped so the line stays clean.
func podExtras(p ports.BaoStatus) string {
	extras := "v" + p.Version
	if p.HAMode != "" {
		extras += "  ha=" + p.HAMode
	}
	if p.RaftIndex > 0 {
		extras += fmt.Sprintf("  raft-index=%d", p.RaftIndex)
	}
	return extras
}
