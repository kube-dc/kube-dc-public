package clusterinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// canonicalRKE2Kubeconfig is what /etc/rancher/rke2/rke2.yaml
// looks like on a fresh RKE2 master. All three names are "default"
// and the server URL points at 127.0.0.1:6443 (loopback because
// the file is meant to be pulled off the node itself). The
// certificate-authority-data is a stub base64 payload — clientcmd
// doesn't validate its contents at parse time, only that it's
// syntactically valid base64.
const canonicalRKE2Kubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: TE9OR0NBUEVN
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user:
    client-certificate-data: TE9OR0NFUlRQRU0=
    client-key-data: TE9OR0tFWVBFTQ==
`

// fakeSSHClient is a minimal ports.SSHClient. Records call args +
// returns canned Fetch bytes / errors.
type fakeSSHClient struct {
	fetchReturn []byte
	fetchErr    error
	lastPath    string
	lastHost    ports.SSHHost
	calls       int
}

func (f *fakeSSHClient) Fetch(_ context.Context, host ports.SSHHost, remotePath string) ([]byte, error) {
	f.calls++
	f.lastHost = host
	f.lastPath = remotePath
	return f.fetchReturn, f.fetchErr
}
func (f *fakeSSHClient) Run(_ context.Context, _ ports.SSHHost, _ string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSSHClient) Put(_ context.Context, _ ports.SSHHost, _ string, _ []byte, _ uint32) error {
	return nil
}

// TestFetchKubeconfig_HappyPath — canonical case. Fetches from
// /etc/rancher/rke2/rke2.yaml on the given host, rewrites server
// + renames default→<cluster>.
func TestFetchKubeconfig_HappyPath(t *testing.T) {
	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{Alias: "master.acme"},
		ClusterName: "acme-cloud",
		Domain:      "acme.com",
	})
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	// SSH.Fetch was called with the default remote path + the given host.
	if ssh.calls != 1 {
		t.Errorf("SSH.Fetch calls = %d, want 1", ssh.calls)
	}
	if ssh.lastPath != "/etc/rancher/rke2/rke2.yaml" {
		t.Errorf("wrong remote path: %q", ssh.lastPath)
	}
	if ssh.lastHost.Alias != "master.acme" {
		t.Errorf("wrong host: %+v", ssh.lastHost)
	}
	// Cluster entry renamed + server URL rewritten.
	if _, ok := cfg.Clusters["acme-cloud"]; !ok {
		t.Errorf("cluster not renamed to acme-cloud; got %v", keysOf(cfg.Clusters))
	}
	if _, ok := cfg.Clusters["default"]; ok {
		t.Errorf("RKE2 default cluster leaked past rename")
	}
	if got := cfg.Clusters["acme-cloud"].Server; got != "https://kube-api.acme.com:6443" {
		t.Errorf("server URL not rewritten: %q", got)
	}
	// User + context also renamed.
	if _, ok := cfg.AuthInfos["acme-cloud"]; !ok {
		t.Errorf("user not renamed; got %v", keysOfAuth(cfg.AuthInfos))
	}
	if _, ok := cfg.Contexts["acme-cloud"]; !ok {
		t.Errorf("context not renamed; got %v", keysOfCtx(cfg.Contexts))
	}
	// Context's internal cluster/user references remapped.
	ctx := cfg.Contexts["acme-cloud"]
	if ctx.Cluster != "acme-cloud" || ctx.AuthInfo != "acme-cloud" {
		t.Errorf("context internal refs not remapped: cluster=%s user=%s",
			ctx.Cluster, ctx.AuthInfo)
	}
	// current-context also remapped.
	if cfg.CurrentContext != "acme-cloud" {
		t.Errorf("current-context = %q, want acme-cloud", cfg.CurrentContext)
	}
	// Defensive: assert the RKE2 default key is gone from all three
	// maps (individual asserts above cover Clusters; add the other
	// two here so a future rename-map refactor can't drop them
	// silently).
	if _, ok := cfg.AuthInfos["default"]; ok {
		t.Errorf("RKE2 default user leaked past rename")
	}
	if _, ok := cfg.Contexts["default"]; ok {
		t.Errorf("RKE2 default context leaked past rename")
	}
}

// TestFetchKubeconfig_CustomRemotePath — non-standard rke2.yaml
// location (rare — kube-vip / custom RKE2 layout).
func TestFetchKubeconfig_CustomRemotePath(t *testing.T) {
	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	_, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{Alias: "m1"},
		ClusterName: "c1",
		Domain:      "example.com",
		RemotePath:  "/opt/rke2/kubeconfig.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ssh.lastPath != "/opt/rke2/kubeconfig.yaml" {
		t.Errorf("custom RemotePath not honoured: %q", ssh.lastPath)
	}
}

// TestFetchKubeconfig_SSHFetchError_Propagates — SSH-level error
// wraps up to the caller with the host + remote path in context.
func TestFetchKubeconfig_SSHFetchError_Propagates(t *testing.T) {
	ssh := &fakeSSHClient{fetchErr: errors.New("connection refused")}
	_, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{Alias: "unreachable"},
		ClusterName: "c1",
		Domain:      "example.com",
	})
	if err == nil {
		t.Fatal("expected propagated fetch error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("expected host in error, got %v", err)
	}
}

// TestFetchKubeconfig_MalformedYAML_ReturnsParseSentinel — remote
// file isn't a kubeconfig (permission-denied shell error, wrong
// path, etc). Distinct sentinel so cobra can suggest triage.
func TestFetchKubeconfig_MalformedYAML_ReturnsParseSentinel(t *testing.T) {
	ssh := &fakeSSHClient{fetchReturn: []byte("Permission denied\n")}
	_, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{Alias: "m"},
		ClusterName: "c",
		Domain:      "d",
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !errors.Is(err, ErrFetchKubeconfigParse) {
		t.Errorf("expected ErrFetchKubeconfigParse, got %v", err)
	}
}

// TestFetchKubeconfig_EmptyClusterList_ReturnsNoServer — parseable
// YAML but zero cluster entries. Signals RKE2 is still initialising
// or the remote file is malformed.
func TestFetchKubeconfig_EmptyClusterList_ReturnsNoServer(t *testing.T) {
	ssh := &fakeSSHClient{fetchReturn: []byte("apiVersion: v1\nkind: Config\nclusters: []\n")}
	_, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{Alias: "m"},
		ClusterName: "c",
		Domain:      "d",
	})
	if !errors.Is(err, ErrFetchKubeconfigNoServer) {
		t.Errorf("expected ErrFetchKubeconfigNoServer, got %v", err)
	}
}

// TestFetchKubeconfig_MissingDependency — programmer-error catches.
func TestFetchKubeconfig_MissingDependency(t *testing.T) {
	ssh := &fakeSSHClient{}
	cases := []struct {
		name string
		opts FetchKubeconfigOptions
	}{
		{"nil-ssh", FetchKubeconfigOptions{ClusterName: "c", Domain: "d"}},
		{"empty-cluster", FetchKubeconfigOptions{SSH: ssh, Domain: "d"}},
		{"empty-domain", FetchKubeconfigOptions{SSH: ssh, ClusterName: "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FetchKubeconfig(context.Background(), c.opts)
			if !errors.Is(err, ErrFetchKubeconfigMissingDependency) {
				t.Errorf("want ErrFetchKubeconfigMissingDependency, got %v", err)
			}
		})
	}
}

// TestMergeKubeconfig_FreshFile_WritesConfig — no existing
// kubeconfig at destPath. Merge should create the file with the
// new cluster + parent directory.
func TestMergeKubeconfig_FreshFile_WritesConfig(t *testing.T) {
	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh, Host: ports.SSHHost{Alias: "m"},
		ClusterName: "fresh", Domain: "fresh.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), ".kube", "config") // parent doesn't exist
	if err := MergeKubeconfig(dest, cfg, false); err != nil {
		t.Fatalf("merge: %v", err)
	}
	loaded, err := clientcmd.LoadFromFile(dest)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if _, ok := loaded.Clusters["fresh"]; !ok {
		t.Errorf("cluster 'fresh' not present after merge; keys=%v", keysOf(loaded.Clusters))
	}
	if loaded.CurrentContext != "" {
		t.Errorf("setCurrent=false must not touch current-context; got %q", loaded.CurrentContext)
	}
}

// TestMergeKubeconfig_PreservesOtherClusters — an existing
// kubeconfig with a "prod" cluster stays intact when we merge a
// "staging" cluster in.
func TestMergeKubeconfig_PreservesOtherClusters(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "config")
	// Seed an existing kubeconfig with an unrelated cluster.
	seed := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://prod.example.com:6443
  name: prod
contexts:
- context:
    cluster: prod
    user: prod
  name: prod
current-context: prod
users:
- name: prod
  user: {}
`
	if err := os.WriteFile(dest, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh, Host: ports.SSHHost{Alias: "s"},
		ClusterName: "staging", Domain: "staging.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := MergeKubeconfig(dest, cfg, false); err != nil {
		t.Fatal(err)
	}
	loaded, _ := clientcmd.LoadFromFile(dest)
	if _, ok := loaded.Clusters["prod"]; !ok {
		t.Errorf("prod cluster wiped by merge (regression); keys=%v", keysOf(loaded.Clusters))
	}
	if _, ok := loaded.Clusters["staging"]; !ok {
		t.Errorf("staging cluster not merged; keys=%v", keysOf(loaded.Clusters))
	}
	// setCurrent=false MUST preserve the operator's prior current-context.
	if loaded.CurrentContext != "prod" {
		t.Errorf("current-context flipped from prod to %q despite setCurrent=false", loaded.CurrentContext)
	}
}

