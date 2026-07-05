package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/i18n"
)

// bootstrapAddNodeCmd registers `kube-dc bootstrap add-node` — the V4
// no-op stub (installer-agentic-implementation-plan §13). The real
// driver (SSH to the target host, run the join flow, verify the node
// registers + CSRs approve) is a v2 feature; the stub exists NOW so
// the command is discoverable in `bootstrap --help` and prints the
// current manual join path instead of leaving operators to grep the
// fleet repo.
//
// Exit code 64 (EX_USAGE, per the plan contract): scripts probing for
// the v2 feature must not mistake the printed instructions for a
// successful join.
//
// NOTE: the plan's original instruction text (scp install-agent.sh +
// an inline SSH one-liner) predates the fleet's current join flow.
// The canonical path today is `bootstrap/rke2/add-worker.sh` — an
// env-driven orchestrator that runs ON the target node (network
// setup + RKE2 agent install + a connectivity self-heal watchdog).
// The stub prints THAT contract; per the plan's stop-condition the
// signature was re-verified against the fleet script 2026-07-04.
func bootstrapAddNodeCmd() *cobra.Command {
	var worker, master bool

	cmd := &cobra.Command{
		Use:           "add-node <node-ip>",
		Short:         "Join a new node to a Kube-DC cluster (v2 — prints the manual path for now)",
		Args:          cobra.MaximumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if master {
				fmt.Fprintln(cmd.OutOrStdout(), `add-node --master is a v2 feature with no manual shortcut — additional
control-plane nodes need etcd quorum planning. Track:
docs/prd/installer-agentic-implementation-plan.md §13 (V4).`)
				return &doctorExitCodeErr{code: 64}
			}
			_ = worker // --worker is the default (and only) v1-documented shape
			// C3 exemplar: the notice + verify lines route through
			// the i18n seam (see internal/i18n adoption policy).
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("addnode.v2_notice"))
			fmt.Fprintln(cmd.OutOrStdout(), `
  # 1. Copy the join scripts to the new node:
  scp <fleet-repo>/bootstrap/rke2/{add-worker.sh,setup-worker-network.sh,install-agent.sh} root@<node-ip>:/tmp/

  # 2. Read the control-plane join token (on a master):
  cat /var/lib/rancher/rke2/server/node-token

  # 3. Run the orchestrator on the new node (see the script header for
  #    the full env contract — network values are cluster-specific):
  SERVER_TOKEN=<token> CP_HOST=<control-plane-internal-ip> \
    NODE_NAME=<node-name> INTERNAL_IP=<internal-vlan-ip> \
    PUBLIC_CIDR=<public-ip/prefix> PUBLIC_GW=<public-gateway> \
    bash /tmp/add-worker.sh

add-worker.sh arms a connectivity self-heal watchdog around the
destructive network steps — disarm it (command printed at the end)
once the node is Ready.`)
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("addnode.verify"))
			return &doctorExitCodeErr{code: 64}
		},
	}

	cmd.Flags().BoolVar(&worker, "worker", false, "Join as a worker node (default)")
	cmd.Flags().BoolVar(&master, "master", false, "Join as an additional control-plane node (v2)")
	return cmd
}
