package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/access"
)

// bootstrapAccessCmd registers `kube-dc bootstrap access <cluster>`.
//
// Surfaces installer-ux-prd §5.4's success-summary on demand: every
// public URL the platform serves + the NAMES of secrets in the
// cluster overlay's secrets.enc.yaml + how to reveal them. Read-only,
// decryption-free (no SOPS age key needed). Safe to run from CI,
// ops chat, anywhere.
//
// The success summary is normally displayed at the end of
// `kube-dc bootstrap init` (installer-ux-prd §5.4 lines 363-392).
// This subcommand re-displays it any time later.
func bootstrapAccessCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access <cluster-name>",
		Short: "Print the post-install access summary (URLs, secret names, login how-to)",
		Long: `Reads clusters/<name>/cluster-config.env + the NAMES of
secrets in clusters/<name>/secrets.enc.yaml (no decryption) and
prints the same success-summary that 'kube-dc bootstrap init'
shows at the end of an install.

The output covers:
  - Every public URL the platform serves (console, keycloak,
    grafana, flux web UI, openbao, S3, mimir, loki, the K8s API).
  - How to sign in (SSO for the UIs, kube-dc login --admin for
    kubectl, openbao unseal/reveal ceremonies).
  - The list of secrets in secrets.enc.yaml — by NAME, never by
    value — plus the commands to reveal individual values.

Read-only: this subcommand never decrypts anything, so it can
run without your SOPS age key. Reveal commands (sops -d, kube-dc
bootstrap openbao reveal-shares) are printed for the operator to
copy-paste when they need the actual values.`,
		Example: `  kube-dc bootstrap access atlantis --repo ~/projects/kube-dc-fleet`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}
			if repo == "" {
				return fmt.Errorf("access: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/cluster-config.env", clusterName)
			}
			return access.Print(access.Options{
				ClusterName: clusterName,
				FleetRepo:   repo,
				Out:         cmd.OutOrStdout(),
			})
		},
	}
	return cmd
}
