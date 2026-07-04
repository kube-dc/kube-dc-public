package main

import (
	"github.com/spf13/cobra"
)

// bootstrapOpenBaoCmd registers `kube-dc bootstrap openbao` and its
// subcommands (M5). The no-arg form prints help; operators reach the
// per-cluster ceremony via:
//
//	# Fresh install: two commands
//	kube-dc bootstrap openbao init                   <name> --repo <fleet>
//	kube-dc bootstrap openbao unseal                 <name> --repo <fleet>
//
//	# Day-2 commands
//	kube-dc bootstrap openbao setup-controller-auth  <name> --repo <fleet>
//	kube-dc bootstrap openbao setup-controller-auth  <name> --repo <fleet> --refresh-policy
//	kube-dc bootstrap openbao status                 <name> --repo <fleet>
//	kube-dc bootstrap openbao reveal-shares          <name> --repo <fleet> --i-understand-the-risk
//	kube-dc bootstrap openbao generate-root          <name> --repo <fleet> --i-understand-the-risk
func bootstrapOpenBaoCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "openbao",
		Short:         "OpenBao share-custody + unseal + controller-auth ceremony for a fleet cluster (M5)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `OpenBao subcommands cover the fresh-install ceremony + day-2
operations against an OpenBao deployment in a kube-dc-fleet cluster.
The share-custody contract is documented in installer-prd §12.3:
shares + root token live only in a secrets.Buffer, never on disk
on the operator's machine.

Fresh install (two commands, no shell scripts):

  - init captures the 5 Shamir shares + root token in-memory only,
    encrypts the shares into clusters/<name>/secrets.enc.yaml via
    SOPS, commits + pushes, unseals openbao-0, AND (since M5-T08)
    runs the controller-auth setup as part of Phase C — auth/k8s-host
    + kube-dc-controller-manager and db-manager policies + roles
    land in one ceremony. Plaintext never on disk.

  - unseal decrypts OPENBAO_UNSEAL_KEY_{1..3} from secrets.enc.yaml
    and feeds them to every sealed pod (joins fresh Raft followers
    first). Idempotent — pods already unsealed are no-ops.

Day-2 operations:

  - setup-controller-auth re-runs the controller-auth setup against
    an already-initialized cluster — for partial-init recovery, for
    migrating clusters that were bootstrapped by the legacy shell
    script, and (--refresh-policy) for chart-upgrade HCL refreshes
    that extend policy paths or rotate role params.

  - status is a read-only projection of every OpenBao pod
    (Initialized / Sealed / version / HA mode) plus the two svc/openbao
    operational markers. No SOPS key required. Exits 0 fully-ready,
    1 recoverable (sealed / missing marker), 2 structural failure.

  - reveal-shares (EMERGENCY-ONLY) decrypts every OPENBAO_UNSEAL_KEY_
    {1..5} entry to stdout. Refuses without --i-understand-the-risk
    or REVEAL=true. Audit line lands on stderr so the reveal is
    never invisible.

  - generate-root (EMERGENCY / DIAGNOSTIC) runs the generate-root
    ceremony against live OpenBao pods and emits a short-lived
    root token to stdout. Same two-gate consent as reveal-shares.
    --revoke-immediately turns it into a ceremony health-check
    (no live token left when the command returns).

All commands take the cluster name as a positional arg (use slash
form 'eu/dc1' for clusters nested under a region subdir) + --repo
from the parent bootstrap command.`,
	}
	cmd.AddCommand(bootstrapOpenBaoInitCmd(fleetRepo))
	cmd.AddCommand(bootstrapOpenBaoUnsealCmd(fleetRepo))
	cmd.AddCommand(bootstrapOpenBaoSetupControllerAuthCmd(fleetRepo))
	cmd.AddCommand(bootstrapOpenBaoStatusCmd(fleetRepo))
	cmd.AddCommand(bootstrapOpenBaoRevealSharesCmd(fleetRepo))
	cmd.AddCommand(bootstrapOpenBaoGenerateRootCmd(fleetRepo))
	return cmd
}
