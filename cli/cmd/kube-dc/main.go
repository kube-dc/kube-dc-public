package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/alerts"
	alertstui "github.com/shalb/kube-dc/cli/internal/alerts/tui"
	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/config"
	"github.com/shalb/kube-dc/cli/internal/jwt"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
	"github.com/shalb/kube-dc/cli/pkg/credential"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	rootCmd := newRootCmd()

	// Cobra prints "Error: <err>" itself, and the handler below prints the same
	// error again — so every failure has always been emitted twice. It went
	// unnoticed while messages were one-liners; a multi-line explanation makes
	// it obvious. main() is the single place that knows about the doctor's exit
	// codes, so it stays the printer and cobra stays quiet.
	rootCmd.SilenceErrors = true

	if err := rootCmd.Execute(); err != nil {
		code, msg := classifyExecuteErr(err)
		if msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(code)
	}
}

// newRootCmd assembles the whole command tree. Split out of main() so tests can
// walk it — in particular so the operator-facing quickstart can be checked to
// only reference commands and flags that actually exist.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kube-dc",
		Short: "Authenticate to Organizations and select Project contexts",
		Long: `Kube-DC CLI provides browser-based Organization authentication and
Project-scoped kubeconfig contexts.

It follows the same patterns as AWS CLI, GCloud, and other cloud provider CLIs:
- Browser-based OAuth login
- Automatic token refresh
- Project context selection with kube-dc use
- Seamless kubectl integration`,
	}

	rootCmd.AddCommand(loginCmd())
	rootCmd.AddCommand(logoutCmd())
	rootCmd.AddCommand(useCmd())
	rootCmd.AddCommand(nsCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(credentialCmd())
	rootCmd.AddCommand(alertsCmd())
	rootCmd.AddCommand(bootstrapCmd())
	rootCmd.AddCommand(secretsCmd())
	rootCmd.AddCommand(certificatesCmd())
	rootCmd.AddCommand(kmsCmd())
	rootCmd.AddCommand(dbCmd())
	rootCmd.AddCommand(orgsCmd())
	rootCmd.AddCommand(auditCmd())
	rootCmd.AddCommand(versionCmd())
	return rootCmd
}

// classifyExecuteErr decides the process exit code and what (if anything)
// to print for an error returned by rootCmd.Execute().
//
// Split out of main() so the contract is testable: main() itself calls
// os.Exit and cannot be exercised by a unit test, which is how the bug
// below survived.
//
//   - *doctorExitCodeErr (incl. wrapped): the command has ALREADY rendered
//     its full report, so exit with its code and print nothing.
//   - everything else: print the error and exit 1.
//
// The match MUST be on the concrete *doctorExitCodeErr. It was previously
// `var de interface{ ExitCode() int }`, and *exec.ExitError satisfies that
// too — so every failing child process (sops, kubectl, bao, ssh) took the
// silent path and exited with the child's status while printing nothing.
// `openbao setup-controller-auth` surfaced as exit=100 with zero bytes on
// stdout and stderr, with no way to diagnose it.
func classifyExecuteErr(err error) (code int, msg string) {
	if err == nil {
		return 0, ""
	}
	var de *doctorExitCodeErr
	if errors.As(err, &de) {
		return de.ExitCode(), ""
	}
	// Our OWN exit-code error type, matched by concrete type rather than by an
	// anonymous interface. That distinction is the whole point: *exec.ExitError
	// also has ExitCode(), so an interface match silently propagated every
	// failing child process's status with no output at all — see
	// TestClassifyExecuteErr, which pins that regression. Opting in by type
	// means only errors we author participate.
	//
	// Needed because commands like `bootstrap accept` distinguish "not usable
	// yet" (1) from "cannot even read the cluster" (2), which is the reason to
	// script against them; collapsing both to 1 erases it. Unlike the doctor,
	// these DO print their message.
	var ce *exitCodeError
	if errors.As(err, &ce) {
		return ce.ExitCode(), ce.Error()
	}
	return 1, err.Error()
}

