package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	k8sadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/k8s"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapOIDCCutoverCmd registers `kube-dc bootstrap oidc-cutover`.
//
// This replaces the mandatory manual step that used to sit in the install guide
// as §3.5.1: SSH to each control-plane node, append two apiserver flags, restart
// rke2-server, one node at a time, gating on the apiserver coming back.
//
// It was the single largest obstacle to anyone installing kube-dc without us on
// a call, for three reasons:
//
//  1. Nothing tells you it is needed. Flux goes green, every pod is Ready, and
//     the cluster is simply unusable: every Keycloak JWT returns 401, so tenant
//     kubectl fails, the console's Manage-Organization calls fail, and the
//     k8-manager / db-manager operators fail.
//  2. Doing it half-way is worse than not starting. kubectl load-balances across
//     apiservers, so a tenant token is accepted only by the nodes already wired
//     — an intermittent 401 that looks like a Keycloak or clock problem.
//  3. Two of its failure modes destroy a control-plane node: a duplicated
//     `kube-apiserver-arg:` key (which RKE2 resolves by silently discarding one
//     block), and restarting a node where something else already holds :6443.
func bootstrapOIDCCutoverCmd(fleetRepo *string) *cobra.Command {
	var (
		sshHosts     []string
		sshUser      string
		dryRun       bool
		rollback     bool
		allowPartial bool
		readyTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:           "oidc-cutover",
		Short:         "Point every control-plane apiserver at the OIDC webhook (required before anyone can log in)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `RKE2 boots cert-only: the apiserver has no way to validate a
Keycloak token until it is pointed at the oidc-webhook-authenticator, which
Flux brings up later (infra-core). Until then the cluster looks completely
healthy and NOBODY CAN LOG IN — every Keycloak JWT returns 401, so tenant
kubectl, the console's organization management, and the k8-manager /
db-manager operators all fail.

This wires every control-plane node, ONE AT A TIME, gating each on its
apiserver returning before touching the next.

It refuses to start unless it can reach EVERY control-plane node, because a
partially cut-over cluster is worse than an un-cut-over one: kubectl
load-balances across apiservers, so a tenant's token is accepted only by the
nodes already wired, producing intermittent 401s that look like a Keycloak or
a clock problem.

Safe to re-run: a node that is already wired is skipped, not restarted.

  # discover the control-plane nodes from the live cluster
  kube-dc bootstrap oidc-cutover --ssh-user ubuntu --dry-run
  kube-dc bootstrap oidc-cutover --ssh-user ubuntu

  # or name the hosts explicitly
  kube-dc bootstrap oidc-cutover --ssh-host ubuntu@10.0.0.11 --ssh-host ubuntu@10.0.0.12

  # bare ssh_config aliases, with the user overridden
  kube-dc bootstrap oidc-cutover --ssh-host cp-0 --ssh-host cp-1 --ssh-user root

  # undo (restores the pre-cutover snapshot and restarts)
  kube-dc bootstrap oidc-cutover --ssh-user ubuntu --rollback`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			out := cmd.OutOrStdout()

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: *fleetRepo})
			if err != nil {
				return fmt.Errorf("session: %w", err)
			}
			defer session.Close()

			nodes, err := resolveCutoverNodes(ctx, sshHosts, sshUser, allowPartial)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "control-plane nodes to wire (%d):\n", len(nodes))
			for _, n := range nodes {
				fmt.Fprintf(out, "  %-24s via %s\n", n.Name, sshHostString(n.Host))
			}

			res, err := oidccutover.Run(ctx, oidccutover.Options{
				SSH:          session.SSH,
				K8s:          session.K8s,
				Nodes:        nodes,
				DryRun:       dryRun,
				Rollback:     rollback,
				ReadyTimeout: readyTimeout,
				Out:          out,
			})
			// Only summarise a run that finished. Printing "0 node(s) would be
			// wired" above a hard failure reads like a successful no-op.
			if err != nil {
				printCutoverFailure(cmd, res)
				return err
			}
			printCutoverSummary(cmd, res, dryRun, rollback)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sshHosts, "ssh-host", nil,
		"Control-plane SSH target (repeatable). Either user@hostname/IP, or a BARE ssh_config alias "+
			"(combine a bare alias with --ssh-user to override the user; `user@alias` does NOT resolve "+
			"the alias — it is treated as a literal hostname). "+
			"Omit to discover the control-plane nodes from the live cluster and reach them at their InternalIP")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "",
		"SSH user for discovered nodes (default: root). Ignored for --ssh-host entries that carry user@")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what each node needs; change nothing")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false,
		"Permit naming fewer --ssh-host targets than the cluster has control-plane nodes. "+
			"Only for wiring them in deliberate batches: until every node is done, Keycloak logins "+
			"succeed only on the wired ones, which presents as intermittent 401s")
	cmd.Flags().BoolVar(&rollback, "rollback", false,
		"Restore each node's pre-cutover config snapshot and restart rke2-server")
	cmd.Flags().DurationVar(&readyTimeout, "ready-timeout", 5*time.Minute,
		"How long to wait for an apiserver to report /readyz after its restart")
	return cmd
}

// resolveCutoverNodes builds the node list, preferring explicit --ssh-host and
// otherwise discovering the control plane from the live cluster.
//
// Discovery is the default because the operator should not have to keep a list
// of control-plane addresses in their head, and an incomplete list is exactly
// the partial cutover this command refuses to perform.
func resolveCutoverNodes(ctx context.Context, sshHosts []string, sshUser string, allowPartialCutover bool) ([]oidccutover.Node, error) {
	if len(sshHosts) > 0 {
		var nodes []oidccutover.Node
		for _, raw := range sshHosts {
			host := parseSSHHostArg(raw)
			if host.User == "" {
				host.User = sshUser
			}
			nodes = append(nodes, oidccutover.Node{Name: sshHostString(host), Host: host})
		}
		// The completeness invariant applies to EXPLICIT hosts too. Enforcing it
		// only on the discovery path meant `--ssh-host one-of-three` was
		// accepted and produced exactly the partially-wired cluster the command
		// advertises that it refuses to create.
		if facts, err := k8sadapter.GatherNodeNetworkFacts(ctx, ""); err == nil {
			cp := 0
			for _, f := range facts {
				if f.ControlPlane {
					cp++
				}
			}
			if cp > 0 && len(nodes) < cp && !allowPartialCutover {
				return nil, fmt.Errorf("oidc-cutover: you named %d host(s) but this cluster has %d "+
					"control-plane node(s). %s\n\n"+
					"Omit --ssh-host to have every control-plane node discovered automatically, name all "+
					"%d, or pass --allow-partial if you are deliberately wiring them in separate batches",
					len(nodes), cp, oidccutover.PartialCutoverHazard, cp)
			}
		}
		return nodes, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	facts, err := k8sadapter.GatherNodeNetworkFacts(probeCtx, "")
	if err != nil {
		return nil, fmt.Errorf("oidc-cutover: cannot reach the cluster to discover control-plane nodes "+
			"(%w). Point KUBECONFIG at the cluster, or name the hosts with --ssh-host", err)
	}
	var nodes []oidccutover.Node
	for _, f := range facts {
		if !f.ControlPlane {
			continue
		}
		if f.InternalIP == "" {
			return nil, fmt.Errorf("oidc-cutover: control-plane node %q has no InternalIP, so it cannot "+
				"be reached automatically — name every control-plane host with --ssh-host instead", f.Name)
		}
		nodes = append(nodes, oidccutover.Node{
			Name: f.Name,
			Host: ports.SSHHost{Hostname: f.InternalIP, User: sshUser},
		})
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("oidc-cutover: no control-plane nodes found in the cluster")
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func sshHostString(h ports.SSHHost) string {
	target := h.Hostname
	if target == "" {
		target = h.Alias
	}
	if h.User != "" {
		return h.User + "@" + target
	}
	return target
}

// printCutoverFailure states what DID happen before the failure, so the
// operator knows whether the cluster is now in the partially-wired state.
func printCutoverFailure(cmd *cobra.Command, res oidccutover.Result) {
	out := cmd.OutOrStdout()
	if len(res.Wired) == 0 && len(res.AlreadyWired) == 0 {
		fmt.Fprintln(out, "\nNo node was changed.")
		return
	}
	fmt.Fprintf(out, "\nSTOPPED after wiring %d node(s)", len(res.Wired))
	if len(res.AlreadyWired) > 0 {
		fmt.Fprintf(out, " (%d were already done)", len(res.AlreadyWired))
	}
	fmt.Fprintln(out, ".")
	if len(res.Wired) > 0 {
		fmt.Fprintf(out, "The cluster is PARTIALLY wired: %s\n", oidccutover.PartialCutoverHazard)
		fmt.Fprintln(out, "Fix the cause and re-run — finished nodes are skipped — or use --rollback.")
	}
}

func printCutoverSummary(cmd *cobra.Command, res oidccutover.Result, dryRun, rollback bool) {
	out := cmd.OutOrStdout()
	if rollback {
		return
	}
	fmt.Fprintln(out)
	switch {
	case dryRun:
		fmt.Fprintf(out, "DRY RUN: %d node(s) would be wired, %d already done.\n",
			len(res.Wired), len(res.AlreadyWired))
		fmt.Fprintln(out, "Re-run without --dry-run to apply.")
	case len(res.Wired) == 0 && len(res.AlreadyWired) > 0:
		fmt.Fprintf(out, "Nothing to do: all %d control-plane node(s) already wired.\n", len(res.AlreadyWired))
	default:
		fmt.Fprintf(out, "Wired %d node(s)", len(res.Wired))
		if len(res.AlreadyWired) > 0 {
			fmt.Fprintf(out, " (%d already done)", len(res.AlreadyWired))
		}
		fmt.Fprintln(out, ".")
		fmt.Fprintln(out, "Keycloak logins now work. Verify with a real tenant token:")
		fmt.Fprintln(out, "  kube-dc login --server https://<domain> && kubectl get ns")
	}
	if len(res.Skipped) > 0 {
		names := make([]string, 0, len(res.Skipped))
		for n := range res.Skipped {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(out, "\nNOT wired: %s\n", strings.Join(names, ", "))
		fmt.Fprintf(out, "%s\n", oidccutover.PartialCutoverHazard)
	}
}
