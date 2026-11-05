package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
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
  kube-dc bootstrap openbao init east1 --repo ~/projects/kube-dc-fleet

  # Off-fleet plaintext backup (operator-managed)
  kube-dc bootstrap openbao init east1 --repo ~/projects/kube-dc-fleet \
    --openbao-shares-out=/secure/usb/east1-shares.yaml

  # Local-only commit (for testing the share-encryption path)
  kube-dc bootstrap openbao init east1 --repo ~/projects/kube-dc-fleet --no-push`,
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
				token = resolveGHToken(repo, out)
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

// resolveGHToken sources the Fleet push token for openbao init.
func resolveGHToken(repo string, out io.Writer) string {
	return resolveGHTokenFor("openbao", repo, out)
}

// finalizeTokenExec is the provider CLI seam for resolveGHTokenFor.
var finalizeTokenExec tokenExecFn = defaultTokenExec

// resolveGHTokenFor sources the push token for a Fleet commit made by a
// finalize command. It looks the token up for the origin's own host: an SSH
// remote needs none, a GitHub host uses gh, and any other host uses glab's
// stored credential for that host. A GitHub token is never sent elsewhere.
func resolveGHTokenFor(component, repo string, out io.Writer) string {
	if fleetRemoteIsSSH(repo) {
		fmt.Fprintf(out, "[%s] Fleet remote uses SSH; no token needed\n", component)
		return ""
	}
	host := fleetRemoteHost(repo)
	if host == "" || host == "github.com" {
		return resolveProviderToken(component, clusterinit.ProviderGitHub, "github.com", out, finalizeTokenExec)
	}
	// Another host: GitLab or GitHub Enterprise. Only what glab or gh stored
	// for that exact host is used, never an environment token.
	return resolveStoredHostToken(component, host, out, finalizeTokenExec)
}