func loginCmd() *cobra.Command {
	var domain string
	var org string
	var admin bool
	var deviceCode bool
	var caCertFile string
	var insecure bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with a Kube-DC server",
		Long: `Authenticate with a Kube-DC server using browser-based OAuth.

Opens your default browser for authentication. After successful login,
your credentials are cached and kubectl is configured automatically.

The domain is used to derive the API and login URLs:
  - API Server: https://kube-api.{domain}:6443
  - Keycloak:   https://login.{domain}

Two identity modes:
  --org <name>   Organization login against the per-Organization realm. Writes
                 one context named kube-dc/<domain>/<org>/<project> for each
                 accessible Project. Each context selects that Project's
                 backing namespace and pins the Organization realm.

  --admin        Platform-admin login against the master realm and the
                 'kube-dc-admin' OIDC client. Writes a single context
                 named kube-dc/<domain>/admin with cluster-wide RBAC
                 (via the platform:admin group claim).`,
		Example: `  # Organization login
  kube-dc login --domain stage.kube-dc.com --org shalb

  # Platform-admin login
  kube-dc login --domain kube-dc.cloud --admin

  # With CA certificate (for self-hosted)
  kube-dc login --domain internal.example.com --org myorg --ca-cert /path/to/ca.crt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if admin && org != "" {
				return fmt.Errorf("--admin and --org are mutually exclusive (admin always uses the master realm)")
			}
			// Past flag validation everything that can fail is a RUNTIME problem
			// (unknown realm, unreachable Keycloak, browser flow timeout). Dumping
			// the usage block in front of those buries the actual explanation —
			// which is the whole point of the realm pre-flight below.
			cmd.SilenceUsage = true
			if admin {
				return runAdminLogin(domain, caCertFile, insecure, deviceCode)
			}
			return runLogin(domain, org, caCertFile, insecure, deviceCode)
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Kube-DC domain (e.g., stage.kube-dc.com)")
	cmd.Flags().StringVar(&org, "org", "", "Organization (Keycloak realm). Mutually exclusive with --admin.")
	cmd.Flags().BoolVar(&admin, "admin", false, "Login as a platform admin against the master realm (cluster-wide RBAC)")
	cmd.Flags().StringVar(&caCertFile, "ca-cert", "", "Path to CA certificate file")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification (not recommended)")
	cmd.Flags().BoolVar(&deviceCode, "device-code", false, "NOT IMPLEMENTED — returns an error. On a headless machine, either run `kube-dc login` on a workstation WITH a browser and copy the resulting ~/.kube/config, or use the client-certificate kubeconfig from `kube-dc bootstrap fetch-kubeconfig` (which needs no browser and no OIDC)")

	return cmd
}

func runLogin(domain, org, caCertFile string, insecure, deviceCode bool) error {
	reader := bufio.NewReader(os.Stdin)

	// Prompt for domain if not provided
	if domain == "" {
		fmt.Print("Enter Kube-DC domain (e.g., stage.kube-dc.com): ")
		input, _ := reader.ReadString('\n')
		domain = strings.TrimSpace(input)
	}
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	// Prompt for organization if not provided
	if org == "" {
		fmt.Print("Enter Organization name: ")
		input, _ := reader.ReadString('\n')
		org = strings.TrimSpace(input)
	}
	if org == "" {
		return fmt.Errorf("Organization is required")
	}

	// Derive URLs from domain
	server := fmt.Sprintf("https://kube-api.%s:6443", domain)
	keycloakURL := fmt.Sprintf("https://login.%s", domain)

	// Load CA certificate if provided
	var caCertPEM string
	if caCertFile != "" {
		certData, err := os.ReadFile(caCertFile)
		if err != nil {
			return fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPEM = string(certData)
		fmt.Printf("Using CA certificate from %s\n", caCertFile)
	}

	if deviceCode {
		return fmt.Errorf("device code flow is not implemented. " +
			"On a headless machine use one of these instead:\n" +
			"  - run `kube-dc login` on a workstation WITH a browser, then copy ~/.kube/config over; or\n" +
			"  - use the client-certificate kubeconfig from `kube-dc bootstrap fetch-kubeconfig`, " +
			"which needs neither a browser nor OIDC")
	}

	fmt.Printf("\n🔐 Logging in to %s (Organization: %s)\n", domain, org)
	fmt.Printf("   API Server: %s\n", server)
	fmt.Printf("   Keycloak:   %s\n\n", keycloakURL)

	// Confirm the realm exists BEFORE opening a browser. Keycloak answers an
	// unknown realm with a bare 404 page, so without this the user gets a
	// browser window showing "404" and no indication of what went wrong or
	// which value was rejected.
	//
	// The mistake this catches is not a typo — it is a genuine ambiguity in our
	// own naming. `kube-dc login` asks for the ORGANIZATION, but the thing a
	// tenant works in day to day is a PROJECT, and project namespaces are
	// `<org>-<project>`. Someone in org `test2` working on project `test2-2`
	// naturally enters `test2-2` and lands on a realm that does not exist
	// (reported 2026-07-20).
	if err := verifyRealmExists(keycloakURL, org, caCertPEM); err != nil {
		return err
	}

	// Create OAuth config
	oauthConfig := &auth.OAuthConfig{
		KeycloakURL: keycloakURL,
		Realm:       org,
		ClientID:    "kube-dc",
		CACert:      caCertPEM,
		Insecure:    insecure,
	}

	// Run OAuth flow
	flow := auth.NewOAuthFlow(oauthConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tokenResponse, err := flow.Login(ctx)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	fmt.Println("\n✓ Authentication successful!")

	// Parse the access token to get user info
	claims, err := jwt.ParseToken(tokenResponse.AccessToken)
	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}

	fmt.Printf("  User: %s\n", claims.Email)
	fmt.Printf("  Organization: %s\n", claims.Org)
	if len(claims.Namespaces) > 0 {
		fmt.Printf("  Accessible Projects (backing namespaces): %s\n", strings.Join(claims.Namespaces, ", "))
	}

	// Save credentials
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return fmt.Errorf("failed to initialize credentials manager: %w", err)
	}

	// Calculate refresh token expiry
	// For offline tokens, RefreshExpiresIn is 0 which means "never expires"
	// We set a reasonable default of 30 days (standard for CLI tools like gcloud)
	var refreshTokenExpiry time.Time
	if tokenResponse.RefreshExpiresIn <= 0 {
		// Offline token - set expiry to 30 days from now
		refreshTokenExpiry = time.Now().Add(30 * 24 * time.Hour)
	} else {
		refreshTokenExpiry = time.Now().Add(time.Duration(tokenResponse.RefreshExpiresIn) * time.Second)
	}

	creds := &config.Credentials{
		Server:             server,
		KeycloakURL:        keycloakURL,
		Realm:              org,
		ClientID:           "kube-dc",
		AccessToken:        tokenResponse.AccessToken,
		RefreshToken:       tokenResponse.RefreshToken,
		IDToken:            tokenResponse.IDToken,
		AccessTokenExpiry:  claims.ExpiryTime(),
		RefreshTokenExpiry: refreshTokenExpiry,
		User: config.UserInfo{
			Email:      claims.Email,
			Org:        claims.Org,
			Groups:     claims.Groups,
			Namespaces: claims.Namespaces,
		},
		CACert:   caCertPEM,
		Insecure: insecure,
	}

	if err := credMgr.Save(creds); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}
	fmt.Println("  Credentials cached to ~/.kube-dc/credentials/")

	// Footgun guard: if $KUBECONFIG points at a non-default file, the
	// new contexts land THERE — not in ~/.kube/config — and tools like
	// kubectx that read ~/.kube/config by default won't see them.
	// Surfacing the destination + asking for confirmation prevents the
	// "I logged in but kubectx shows nothing" trap.
	if err := warnIfNonDefaultKubeconfig(); err != nil {
		return err
	}

	// Update kubeconfig
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("failed to initialize kubeconfig manager: %w", err)
	}

	// Create one context for each accessible Project.
	// Include domain in names to support multiple kube-dc installations
	for i, ns := range claims.Namespaces {
		params := tenantContextParams(domain, org, server, ns, caCertPEM, insecure, i == 0)

		if err := kubeMgr.AddKubeDCContext(params); err != nil {
			fmt.Printf("  Warning: failed to add context %s: %v\n", params.ContextName, err)
		}
	}

	// An Organization with NO Project yields a token with no namespaces claim.
	// The loop above then writes zero contexts while everything still reports
	// success — authentication really did work, the token is valid and cached —
	// so the operator is left with a working login and a kubectl that has
	// nothing to talk to, and no hint as to why. Say it plainly instead of
	// printing "Kubeconfig updated" over an empty change.
	if len(claims.Namespaces) == 0 {
		fmt.Println()
		fmt.Println("  Authenticated, but NO kubectl context was created.")
		fmt.Printf("  Your token carries no Projects for organization %q, and tenant\n", org)
		fmt.Println("  contexts are created per Project — one Kubernetes namespace each.")
		fmt.Println()
		fmt.Println("  Ask an administrator to create a Project in your organization, or if")
		fmt.Println("  you administer it yourself:")
		fmt.Printf("    kubectl apply -f - <<'EOF'\n")
		fmt.Printf("    apiVersion: kube-dc.com/v1\n")
		fmt.Printf("    kind: Project\n")
		fmt.Printf("    metadata:\n      name: web\n      namespace: %s\n", org)
		fmt.Printf("    spec:\n      egressNetworkType: cloud\n")
		fmt.Printf("    EOF\n")
		fmt.Println()
		fmt.Println("  Then run this login again to pick up the new context.")
		return nil
	}

	fmt.Println("  Kubeconfig updated in ~/.kube/config")

	// Show next steps
	if len(claims.Namespaces) > 0 {
		projectName := claims.Namespaces[0]
		if strings.HasPrefix(projectName, org+"-") {
			projectName = strings.TrimPrefix(projectName, org+"-")
		}
		fmt.Printf("\n🚀 Ready! Current context: kube-dc/%s/%s/%s\n", domain, org, projectName)
		fmt.Println("\nTry: kubectl get pods")
	}

	return nil
}

// tenantContextParams keeps the security-sensitive relationship between a
// Project context and its Organization realm in one testable place.
func tenantContextParams(domain, org, server, namespace, caCertPEM string, insecure, setCurrent bool) kubeconfig.AddContextParams {
	projectName := namespace
	if strings.HasPrefix(namespace, org+"-") {
		projectName = strings.TrimPrefix(namespace, org+"-")
	}

	return kubeconfig.AddContextParams{
		Server:      server,
		ClusterName: fmt.Sprintf("kube-dc-%s-%s", domain, org),
		UserName:    fmt.Sprintf("kube-dc@%s/%s", domain, org),
		ContextName: fmt.Sprintf("kube-dc/%s/%s/%s", domain, org, projectName),
		Namespace:   namespace,
		CACert:      caCertPEM,
		Insecure:    insecure,
		SetCurrent:  setCurrent,
		Realm:       org,
	}
}

func logoutCmd() *cobra.Command {
	var server string
	var all bool
	var removeContexts bool

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove cached credentials",
		Long:  `Remove cached credentials and optionally remove kubeconfig entries.`,
		Example: `  # Logout from current server
  kube-dc logout

  # Logout from specific server
  kube-dc logout --server https://api.kube-dc.cloud

  # Logout from all servers
  kube-dc logout --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout(server, all, removeContexts)
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "Kube-DC API server URL")
	cmd.Flags().BoolVar(&all, "all", false, "Logout from all servers")
	cmd.Flags().BoolVar(&removeContexts, "remove-contexts", false, "Also remove kubeconfig contexts")

	return cmd
}

