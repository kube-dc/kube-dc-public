// Package ssh is the real ports.SSHClient adapter.
//
// **Auth flow** (per B-004): try ssh-agent first (via
// `$SSH_AUTH_SOCK`), fall back to `IdentityFile` from `~/.ssh/config`.
// NEVER accept a `--ssh-key` flag — operators put keys in their ssh
// config; the CLI doesn't manage them.
//
// Host config (User, Port, ProxyJump, …) comes from `~/.ssh/config`
// via kevinburke/ssh_config. SSHHost.Hostname / User / Port act as
// overrides when the operator passes them explicitly.
//
// **No ProxyJump in v1**. Multi-hop tunnels are a v2 concern; the
// adapter returns a clear error if the config asks for ProxyJump so
// operators see the gap immediately rather than getting a confusing
// connect failure.
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
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

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
}

// New returns a Client using the operator's `~/.ssh/config`.
func New() *Client {
	d := &net.Dialer{Timeout: 30 * time.Second}
	return &Client{
		dialContext: d.DialContext,
		loadAuth:    loadAuthMethods,
	}
}

// Compile-time assertion.
var _ ports.SSHClient = (*Client)(nil)

// ---------- ports.SSHClient ----------

func (c *Client) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	if cmd == "" {
		return nil, fmt.Errorf("ssh: empty cmd")
	}
	session, conn, err := c.dialSession(ctx, host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out

	if err := runSessionWithCtx(ctx, session, conn, cmd); err != nil {
		return out.Bytes(), fmt.Errorf("ssh: run %q on %s: %w", cmd, sshTarget(host), err)
	}
	return out.Bytes(), nil
}

func (c *Client) Fetch(ctx context.Context, host ports.SSHHost, remotePath string) ([]byte, error) {
	if remotePath == "" {
		return nil, fmt.Errorf("ssh: empty remote path")
	}
	// Use `cat` over an ssh session — works against any POSIX shell;
	// avoids vendoring SFTP which would double the SSH dep surface.
	session, conn, err := c.dialSession(ctx, host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = newCappedWriter(&out, maxFetchBytes)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	// Single-quote the remote path so spaces / shell-metachars survive.
	// Operators' kubeconfig paths are predictable (/etc/rancher/...) so
	// the simple quoting is fine; if M4 ever needs operator-controlled
	// paths, escape via shellescape.
	runErr := runSessionWithCtx(ctx, session, conn, fmt.Sprintf("cat -- %s", shellSingleQuote(remotePath)))
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
	session, conn, err := c.dialSession(ctx, host)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer session.Close()

	session.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	session.Stdout = io.Discard

	// install(1) handles atomic write + mode in one shot. -D would also
	// create parent dirs but we deliberately don't (callers know where
	// they're writing: /usr/local/sbin/, /etc/systemd/system/).
	cmd := fmt.Sprintf("install -m %04o /dev/stdin %s", mode, shellSingleQuote(remotePath))
	if err := runSessionWithCtx(ctx, session, conn, cmd); err != nil {
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

// dialSession resolves host config, builds the ssh.ClientConfig, dials,
// and opens a single session. Honours ctx for TCP dial AND for the
// SSH handshake (Timeout in ClientConfig caps handshake; ctx cancel
// short-circuits via conn.Close from a watcher goroutine).
func (c *Client) dialSession(ctx context.Context, host ports.SSHHost) (*ssh.Session, *ssh.Client, error) {
	hostCfg, err := c.resolveHostConfig(host)
	if err != nil {
		return nil, nil, err
	}
	authMethods, err := c.loadAuth(hostCfg.IdentityFile)
	if err != nil {
		return nil, nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User: hostCfg.User,
		Auth: authMethods,
		// Bound the handshake so an unreachable / misbehaving peer
		// doesn't park us indefinitely. ctx cancel still wins via
		// the watcher goroutine below.
		Timeout:         30 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // M4-T06 trusts the operator's ssh_config + agent — TOFU is acceptable for bootstrap; v2 plugs in ~/.ssh/known_hosts
	}

	addr := net.JoinHostPort(hostCfg.Hostname, strconv.Itoa(hostCfg.Port))

	conn, err := c.dialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}

	// ctx watcher: close the underlying conn if ctx fires mid-
	// handshake. ssh.NewClientConn doesn't take a ctx itself.
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
		// If the underlying error came from a ctx-driven close,
		// surface ctx.Err() so callers can distinguish cancellation
		// from a real handshake failure.
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("ssh: handshake %s: %w", addr, ctx.Err())
		}
		return nil, nil, fmt.Errorf("ssh: handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("ssh: new session on %s: %w", addr, err)
	}
	return session, client, nil
}

type hostConfig struct {
	Hostname     string
	User         string
	Port         int
	IdentityFile string
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
		// ProxyJump unsupported in v1 — fail loudly.
		if v, _ := ssh_config.GetStrict(target, "ProxyJump"); v != "" {
			return cfg, fmt.Errorf("ssh: %s requires ProxyJump=%s; multi-hop tunnels not supported in v1", target, v)
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

	// 2. IdentityFile from ssh_config (or operator-supplied) fallback.
	if identityFile != "" {
		body, err := os.ReadFile(identityFile)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(body)
			if err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("ssh: no auth methods available (set SSH_AUTH_SOCK or configure IdentityFile)")
	}
	return methods, nil
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
