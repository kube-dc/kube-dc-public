package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/anchors"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// bootstrapAnchorsCmd registers `kube-dc bootstrap anchors`. The
// no-arg form prints help; operators reach the per-cluster apply via:
//
//	kube-dc bootstrap anchors apply <cluster-name>   --repo <fleet>
//
// Future slices (status — already covered by `bootstrap doctor
// anchors`; remove — for cluster decommission) attach here.
func bootstrapAnchorsCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "anchors",
		Short:         "Install per-node MetalLB L3 anchor systemd units on a cluster's gw nodes",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `MetalLB L2 mode announces its LoadBalancer IPs via gratuitous
ARP from a single elected speaker. The speaker source-addresses
the GARP from an IP bound to the host interface in its matching
L2Advertisement (here: br-ext-cloud, the kube-ovn-cni external
bridge). Without ANY host-bound IP on br-ext-cloud, kernel
arp_announce / arp_filter quietly suppress the GARP — the VIP
is "announced" but no peer in the broadcast domain ever sees it.

` + "`bootstrap anchors apply`" + ` reads the cluster's
EXT_NET_ANCHOR_IPS map (host=CIDR per gw node) from
cluster-config.env, then over SSH installs a Type=oneshot systemd
unit on each gw node that binds its anchor CIDR to the bridge at
boot (after kube-ovn-cni has created it). Idempotent: re-running
on an already-installed node is a no-op.

This is a sibling subcommand — not folded into ` + "`bootstrap init`" + `
— so per-node SSH loops stay separate from the cluster-overlay
waterfall. Operator runs anchors apply explicitly after init (or
after adding a new gw node).`,
	}
	cmd.AddCommand(bootstrapAnchorsApplyCmd(fleetRepo))
	return cmd
}

