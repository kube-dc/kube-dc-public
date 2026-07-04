package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
)

// bootstrapOpenBaoUnsealCmd registers `kube-dc bootstrap openbao
// unseal <cluster-name>` (M5-T02).
//
// Idempotent: pods already reporting `Sealed: false` are skipped.
// Reads OPENBAO_UNSEAL_KEY_{1..3} (threshold = 3) from
// clusters/<name>/secrets.enc.yaml — the remaining 2 shares stay
// encrypted to keep the in-memory plaintext window narrow.
//
// Operationally this is invoked:
//   - Immediately after `bootstrap openbao init` (some OpenBao
//     versions seal automatically after init; cheap re-unseal).
//   - On every pod restart (rolling deploy, node reboot, HelmRelease
//     upgrade). Auto-runnable from `bootstrap doctor` once that
//     surfaces sealed pods (M5 doctor signal — deferred from M1).
func bootstrapOpenBaoUnsealCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unseal <cluster-name>",
		Short: "Unseal OpenBao pods using the SOPS-encrypted shares (M5-T02)",
		Long: `Decrypts OPENBAO_UNSEAL_KEY_{1..3} from
clusters/<name>/secrets.enc.yaml in memory + runs
'bao operator unseal' on every sealed pod. The remaining 2 shares
stay encrypted on disk.

Idempotent: pods already unsealed are skipped with a status line.

Auto-scrubs every share buffer on return (success or error). The
decrypted secrets.enc.yaml blob is zeroed in-memory before unseal
returns.`,
		Example: `  # Standard flow after openbao init or on pod restart
  kube-dc bootstrap openbao unseal cloudacropolis --repo ~/projects/kube-dc-fleet`,
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}
			if repo == "" {
				return fmt.Errorf("openbao unseal: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("openbao unseal: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}

			return openbao.Unseal(cmd.Context(), openbao.UnsealOptions{
				ClusterName: clusterName,
				FleetRepo:   repo,
				SOPS:        session.SOPS,
				OpenBao:     session.OpenBao,
				Out:         cmd.OutOrStdout(),
			})
		},
	}
	return cmd
}
