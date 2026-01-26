package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials stores the cached authentication tokens for a server
type Credentials struct {
	Server              string    `json:"server"`
	KeycloakURL         string    `json:"keycloak_url"`
	Realm               string    `json:"realm"`
	ClientID            string    `json:"client_id"`
	AccessToken         string    `json:"access_token"`
	RefreshToken        string    `json:"refresh_token"`
	IDToken             string    `json:"id_token,omitempty"`
	AccessTokenExpiry   time.Time `json:"access_token_expiry"`
	RefreshTokenExpiry  time.Time `json:"refresh_token_expiry"`
	User                UserInfo  `json:"user"`
	CACert              string    `json:"ca_cert,omitempty"`
	Insecure            bool      `json:"insecure,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UserInfo contains user details extracted from the JWT token
type UserInfo struct {
	Email      string   `json:"email"`
	Org        string   `json:"org"`
	Groups     []string `json:"groups"`
	Namespaces []string `json:"namespaces"`
}

// CredentialsManager handles loading and saving credentials
type CredentialsManager struct {
	baseDir string
}

// NewCredentialsManager creates a new credentials manager
func NewCredentialsManager() (*CredentialsManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".kube-dc", "credentials")
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create credentials directory: %w", err)
	}

	return &CredentialsManager{baseDir: baseDir}, nil
}

// Load loads credentials for a server
func (m *CredentialsManager) Load(server string) (*Credentials, error) {
	filePath := m.credentialsFilePath(server)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no credentials found for %s", server)
		}
		return nil, fmt.Errorf("failed to read credentials: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	return &creds, nil
}

// Save saves credentials for a server
func (m *CredentialsManager) Save(creds *Credentials) error {
	creds.UpdatedAt = time.Now()
	if creds.CreatedAt.IsZero() {
		creds.CreatedAt = creds.UpdatedAt
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	filePath := m.credentialsFilePath(creds.Server)
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials: %w", err)
	}

	return nil
}

// Delete deletes credentials for a server
func (m *CredentialsManager) Delete(server string) error {
	filePath := m.credentialsFilePath(server)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}
	return nil
}

// List lists all saved credentials
func (m *CredentialsManager) List() ([]*Credentials, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list credentials: %w", err)
	}

	var creds []*Credentials
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(m.baseDir, entry.Name()))
		if err != nil {
			continue
		}

		var c Credentials
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		creds = append(creds, &c)
	}

	return creds, nil
}

// IsAccessTokenValid checks if the access token is still valid
func (c *Credentials) IsAccessTokenValid() bool {
	// Add 30 second buffer for clock skew
	return time.Now().Add(30 * time.Second).Before(c.AccessTokenExpiry)
}

// IsRefreshTokenValid checks if the refresh token is still valid
func (c *Credentials) IsRefreshTokenValid() bool {
	// Add 60 second buffer for clock skew
	return time.Now().Add(60 * time.Second).Before(c.RefreshTokenExpiry)
}

// credentialsFilePath returns the file path for a server's credentials
func (m *CredentialsManager) credentialsFilePath(server string) string {
	// Hash the server URL to create a safe filename
	hash := sha256.Sum256([]byte(server))
	filename := hex.EncodeToString(hash[:8]) + ".json"
	return filepath.Join(m.baseDir, filename)
}
