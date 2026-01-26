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
	APIVersion     string           `yaml:"apiVersion"`
	Kind           string           `yaml:"kind"`
	CurrentContext string           `yaml:"current-context"`
	Clusters       []NamedCluster   `yaml:"clusters"`
	Contexts       []NamedContext   `yaml:"contexts"`
	Users          []NamedUser      `yaml:"users"`
	Preferences    map[string]any   `yaml:"preferences,omitempty"`
}

// NamedCluster represents a cluster entry in kubeconfig
type NamedCluster struct {
	Name    string  `yaml:"name"`
	Cluster Cluster `yaml:"cluster"`
}

// Cluster contains cluster connection information
type Cluster struct {
	Server                   string `yaml:"server"`
	CertificateAuthorityData string `yaml:"certificate-authority-data,omitempty"`
	InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify,omitempty"`
}

// NamedContext represents a context entry in kubeconfig
type NamedContext struct {
	Name    string  `yaml:"name"`
	Context Context `yaml:"context"`
}

// Context contains context settings
type Context struct {
	Cluster   string `yaml:"cluster"`
	User      string `yaml:"user"`
	Namespace string `yaml:"namespace,omitempty"`
}

// NamedUser represents a user entry in kubeconfig
type NamedUser struct {
	Name string `yaml:"name"`
	User User   `yaml:"user"`
}

// User contains user authentication information
type User struct {
	Exec *ExecConfig `yaml:"exec,omitempty"`
}

// ExecConfig contains exec credential plugin configuration
type ExecConfig struct {
	APIVersion      string   `yaml:"apiVersion"`
	Command         string   `yaml:"command"`
	Args            []string `yaml:"args,omitempty"`
	InteractiveMode string   `yaml:"interactiveMode,omitempty"`
}

// Manager handles kubeconfig file operations
type Manager struct {
	path string
}

// NewManager creates a new kubeconfig manager
func NewManager() (*Manager, error) {
	path := os.Getenv("KUBECONFIG")
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
	config, err := m.Load()
	if err != nil {
		return err
	}

	clusterName := params.ClusterName
	userName := params.UserName
	contextName := params.ContextName

	// Encode CA cert to base64 if provided
	var caCertBase64 string
	if params.CACert != "" {
		caCertBase64 = base64.StdEncoding.EncodeToString([]byte(params.CACert))
	}

	// Determine if we should skip TLS verification
	skipTLS := params.Insecure || (params.CACert == "" && !params.Insecure)

	// Add or update cluster
	clusterFound := false
	for i, c := range config.Clusters {
		if c.Name == clusterName {
			config.Clusters[i].Cluster = Cluster{
				Server:                   params.Server,
				CertificateAuthorityData: caCertBase64,
				InsecureSkipTLSVerify:    skipTLS && caCertBase64 == "",
			}
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
				InsecureSkipTLSVerify:    skipTLS && caCertBase64 == "",
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
					Args:            []string{"credential", "--server", params.Server},
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
					Args:            []string{"credential", "--server", params.Server},
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
	Insecure    bool   // Skip TLS verification
	SetCurrent  bool
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
