// Package ssh is the real ports.SSHClient adapter.
//
// **Auth flow** (per B-004): try ssh-agent first (via
// `$SSH_AUTH_SOCK`), fall back to `IdentityFile` from `~/.ssh/config`, then
// OpenSSH's standard ~/.ssh/id_* identity files.
// NEVER accept a `--ssh-key` flag — operators put keys in their ssh
// config; the CLI doesn't manage them.
//
// Host config (User, Port, ProxyJump, …) comes from `~/.ssh/config`
// via kevinburke/ssh_config. SSHHost.Hostname / User / Port act as
// overrides when the operator passes them explicitly.
//
// **ProxyJump / bastion**. The adapter tunnels through an SSH jump
// chain when the target's `~/.ssh/config` sets ProxyJump, or when the
// caller sets SSHHost.ProxyJump (which overrides the config). The chain
// is applied left-to-right: connect to the first hop directly, each
// subsequent hop through the previous, then the target through the last
// hop. Every hop's host key is verified through the same known_hosts
// callback. Nested ProxyJump on a hop itself is not followed — specify
// the full chain on the target (matches how operators write `-J`).
//
// **Host-key trust**. Verification is strict by default (unknown host
// or fingerprint mismatch → refuse, with an actionable ssh-keyscan /
// ssh-keygen -R remedy). WithAcceptNewHostKeys() opts into OpenSSH's
// `StrictHostKeyChecking=accept-new` posture: an UNKNOWN host key is
// recorded to known_hosts and accepted (for unattended installs), but a
// MISMATCH is still refused (MITM protection is never relaxed).
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// maxFetchBytes mirrors the ports.SSHClient contract — 4 MiB is the
// kubeconfig sanity ceiling.
const maxFetchBytes = 4 * 1024 * 1024

// Client implements ports.SSHClient.
//
// **ctx contract** (M0-T06 batch-2 review): caller-set context
// governs both connect (DialContext) AND command runtime. A hung
// remote command terminates when ctx.Done() fires — the adapter
// closes the underlying session + client to interrupt session.Run.
type Client struct {
	// sshConfigPath overrides the location of the ssh_config file.
	// Empty resolves to `~/.ssh/config`.
	sshConfigPath string

	// dialContext is the network dialer hook. Tests inject a stub;
	// production wraps net.Dialer.DialContext with a 30s connect
	// timeout that interacts cleanly with the caller's ctx deadline
	// (whichever fires first wins).
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// loadAuth resolves auth methods. Tests override to inject a
	// dummy signer when the host has no ssh-agent / IdentityFile —
	// production reads ssh-agent + IdentityFile per B-004.
	loadAuth func(identityFile string) ([]ssh.AuthMethod, error)

	// khMu guards the cached host-key callback. The known_hosts
	// file is parsed once per Client + reused for every Run/Fetch/
	// Put call; mutex covers concurrent access from multiple
	// goroutines using the same Client.
	khMu sync.Mutex
	// khCallback is the cached ssh.HostKeyCallback. See
	// hostKeyCallback() for construction semantics.
	khCallback ssh.HostKeyCallback

	// hostKeyCallbackForTest lets test code inject a callback that
	// bypasses known_hosts (e.g. accept a canned test key). When
	// non-nil, hostKeyCallback() returns this instead of the real
	// known_hosts-backed callback. Production paths never set this.
	hostKeyCallbackForTest ssh.HostKeyCallback

	// acceptNewHostKeys, when true, records an UNKNOWN host key to
	// known_hosts and accepts the connection (OpenSSH
	// StrictHostKeyChecking=accept-new). A MISMATCH is still refused.
	// Off by default (strict). Set via WithAcceptNewHostKeys.
	acceptNewHostKeys bool

	// acceptedHosts remembers the key we accept-new'd per host within a
	// single process (hostname → marshaled key). The cached known_hosts
	// callback never sees our appends, so this is the ONLY guard on
	// repeat contacts: same key → no-op (avoids a duplicate line); a
	// DIFFERENT key for an already-accepted host → refuse as a mismatch
	// (the MITM guarantee must hold within the process too). Guarded by
	// khMu.
	acceptedHosts map[string]string
}

