package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
)

// bootstrapOpenBaoRevealSharesCmd registers `kube-dc bootstrap
// openbao reveal-shares <cluster-name>` (M5-T05).
//
// Emergency-only. Refuses without an explicit opt-in via either
// `--i-understand-the-risk` OR `REVEAL=true` env-var. The double
// gate is deliberate friction — this subcommand hands the operator
// enough share material to unseal a cluster; muscle-memory or
// typoed invocations must be denied.
//
// Shares stream to stdout (so `> shares.txt` cleanly captures them);
// the audit line goes to stderr so operators see who-ran-what-when
// on their terminal even when stdout is redirected.
//
// Exit codes (via doctorExitCodeErr):
//
//	0 — shares written to stdout, audit written to stderr
//	1 — consent gate not satisfied (recoverable — pass the flag/env)
//	2 — structural failure (decrypt error, missing shares, adapter error)
func bootstrapOpenBaoRevealSharesCmd(fleetRepo *string) *cobra.Command {
	var understand bool
	cmd := &cobra.Command{
		Use:           "reveal-shares <cluster-name>",
		Short:         "Emergency-only: decrypt every Shamir share to stdout (M5-T05)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Decrypts every OPENBAO_UNSEAL_KEY_{1..5} entry from
clusters/<name>/secrets.enc.yaml and streams them to stdout. All 5
shares, not just the 3-of-5 unseal threshold — this subcommand
exists for worst-case recovery (share-holder unreachable; need to
reconstruct threshold access via a different subset).

Refuses without an explicit opt-in: pass --i-understand-the-risk
OR set REVEAL=true in the environment. Both gates map to the same
consent bit; either is sufficient.

Output split:
  stdout — the share block (BEGIN/END markers + 5 KEY: VALUE lines)
  stderr — one audit line naming operator + cluster + timestamp

The audit line lands on the terminal even when stdout is redirected,
so a reveal is never invisible.

Never contacts the apiserver. Never mutates cluster state. The root
token is NOT surfaced by this subcommand — use openbao generate-root
for a fresh short-lived root token.

Exit code:
  0  shares revealed + audit written
  1  consent gate not satisfied
  2  structural failure (decrypt / missing shares / adapter error)

After use, clear your terminal + scrollback:
  clear && printf '\\033[3J'`,
		Example: `  # Emergency recovery — flag opt-in
  kube-dc bootstrap openbao reveal-shares eu/dc1 \
    --repo ~/projects/kube-dc-fleet --i-understand-the-risk > shares.txt

  # CI runbook opt-in via env
  REVEAL=true kube-dc bootstrap openbao reveal-shares eu/dc1 \
    --repo ./fleet > shares.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}
			if repo == "" {
				return fmt.Errorf("openbao reveal-shares: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			// Consent gate — either flag OR env-var. Refusing here
			// short-circuits before the session is built or the file
			// is touched, keeping the failure loud + cheap.
			consent := understand || os.Getenv("REVEAL") == "true"
			if !consent {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"[openbao] reveal-shares refuses without consent — pass --i-understand-the-risk or set REVEAL=true")
				return &doctorExitCodeErr{code: 1}
			}

			// Soft fleet-overlay existence check (same pattern as
			// `openbao status`). A missing overlay is almost always
			// a label typo — surface it before the decrypt error.
			secretsPath := filepath.Join(repo, "clusters", clusterName, "secrets.enc.yaml")
			if _, err := os.Stat(secretsPath); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"openbao reveal-shares: secrets.enc.yaml not found at %s: %v\n",
					secretsPath, err)
				return &doctorExitCodeErr{code: 2}
			}

			// Emergency-recovery path: operator may have SOPS key +
			// fleet repo but no valid kubeconfig (the whole reason
			// they're revealing shares — cluster is down / apiserver
			// unreachable). Bypass the full `bootstrap.NewSession`
			// which requires K8s adapter construction; wire just the
			// SOPS port via NewSOPSOnly. Honours KUBE_DC_MOCK for
			// hermetic tests. See wire.go NewSOPSOnly for rationale.
			sopsClient, err := bootstrap.NewSOPSOnly()
			if err != nil {
				// Structural failure per the doc'd exit-code
				// contract — same P2 fix as generate-root: return
				// exit 2 via doctorExitCodeErr rather than a plain
				// error (which main.go maps to exit 1).
				fmt.Fprintf(cmd.ErrOrStderr(), "openbao reveal-shares: build sops adapter: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}

			operator := os.Getenv("USER")
			// $USER is unset in some CI environments; fall back to
			// $LOGNAME before letting the engine hit "<unknown>".
			if operator == "" {
				operator = os.Getenv("LOGNAME")
			}

			if err := openbao.RevealShares(cmd.Context(), openbao.RevealOptions{
				ClusterName:  clusterName,
				FleetRepo:    repo,
				SOPS:         sopsClient,
				Consent:      true,
				OperatorName: operator,
				Out:          cmd.OutOrStdout(),
				Audit:        cmd.ErrOrStderr(),
			}); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openbao reveal-shares: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&understand, "i-understand-the-risk", false,
		"Explicit consent to reveal every Shamir share on stdout. Equivalent to REVEAL=true env-var.")
	return cmd
}
