package main

import (
	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/breakglass"
)

// bootstrapBreakGlassCmd registers `kube-dc bootstrap break-glass …` —
// the recovery kubeconfig flow described in installer-prd §16.3.3.
//
// Surface:
//
//   bootstrap break-glass adopt   <cluster>   create SA + token + sops-encrypted kubeconfig
//   bootstrap break-glass         <cluster>   open a sub-shell with the decrypted kubeconfig
//   bootstrap break-glass rotate  <cluster>   delete + recreate the SA token, re-encrypt
//   bootstrap break-glass status  <cluster>   summary (server URL, last-modified) without using
func bootstrapBreakGlassCmd(repoFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "break-glass <cluster>",
		Short: "Recovery kubeconfig (use ONLY when OIDC is unavailable)",
		Long: `break-glass manages the static-token cluster-admin kubeconfig committed
to the fleet repo at clusters/<name>/break-glass-kubeconfig.enc.yaml.
Used only when OIDC is unavailable (Keycloak down, auth-config-sync wedged
on the apiserver, master realm unreachable). For everyday admin work,
use 'kube-dc login --admin' instead — that gives per-engineer audit trail.

Flow:
  adopt   one-time per cluster: create the ServiceAccount + token Secret +
          ClusterRoleBinding on the cluster, encrypt the resulting
          kubeconfig with SOPS, write to clusters/<name>/break-glass-kubeconfig.enc.yaml
  use     decrypt the encrypted kubeconfig to a tempfile, spawn a sub-shell
          with KUBECONFIG=<tempfile>. Tempfile is removed on shell exit.
  rotate  delete the existing SA token Secret so K8s mints a fresh token,
          re-encrypt the kubeconfig with the new value. Run after every use.
  status  show non-secret metadata (server URL, last-modified) without
          opening the kubeconfig.

The bare 'break-glass <cluster>' form is shorthand for 'use'.`,
		Example: `  # First time — adopt an existing cluster you already have admin on:
  kube-dc bootstrap break-glass adopt cloud

  # Recovery: open a sub-shell with the break-glass kubeconfig
  kube-dc bootstrap break-glass cloud
  # …recovery work…
  exit

  # After every break-glass use, rotate the token:
  kube-dc bootstrap break-glass rotate cloud
  git -C ~/projects/kube-dc-fleet add clusters/cloud/break-glass-kubeconfig.enc.yaml
  git -C ~/projects/kube-dc-fleet commit -m "cloud: rotate break-glass token after recovery"
  git -C ~/projects/kube-dc-fleet push`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			return breakglass.Use(cmd.Context(), repo, args[0])
		},
	}

	cmd.AddCommand(bootstrapBreakGlassAdoptCmd(repoFlag))
	cmd.AddCommand(bootstrapBreakGlassUseCmd(repoFlag))
	cmd.AddCommand(bootstrapBreakGlassRotateCmd(repoFlag))
	cmd.AddCommand(bootstrapBreakGlassStatusCmd(repoFlag))
	return cmd
}

func bootstrapBreakGlassAdoptCmd(repoFlag *string) *cobra.Command {
	var (
		kubectlContext string
		serverURL      string
		dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "adopt <cluster>",
		Short: "Create the SA + token + encrypted kubeconfig for an existing cluster",
		Long: `Adopt the named cluster as a break-glass target. Requires that you
already have admin access via your current kubectl context (or pass
--kube-context). Creates the SA + ClusterRoleBinding + token Secret in
kube-system, waits for the SA-token controller to populate the Secret,
builds a kubeconfig from the SA token + cluster CA, and SOPS-encrypts
it to clusters/<name>/break-glass-kubeconfig.enc.yaml.

Idempotent: running again on an already-adopted cluster patches
existing objects and refreshes the encrypted file with the current
token. (Use 'rotate' if you want a NEW token, not a refresh of the
encrypted copy of the existing one.)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			return breakglass.Adopt(cmd.Context(), breakglass.AdoptOpts{
				FleetRoot:      repo,
				ClusterName:    args[0],
				KubectlContext: kubectlContext,
				ServerURL:      serverURL,
				DryRun:         dryRun,
			})
		},
	}
	cmd.Flags().StringVar(&kubectlContext, "kube-context", "", "kubectl context with admin access (defaults to current-context)")
	cmd.Flags().StringVar(&serverURL, "server", "", "Override apiserver URL (defaults to KUBE_API_EXTERNAL_URL from cluster-config.env)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without applying or writing")
	return cmd
}

func bootstrapBreakGlassUseCmd(repoFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "use <cluster>",
		Short: "Open a sub-shell with the break-glass kubeconfig (= bare `break-glass <cluster>`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			return breakglass.Use(cmd.Context(), repo, args[0])
		},
	}
}

func bootstrapBreakGlassRotateCmd(repoFlag *string) *cobra.Command {
	var (
		kubectlContext string
		serverURL      string
		dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "rotate <cluster>",
		Short: "Rotate the break-glass SA token + re-encrypt the fleet's kubeconfig",
		Long: `Delete the break-glass SA token Secret on the cluster (the SA itself
stays put), let the K8s controller mint a fresh token, then re-encrypt
the kubeconfig at clusters/<name>/break-glass-kubeconfig.enc.yaml with
the new value. Run after every break-glass session.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			return breakglass.Rotate(cmd.Context(), breakglass.AdoptOpts{
				FleetRoot:      repo,
				ClusterName:    args[0],
				KubectlContext: kubectlContext,
				ServerURL:      serverURL,
				DryRun:         dryRun,
			})
		},
	}
	cmd.Flags().StringVar(&kubectlContext, "kube-context", "", "kubectl context with admin access (defaults to current-context)")
	cmd.Flags().StringVar(&serverURL, "server", "", "Override apiserver URL (defaults to KUBE_API_EXTERNAL_URL from cluster-config.env)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without applying")
	return cmd
}

func bootstrapBreakGlassStatusCmd(repoFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status <cluster>",
		Short: "Show non-secret metadata about the break-glass kubeconfig",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			return breakglass.Status(cmd.Context(), repo, args[0])
		},
	}
}