// Option configures a Client.
type Option func(*Client)

// WithAcceptNewHostKeys opts into recording+accepting an unknown host
// key (OpenSSH StrictHostKeyChecking=accept-new) instead of refusing.
// For unattended installs where a manual ssh-keyscan is impractical. A
// host-key MISMATCH is still refused — MITM protection is not relaxed.
func WithAcceptNewHostKeys() Option {
	return func(c *Client) { c.acceptNewHostKeys = true }
}

// New returns a Client using the operator's `~/.ssh/config`.
func New(opts ...Option) *Client {
	d := &net.Dialer{Timeout: 30 * time.Second}
	c := &Client{
		dialContext: d.DialContext,
		loadAuth:    loadAuthMethods,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Compile-time assertion.
var _ ports.SSHClient = (*Client)(nil)

// ---------- ports.SSHClient ----------

func (c *Client) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	if cmd == "" {
		return nil, fmt.Errorf("ssh: empty cmd")
	}
	session, client, cleanup, err := c.dialSession(ctx, host)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// stdout and stderr are copied by SEPARATE goroutines inside the ssh
	// library, so they MUST NOT share a plain bytes.Buffer — that's a data
	// race, and over a ProxyJump tunnel it manifests as SILENTLY LOST
	// stdout (the timing differs from a direct conn, where it happened to
	// be benign). Use a mutex-guarded buffer for the combined stream.
	var out syncBuffer
	session.Stdout = &out
	session.Stderr = &out

	if err := runSessionWithCtx(ctx, session, client, cmd); err != nil {
		return out.Bytes(), fmt.Errorf("ssh: run %q on %s: %w", cmd, sshTarget(host), err)
	}
	return out.Bytes(), nil
}

// syncBuffer is a goroutine-safe bytes.Buffer for the combined
// stdout+stderr capture in Run (the ssh library writes the two streams
// from different goroutines).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Return a copy so callers can't race the buffer's backing array.
	b := s.buf.Bytes()
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

func (c *Client) Fetch(ctx context.Context, host ports.SSHHost, remotePath string) ([]byte, error) {
	if remotePath == "" {
		return nil, fmt.Errorf("ssh: empty remote path")
	}
	// Use `cat` over an ssh session — works against any POSIX shell;
	// avoids vendoring SFTP which would double the SSH dep surface.
	session, client, cleanup, err := c.dialSession(ctx, host)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	var out bytes.Buffer
	session.Stdout = newCappedWriter(&out, maxFetchBytes)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	// Single-quote the remote path so spaces / shell-metachars survive.
	// Operators' kubeconfig paths are predictable (/etc/rancher/...) so
	// the simple quoting is fine; if M4 ever needs operator-controlled
	// paths, escape via shellescape.
	// `sudo -n cat ||` first: the canonical target (RKE2's
	// /etc/rancher/rke2/rke2.yaml) is root-owned 0600, and cloud
	// images disable root SSH — a plain `cat` as the login user gets
	// Permission denied (E2E finding 1, 2026-07-04). Passwordless
	// sudo is standard on cloud images; when it's unavailable the
	// fallback plain `cat` preserves the old behavior (root logins,
	// world-readable staged copies).
	q := shellSingleQuote(remotePath)
	runErr := runSessionWithCtx(ctx, session, client,
		fmt.Sprintf("sudo -n cat -- %s 2>/dev/null || cat -- %s", q, q))
	if runErr != nil {
		if errors.Is(runErr, ports.ErrFileTooLarge) {
			return nil, runErr
		}
		return nil, fmt.Errorf("ssh: fetch %s from %s: %w (stderr: %s)", remotePath, sshTarget(host), runErr, bytes.TrimSpace(stderr.Bytes()))
	}
	return out.Bytes(), nil
}

func (c *Client) Put(ctx context.Context, host ports.SSHHost, remotePath string, body []byte, mode uint32) error {
	if remotePath == "" {
		return fmt.Errorf("ssh: empty remote path")
	}
	if !strings.HasPrefix(remotePath, "/") {
		return fmt.Errorf("ssh: remote path must be absolute: %q", remotePath)
	}
	if len(body) > maxFetchBytes {
		return ports.ErrFileTooLarge
	}
	session, client, cleanup, err := c.dialSession(ctx, host)
	if err != nil {
		return err
	}
	defer cleanup()

	session.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	session.Stdout = io.Discard

	// install(1) handles atomic write + mode in one shot. -D would also
	// create parent dirs but we deliberately don't (callers know where
	// they're writing: /usr/local/sbin/, /etc/systemd/system/).
	cmd := fmt.Sprintf("install -m %04o /dev/stdin %s", mode, shellSingleQuote(remotePath))
	if err := runSessionWithCtx(ctx, session, client, cmd); err != nil {
		return fmt.Errorf("ssh: put %s on %s: %w (stderr: %s)",
			remotePath, sshTarget(host), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

// runSessionWithCtx executes `cmd` on the open session and waits for
// completion OR ctx cancellation. On ctx cancel, the session is
// closed (which interrupts session.Run) and the underlying client is
// torn down — ssh.ServerAliveInterval would be the cleaner long-
// term path but session.Close is good enough for the bootstrap CLI's
// "one short command per session" pattern.
func runSessionWithCtx(ctx context.Context, session *ssh.Session, client *ssh.Client, cmd string) error {
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Try to send SIGTERM first so the remote command gets a
		// chance to clean up; not all sshd builds honour Signal so
		// fall through to closing the session/client.
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		_ = client.Close()
		<-done // drain so the goroutine exits before we return
		return ctx.Err()
	}
}

// ---------- internals ----------

// dialSession dials the target (through any ProxyJump chain), opens one
// session, and returns it with the target client (so runSessionWithCtx
// can close it to interrupt a hung command on ctx-cancel) and a cleanup
// func that tears down the session, the target client, AND every jump
// client in the chain.
func (c *Client) dialSession(ctx context.Context, host ports.SSHHost) (*ssh.Session, *ssh.Client, func(), error) {
	client, cleanupClient, err := c.dialClient(ctx, host)
	if err != nil {
		return nil, nil, nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		cleanupClient()
		return nil, nil, nil, fmt.Errorf("ssh: new session on %s: %w", sshTarget(host), err)
	}
	cleanup := func() {
		_ = session.Close()
		cleanupClient()
	}
	return session, client, cleanup, nil
}

// dialClient dials host's *ssh.Client, tunneling through its ProxyJump
// chain if any (each hop reached through the previous; the target
// through the last). The returned cleanup closes the target client and
// every jump client in reverse order. Every hop's host key is verified
// through the same known_hosts callback.
func (c *Client) dialClient(ctx context.Context, host ports.SSHHost) (*ssh.Client, func(), error) {
	hostCfg, err := c.resolveHostConfig(host)
	if err != nil {
		return nil, nil, err
	}

	var chain []*ssh.Client
	cleanup := func() {
		for i := len(chain) - 1; i >= 0; i-- {
			_ = chain[i].Close()
		}
	}

	// Walk the jump chain: each hop is dialed THROUGH the previous one.
	var proxy *ssh.Client
	for _, hopSpec := range hostCfg.ProxyJump {
		hopCfg, herr := c.resolveHopConfig(parseJumpHop(hopSpec))
		if herr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("ssh: resolve jump host %q: %w", hopSpec, herr)
		}
		hopClient, herr := c.handshakeClient(ctx, proxy, hopCfg)
		if herr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("ssh: jump host %s: %w", net.JoinHostPort(hopCfg.Hostname, strconv.Itoa(hopCfg.Port)), herr)
		}
		chain = append(chain, hopClient)
		proxy = hopClient
	}

	// Finally dial the target through the last jump (or directly).
	target, err := c.handshakeClient(ctx, proxy, hostCfg)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	chain = append(chain, target)
	return target, cleanup, nil
}

// handshakeClient dials hostCfg's address — directly when proxy is nil,
// otherwise via a tunneled channel through proxy — performs the SSH
// handshake with ctx-cancel support, and returns the client. Honours
// ctx for TCP dial AND handshake (Timeout in ClientConfig caps the
// handshake; ctx cancel short-circuits via conn.Close from a watcher).
func (c *Client) handshakeClient(ctx context.Context, proxy *ssh.Client, hostCfg hostConfig) (*ssh.Client, error) {
	authMethods, err := c.loadAuth(hostCfg.IdentityFile)
	if err != nil {
		return nil, err
	}
	// M4-T06 reviewer P2 (security): host-key verification via
	// ~/.ssh/known_hosts. Pre-P2 used ssh.InsecureIgnoreHostKey(),
	// which is a MITM opening — an attacker on the path could deliver a
	// mangled kubeconfig that `fetch-kubeconfig` would happily merge as
	// cluster-admin creds. Now we refuse unknown/mismatched fingerprints
	// (or record+accept unknowns under WithAcceptNewHostKeys).
	hostKeyCallback, err := c.hostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("ssh: host-key verification setup: %w", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            hostCfg.User,
		Auth:            authMethods,
		Timeout:         30 * time.Second,
		HostKeyCallback: hostKeyCallback,
	}
	addr := net.JoinHostPort(hostCfg.Hostname, strconv.Itoa(hostCfg.Port))

	var conn net.Conn
	if proxy == nil {
		conn, err = c.dialContext(ctx, "tcp", addr)
	} else {
		// Tunnel a TCP channel to the target through the jump host.
		conn, err = proxy.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}

	// ctx watcher: close the underlying conn if ctx fires mid-handshake.
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-handshakeDone:
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	close(handshakeDone)
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ssh: handshake %s: %w", addr, ctx.Err())
		}
		return nil, fmt.Errorf("ssh: handshake %s: %w", addr, err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

type hostConfig struct {
	Hostname     string
	User         string
	Port         int
	IdentityFile string
	// ProxyJump is the resolved jump chain (each entry a
	// `[user@]host[:port]` spec), left-to-right. Empty → direct.
	ProxyJump []string
}

// hostKeyCallback returns the ssh.HostKeyCallback used for
// verifying the remote's host key. Reads `~/.ssh/known_hosts` (or
// `$SSH_KNOWN_HOSTS` when the operator overrides). Fails closed —
// an unknown host or a mismatch refuses the connection with a
// specific, actionable error containing an ssh-keyscan recipe.
//
// **Security contract** (M4-T06 reviewer P2): the prior
// `InsecureIgnoreHostKey` accepted any host key, opening the flow
// to MITM. Because `fetch-kubeconfig` persists cluster-admin
// credentials from the remote, MITM there is a total takeover;
// this callback closes that gap.
//
// **First-connect UX** (documented in the error): an operator who
// has never SSH'd to the target from this machine has no
// known_hosts entry. Rather than silently accept + trust (TOFU —
// which was the pre-P2 default), we refuse and print
// `ssh-keyscan -H <host> >> ~/.ssh/known_hosts` so the operator
// makes an EXPLICIT trust decision. Same as OpenSSH's
// `StrictHostKeyChecking=yes` posture.
//
// Cached on first call (khCallback field), so subsequent Run/Fetch/
// Put calls don't reparse the file.
func (c *Client) hostKeyCallback() (ssh.HostKeyCallback, error) {
	c.khMu.Lock()
	defer c.khMu.Unlock()
	// Test seam — non-nil override bypasses the real known_hosts
	// path. Production never sets this.
	if c.hostKeyCallbackForTest != nil {
		return c.hostKeyCallbackForTest, nil
	}
	if c.khCallback != nil {
		return c.khCallback, nil
	}

	path, err := resolveKnownHostsPath()
	if err != nil {
		return nil, err
	}
	// accept-new needs a known_hosts to append to; create an empty one
	// (+ ~/.ssh) if the operator has never SSH'd from this machine.
	if c.acceptNewHostKeys {
		if err := ensureKnownHostsFile(path); err != nil {
			return nil, fmt.Errorf("ssh: prepare known_hosts %s for accept-new: %w", path, err)
		}
	}
	base, err := knownhosts.New(path)
	if err != nil {
		// The file doesn't exist / is unreadable → operator hasn't
		// SSH'd to anything from this machine, or filesystem trouble.
		// Refuse loudly with the ssh-keyscan recipe.
		return nil, fmt.Errorf("known_hosts %s unavailable: %w\n\t(run `ssh-keyscan -H <host> >> %s` after verifying the host key out-of-band, or pass --ssh-accept-new-host-keys)",
			path, err, path)
	}

	// Wrap `base` so unknown-host + mismatch errors carry the
	// operator-actionable ssh-keyscan recipe. base's returned
	// *knownhosts.KeyError distinguishes the two cases (Want empty
	// → unknown; Want non-empty → mismatch = potential MITM).
	c.khCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				// Unknown host. accept-new records + trusts it (once per
				// process); strict mode refuses with the ssh-keyscan recipe.
				if c.acceptNewHostKeys {
					if aerr := c.recordNewHost(path, hostname, key); aerr != nil {
						return aerr
					}
					return nil
				}
				return fmt.Errorf("ssh host %s (%s) NOT in %s — refusing connection.\n\tRun the following AFTER verifying the host key out-of-band (e.g. via console access to the RKE2 master):\n\t\tssh-keyscan -H %s >> %s\n\t(or pass --ssh-accept-new-host-keys to trust-on-first-use)",
					hostname, remote, path,
					splitHostForKeyscan(hostname), path)
			}
			// Fingerprint MISMATCH is refused even under accept-new — this
			// is the MITM signal and trust is never relaxed for it.
			return fmt.Errorf("ssh host %s (%s) key MISMATCH against %s — refusing connection (possible MITM).\n\tIf the host was legitimately re-installed / re-keyed, remove the stale entry via:\n\t\tssh-keygen -R %s\n\tthen re-add via ssh-keyscan.",
				hostname, remote, path,
				splitHostForKeyscan(hostname))
		}
		return err
	}
	return c.khCallback, nil
}

