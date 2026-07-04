// M4-T06 — auto-pull the RKE2 admin kubeconfig from the master
// node's `/etc/rancher/rke2/rke2.yaml` over SSH, rewrite the server
// URL to the operator's `kube-api.<domain>` FQDN, and merge into
// the local kubeconfig.
//
// Motivation: today an operator finishing a fresh RKE2 install
// runs a manual scp + sed chain:
//
//   scp master:/etc/rancher/rke2/rke2.yaml /tmp/rke2.yaml
//   sed -i "s|127.0.0.1:6443|kube-api.acme.com:6443|" /tmp/rke2.yaml
//   KUBECONFIG=~/.kube/config:/tmp/rke2.yaml kubectl config view --flatten > ~/.kube/config
//
// This slice replaces that with `kube-dc bootstrap fetch-kubeconfig
// <cluster> --ssh-host operator@master.acme.com --domain acme.com`.
// The init auto-run (last step of `bootstrap init` when `--ssh-host`
// is set) is a follow-up wire-in commit.
//
// Design:
//
//   1. FetchKubeconfig(ctx, opts) reads the remote YAML via
//      SSHClient.Fetch, parses via clientcmd, renames every "default"
//      cluster/user/context to the operator's cluster name (so
//      multiple installs from the same operator's laptop don't
//      collide on `kubectl config get-contexts`), rewrites the
//      server URL to the public FQDN, and returns the *api.Config
//      ready to write.
//
//   2. MergeKubeconfig(destPath, cfg, setCurrent) merges the returned
//      Config into an existing kubeconfig (or creates one). Uses
//      clientcmd's canonical load-merge-write path so multi-context
//      operators don't lose their other clusters. Atomic write
//      (temp + rename) so a mid-write crash doesn't corrupt the
//      operator's primary kubeconfig.
//
// **Safe on re-runs** — MergeKubeconfig upserts by name (same
// contract as `kubectl config set-cluster` / `set-context`), so
// pulling the kubeconfig again after a re-install refreshes the
// entries instead of accumulating duplicates.
//
// **Never touches disk directly for the RKE2 file** — SSHClient.Fetch
// returns bytes in-memory only; we parse + transform + write to the
// destination path in one shot. No temp files with secrets.

package clusterinit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// RKE2 defaults — every rke2.yaml on a fresh install carries these
// names + this server URL.
const (
	defaultRKE2RemotePath = "/etc/rancher/rke2/rke2.yaml"
	rke2DefaultName       = "default"
)

// FetchKubeconfigOptions is the parameter bundle for FetchKubeconfig.
type FetchKubeconfigOptions struct {
	// SSH is the port adapter — production wires the real SSH
	// client from bootstrap.Session; tests inject a fake.
	SSH ports.SSHClient

	// Host is the SSH endpoint of an RKE2 control-plane node. When
	// Host.Alias is set, the SSH adapter resolves the rest from
	// ~/.ssh/config.
	Host ports.SSHHost

	// ClusterName becomes the cluster/user/context name in the
	// resulting kubeconfig. Required.
	ClusterName string

	// Domain is the cluster's public FQDN suffix — the fetched
	// server URL (`https://127.0.0.1:6443`) is rewritten to
	// `https://kube-api.<Domain>:6443` before returning. Required.
	Domain string

	// RemotePath overrides the default `/etc/rancher/rke2/rke2.yaml`
	// path on the remote host. Empty is normal; kept for operators
	// running kube-vip or a non-standard RKE2 layout.
	RemotePath string

	// Out is the operator-facing log writer. Nil is safe.
	Out io.Writer
}

// --- Errors ---

// ErrFetchKubeconfigMissingDependency surfaces on nil SSH / empty
// ClusterName / empty Domain. Cobra populates these unconditionally;
// seeing this in production is a programmer error.
var ErrFetchKubeconfigMissingDependency = errors.New("init: fetch-kubeconfig missing required dependency")

// ErrFetchKubeconfigParse wraps a YAML/clientcmd parse failure on
// the fetched remote file. Usually means SSH.Fetch returned
// something that isn't a kubeconfig (wrong path, RKE2 not yet
// bootstrapped on the remote, permission-denied → shell error
// leaked into the "kubeconfig" bytes).
var ErrFetchKubeconfigParse = errors.New("init: fetch-kubeconfig: remote file is not a valid kubeconfig")

