package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
)

// bootstrapAdoptCmd registers `kube-dc bootstrap adopt` (V2) — a
// pre-install inventory for an EXISTING cluster. It detects components
// kube-dc would install that are ALREADY present (cert-manager,
// kube-ovn, kubevirt, …) and advises a per-component decision so `init`
// doesn't clobber existing infra.
//
// v1 is READ-ONLY: detect + advise + print the exact fleet-overlay edit.
// It does NOT rewrite the fleet overlay (the risky half — see the adopt
// package doc); that lands as a follow-up once the advisory is reviewed.
func bootstrapAdoptCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:           "adopt [cluster]",
		Short:         "Inventory pre-existing components on a cluster before install (advisory)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Detects components already on the target cluster that kube-dc would
otherwise install (cert-manager, kube-ovn, envoy-gateway, kubevirt,
kamaji, rook-ceph, monitoring, cnpg, metallb, keycloak, …), by CRD
(most reliable) then namespace. For each, it advises:

  adopt    keep the existing component; exclude it from kube-dc's Flux
           (kube-dc won't manage or clobber it) — the safe default.
  replace  let kube-dc's Flux take it over (riskier; you opt in).

This is READ-ONLY — it prints the exact fleet-overlay edit for each
decision but does not change anything. Point it at the target cluster
with --kubeconfig (or KUBECONFIG). The optional [cluster] name is used
only to render accurate clusters/<cluster>/… paths in the advice.`,
		Example: `  kube-dc bootstrap adopt --kubeconfig ./target.kubeconfig
  kube-dc bootstrap adopt acme --kubeconfig ./acme.kubeconfig`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := "<cluster>"
			if len(args) == 1 && args[0] != "" {
				clusterName = args[0]
			}
			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: *fleetRepo,
				Kubeconfig:    kubeconfig,
			})
			if session != nil {
				defer session.Close()
			}
			// adopt needs a live cluster — unlike doctor, a missing
			// kubeconfig is fatal (there's nothing to inventory).
			if err != nil {
				return fmt.Errorf("bootstrap adopt: need a target cluster (set --kubeconfig / KUBECONFIG): %w", err)
			}
			k8s := k8sFromSession(session)
			if k8s == nil {
				return fmt.Errorf("bootstrap adopt: no k8s client (set --kubeconfig / KUBECONFIG)")
			}

			res, err := adopt.Detect(cmd.Context(), k8s)
			if err != nil {
				return fmt.Errorf("bootstrap adopt: %w", err)
			}
			renderAdopt(cmd.OutOrStdout(), res, clusterName)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Kubeconfig for the TARGET cluster to inventory (default: $KUBECONFIG, then ~/.kube/config)")
	return cmd
}

func renderAdopt(out io.Writer, res *adopt.Result, clusterName string) {
	fmt.Fprintf(out, "== adopt inventory — target cluster (%d pre-existing component(s)) ==\n", len(res.Findings))
	if res.FluxInstalled {
		fmt.Fprintln(out, "  note: Flux is already installed here — this may already be a kube-dc-managed cluster.")
	}
	if len(res.Findings) == 0 {
		fmt.Fprintln(out, "  ✓ greenfield — none of kube-dc's components are already present; nothing to adopt.")
		fmt.Fprintln(out, "  Proceed with `kube-dc bootstrap init`.")
		return
	}

	for _, f := range res.Findings {
		fmt.Fprintf(out, "\n  • %s  (detected via %s)\n", f.Component.Name, f.Via)
		fmt.Fprintf(out, "      kube-dc would install it at: %s\n", f.Component.FleetPath)
		if f.Component.Note != "" {
			fmt.Fprintf(out, "      note: %s\n", f.Component.Note)
		}
		fmt.Fprintf(out, "      recommended: %s (keep it; exclude from kube-dc's Flux)\n", f.Recommend)
	}

	fmt.Fprintln(out, "\n  To ADOPT a component (keep it, exclude from kube-dc's Flux), omit its")
	fmt.Fprintf(out, "  line from clusters/%s/<layer>/kustomization.yaml (keep prune:false +\n", clusterName)
	fmt.Fprintln(out, "  force:true on the Flux Kustomizations so nothing you own is deleted).")
	fmt.Fprintln(out, "  To REPLACE (let kube-dc manage it), leave it in — but verify live-vs-fleet")
	fmt.Fprintln(out, "  first (a version/CRD mismatch can disrupt the running component).")
	fmt.Fprintln(out, "\n  (advisory only — this command changes nothing; automated fleet-overlay")
	fmt.Fprintln(out, "  edits are a planned follow-up.)")
}