func runLogout(server string, all, removeContexts bool) error {
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return fmt.Errorf("failed to initialize credentials manager: %w", err)
	}

	if all {
		// Remove all credentials
		creds, err := credMgr.List()
		if err != nil {
			return fmt.Errorf("failed to list credentials: %w", err)
		}

		if len(creds) == 0 {
			fmt.Println("No credentials to remove.")
			return nil
		}

		for _, c := range creds {
			if err := credMgr.Delete(c.Server); err != nil {
				fmt.Printf("Warning: failed to remove credentials for %s: %v\n", c.Server, err)
			} else {
				fmt.Printf("Removed credentials for %s\n", c.Server)
			}

			if removeContexts {
				kubeMgr, _ := kubeconfig.NewManager()
				if kubeMgr != nil {
					kubeMgr.RemoveKubeDCContexts(c.Server)
				}
			}
		}
		fmt.Println("Logged out from all servers.")
		return nil
	}

	// If no server specified, try to get from current context
	if server == "" {
		kubeMgr, err := kubeconfig.NewManager()
		if err == nil {
			kubeConfig, err := kubeMgr.Load()
			if err == nil && strings.HasPrefix(kubeConfig.CurrentContext, "kube-dc/") {
				for _, ctx := range kubeConfig.Contexts {
					if ctx.Name == kubeConfig.CurrentContext {
						for _, cluster := range kubeConfig.Clusters {
							if cluster.Name == ctx.Context.Cluster {
								server = cluster.Cluster.Server
								break
							}
						}
						break
					}
				}
			}
		}
	}

	if server == "" {
		return fmt.Errorf("no server specified and no kube-dc context active. Use --server or --all")
	}

	// Remove credentials for specific server
	if err := credMgr.Delete(server); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	fmt.Printf("Removed credentials for %s\n", server)

	if removeContexts {
		kubeMgr, err := kubeconfig.NewManager()
		if err == nil {
			if err := kubeMgr.RemoveKubeDCContexts(server); err != nil {
				fmt.Printf("Warning: failed to remove kubeconfig contexts: %v\n", err)
			} else {
				fmt.Println("Removed kubeconfig contexts.")
			}
		}
	}

	return nil
}

func useCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use [domain/org/project|context]",
		Short: "Switch to a different Organization or Project context",
		Long: `Switch to a different Kube-DC context.

Pass domain/org/project or a full kube-dc/<domain>/<org>/<project> context
name. If no argument is provided, available Kube-DC contexts are listed.`,
		Example: `  # Switch to specific project
  kube-dc use kube-dc.cloud/shalb/demo

  # List available contexts
  kube-dc use`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUse(args)
		},
	}

	return cmd
}

func runUse(args []string) error {
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	kubeConfig, err := kubeMgr.Load()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Get all kube-dc contexts
	var kubedcContexts []string
	for _, ctx := range kubeConfig.Contexts {
		if strings.HasPrefix(ctx.Name, "kube-dc/") {
			kubedcContexts = append(kubedcContexts, ctx.Name)
		}
	}

	if len(kubedcContexts) == 0 {
		return fmt.Errorf("no kube-dc contexts found. Run: kube-dc login")
	}

	// If no argument, list contexts
	if len(args) == 0 {
		fmt.Println("Available kube-dc contexts:")
		for _, ctx := range kubedcContexts {
			marker := "  "
			if ctx == kubeConfig.CurrentContext {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, ctx)
		}
		return nil
	}

	// Switch to specified context
	target := args[0]

	// Allow domain/org/project or the full kube-dc/domain/org/project name.
	contextName := target
	if !strings.HasPrefix(target, "kube-dc/") {
		contextName = "kube-dc/" + target
	}

	// Verify context exists
	found := false
	for _, ctx := range kubedcContexts {
		if ctx == contextName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("context '%s' not found. Available: %v", contextName, kubedcContexts)
	}

	if err := kubeMgr.SetCurrentContext(contextName); err != nil {
		return fmt.Errorf("failed to switch context: %w", err)
	}

	fmt.Printf("Switched to context: %s\n", contextName)
	return nil
}

func nsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ns [project-namespace]",
		Short: "Select or list legacy Project backing namespaces",
		Long: `Compatibility command for selecting a Project's backing namespace.

This rewrites the namespace field of the current context; it does not switch
the context name. Prefer "kube-dc use <domain>/<org>/<project>" so the context
name and selected Project stay aligned.

If no argument is provided, accessible Project backing namespaces from the
current Organization token are listed.`,
		Example: `  # List accessible Project backing namespaces
  kube-dc ns

  # Select a backing namespace (compatibility behavior)
  kube-dc ns shalb-dev`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNs(args)
		},
	}

	return cmd
}

func runNs(args []string) error {
	// Get current context from kubeconfig
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	kubeConfig, err := kubeMgr.Load()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Check if current context is a kube-dc context
	if !strings.HasPrefix(kubeConfig.CurrentContext, "kube-dc/") {
		return fmt.Errorf("current context '%s' is not a kube-dc context", kubeConfig.CurrentContext)
	}

	// Find the server URL from the current context
	var serverURL string
	var currentNamespace string
	for _, ctx := range kubeConfig.Contexts {
		if ctx.Name == kubeConfig.CurrentContext {
			currentNamespace = ctx.Context.Namespace
			for _, cluster := range kubeConfig.Clusters {
				if cluster.Name == ctx.Context.Cluster {
					serverURL = cluster.Cluster.Server
					break
				}
			}
			break
		}
	}

	if serverURL == "" {
		return fmt.Errorf("could not find server URL for current context")
	}

	// Load credentials for the realm encoded in the current context. A machine
	// can cache Organization and platform-admin identities for the same server;
	// an unqualified load could otherwise select the wrong token.
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	realm := realmFromContext(kubeConfig.CurrentContext)
	creds, err := credMgr.LoadForRealm(serverURL, realm)
	if err != nil {
		loginMode := fmt.Sprintf("--org %s", realm)
		if realm == "master" {
			loginMode = "--admin"
		}
		return fmt.Errorf("not logged in. Run: kube-dc login --domain %s %s", domainFromAPI(serverURL), loginMode)
	}

	namespaces := creds.User.Namespaces
	if len(namespaces) == 0 {
		// Try to get from token
		claims, err := jwt.ParseToken(creds.AccessToken)
		if err == nil && len(claims.Namespaces) > 0 {
			namespaces = claims.Namespaces
		}
	}

	if len(namespaces) == 0 {
		return fmt.Errorf("no accessible Projects found in credentials")
	}

	// If no argument, list Project backing namespaces.
	if len(args) == 0 {
		fmt.Println("Accessible Project backing namespaces:")
		for _, ns := range namespaces {
			marker := "  "
			if ns == currentNamespace {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, ns)
		}
		return nil
	}

	// Select a Project backing namespace on the current context.
	targetNs := args[0]

	// Validate namespace is in allowed list
	found := false
	for _, ns := range namespaces {
		if ns == targetNs {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("Project backing namespace '%s' is not in your accessible Projects: %v", targetNs, namespaces)
	}

	// Update kubeconfig
	if err := kubeMgr.SetNamespace(targetNs); err != nil {
		return fmt.Errorf("failed to set namespace: %w", err)
	}

	fmt.Printf("Selected Project backing namespace: %s\n", targetNs)
	return nil
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
		Long:  `Manage CLI configuration including servers and preferences.`,
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration and credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow()
		},
	}

	getContextsCmd := &cobra.Command{
		Use:   "get-contexts",
		Short: "List all kube-dc contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUse(nil) // Reuse the use command's list functionality
		},
	}

	cmd.AddCommand(showCmd)
	cmd.AddCommand(getContextsCmd)

	return cmd
}