// recordNewHost accepts key for hostname under accept-new. On the FIRST
// contact it appends the key to known_hosts and remembers it. On a
// REPEAT contact it compares: the same key is a no-op (the cached
// callback can't see our append, so this prevents a duplicate line); a
// DIFFERENT key for the same host is refused as a mismatch — accept-new
// trusts a host on first sight, never a key change (that is the MITM
// signal, and it must be caught within the process too, since the cached
// known_hosts callback won't).
func (c *Client) recordNewHost(path, hostname string, key ssh.PublicKey) error {
	c.khMu.Lock()
	defer c.khMu.Unlock()
	keyID := string(key.Marshal())
	if prev, seen := c.acceptedHosts[hostname]; seen {
		if prev == keyID {
			return nil // same key already accepted this run
		}
		return fmt.Errorf("ssh host %s key CHANGED from the one accepted earlier this run — refusing connection (possible MITM).\n\tIf the host was legitimately re-keyed, remove the stale entry via:\n\t\tssh-keygen -R %s",
			hostname, splitHostForKeyscan(hostname))
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: record new host key for %s: %w", hostname, err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return errors.Join(fmt.Errorf("ssh: record new host key for %s: %w", hostname, err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("ssh: sync new host key for %s: %w", hostname, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("ssh: close known_hosts after recording %s: %w", hostname, err)
	}
	if c.acceptedHosts == nil {
		c.acceptedHosts = map[string]string{}
	}
	c.acceptedHosts[hostname] = keyID
	return nil
}

