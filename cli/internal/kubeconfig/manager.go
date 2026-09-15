package kubeconfig

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents a kubeconfig file structure
type Config struct {
	APIVersion     string         `yaml:"apiVersion"`
	Kind           string         `yaml:"kind"`
	CurrentContext string         `yaml:"current-context"`
	Clusters       []NamedCluster `yaml:"clusters"`
	Contexts       []NamedContext `yaml:"contexts"`
	Users          []NamedUser    `yaml:"users"`
	Preferences    map[string]any `yaml:"preferences,omitempty"`
	Extra          map[string]any `yaml:",inline"`
}

// NamedCluster represents a cluster entry in kubeconfig
type NamedCluster struct {
	Name    string         `yaml:"name"`
	Cluster Cluster        `yaml:"cluster"`
	Extra   map[string]any `yaml:",inline"`
}

// Cluster contains cluster connection information
type Cluster struct {
	Server                   string         `yaml:"server"`
	CertificateAuthority     string         `yaml:"certificate-authority,omitempty"`
	CertificateAuthorityData string         `yaml:"certificate-authority-data,omitempty"`
	InsecureSkipTLSVerify    bool           `yaml:"insecure-skip-tls-verify,omitempty"`
	TLSServerName            string         `yaml:"tls-server-name,omitempty"`
	ProxyURL                 string         `yaml:"proxy-url,omitempty"`
	Extra                    map[string]any `yaml:",inline"`
}

// NamedContext represents a context entry in kubeconfig
type NamedContext struct {
	Name    string         `yaml:"name"`
	Context Context        `yaml:"context"`
	Extra   map[string]any `yaml:",inline"`
}

// Context contains context settings
type Context struct {
	Cluster   string         `yaml:"cluster"`
	User      string         `yaml:"user"`
	Namespace string         `yaml:"namespace,omitempty"`
	Extra     map[string]any `yaml:",inline"`
}

// NamedUser represents a user entry in kubeconfig
type NamedUser struct {
	Name  string         `yaml:"name"`
	User  User           `yaml:"user"`
	Extra map[string]any `yaml:",inline"`
}

// User contains user authentication information
type User struct {
	Exec  *ExecConfig    `yaml:"exec,omitempty"`
	Extra map[string]any `yaml:",inline"`
}

// ExecConfig contains exec credential plugin configuration
type ExecConfig struct {
	APIVersion      string         `yaml:"apiVersion"`
	Command         string         `yaml:"command"`
	Args            []string       `yaml:"args,omitempty"`
	InteractiveMode string         `yaml:"interactiveMode,omitempty"`
	Extra           map[string]any `yaml:",inline"`
}

// Manager handles kubeconfig file operations
type Manager struct {
	path string
}

// NewManager creates a new kubeconfig manager
func NewManager() (*Manager, error) {
	// Match kubectl's write destination: the first non-empty path in the list.
	var path string
	for _, candidate := range filepath.SplitList(os.Getenv("KUBECONFIG")) {
		if candidate != "" {
			path = candidate
			break
		}
	}
	if path == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(homeDir, ".kube", "config")
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	return &Manager{path: path}, nil
}

// Path returns the kubeconfig file path the manager will read from /
// write to. Useful for surfacing in the TUI ("Kube-DC Contexts —
// ~/.kube/config").
func (m *Manager) Path() string { return m.path }

// ClusterConnection returns settings only for the exact named API endpoint.
func (m *Manager) ClusterConnection(clusterName, server string) (Cluster, error) {
	config, err := m.Load()
	if err != nil {
		return Cluster{}, err
	}
	for _, named := range config.Clusters {
		if named.Name != clusterName || named.Cluster.Server != server {
			continue
		}
		return named.Cluster, nil
	}
	return Cluster{}, nil
}

