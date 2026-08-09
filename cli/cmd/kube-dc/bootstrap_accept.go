package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/accept"
)

// bootstrapAcceptCmd registers `kube-dc bootstrap accept`.
//
// It exists because every other signal the installer produces reports
// CONVERGENCE, and a converged cluster can be completely unusable: Flux green,
// pods Running, and every Keycloak login returning 401. An operator following
// the guide had no command that answered "am I done?", so the honest answer
// arrived from a user instead.
func bootstrapAcceptCmd(fleetRepo *string) *cobra.Command {
	var (
		domain  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:           "accept [cluster]",
		Short:         "Prove the cluster is USABLE, not merely converged",
		Args:          cobra.MaximumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Runs the checks that decide whether a real person can use this
cluster, and reports one of three states:

  reconciling  Flux has not settled; ask again later
  converged    Flux settled and components are up — but something a user
               would hit is still broken
  usable       identity works, the front door is trusted, tenancy is installed

Only "usable" means the install is finished. Exit code is 0 for usable,
1 for converged, 2 for reconciling or an unreadable cluster, so this is
usable as a gate in a script.

The check that matters most is identity/oidc-cutover: it reads the flags every
kube-apiserver is ACTUALLY running with, from the static pods RKE2 registers per
control-plane node. That is stronger than reading config files (RKE2 accepts a
malformed config and drops the flag) and stronger than seeing the
oidc-webhook-authenticator pods Ready (which proves the webhook is alive, not
that any apiserver calls it).

A check that cannot be performed is reported SKIP with the reason, and a skipped
required check never yields "usable" — "I could not tell" must not read as
"fine".

  kube-dc bootstrap accept dc1 --domain dc.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cluster := ""
			if len(args) == 1 {
				cluster = args[0]
			}

			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: *fleetRepo,
				Cluster:       cluster,
			})
			if err != nil {
				return fmt.Errorf("accept: %w\n\nPoint KUBECONFIG at the cluster first "+
					"(kube-dc bootstrap fetch-kubeconfig <cluster> --ssh-host <cp> --domain <domain>)", err)
			}
			defer session.Close()

			// Fall back to the cluster's own recorded domain so the front-door
			// check works without the operator repeating themselves.
			if domain == "" && cluster != "" {
				if env, _, err := loadClusterEnv(*fleetRepo, cluster); err == nil {
					domain = env.GetOr("DOMAIN", "")
				}
			}

			rep, err := accept.Run(ctx, accept.Options{
				K8s:         session.K8s,
				Cluster:     firstNonEmpty(cluster, "current-context"),
				Domain:      domain,
				HTTPTimeout: timeout,
			})
			if err != nil {
				return err
			}
			accept.Render(cmd.OutOrStdout(), rep)

			switch rep.State {
			case accept.StateUsable:
				return nil
			case accept.StateConverged:
				return &exitCodeError{code: 1, msg: fmt.Sprintf(
					"cluster is converged but NOT usable: %d required check(s) failed", len(rep.Failed()))}
			default:
				return &exitCodeError{code: 2, msg: "cluster is still reconciling (or unreadable)"}
			}
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "",
		"Cluster domain for the front-door TLS check (default: the cluster's own DOMAIN from the fleet)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second,
		"Per-probe timeout for the front-door check")
	return cmd
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// exitCodeError lets accept distinguish "not usable yet" from "broken", which a
// script gating on the install needs to tell apart.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }
func (e *exitCodeError) ExitCode() int { return e.code }
