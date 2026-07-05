package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// testPubKey generates a throwaway ed25519 ssh.PublicKey for host-key tests.
func testPubKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh pubkey: %v", err)
	}
	return sshPub
}

// Compile-time assertion.
var _ ports.SSHClient = (*Client)(nil)

func TestResolveHostConfig_OperatorOverridesWin(t *testing.T) {
	c := New()
	cfg, err := c.resolveHostConfig(ports.SSHHost{
		Hostname: "explicit.example.com",
		User:     "operator",
		Port:     2222,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Hostname != "explicit.example.com" {
		t.Errorf("Hostname=%q", cfg.Hostname)
	}
	if cfg.User != "operator" {
		t.Errorf("User=%q", cfg.User)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port=%d", cfg.Port)
	}
}

func TestResolveHostConfig_Defaults(t *testing.T) {
	c := New()
	cfg, err := c.resolveHostConfig(ports.SSHHost{Hostname: "h.example.com"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.User != "root" {
		t.Errorf("default User=%q want root", cfg.User)
	}
	if cfg.Port != 22 {
		t.Errorf("default Port=%d want 22", cfg.Port)
	}
}

func TestResolveHostConfig_NoHostname_Errors(t *testing.T) {
	c := New()
	if _, err := c.resolveHostConfig(ports.SSHHost{}); err == nil {
		t.Fatal("empty host should be rejected")
	}
}

func TestExpandTilde(t *testing.T) {
	// "~/foo" should expand to $HOME/foo (or unchanged if $HOME
	// unset). Just check it's no longer literal "~/foo".
	out := expandTilde("~/.ssh/id_ed25519")
	if strings.HasPrefix(out, "~/") {
		t.Errorf("expandTilde did not expand: %q", out)
	}
	// Path without "~/" prefix is unchanged.
	if expandTilde("/etc/passwd") != "/etc/passwd" {
		t.Error("non-tilde path mutated")
	}
}

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"/etc/rancher/rke2/rke2.yaml": "'/etc/rancher/rke2/rke2.yaml'",
		"path with spaces.yaml":       "'path with spaces.yaml'",
		"can't quote me":              `'can'\''t quote me'`,
	}
	for in, want := range cases {
		got := shellSingleQuote(in)
		if got != want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCappedWriter_AtBoundary(t *testing.T) {
	var buf bytes.Buffer
	cw := newCappedWriter(&buf, 5)
	n, err := cw.Write([]byte("hello"))
	if err != nil {
		t.Errorf("err at boundary: %v", err)
	}
	if n != 5 {
		t.Errorf("n=%d want 5", n)
	}
}

func TestCappedWriter_OverflowReturnsErrFileTooLarge(t *testing.T) {
	var buf bytes.Buffer
	cw := newCappedWriter(&buf, 4)
	_, err := cw.Write([]byte("hello"))
	if !errors.Is(err, ports.ErrFileTooLarge) {
		t.Errorf("err=%v want ErrFileTooLarge", err)
	}
}

func TestCappedWriter_AccumulatedOverflow(t *testing.T) {
	var buf bytes.Buffer
	cw := newCappedWriter(&buf, 10)
	if _, err := cw.Write([]byte("hello ")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := cw.Write([]byte("worldworldworld"))
	if !errors.Is(err, ports.ErrFileTooLarge) {
		t.Errorf("second write should overflow: %v", err)
	}
}

func TestSSHTarget(t *testing.T) {
	if got := sshTarget(ports.SSHHost{Alias: "bastion"}); got != "bastion" {
		t.Errorf("Alias case: %q", got)
	}
	if got := sshTarget(ports.SSHHost{Hostname: "h.example.com"}); got != "h.example.com" {
		t.Errorf("Hostname fallback: %q", got)
	}
	if got := sshTarget(ports.SSHHost{Alias: "bastion", Hostname: "h"}); got != "bastion" {
		t.Errorf("Alias should win: %q", got)
	}
}

func TestRun_EmptyCmd_Rejected(t *testing.T) {
	c := New()
	if _, err := c.Run(context.Background(), ports.SSHHost{Hostname: "h"}, ""); err == nil {
		t.Fatal("empty cmd should be rejected")
	}
}

func TestFetch_EmptyPath_Rejected(t *testing.T) {
	c := New()
	if _, err := c.Fetch(context.Background(), ports.SSHHost{Hostname: "h"}, ""); err == nil {
		t.Fatal("empty remote path should be rejected")
	}
}

// ctx-aware dial: cancelling ctx mid-dial must short-circuit. We
// substitute a dialContext stub that blocks until ctx fires + a
// loadAuth stub that returns an empty (but non-error) method list
// so the dial path is the failure point under test.
func TestRun_ContextCancel_TerminatesDial(t *testing.T) {
	c := &Client{
		dialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Block until ctx fires; the test cancels after a short
			// delay so we exit quickly.
			<-ctx.Done()
			return nil, ctx.Err()
		},
		loadAuth: func(_ string) ([]ssh.AuthMethod, error) {
			return []ssh.AuthMethod{ssh.Password("test")}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := c.Run(ctx, ports.SSHHost{Hostname: "h.example.com"}, "echo hi")
	if err == nil {
		t.Fatal("expected ctx.Cancel to terminate dial")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error should surface cancellation: %v", err)
	}
}

// ---------- ProxyJump ----------

func TestParseJumpHop(t *testing.T) {
	cases := map[string]ports.SSHHost{
		"bastion":             {Alias: "bastion"},
		"user@192.0.2.1":      {Hostname: "192.0.2.1", User: "user"},
		"user@192.0.2.1:2222": {Hostname: "192.0.2.1", User: "user", Port: 2222},
		"192.0.2.1:2222":      {Hostname: "192.0.2.1", Port: 2222},
	}
	for spec, want := range cases {
		got := parseJumpHop(spec)
		if got != want {
			t.Errorf("parseJumpHop(%q) = %+v, want %+v", spec, got, want)
		}
	}
}

func TestSplitProxyJump(t *testing.T) {
	got := splitProxyJump(" a , b ,,c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitProxyJump = %v", got)
	}
	if splitProxyJump("") != nil {
		t.Error("empty → nil")
	}
}

func TestResolveHostConfig_ProxyJumpOverrideAndNone(t *testing.T) {
	c := New()
	// Explicit override → chain populated.
	cfg, err := c.resolveHostConfig(ports.SSHHost{Hostname: "10.0.0.9", ProxyJump: "bastion,b2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProxyJump) != 2 || cfg.ProxyJump[0] != "bastion" || cfg.ProxyJump[1] != "b2" {
		t.Errorf("ProxyJump chain = %v", cfg.ProxyJump)
	}
	// "none" clears it (OpenSSH sentinel).
	cfg2, _ := c.resolveHostConfig(ports.SSHHost{Hostname: "10.0.0.9", ProxyJump: "none"})
	if len(cfg2.ProxyJump) != 0 {
		t.Errorf("ProxyJump=none should clear the chain, got %v", cfg2.ProxyJump)
	}
}

// With a ProxyJump chain, the FIRST network dial must be the JUMP host
// (not the target). The injected dialer records + fails fast so we assert
// ordering without a real SSH server (the tunnel itself is live-validated).
func TestDialClient_ProxyJumpDialsJumpHostFirst(t *testing.T) {
	var dialed []string
	c := &Client{
		dialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			return nil, fmt.Errorf("stop after recording")
		},
		loadAuth:               func(_ string) ([]ssh.AuthMethod, error) { return []ssh.AuthMethod{ssh.Password("x")}, nil },
		hostKeyCallbackForTest: ssh.InsecureIgnoreHostKey(),
	}
	_, _, err := c.dialClient(context.Background(),
		ports.SSHHost{Hostname: "10.0.0.9", ProxyJump: "bastion@192.0.2.1:2222"})
	if err == nil {
		t.Fatal("expected dial to fail (fake dialer)")
	}
	if len(dialed) == 0 || dialed[0] != "192.0.2.1:2222" {
		t.Errorf("first dial should be the jump host 192.0.2.1:2222, got %v", dialed)
	}
}

// ---------- host-key accept-new ----------

func TestHostKeyCallback_AcceptNew_RecordsUnknownHost(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS", kh)

	c := New(WithAcceptNewHostKeys())
	cb, err := c.hostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	key := testPubKey(t)
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.5"), Port: 22}
	if err := cb("198.51.100.5:22", addr, key); err != nil {
		t.Fatalf("accept-new should accept an unknown host: %v", err)
	}
	// The key must now be recorded.
	body, _ := os.ReadFile(kh)
	if !strings.Contains(string(body), "198.51.100.5") {
		t.Errorf("known_hosts should contain the new host, got:\n%s", body)
	}
	// A second contact must NOT append a duplicate (process dedup).
	_ = cb("198.51.100.5:22", addr, key)
	if n := strings.Count(string(mustRead(t, kh)), "198.51.100.5"); n != 1 {
		t.Errorf("expected exactly 1 recorded line, got %d", n)
	}
}

// accept-new trusts a host on FIRST sight but not a key CHANGE: a
// different key for a host already accepted THIS process must be refused
// (the cached known_hosts callback can't see our append, so the in-memory
// guard has to catch it) and must not append a second line.
func TestHostKeyCallback_AcceptNew_ChangedKeySameProcessRefused(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS", kh)

	c := New(WithAcceptNewHostKeys())
	cb, err := c.hostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.5"), Port: 22}
	if err := cb("198.51.100.5:22", addr, testPubKey(t)); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	// Same host, a DIFFERENT key, same process → must refuse.
	err = cb("198.51.100.5:22", addr, testPubKey(t))
	if err == nil {
		t.Fatal("a changed key for an already-accepted host must be refused (MITM)")
	}
	if !strings.Contains(err.Error(), "CHANGED") && !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("want a mismatch/changed refusal, got %v", err)
	}
	// The changed key must NOT have been appended.
	if n := strings.Count(string(mustRead(t, kh)), "198.51.100.5"); n != 1 {
		t.Errorf("changed key must not be appended, expected 1 line, got %d", n)
	}
}

func TestHostKeyCallback_AcceptNew_StillRefusesMismatch(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	// Pre-seed a DIFFERENT key for the host → any other key is a mismatch.
	existing := testPubKey(t)
	line := fmt.Sprintf("198.51.100.9 %s\n", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(existing))))
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", kh)

	c := New(WithAcceptNewHostKeys())
	cb, err := c.hostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.9"), Port: 22}
	err = cb("198.51.100.9:22", addr, testPubKey(t)) // a NEW, different key
	if err == nil {
		t.Fatal("accept-new must STILL refuse a host-key mismatch (MITM)")
	}
	if !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("want a MISMATCH refusal, got %v", err)
	}
}

func TestHostKeyCallback_Strict_RefusesUnknown(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(kh, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", kh)

	c := New() // strict (no accept-new)
	cb, err := c.hostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 22}
	err = cb("198.51.100.7:22", addr, testPubKey(t))
	if err == nil || !strings.Contains(err.Error(), "NOT in") {
		t.Errorf("strict mode must refuse unknown host with ssh-keyscan hint, got %v", err)
	}
}

// syncBuffer is what Run uses to capture combined stdout+stderr, because
// the ssh library writes the two streams from separate goroutines. This
// guards the fix for the data race that silently dropped stdout over a
// ProxyJump tunnel — run under -race. A plain bytes.Buffer here trips the
// race detector; syncBuffer must not.
func TestSyncBuffer_ConcurrentWritesNoRaceNoLoss(t *testing.T) {
	var sb syncBuffer
	const writers, perWriter = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := sb.Write([]byte("x")); err != nil {
					t.Errorf("write: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if got := len(sb.Bytes()); got != writers*perWriter {
		t.Errorf("lost bytes under concurrency: got %d want %d", got, writers*perWriter)
	}
	// Bytes() returns a copy — mutating it must not corrupt the buffer.
	snap := sb.Bytes()
	if len(snap) > 0 {
		snap[0] = 'Z'
	}
	if sb.Bytes()[0] == 'Z' {
		t.Error("Bytes() must return a copy, not the live backing array")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
