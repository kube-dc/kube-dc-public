package connectcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeSSH struct {
	out []byte
	err error
	ran []string
}

func (f *fakeSSH) Run(_ context.Context, _ ports.SSHHost, cmd string) ([]byte, error) {
	f.ran = append(f.ran, cmd)
	return f.out, f.err
}
func (f *fakeSSH) Fetch(_ context.Context, _ ports.SSHHost, _ string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSSH) Put(_ context.Context, _ ports.SSHHost, _ string, _ []byte, _ uint32) error {
	return nil
}

const okProbe = "USER=ubuntu\nOS=Ubuntu 24.04.4 LTS\nSUDO=yes\nIP=198.51.100.5\n"

func TestCheck_ReachableWithSudo(t *testing.T) {
	var out bytes.Buffer
	ssh := &fakeSSH{out: []byte(okProbe)}
	err := Check(context.Background(), Options{SSH: ssh, Host: ports.SSHHost{Hostname: "198.51.100.5"}, Out: &out})
	if err != nil {
		t.Fatalf("healthy node: %v", err)
	}
	s := out.String()
	for _, want := range []string{"✓ SSH reach+auth", "ubuntu", "198.51.100.5", "advertise-address", "✓ passwordless sudo", "ready"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
	}
}

func TestCheck_Unreachable(t *testing.T) {
	var out bytes.Buffer
	ssh := &fakeSSH{err: fmt.Errorf("ssh: dial 10.0.0.9:22: connection refused")}
	err := Check(context.Background(), Options{SSH: ssh, Host: ports.SSHHost{Hostname: "10.0.0.9"}, Out: &out})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("want ErrUnreachable, got %v", err)
	}
	if !strings.Contains(out.String(), "✗ SSH reach+auth") {
		t.Errorf("report should show reach failure:\n%s", out.String())
	}
}

func TestCheck_NoPasswordlessSudo(t *testing.T) {
	var out bytes.Buffer
	ssh := &fakeSSH{out: []byte("USER=ubuntu\nOS=Ubuntu\nSUDO=no\nIP=198.51.100.5\n")}
	err := Check(context.Background(), Options{SSH: ssh, Host: ports.SSHHost{Hostname: "198.51.100.5"}, Out: &out})
	if !errors.Is(err, ErrNoPasswordlessSudo) {
		t.Errorf("want ErrNoPasswordlessSudo, got %v", err)
	}
	if !strings.Contains(out.String(), "✗ passwordless sudo") {
		t.Errorf("report should flag missing sudo:\n%s", out.String())
	}
}

func TestCheck_ReportsJumpAndHandlesMissingIP(t *testing.T) {
	var out bytes.Buffer
	ssh := &fakeSSH{out: []byte("USER=root\nOS=Debian\nSUDO=yes\nIP=\n")}
	err := Check(context.Background(), Options{
		SSH:  ssh,
		Host: ports.SSHHost{Hostname: "10.0.0.9", ProxyJump: "root@bastion"},
		Out:  &out,
	})
	if err != nil {
		t.Fatalf("healthy (no IP) should still pass: %v", err)
	}
	if !strings.Contains(out.String(), "via jump:") || !strings.Contains(out.String(), "bastion") {
		t.Errorf("report should note the jump:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "could not detect") {
		t.Errorf("missing IP should be a soft warning:\n%s", out.String())
	}
}

func TestCheck_MissingSSH(t *testing.T) {
	if err := Check(context.Background(), Options{}); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("nil SSH → ErrMissingDependency, got %v", err)
	}
}

func TestParseFields(t *testing.T) {
	m := parseFields([]byte("A=1\n B = 2 \nnoeq\nC=\n"))
	if m["A"] != "1" || m["B"] != "2" || m["C"] != "" {
		t.Errorf("parseFields = %v", m)
	}
	if _, ok := m["noeq"]; ok {
		t.Error("lines without = must be skipped")
	}
}