// ErrFetchKubeconfigNoServer surfaces when the parsed kubeconfig
// has no cluster entries — RKE2 mid-install state or a malformed
// input. Distinct sentinel so cobra can suggest waiting a bit and
// re-running.
var ErrFetchKubeconfigNoServer = errors.New("init: fetch-kubeconfig: remote kubeconfig has no cluster entries (RKE2 still initialising?)")

// --- Engine ---

// FetchKubeconfig pulls the RKE2 admin kubeconfig from the master
// node, rewrites cluster/user/context names to `opts.ClusterName`,
// and rewrites the server URL to `https://kube-api.<Domain>:6443`.
// Returns the transformed *clientcmdapi.Config ready to feed into
// MergeKubeconfig; never writes to disk.
func FetchKubeconfig(ctx context.Context, opts FetchKubeconfigOptions) (*clientcmdapi.Config, error) {
	if opts.SSH == nil {
		return nil, fmt.Errorf("%w: SSH", ErrFetchKubeconfigMissingDependency)
	}
	if opts.ClusterName == "" {
		return nil, fmt.Errorf("%w: ClusterName", ErrFetchKubeconfigMissingDependency)
	}
	if opts.Domain == "" {
		return nil, fmt.Errorf("%w: Domain", ErrFetchKubeconfigMissingDependency)
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	remote := opts.RemotePath
	if remote == "" {
		remote = defaultRKE2RemotePath
	}

	hostLabel := opts.Host.Alias
	if hostLabel == "" {
		hostLabel = opts.Host.Hostname
	}
	fmt.Fprintf(out, "[fetch-kubeconfig] pulling %s from %s\n", remote, hostLabel)
	body, err := opts.SSH.Fetch(ctx, opts.Host, remote)
	if err != nil {
		return nil, fmt.Errorf("init: fetch-kubeconfig: SSH.Fetch %s:%s: %w",
			hostLabel, remote, err)
	}

	cfg, err := clientcmd.Load(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchKubeconfigParse, err)
	}
	if len(cfg.Clusters) == 0 {
		return nil, ErrFetchKubeconfigNoServer
	}

	// Rewrite server URL for every cluster (RKE2 emits one, but
	// belt-and-braces in case a downstream fork adds multiple).
	// Target port is 6443 by RKE2 convention — matches the
	// front-door kube-api Service's advertised port.
	targetServer := fmt.Sprintf("https://kube-api.%s:6443", opts.Domain)
	for _, c := range cfg.Clusters {
		c.Server = targetServer
	}

	// Rename the RKE2 default entries to the operator's cluster
	// name. clientcmd's api.Config keys the maps by name, so we
	// rebuild the three maps with the new key.
	cfg.Clusters = renameConfigMap(cfg.Clusters, rke2DefaultName, opts.ClusterName)
	cfg.AuthInfos = renameConfigMap(cfg.AuthInfos, rke2DefaultName, opts.ClusterName)
	cfg.Contexts = renameConfigMap(cfg.Contexts, rke2DefaultName, opts.ClusterName)

	// Contexts hold references to cluster + user by name — update
	// those references so the rebound names are consistent.
	for _, ctx := range cfg.Contexts {
		if ctx.Cluster == rke2DefaultName {
			ctx.Cluster = opts.ClusterName
		}
		if ctx.AuthInfo == rke2DefaultName {
			ctx.AuthInfo = opts.ClusterName
		}
	}
	// CurrentContext usually points at "default" post-rke2-boot;
	// remap it too so a naked `kubectl` after the fetch lands on
	// the operator's cluster (unless the operator's existing
	// kubeconfig had a current-context, in which case
	// MergeKubeconfig preserves it — see setCurrent).
	if cfg.CurrentContext == rke2DefaultName {
		cfg.CurrentContext = opts.ClusterName
	}

	fmt.Fprintf(out, "[fetch-kubeconfig] rewrote server → %s; renamed entries → %s\n",
		targetServer, opts.ClusterName)
	return cfg, nil
}

