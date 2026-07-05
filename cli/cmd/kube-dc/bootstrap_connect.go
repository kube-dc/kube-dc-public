package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/connectcheck"
)

// bootstrapConnectCmd registers `kube-dc bootstrap connect <ssh-host>`
// (V5 — Layer-0 reachability). A pre-flight for `install`: proves the CLI
// can reach + drive a node over SSH (through a bastion if needed) before
// committing to an install.
func bootstrapConnectCmd(fleetRepo *string) *cobra.Command {
	var (
		sshJump      string
		sshAcceptNew bool
	)
	cmd := &cobra.Command{
		Use:           "connect <ssh-host>",
		Short:         "Pre-flight a node's SSH reachability before install (Layer-0)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Checks whether the CLI can reach and drive a node over SSH — the
Layer-0 pre-flight for 'kube-dc bootstrap install'. It verifies, from
wherever you run it (including through a bastion):

  - SSH reach + auth (ssh-agent / ~/.ssh/config IdentityFile)
  - passwordless sudo (install runs 'sudo -n')
  - the node's internal IP (what would become advertise-address)
  - OS / login user

Exits non-zero if the node is unreachable or lacks passwordless sudo, so
it doubles as a CI gate. Use --ssh-jump (or ~/.ssh/config ProxyJump) for a
bastion, and --ssh-accept-new-host-keys for a first-contact host key.

SSH auth: ssh-agent first, then ~/.ssh/config IdentityFile (never a
--ssh-key flag).`,
		Example: `  # Direct
  kube-dc bootstrap connect root@203.0.113.10

  # Through a bastion, first contact (records the host key)
  kube-dc bootstrap connect root@10.0.0.10 \
    --ssh-jump root@bastion.example.com --ssh-accept-new-host-keys`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sshOpts []sshadapter.Option
			if sshAcceptNew {
				sshOpts = append(sshOpts, sshadapter.WithAcceptNewHostKeys())
			}
			sshClient, err := bootstrap.NewSSHOnly(sshOpts...)
			if err != nil {
				return fmt.Errorf("bootstrap connect: build ssh adapter: %w", err)
			}
			host := parseSSHHostArg(args[0])
			if sshJump != "" {
				host.ProxyJump = sshJump
			}
			err = connectcheck.Check(cmd.Context(), connectcheck.Options{
				SSH:  sshClient,
				Host: host,
				Out:  cmd.OutOrStdout(),
			})
			// Unreachable / no-sudo are expected diagnostic outcomes: the
			// report already printed, so surface a clean non-zero exit
			// rather than a duplicate wrapped error.
			if errors.Is(err, connectcheck.ErrUnreachable) || errors.Is(err, connectcheck.ErrNoPasswordlessSudo) {
				return &doctorExitCodeErr{code: 1}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&sshJump, "ssh-jump", "",
		"SSH jump chain to reach the node: `[user@]host[:port][,...]` (like ssh -J). Empty honours ~/.ssh/config ProxyJump")
	cmd.Flags().BoolVar(&sshAcceptNew, "ssh-accept-new-host-keys", false,
		"Trust-on-first-use for the node's SSH host key (a MISMATCH is still refused)")
	_ = fleetRepo // connect is fleet-independent; flag accepted for parity
	return cmd
}
