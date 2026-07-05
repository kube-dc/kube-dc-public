// `kube-dc orgs` — org-admin elevation flow CLI surface (M1-T06 + T14).
// Backend contract lives in ui/backend/controllers/elevationModule.js.
// All four verbs require org-admin in the JWT; the CLI doesn't try to
// guess the user's groups — it just forwards and surfaces the backend's
// 403 with the original error message when the gate trips.
//
// Verbs:
//   elevate    <project> --reason "..."  → POST   /elevate
//   release    <project>                 → DELETE /elevate
//   status     <project>                 → GET    /elevate
//   elevations                            → GET    /elevations  (org-wide)

package main

import (
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
	"github.com/spf13/cobra"
)

func orgsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orgs",
		Short: "Manage organization-level controls (elevation, audit)",
		Long: `Organization-scoped controls reserved to org-admins. Use these
when an admin needs short-lived cross-project access (15-minute
elevation window) so secret-value reads are explicitly logged with a
reason instead of being silently picked up by routine admin
permissions.`,
	}
	cmd.AddCommand(orgsElevateCmd())
	cmd.AddCommand(orgsReleaseCmd())
	cmd.AddCommand(orgsStatusCmd())
	cmd.AddCommand(orgsElevationsCmd())
	return cmd
}

// orgFromContextOrFlag returns the org name to use. Order:
//  1. --org flag if non-empty.
//  2. realm parsed out of the current kubeconfig context name
//     (kube-dc/<domain>/<org>/<project> → org). Returns an error
//     for an admin context (kube-dc/<domain>/admin) because org-admin
//     elevation is meaningless from the realm-master persona.
func orgFromContextOrFlag(flagOrg string) (string, error) {
	if flagOrg != "" {
		return flagOrg, nil
	}
	realm := readCurrentRealm()
	switch realm {
	case "":
		return "", fmt.Errorf("could not determine org from current context — pass --org")
	case "master":
		return "", fmt.Errorf("admin context has no org; pass --org <org>")
	default:
		return realm, nil
	}
}

// readCurrentRealm parses the realm out of the kubeconfig's current
// context name without doing a token refresh. Returns "" if the
// context isn't a kube-dc one (the caller falls back to --org).
//
// Org-detection is purely a CLI ergonomic — the backend's cross-org
// guard is the authority. If the parsed realm is wrong, the request
// 403s with a clear error; we don't try to second-guess.
func readCurrentRealm() string {
	name, _ := readKubeconfigContextName()
	return realmFromContext(name)
}

// readKubeconfigContextName returns the current-context name from the
// active kubeconfig via the same loader resolveScope() uses. Reusing
// the typed loader (rather than the hand-rolled YAML parse it
// replaced — T14 review P3) keeps us correct against multi-document
// configs, quoted values, and other formatter quirks.
func readKubeconfigContextName() (string, error) {
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		return "", err
	}
	cfg, err := mgr.Load()
	if err != nil {
		return "", err
	}
	return cfg.CurrentContext, nil
}

// withBackend resolves the scope (token + domain) and returns an
// initialised backend client. Errors are propagated unchanged.
func withBackend() (*backend.Client, *secretsScope, error) {
	scope, err := resolveScope("")
	if err != nil {
		return nil, nil, err
	}
	cli, err := scope.backend()
	if err != nil {
		return nil, nil, err
	}
	return cli, scope, nil
}

// printGrant renders a Grant in table mode (the only mode for orgs
// commands — these are operator interactions, not pipeable data).
func printGrant(g *backend.Grant) {
	fmt.Printf("Elevation %s\n", g.ElevationID)
	fmt.Printf("  User:       %s\n", g.User)
	fmt.Printf("  Scope:      %s/%s\n", g.Org, g.Project)
	fmt.Printf("  Reason:     %s\n", g.Reason)
	fmt.Printf("  Granted at: %s\n", g.GrantedAt)
	fmt.Printf("  Expires at: %s (%ds remaining)\n", g.ExpiresAt, g.TTLSeconds)
}

// orgsElevateCmd implements `kube-dc orgs elevate <project> --reason "..."`.
func orgsElevateCmd() *cobra.Command {
	var org, reason string
	cmd := &cobra.Command{
		Use:   "elevate <project>",
		Short: "Open a 15-minute elevation window for secret-value reads in a project (org-admin only).",
		Long: `Open a 15-minute elevation window. Every secret-value read during
the window carries the elevation_id in the audit trail, so an auditor
can correlate sensitive reads back to the reason you provided.

The window cannot be extended; calling elevate again replaces the
current grant with a new one (and a fresh audit event). Use
"kube-dc orgs release" to end early.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			o, err := orgFromContextOrFlag(org)
			if err != nil {
				return err
			}
			cli, _, err := withBackend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			g, err := cli.Elevate(ctx, o, project, reason)
			if err != nil {
				return err
			}
			printGrant(g)
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: current context's org)")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for elevation (required, audited)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// orgsReleaseCmd implements `kube-dc orgs release <project>`.
func orgsReleaseCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "release <project>",
		Short: "End the elevation window early (org-admin only).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			o, err := orgFromContextOrFlag(org)
			if err != nil {
				return err
			}
			cli, _, err := withBackend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			g, err := cli.ReleaseElevation(ctx, o, project)
			if err != nil {
				return err
			}
			fmt.Printf("Released elevation %s at %s\n", g.ElevationID, g.ReleasedAt)
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: current context's org)")
	return cmd
}

// orgsStatusCmd implements `kube-dc orgs status <project>`.
func orgsStatusCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "status <project>",
		Short: "Show the current elevation state for a project (org-admin only).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			o, err := orgFromContextOrFlag(org)
			if err != nil {
				return err
			}
			cli, _, err := withBackend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			st, err := cli.GetElevationStatus(ctx, o, project)
			if err != nil {
				return err
			}
			if !st.Active {
				fmt.Printf("No active elevation for %s/%s\n", o, project)
				return nil
			}
			printGrant(&st.Grant)
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: current context's org)")
	return cmd
}

// orgsElevationsCmd implements `kube-dc orgs elevations`.
func orgsElevationsCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "elevations",
		Short: "List all active elevations in the org (org-admin only).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := orgFromContextOrFlag(org)
			if err != nil {
				return err
			}
			cli, _, err := withBackend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := cli.ListElevations(ctx, o)
			if err != nil {
				return err
			}
			if len(list.Items) == 0 {
				fmt.Printf("No active elevations in %s\n", o)
				return nil
			}
			fmt.Printf("%-15s  %-20s  %-12s  %-25s  %s\n", "ELEVATION", "USER", "PROJECT", "EXPIRES", "REASON")
			for _, g := range list.Items {
				fmt.Printf("%-15s  %-20s  %-12s  %-25s  %s\n",
					truncCLI(g.ElevationID, 15),
					truncCLI(g.User, 20),
					truncCLI(g.Project, 12),
					g.ExpiresAt,
					truncCLI(g.Reason, 60),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: current context's org)")
	return cmd
}