// renameConfigMap[T] renames the entry keyed at `from` to `to`
// inside a clientcmdapi map (Clusters / AuthInfos / Contexts). No-op
// when `from` is missing or when from==to. Preserves every other
// entry unchanged.
func renameConfigMap[T any](m map[string]*T, from, to string) map[string]*T {
	if m == nil || from == to {
		return m
	}
	v, ok := m[from]
	if !ok {
		return m
	}
	delete(m, from)
	m[to] = v
	return m
}

// MergeKubeconfig merges `cfg` into the kubeconfig at `destPath`,
// creating the file if it doesn't exist. Upsert semantics: entries
// with the same name in the existing file are REPLACED (matches
// `kubectl config set-cluster` / `set-context` behaviour); other
// entries are preserved. When `setCurrent` is true, the destination
// file's current-context is set to `cfg.CurrentContext`.
//
// Atomic write: renders into a temp file in the same directory,
// fsyncs, then rename() so a mid-write crash leaves the operator's
// primary kubeconfig intact.
func MergeKubeconfig(destPath string, cfg *clientcmdapi.Config, setCurrent bool) error {
	if destPath == "" {
		return fmt.Errorf("init: MergeKubeconfig: empty destPath")
	}
	if cfg == nil {
		return fmt.Errorf("init: MergeKubeconfig: nil config")
	}

	// Load existing (or start empty if the file doesn't exist).
	var existing *clientcmdapi.Config
	if _, err := os.Stat(destPath); err == nil {
		existing, err = clientcmd.LoadFromFile(destPath)
		if err != nil {
			return fmt.Errorf("init: MergeKubeconfig: load %s: %w", destPath, err)
		}
	} else if os.IsNotExist(err) {
		existing = clientcmdapi.NewConfig()
	} else {
		return fmt.Errorf("init: MergeKubeconfig: stat %s: %w", destPath, err)
	}

	// Upsert clusters / users / contexts.
	for name, c := range cfg.Clusters {
		existing.Clusters[name] = c
	}
	for name, a := range cfg.AuthInfos {
		existing.AuthInfos[name] = a
	}
	for name, c := range cfg.Contexts {
		existing.Contexts[name] = c
	}
	if setCurrent && cfg.CurrentContext != "" {
		existing.CurrentContext = cfg.CurrentContext
	}

	// Ensure parent dir exists — first-ever fetch typically hits
	// ~/.kube which the operator may not have created yet.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return fmt.Errorf("init: MergeKubeconfig: mkdir %s: %w",
			filepath.Dir(destPath), err)
	}

	// Atomic write via clientcmd.Write → temp + rename.
	body, err := clientcmd.Write(*existing)
	if err != nil {
		return fmt.Errorf("init: MergeKubeconfig: serialise: %w", err)
	}
	return writeFileAtomicKubeconfig(destPath, body, 0o600)
}

// writeFileAtomicKubeconfig writes body to path via a same-directory
// temp file + rename. Mode is applied at OpenFile time so the target
// never briefly exists at a wider mode. Cleans up on error.
//
// Suffixed with "Kubeconfig" to avoid a collision with the existing
// writeFileAtomic in the scaffold engine's env helper — same idea,
// slightly different mode discipline.
func writeFileAtomicKubeconfig(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Errorf("write-atomic: rand: %w", err)
	}
	tmp := filepath.Join(dir, base+".tmp."+hex.EncodeToString(buf[:]))

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("write-atomic: open %s: %w", tmp, err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write-atomic: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write-atomic: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write-atomic: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write-atomic: rename %s→%s: %w", tmp, path, err)
	}
	return nil
}

// DefaultKubeconfigPath resolves the operator's local kubeconfig
// path via the standard precedence: `$KUBECONFIG` (first entry
// only — multi-file kubeconfig merge is out of scope for the
// auto-pull), otherwise `~/.kube/config`. Empty string on any
// resolution failure (caller falls back to `~/.kube/config`).
//
// **Multi-file KUBECONFIG note**: the operator can set
// `KUBECONFIG=/a:/b` to merge multiple files at kubectl-load time.
// Auto-pull writes to the FIRST file only — that's the operator's
// primary kubeconfig by convention (kubectl also writes there via
// `set-context`). Documented in the cobra help.
func DefaultKubeconfigPath() string {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		parts := filepath.SplitList(env)
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}