func runConfigShow() error {
	// Show current kubeconfig context
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	kubeConfig, err := kubeMgr.Load()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	fmt.Println("=== Current Context ===")
	fmt.Printf("Context: %s\n", kubeConfig.CurrentContext)

	// Find the current Project's backing namespace.
	for _, ctx := range kubeConfig.Contexts {
		if ctx.Name == kubeConfig.CurrentContext {
			fmt.Printf("Backing namespace: %s\n", ctx.Context.Namespace)
			for _, cluster := range kubeConfig.Clusters {
				if cluster.Name == ctx.Context.Cluster {
					fmt.Printf("Server: %s\n", cluster.Cluster.Server)
					break
				}
			}
			break
		}
	}

	// Show cached credentials
	fmt.Println("\n=== Cached Credentials ===")
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		fmt.Printf("Error loading credentials: %v\n", err)
		return nil
	}

	creds, err := credMgr.List()
	if err != nil {
		fmt.Printf("Error listing credentials: %v\n", err)
		return nil
	}

	if len(creds) == 0 {
		fmt.Println("No cached credentials.")
		return nil
	}

	for _, c := range creds {
		fmt.Printf("\nServer: %s\n", c.Server)
		fmt.Printf("  User: %s\n", c.User.Email)
		fmt.Printf("  Organization: %s\n", c.User.Org)
		fmt.Printf("  Groups: %v\n", c.User.Groups)
		fmt.Printf("  Accessible Project backing namespaces: %v\n", c.User.Namespaces)

		// Token status
		if c.IsAccessTokenValid() {
			fmt.Printf("  Access Token: valid (expires in %s)\n", time.Until(c.AccessTokenExpiry).Round(time.Second))
		} else {
			fmt.Printf("  Access Token: expired\n")
		}

		if c.IsRefreshTokenValid() {
			fmt.Printf("  Refresh Token: valid (expires in %s)\n", time.Until(c.RefreshTokenExpiry).Round(time.Second))
		} else {
			fmt.Printf("  Refresh Token: expired (re-login required)\n")
		}
	}

	return nil
}

