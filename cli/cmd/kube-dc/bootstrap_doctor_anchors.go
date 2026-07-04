package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/anchors"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// bootstrapDoctorAnchorsCmd registers `kube-dc bootstrap doctor anchors
// <cluster-name>` — the read-only per-node anchor-state view.
//
// Lives as a sibling subcommand under `doctor` (rather than folded into
// the unified `bootstrap doctor` probe set) because the anchors probe
// needs a fleet repo + cluster name to know which nodes to SSH to,
// while the rest of doctor runs against the operator's local
// kubeconfig + host. Forcing both into one command shape would mean
// every doctor invocation either needs a --cluster flag (UX cost on
// the common case) or silently skips anchor probes (a footgun).
//
// Renders a five-column table:
//
//	Node  Unit-State  IP-Bound  Expected  Notes
//	srv5  active      yes       100.64.0.11/16 on br-ext-cloud
//	srv6  active      yes       100.64.0.12/16 on br-ext-cloud
//	srv7  not-installed no      100.64.0.13/16 on br-ext-cloud   ← FAIL
//
// Exit code: 0 if every node is healthy (unit active + IP bound), 1
// if any drift, 2 on input errors.
func bootstrapDoctorAnchorsCmd(fleetRepo *string) *cobra.Command {
	var sshHostMapFlag []string

	cmd := &cobra.Command{
		Use:           "anchors <cluster-name>",
		Short:         "Show per-node MetalLB L3 anchor state for a cluster",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		Example: `  kube-dc bootstrap doctor anchors cloudacropolis --repo ~/projects/kube-dc-fleet
  kube-dc bootstrap doctor anchors cs/zrh
  kube-dc bootstrap doctor anchors acme --ssh-host-map srv5-kub1=10.0.0.5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]

			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			cluster, err := loadAnchorCluster(repo, clusterName)
			if err != nil {
				return err
			}

			// Run the same semantic validator bootstrap init uses so
			// drift in a hand-edited cluster-config.env is surfaced
			// before SSH dials (otherwise doctor would dutifully report
			// the per-node state of an internally-inconsistent config).
			if err := clusterinit.ValidateAnchorConfig(cluster.Env.AsMap()); err != nil {
				return fmt.Errorf("cluster %s: %w", clusterName, err)
			}

			rawAnchors := strings.TrimSpace(cluster.Env.GetOr("EXT_NET_ANCHOR_IPS", ""))
			iface := strings.TrimSpace(cluster.Env.GetOr("EXT_NET_ANCHOR_INTERFACE", "br-ext-cloud"))

			if rawAnchors == "" {
				fmt.Fprintf(cmd.OutOrStdout(),
					"cluster %s: EXT_NET_ANCHOR_IPS empty — no anchors to probe.\n",
					clusterName)
				return nil
			}

			entries, err := anchors.ParseAnchorMap(rawAnchors)
			if err != nil {
				return fmt.Errorf("parse EXT_NET_ANCHOR_IPS: %w", err)
			}

			resolver, err := buildAnchorResolver(cluster.Env.GetOr("EXT_NET_ANCHOR_SSH_HOSTS", ""), sshHostMapFlag)
			if err != nil {
				return err
			}

			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: repo,
				Cluster:       clusterName,
			})
			if err != nil {
				return fmt.Errorf("session: %w", err)
			}
			defer session.Close()

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			states := anchors.Probe(ctx, session.SSH, iface, entries, resolver)
			drift := printAnchorDoctorTable(cmd.OutOrStdout(), states)
			if drift > 0 {
				return &doctorExitCodeErr{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sshHostMapFlag, "ssh-host-map", nil,
		"Override SSH target for a node: --ssh-host-map srv5-kub1=10.0.0.5 (repeatable). "+
			"Wins over EXT_NET_ANCHOR_SSH_HOSTS from cluster-config.env. "+
			"Falls back to ~/.ssh/config alias when no mapping is supplied.")
	return cmd
}

// printAnchorDoctorTable writes the five-column status table and
// returns the count of nodes that fail the IsHealthy check.
func printAnchorDoctorTable(w io.Writer, states []anchors.NodeStatus) int {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tUNIT-STATE\tIP-BOUND\tEXPECTED\tNOTES")
	drift := 0
	for _, s := range states {
		bound := "no"
		if s.IPBound {
			bound = "yes"
		}
		notes := ""
		if s.Err != nil {
			notes = fmt.Sprintf("probe error: %v", s.Err)
		} else if !s.IsHealthy() {
			notes = "DRIFT — run `kube-dc bootstrap anchors apply`"
		}
		if !s.IsHealthy() {
			drift++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s on %s\t%s\n",
			s.Host, s.UnitState, bound, s.ExpectedCIDR, s.Iface, notes)
	}
	tw.Flush()
	return drift
}
