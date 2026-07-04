// `kube-dc audit list [--org] [--csv]` — read the structured audit
// stream the backend emits to Loki (M1-T05 + T05b + T14). Two
// resolution modes:
//
//   - Project mode (default): scope to the current context's
//     <org>/<project>. Any project member can query their own
//     audit stream.
//   - Org mode (--org): scope to org-wide events. Org-admin only —
//     non-admin tokens get a 403 at the backend.
//
// `--csv` switches to the streamed CSV export and writes either to
// stdout or to `--output-file <path>`. CSV is org-wide only at the
// backend, so `--csv` implies `--org` and `--csv --project` is
// rejected up-front rather than silently ignored (T14 review P2).
//
// Output formats mirror `kube-dc secrets`:
//   - table (default): one row per event with TS / service / action /
//                       result / resource / actor / elevation_id.
//   - json:           the raw backend JSON ({events, query, returned}).
//   - yaml:           same as json but YAML-encoded.

package main

import (
	"fmt"
	"os"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/spf13/cobra"
)

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Query the structured audit trail (Loki-backed).",
		Long: `Read the kube-dc audit stream emitted by the backend on every
/api/secrets/* call (and surrounding ops like org-admin elevation).

Project-scoped queries are open to any project member; org-wide
queries (--org alone) and CSV exports (--csv, implies --org) are
org-admin only.`,
	}
	cmd.AddCommand(auditListCmd())
	return cmd
}

