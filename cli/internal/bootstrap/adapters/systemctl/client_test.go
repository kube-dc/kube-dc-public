package systemctl

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeExitError satisfies *exec.ExitError look-alike for tests that
// need to simulate a non-zero systemctl exit without actually running
// /bin/false. The stdlib makes exec.ExitError non-constructable, so we
// route through "go test"'s exec helper via a sub-binary in
// integration tests; here, we use exec.Command on a known-failing
// command to produce a genuine *exec.ExitError.
func makeExitError(t *testing.T) error {
	t.Helper()
	// `false` exits 1 reliably on POSIX; on systems where it's at a
	// different path, /bin/false is the canonical one.
	err := exec.Command("/bin/false").Run()
	if err == nil {
		t.Fatal("expected /bin/false to fail")
	}
	return err
}

func TestIsActive_ActiveUnit(t *testing.T) {
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if args[0] != "is-active" || args[1] != "rke2-server" {
				t.Errorf("unexpected args %v", args)
			}
			return []byte("active\n"), nil, nil
		},
		geteuid: func() int { return 1000 },
	}
	active, state, err := c.IsActive(context.Background(), "rke2-server")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !active {
		t.Errorf("active=false, want true")
	}
	if state != "active" {
		t.Errorf("state=%q, want active", state)
	}
}

func TestIsActive_InactiveUnit_NoError(t *testing.T) {
	exitErr := makeExitError(t)
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return []byte("inactive\n"), nil, exitErr
		},
		geteuid: func() int { return 1000 },
	}
	active, state, err := c.IsActive(context.Background(), "rke2-server")
	if err != nil {
		t.Fatalf("inactive should not surface as error, got %v", err)
	}
	if active {
		t.Errorf("active=true for inactive unit")
	}
	if state != "inactive" {
		t.Errorf("state=%q, want inactive", state)
	}
}

func TestIsActive_FailedUnit_NoError(t *testing.T) {
	exitErr := makeExitError(t)
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return []byte("failed\n"), nil, exitErr
		},
		geteuid: func() int { return 1000 },
	}
	active, state, err := c.IsActive(context.Background(), "rke2-server")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if active {
		t.Error("failed unit treated as active")
	}
	if state != "failed" {
		t.Errorf("state=%q, want failed", state)
	}
}

func TestIsActive_TransportError_Surfaces(t *testing.T) {
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			// Not a *exec.ExitError → must surface.
			return nil, nil, errors.New("exec: systemctl: not found")
		},
		geteuid: func() int { return 1000 },
	}
	_, _, err := c.IsActive(context.Background(), "rke2-server")
	if err == nil {
		t.Fatal("transport error should surface")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing underlying error: %v", err)
	}
}

func TestIsActive_EmptyUnit_Rejects(t *testing.T) {
	c := New()
	_, _, err := c.IsActive(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty unit")
	}
}

func TestRestart_NonRoot_ErrNeedsSudo(t *testing.T) {
	calls := 0
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			calls++
			return nil, nil, nil
		},
		geteuid: func() int { return 1000 },
	}
	err := c.Restart(context.Background(), "rke2-server")
	if !errors.Is(err, ports.ErrNeedsSudo) {
		t.Fatalf("want ErrNeedsSudo, got %v", err)
	}
	if calls != 0 {
		t.Errorf("Restart called exec %d times as non-root (should short-circuit)", calls)
	}
}

func TestRestart_Root_Success(t *testing.T) {
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if args[0] != "restart" {
				t.Errorf("args[0]=%q want restart", args[0])
			}
			return nil, nil, nil
		},
		geteuid: func() int { return 0 },
	}
	if err := c.Restart(context.Background(), "rke2-server"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
}

func TestRestart_Root_PropagatesStderr(t *testing.T) {
	exitErr := makeExitError(t)
	c := &Client{
		exec: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return nil, []byte("Failed to start rke2-server.service: Unit not found."), exitErr
		},
		geteuid: func() int { return 0 },
	}
	err := c.Restart(context.Background(), "rke2-server")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "Unit not found") {
		t.Errorf("stderr not propagated: %v", err)
	}
}

func TestRestart_EmptyUnit_Rejects(t *testing.T) {
	c := New()
	if err := c.Restart(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty unit")
	}
}