// ensureKnownHostsFile creates the known_hosts file (and ~/.ssh) if
// missing so accept-new has somewhere to append.
func ensureKnownHostsFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// resolveKnownHostsPath honours `$SSH_KNOWN_HOSTS` (some operators
// use non-default paths) before falling back to `~/.ssh/known_hosts`.
func resolveKnownHostsPath() (string, error) {
	if p := os.Getenv("SSH_KNOWN_HOSTS"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// splitHostForKeyscan strips the port from `hostname:port` so the
// generated `ssh-keyscan -H <host>` recipe uses the bare hostname
// (ssh-keyscan takes a hostname, not a hostport). Some go-ssh
// versions pass the hostname bare, others include the port — cover
// both without depending on the version.
func splitHostForKeyscan(hostname string) string {
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		return h
	}
	return hostname
}

func (c *Client) resolveHostConfig(h ports.SSHHost) (hostConfig, error) {
	cfg := hostConfig{
		Hostname:     h.Hostname,
		User:         h.User,
		Port:         h.Port,
		IdentityFile: "",
	}

	// Pull ssh_config entries when an alias is set, OR when the
	// hostname matches a config block.
	target := h.Alias
	if target == "" {
		target = h.Hostname
	}

	if target != "" {
		// ssh_config.GetStrict auto-loads ~/.ssh/config on first call.
		if v, _ := ssh_config.GetStrict(target, "HostName"); v != "" && cfg.Hostname == "" {
			cfg.Hostname = v
		}
		if v, _ := ssh_config.GetStrict(target, "User"); v != "" && cfg.User == "" {
			cfg.User = v
		}
		if v, _ := ssh_config.GetStrict(target, "Port"); v != "" && cfg.Port == 0 {
			if p, err := strconv.Atoi(v); err == nil {
				cfg.Port = p
			}
		}
		if v, _ := ssh_config.GetStrict(target, "IdentityFile"); v != "" {
			cfg.IdentityFile = expandTilde(v)
		}
		// ProxyJump from ssh_config is honoured unless the caller
		// supplied an explicit override (applied below).
		if v, _ := ssh_config.GetStrict(target, "ProxyJump"); v != "" {
			cfg.ProxyJump = splitProxyJump(v)
		}
	}

	// An explicit SSHHost.ProxyJump overrides the ssh_config value.
	// "none" (OpenSSH's sentinel to disable a config-set ProxyJump)
	// clears the chain.
	if h.ProxyJump != "" {
		if strings.EqualFold(strings.TrimSpace(h.ProxyJump), "none") {
			cfg.ProxyJump = nil
		} else {
			cfg.ProxyJump = splitProxyJump(h.ProxyJump)
		}
	}

	// Apply defaults LAST so operator-supplied overrides win.
	if cfg.Hostname == "" {
		cfg.Hostname = h.Alias // last resort — operator passed alias only and ssh_config had nothing
	}
	if cfg.Hostname == "" {
		return cfg, fmt.Errorf("ssh: no hostname for %+v", h)
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	return cfg, nil
}

// splitProxyJump splits a ProxyJump value ("h1,h2" or "user@h:22")
// into its comma-separated hops, trimming spaces and dropping empties.
func splitProxyJump(v string) []string {
	var hops []string
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hops = append(hops, h)
		}
	}
	return hops
}

