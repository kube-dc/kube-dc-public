package ssh

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

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