func credentialCmd() *cobra.Command {
	var server string
	var realm string

	cmd := &cobra.Command{
		Use:    "credential",
		Short:  "Get credentials for kubectl (exec plugin)",
		Long:   `Returns an ExecCredential for kubectl. This command is typically called by kubectl, not directly by users.`,
		Hidden: false, // Make visible for debugging
		RunE: func(cmd *cobra.Command, args []string) error {
			if server == "" {
				return fmt.Errorf("--server flag is required")
			}
			// kubectl invokes this as an exec plugin, so its stderr is what the
			// user actually sees when a session expires. Printing the flag usage
			// block in front of "session expired" buries the one line that tells
			// them what to do — and they never typed the command anyway.
			cmd.SilenceUsage = true

			provider, err := credential.NewProvider()
			if err != nil {
				return err
			}

			// Realm-aware lookup when the kubeconfig context provides it (all
			// newly generated contexts do). Older tenant kubeconfigs call
			// without --realm and use the fail-closed legacy lookup in
			// credentials.Manager.
			cred, err := provider.GetCredentialForRealm(server, realm)
			if err != nil {
				return err
			}

			return cred.Output()
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "Kube-DC API server URL")
	cmd.Flags().StringVar(&realm, "realm", "", "Keycloak realm (admin: master; Organization identity: Organization name). Optional; legacy kubeconfigs omit this.")
	cmd.MarkFlagRequired("server")

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("kube-dc CLI %s\n", version)
		},
	}
}

