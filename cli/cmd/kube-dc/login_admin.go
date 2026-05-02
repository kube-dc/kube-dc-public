package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/config"
	"github.com/shalb/kube-dc/cli/internal/jwt"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

// adminRealm + adminClientID + adminGroup match what the fleet-side
// bootstrap creates in the master realm (see PRD §16.5 + the
// kube-dc-fleet/bootstrap/setup-keycloak-oidc.sh extension). Hard-coded
// here because the CLI doesn't see the cluster's auth-config; if any
// of these names ever changes both sides have to move together.
//
// We reuse the existing master-realm `admin` group (created by
// setup-keycloak-oidc.sh today and already used to gate flux-web /
// grafana SSO). On the apiserver side, a claim-mapping prefix turns
// the JWT's group `admin` into the K8s Group `platform:admin`, which
// the cluster-admin ClusterRoleBinding subjects on. That prefix is
// what stops a tenant realm's `admin` group from accidentally getting
// cluster-wide RBAC.
const (
	adminRealm    = "master"
	adminClientID = "kube-dc-admin"
	adminGroup    = "admin"
)

// runAdminLogin authenticates the operator against the master realm's
// kube-dc-admin OIDC client and writes a single cluster-scope context
// named kube-dc/<domain>/admin into ~/.kube/config.
//
// Flow:
//  1. PKCE OAuth against https://login.<domain>/realms/master.
//  2. Parse the access token's claims; verify the user is a member of
//     the kube-dc-admin Keycloak group. (The API server enforces RBAC
//     too, but failing here gives a much better error than kubectl's
//     "forbidden" later on.)
//  3. Cache tokens at ~/.kube-dc/credentials/<server>-master.json so
//     they coexist with any tenant credentials the operator already has
//     for the same cluster.
//  4. Merge a single new context into ~/.kube/config via
//     kubeconfig.AddKubeDCContext with Realm=master so the exec plugin
//     loads the right cache entry on each kubectl invocation.
func runAdminLogin(domain, caCertFile string, insecure, deviceCode bool) error {
	if domain == "" {
		return fmt.Errorf("--domain is required for --admin login")
	}
	if deviceCode {
		return fmt.Errorf("device code flow not yet implemented for --admin")
	}

	server := fmt.Sprintf("https://kube-api.%s:6443", domain)
	keycloakURL := fmt.Sprintf("https://login.%s", domain)

	var caCertPEM string
	if caCertFile != "" {
		b, err := os.ReadFile(caCertFile)
		if err != nil {
			return fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPEM = string(b)
		fmt.Printf("Using CA certificate from %s\n", caCertFile)
	}

	fmt.Printf("\n🔐 Admin login to %s (realm: %s, client: %s)\n", domain, adminRealm, adminClientID)
	fmt.Printf("   API Server: %s\n", server)
	fmt.Printf("   Keycloak:   %s\n\n", keycloakURL)

	flow := auth.NewOAuthFlow(&auth.OAuthConfig{
		KeycloakURL: keycloakURL,
		Realm:       adminRealm,
		ClientID:    adminClientID,
		CACert:      caCertPEM,
		Insecure:    insecure,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tokens, err := flow.Login(ctx)
	if err != nil {
		return fmt.Errorf("admin login failed: %w", err)
	}
	fmt.Println("\n✓ Authentication successful!")

	claims, err := jwt.ParseToken(tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("failed to parse access token: %w", err)
	}
	if !hasAdminGroup(claims.Groups, adminGroup) {
		return fmt.Errorf(
			"user %q is authenticated but NOT in the %q Keycloak group — admin context not written.\n"+
				"Ask a Keycloak admin to add you to the master realm's %q group, then retry.\n"+
				"(JWT groups present: %v)",
			claims.Email, adminGroup, adminGroup, claims.Groups)
	}

	fmt.Printf("  User:     %s\n", claims.Email)
	fmt.Printf("  Groups:   %s\n", strings.Join(claims.Groups, ", "))

	// Persist tokens to ~/.kube-dc/credentials/ keyed by (server, master)
	// so they don't clobber any tenant-realm tokens for the same cluster.
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return fmt.Errorf("init credentials manager: %w", err)
	}

	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)
	if tokens.RefreshExpiresIn > 0 {
		refreshExpiry = time.Now().Add(time.Duration(tokens.RefreshExpiresIn) * time.Second)
	}

	creds := &config.Credentials{
		Server:             server,
		KeycloakURL:        keycloakURL,
		Realm:              adminRealm,
		ClientID:           adminClientID,
		AccessToken:        tokens.AccessToken,
		RefreshToken:       tokens.RefreshToken,
		IDToken:            tokens.IDToken,
		AccessTokenExpiry:  claims.ExpiryTime(),
		RefreshTokenExpiry: refreshExpiry,
		User: config.UserInfo{
			Email:  claims.Email,
			Groups: claims.Groups,
			// Org/Namespaces intentionally empty for admin identity:
			// admin is cluster-scoped, no per-namespace context fan-out.
		},
		CACert:   caCertPEM,
		Insecure: insecure,
	}
	if err := credMgr.Save(creds); err != nil {
		return fmt.Errorf("failed to save admin credentials: %w", err)
	}
	fmt.Printf("  Cached:   ~/.kube-dc/credentials/ (realm=%s)\n", adminRealm)

	// Write a single cluster-scope context into ~/.kube/config. Naming
	// stays consistent with tenant contexts (kube-dc/<domain>/...) so
	// kubectx and the future bootstrap-context TUI can list them
	// uniformly.
	//
	// Footgun guard: if $KUBECONFIG is set to a non-default path, the
	// admin context lands THERE, not in ~/.kube/config — and kubectx
	// (which reads ~/.kube/config by default) won't see it. This bit
	// us once when an `export KUBECONFIG=...cloud_kubeconfig_tunnel`
	// from a fleet bootstrap step leaked into a follow-up
	// `kube-dc login --admin`. Surface the destination explicitly so
	// the operator can confirm or abort.
	if err := warnIfNonDefaultKubeconfig(); err != nil {
		return err
	}
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return fmt.Errorf("init kubeconfig manager: %w", err)
	}
	contextName := fmt.Sprintf("kube-dc/%s/admin", domain)
	clusterName := fmt.Sprintf("kube-dc-%s-admin", domain)
	userName := fmt.Sprintf("kube-dc-admin@%s", domain)

	if err := kubeMgr.AddKubeDCContext(kubeconfig.AddContextParams{
		Server:      server,
		ClusterName: clusterName,
		UserName:    userName,
		ContextName: contextName,
		// No Namespace: admin is cluster-scoped.
		CACert:     caCertPEM,
		Insecure:   insecure,
		SetCurrent: true,
		Realm:      adminRealm, // exec plugin will use --realm master
	}); err != nil {
		return fmt.Errorf("failed to update kubeconfig: %w", err)
	}

	fmt.Println("  Kubeconfig updated in ~/.kube/config")
	fmt.Println()
	fmt.Printf("🚀 You are now cluster-admin on %s. Current context: %s\n", domain, contextName)
	fmt.Println()
	fmt.Println("Try: kubectl get nodes")
	return nil
}

// hasAdminGroup is a tolerant membership check. Keycloak emits group
// names with a leading "/" by default; the apiserver's claim mapping
// prepends "platform:" so the K8s RBAC subject is "platform:admin".
// The CLI sees the *raw* token (pre-claim-mapping), so it may show:
//   - "admin"             (full.path: false on the mapper)
//   - "/admin"            (full.path: true, default Keycloak behavior)
//   - "platform:admin"    (already-mapped, e.g. from a re-issued token)
//   - "platform:/admin"
// Accept all four so the CLI doesn't gatekeep on a stylistic
// difference between realms or claim mappings.
func hasAdminGroup(groups []string, want string) bool {
	for _, g := range groups {
		switch g {
		case want,
			"/" + want,
			"platform:" + want,
			"platform:/" + want:
			return true
		}
	}
	return false
}
