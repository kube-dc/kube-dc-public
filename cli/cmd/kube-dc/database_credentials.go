// `kube-dc db credentials` subcommand — M4-T04 CLI for the Database
// Credentials Manager. Mirrors `kube-dc kms` + `kube-dc certificates`:
// scope resolution + JWT via the existing kubeconfig context, all
// calls go through the backend at backend.<domain>/api/database-
// credentials/* so the audit + per-role OpenBao policy gates there
// are exercised for CLI users too.
//
// Surface (UX PRD §5.5):
//
//   kube-dc db credentials list
//   kube-dc db credentials describe <name>            (alias: show)
//   kube-dc db credentials create <name> --database <db> [--mode static-rotated|dynamic] ...
//   kube-dc db credentials rotate <name> [--root]
//   kube-dc db credentials get <name> [--show-password] [-o table|json|yaml|env]
//   kube-dc db credentials delete <name> --yes
//   kube-dc db credentials issue <name> [-o table|json|yaml|env]
//
// `-o env` emits `KUBE_DC_DB_*=value` lines suitable for shell
// `eval "$(...)"` sourcing. For `get`, requires --show-password
// (a masked env is useless for eval). For `issue`, includes the
// lease ID / duration so the consumer can drive renew/revoke.
//
// Phase-1 (M4): dynamic mode (--mode dynamic + `issue`) is the
// CLI/API-only surface — UI ships static-rotated only per dev-scope
// §13. The controller currently returns 501/DynamicModeDeferred on
// /issue; the CLI command is wired so the surface doesn't shift when
// the controller catches up.

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/spf13/cobra"
)

// dbCmd is the parent. Sub-tree:
//
//	db
//	  credentials
//	    list / describe / create / rotate / get / delete / issue
//
// Kept under `db` (not a top-level alias) so future `kube-dc db
// instances` / `kube-dc db backups` verbs have a home.
func dbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Database operations (credentials, backups — credentials in M4)",
		Long: `Database operations against KdcDatabase + DatabaseCredentialPolicy.
Phase 1 covers credentials (rotation, reveal, dynamic issue); backups
are reachable today via kubectl edit kdcdatabase. A future CLI verb
will likely wrap that flow once the UX is stable.

Permissions follow your project role (cap matrix in dev-scope §6.1):
  user/viewer        list + describe only
  developer          + create / delete; can issue dynamic leases
  project-manager    + rotate + read static password
  project-admin      + rotate-root (rotates the engine's privileged user)`,
	}
	cmd.AddCommand(dbCredentialsCmd())
	return cmd
}

func dbCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "DatabaseCredentialPolicy lifecycle (list/create/rotate/get/delete/issue)",
	}
	cmd.AddCommand(dbCredentialsListCmd())
	cmd.AddCommand(dbCredentialsDescribeCmd())
	cmd.AddCommand(dbCredentialsCreateCmd())
	cmd.AddCommand(dbCredentialsRotateCmd())
	cmd.AddCommand(dbCredentialsGetCmd())
	cmd.AddCommand(dbCredentialsDeleteCmd())
	cmd.AddCommand(dbCredentialsIssueCmd())
	return cmd
}

// -------- list -----------------------------------------------------

func dbCredentialsListCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DatabaseCredentialPolicies in the project namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := cli.ListDBCredentialPolicies(ctx, scope.Namespace)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			return printDBCPTable(list.Items)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- describe / get -------------------------------------------

func dbCredentialsDescribeCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:     "describe <name>",
		Aliases: []string{"show"},
		Short:   "Show one DatabaseCredentialPolicy + status mirror",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			p, err := cli.GetDBCredentialPolicy(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, p)
			}
			return printDBCPDetail(p)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- create ----------------------------------------------------

