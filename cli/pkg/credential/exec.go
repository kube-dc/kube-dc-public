package credential

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/config"
	"github.com/shalb/kube-dc/cli/internal/jwt"
)

// reloginCmd returns a copy-pasteable login command for the given
// server + realm pair. master realm → admin login; everything else →
// tenant login. Used in error messages so the operator's next move is
// always the right one for the identity that just expired.
func reloginCmd(server, realm string) string {
	domain := extractDomain(server)
	if realm == "master" {
		return fmt.Sprintf("kube-dc login --domain %s --admin", domain)
	}
	if realm != "" {
		return fmt.Sprintf("kube-dc login --domain %s --org %s", domain, realm)
	}
	return fmt.Sprintf("kube-dc login --domain %s --org <your-org>", domain)
}

// notLoggedInErr is the consistent "no creds for this (server, realm)"
// surface every Load path bubbles up.
func notLoggedInErr(server, realm string) error {
	return fmt.Errorf("not logged in. Run: %s", reloginCmd(server, realm))
}

// extractDomain extracts the domain from a server URL
// e.g., https://kube-api.stage.kube-dc.com:6443 -> stage.kube-dc.com
func extractDomain(server string) string {
	re := regexp.MustCompile(`https?://kube-api\.([^:/]+)`)
	matches := re.FindStringSubmatch(server)
	if len(matches) > 1 {
		return matches[1]
	}
	// Fallback: try to extract domain without kube-api prefix
	re = regexp.MustCompile(`https?://([^:/]+)`)
	matches = re.FindStringSubmatch(server)
	if len(matches) > 1 {
		return matches[1]
	}
	return server
}

// ExecCredential represents the Kubernetes ExecCredential response
type ExecCredential struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Status     ExecCredentialStatus `json:"status"`
}

// ExecCredentialStatus contains the token and expiration
type ExecCredentialStatus struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp,omitempty"`
}

// Provider handles credential provisioning for kubectl
type Provider struct {
	credMgr *config.CredentialsManager
}

// NewProvider creates a new credential provider
func NewProvider() (*Provider, error) {
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		return nil, err
	}
	return &Provider{credMgr: credMgr}, nil
}

// GetCredential returns a valid access token for the given server. It
// finds whichever realm is cached for that server (legacy single-file
// path or any realm-suffixed file). New code that knows the realm
// should call GetCredentialForRealm so multiple identities cached for
// the same cluster (tenant + admin) don't collide.
func (p *Provider) GetCredential(server string) (*ExecCredential, error) {
	return p.getCredential(server, "")
}

// GetCredentialForRealm is the realm-aware variant invoked by exec
// plugins whose kubeconfig context includes `--realm <name>` in args.
func (p *Provider) GetCredentialForRealm(server, realm string) (*ExecCredential, error) {
	return p.getCredential(server, realm)
}

func (p *Provider) getCredential(server, realm string) (*ExecCredential, error) {
	creds, err := p.credMgr.LoadForRealm(server, realm)
	if err != nil {
		return nil, notLoggedInErr(server, realm)
	}

	// Check if access token is still valid
	if creds.IsAccessTokenValid() {
		return p.buildExecCredential(creds.AccessToken, creds.AccessTokenExpiry), nil
	}

	// Access token expired, try to refresh
	// Always try to refresh - let Keycloak be the source of truth for token validity
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("session expired (no refresh token). Run: %s", reloginCmd(server, creds.Realm))
	}

	// Refresh the token
	newTokens, err := auth.RefreshToken(
		creds.KeycloakURL,
		creds.Realm,
		creds.ClientID,
		creds.RefreshToken,
		creds.CACert,
		creds.Insecure,
	)
	if err != nil {
		return nil, fmt.Errorf("session expired. Run: %s", reloginCmd(server, creds.Realm))
	}

	// Parse the new access token to get expiry and user info
	claims, err := jwt.ParseToken(newTokens.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Update cached credentials
	creds.AccessToken = newTokens.AccessToken
	creds.RefreshToken = newTokens.RefreshToken
	creds.AccessTokenExpiry = claims.ExpiryTime()
	// For offline tokens, RefreshExpiresIn is 0 which means "never expires"
	// We set a reasonable default of 30 days (standard for CLI tools)
	if newTokens.RefreshExpiresIn <= 0 {
		creds.RefreshTokenExpiry = time.Now().Add(30 * 24 * time.Hour)
	} else {
		creds.RefreshTokenExpiry = time.Now().Add(time.Duration(newTokens.RefreshExpiresIn) * time.Second)
	}
	if newTokens.IDToken != "" {
		creds.IDToken = newTokens.IDToken
	}

	// Update user info from new token
	creds.User.Email = claims.Email
	creds.User.Org = claims.Org
	creds.User.Groups = claims.Groups
	creds.User.Namespaces = claims.Namespaces

	if err := p.credMgr.Save(creds); err != nil {
		// Log warning but don't fail - we still have a valid token
		fmt.Printf("Warning: failed to cache refreshed credentials: %v\n", err)
	}

	return p.buildExecCredential(creds.AccessToken, creds.AccessTokenExpiry), nil
}

// buildExecCredential creates the ExecCredential JSON structure
func (p *Provider) buildExecCredential(token string, expiry time.Time) *ExecCredential {
	return &ExecCredential{
		APIVersion: "client.authentication.k8s.io/v1",
		Kind:       "ExecCredential",
		Status: ExecCredentialStatus{
			Token:               token,
			ExpirationTimestamp: expiry.UTC().Format(time.RFC3339),
		},
	}
}

// Output prints the ExecCredential as JSON to stdout
func (e *ExecCredential) Output() error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to marshal credential: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