// bootstrapAnchorsApplyCmd registers `bootstrap anchors apply <name>`.
//
// Reads the cluster overlay's cluster-config.env, parses
// EXT_NET_ANCHOR_IPS + EXT_NET_ANCHOR_INTERFACE + EXT_NET_ANCHOR_SSH_HOSTS,
// merges with any --ssh-host-map overrides, and fans the systemd-unit
// install across every node listed via session.SSH. Per-node failures
// don't abort — operator sees the full result set and re-runs after
// fixing.
//
// Exit code: 0 on full success (every node Wrote=true), 1 on any
// node failure, 2 on input/discovery errors (cluster not found,
// EXT_NET_ANCHOR_IPS empty + REQUIRED=true).
func bootstrapAnchorsApplyCmd(fleetRepo *string) *cobra.Command {
	var (
		dryRun         bool
		sshHostMapFlag []string
	)

	cmd := &cobra.Command{
		Use:           "apply <cluster-name>",
		Short:         "Install + enable kube-dc-anchor.service on every gw node of a cluster",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		Example: `  kube-dc bootstrap anchors apply atlantis --repo ~/projects/kube-dc-fleet
  kube-dc bootstrap anchors apply eu/dc1 --dry-run
  kube-dc bootstrap anchors apply acme --ssh-host-map host5-a=10.0.0.5 --ssh-host-map host6-a=10.0.0.6`,
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
			// hand-edited cluster-config.env files are caught at apply
			// time (dup-IP, out-of-CIDR, prefix mismatch, REQUIRED=true
			// coverage, interface typo, SSH host map shape). Without
			// this, the CLI would silently apply a partial-coverage
			// config that doctor anchors would then report as green.
			if err := clusterinit.ValidateAnchorConfig(cluster.Env.AsMap()); err != nil {
				return fmt.Errorf("cluster %s: %w", clusterName, err)
			}

			rawAnchors := strings.TrimSpace(cluster.Env.GetOr("EXT_NET_ANCHOR_IPS", ""))
			iface := strings.TrimSpace(cluster.Env.GetOr("EXT_NET_ANCHOR_INTERFACE", "br-ext-cloud"))
			required := cluster.Env.GetOr("EXT_NET_ANCHOR_REQUIRED", "false") == "true"

			if rawAnchors == "" {
				if required {
					return fmt.Errorf("cluster %s: EXT_NET_ANCHOR_REQUIRED=true but EXT_NET_ANCHOR_IPS empty", clusterName)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"cluster %s: EXT_NET_ANCHOR_IPS empty — nothing to apply (set it in cluster-config.env)\n",
					clusterName)
				return nil
			}

			entries, err := anchors.ParseAnchorMap(rawAnchors)
			if err != nil {
				return fmt.Errorf("cluster %s: parse EXT_NET_ANCHOR_IPS: %w", clusterName, err)
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

			fmt.Fprintf(cmd.OutOrStdout(),
				"cluster %s: applying %d anchor(s) on %s\n",
				clusterName, len(entries), iface)
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "  (dry-run: no SSH writes)")
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			res, err := anchors.Apply(ctx, session.SSH, anchors.ApplyOptions{
				Anchors:  entries,
				Iface:    iface,
				DryRun:   dryRun,
				Resolver: resolver,
			})
			if err != nil {
				return err
			}

			printAnchorApplyResult(cmd.OutOrStdout(), res, dryRun)
			if res.Failed > 0 {
				return fmt.Errorf("%d of %d nodes failed", res.Failed, len(res.Nodes))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print the would-write actions without touching any node")
	cmd.Flags().StringSliceVar(&sshHostMapFlag, "ssh-host-map", nil,
		"Override SSH target for a node: --ssh-host-map host5-a=10.0.0.5 (repeatable). "+
			"Wins over EXT_NET_ANCHOR_SSH_HOSTS from cluster-config.env. "+
			"Falls back to ~/.ssh/config alias when no mapping is supplied.")
	return cmd
}

// buildAnchorResolver merges the cluster's EXT_NET_ANCHOR_SSH_HOSTS
// (fleet default) with any --ssh-host-map flag values (per-invocation
// override) and returns the HostResolver Apply/Probe should use.
// Empty inputs return the nil-safe legacy resolver (alias-only).
func buildAnchorResolver(fleetRaw string, flagValues []string) (anchors.HostResolver, error) {
	fleetMap, err := anchors.ParseSSHHostMap(fleetRaw)
	if err != nil {
		return nil, fmt.Errorf("EXT_NET_ANCHOR_SSH_HOSTS: %w", err)
	}
	flagMap, err := anchors.ParseSSHHostMapSlice(flagValues)
	if err != nil {
		return nil, fmt.Errorf("--ssh-host-map: %w", err)
	}
	return anchors.NewHostResolver(anchors.MergeSSHHostMaps(fleetMap, flagMap)), nil
}

// loadAnchorCluster resolves <cluster-name> to a discover.Cluster.
// Returns a typed error when the overlay isn't in the fleet so the
// caller can render a helpful message instead of a generic "not
// found".
func loadAnchorCluster(repoRoot, name string) (discover.Cluster, error) {
	clusters, err := discover.ListClusters(repoRoot)
	if err != nil {
		return discover.Cluster{}, fmt.Errorf("list clusters in %s: %w", repoRoot, err)
	}
	for _, c := range clusters {
		if c.Name == name {
			return c, nil
		}
	}
	// Help the operator by listing what IS available — typos +
	// path/slash confusion (eu/dc1 vs eu-dc1) are the common modes.
	var names []string
	for _, c := range clusters {
		names = append(names, c.Name)
	}
	return discover.Cluster{}, fmt.Errorf(
		"cluster %q not found under %s (available: %s)",
		name, filepath.Join(repoRoot, "clusters"), strings.Join(names, ", "))
}

// printAnchorApplyResult writes the operator-facing per-node summary.
// Plain text — the doctor printer covers the lipgloss path; an apply
// result is short and runs only when the operator explicitly invokes,
// so a structured monospace table beats lipgloss styling here.
func printAnchorApplyResult(w io.Writer, res anchors.Result, dryRun bool) {
	for _, n := range res.Nodes {
		status := "wrote"
		if dryRun {
			status = "would-write"
		}
		if !n.Wrote && !dryRun {
			fmt.Fprintf(w, "  %-20s %-20s FAILED: %v\n", n.Host, n.CIDR, n.Err)
			continue
		}
		fmt.Fprintf(w, "  %-20s %-20s %s\n", n.Host, n.CIDR, status)
	}
	if res.Failed > 0 {
		fmt.Fprintf(w, "\n%d of %d nodes failed — re-run after fixing.\n", res.Failed, len(res.Nodes))
		return
	}
	if dryRun {
		return
	}
	fmt.Fprintf(w, "\nAll %d node(s) installed kube-dc-anchor.service.\n", len(res.Nodes))
}
