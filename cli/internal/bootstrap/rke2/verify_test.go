package rke2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const verifyTestPin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type verifySSH struct {
	answers map[string][]string
	calls   map[string]int
	key     string
	writes  int
}

func (s *verifySSH) RunCappedWithHostKey(ctx context.Context, host ports.SSHHost, cmd string, maxBytes int) ([]byte, ports.SSHHostKeyEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, ports.SSHHostKeyEvidence{}, err
	}
	if maxBytes != maxNodeVerifyOutput {
		return nil, ports.SSHHostKeyEvidence{}, fmt.Errorf("wrong output bound")
	}
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	key := host.Hostname + ":" + cmd
	answers, ok := s.answers[key]
	if !ok || len(answers) == 0 {
		return nil, ports.SSHHostKeyEvidence{}, fmt.Errorf("unexpected read on %s", key)
	}
	index := s.calls[key]
	s.calls[key]++
	if index >= len(answers) {
		index = len(answers) - 1
	}
	pin := s.key
	if pin == "" {
		pin = verifyTestPin
	}
	return []byte(answers[index]), ports.SSHHostKeyEvidence{Address: host.Hostname + ":22", Algorithm: "ssh-ed25519", FingerprintSHA256: pin}, nil
}
func (s *verifySSH) RunCapped(context.Context, ports.SSHHost, string, int) ([]byte, error) {
	return nil, errors.New("verification must request host-key evidence")
}
func (s *verifySSH) Run(context.Context, ports.SSHHost, string) ([]byte, error) {
	return nil, errors.New("uncapped verification read")
}
func (s *verifySSH) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) {
	s.writes++
	return nil, errors.New("unexpected fetch")
}
func (s *verifySSH) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	s.writes++
	return errors.New("unexpected write")
}

func verifyOptions(ssh *verifySSH, role string) VerifyNodeOptions {
	return VerifyNodeOptions{
		SSH: ssh, Node: ports.SSHHost{Hostname: "192.0.2.20", ExpectedHostKeySHA256: verifyTestPin},
		APIHost:  ports.SSHHost{Hostname: "192.0.2.10", ExpectedHostKeySHA256: verifyTestPin},
		NodeName: "worker-1", Role: role, InternalIP: "10.0.0.20", Attempts: 4, RetryDelay: time.Nanosecond,
	}
}

func nodeJSON(name, role, ip string) string {
	labels := "{}"
	if role == "server" {
		labels = `{"node-role.kubernetes.io/control-plane":"true"}`
	}
	return fmt.Sprintf(`{"kind":"Node","metadata":{"name":%q,"uid":"node-uid-1","labels":%s},"status":{"addresses":[{"type":"InternalIP","address":%q}],"conditions":[{"type":"Ready","status":"False"}]}}`, name, labels, ip)
}

func TestVerifyRegisteredNodeWaitsForServiceAndAPIWithoutNodeReady(t *testing.T) {
	service := "sudo -n systemctl show -p LoadState -p ActiveState rke2-agent"
	query := nodeQueryCommand("worker-1")
	ssh := &verifySSH{answers: map[string][]string{
		"192.0.2.20:" + service: {"LoadState=loaded\nActiveState=activating\n", "LoadState=loaded\nActiveState=active\n"},
		"192.0.2.10:" + query:   {"", nodeJSON("worker-1", "agent", "10.0.0.20")},
	}}
	got, err := VerifyRegisteredNode(context.Background(), verifyOptions(ssh, "agent"))
	if err != nil || got.Name != "worker-1" || got.UID != "node-uid-1" || got.InternalIP != "10.0.0.20" || got.Role != "agent" {
		t.Fatalf("NotReady node was not verified: %+v, %v", got, err)
	}
	if ssh.calls["192.0.2.20:"+service] != 3 || ssh.calls["192.0.2.10:"+query] != 2 || ssh.writes != 0 {
		t.Fatalf("unexpected verification reads or writes: %+v", ssh)
	}
}

func TestVerifyRegisteredNodeRejectsWrongEvidence(t *testing.T) {
	service := "sudo -n systemctl show -p LoadState -p ActiveState rke2-server"
	query := nodeQueryCommand("worker-1")
	for _, tc := range []struct {
		name string
		body string
		key  string
	}{
		{"wrong-role", nodeJSON("worker-1", "agent", "10.0.0.20"), ""},
		{"wrong-ip", nodeJSON("worker-1", "server", "10.0.0.21"), ""},
		{"wrong-name", nodeJSON("other", "server", "10.0.0.20"), ""},
		{"wrong-host-key", nodeJSON("worker-1", "server", "10.0.0.20"), "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssh := &verifySSH{key: tc.key, answers: map[string][]string{
				"192.0.2.20:" + service: {"LoadState=loaded\nActiveState=active\n"},
				"192.0.2.10:" + query:   {tc.body},
			}}
			o := verifyOptions(ssh, "server")
			o.Attempts = 1
			if _, err := VerifyRegisteredNode(context.Background(), o); err == nil {
				t.Fatal("invalid node evidence passed verification")
			}
			if ssh.writes != 0 {
				t.Fatal("verification wrote to a host")
			}
		})
	}
}

func TestCheckNodeAbsentBlocksExistingNameAndInjection(t *testing.T) {
	host := ports.SSHHost{Hostname: "192.0.2.10", ExpectedHostKeySHA256: verifyTestPin}
	query := "192.0.2.10:" + nodeQueryCommand("worker-1")
	ssh := &verifySSH{answers: map[string][]string{query: {""}}}
	if err := CheckNodeAbsent(context.Background(), ssh, host, "worker-1"); err != nil {
		t.Fatal(err)
	}
	ssh.answers[query] = []string{nodeJSON("worker-1", "agent", "10.0.0.20")}
	ssh.calls = nil
	if err := CheckNodeAbsent(context.Background(), ssh, host, "worker-1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing node was accepted: %v", err)
	}
	before := ssh.calls[query]
	if err := CheckNodeAbsent(context.Background(), ssh, host, "worker-1;id"); err == nil || ssh.calls[query] != before {
		t.Fatalf("unsafe node name reached SSH: %v, %+v", err, ssh.calls)
	}
}

func TestVerifyRegisteredNodeCancellation(t *testing.T) {
	ssh := &verifySSH{}
	o := verifyOptions(ssh, "agent")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyRegisteredNode(ctx, o); !errors.Is(err, context.Canceled) || len(ssh.calls) != 0 {
		t.Fatalf("canceled verification read a host: %v, %+v", err, ssh.calls)
	}
}
