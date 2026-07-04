package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapStatusCmd registers `kube-dc bootstrap status` — the
// CI-friendly projection of the fleet view (installer-prd §9.6).
//
// Three render modes:
//
//	kube-dc bootstrap status                        list every cluster
//	kube-dc bootstrap status <name>                 deep view for one cluster
//	kube-dc bootstrap status [--watch]              re-print every 5s
//
// Without --no-tty, lipgloss colours the status pill column. With
// --no-tty, plain-text columns suitable for piping to grep/awk.
//
// Exit code (matches installer-prd §9.6 contract):
//
//	0 — every cluster Ready
//	1 — at least one Reconciling or Drifted
//	2 — at least one Failed / Unreachable / Unknown
//
// **Unknown rolls up to exit 2** (M2 review-pass): a cluster the
// CLI cannot characterise (missing kubeAPIURL, no scenario data,
// etc.) is not safe to treat as healthy in CI. Plan §9.6 explicitly
// lists Ready as the only exit-0 status; Unknown is non-zero.
//
// Data source comes from `session.Probes.StatusRows / StatusDeep`:
// real session walks the fleet repo via discover.ListClusters +
// ClusterProbe; mock session reads `scenario.Fleet.Statuses`. This
// makes `KUBE_DC_MOCK=cloud bootstrap status --no-tty` deterministic
// per the M2-T01 acceptance contract.
func bootstrapStatusCmd(fleetRepo *string) *cobra.Command {
	var (
		noTTY    bool
		watch    bool
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:           "status [cluster-name]",
		Args:          cobra.MaximumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		Short:         "Project fleet cluster state as a no-TTY table or per-cluster deep view",
		Long: `Status is the CI-friendly projection of the fleet TUI. Without an
argument it lists every cluster overlay in the fleet repo with a
status pill (Ready / Reconciling / Drifted / Failed / Unreachable /
Unknown). With a cluster name, it prints the per-cluster deep view:
Kustomization ready states, image drift, and the recommended next
command.

Exit code is the highest severity across all probed clusters:

  0  all clusters Ready
  1  any cluster Reconciling or Drifted
  2  any cluster Failed / Unreachable / Unknown

Unknown rolls up to exit 2 — a cluster the CLI cannot characterise
is not safe to treat as healthy in CI.

Mock mode (` + "`KUBE_DC_MOCK=<scenario>`" + `) sources rows from the
scenario's ` + "`fleet.statuses`" + ` fixture instead of the fleet
repo + apiserver, so CI smokes are deterministic across machines.

Use this in CI to gate ` + "`init`" + ` / chart upgrades:

  kube-dc bootstrap status --no-tty --repo ./fleet || exit $?

Without --no-tty, the status pill column uses lipgloss colours.
` + "`NO_COLOR=1`" + ` disables colour even on a TTY (per no-color.org).`,
		Example: `  # List every cluster
  kube-dc bootstrap status --repo ~/projects/kube-dc-fleet

  # Per-cluster deep view
  kube-dc bootstrap status cloud --repo ~/projects/kube-dc-fleet

  # Mock-backed CI smoke
  KUBE_DC_MOCK=cloud kube-dc bootstrap status --no-tty

  # CI gate
  kube-dc bootstrap status --no-tty --repo ./fleet || exit $?

  # Re-render every 5s
  kube-dc bootstrap status --watch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: *fleetRepo,
			})
			// In real mode without a kubeconfig we still want to run
			// status against the fleet repo (the OIDC probe will
			// degrade per-cluster to Unreachable but the operator
			// at least sees which clusters are configured).
			if err != nil && !errors.Is(err, bootstrap.ErrRealAdaptersNotReady) {
				return err
			}
			if session != nil {
				defer session.Close()
			}

			// Resolve the fleet repo only in real mode — mock mode
			// reads from scenario fixtures and doesn't need a disk
			// repo. resolveFleetRepo refuses an empty path with a
			// clean "no fleet repo" error which is the right shape
			// for real-mode misuse.
			repo := ""
			if session == nil || session.Scenario == "" {
				resolved, rerr := resolveFleetRepo(*fleetRepo)
				if rerr != nil {
					return rerr
				}
				repo = resolved
			}

			useNoTTY := noTTY || !isWriterTTY(cmd.OutOrStdout())

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			factory := factoryFromSession(session)
			k8s := k8sFromSession(session)

			runOnce := func() (int, error) {
				if len(args) == 1 {
					code, rerr := renderDeepView(ctx, cmd.OutOrStdout(), useNoTTY, factory, k8s, repo, args[0])
					return code, rerr
				}
				return renderListAll(ctx, cmd.OutOrStdout(), useNoTTY, factory, repo)
			}

			if !watch {
				code, err := runOnce()
				if err != nil {
					return err
				}
				if code != 0 {
					return &doctorExitCodeErr{code: code}
				}
				return nil
			}

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			lastCode := 0
			clearScreen := !useNoTTY
			for {
				if clearScreen {
					// ANSI clear-screen + home cursor — same sequence
					// `watch -d` uses. NO-OP on --no-tty piping.
					fmt.Fprint(cmd.OutOrStdout(), "\x1b[2J\x1b[H")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("=", 60))
					fmt.Fprintf(cmd.OutOrStdout(), "[watch] %s\n", time.Now().Format(time.RFC3339))
				}
				code, err := runOnce()
				if err != nil {
					return err
				}
				lastCode = code
				select {
				case <-ctx.Done():
					if lastCode != 0 {
						return &doctorExitCodeErr{code: lastCode}
					}
					return nil
				case <-ticker.C:
				}
			}
		},
	}

	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "Force plain-text output even when stdout is a terminal")
	cmd.Flags().BoolVar(&watch, "watch", false, "Re-render every --watch-interval seconds (Ctrl-C to exit)")
	cmd.Flags().DurationVar(&interval, "watch-interval", 5*time.Second, "Refresh interval for --watch mode")

	return cmd
}

// ---------- list-all renderer ----------

func renderListAll(ctx context.Context, out io.Writer, noTTY bool, factory discover.Factory, fleetRepo string) (int, error) {
	rows, err := factory.StatusRows(ctx, fleetRepo)
	if err != nil {
		return 0, fmt.Errorf("status: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no clusters found in fleet repo (no `clusters/*/cluster-config.env` files)")
		return 0, nil
	}

	// Compute column widths so plain output stays grep-friendly.
	nameWidth, statusWidth := 8, 12
	for _, r := range rows {
		if l := len(r.Name); l > nameWidth {
			nameWidth = l
		}
		if l := len(string(r.Status)); l > statusWidth {
			statusWidth = l
		}
	}

	worstCode := 0
	for _, r := range rows {
		pill := renderStatusPill(string(r.Status), r.Status, noTTY)
		fmt.Fprintf(out, "%-*s  %-*s  %s\n", nameWidth, r.Name, statusWidth, pill, r.Detail)
		if code := exitCodeForClusterStatus(r.Status); code > worstCode {
			worstCode = code
		}
	}
	return worstCode, nil
}

// ---------- deep-view renderer ----------

func renderDeepView(ctx context.Context, out io.Writer, noTTY bool, factory discover.Factory, k8s ports.K8sClient, fleetRepo, name string) (int, error) {
	deep, err := factory.StatusDeep(ctx, fleetRepo, name)
	if err != nil {
		if errors.Is(err, discover.ErrClusterNotFound) {
			rows, _ := factory.StatusRows(ctx, fleetRepo)
			known := make([]string, 0, len(rows))
			for _, r := range rows {
				known = append(known, r.Name)
			}
			return 0, fmt.Errorf("cluster %q not found in fleet repo (known: %s)", name, strings.Join(known, ", "))
		}
		return 0, err
	}

	fmt.Fprintf(out, "Cluster: %s\n", deep.Name)
	if deep.Domain != "" {
		fmt.Fprintf(out, "Domain:  %s\n", deep.Domain)
	}
	if deep.APIURL != "" {
		fmt.Fprintf(out, "API:     %s\n", deep.APIURL)
	}
	fmt.Fprintf(out, "Status:  %s\n", renderStatusPill(string(deep.Result.Status), deep.Result.Status, noTTY))
	if deep.Result.Detail != "" {
		fmt.Fprintf(out, "Detail:  %s\n", deep.Result.Detail)
	}
	if deep.Result.FixHint != "" {
		fmt.Fprintf(out, "Fix:     %s\n", deep.Result.FixHint)
	}

	if len(deep.Result.Reconcilers) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Reconcilers:")
		for _, r := range deep.Result.Reconcilers {
			state := "✓"
			if !r.Ready {
				state = "✗"
			}
			suspended := ""
			if r.Suspended {
				suspended = " (suspended)"
			}
			fmt.Fprintf(out, "  %s  %s%s  %s\n", state, r.Name, suspended, r.Message)
		}
	}

	// M6-T04: Nodes section — physical/hardware facts (kubevirt-eligible,
	// GPU inventory) surfaced from the M6-T02 composite labels via the
	// M1-T04 NFDDetect reader. Non-fatal by design: a failure to talk to
	// the target cluster's Node list is expected on real-mode runs where
	// the operator's current kubeconfig context doesn't match the fleet
	// entry they're inspecting. When we can't detect, we skip the
	// section rather than clutter the deep view with a warning that the
	// doctor already surfaces separately.
	renderNodesSection(ctx, out, k8s)

	if len(deep.Result.Drifts) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Image drift:")
		for _, d := range deep.Result.Drifts {
			running := d.Running
			if running == "" {
				running = "(deployment missing)"
			}
			fmt.Fprintf(out, "  %s/%s  expected=%s  running=%s  (env=%s)\n",
				d.Namespace, d.Deployment, d.Expected, running, d.EnvVar)
		}
	}

	// HelmRelease + OpenBao seal sections from M2-T01's contract
	// are deferred: discover.ProbeResult doesn't surface them
	// today (T2's ClusterProbe only walks Kustomizations + image
	// drift). Tracker carries the deferral; both will land when
	// ClusterProbe gains those signals.

	return exitCodeForClusterStatus(deep.Result.Status), nil
}

// renderNodesSection appends a "Nodes:" block to the deep view with
// the NFD-derived capability summary (kubevirt-eligible, NVIDIA/AMD
// GPU counts + node names). Silent no-op when k8s is nil (real-mode
// deep view without a session's kubeconfig; NFDDetect would refuse
// its nil source anyway) or when NFDDetect errors (target cluster
// unreachable, RBAC-scoped away, etc.) — a hard failure here would
// break `bootstrap status` deep views on any cross-cluster inspection
// where the operator's local kubectl context doesn't line up with
// the fleet entry. NFD absence on the target is not an error either:
// we print "NFD not installed" so the operator sees the state
// without the section turning into an error page.
func renderNodesSection(ctx context.Context, out io.Writer, k8s ports.K8sClient) {
	if k8s == nil {
		return
	}
	r, err := discover.NFDDetect(ctx, k8s)
	if err != nil {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Nodes:")
	if !r.NFDInstalled {
		fmt.Fprintf(out, "  NFD not installed (%d nodes)\n", r.TotalNodes)
		return
	}
	source := "M6-T02 composites"
	if !r.CompositesInstalled {
		source = "raw NFD labels (composites absent)"
	}
	fmt.Fprintf(out, "  Total:             %d\n", r.TotalNodes)
	fmt.Fprintf(out, "  KubeVirt-eligible: %d%s\n", r.KubeVirtEligibleCount(), formatNodeList(r.KubeVirtEligibleNodes))
	fmt.Fprintf(out, "  NVIDIA GPU:        %d%s\n", r.NvidiaGPUCount(), formatNodeList(r.NvidiaGPUNodes))
	fmt.Fprintf(out, "  AMD GPU:           %d%s\n", r.AMDGPUCount(), formatNodeList(r.AMDGPUNodes))
	fmt.Fprintf(out, "  Source:            %s\n", source)
}

// formatNodeList returns " (nodeA, nodeB)" or "" for empty. Keeping
// the list inline with the count is the shape M6-T05's error
// messages want to reuse (they name the specific nodes that failed
// the preset check).
func formatNodeList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (" + strings.Join(names, ", ") + ")"
}

// renderStatusPill returns a coloured pill (TTY) or plain status
// text (no-TTY) for one ClusterStatus.
func renderStatusPill(text string, status discover.ClusterStatus, noTTY bool) string {
	if noTTY {
		return text
	}
	colour := "8" // muted grey for Unknown
	switch status {
	case discover.StatusReady:
		colour = "10" // accent green
	case discover.StatusReconciling, discover.StatusDrifted:
		colour = "11" // orange
	case discover.StatusFailed, discover.StatusUnreachable:
		colour = "9" // red
	case discover.StatusUnknown:
		colour = "9" // red — Unknown is not safe to treat as healthy in CI
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colour)).Render(text)
}

// exitCodeForClusterStatus maps a per-cluster status into the
// aggregate exit code per installer-prd §9.6 + the M2 review-pass
// (Unknown → 2).
func exitCodeForClusterStatus(s discover.ClusterStatus) int {
	switch s {
	case discover.StatusReady:
		return 0
	case discover.StatusReconciling, discover.StatusDrifted:
		return 1
	case discover.StatusFailed, discover.StatusUnreachable:
		return 2
	case discover.StatusUnknown:
		// Unknown rolls up to exit 2 — a cluster the CLI can't
		// characterise (missing kubeAPIURL, no scenario data, etc.)
		// is not safe to treat as healthy.
		return 2
	default:
		return 2
	}
}