// TestMergeKubeconfig_SetCurrent_FlipsContext — setCurrent=true
// overwrites current-context. The upsert semantic for entries is
// unchanged.
func TestMergeKubeconfig_SetCurrent_FlipsContext(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "config")
	seed := `apiVersion: v1
kind: Config
clusters:
- cluster: {server: https://old:6443}
  name: old
contexts:
- context: {cluster: old, user: old}
  name: old
current-context: old
users:
- name: old
  user: {}
`
	_ = os.WriteFile(dest, []byte(seed), 0o600)

	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg, err := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh, Host: ports.SSHHost{Alias: "n"},
		ClusterName: "new", Domain: "new.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := MergeKubeconfig(dest, cfg, true); err != nil {
		t.Fatal(err)
	}
	loaded, _ := clientcmd.LoadFromFile(dest)
	if loaded.CurrentContext != "new" {
		t.Errorf("setCurrent=true should have flipped current-context to 'new'; got %q",
			loaded.CurrentContext)
	}
}

// TestMergeKubeconfig_UpsertReplacesSameName — running the fetch
// twice against the same cluster (e.g. re-install) refreshes the
// entries in place rather than duplicating.
func TestMergeKubeconfig_UpsertReplacesSameName(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "config")

	// First merge — writes the initial state.
	ssh1 := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg1, _ := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh1, Host: ports.SSHHost{Alias: "m"},
		ClusterName: "cluster1", Domain: "cluster1.example.com",
	})
	if err := MergeKubeconfig(dest, cfg1, false); err != nil {
		t.Fatal(err)
	}

	// Second merge with a DIFFERENT server URL for the same cluster
	// name — models a reinstall or a domain change.
	newYAML := strings.Replace(canonicalRKE2Kubeconfig,
		"https://127.0.0.1:6443",
		"https://127.0.0.1:6443", 1) // same; the rewrite kicks in via Domain
	ssh2 := &fakeSSHClient{fetchReturn: []byte(newYAML)}
	cfg2, _ := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh2, Host: ports.SSHHost{Alias: "m"},
		ClusterName: "cluster1", Domain: "cluster1-v2.example.com", // NEW domain
	})
	if err := MergeKubeconfig(dest, cfg2, false); err != nil {
		t.Fatal(err)
	}

	loaded, _ := clientcmd.LoadFromFile(dest)
	// Exactly ONE cluster1 entry, refreshed with the new server.
	got := loaded.Clusters["cluster1"].Server
	if got != "https://kube-api.cluster1-v2.example.com:6443" {
		t.Errorf("upsert failed to replace server; got %q", got)
	}
	// No duplicate entries.
	if n := len(loaded.Clusters); n != 1 {
		t.Errorf("expected 1 cluster after upsert; got %d (%v)", n, keysOf(loaded.Clusters))
	}
}

