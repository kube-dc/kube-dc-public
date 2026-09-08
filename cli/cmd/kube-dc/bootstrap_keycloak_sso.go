package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/keycloak"
)

// bootstrapKeycloakSSOCmd registers `kube-dc bootstrap keycloak sso
// <cluster-name>` — self-service sign-up for the console.
//
// Wraps kube-dc-fleet/bootstrap/setup-sso-realm.sh: creates the central
// `sso` realm (email self-registration + verification; Google IdP only when
// the GOOGLE_* credential pair is provided — email-only is a fully
// supported mode), SOPS-encrypts the SMTP credentials + broker secret into
// the cluster overlay, and flips SSO_ENABLED=true ONLY after every realm
// step succeeded (the flag without the realm fails every Organization
// sync — E2E finding 20; this command makes the ordering unmisusable).
//
// SECRET DISCIPLINE (same as the DNS-01 token): SMTP_USER, SMTP_PASSWORD
// and GOOGLE_CLIENT_SECRET are accepted via environment only — there are
// deliberately no flags for them, so they cannot land in shell history or
// process listings. Non-secret settings have flags and fall back to the
// cluster's existing cluster-config.env values.
func bootstrapKeycloakSSOCmd(fleetRepo *string) *cobra.Command {
	var (
		noPush         bool
		smtpHost       string
		smtpPort       string
		smtpFrom       string
		smtpFromName   string
		smtpSecure     string
		googleClientID string
	)
	cmd := &cobra.Command{
		Use:   "sso <cluster-name>",
		Short: "Enable self-service sign-up: sso realm + SMTP, email-only or with Google",
		Long: `Runs bootstrap/setup-sso-realm.sh against the named cluster. The script:

  1. Reads the Keycloak admin password from the cluster
     (secret keycloak/keycloak) via your KUBECONFIG.
  2. Creates the 'sso' realm: email self-registration, mandatory email
     verification, passwordless registration flow, the kube-dc console
     client (PKCE) and the sso-broker client.
  3. Adds the Google identity provider ONLY when GOOGLE_CLIENT_ID (flag)
     and GOOGLE_CLIENT_SECRET (env) are both provided — without them the
     realm is email-only and the console hides the Google buttons.
  4. Configures the realm's SMTP from the values below (email
     verification cannot work without a mail relay).
  5. Verifies the sign-up surface end-to-end (realm discovery and the
     registration page must both answer 200).
  6. Persists everything in the fleet overlay: SMTP_USER/SMTP_PASSWORD +
     SSO_BROKER_SECRET (+ GOOGLE_CLIENT_SECRET) SOPS-encrypted into
     secrets.enc.yaml, the non-secret SMTP keys and SSO_ENABLED=true into
     cluster-config.env — committed and pushed.

Secrets come from the environment ONLY:

  SMTP_USER / SMTP_PASSWORD   required on the first run; later runs reuse
                              the SOPS-stored values
  GOOGLE_CLIENT_SECRET        only together with --google-client-id

Idempotent: re-running converges with no Git diff, and the stored
SSO_BROKER_SECRET is never rotated implicitly.

After it completes, reconcile so the platform picks the flag up:

  flux reconcile kustomization flux-system --with-source
  flux reconcile kustomization platform`,
		Example: `  # Email-only sign-up (no Google):
  SMTP_USER=postmark-token SMTP_PASSWORD=postmark-token \
    kube-dc bootstrap keycloak sso atlantis --repo ~/fleet \
    --smtp-host smtp.postmarkapp.com --smtp-port 587 --smtp-from noreply@example.com

  # With Google social login:
  SMTP_USER=... SMTP_PASSWORD=... GOOGLE_CLIENT_SECRET=... \
    kube-dc bootstrap keycloak sso atlantis --repo ~/fleet \
    --smtp-host smtp.example.com --google-client-id 1234-abc.apps.googleusercontent.com`,
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
				return fmt.Errorf("keycloak sso: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/", clusterName)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("keycloak sso: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}

			env := map[string]string{}
			set := func(k, v string) {
				if v != "" {
					env[k] = v
				}
			}
			set("SMTP_HOST", smtpHost)
			set("SMTP_PORT", smtpPort)
			set("SMTP_FROM", smtpFrom)
			set("SMTP_FROM_NAME", smtpFromName)
			set("SMTP_SECURE", smtpSecure)
			set("GOOGLE_CLIENT_ID", googleClientID)
			// Secrets: environment pass-through only.
			set("SMTP_USER", os.Getenv("SMTP_USER"))
			set("SMTP_PASSWORD", os.Getenv("SMTP_PASSWORD"))
			set("GOOGLE_CLIENT_SECRET", os.Getenv("GOOGLE_CLIENT_SECRET"))

			out := cmd.OutOrStdout()
			var token string
			if !noPush {
				token = resolveGHTokenFor("keycloak", out)
			}

			return keycloak.SSORealm(cmd.Context(), keycloak.SSOOptions{
				ClusterName: clusterName,
				FleetRepo:   repo,
				Runner:      session.Scripts,
				Git:         session.Git,
				GitHubToken: token,
				NoPush:      noPush,
				Out:         out,
				Env:         env,
			})
		},
	}
	cmd.Flags().BoolVar(&noPush, "no-push", false,
		"Commit the fleet config locally; do not push")
	cmd.Flags().StringVar(&smtpHost, "smtp-host", "",
		"SMTP relay host (falls back to SMTP_HOST in cluster-config.env)")
	cmd.Flags().StringVar(&smtpPort, "smtp-port", "",
		"SMTP port (default 587/STARTTLS; falls back to cluster-config.env)")
	cmd.Flags().StringVar(&smtpFrom, "smtp-from", "",
		"From address for verification mail (falls back to EMAIL in cluster-config.env)")
	cmd.Flags().StringVar(&smtpFromName, "smtp-from-name", "",
		"Display name on verification mail")
	cmd.Flags().StringVar(&smtpSecure, "smtp-secure", "",
		"true = TLS-on-connect (port 465); default false = STARTTLS (port 587)")
	cmd.Flags().StringVar(&googleClientID, "google-client-id", "",
		"Google OAuth client id — enables the Google IdP (secret via GOOGLE_CLIENT_SECRET env)")
	return cmd
}