// parseJumpHop turns a `[user@]host[:port]` jump spec into an SSHHost.
// A spec with no `@`/`:` is treated as an alias so the hop's own
// ~/.ssh/config block (HostName/User/Port/IdentityFile) applies.
func parseJumpHop(spec string) ports.SSHHost {
	var user string
	if i := strings.Index(spec, "@"); i >= 0 {
		user = spec[:i]
		spec = spec[i+1:]
	}
	var port int
	// Split a trailing :port (bare host:port; IPv6 literals aren't
	// supported for jump hops in v1 — use an ssh_config alias instead).
	if h, p, err := net.SplitHostPort(spec); err == nil {
		if n, aerr := strconv.Atoi(p); aerr == nil {
			spec, port = h, n
		}
	}
	if user == "" && port == 0 {
		// Bare token → resolve everything from ~/.ssh/config.
		return ports.SSHHost{Alias: spec}
	}
	return ports.SSHHost{Hostname: spec, User: user, Port: port}
}

// resolveHopConfig resolves a jump hop's connection details. It reuses
// resolveHostConfig for the ssh_config lookups but a nested ProxyJump on
// the hop itself is NOT followed (v1 supports a single explicit chain on
// the target — specify all hops there), so any resolved chain is dropped.
func (c *Client) resolveHopConfig(h ports.SSHHost) (hostConfig, error) {
	cfg, err := c.resolveHostConfig(h)
	if err != nil {
		return cfg, err
	}
	cfg.ProxyJump = nil
	return cfg, nil
}