func auditListCmd() *cobra.Command {
	var orgFlag, project, service, actor, result, since, until, outFlag, csvPath string
	var limit int
	var orgWide, csvMode bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Query audit events (project-scoped by default; --org for org-wide).",
		Long: `Examples:

  # Last hour of secret reads in this project:
  kube-dc audit list --service secrets --since 1h

  # Cross-project audit for an org-admin reviewing an incident:
  kube-dc audit list --org --since 2026-05-21T13:00:00Z

  # Drop the org-wide stream to a CSV file (org-admin only):
  kube-dc audit list --csv --output-file incident-2026-05-21.csv

Flag semantics:
  -o / --output:  table | json | yaml — only used WITHOUT --csv.
  --output-file:  destination path WITH --csv (omit for stdout).
  --project:      ignored with --csv (CSV is org-wide only); the
                  CLI rejects --csv --project so the mismatch is
                  caught at flag-parse time instead of silently
                  scoping to the org.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// T14 review P1/P2: validate the flag combos up-front so
			// `--csv --project` doesn't get silently coerced into the
			// org-wide endpoint and `--csv -o json` doesn't try to
			// parse a non-CSV format that the CSV path ignores.
			if csvMode {
				if project != "" {
					return fmt.Errorf("--csv exports org-wide events; --project is incompatible (omit it or drop --csv)")
				}
				if cmd.Flags().Changed("output") {
					return fmt.Errorf("--csv emits CSV regardless of -o/--output; use --output-file <path> to write to a file (or omit it for stdout)")
				}
			}
			// Format flag is only meaningful in non-CSV mode; parse
			// AFTER the csvMode gate so a typo on --output doesn't
			// affect CSV runs.
			var out outputFormat
			if !csvMode {
				var err error
				out, err = parseOutput(outFlag)
				if err != nil {
					return err
				}
			}
			cli, scope, err := withBackend()
			if err != nil {
				return err
			}
			// Resolve org: --org wins, else current context's realm.
			org, err := orgFromContextOrFlag(orgFlag)
			if err != nil {
				return err
			}

			q := backend.AuditQuery{
				Service: service,
				Actor:   actor,
				Result:  result,
				Since:   since,
				Until:   until,
				Limit:   limit,
			}

			ctx, cancel := ctxWithTimeout()
			defer cancel()

			if csvMode {
				// CSV is org-wide only at the backend; we already
				// rejected --project above so the route is unambiguous.
				body, err := cli.ExportOrgAuditCSV(ctx, org, q)
				if err != nil {
					return err
				}
				if csvPath == "" {
					_, err = os.Stdout.Write(body)
					return err
				}
				if err := os.WriteFile(csvPath, body, 0o600); err != nil {
					return fmt.Errorf("write --output-file: %w", err)
				}
				fmt.Printf("Wrote %d bytes to %s\n", len(body), csvPath)
				return nil
			}

			// Project-scoped path: prefer --project flag, else derive
			// from the current context's namespace via the scope.
			var list *backend.AuditList
			if orgWide {
				list, err = cli.ListOrgAudit(ctx, org, q)
			} else {
				p := project
				if p == "" {
					// Same parse rule as the orgs commands: tenant
					// context names are kube-dc/<domain>/<org>/<project>.
					p = projectFromContextName()
				}
				if p == "" {
					return fmt.Errorf("could not derive project from context — pass --project")
				}
				list, err = cli.ListProjectAudit(ctx, org, p, q)
				_ = scope // scope used implicitly via withBackend; reference to silence linter.
			}
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			printAuditTable(list)
			return nil
		},
	}
	cmd.Flags().BoolVar(&orgWide, "org", false, "Query org-wide audit (org-admin only)")
	cmd.Flags().StringVar(&orgFlag, "org-name", "", "Organization name (default: current context's org)")
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current context's project; ignored with --org, rejected with --csv)")
	cmd.Flags().StringVar(&service, "service", "", "Filter: secrets|certificates|kms|db-credentials|org-admin|audit")
	cmd.Flags().StringVar(&actor, "actor", "", "Filter: substring match against actor email / preferred_username")
	cmd.Flags().StringVar(&result, "result", "", "Filter: allowed|denied|error")
	cmd.Flags().StringVar(&since, "since", "", "Lower bound (epoch seconds OR RFC3339; e.g. 1h ago = backend default)")
	cmd.Flags().StringVar(&until, "until", "", "Upper bound (epoch seconds OR RFC3339; default = now)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max events to return (backend default 500, max 5000)")
	cmd.Flags().BoolVar(&csvMode, "csv", false, "Stream the org-wide audit as CSV (org-admin only)")
	cmd.Flags().StringVar(&csvPath, "output-file", "", "With --csv: write to this file instead of stdout")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format (non-CSV): table|json|yaml")
	return cmd
}

// projectFromContextName returns the project segment of a tenant
// kubeconfig context name. Empty for admin or non-kube-dc contexts.
func projectFromContextName() string {
	name, _ := readKubeconfigContextName()
	// Tenant context: kube-dc/<domain>/<org>/<project>
	// Admin context:  kube-dc/<domain>/admin
	parts := splitKubeDC(name)
	if len(parts) == 3 {
		return parts[2]
	}
	return ""
}

// splitKubeDC strips the kube-dc/ prefix and returns the remaining
// path segments. Returns nil if the prefix doesn't match.
func splitKubeDC(ctx string) []string {
	const prefix = "kube-dc/"
	if len(ctx) < len(prefix) || ctx[:len(prefix)] != prefix {
		return nil
	}
	rest := ctx[len(prefix):]
	if rest == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			out = append(out, rest[start:i])
			start = i + 1
		}
	}
	out = append(out, rest[start:])
	return out
}

// printAuditTable renders an AuditList as a fixed-width table. Body
// fields we surface: ts (epoch-ns → seconds float), service, action,
// result, resource, actor, elevation_id (truncated).
func printAuditTable(list *backend.AuditList) {
	if len(list.Events) == 0 {
		fmt.Println("No audit events match the filters.")
		return
	}
	fmt.Printf("%-20s  %-10s  %-22s  %-8s  %-30s  %-20s  %s\n",
		"TIME", "SERVICE", "ACTION", "RESULT", "RESOURCE", "ACTOR", "ELEVATION")
	for _, ev := range list.Events {
		b := ev.Body
		fmt.Printf("%-20s  %-10s  %-22s  %-8s  %-30s  %-20s  %s\n",
			backend.FormatEpochNs(ev.TS),
			truncCLI(stringField(b, "service"), 10),
			truncCLI(stringField(b, "action"), 22),
			truncCLI(stringField(b, "result"), 8),
			truncCLI(stringField(b, "resource"), 30),
			truncCLI(stringField(b, "actor"), 20),
			truncCLI(stringField(b, "elevation_id"), 16),
		)
	}
	fmt.Printf("\n%d events (limit %d).\n", list.Returned, list.Limit)
}

// stringField fetches a string from a body map without panicking on
// missing keys / non-string values. Returns "" for both.
func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, ok := m[k]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
