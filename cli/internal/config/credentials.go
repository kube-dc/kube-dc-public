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

// Load supports kubeconfigs that predate realm-aware credential files. It
// returns the legacy single-file entry when present. Realm-specific entries
// are never selected without an explicit realm, even when only one exists: an
// old Organization context must not silently consume a different identity.
// New callers must use LoadForRealm.
func (m *CredentialsManager) Load(server string) (*Credentials, error) {
	// Legacy path: single <server-hash>.json file (no realm in name).
	if c, err := readCredentials(m.legacyPath(server)); err == nil {
		return c, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Realm-specific files require an explicit realm, regardless of count.
	matches, err := m.realmMatches(server)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no credentials found for %s", server)
	}
	if len(matches) == 1 {
		// Exactly one identity is cached for this server, so there is nothing to
		// confuse it with. Refusing here would break every kubeconfig context
		// written before contexts carried --realm: those users would find each
		// kubectl call failing after a CLI upgrade until they logged in again,
		// which is a worse outcome than using the one credential that exists.
		return readCredentials(matches[0])
	}
	// Two or more identities (e.g. an Organization login and a platform-admin
	// login against the same cluster). Guessing would run kubectl as the wrong
	// one, so require the caller to say which.
	return nil, fmt.Errorf("credentials for %s exist for %d realms but this context names none; run kube-dc login again to pin this context to an identity", server, len(matches))
}

// LoadForRealm loads credentials for a specific (server, realm) pair.
// This is what the exec plugin uses when the kubeconfig context's exec
// args include `--realm <name>`. When realm == "" the call is
// equivalent to Load (legacy fallback first).
func (m *CredentialsManager) LoadForRealm(server, realm string) (*Credentials, error) {
	if realm == "" {
		return m.Load(server)
	}
	// New realm-aware path first.
	if c, err := readCredentials(m.realmPath(server, realm)); err == nil {
		return c, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Fallback to the legacy file ONLY if its embedded realm matches —
	// otherwise we'd hand a tenant token to an admin context.
	if c, err := readCredentials(m.legacyPath(server)); err == nil {
		if c.Realm == realm {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no credentials found for %s realm=%s", server, realm)
}

// Save saves credentials for a (server, realm) pair. Older callers that
// don't set creds.Realm continue to write the legacy filename so an
// existing operator's tenant kubeconfig keeps working through the
// upgrade.
func (m *CredentialsManager) Save(creds *Credentials) error {
	creds.UpdatedAt = time.Now()
	if creds.CreatedAt.IsZero() {
		creds.CreatedAt = creds.UpdatedAt
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	filePath := m.legacyPath(creds.Server)
	if creds.Realm != "" {
		filePath = m.realmPath(creds.Server, creds.Realm)
	}
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials: %w", err)
	}

	return nil
}

// Delete removes the legacy file plus every realm-specific file for the
// given server. Callers that want to delete just one realm should use
// DeleteForRealm.
func (m *CredentialsManager) Delete(server string) error {
	if err := os.Remove(m.legacyPath(server)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}
	matches, err := m.realmMatches(server)
	if err != nil {
		return err
	}
	for _, p := range matches {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete %s: %w", p, err)
		}
	}
	return nil
}

// DeleteForRealm removes a single (server, realm) credential file.
func (m *CredentialsManager) DeleteForRealm(server, realm string) error {
	path := m.realmPath(server, realm)
	if realm == "" {
		path = m.legacyPath(server)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
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

// legacyPath returns the pre-realm-aware credential path. Old logins
// landed here; we keep reading from it forever so an in-flight upgrade
// doesn't invalidate a working tenant kubeconfig on the operator's
// machine.
func (m *CredentialsManager) legacyPath(server string) string {
	return filepath.Join(m.baseDir, serverHash(server)+".json")
}

// realmPath is the new layout: <server-hash>-<realm>.json. The realm
// name is slug-cleaned to keep filenames safe across operating systems
// (master, shalb, demo-org, etc. are all fine; we strip anything else).
func (m *CredentialsManager) realmPath(server, realm string) string {
	return filepath.Join(m.baseDir, serverHash(server)+"-"+slugify(realm)+".json")
}

// realmMatches returns every realm-specific credential path for a
// server (does not include the legacy file).
func (m *CredentialsManager) realmMatches(server string) ([]string, error) {
	prefix := serverHash(server) + "-"
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read credentials dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			out = append(out, filepath.Join(m.baseDir, name))
		}
	}
	return out, nil
}

func serverHash(server string) string {
	h := sha256.Sum256([]byte(server))
	return hex.EncodeToString(h[:8])
}

// slugify keeps [a-zA-Z0-9._-] from the realm name. Realm names from
// Keycloak are already simple identifiers (master, shalb, demo) so this
// usually passes through unchanged.
func slugify(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.':
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}

// readCredentials is the shared decoder used by every Load* path.
func readCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &creds, nil
}
