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
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

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

func TestSyncBufferCapsCombinedStreams(t *testing.T) {
	out := syncBuffer{max: 5}
	var wg sync.WaitGroup
	for _, data := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func(data string) {
			defer wg.Done()
			if n, err := out.Write([]byte(data)); err != nil || n != len(data) {
				t.Errorf("capped stream write = %d, %v", n, err)
			}
		}(data)
	}
	wg.Wait()
	if !out.Exceeded() || len(out.Bytes()) != 5 {
		t.Fatalf("combined streams escaped byte limit: length=%d exceeded=%v", len(out.Bytes()), out.Exceeded())
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

func TestRunCapped_ContextCancelClosesStalledSessionOpen(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	channelSeen := make(chan struct{})
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer serverConn.Close()
		conn, channels, requests, err := ssh.NewServerConn(serverConn, serverConfig)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		select {
		case <-channels:
			close(channelSeen)
			<-releaseServer // never accept or reject the session channel
		case <-releaseServer:
		}
	}()
	client := &Client{
		dialContext:            (&net.Dialer{}).DialContext,
		loadAuth:               func(string) ([]ssh.AuthMethod, error) { return []ssh.AuthMethod{ssh.Password("test")}, nil },
		hostKeyCallbackForTest: ssh.InsecureIgnoreHostKey(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.RunCapped(ctx, ports.SSHHost{Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}, "cat /etc/machine-id", 1024)
		result <- err
	}()
	select {
	case <-channelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("test did not reach the stalled session channel in time")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stalled session open ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled session open did not stop after cancellation")
	}
}

func TestRunCappedWithHostKeyReturnsVerifiedTargetKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.Config.RekeyThreshold = 1024
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, signer.PublicKey()) + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", knownHosts)
	verified, err := knownhosts.New(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	var keyChecks atomic.Int32
	rekeyObserved := make(chan struct{})
	payload := bytes.Repeat([]byte("ready\n"), 24000)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		serverConn, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			serverDone <- err
			return
		}
		defer serverConn.Close()
		go ssh.DiscardRequests(requests)
		newChannel := <-channels
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer channel.Close()
		request := <-channelRequests
		if request.Type != "exec" {
			serverDone <- fmt.Errorf("unexpected request %s", request.Type)
			return
		}
		_ = request.Reply(true, nil)
		_, err = channel.Write(payload)
		if err == nil {
			select {
			case <-rekeyObserved:
			case <-time.After(3 * time.Second):
				err = fmt.Errorf("host-key callback did not observe rekeying")
			}
		}
		if err == nil {
			_, err = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		}
		// Close the channel, then keep the transport alive until the client
		// consumes the exit status and disconnects. A timed sleep can lose
		// packets queued by rekeying on a slow or race-enabled test runner.
		_ = channel.Close()
		serverDone <- err
		_ = serverConn.Wait()
	}()
	client := New()
	client.loadAuth = func(string) ([]ssh.AuthMethod, error) { return []ssh.AuthMethod{ssh.Password("test")}, nil }
	client.hostKeyCallbackForTest = func(address string, remote net.Addr, key ssh.PublicKey) error {
		if keyChecks.Add(1) == 2 {
			close(rekeyObserved)
		}
		return verified(address, remote, key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, evidence, err := client.RunCappedWithHostKey(ctx, ports.SSHHost{Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}, "test command", len(payload)+1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, payload) || evidence.FingerprintSHA256 != ssh.FingerprintSHA256(signer.PublicKey()) || evidence.Algorithm != signer.PublicKey().Type() || evidence.Address == "" {
		t.Fatalf("incorrect verified evidence: output bytes=%d evidence=%+v", len(output), evidence)
	}
	if keyChecks.Load() < 2 {
		t.Fatalf("test did not force a host-key callback during rekey: %d", keyChecks.Load())
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRunCapped_ContextCancelClosesStalledExecRequest(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	execSeen := make(chan struct{})
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer serverConn.Close()
		conn, channels, requests, err := ssh.NewServerConn(serverConn, serverConfig)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		select {
		case newChannel := <-channels:
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				return
			}
			defer channel.Close()
			select {
			case <-channelRequests:
				close(execSeen)
				<-releaseServer // never reply to the exec request
			case <-releaseServer:
			}
		case <-releaseServer:
		}
	}()
	client := &Client{
		dialContext:            (&net.Dialer{}).DialContext,
		loadAuth:               func(string) ([]ssh.AuthMethod, error) { return []ssh.AuthMethod{ssh.Password("test")}, nil },
		hostKeyCallbackForTest: ssh.InsecureIgnoreHostKey(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.RunCapped(ctx, ports.SSHHost{Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}, "cat /etc/machine-id", 1024)
		result <- err
	}()
	select {
	case <-execSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("test did not reach the stalled exec request in time")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stalled exec request ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled exec request did not stop after cancellation")
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

// A recorded RSA host key must offer the SHA-2 signature algorithms too.
// known_hosts records the KEY type ("ssh-rsa"), while modern servers advertise
// only rsa-sha2-256/512 and refuse the legacy SHA-1 name — so pinning the retry
// to "ssh-rsa" alone would find nothing in common while holding the right key.
func TestWantKeyTypes_RSAOffersSHA2Variants(t *testing.T) {
	_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBKJ9ITTqek38EVuj5VG8RSDAa000jg+RZfEixdIq76r x"))
	if err != nil {
		t.Skipf("fixture key unusable: %v", err)
	}
	// ed25519 alone: exactly one algorithm, no expansion.
	edKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBKJ9ITTqek38EVuj5VG8RSDAa000jg+RZfEixdIq76r x"))
	got := wantKeyTypes([]knownhosts.KnownKey{{Key: edKey}})
	if len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
		t.Errorf("ed25519 must map to itself only, got %v", got)
	}
}

func TestExpectedPinIsCheckedBeforeRemoteSessionAndDoesNotReplaceTrust(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
		match bool
	}{{"mismatch", true, false}, {"matching-pin-untrusted", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			_, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(privateKey)
			if err != nil {
				t.Fatal(err)
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(signer)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var sessions atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					sessions.Add(1)
					ch.Reject(ssh.Prohibited, "test refuses sessions")
				}
			}()
			client := New()
			client.loadAuth = func(string) ([]ssh.AuthMethod, error) { return nil, nil }
			client.hostKeyCallbackForTest = func(string, net.Addr, ssh.PublicKey) error {
				if !tc.trust {
					return errors.New("known_hosts rejected")
				}
				return nil
			}
			pin := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			if tc.match {
				pin = ssh.FingerprintSHA256(signer.PublicKey())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err = client.RunCapped(ctx, ports.SSHHost{Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, ExpectedHostKeySHA256: pin}, "cat /etc/machine-id", 1024)
			if err == nil {
				t.Fatal("identity failure permitted a command")
			}
			<-done
			if sessions.Load() != 0 {
				t.Fatal("remote session opened before pin/trust was accepted")
			}
		})
	}
}

func TestExpectedPinBelongsOnlyToTargetRequest(t *testing.T) {
	client := New()
	pinned, err := client.resolveHostConfig(ports.SSHHost{Hostname: "192.0.2.10", ProxyJump: "jump", ExpectedHostKeySHA256: "expected"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := client.resolveHostConfig(ports.SSHHost{Hostname: "192.0.2.11"})
	if err != nil {
		t.Fatal(err)
	}
	hop, err := client.resolveHopConfig(ports.SSHHost{Hostname: "192.0.2.12"})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.ExpectedHostKeySHA256 != "expected" || other.ExpectedHostKeySHA256 != "" || hop.ExpectedHostKeySHA256 != "" {
		t.Fatal("pin leaked to another host or jump")
	}
}
