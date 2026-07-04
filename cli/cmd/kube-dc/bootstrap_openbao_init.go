package main

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
)

// bootstrapOpenBaoInitCmd registers `kube-dc bootstrap openbao init
// <cluster-name>` (M5-T01).
//
// Runs after Flux brings up the openbao HelmRelease. The script
// itself is intentionally non-idempotent (matches the fleet script's
// "running twice is an error" contract); the CLI doesn't add an
// idempotence wrapper here — the operator runs this exactly once
// per cluster.
func bootstrapOpenBaoInitCmd(fleetRepo *string) *cobra.Command {
	var (
		sharesOut          string
		noPush             bool
		skipControllerAuth bool
	)
	cmd := &cobra.Command{
		Use:   "init <cluster-name>",
		Short: "Initialize OpenBao + encrypt Shamir shares into secrets.enc.yaml (M5-T01)",
		Long: `Runs bootstrap/openbao-init.sh against the named cluster's openbao-0
pod. The script's JSON output (5 Shamir shares + 1 root token) is
captured in-memory via the ScriptRunner's sentinel callback — the
plaintext NEVER touches disk on the operator's machine. The CLI:

  1. Validates the cluster overlay exists (clusters/<name>/secrets.enc.yaml).
  2. Captures the pre-commit SHA for rollback.
  3. Runs the script; the bracketed JSON between
     KUBE_DC_INIT_JSON_BEGIN / KUBE_DC_INIT_JSON_END diverts to a
     secrets.Buffer (never echoed to logs).
  4. For each of the 5 shares: SOPS-encrypts into secrets.enc.yaml
     under OPENBAO_UNSEAL_KEY_{1..5}, then round-trip-verifies by
     re-decrypting + bytes-comparing.
  5. Optionally writes an off-fleet plaintext copy via
     --openbao-shares-out=<path> (mode 0600; refuses paths inside
     any git working tree).
  6. Commits + pushes the updated secrets.enc.yaml. Push failure
     triggers ResetHard to the captured pre-commit SHA.

Per installer-prd §12.3: never writes plaintext shares to disk on
its own; the optional shares-out file is the operator's explicit
choice for off-fleet backup.`,
		Example: `  # Standard install flow against an existing fleet repo
  kube-dc bootstrap openbao init atlantis --repo ~/projects/kube-dc-fleet

  # Off-fleet plaintext backup (operator-managed)
  kube-dc bootstrap openbao init atlantis --repo ~/projects/kube-dc-fleet \
    --openbao-shares-out=/secure/usb/atlantis-shares.yaml

  # Local-only commit (for testing the share-encryption path)
  kube-dc bootstrap openbao init atlantis --repo ~/projects/kube-dc-fleet --no-push`,
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
				return fmt.Errorf("openbao init: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("openbao init: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}

			out := cmd.OutOrStdout()
			// --no-push skips push, so we don't need the GitHub
			// token (mirrors M4-T12 review-pass — P3). On the push
			// path, source from `gh auth token` directly (no
			// --github-token flag on this subcommand).
			var token string
			if !noPush {
				token = resolveGHToken(out)
			}

			return openbao.Init(cmd.Context(), openbao.InitOptions{
				ClusterName:        clusterName,
				FleetRepo:          repo,
				Runner:             session.Scripts,
				SOPS:               session.SOPS,
				Git:                session.Git,
				OpenBao:            session.OpenBao,
				K8s:                session.K8s,
				GitHubToken:        token,
				NoPush:             noPush,
				SharesOutPath:      sharesOut,
				SkipControllerAuth: skipControllerAuth,
				Out:                out,
			})
		},
	}
	cmd.Flags().StringVar(&sharesOut, "openbao-shares-out", "",
		"Optional path to write a plaintext YAML copy of the 5 shares (mode 0600; refuses paths inside a git tree)")
	cmd.Flags().BoolVar(&noPush, "no-push", false,
		"Commit locally; do not push to the fleet remote (for testing the share-encryption path)")
	// --skip-controller-auth is intentionally not in the Example
	// block above — it's an emergency / debug escape hatch, not the
	// normal flow. Use it only when init must complete share custody
	// before OpenBao auth-mount writes are possible (e.g. wedged
	// OpenBao state). The recovery path is `bootstrap openbao
	// setup-controller-auth <name>`.
	cmd.Flags().BoolVar(&skipControllerAuth, "skip-controller-auth", false,
		"EMERGENCY/DEBUG: skip Phase C controller-auth setup. Cluster will not be usable until `bootstrap openbao setup-controller-auth` runs.")
	return cmd
}

// resolveGHToken sources the GitHub PAT via `gh auth token`. The
// openbao init subcommand doesn't take a --github-token flag (the
// operator's `gh auth login` session is the canonical source for
// this slice — the cluster scaffold path's --github-token override
// is for one-shot CI runs that don't share a gh session). Returns
// empty string on any error; the push will surface the auth
// failure from the git adapter if a token was actually required.
//
// Token value is NEVER logged.
func resolveGHToken(out io.Writer) string {
	cmd := exec.Command("gh", "auth", "token")
	cmd.Stderr = io.Discard
	stdout, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(out, "[openbao] no GitHub token resolved (gh auth token failed; push may fail if remote requires auth)")
		return ""
	}
	token := strings.TrimSpace(string(stdout))
	if token == "" {
		fmt.Fprintln(out, "[openbao] no GitHub token resolved (gh auth token returned empty)")
		return ""
	}
	fmt.Fprintln(out, "[openbao] resolved GitHub token via gh auth token")
	return token
}