func loadAuthMethods(identityFile string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. ssh-agent — per B-004 this is the preferred path. A short
	// Timeout protects against a hung agent socket.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		d := &net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.Dial("unix", sock)
		if err == nil {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	// 2. IdentityFile from ssh_config, or OpenSSH's standard identity-file
	// defaults when the target is an explicit user@host/IP without a Host
	// block. We never accept key bytes or a --ssh-key flag from callers.
	for _, candidate := range identityFileCandidates(identityFile) {
		body, err := os.ReadFile(candidate)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(body)
			if err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("ssh: no auth methods available (set SSH_AUTH_SOCK, configure IdentityFile, or install a standard ~/.ssh/id_* key)")
	}
	return methods, nil
}

func identityFileCandidates(configured string) []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if configured == "" {
			return nil
		}
		return []string{expandTilde(configured)}
	}
	names := []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk", "id_dsa"}
	out := make([]string, 0, len(names)+1)
	seen := make(map[string]struct{}, len(names)+1)
	add := func(path string) {
		if path == "" {
			return
		}
		path = expandTilde(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	// kevinburke/ssh_config returns OpenSSH's default id_rsa value even
	// when no IdentityFile directive exists. Treat the resolved value as
	// the first candidate, not an exclusive one; OpenSSH itself continues
	// through its standard identity list unless IdentitiesOnly is set.
	add(configured)
	for _, name := range names {
		add(filepath.Join(home, ".ssh", name))
	}
	return out
}

func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

func sshTarget(h ports.SSHHost) string {
	if h.Alias != "" {
		return h.Alias
	}
	return h.Hostname
}

func shellSingleQuote(s string) string {
	// '\'' is the canonical shell single-quote escape: end the
	// current quote, insert an escaped quote, restart the quote.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cappedWriter enforces the maxFetchBytes ceiling. Returns
// ports.ErrFileTooLarge as soon as the threshold is crossed so the
// session can be torn down.
type cappedWriter struct {
	w   io.Writer
	max int
	n   int
}

func newCappedWriter(w io.Writer, max int) *cappedWriter {
	return &cappedWriter{w: w, max: max}
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n+len(p) > c.max {
		// Write up to the boundary then surface the error.
		left := c.max - c.n
		if left > 0 {
			_, _ = c.w.Write(p[:left])
			c.n = c.max
		}
		return left, ports.ErrFileTooLarge
	}
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}
