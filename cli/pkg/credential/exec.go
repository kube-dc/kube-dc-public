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

// GetCredential returns a valid access token for the given server
// It handles token refresh automatically
func (p *Provider) GetCredential(server string) (*ExecCredential, error) {
	creds, err := p.credMgr.Load(server)
	if err != nil {
		domain := extractDomain(server)
		return nil, fmt.Errorf("not logged in. Run: kube-dc login --domain %s --org <your-org>", domain)
	}

	// Check if access token is still valid
	if creds.IsAccessTokenValid() {
		return p.buildExecCredential(creds.AccessToken, creds.AccessTokenExpiry), nil
	}

	// Access token expired, try to refresh
	// Always try to refresh - let Keycloak be the source of truth for token validity
	if creds.RefreshToken == "" {
		domain := extractDomain(server)
		return nil, fmt.Errorf("session expired (no refresh token). Run: kube-dc login --domain %s --org %s", domain, creds.Realm)
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
		// Refresh failed - session is truly expired
		domain := extractDomain(server)
		return nil, fmt.Errorf("session expired. Run: kube-dc login --domain %s --org %s", domain, creds.Realm)
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
