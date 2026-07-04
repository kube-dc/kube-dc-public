// `kube-dc certificates` subcommand — M2-T06 CLI for the Certificate
// Manager. Mirrors `kube-dc secrets`: scope resolution + JWT via the
// existing kubeconfig context, all calls go through the backend at
// backend.<domain>/api/certificates/* so the audit + admission
// guard layers there are exercised for CLI users too.
//
// Verbs:
//
//   list     — GET /api/certificates/:ns
//   get      — GET /api/certificates/:ns/:name
//   request  — POST /api/certificates/:ns/:name (a.k.a. "create")
//   renew    — POST /api/certificates/:ns/:name/renew
//   delete   — DELETE /api/certificates/:ns/:name
//
// Revoke is not in phase 1 — needs the OpenBao PKI revoke endpoint and
// per-cert serial tracking; tracked as M2 follow-up. The CLI will show
// an "M2 follow-up" stub once that lands.

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/spf13/cobra"
)

// certificatesCmd is the parent command. Re-uses the secrets
// scope-resolution machinery (secretsScope / resolveScope / etc.) since
// the auth + kubeconfig story is identical.
func certificatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certificates",
		Short: "Manage project TLS certificates",
		Long: `Manage project ManagedCertificate resources. Certificate issuance
is delegated to cert-manager under the hood:

  type=private   signs through the Organization's OpenBao intermediate CA
                 via a namespaced cert-manager Issuer (kube-dc-pki) in
                 the project namespace
  type=public    flows through the shared cluster ACME ClusterIssuer
                 (letsencrypt-prod-http) unchanged

Permissions follow your project role: viewers list and read, developers
request and renew, admins delete.`,
		Aliases: []string{"certs", "certificate"},
	}
	cmd.AddCommand(certsListCmd())
	cmd.AddCommand(certsGetCmd())
	cmd.AddCommand(certsRequestCmd())
	cmd.AddCommand(certsRenewCmd())
	cmd.AddCommand(certsDeleteCmd())
	return cmd
}

// -------- list -----------------------------------------------------

func certsListCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ManagedCertificates in the project namespace",
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
			list, err := cli.ListCertificates(ctx, scope.Namespace)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			return printCertificatesTable(list.Items)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- get ------------------------------------------------------

func certsGetCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:     "get <name>",
		Short:   "Show one ManagedCertificate + status mirror",
		Aliases: []string{"describe"},
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
			cert, err := cli.GetCertificate(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, cert)
			}
			return printCertificateDetail(cert)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- request (create) -----------------------------------------

