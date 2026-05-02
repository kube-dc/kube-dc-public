package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	bkubeconfig "github.com/shalb/kube-dc/cli/internal/bootstrap/kubeconfig"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

// bootstrapKubeconfigCmd registers `kube-dc bootstrap kubeconfig <cluster>`.
//
// Layer-1 of the kubeconfig design (installer-prd §16.3): materialise a
// per-cluster kubeconfig context into ~/.kube/config (merged, not
// overwritten) using the kube-dc OIDC exec plugin. Always pins a realm
// in the exec args so kubectl never silently picks up the wrong cached
// identity (the bug that 13(e) feedback caught in v1).
//
// Default identity: admin (the fleet view is a platform-operator tool,
// so this command's audience is platform admins). Tenants typically run
// `kube-dc login --domain X --org Y` directly, which writes its own
// per-namespace contexts.
func bootstrapKubeconfigCmd(repoFlag *string) *cobra.Command {
	var (
		realmFlag       string
		insecureSkipTLS bool
		caCertFile      string
		setCurrent      bool
		commitTemplate  bool
		dryRun          bool
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig <cluster>",
		Short: "Materialise a per-cluster kubeconfig into ~/.kube/config (admin by default)",
		Long: `Adds or updates a kubeconfig context for the named cluster from the
fleet repo. Uses the kube-dc OIDC exec plugin so each operator gets
their own identity at runtime — no shared credentials, no kubeconfig
files committed to Git.

The cluster name matches the path under clusters/ in the fleet repo
(e.g. "cloud", "stage", "cs/zrh").

Identity:
  By default the kubeconfig is wired for the master-realm 'admin'
  identity (kube-dc/<cluster>/admin context, --realm master in exec
  args). Run 'kube-dc login --domain <cluster-domain> --admin' once to
  mint OIDC tokens, then kubectl works as cluster-admin.

  Pass --realm <name> to wire the kubeconfig for a tenant realm
  instead — but in practice tenants should just run
  'kube-dc login --domain X --org Y', which creates per-namespace
  contexts in one step.

When clusters/<name>/kubeconfig.template.yaml exists in the fleet,
it's used as-is. Otherwise the command synthesises one from
cluster-config.env. CA cert is fetched from the API server's TLS
handshake unless the chain is already publicly-trusted.`,
		Example: `  # Default: admin-flavored kubeconfig for the cloud cluster
  kube-dc bootstrap kubeconfig cloud
  kube-dc login --domain kube-dc.cloud --admin
  kubectl get nodes

  # Tenant-flavored (rarely needed; prefer 'kube-dc login --org' directly)
  kube-dc bootstrap kubeconfig stage --realm shalb

  # Commit a synthesised template back to the fleet
  kube-dc bootstrap kubeconfig stage --commit

  # Bring your own CA file
  kube-dc bootstrap kubeconfig cs/zrh --ca-cert /path/to/ca.crt

  # Show what would be written, don't touch ~/.kube/config
  kube-dc bootstrap kubeconfig cloud --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*repoFlag)
			if err != nil {
				return err
			}
			// Default to the master-realm admin identity. Empty flag →
			// "master" (admin); explicit flag honored verbatim.
			realm := realmFlag
			if realm == "" {
				realm = "master"
			}
			return runBootstrapKubeconfig(cmd.Context(), runKubeconfigOpts{
				RepoRoot:        repo,
				ClusterName:     args[0],
				Realm:           realm,
				InsecureSkipTLS: insecureSkipTLS,
				CACertFile:      caCertFile,
				SetCurrent:      setCurrent,
				CommitTemplate:  commitTemplate,
				DryRun:          dryRun,
			})
		},
	}

	cmd.Flags().StringVar(&realmFlag, "realm", "",
		"Keycloak realm to wire the kubeconfig for. Empty → 'master' (admin). Pass an org name to wire a tenant kubeconfig.")
	cmd.Flags().BoolVar(&insecureSkipTLS, "insecure-skip-tls-verify", false,
		"Skip TLS verification when fetching the API server CA (not recommended)")
	cmd.Flags().StringVar(&caCertFile, "ca-cert", "",
		"Path to a PEM-encoded CA cert file (skips the TLS-handshake fetch)")
	cmd.Flags().BoolVar(&setCurrent, "set-current", true,
		"Switch ~/.kube/config's current-context to the new context")
	cmd.Flags().BoolVar(&commitTemplate, "commit", false,
		"Write the synthesised kubeconfig.template.yaml back into the fleet repo (does not push)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print what would change without modifying ~/.kube/config or the fleet repo")

	return cmd
}

type runKubeconfigOpts struct {
	RepoRoot        string
	ClusterName     string
	Realm           string // "master" → admin; org name → tenant
	InsecureSkipTLS bool
	CACertFile      string
	SetCurrent      bool
	CommitTemplate  bool
	DryRun          bool
}

func runBootstrapKubeconfig(ctx context.Context, opts runKubeconfigOpts) error {
	clusters, err := discover.ListClusters(opts.RepoRoot)
	if err != nil {
		return fmt.Errorf("enumerate fleet: %w", err)
	}

	var target *discover.Cluster
	for i := range clusters {
		if clusters[i].Name == opts.ClusterName {
			target = &clusters[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("cluster %q not found in fleet repo at %s — known: %s",
			opts.ClusterName, opts.RepoRoot, joinNames(clusters))
	}

	// Layer 1: prefer an existing committed template. Falls through to
	// synthesis when no template has been committed yet.
	if existing, err := bkubeconfig.LoadTemplateFile(target.Dir); err == nil {
		fmt.Fprintf(os.Stderr, "✓ found template at clusters/%s/kubeconfig.template.yaml (%d bytes)\n",
			target.Name, len(existing))
		fmt.Fprintln(os.Stderr, "  template-file mode is not yet implemented — falling through to synthesis")
		// TODO(slice 3 follow-up): parse the committed template and
		// merge it via kubeconfig.Manager.AddKubeDCContext directly
		// so per-cluster customisations (server overrides, namespaces)
		// in the template survive. For now we synthesise from
		// cluster-config.env which has the same connection info anyway.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read template: %w", err)
	}

	tmpl, err := bkubeconfig.FromCluster(*target, opts.Realm)
	if err != nil {
		return err
	}

	// Resolve the CA: explicit file > TLS-handshake fetch > none (system trust).
	if opts.CACertFile != "" {
		b, err := os.ReadFile(opts.CACertFile)
		if err != nil {
			return fmt.Errorf("read --ca-cert: %w", err)
		}
		tmpl.CACertPEM = string(b)
	} else if !opts.InsecureSkipTLS {
		fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		ca, fetchErr := bkubeconfig.FetchCA(fetchCtx, tmpl.Server, 5*time.Second)
		if fetchErr != nil {
			fmt.Fprintf(os.Stderr, "⚠  CA fetch from %s failed: %v\n", tmpl.Server, fetchErr)
			fmt.Fprintln(os.Stderr, "   continuing without CA — will rely on system trust")
		} else if ca == "" {
			fmt.Fprintf(os.Stderr, "✓ %s presents a publicly-trusted certificate; no CA embed needed\n", tmpl.Server)
		} else {
			tmpl.CACertPEM = ca
			fmt.Fprintf(os.Stderr, "✓ fetched CA from %s (%d bytes)\n", tmpl.Server, len(ca))
		}
	}

	// Print the resolved template details.
	identityLabel := "admin (master realm)"
	if tmpl.Realm != "master" {
		identityLabel = "tenant (realm: " + tmpl.Realm + ")"
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  identity: %s\n", identityLabel)
	fmt.Fprintf(os.Stderr, "  cluster:  %s\n", tmpl.ClusterName)
	fmt.Fprintf(os.Stderr, "  context:  %s\n", tmpl.ContextName)
	fmt.Fprintf(os.Stderr, "  user:     %s  (exec: kube-dc credential --realm %s)\n", tmpl.UserName, tmpl.Realm)
	fmt.Fprintf(os.Stderr, "  server:   %s\n", tmpl.Server)
	if tmpl.CACertPEM != "" {
		fmt.Fprintf(os.Stderr, "  ca:       embedded (%d bytes PEM)\n", len(tmpl.CACertPEM))
	} else {
		fmt.Fprintf(os.Stderr, "  ca:       (none — relying on system trust)\n")
	}
	fmt.Fprintln(os.Stderr)

	if opts.DryRun {
		fmt.Fprintln(os.Stderr, "✓ dry-run: ~/.kube/config not modified")
		if opts.CommitTemplate {
			fmt.Fprintln(os.Stderr, "✓ dry-run: would write clusters/"+target.Name+"/kubeconfig.template.yaml")
		}
		return nil
	}

	// Layer 1: merge into ~/.kube/config via the existing manager.
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("init kubeconfig manager: %w", err)
	}
	if err := mgr.AddKubeDCContext(kubeconfig.AddContextParams{
		Server:      tmpl.Server,
		ClusterName: tmpl.ClusterName,
		UserName:    tmpl.UserName,
		ContextName: tmpl.ContextName,
		CACert:      tmpl.CACertPEM,
		Insecure:    opts.InsecureSkipTLS,
		SetCurrent:  opts.SetCurrent,
		Realm:       tmpl.Realm, // pins --realm in exec args — no silent fallback
	}); err != nil {
		return fmt.Errorf("update kubeconfig: %w", err)
	}

	fmt.Printf("✓ kubeconfig context %q written to %s\n", tmpl.ContextName, kubeconfigPath())
	if opts.SetCurrent {
		fmt.Printf("  current-context now: %s\n", tmpl.ContextName)
	}
	fmt.Println()
	// Identity-specific next step. Default (admin) tells the operator to
	// run --admin login; tenant flow points at --org login.
	domain := domainFromAPI(tmpl.Server)
	if tmpl.Realm == "master" {
		fmt.Printf("Next: run `kube-dc login --domain %s --admin` to mint admin OIDC tokens, then kubectl works as cluster-admin.\n", domain)
	} else {
		fmt.Printf("Next: run `kube-dc login --domain %s --org %s` to mint tenant OIDC tokens.\n", domain, tmpl.Realm)
		fmt.Println("(Or skip this kubeconfig step entirely — `kube-dc login --org` writes its own per-namespace contexts.)")
	}

	if opts.CommitTemplate {
		path := filepath.Join(target.Dir, "kubeconfig.template.yaml")
		if err := writeTemplateFile(path, *tmpl); err != nil {
			return fmt.Errorf("write template: %w", err)
		}
		fmt.Printf("✓ template written: %s (commit + push manually)\n", path)
	}
	return nil
}

// domainFromAPI strips "https://kube-api." and the trailing port off
// an apiserver URL to produce the domain operators pass to
// `kube-dc login --domain`.
func domainFromAPI(api string) string {
	s := api
	if i := len("https://kube-api."); len(s) > i && s[:i] == "https://kube-api." {
		s = s[i:]
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '/' {
			return s[:i]
		}
	}
	return s
}

// kubeconfigPath returns the path the manager would write to, for the
// success line. Mirrors the resolution rules in kubeconfig.NewManager.
func kubeconfigPath() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube", "config")
	}
	return "~/.kube/config"
}

// writeTemplateFile renders a Layer-1 template back to disk so the next
// operator can use the committed copy instead of re-synthesising.
func writeTemplateFile(path string, t bkubeconfig.Template) error {
	var caBlock string
	if t.CACertPEM != "" {
		// Indent the PEM into a YAML literal block — preserves newlines
		// without base64 in the file; the manager will decode at use time.
		// We emit certificate-authority-data instead (base64) for byte-for-
		// byte compatibility with the existing manager.
		caBlock = "    certificate-authority-data: " + base64String(t.CACertPEM) + "\n"
	}
	body := "" +
		"# Generated by `kube-dc bootstrap kubeconfig --commit`\n" +
		"# Layer 1 (see installer-prd §16): connection metadata only — no credentials.\n" +
		"# Identity is minted at exec-time via `kube-dc credential` (realm=" + t.Realm + ").\n" +
		"apiVersion: v1\n" +
		"kind: Config\n" +
		"clusters:\n" +
		"- name: " + t.ClusterName + "\n" +
		"  cluster:\n" +
		"    server: " + t.Server + "\n" +
		caBlock +
		"contexts:\n" +
		"- name: " + t.ContextName + "\n" +
		"  context:\n" +
		"    cluster: " + t.ClusterName + "\n" +
		"    user: " + t.UserName + "\n" +
		"users:\n" +
		"- name: " + t.UserName + "\n" +
		"  user:\n" +
		"    exec:\n" +
		"      apiVersion: client.authentication.k8s.io/v1\n" +
		"      command: kube-dc\n" +
		"      args: [\"credential\", \"--server\", \"" + t.Server + "\", \"--realm\", \"" + t.Realm + "\"]\n" +
		"      interactiveMode: IfAvailable\n"
	return os.WriteFile(path, []byte(body), 0644)
}

// base64String returns the standard base64 encoding of s.
func base64String(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func joinNames(cs []discover.Cluster) string {
	if len(cs) == 0 {
		return "(none)"
	}
	out := ""
	for i, c := range cs {
		if i > 0 {
			out += ", "
		}
		out += c.Name
	}
	return out
}
