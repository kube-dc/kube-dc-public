package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	rootCmd := &cobra.Command{
		Use:   "kube-dc",
		Short: "Kube-DC CLI - Kubernetes authentication for Kube-DC clusters",
		Long: `Kube-DC CLI provides browser-based authentication for Kube-DC clusters.

It follows the same patterns as AWS CLI, GCloud, and other cloud provider CLIs:
- Browser-based OAuth login
- Automatic token refresh
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
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
  --org <name>   Tenant login against the per-org realm. Writes contexts
                 named kube-dc/<domain>/<org>/<project>, one per namespace
                 the user has access to. RBAC is namespace-scoped.

  --admin        Platform-admin login against the master realm and the
                 'kube-dc-admin' OIDC client. Writes a single context
                 named kube-dc/<domain>/admin with cluster-wide RBAC
                 (via the platform:kube-dc-admin group claim).`,
		Example: `  # Tenant login (existing behavior)
  kube-dc login --domain stage.kube-dc.com --org shalb

  # Platform-admin login (new)
  kube-dc login --domain kube-dc.cloud --admin

  # With CA certificate (for self-hosted)
  kube-dc login --domain internal.example.com --org myorg --ca-cert /path/to/ca.crt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if admin && org != "" {
				return fmt.Errorf("--admin and --org are mutually exclusive (admin always uses the master realm)")
			}
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
	cmd.Flags().BoolVar(&deviceCode, "device-code", false, "Use device code flow for headless environments")

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
		fmt.Print("Enter organization (Keycloak realm): ")
		input, _ := reader.ReadString('\n')
		org = strings.TrimSpace(input)
	}
	if org == "" {
		return fmt.Errorf("organization is required")
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
		return fmt.Errorf("device code flow not yet implemented")
	}

	fmt.Printf("\n🔐 Logging in to %s (org: %s)\n", domain, org)
	fmt.Printf("   API Server: %s\n", server)
	fmt.Printf("   Keycloak:   %s\n\n", keycloakURL)

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
		fmt.Printf("  Available namespaces: %s\n", strings.Join(claims.Namespaces, ", "))
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

	// Create contexts for each namespace
	// Include domain in names to support multiple kube-dc installations
	clusterName := fmt.Sprintf("kube-dc-%s-%s", domain, org)
	userName := fmt.Sprintf("kube-dc@%s/%s", domain, org)

	for i, ns := range claims.Namespaces {
		// Extract project name from namespace (org-project -> project)
		projectName := ns
		if strings.HasPrefix(ns, org+"-") {
			projectName = strings.TrimPrefix(ns, org+"-")
		}

		// Context name includes domain to distinguish between installations
		contextName := fmt.Sprintf("kube-dc/%s/%s/%s", domain, org, projectName)

		params := kubeconfig.AddContextParams{
			Server:      server,
			ClusterName: clusterName,
			UserName:    userName,
			ContextName: contextName,
			Namespace:   ns,
			CACert:      caCertPEM,
			Insecure:    insecure,
			SetCurrent:  i == 0, // Set first namespace as current
		}

		if err := kubeMgr.AddKubeDCContext(params); err != nil {
			fmt.Printf("  Warning: failed to add context %s: %v\n", contextName, err)
		}
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
		Use:   "use [org/project]",
		Short: "Switch to a different organization/project context",
		Long: `Switch to a different organization/project context.

If no argument is provided, lists available kube-dc contexts.`,
		Example: `  # Switch to specific project
  kube-dc use shalb/demo

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

	// Allow short form (org/project) or full form (kube-dc/org/project)
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
		Use:   "ns [namespace]",
		Short: "Switch or list namespaces",
		Long: `Switch or list namespaces from your access token.

If no argument is provided, shows available namespaces.
Namespaces are derived from your JWT token claims.`,
		Example: `  # List available namespaces
  kube-dc ns

  # Switch namespace
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

	// Load credentials to get available namespaces
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	creds, err := credMgr.Load(serverURL)
	if err != nil {
		return fmt.Errorf("not logged in. Run: kube-dc login --server %s", serverURL)
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
		return fmt.Errorf("no namespaces found in credentials")
	}

	// If no argument, list namespaces
	if len(args) == 0 {
		fmt.Println("Available namespaces:")
		for _, ns := range namespaces {
			marker := "  "
			if ns == currentNamespace {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, ns)
		}
		return nil
	}

	// Switch namespace
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
		return fmt.Errorf("namespace '%s' not in your allowed namespaces: %v", targetNs, namespaces)
	}

	// Update kubeconfig
	if err := kubeMgr.SetNamespace(targetNs); err != nil {
		return fmt.Errorf("failed to set namespace: %w", err)
	}

	fmt.Printf("Switched to namespace: %s\n", targetNs)
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

	// Find current namespace
	for _, ctx := range kubeConfig.Contexts {
		if ctx.Name == kubeConfig.CurrentContext {
			fmt.Printf("Namespace: %s\n", ctx.Context.Namespace)
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
		fmt.Printf("  Namespaces: %v\n", c.User.Namespaces)

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

			provider, err := credential.NewProvider()
			if err != nil {
				return err
			}

			// Realm-aware lookup when the kubeconfig context provides
			// it (admin contexts always do). Tenant kubeconfigs that
			// pre-date this change call without --realm and hit the
			// legacy single-file fallback in credentials.Manager.
			cred, err := provider.GetCredentialForRealm(server, realm)
			if err != nil {
				return err
			}

			return cred.Output()
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "Kube-DC API server URL")
	cmd.Flags().StringVar(&realm, "realm", "", "Keycloak realm (admin: master; tenant: org name). Optional; legacy kubeconfigs omit this.")
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

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
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
