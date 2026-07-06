package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/fleetconfig"
)

// bootstrapConfigCmd registers `kube-dc bootstrap config` (V3) — the
// day-2 editor for a cluster's clusters/<name>/cluster-config.env:
// list / get values, and set (validated, with a diff + git commit/push)
// instead of hand-editing the fleet repo.
func bootstrapConfigCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read/edit a cluster's cluster-config.env (day-2)",
		Long: `Day-2 editor for clusters/<cluster>/cluster-config.env — the per-cluster
knobs (image tags, component versions, CIDRs, feature flags) that Flux
renders into the cluster-config ConfigMap.

  config list <cluster>              show every key = value
  config get  <cluster> <KEY>        print one value
  config set  <cluster> KEY=VAL ...  change values (diff → commit → push)

'set' validates, prints a diff, and — with --yes — commits to the fleet
repo and pushes (Flux then reconciles). Without --yes it only previews.
Only keys already present are settable unless --add. Requires a clean
fleet working tree (like 'bootstrap init').`,
	}
	cmd.AddCommand(bootstrapConfigListCmd(fleetRepo))
	cmd.AddCommand(bootstrapConfigGetCmd(fleetRepo))
	cmd.AddCommand(bootstrapConfigSetCmd(fleetRepo))
	return cmd
}

// loadClusterEnv resolves the fleet repo + the named cluster's
// cluster-config.env and loads it. Returns the repo root too (the git
// transaction operates on the repo, not the env file).
func loadClusterEnv(fleetRepoFlag, name string) (env *config.Env, repoRoot string, err error) {
	repoRoot, err = resolveFleetRepo(fleetRepoFlag)
	if err != nil {
		return nil, "", err
	}
	clusters, err := discover.ListClusters(repoRoot)
	if err != nil {
		return nil, "", fmt.Errorf("list clusters in %s: %w", repoRoot, err)
	}
	for _, c := range clusters {
		if c.Name == name {
			e, lerr := config.LoadEnv(c.EnvPath)
			if lerr != nil {
				return nil, "", fmt.Errorf("load %s: %w", c.EnvPath, lerr)
			}
			return e, repoRoot, nil
		}
	}
	names := make([]string, 0, len(clusters))
	for _, c := range clusters {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return nil, "", fmt.Errorf("cluster %q not found in %s (known: %s)", name, repoRoot, strings.Join(names, ", "))
}

func bootstrapConfigListCmd(fleetRepo *string) *cobra.Command {
	return &cobra.Command{
		Use:           "list <cluster>",
		Short:         "List every key=value in the cluster's cluster-config.env",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, err := loadClusterEnv(*fleetRepo, args[0])
			if err != nil {
				return fmt.Errorf("bootstrap config list: %w", err)
			}
			out := cmd.OutOrStdout()
			for _, k := range env.Keys() {
				v, _ := env.Get(k)
				fmt.Fprintf(out, "%s=%s\n", k, fleetconfig.StripInlineComment(v))
			}
			return nil
		},
	}
}

func bootstrapConfigGetCmd(fleetRepo *string) *cobra.Command {
	return &cobra.Command{
		Use:           "get <cluster> <KEY>",
		Short:         "Print one value from the cluster's cluster-config.env",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, err := loadClusterEnv(*fleetRepo, args[0])
			if err != nil {
				return fmt.Errorf("bootstrap config get: %w", err)
			}
			v, ok := env.Get(args[1])
			if !ok {
				return fmt.Errorf("bootstrap config get: key %q not set", args[1])
			}
			fmt.Fprintln(cmd.OutOrStdout(), fleetconfig.StripInlineComment(v))
			return nil
		},
	}
}