// ClusterCA returns only trust already configured for this exact API endpoint.
// Relative certificate file paths resolve beside the kubeconfig that owns them.
func (m *Manager) ClusterCA(clusterName, server string) (string, error) {
	cluster, err := m.ClusterConnection(clusterName, server)
	if err != nil {
		return "", err
	}
	if cluster.CertificateAuthorityData != "" {
		data, err := base64.StdEncoding.DecodeString(cluster.CertificateAuthorityData)
		if err != nil {
			return "", fmt.Errorf("decode CA for cluster %q: %w", clusterName, err)
		}
		return string(data), nil
	}
	if cluster.CertificateAuthority != "" {
		path := cluster.CertificateAuthority
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(m.path), path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read CA for cluster %q: %w", clusterName, err)
		}
		return string(data), nil
	}
	return "", nil
}

// Load loads the kubeconfig file
func (m *Manager) Load() (*Config, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config
			return &Config{
				APIVersion: "v1",
				Kind:       "Config",
				Clusters:   []NamedCluster{},
				Contexts:   []NamedContext{},
				Users:      []NamedUser{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	return &config, nil
}

// Save saves the kubeconfig file
func (m *Manager) Save(config *Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal kubeconfig: %w", err)
	}

	if err := os.WriteFile(m.path, data, 0600); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	return nil
}

// AddKubeDCContext adds or updates a Kube-DC context in the kubeconfig
// It preserves all non-Kube-DC entries
func (m *Manager) AddKubeDCContext(params AddContextParams) error {
	if params.UseSystemCA && params.CACert != "" {
		return fmt.Errorf("cannot set both a CA bundle and system CA trust")
	}
	config, err := m.Load()
	if err != nil {
		return err
	}

	clusterName := params.ClusterName
	userName := params.UserName
	contextName := params.ContextName

	// Encode CA cert to base64 if provided.
	//
	// NOT when verification is explicitly waived: client-go rejects a cluster
	// that carries both certificate-authority-data and
	// insecure-skip-tls-verify, so writing both produces a kubeconfig that
	// fails to load at all. --insecure is the stronger, explicit instruction,
	// so it wins and the CA is omitted.
	var caCertBase64 string
	if params.CACert != "" && !params.Insecure {
		caCertBase64 = base64.StdEncoding.EncodeToString([]byte(params.CACert))
	}

	// TLS verification is skipped ONLY when the caller explicitly asked for it.
	//
	// This used to read `params.Insecure || (params.CACert == "" && !params.Insecure)`,
	// which is true whenever no CA is supplied — and an empty CA is the NORMAL,
	// intended case for a cluster with a publicly-trusted certificate ("rely on
	// the system trust store"). The effect was that essentially every kubeconfig
	// this writes, for operators and tenants alike, carried
	// insecure-skip-tls-verify: true. Nobody asked for that, nothing surfaced
	// it, and it silently removed authentication of the API server from every
	// kubectl call the product hands out.
	//
	// With an empty CA and this false, client-go verifies against the system
	// roots, which is correct for a public certificate and correctly FAILS for
	// a private CA whose bundle was not supplied — the operator then passes the
	// CA (see --trusted-ca-bundle / the private-CA install path) or opts in
	// explicitly with --insecure.
	skipTLS := params.Insecure

	// Add or update cluster
	clusterFound := false
	for i, c := range config.Clusters {
		if c.Name == clusterName {
			cluster := Cluster{
				Server:                   params.Server,
				CertificateAuthorityData: caCertBase64,
				InsecureSkipTLSVerify:    skipTLS,
			}
			if c.Cluster.Server == params.Server {
				cluster.Extra = c.Cluster.Extra
				cluster.TLSServerName = c.Cluster.TLSServerName
				cluster.ProxyURL = c.Cluster.ProxyURL
			}
			// Re-authentication does not change trust for an existing API
			// endpoint. Omitting --ca-cert must not erase a private CA already
			// configured for it. Explicit CA replacement or --insecure wins;
			// never carry a CA over when the server address changes.
			if params.CACert == "" && !params.UseSystemCA && !params.Insecure && c.Cluster.Server == params.Server {
				cluster.CertificateAuthority = c.Cluster.CertificateAuthority
				cluster.CertificateAuthorityData = c.Cluster.CertificateAuthorityData
			}
			config.Clusters[i].Cluster = cluster
			clusterFound = true
			break
		}
	}
	if !clusterFound {
		config.Clusters = append(config.Clusters, NamedCluster{
			Name: clusterName,
			Cluster: Cluster{
				Server:                   params.Server,
				CertificateAuthorityData: caCertBase64,
				InsecureSkipTLSVerify:    skipTLS,
			},
		})
	}

	// Add or update user with exec credential plugin
	userFound := false
	for i, u := range config.Users {
		if u.Name == userName {
			config.Users[i].User = User{
				Exec: &ExecConfig{
					APIVersion:      "client.authentication.k8s.io/v1",
					Command:         "kube-dc",
					Args:            execArgs(params),
					InteractiveMode: "IfAvailable",
				},
			}
			userFound = true
			break
		}
	}
	if !userFound {
		config.Users = append(config.Users, NamedUser{
			Name: userName,
			User: User{
				Exec: &ExecConfig{
					APIVersion:      "client.authentication.k8s.io/v1",
					Command:         "kube-dc",
					Args:            execArgs(params),
					InteractiveMode: "IfAvailable",
				},
			},
		})
	}

	// Add or update context
	contextFound := false
	for i, c := range config.Contexts {
		if c.Name == contextName {
			config.Contexts[i].Context = Context{
				Cluster:   clusterName,
				User:      userName,
				Namespace: params.Namespace,
				Extra:     c.Context.Extra,
			}
			contextFound = true
			break
		}
	}
	if !contextFound {
		config.Contexts = append(config.Contexts, NamedContext{
			Name: contextName,
			Context: Context{
				Cluster:   clusterName,
				User:      userName,
				Namespace: params.Namespace,
			},
		})
	}

	// Set as current context if requested
	if params.SetCurrent {
		config.CurrentContext = contextName
	}

	return m.Save(config)
}

// AddContextParams contains parameters for adding a context
type AddContextParams struct {
	Server      string
	ClusterName string
	UserName    string
	ContextName string
	Namespace   string
	CACert      string // PEM-encoded CA certificate
	UseSystemCA bool   // Explicitly clear old CA data/files instead of preserving them
	Insecure    bool   // Skip TLS verification
	SetCurrent  bool

	// Realm, when non-empty, is passed to the exec plugin so it can
	// pick the right cached credentials when more than one identity is logged
	// in to the same cluster (Organization + platform admin). Older Organization
	// logins omitted this; a fresh login updates their shared user entry, while
	// kubeconfigs backed by the legacy single-file cache keep working.
	Realm string
}

// execArgs builds the args slice for the kubeconfig user's exec plugin.
// Layout: ["credential", "--server", <url>, "--realm", <realm>?].
// Older Organization logins omitted --realm; kubeconfigs backed by the legacy
// single-file cache keep working via credentials.Manager's fallback.
func execArgs(p AddContextParams) []string {
	args := []string{"credential", "--server", p.Server}
	if p.Realm != "" {
		args = append(args, "--realm", p.Realm)
	}
	return args
}

// RemoveContext deletes a single named context from the kubeconfig and
// also drops the cluster and user entries it referenced **only if** no
// other surviving context still uses them. This is the surgical
// counterpart to RemoveKubeDCContexts, matching what an operator
// expects from `kx -d`: hit one row, lose one row.
//
// If the deleted context was current-context, current-context is
// cleared (operator can switch with `kube-dc bootstrap context` or
// `kx`).
//
// Returns nil if the context didn't exist (idempotent).
func (m *Manager) RemoveContext(name string) error {
	config, err := m.Load()
	if err != nil {
		return err
	}

	// Find the doomed context and remember which cluster + user it
	// pointed at so we can decide whether they're now orphaned.
	var (
		victim      Context
		victimFound bool
		newContexts = make([]NamedContext, 0, len(config.Contexts))
	)
	for _, c := range config.Contexts {
		if c.Name == name {
			victim = c.Context
			victimFound = true
			continue
		}
		newContexts = append(newContexts, c)
	}
	if !victimFound {
		return nil // already gone
	}
	config.Contexts = newContexts

	// Cluster GC: keep it iff some surviving context still points at it.
	if victim.Cluster != "" && !clusterStillReferenced(victim.Cluster, config.Contexts) {
		filtered := make([]NamedCluster, 0, len(config.Clusters))
		for _, cl := range config.Clusters {
			if cl.Name != victim.Cluster {
				filtered = append(filtered, cl)
			}
		}
		config.Clusters = filtered
	}

	// User GC: same rule.
	if victim.User != "" && !userStillReferenced(victim.User, config.Contexts) {
		filtered := make([]NamedUser, 0, len(config.Users))
		for _, u := range config.Users {
			if u.Name != victim.User {
				filtered = append(filtered, u)
			}
		}
		config.Users = filtered
	}

	// If we just deleted the current context, clear it. Picking a
	// "next" context automatically would surprise the operator;
	// silence is a safer default and `kube-dc bootstrap context`
	// makes switching trivial.
	if config.CurrentContext == name {
		config.CurrentContext = ""
	}

	return m.Save(config)
}

func clusterStillReferenced(cluster string, contexts []NamedContext) bool {
	for _, c := range contexts {
		if c.Context.Cluster == cluster {
			return true
		}
	}
	return false
}

func userStillReferenced(user string, contexts []NamedContext) bool {
	for _, c := range contexts {
		if c.Context.User == user {
			return true
		}
	}
	return false
}

// RemoveKubeDCContexts removes all Kube-DC contexts for a server
func (m *Manager) RemoveKubeDCContexts(server string) error {
	config, err := m.Load()
	if err != nil {
		return err
	}

	// Find clusters matching the server
	var clusterNames []string
	var newClusters []NamedCluster
	for _, c := range config.Clusters {
		if c.Cluster.Server == server && strings.HasPrefix(c.Name, "kube-dc-") {
			clusterNames = append(clusterNames, c.Name)
		} else {
			newClusters = append(newClusters, c)
		}
	}
	config.Clusters = newClusters

	// Remove users and contexts for those clusters
	var newUsers []NamedUser
	for _, u := range config.Users {
		if !strings.HasPrefix(u.Name, "kube-dc@") {
			newUsers = append(newUsers, u)
		}
	}
	config.Users = newUsers

	var newContexts []NamedContext
	for _, c := range config.Contexts {
		if !strings.HasPrefix(c.Name, "kube-dc/") {
			newContexts = append(newContexts, c)
		}
	}
	config.Contexts = newContexts

	// Reset current context if it was a Kube-DC context
	if strings.HasPrefix(config.CurrentContext, "kube-dc/") {
		if len(newContexts) > 0 {
			config.CurrentContext = newContexts[0].Name
		} else {
			config.CurrentContext = ""
		}
	}

	return m.Save(config)
}

// SetCurrentContext sets the current context
func (m *Manager) SetCurrentContext(contextName string) error {
	config, err := m.Load()
	if err != nil {
		return err
	}

	// Verify context exists
	found := false
	for _, c := range config.Contexts {
		if c.Name == contextName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("context %s not found", contextName)
	}

	config.CurrentContext = contextName
	return m.Save(config)
}

// SetNamespace sets the namespace for the current context
func (m *Manager) SetNamespace(namespace string) error {
	config, err := m.Load()
	if err != nil {
		return err
	}

	for i, c := range config.Contexts {
		if c.Name == config.CurrentContext {
			config.Contexts[i].Context.Namespace = namespace
			return m.Save(config)
		}
	}

	return fmt.Errorf("current context not found")
}

// ListKubeDCContexts lists all Kube-DC contexts
func (m *Manager) ListKubeDCContexts() ([]NamedContext, error) {
	config, err := m.Load()
	if err != nil {
		return nil, err
	}

	var contexts []NamedContext
	for _, c := range config.Contexts {
		if strings.HasPrefix(c.Name, "kube-dc/") {
			contexts = append(contexts, c)
		}
	}

	return contexts, nil
}