// TestMergeKubeconfig_AtomicWrite_NoTempResidue — successful merge
// leaves ONLY the destination file in the directory (no `.tmp.*`
// leftovers).
func TestMergeKubeconfig_AtomicWrite_NoTempResidue(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config")

	ssh := &fakeSSHClient{fetchReturn: []byte(canonicalRKE2Kubeconfig)}
	cfg, _ := FetchKubeconfig(context.Background(), FetchKubeconfigOptions{
		SSH: ssh, Host: ports.SSHHost{Alias: "m"},
		ClusterName: "c", Domain: "d.example.com",
	})
	if err := MergeKubeconfig(dest, cfg, false); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.tmp.") {
			t.Errorf("temp file leaked past atomic write: %s", e.Name())
		}
	}
}

// TestMergeKubeconfig_MissingDest_Refuses — programmer-error catch.
func TestMergeKubeconfig_MissingDest_Refuses(t *testing.T) {
	err := MergeKubeconfig("", nil, false)
	if err == nil {
		t.Fatal("empty destPath must error")
	}
}

// TestDefaultKubeconfigPath_KUBECONFIG_Wins — first entry of the
// KUBECONFIG env wins over ~/.kube/config.
func TestDefaultKubeconfigPath_KUBECONFIG_Wins(t *testing.T) {
	t.Setenv("KUBECONFIG", "/foo/config:/bar/config")
	got := DefaultKubeconfigPath()
	if got != "/foo/config" {
		t.Errorf("KUBECONFIG first-entry precedence broken: %q", got)
	}
}

// TestDefaultKubeconfigPath_NoKUBECONFIG_HomeConfig — fallback to
// ~/.kube/config when KUBECONFIG is unset.
func TestDefaultKubeconfigPath_NoKUBECONFIG_HomeConfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", "/home/testuser")
	got := DefaultKubeconfigPath()
	if got != "/home/testuser/.kube/config" {
		t.Errorf("HOME fallback broken: %q", got)
	}
}

// ---------- helpers ----------

func keysOf[T any](m map[string]*T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
func keysOfAuth[T any](m map[string]*T) []string { return keysOf(m) }
func keysOfCtx[T any](m map[string]*T) []string  { return keysOf(m) }
