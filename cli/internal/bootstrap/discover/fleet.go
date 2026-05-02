// Package discover implements the probe set that drives the bootstrap TUI.
// Each Probe inspects local tooling, host state, the cluster, or the fleet
// repo and returns a Status that screens render against.
package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// Cluster is a single cluster overlay discovered in the fleet repo
// (one row in the fleet landing screen).
type Cluster struct {
	// Name is the directory path relative to clusters/, e.g. "cloud",
	// "stage", or "cs/zrh". Round-trips through git commit messages and
	// the `kube-dc bootstrap status <name>` argument.
	Name string

	// Dir is the absolute filesystem path to the cluster overlay.
	Dir string

	// EnvPath is the absolute path to cluster-config.env.
	EnvPath string

	// Env is the parsed cluster-config.env.
	Env *config.Env

	// Convenience accessors populated from Env at parse time.
	Domain          string
	NodeExternalIP  string
	KubeAPIURL      string
	KeycloakHost    string
	ExtNetName      string

	// HasInTreeKubeconfig is true when a `kubeconfig*` or `*.kubeconfig`
	// file is present in the cluster directory. The fleet's convention
	// is no kubeconfigs in tree (see installer-prd §9.7); the fleet view
	// flags such files as a warning.
	HasInTreeKubeconfig bool
}

// ListClusters walks the fleet repo at repoRoot and returns every cluster
// overlay it finds. A cluster overlay is any directory under clusters/
// that contains a cluster-config.env file. Subpaths with slashes (e.g.
// clusters/cs/zrh) are supported up to two levels deep.
//
// repoRoot must be an absolute path; if empty, ListClusters returns an
// error rather than guessing.
func ListClusters(repoRoot string) ([]Cluster, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("fleet repo root not configured")
	}
	if !filepath.IsAbs(repoRoot) {
		abs, err := filepath.Abs(repoRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve repo root: %w", err)
		}
		repoRoot = abs
	}

	clustersDir := filepath.Join(repoRoot, "clusters")
	info, err := os.Stat(clustersDir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", clustersDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", clustersDir)
	}

	// Walk two levels deep — clusters/<name>/cluster-config.env or
	// clusters/<group>/<name>/cluster-config.env. The double walk is
	// mandatory: cs/zrh is a real cluster path with a slash.
	var envFiles []string
	level1, err := os.ReadDir(clustersDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", clustersDir, err)
	}
	for _, e1 := range level1 {
		if !e1.IsDir() {
			continue
		}
		l1Path := filepath.Join(clustersDir, e1.Name())
		if fileExists(filepath.Join(l1Path, "cluster-config.env")) {
			envFiles = append(envFiles, filepath.Join(l1Path, "cluster-config.env"))
			continue
		}
		// One level deeper.
		level2, err := os.ReadDir(l1Path)
		if err != nil {
			continue
		}
		for _, e2 := range level2 {
			if !e2.IsDir() {
				continue
			}
			l2Path := filepath.Join(l1Path, e2.Name())
			if fileExists(filepath.Join(l2Path, "cluster-config.env")) {
				envFiles = append(envFiles, filepath.Join(l2Path, "cluster-config.env"))
			}
		}
	}

	out := make([]Cluster, 0, len(envFiles))
	for _, ep := range envFiles {
		c, err := loadCluster(clustersDir, ep)
		if err != nil {
			// Don't fail the whole walk for one broken cluster — the fleet
			// view will surface the error inline. Append a sentinel row.
			out = append(out, Cluster{
				Name:    relName(clustersDir, filepath.Dir(ep)),
				Dir:     filepath.Dir(ep),
				EnvPath: ep,
				Domain:  fmt.Sprintf("error: %v", err),
			})
			continue
		}
		out = append(out, c)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadCluster(clustersDir, envPath string) (Cluster, error) {
	dir := filepath.Dir(envPath)
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return Cluster{}, err
	}

	c := Cluster{
		Name:           relName(clustersDir, dir),
		Dir:            dir,
		EnvPath:        envPath,
		Env:            env,
		Domain:         env.GetOr("DOMAIN", ""),
		NodeExternalIP: env.GetOr("NODE_EXTERNAL_IP", ""),
		KubeAPIURL:     env.GetOr("KUBE_API_EXTERNAL_URL", ""),
		KeycloakHost:   env.GetOr("KEYCLOAK_HOSTNAME", ""),
		ExtNetName:     env.GetOr("EXT_NET_NAME", ""),
	}

	// A kubeconfig in tree is a credential leak red flag — surface it.
	c.HasInTreeKubeconfig = anyFileMatches(dir, []string{"kubeconfig", "kubeconfig.yaml"}, ".kubeconfig")

	return c, nil
}

// relName converts /repo/clusters/cs/zrh → "cs/zrh".
func relName(clustersDir, dir string) string {
	rel, err := filepath.Rel(clustersDir, dir)
	if err != nil {
		return filepath.Base(dir)
	}
	return filepath.ToSlash(rel)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// anyFileMatches reports whether dir contains a file whose name is one of
// the exact matches OR ends with the given suffix.
func anyFileMatches(dir string, exactNames []string, suffix string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		for _, ex := range exactNames {
			if name == ex {
				return true
			}
		}
		if suffix != "" && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