func alertsCmd() *cobra.Command {
	var severity string
	var source string
	var namespace string
	var output string
	var refresh int
	var alertmanagerURL string
	var portForward bool
	var cluster string

	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "View and manage Alertmanager alerts",
		Long: `View and manage Alertmanager alerts in Kube-DC clusters.

Provides a terminal-based interface to browse, filter, and sort alerts
from Alertmanager. Uses an admin kubeconfig to reach Alertmanager via
kubectl port-forward by default. Mimir tenant auth will be added later.`,
		Example: `  # View alerts in TUI mode (auto port-forward)
  kube-dc alerts

  # Filter by severity
  kube-dc alerts --severity critical

  # Use an existing Alertmanager URL
  kube-dc alerts --alertmanager-url http://localhost:9093

  # Output as JSON
  kube-dc alerts --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlerts(runAlertsOpts{
				Severity:        severity,
				Source:          source,
				Namespace:       namespace,
				Output:          output,
				Refresh:         refresh,
				AlertmanagerURL: alertmanagerURL,
				PortForward:     portForward,
				Cluster:         cluster,
			})
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (critical, warning, info, none)")
	cmd.Flags().StringVar(&source, "source", "", "Filter by source/component")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Filter by namespace")
	cmd.Flags().StringVar(&output, "output", "tui", "Output format: tui (default), json, table")
	cmd.Flags().IntVar(&refresh, "refresh", 30, "Refresh interval in seconds")
	cmd.Flags().StringVar(&alertmanagerURL, "alertmanager-url", "", "Alertmanager URL (overrides ALERTMANAGER_URL env; disables port-forward)")
	cmd.Flags().BoolVar(&portForward, "port-forward", true, "Auto-start kubectl port-forward to Alertmanager when no URL is provided")
	cmd.Flags().StringVar(&cluster, "cluster", "", "Cluster name (shown in the TUI header)")

	return cmd
}

type runAlertsOpts struct {
	Severity        string
	Source          string
	Namespace       string
	Output          string
	Refresh         int
	AlertmanagerURL string
	PortForward     bool
	Cluster         string
}

func runAlerts(opts runAlertsOpts) error {
	// Resolve Alertmanager endpoint.
	url := opts.AlertmanagerURL
	if url == "" {
		url = os.Getenv("ALERTMANAGER_URL")
	}

	var pf *alerts.PortForward
	if url == "" && opts.PortForward {
		pf = alerts.NewAlertmanagerPortForward()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := pf.Start(ctx); err != nil {
			return fmt.Errorf("port-forward to alertmanager failed: %w\n\nHint: set --alertmanager-url or pre-run\n  kubectl port-forward -n monitoring svc/prom-operator-alertmanager 9093:9093", err)
		}
		defer pf.Stop()
		url = pf.URL()
	}
	if url == "" {
		url = "http://localhost:9093"
	}

	client := alerts.NewAlertmanagerClient(url)

	// Non-interactive output formats: fetch, filter, print.
	if opts.Output == "json" || opts.Output == "table" || opts.Output == "list" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		alertList, err := client.GetAlerts(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch alerts: %w", err)
		}
		filtered := alerts.ApplyFilter(alertList, alerts.FilterSpec{
			Severity:  opts.Severity,
			Source:    opts.Source,
			Namespace: opts.Namespace,
		})
		alerts.SortAlerts(filtered)
		if opts.Output == "json" {
			return outputJSON(filtered)
		}
		return outputTable(filtered)
	}

	// TUI mode.
	model := alertstui.NewModel(client, opts.Cluster, pf)
	if opts.Severity != "" {
		model.SetSeverity(opts.Severity)
	}
	if opts.Namespace != "" {
		model.SetNamespace(opts.Namespace)
	}
	if opts.Source != "" {
		model.SetSource(opts.Source)
	}

	// v2: alt-screen + mouse-mode are declared on the model's tea.View
	// (alertstui.Model.View), not as NewProgram options.
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("failed to run TUI: %w", err)
	}
	return nil
}

func outputJSON(alertList []alerts.Alert) error {
	data, err := json.MarshalIndent(alertList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alerts: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func outputTable(alertList []alerts.Alert) error {
	if len(alertList) == 0 {
		fmt.Println("No alerts found")
		return nil
	}

	// Print header
	fmt.Printf("%-40s %-10s %-20s %-10s\n", "Alert Name", "Severity", "Source", "Age")
	fmt.Println(strings.Repeat("-", 80))

	// Print alerts
	for _, a := range alertList {
		source := a.Labels["job"]
		if source == "" {
			source = a.Labels["namespace"]
		}
		if source == "" {
			source = a.AlertName
		}
		age := time.Since(a.StartsAt).Round(time.Minute)
		fmt.Printf("%-40s %-10s %-20s %-10s\n", a.AlertName, a.Severity, source, age)
	}

	fmt.Printf("\nTotal: %d alerts\n", len(alertList))
	return nil
}

// verifyRealmExists checks that the Keycloak realm backing an organization is
// actually there, so a wrong org name fails here with an explanation instead of
// as a bare 404 in the user's browser.
//
// Deliberately fails OPEN on anything that is not a definitive 404: a proxy, a
// captive portal, an offline resolver or a self-signed chain must not be able to
// block a login that would otherwise have worked. Only Keycloak explicitly
// saying "no such realm" stops us.
func verifyRealmExists(keycloakURL, realm, caCertPEM string) error {
	transport := &http.Transport{}
	if caCertPEM != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(caCertPEM)) {
			transport.TLSClientConfig = &tls.Config{RootCAs: pool}
		}
	}
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport}

	url := fmt.Sprintf("%s/realms/%s/.well-known/openid-configuration",
		strings.TrimSuffix(keycloakURL, "/"), realm)
	resp, err := client.Get(url)
	if err != nil {
		return nil // unreachable / TLS problem — let the normal flow surface it
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		return nil
	}
	return fmt.Errorf(`organization %q does not exist on this cluster

Keycloak has no realm %q, so the browser would just show a 404.

`+"`kube-dc login`"+` wants the ORGANIZATION, which is not the same as the
Project you work in. Project backing namespaces are named <org>-<project>, so if you
work in something like "acme-web" the organization is usually "acme".

  - the Organization is the top-level identity boundary (the Keycloak realm)
  - the Project lives inside it and is selected AFTER login, with:
        kube-dc use
        kube-dc use <domain>/<org>/<project>

Check the organization name in the console, or with:
    kubectl get organizations -A`, realm, realm)
}