func certsRequestCmd() *cobra.Command {
	var (
		namespace, certType, purpose, targetSecret, duration, renewBefore string
		dnsNames                                                          []string
	)
	cmd := &cobra.Command{
		Use:   "request <name>",
		Short: "Request a new ManagedCertificate",
		Long: `Request a new ManagedCertificate. The reconciler walks the lazy
provisioning chain — Organization PKI + per-Project role + namespaced
Issuer — on the first --type=private cert, and reuses everything for
subsequent requests.`,
		Example: `  # Private cert for an internal API:
  kube-dc certificates request internal-api \
    --dns api.docs.internal \
    --dns docs.internal

  # Public cert via Let's Encrypt:
  kube-dc certificates request public-api \
    --type public \
    --dns api.docs.example.com

  # Custom duration + renewal:
  kube-dc certificates request short-lived \
    --dns api.docs.internal \
    --duration 30d --renew-before 5d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if len(dnsNames) == 0 {
				return fmt.Errorf("at least one --dns is required")
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
			cert, err := cli.CreateCertificate(ctx, scope.Namespace, name, backend.CreateCertificateOptions{
				Type:             certType,
				Purpose:          purpose,
				DnsNames:         dnsNames,
				Duration:         duration,
				RenewBefore:      renewBefore,
				TargetSecretName: targetSecret,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Requested certificate %s/%s (type=%s, purpose=%s, target=%s)\n",
				cert.Namespace, cert.Name, cert.Type, cert.Purpose, fmtCoalesce(cert.TargetSecretName, "-"))
			fmt.Printf("Watch progress: kube-dc certificates get %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&certType, "type", "private", "Certificate type: private|public")
	cmd.Flags().StringVar(&purpose, "purpose", "server", "Cert purpose: server|client|mtls|code-signing")
	cmd.Flags().StringSliceVar(&dnsNames, "dns", nil, "DNS name (SAN) to include — repeatable")
	cmd.Flags().StringVar(&targetSecret, "target", "", "Target Kubernetes Secret name (default: <name>-tls)")
	cmd.Flags().StringVar(&duration, "duration", "", "Certificate validity period (e.g. 90d)")
	cmd.Flags().StringVar(&renewBefore, "renew-before", "", "Renew this long before expiry (e.g. 15d)")
	return cmd
}

// -------- renew ----------------------------------------------------

func certsRenewCmd() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "renew <name>",
		Short: "Force cert-manager to re-issue the certificate",
		Long: `Mark the underlying cert-manager Certificate for renewal. cert-manager
checks renewal eligibility on every reconcile — calling renew just
nudges it without waiting for spec.renewBefore to elapse. No
CertificateRequest churn happens unless renewal is actually due.`,
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
			res, err := cli.RenewCertificate(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Renewal requested at %s for %s/%s\n", res.RenewRequestedAt, res.Namespace, res.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	return cmd
}

// -------- delete ---------------------------------------------------

func certsDeleteCmd() *cobra.Command {
	var namespace string
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a ManagedCertificate",
		Long: `Delete a ManagedCertificate. The owned cert-manager Certificate
follows automatically via ownerRef GC; the project's namespaced PKI
Issuer and the per-Project OpenBao PKI role stay (they are shared
across every cert in the project — Project deletion cleans those).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				fmt.Fprintf(os.Stderr, "Delete %s? Re-run with --yes to confirm.\n", name)
				return fmt.Errorf("not confirmed")
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
			res, err := cli.DeleteCertificate(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Deleted %s/%s\n", res.Namespace, res.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion")
	return cmd
}

// -------- table rendering -----------------------------------------

// printCertificatesTable renders the list in column form. Pure — no
// network — extracted so unit tests can exercise the rendering with
// canned inputs.
func printCertificatesTable(items []backend.CertificateSummary) error {
	if len(items) == 0 {
		fmt.Println("No managed certificates.")
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tPURPOSE\tDNS\tSECRET\tREADY\tEXPIRES\tAGE")
	for _, it := range items {
		ready := certificateReadyFromConditions(it.Status.Conditions)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			it.Name,
			fmtCoalesce(it.Type, "private"),
			fmtCoalesce(it.Purpose, "server"),
			joinTruncate(it.DnsNames, 2),
			fmtCoalesce(it.TargetSecretName, "-"),
			ready,
			fmtCoalesce(formatExpiry(it.Status.NotAfter), "-"),
			formatAge(it.CreationTimestamp),
		)
	}
	return w.Flush()
}

func printCertificateDetail(c *backend.CertificateSummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "Name:\t%s/%s\n", c.Namespace, c.Name)
	fmt.Fprintf(w, "Type:\t%s\n", fmtCoalesce(c.Type, "private"))
	fmt.Fprintf(w, "Purpose:\t%s\n", fmtCoalesce(c.Purpose, "server"))
	fmt.Fprintf(w, "DNS Names:\t%s\n", strings.Join(c.DnsNames, ", "))
	fmt.Fprintf(w, "Target Secret:\t%s\n", c.TargetSecretName)
	if c.Duration != "" {
		fmt.Fprintf(w, "Duration:\t%s\n", c.Duration)
	}
	if c.RenewBefore != "" {
		fmt.Fprintf(w, "Renew Before:\t%s\n", c.RenewBefore)
	}
	fmt.Fprintf(w, "Created:\t%s\n", fmtCoalesce(c.CreationTimestamp, "-"))
	if c.Status.Issuer != "" {
		fmt.Fprintf(w, "Issuer:\t%s\n", c.Status.Issuer)
	}
	if c.Status.NotBefore != "" {
		fmt.Fprintf(w, "Not Before:\t%s\n", c.Status.NotBefore)
	}
	if c.Status.NotAfter != "" {
		fmt.Fprintf(w, "Not After:\t%s\n", c.Status.NotAfter)
	}
	if c.Status.RenewalTime != "" {
		fmt.Fprintf(w, "Renewal:\t%s\n", c.Status.RenewalTime)
	}
	fmt.Fprintf(w, "Ready:\t%s\n", certificateReadyFromConditions(c.Status.Conditions))
	return nil
}

// certificateReadyFromConditions extracts the Ready condition into a
// terse string for table cells. Returns "-" when the condition is
// absent (object not yet observed by the reconciler).
func certificateReadyFromConditions(conds []map[string]any) string {
	for _, c := range conds {
		if t, _ := c["type"].(string); t == "Ready" {
			if s, _ := c["status"].(string); s != "" {
				return s
			}
		}
	}
	return "-"
}

// formatExpiry converts an RFC3339 timestamp to a "in 2d/expired/-"
// shorthand for table cells. Empty input → "" so callers can chain
// fmtCoalesce.
func formatExpiry(rfc3339 string) string {
	if rfc3339 == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	days := int(d.Hours() / 24)
	switch {
	case days >= 1:
		return fmt.Sprintf("in %dd", days)
	case d >= time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	}
}

// formatAge renders an RFC3339 timestamp as a "5m / 3h / 4d" age
// indicator for table cells. Empty input → "-".
func formatAge(rfc3339 string) string {
	if rfc3339 == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// joinTruncate joins a slice with ", ", truncating to keep table
// cells readable. Returns "" for empty input.
func joinTruncate(items []string, max int) string {
	if len(items) == 0 {
		return "-"
	}
	if len(items) <= max {
		return strings.Join(items, ",")
	}
	return strings.Join(items[:max], ",") + fmt.Sprintf(",+%d", len(items)-max)
}
