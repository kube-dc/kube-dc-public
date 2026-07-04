package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
)

// bootstrapOpenBaoGenerateRootCmd registers `kube-dc bootstrap
// openbao generate-root <cluster-name>` (M5-T06).
//
// One-shot diagnostic that runs the OpenBao `bao operator
// generate-root` ceremony against the live cluster's OpenBao pods
// and prints the resulting single-use root token to stdout.
//
// Distinct from `reveal-shares` (M5-T05):
//
//   - Reveal-shares hands out persistent SHARE material (unseal
//     keys), works on SOPS material alone, no cluster contact.
//   - Generate-root hands out a short-lived ROOT TOKEN, REQUIRES a
//     live cluster (the ceremony runs via kubectl exec in the
//     active OpenBao pod).
//
// Two-gate consent, same shape as reveal-shares:
//
//	--i-understand-the-risk  (interactive flag)
//	REVEAL=true              (env-var, CI/runbook)
//
// Revoke policy:
//
//   - Default: print token, do NOT auto-revoke. Operators asking
//     for a root token typically want a working one — auto-revoking
//     it produces a useless output. Operator revokes manually via
//     `bao token revoke <token>` when done.
//   - `--revoke-immediately`: print token then RevokeSelf. Health-
//     check mode — proves generate-root works without leaving a
//     live root token. Suitable for CI smokes.
//
// Exit codes (via doctorExitCodeErr):
//
//	0 — token emitted + audit written (+ revoked if --revoke-immediately)
//	1 — consent gate not satisfied
//	2 — structural failure (decrypt / missing shares / adapter /
//	    ceremony / revoke error)
func bootstrapOpenBaoGenerateRootCmd(fleetRepo *string) *cobra.Command {
	var understand bool
	var revokeNow bool
	cmd := &cobra.Command{
		Use:           "generate-root <cluster-name>",
		Short:         "Emergency-only: run generate-root ceremony + emit a single-use root token (M5-T06)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Runs OpenBao's 'bao operator generate-root' ceremony against
the live OpenBao pods (kubectl exec inside the active pod) using the
3-of-5 Shamir threshold from clusters/<name>/secrets.enc.yaml, then
emits the resulting root token to stdout.

Distinct from reveal-shares — the token is short-lived (subject to
OpenBao's default root-token TTL) and REVOCABLE. Reveal-shares hands
out share material that unseals the cluster; this hands out a token
that acts AS the cluster's root.

Refuses without --i-understand-the-risk OR REVEAL=true.

Requires a reachable cluster — the ceremony runs in-pod via
kubectl exec. Unlike reveal-shares this is NOT a cluster-is-down
emergency path.

Revoke policy:
  Default             — print token, LIVE, operator revokes manually
                        via 'bao token revoke <token>' when done.
  --revoke-immediately — print token then RevokeSelf. Health-check
                        mode; proves the ceremony works end-to-end.
                        Suitable for CI smokes.

Output split:
  stdout — OPENBAO_ROOT_TOKEN: <value>
  stderr — one audit line + revoke-status line

Exit code:
  0  token emitted + audit written
  1  consent gate not satisfied
  2  structural failure (decrypt / ceremony / revoke error)

After use, revoke:
  bao token revoke <token>
or scrub the terminal (clear + scrollback erase) so the token
doesn't survive the shell session.`,
		Example: `  # Emergency: get a working root token
  kube-dc bootstrap openbao generate-root cs/zrh \
    --repo ~/projects/kube-dc-fleet --i-understand-the-risk

  # CI smoke: prove generate-root still works, no live token left
  REVEAL=true kube-dc bootstrap openbao generate-root cs/zrh \
    --repo ./fleet --revoke-immediately`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}
			if repo == "" {
				return fmt.Errorf("openbao generate-root: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			// Consent gate — short-circuit before any adapter build.
			consent := understand || os.Getenv("REVEAL") == "true"
			if !consent {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"[openbao] generate-root refuses without consent — pass --i-understand-the-risk or set REVEAL=true")
				return &doctorExitCodeErr{code: 1}
			}

			// Soft fleet-overlay existence check catches label typos
			// before session build + decrypt error surface.
			secretsPath := filepath.Join(repo, "clusters", clusterName, "secrets.enc.yaml")
			if _, err := os.Stat(secretsPath); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"openbao generate-root: secrets.enc.yaml not found at %s: %v\n",
					secretsPath, err)
				return &doctorExitCodeErr{code: 2}
			}

			// Full session — this subcommand DOES need K8s + OpenBao
			// (the ceremony runs in-pod). Unlike M5-T05 reveal-shares,
			// no-kubeconfig is a hard failure here: without a live
			// cluster the whole operation is meaningless. Reviewer P2
			// fix: session-build failure is a STRUCTURAL failure per
			// the doc'd exit-code contract; return exit 2 via
			// doctorExitCodeErr rather than a plain error (which
			// main.go maps to exit 1 → indistinguishable from consent
			// refusal in CI).
			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openbao generate-root: build session: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}
			if session != nil {
				defer session.Close()
			}

			operator := os.Getenv("USER")
			if operator == "" {
				operator = os.Getenv("LOGNAME")
			}

			if err := openbao.GenerateRoot(cmd.Context(), openbao.GenerateRootOptions{
				ClusterName:       clusterName,
				FleetRepo:         repo,
				SOPS:              session.SOPS,
				OpenBao:           session.OpenBao,
				Consent:           true,
				OperatorName:      operator,
				RevokeImmediately: revokeNow,
				Out:               cmd.OutOrStdout(),
				Audit:             cmd.ErrOrStderr(),
			}); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openbao generate-root: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&understand, "i-understand-the-risk", false,
		"Explicit consent to run the generate-root ceremony + emit a root token on stdout. Equivalent to REVEAL=true env-var.")
	cmd.Flags().BoolVar(&revokeNow, "revoke-immediately", false,
		"Revoke the generated token right after emitting it. Turns this into a health-check for the generate-root ceremony; the emitted token will not be usable by the time the command returns.")
	return cmd
}
