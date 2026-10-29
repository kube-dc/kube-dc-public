// Package platform tells the CLI what it is talking to, so one CLI works
// against older and newer Kube-DC installations.
//
// Two sources:
//
//   - The contract: /.well-known/kube-dc-config on the installation's backend
//     names the platform release and which CLI versions it accepts
//     (cli.minimumVersion, cli.recommendedVersion). A CLI older than the
//     minimum refuses to run and asks to be updated; one older than the
//     recommended version says an update is available. An installation that
//     publishes no contract, or cannot be reached, never blocks the CLI.
//   - Capabilities: Kubernetes discovery says which API group versions and
//     resources the cluster serves. A command uses the newest API the cluster
//     serves (the Kubernetes or platform.kube-dc.com resource) and falls back
//     to the backend route older installations have.
//
// Both are cached per installation for an hour under ~/.kube-dc/platform/.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/selfupdate"
)

// CacheTTL is how long a contract or a capability list is reused.
const CacheTTL = time.Hour

// Contract is what /.well-known/kube-dc-config says about the installation.
type Contract struct {
	APIVersion string `json:"apiVersion"`
	Platform   struct {
		Version        string `json:"version,omitempty"`
		BackendVersion string `json:"backendVersion,omitempty"`
	} `json:"platform"`
	CLI struct {
		MinimumVersion     string `json:"minimumVersion,omitempty"`
		RecommendedVersion string `json:"recommendedVersion,omitempty"`
	} `json:"cli"`
	Audit struct {
		KubernetesGroups []string `json:"kubernetesGroups,omitempty"`
	} `json:"audit"`
}

// Audits reports whether the installation's platform audit log records
// Kubernetes calls on group. Only then may the CLI use that group's
// resources directly; otherwise it keeps the backend routes, which write the
// audit events themselves. Unknown (no contract) answers false.
func (c *Contract) Audits(group string) bool {
	if c == nil {
		return false
	}
	for _, g := range c.Audit.KubernetesGroups {
		if g == group {
			return true
		}
	}
	return false
}

// ErrCLITooOld is returned when the installation requires a newer CLI.
type ErrCLITooOld struct{ Have, Need, Platform string }

func (e *ErrCLITooOld) Error() string {
	on := ""
	if e.Platform != "" {
		on = " (platform " + e.Platform + ")"
	}
	return fmt.Sprintf("this installation%s requires kube-dc CLI %s or newer; this is %s. Run `kube-dc update`", on, e.Need, e.Have)
}

// Check applies the contract to cliVersion: an error when the CLI is below
// the minimum, a notice when it is below the recommended version. Development
// builds are never refused.
func (c *Contract) Check(cliVersion string) (notice string, err error) {
	if c == nil || !selfupdate.Released(cliVersion) {
		return "", nil
	}
	if min := c.CLI.MinimumVersion; selfupdate.Released(min) && selfupdate.Newer(min, cliVersion) {
		return "", &ErrCLITooOld{Have: cliVersion, Need: min, Platform: c.Platform.Version}
	}
	if rec := c.CLI.RecommendedVersion; selfupdate.Released(rec) && selfupdate.Newer(rec, cliVersion) {
		return fmt.Sprintf("kube-dc CLI %s is recommended for this installation (this is %s); run `kube-dc update`", rec, cliVersion), nil
	}
	return "", nil
}

// Cache stores contracts and capabilities on disk; tests point Dir elsewhere.
type Cache struct {
	Dir string
	Now func() time.Time
}

// DefaultCache is ~/.kube-dc/platform.
func DefaultCache() *Cache {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Cache{Now: time.Now}
	}
	return &Cache{Dir: filepath.Join(home, ".kube-dc", "platform"), Now: time.Now}
}

type entry struct {
	StoredAt time.Time       `json:"storedAt"`
	Value    json.RawMessage `json:"value"`
}

func (c *Cache) file(kind, key string) string {
	return filepath.Join(c.Dir, kind+"-"+url.PathEscape(key)+".json")
}

func (c *Cache) get(kind, key string, out any) bool {
	if c == nil || c.Dir == "" {
		return false
	}
	b, err := os.ReadFile(c.file(kind, key))
	if err != nil {
		return false
	}
	var e entry
	if json.Unmarshal(b, &e) != nil || c.Now().Sub(e.StoredAt) >= CacheTTL {
		return false
	}
	return json.Unmarshal(e.Value, out) == nil
}

func (c *Cache) put(kind, key string, v any) {
	if c == nil || c.Dir == "" {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil || os.MkdirAll(c.Dir, 0o700) != nil {
		return
	}
	b, _ := json.Marshal(entry{StoredAt: c.Now(), Value: raw})
	_ = os.WriteFile(c.file(kind, key), b, 0o600)
}

// FetchContract reads the contract from discoveryURL (cached). It returns
// nil, nil when the installation does not publish one.
func FetchContract(ctx context.Context, client *http.Client, cache *Cache, discoveryURL string) (*Contract, error) {
	var c Contract
	if cache.get("contract", discoveryURL, &c) {
		return &c, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform discovery: %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
		return nil, fmt.Errorf("platform discovery: %w", err)
	}
	cache.put("contract", discoveryURL, c)
	return &c, nil
}

// Lister is the Kubernetes discovery the capabilities need (k8sapi.Client).
type Lister interface {
	ServedResources(ctx context.Context, groupVersion string) ([]string, error)
}

// Capabilities answers which resources a cluster serves.
type Capabilities struct {
	server string
	lister Lister
	cache  *Cache
	mu     sync.Mutex
	served map[string]map[string]bool
}

// NewCapabilities reads discovery from lister for the API server at server.
func NewCapabilities(server string, lister Lister, cache *Cache) *Capabilities {
	return &Capabilities{server: server, lister: lister, cache: cache, served: map[string]map[string]bool{}}
}

// Serves reports whether groupVersion serves resource (a resource or
// "resource/subresource" name). A discovery failure answers false: the caller
// keeps the API every installation has.
func (c *Capabilities) Serves(ctx context.Context, groupVersion, resource string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	set, ok := c.served[groupVersion]
	if !ok {
		var names []string
		key := c.server + " " + groupVersion
		if !c.cache.get("apis", key, &names) {
			var err error
			names, err = c.lister.ServedResources(ctx, groupVersion)
			if err != nil {
				return false // not cached: asked again next time
			}
			if names == nil {
				names = []string{}
			}
			c.cache.put("apis", key, names)
		}
		set = map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		c.served[groupVersion] = set
	}
	return set[strings.TrimSpace(resource)]
}
