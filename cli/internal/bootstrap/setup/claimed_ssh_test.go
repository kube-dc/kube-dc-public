package setup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type guardedTransportFixture struct {
	commands []string
	puts     int
	fail     bool
}

func (s *guardedTransportFixture) Run(_ context.Context, _ ports.SSHHost, c string) ([]byte, error) {
	s.commands = append(s.commands, c)
	if s.fail {
		return nil, errors.New("unknown remote outcome")
	}
	return []byte("done"), nil
}
func (s *guardedTransportFixture) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return errors.New("unguarded upload forbidden")
}
func (s *guardedTransportFixture) PutGuarded(_ context.Context, _ ports.SSHHost, _ string, _ []byte, _ uint32, guard func(string) (string, error)) error {
	s.puts++
	c, err := guard("sudo -n write-the-upload")
	if err != nil {
		return err
	}
	s.commands = append(s.commands, c)
	if s.fail {
		return errors.New("upload interrupted")
	}
	return nil
}
func (s *guardedTransportFixture) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) {
	return nil, nil
}
func (s *guardedTransportFixture) RunCapped(context.Context, ports.SSHHost, string, int) ([]byte, error) {
	return nil, nil
}
func (s *guardedTransportFixture) RunCappedWithHostKey(context.Context, ports.SSHHost, string, int) ([]byte, ports.SSHHostKeyEvidence, error) {
	return nil, ports.SSHHostKeyEvidence{}, nil
}
func TestClaimedTransportFencesStagingUploadsAndCleanup(t *testing.T) {
	c, review, _, _ := safetyReviewFixture(t)
	transport := &guardedTransportFixture{}
	claims, err := NewHostClaims(c, review, strings.Repeat("a", 64), strings.Repeat("b", 64), transport)
	if err != nil {
		t.Fatal(err)
	}
	guarded := claims.GuardedSSH()
	_, endpoint, err := claims.expected(c.Spec.Hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"sudo -n mktemp -d /var/lib/kube-dc-rke2.XXXXXXXXXX", "sudo -n curl https://example.test/reviewed", "sudo -n rm -rf -- /var/lib/kube-dc-rke2.fixture"} {
		if _, err := guarded.Run(context.Background(), endpoint, command); err != nil {
			t.Fatal(err)
		}
	}
	if err := guarded.Put(context.Background(), endpoint, "/private/upload", []byte("private-upload-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range transport.commands {
		for _, required := range []string{"flock -x -n 9", "fields[9]=started", "/var/lib/kube-dc", "wait", strings.Repeat("b", 64)} {
			if !strings.Contains(command, required) {
				t.Fatalf("unguarded consumer command lacks %s", required)
			}
		}
		if strings.Contains(command, "private-upload-bytes") {
			t.Fatal("body leaked into command")
		}
	}
	changed := endpoint
	changed.ExpectedHostKeySHA256 = "SHA256:changed"
	before := len(transport.commands)
	if _, err := guarded.Run(context.Background(), changed, "touch /unclaimed"); err == nil {
		t.Fatal("unclaimed endpoint accepted")
	}
	if len(transport.commands) != before {
		t.Fatal("unclaimed write reached SSH")
	}
}
func TestClaimedTransportStopsAfterAmbiguousOperation(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "upload"}[upload], func(t *testing.T) {
			c, review, _, _ := safetyReviewFixture(t)
			transport := &guardedTransportFixture{fail: true}
			claims, err := NewHostClaims(c, review, strings.Repeat("a", 64), strings.Repeat("b", 64), transport)
			if err != nil {
				t.Fatal(err)
			}
			guarded := claims.GuardedSSH()
			_, endpoint, _ := claims.expected(c.Spec.Hosts[0])
			if upload {
				err = guarded.Put(context.Background(), endpoint, "/private/upload", []byte("body"), 0600)
			} else {
				_, err = guarded.Run(context.Background(), endpoint, "sudo -n install")
			}
			if err == nil {
				t.Fatal("ambiguous write accepted")
			}
			before := len(transport.commands)
			transport.fail = false
			if _, err := guarded.Run(context.Background(), endpoint, "sudo -n rm -rf -- /private/staging"); err == nil {
				t.Fatal("cleanup proceeded after ambiguous operation")
			}
			if len(transport.commands) != before {
				t.Fatal("further write reached transport")
			}
		})
	}
}