func dbCredentialsCreateCmd() *cobra.Command {
	var (
		namespace, database, mode, username, rotation, strategy string
		role, ttl, maxTTL, syncSecret                           string
		syncDisabled                                            bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new DatabaseCredentialPolicy",
		Example: `  # 30-day static rotation, project Secret auto-synced:
  kube-dc db credentials create docs-pg-app \
    --database docs-pg --mode static-rotated --rotate 30d

  # Static rotation, custom username + target Secret:
  kube-dc db credentials create reporting-creds \
    --database docs-pg --username reporting \
    --rotate 7d --sync-secret reporting-db-creds

  # Dynamic credentials (lease-based; phase-1 controller returns
  # DynamicModeDeferred — the CR is created but ready=False until
  # the dynamic surface ships):
  kube-dc db credentials create docs-pg-readonly \
    --database docs-pg --mode dynamic --role readonly \
    --ttl 1h --max-ttl 24h`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if database == "" {
				return fmt.Errorf("--database is required")
			}
			if mode == "" {
				mode = "static-rotated"
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			spec := backend.CreateDBCredentialPolicySpec{
				DatabaseRef: backend.DatabaseRef{Name: database},
				Mode:        mode,
				Username:    username,
				Role:        role,
				TTL:         ttl,
				MaxTTL:      maxTTL,
			}
			if rotation != "" || strategy != "" {
				spec.Rotation = backend.DBCredentialRotation{Interval: rotation, Strategy: strategy}
			}
			// Sync block is omitted when user neither set --sync-secret
			// nor --no-sync — webhook defaults sync.enabled=true for
			// static-rotated and falls back targetSecretName=<name>.
			if syncDisabled || syncSecret != "" {
				disabled := syncDisabled
				enabled := !disabled
				spec.Sync = backend.DBCredentialSync{
					Enabled:          &enabled,
					TargetSecretName: syncSecret,
				}
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			if err := cli.CreateDBCredentialPolicy(ctx, scope.Namespace, name, backend.CreateDBCredentialPolicyOptions{Spec: spec}); err != nil {
				return err
			}
			fmt.Printf("DatabaseCredentialPolicy %s/%s created (mode=%s, database=%s)\n",
				scope.Namespace, name, mode, database)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&database, "database", "", "KdcDatabase name in the same project (required)")
	cmd.Flags().StringVar(&mode, "mode", "static-rotated", "Credential mode: static-rotated|dynamic")
	cmd.Flags().StringVar(&username, "username", "", "DB user to manage (default: app — must already exist on the engine)")
	cmd.Flags().StringVar(&rotation, "rotate", "", "Rotation interval (e.g. 30d, 12h). Default: 30d when omitted.")
	cmd.Flags().StringVar(&strategy, "rotate-strategy", "", "Rotation strategy: rolling|immediate (default: rolling)")
	cmd.Flags().StringVar(&role, "role", "", "OpenBao DB role for dynamic mode (required when --mode=dynamic)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Lease TTL for dynamic mode (e.g. 1h)")
	cmd.Flags().StringVar(&maxTTL, "max-ttl", "", "Maximum lease TTL incl. renewals (e.g. 24h)")
	cmd.Flags().StringVar(&syncSecret, "sync-secret", "", "Project Secret name to receive the rotated credentials (default: <name>)")
	cmd.Flags().BoolVar(&syncDisabled, "no-sync", false, "Disable automatic project Secret sync (rotated creds only via `db credentials get`)")
	return cmd
}

// -------- rotate ---------------------------------------------------

func dbCredentialsRotateCmd() *cobra.Command {
	var namespace string
	var rotateRoot bool
	cmd := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Rotate the credential's password (forces OpenBao to mint a new one)",
		Long: `Force-rotates the rotated user's password. The new password lands in
the target K8s Secret on the next reconciler tick (~5min) — the
backend nudges the controller via an annotation, so the wait is
usually a few seconds.

--root reassigns the engine's privileged user (the one db-manager
itself uses to drive rotations). Project-admin only; destructive
enough that you should be sure before running it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			res, err := cli.RotateDBCredentialPolicy(ctx, scope.Namespace, name, rotateRoot)
			if err != nil {
				return err
			}
			target := res.Target
			if target == "" {
				target = name
			}
			if rotateRoot {
				fmt.Printf("Rotated engine root for %s/%s (config %q)\n", scope.Namespace, name, target)
			} else {
				fmt.Printf("Rotated %s/%s (role %q, reconcile triggered: %v)\n",
					scope.Namespace, name, target, res.ReconcileTriggered)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().BoolVar(&rotateRoot, "root", false, "Rotate the engine's privileged user (project-admin only; destructive)")
	return cmd
}

// -------- get (read static password) -------------------------------

func dbCredentialsGetCmd() *cobra.Command {
	var namespace, outFlag string
	var showPassword bool
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Read the current rotated credentials for a static-rotated policy",
		Long: `Fetches the current username + password OpenBao has set for the
rotated user. Audited on the backend — every fetch records a
database-credentials.credentials.read event.

The password is NOT printed unless --show-password is passed.
Without it the output shows username + a masked placeholder so the
common "did I configure this right?" check doesn't leak the value
into terminal scrollback.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			envOut, out, err := parseDBOutput(outFlag)
			if err != nil {
				return err
			}
			// `-o env` only makes sense WITH the actual password — the
			// whole point of the format is to source it into a shell
			// (`eval "$(... -o env)"`), and a masked value is useless
			// there. Refuse the contradiction explicitly so the user
			// gets a clear message instead of a confusing masked env.
			if envOut && !showPassword {
				return fmt.Errorf("-o env requires --show-password (env output is meant for `eval` / shell-sourcing)")
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			creds, err := cli.GetDBCredentials(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if !showPassword {
				creds.Password = strings.Repeat("*", 8)
			}
			if envOut {
				return printDBCredentialsEnv(creds)
			}
			if out != outTable {
				return printSerialized(out, creds)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			defer w.Flush()
			fmt.Fprintf(w, "Username:\t%s\n", creds.Username)
			fmt.Fprintf(w, "Password:\t%s\n", creds.Password)
			if creds.LastVaultRotation != "" {
				fmt.Fprintf(w, "Last Rotation:\t%s\n", creds.LastVaultRotation)
			}
			if creds.RotationPeriod > 0 {
				fmt.Fprintf(w, "Rotation Period:\t%d s\n", creds.RotationPeriod)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml|env (env requires --show-password)")
	cmd.Flags().BoolVar(&showPassword, "show-password", false, "Print the actual password (default: masked)")
	return cmd
}

// -------- delete ---------------------------------------------------

func dbCredentialsDeleteCmd() *cobra.Command {
	var namespace string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a DatabaseCredentialPolicy",
		Long: `Removes the DBCP CR. The reconciler's finalizer revokes the OpenBao
static-role + syncs the engine's per-user Secret to OpenBao's
last-known password (so the next DBCP on the same KdcDatabase doesn't
trip the SQLSTATE 28P01 trap).

Requires --yes to proceed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				return fmt.Errorf("refusing to delete without --yes")
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			if err := cli.DeleteDBCredentialPolicy(ctx, scope.Namespace, name); err != nil {
				return err
			}
			fmt.Printf("DatabaseCredentialPolicy %s/%s deleted\n", scope.Namespace, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm deletion")
	return cmd
}

// -------- issue (dynamic mode) -------------------------------------

func dbCredentialsIssueCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "issue <name>",
		Short: "Issue a short-lived dynamic credential lease (mode=dynamic only)",
		Long: `Mints a fresh username + password from OpenBao's dynamic role
machinery and returns the lease ID + TTL. Static-rotated policies
return 400 (use 'kube-dc db credentials get' instead).

NOTE: Phase-1 the controller returns 501/DynamicModeDeferred because
the dynamic-role plumbing isn't wired yet. The CLI surface is
shipped now so it doesn't shift when the controller catches up. The
backend will surface a clear "dynamic credential issuance is not yet
implemented" error in the meantime.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			envOut, out, err := parseDBOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			lease, err := cli.IssueDBCredentials(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if envOut {
				return printDBLeaseEnv(lease)
			}
			if out != outTable {
				return printSerialized(out, lease)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			defer w.Flush()
			fmt.Fprintf(w, "Username:\t%s\n", lease.Username)
			fmt.Fprintf(w, "Password:\t%s\n", lease.Password)
			if lease.LeaseId != "" {
				fmt.Fprintf(w, "Lease ID:\t%s\n", lease.LeaseId)
			}
			if lease.LeaseDuration > 0 {
				fmt.Fprintf(w, "Lease Duration:\t%d s\n", lease.LeaseDuration)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml|env (env emits `KEY=value` for shell `eval`)")
	return cmd
}

// -------- table renderers ------------------------------------------

func printDBCPTable(items []backend.DBCredentialPolicySummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tDATABASE\tMODE\tUSER\tROTATION\tTARGET-SECRET\tREADY")
	for _, p := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Name,
			fmtCoalesce(p.DatabaseRef, "-"),
			fmtCoalesce(p.Mode, "static-rotated"),
			fmtCoalesce(p.Username, "app"),
			dbcpRotationDescription(p.Rotation, p.Mode),
			dbcpSyncDescription(p.Sync, p.Status.TargetSecretName),
			kmsReadyFromConditions(p.Status.Conditions),
		)
	}
	return nil
}

func printDBCPDetail(p *backend.DBCredentialPolicySummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "Name:\t%s/%s\n", p.Namespace, p.Name)
	fmt.Fprintf(w, "Database:\t%s\n", fmtCoalesce(p.DatabaseRef, "-"))
	fmt.Fprintf(w, "Mode:\t%s\n", fmtCoalesce(p.Mode, "static-rotated"))
	fmt.Fprintf(w, "Username:\t%s\n", fmtCoalesce(p.Username, "app"))
	fmt.Fprintf(w, "Rotation:\t%s\n", dbcpRotationDescription(p.Rotation, p.Mode))
	fmt.Fprintf(w, "Sync:\t%s\n", dbcpSyncDescription(p.Sync, p.Status.TargetSecretName))
	if p.Role != "" {
		fmt.Fprintf(w, "Dynamic Role:\t%s\n", p.Role)
	}
	if p.TTL != "" {
		fmt.Fprintf(w, "TTL:\t%s\n", p.TTL)
	}
	if p.MaxTTL != "" {
		fmt.Fprintf(w, "Max TTL:\t%s\n", p.MaxTTL)
	}
	fmt.Fprintf(w, "Created:\t%s\n", fmtCoalesce(p.CreationTimestamp, "-"))
	if p.Status.Endpoint != "" {
		fmt.Fprintf(w, "Endpoint:\t%s\n", p.Status.Endpoint)
	}
	if p.Status.LastRotatedTime != "" {
		fmt.Fprintf(w, "Last Rotated:\t%s\n", p.Status.LastRotatedTime)
	}
	if p.Status.NextRotationTime != "" {
		fmt.Fprintf(w, "Next Rotation:\t%s\n", p.Status.NextRotationTime)
	}
	fmt.Fprintf(w, "Lease Supported:\t%v\n", p.Status.LeaseSupported)
	fmt.Fprintf(w, "Ready:\t%s\n", kmsReadyFromConditions(p.Status.Conditions))
	return nil
}

// dbcpRotationDescription mirrors rotationDescription in kms.go but
// handles the dynamic mode case (rotation interval is not meaningful
// — show the role + TTL signal instead).
func dbcpRotationDescription(r backend.DBCredentialRotation, mode string) string {
	if mode == "dynamic" {
		return "n/a (dynamic)"
	}
	if r.Interval == "" {
		return "30d (default)"
	}
	if r.Strategy != "" && r.Strategy != "rolling" {
		return fmt.Sprintf("%s (%s)", r.Interval, r.Strategy)
	}
	return r.Interval
}

// parseDBOutput extends the shared parseOutput with an `env` value
// used by `get` and `issue` to emit shell-sourceable `KEY=value`
// lines. Returns (envOut, fallback, err). `env` is opt-in per
// subcommand — only the credential-emitting commands accept it; the
// other subcommands keep the shared table|json|yaml surface.
func parseDBOutput(s string) (bool, outputFormat, error) {
	if strings.EqualFold(s, "env") {
		return true, "", nil
	}
	f, err := parseOutput(s)
	return false, f, err
}

// printDBCredentialsEnv emits the static-creds payload as
// shell-sourceable `KEY=value` lines on stdout. The variable names
// match what the database/static-creds payload encodes so
// `eval "$(kube-dc db credentials get foo -o env --show-password)"`
// hands the consumer a complete credential pair plus rotation metadata.
//
// Values are NOT shell-escaped — passwords containing single quotes,
// `$`, or backticks would break a naive `eval`. Documented limitation:
// the typical OpenBao-generated password charset (alnum + safe
// symbols) is `eval`-safe; if the operator overrides via
// `rotation_statements` to a charset that includes shell-special
// chars, prefer `-o json` and parse with `jq`.
func printDBCredentialsEnv(creds *backend.DBCredentials) error {
	fmt.Printf("KUBE_DC_DB_USERNAME=%s\n", creds.Username)
	fmt.Printf("KUBE_DC_DB_PASSWORD=%s\n", creds.Password)
	if creds.LastVaultRotation != "" {
		fmt.Printf("KUBE_DC_DB_LAST_ROTATION=%s\n", creds.LastVaultRotation)
	}
	if creds.RotationPeriod > 0 {
		fmt.Printf("KUBE_DC_DB_ROTATION_PERIOD=%d\n", creds.RotationPeriod)
	}
	return nil
}

// printDBLeaseEnv is the dynamic-mode equivalent of
// printDBCredentialsEnv — emits the lease payload as KEY=value lines.
// Lease metadata included so the consumer can implement renew/revoke
// against the lease ID. Same shell-escape caveat as the static helper.
func printDBLeaseEnv(lease *backend.DBLease) error {
	fmt.Printf("KUBE_DC_DB_USERNAME=%s\n", lease.Username)
	fmt.Printf("KUBE_DC_DB_PASSWORD=%s\n", lease.Password)
	if lease.LeaseId != "" {
		fmt.Printf("KUBE_DC_DB_LEASE_ID=%s\n", lease.LeaseId)
	}
	if lease.LeaseDuration > 0 {
		fmt.Printf("KUBE_DC_DB_LEASE_DURATION=%d\n", lease.LeaseDuration)
	}
	return nil
}

// dbcpSyncDescription folds the sync.enabled + targetSecretName +
// status.targetSecretName triple into a single table cell.
//
// Priority: status.targetSecretName (what's actually projected) →
// spec.sync.targetSecretName → "disabled" / "-".
func dbcpSyncDescription(s backend.DBCredentialSync, statusTarget string) string {
	if s.Enabled != nil && !*s.Enabled {
		return "disabled"
	}
	if statusTarget != "" {
		return statusTarget
	}
	if s.TargetSecretName != "" {
		return s.TargetSecretName
	}
	return "-"
}
