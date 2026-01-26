package jwt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims represents the JWT claims from a Kube-DC token
type Claims struct {
	// Standard claims
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  any    `json:"aud"` // Can be string or []string
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`

	// Kube-DC specific claims
	Email      string   `json:"email"`
	Org        string   `json:"org"`
	Groups     []string `json:"groups"`
	Namespaces []string `json:"namespaces"`

	// Keycloak claims
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
}

// ParseToken parses a JWT token and extracts the claims
// Note: This does NOT verify the signature - use only for extracting claims
// The Kubernetes API server will verify the token signature
func ParseToken(tokenString string) (*Claims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Try with padding
		payload, err = base64.StdEncoding.DecodeString(addPadding(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("failed to decode token payload: %w", err)
		}
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse token claims: %w", err)
	}

	return &claims, nil
}

// IsExpired checks if the token is expired
func (c *Claims) IsExpired() bool {
	return time.Now().Unix() >= c.ExpiresAt
}

// ExpiresIn returns the duration until the token expires
func (c *Claims) ExpiresIn() time.Duration {
	expiresAt := time.Unix(c.ExpiresAt, 0)
	return time.Until(expiresAt)
}

// ExpiryTime returns the token expiry time
func (c *Claims) ExpiryTime() time.Time {
	return time.Unix(c.ExpiresAt, 0)
}

// GetAudience returns the audience as a string slice
func (c *Claims) GetAudience() []string {
	switch v := c.Audience.(type) {
	case string:
		return []string{v}
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, a := range v {
			if s, ok := a.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

// addPadding adds base64 padding if needed
func addPadding(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	default:
		return s
	}
}
