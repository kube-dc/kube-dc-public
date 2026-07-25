package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/keycloak"
)

// bootstrapKeycloakInitCmd registers `kube-dc bootstrap keycloak init
// <cluster-name>` (D'-1).
//
// Wraps kube-dc-fleet/bootstrap/setup-keycloak-oidc.sh — the
// idempotent ceremony that creates the master-realm OIDC clients,
// extracts their secrets, and SOPS-encrypts them into the cluster
// overlay's secrets.enc.yaml.
//
// Pre-conditions: the platform Kustomization has the keycloak
// HelmRelease Ready (the script polls the OIDC discovery endpoint
// internally before doing anything else, so out-of-order invocations
// fail with an actionable timeout rather than producing partial
// state).
//
// Idempotent end-to-end: re-running on a cluster where all clients
// already exist produces no diff.
func bootstrapKeycloakInitCmd(fleetRepo *string) *cobra.Command {
	var noPush bool
	cmd := &cobra.Command{
		Use:   "init <cluster-name>",
		Short: "Bootstrap Keycloak OIDC clients + persist encrypted secrets (D'-1)",
		Long: `Runs bootstrap/setup-keycloak-oidc.sh against the named cluster's
Keycloak deployment. The script:

  1. Polls Keycloak's master-realm OIDC discovery endpoint until
     it returns HTTP 200 (10-minute budget).
  2. Reads the Keycloak admin password from cluster-secrets in
     flux-system (or the keycloak Secret in the keycloak namespace
     for older bootstraps).
  3. Ensures the realm accessTokenLifespan is ≥ 900s.
  4. Creates the 'groups' client scope + group-membership mapper.
  5. Creates the flux-web (confidential), grafana (confidential),
     and kube-dc-admin (PKCE/public) clients. The 'groups' scope is
     assigned as default on flux-web + grafana so role-mapping in
     each application can match the admin group.
  6. Creates the 'admin' group; adds the bootstrap 'admin' user;
     sets the user's email (Grafana requires this).
  7. Extracts and round-trip-verifies the Flux, Grafana, and confidential
     admin-backend secrets. The admin Secret is materialised live before the
     chart reference is enabled, then owned by the generated addons layer.
  8. Commits and pushes the encrypted files plus chart wiring (--no-push
     keeps the commit local for an explicit test workflow).

After this completes, run:

  flux reconcile kustomization flux-system --with-source
  flux reconcile kustomization addons
  flux reconcile kustomization platform
  kubectl rollout restart deploy/flux-operator       -n flux-system
  kubectl rollout restart deploy/prom-operator-grafana -n monitoring

The deployments need to re-read their env-from Secret references —
they don't pick up Secret rotations without a restart.`,
		Example: `  # After keycloak HelmRelease goes Ready
  kube-dc bootstrap keycloak init atlantis --repo ~/projects/kube-dc-fleet`,
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
				return fmt.Errorf("keycloak init: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("keycloak init: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}

			out := cmd.OutOrStdout()
			var token string
			if !noPush {
				token = resolveGHTokenFor("keycloak", out)
			}

			return keycloak.Init(cmd.Context(), keycloak.InitOptions{
				ClusterName: clusterName,
				FleetRepo:   repo,
				Runner:      session.Scripts,
				Git:         session.Git,
				GitHubToken: token,
				NoPush:      noPush,
				Out:         out,
			})
		},
	}
	cmd.Flags().BoolVar(&noPush, "no-push", false,
		"Commit locally; do not push the Keycloak client secrets and chart wiring")
	return cmd
}