func bootstrapConfigSetCmd(fleetRepo *string) *cobra.Command {
	var (
		add         bool
		dryRun      bool
		noPush      bool
		yes         bool
		githubToken string
		provider    string
	)
	cmd := &cobra.Command{
		Use:           "set <cluster> KEY=VALUE [KEY=VALUE ...]",
		Short:         "Change cluster-config.env values (diff → commit → push)",
		Args:          cobra.MinimumNArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Sets one or more keys in clusters/<cluster>/cluster-config.env, prints a
diff, and — with --yes — commits to the fleet repo and pushes so Flux
reconciles. Without --yes it previews the diff and stops. Only keys that
already exist are settable unless --add. Requires a clean fleet working
tree; on a commit/push failure the fleet repo is reset to its prior HEAD.`,
		Example: `  # Preview a manager image bump
  kube-dc bootstrap config set cloud KUBE_DC_MANAGER_TAG=v0.3.90

  # Apply it (commit + push; Flux reconciles)
  kube-dc bootstrap config set cloud KUBE_DC_MANAGER_TAG=v0.3.90 --yes

  # Commit locally without pushing
  kube-dc bootstrap config set cloud PROM_RETENTION=30d --yes --no-push`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName, assignments := args[0], args[1:]
			out := cmd.OutOrStdout()

			switch provider {
			case "", string(clusterinit.ProviderGitHub), string(clusterinit.ProviderGitLab):
			default:
				return fmt.Errorf("bootstrap config set: --provider must be github or gitlab (got %q)", provider)
			}

			sets, err := fleetconfig.ParseAssignments(assignments)
			if err != nil {
				return fmt.Errorf("bootstrap config set: %w", err)
			}
			env, repoRoot, err := loadClusterEnv(*fleetRepo, clusterName)
			if err != nil {
				return fmt.Errorf("bootstrap config set: %w", err)
			}
			changes, err := fleetconfig.Plan(env, sets, add)
			if err != nil {
				return fmt.Errorf("bootstrap config set: %w", err)
			}

			renderConfigPlan(out, clusterName, changes)
			if !fleetconfig.HasEffective(changes) {
				fmt.Fprintln(out, "[config] nothing to change — all values already set.")
				return nil
			}
			if dryRun || !yes {
				fmt.Fprintln(out, "\n[config] preview — re-run with --yes to commit"+pushSuffix(noPush)+".")
				return nil
			}

			return applyConfigSet(cmd, out, repoRoot, env, changes, clusterName, noPush, githubToken, provider)
		},
	}
	cmd.Flags().BoolVar(&add, "add", false, "Allow creating keys that don't already exist (default: only update existing keys)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the diff; change nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply: write + commit (+ push unless --no-push). Without this, set only previews")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "Commit to the fleet repo but do not push to the remote")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "Token for the push (default: `gh auth token`, or `glab auth token` with --provider gitlab)")
	cmd.Flags().StringVar(&provider, "provider", "", "Git host for the push credential: github (default) or gitlab")
	return cmd
}

// applyConfigSet runs the write + git transaction, mirroring
// clusterinit.Apply: snapshot HEAD, refuse a dirty tree, write, commit
// (or commit+push), and reset to the prior HEAD on any failure so a
// half-applied change never lingers in the fleet repo.
func applyConfigSet(cmd *cobra.Command, out io.Writer, repoRoot string, env *config.Env, changes []fleetconfig.Change, clusterName string, noPush bool, githubToken, provider string) error {
	ctx := cmd.Context()
	// config only needs the Git port. NewSession builds k8s FIRST and
	// returns ErrRealAdaptersNotReady before wiring Git, so a git-only
	// day-2 command must use NewGitOnly — otherwise `config set` fails
	// with "git adapter unavailable" whenever there's no kubeconfig.
	git, err := bootstrap.NewGitOnly()
	if err != nil {
		return fmt.Errorf("bootstrap config set: build git adapter: %w", err)
	}

	preSHA, err := git.Head(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("bootstrap config set: read fleet HEAD: %w", err)
	}
	diff, err := git.Diff(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("bootstrap config set: check fleet working tree: %w", err)
	}
	if len(diff.Files) > 0 {
		return fmt.Errorf("bootstrap config set: fleet working tree is dirty (%d changed file(s)); commit or stash first", len(diff.Files))
	}

	// Write the env change.
	for _, c := range changes {
		env.Set(c.Key, c.New)
	}
	if err := env.Write(""); err != nil {
		return fmt.Errorf("bootstrap config set: write cluster-config.env: %w", err)
	}

	msg := configCommitMessage(clusterName, changes)
	var sha string
	if noPush {
		sha, err = git.Commit(ctx, repoRoot, msg)
	} else {
		token := resolveGitHubToken(&clusterinit.InitOptions{
			GitHubToken: githubToken,
			Provider:    clusterinit.Provider(provider),
		}, out)
		sha, err = git.CommitAndPush(ctx, repoRoot, msg, token)
	}
	if err != nil {
		if rerr := git.ResetHard(ctx, repoRoot, preSHA); rerr != nil {
			return fmt.Errorf("bootstrap config set: commit failed (%v) AND rollback to %s failed (%v) — inspect the fleet repo", err, shortSHA(preSHA), rerr)
		}
		return fmt.Errorf("bootstrap config set: commit/push failed, rolled back to %s: %w", shortSHA(preSHA), err)
	}

	if noPush {
		fmt.Fprintf(out, "[config] committed %s locally (not pushed — run `git -C %s push`, then Flux reconciles)\n", shortSHA(sha), repoRoot)
	} else {
		fmt.Fprintf(out, "[config] committed + pushed %s — Flux will reconcile the change\n", shortSHA(sha))
	}
	return nil
}

// renderConfigPlan prints the per-key diff the operator reviews.
func renderConfigPlan(out io.Writer, clusterName string, changes []fleetconfig.Change) {
	fmt.Fprintf(out, "== config set — %s ==\n", clusterName)
	for _, c := range changes {
		switch {
		case c.Added:
			fmt.Fprintf(out, "  + %s = %s   (new key)\n", c.Key, c.New)
		case c.NoOp():
			fmt.Fprintf(out, "    %s = %s   (unchanged)\n", c.Key, c.New)
		default:
			fmt.Fprintf(out, "  ~ %s: %s → %s\n", c.Key, c.Old, c.New)
		}
	}
}

func configCommitMessage(clusterName string, changes []fleetconfig.Change) string {
	keys := make([]string, 0, len(changes))
	for _, c := range changes {
		if !c.NoOp() {
			keys = append(keys, c.Key)
		}
	}
	sort.Strings(keys)
	return fmt.Sprintf("config(%s): set %s", clusterName, strings.Join(keys, ", "))
}

func pushSuffix(noPush bool) string {
	if noPush {
		return " (commit only)"
	}
	return " + push"
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
