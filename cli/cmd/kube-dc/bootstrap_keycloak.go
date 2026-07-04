package main

import (
	"github.com/spf13/cobra"
)

// bootstrapKeycloakCmd registers `kube-dc bootstrap keycloak` and its
// subcommands. The post-bootstrap SSO ceremony (D'-1 in
// docs/prd/installer-bugs-real-install.md): create the master-realm
// OIDC clients that flux-web, grafana, and `kube-dc login --admin`
// all depend on, extract their client secrets, and SOPS-encrypt
// them into the cluster overlay's secrets.enc.yaml.
//
// Today the engine is a thin wrapper around
// kube-dc-fleet/bootstrap/setup-keycloak-oidc.sh — the canonical
// script that owns the Keycloak admin REST dance. Future work moves
// the logic into a pure-Go engine + REST adapter so we get the same
// testing surface as the OpenBao + clusterinit engines.
//
// The subcommand surface mirrors `bootstrap openbao`:
//
//	kube-dc bootstrap keycloak init <name> --repo <fleet>
//
// Run after the platform Kustomization has the keycloak HelmRelease
// Ready (the script polls the OIDC discovery endpoint internally
// before doing anything else).
func bootstrapKeycloakCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "keycloak",
		Short:         "Keycloak master-realm OIDC clients ceremony (D'-1)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Keycloak subcommands wrap bootstrap/setup-keycloak-oidc.sh,
which creates the master-realm OIDC clients that the platform's
public-facing services depend on:

  - flux-web (confidential)   - Flux Web UI SSO
  - grafana  (confidential)   - Grafana SSO
  - kube-dc-admin (PKCE/public) - ` + "`kube-dc login --admin`" + ` flow

Each client's secret is extracted from Keycloak, SOPS-encrypted
into clusters/<name>/secrets.enc.yaml (FLUX_WEB_OIDC_CLIENT_SECRET
and GRAFANA_OIDC_CLIENT_SECRET), and committed to the fleet repo.
The 'admin' group + 'groups' client scope + group-membership
mapper are all configured idempotently so re-running the command
on an already-bootstrapped cluster is safe and produces no diff.

The subcommand takes the cluster name as a positional arg + --repo
from the parent bootstrap command. Both must point at the fleet
repo that holds the cluster overlay.`,
	}
	cmd.AddCommand(bootstrapKeycloakInitCmd(fleetRepo))
	return cmd
}
