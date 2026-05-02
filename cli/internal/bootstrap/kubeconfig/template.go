// Package kubeconfig synthesises and materialises per-cluster kubeconfigs
// for the `kube-dc bootstrap kubeconfig <cluster>` flow.
//
// Design (see installer-prd §16): connection metadata only — server URL,
// optional public CA cert, exec-plugin user entry pointing at
// `kube-dc credential`. Credentials are never read from or written to the
// fleet repo; identity is minted at exec-time from the operator's own
// OIDC tokens cached under ~/.kube-dc/credentials/.
package kubeconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// Template is the in-memory shape of clusters/<name>/kubeconfig.template.yaml.
//
// CACertPEM is the PEM-encoded API-server CA. Empty when the API is fronted
// by a publicly-trusted certificate (Let's Encrypt etc.) — system trust
// handles verification and there's nothing useful to embed.
//
// Realm is what the kube-dc credential exec plugin will pass via --realm;
// "master" means admin identity, an org name means tenant. ClusterName,
// ContextName, and UserName encode the realm so multiple identities for
// the same cluster (admin + tenant) coexist as separate kubeconfig
// entries (kube-dc/<cluster>/admin vs kube-dc/<cluster>/<org>).
type Template struct {
	ClusterName string
	ContextName string
	UserName    string
	Server      string
	Realm       string
	CACertPEM   string
}

// FromCluster synthesises a Template from the cluster-config.env values
// already parsed in c. Realm is the kube-dc identity the kubeconfig is
// wired for: "master" produces an admin-flavored template, an org name
// produces a tenant-flavored one. Does no network I/O; CACertPEM is
// left empty here — call FetchCA separately if the operator wants to
// embed a non-public CA chain into the template.
func FromCluster(c discover.Cluster, realm string) (*Template, error) {
	if c.KubeAPIURL == "" {
		return nil, fmt.Errorf("cluster %q has no KUBE_API_EXTERNAL_URL in cluster-config.env", c.Name)
	}
	if realm == "" {
		return nil, fmt.Errorf("realm is required (use \"master\" for admin, or an org name for tenant)")
	}
	safeCluster := strings.ReplaceAll(c.Name, "/", "-")
	// Identity suffix that lands in kubeconfig context/cluster/user
	// names. "master" → "admin" so the operator-visible name reads
	// naturally ("kube-dc/cloud/admin" vs "kube-dc/cloud/master").
	identitySuffix := realm
	if realm == "master" {
		identitySuffix = "admin"
	}
	return &Template{
		ClusterName: "kube-dc-" + safeCluster + "-" + identitySuffix,
		ContextName: "kube-dc/" + c.Name + "/" + identitySuffix,
		UserName:    "kube-dc-" + identitySuffix + "@" + safeCluster,
		Server:      c.KubeAPIURL,
		Realm:       realm,
	}, nil
}

// FetchCA is re-exported for callers that already import this package
// (the kube-dc bootstrap kubeconfig command). It delegates to
// discover.FetchCA — the canonical home for the probe primitive.
func FetchCA(ctx context.Context, server string, dialTimeout time.Duration) (string, error) {
	return discover.FetchCA(ctx, server, dialTimeout)
}

// LoadTemplateFile reads clusters/<name>/kubeconfig.template.yaml from the
// fleet repo and returns its raw bytes, or an error wrapping os.ErrNotExist
// when no template has been committed yet.
func LoadTemplateFile(clusterDir string) ([]byte, error) {
	p := filepath.Join(clusterDir, "kubeconfig.template.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return b, nil
}
