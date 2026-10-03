package setup

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type journeySSH struct {
	*hostSSHStub
	mu     sync.Mutex
	active map[string]bool
	pins   map[string]string
	config []byte
	events []string
}

func (s *journeySSH) hostName(host ports.SSHHost) (string, error) {
	name := host.Hostname
	if name == "" {
		name = host.Alias
	}
	if s.pins[name] == "" || host.ExpectedHostKeySHA256 != s.pins[name] {
		return "", fmt.Errorf("SSH target does not match its reviewed key")
	}
	return name, nil
}

func (s *journeySSH) RunCappedWithHostKey(ctx context.Context, host ports.SSHHost, cmd string, limit int) ([]byte, ports.SSHHostKeyEvidence, error) {
	name, err := s.hostName(host)
	if err != nil {
		return nil, ports.SSHHostKeyEvidence{}, err
	}
	evidence := ports.SSHHostKeyEvidence{Address: name + ":22", Algorithm: "ssh-ed25519", FingerprintSHA256: s.pins[name]}
	s.mu.Lock()
	active := s.active[name]
	s.mu.Unlock()
	if strings.Contains(cmd, " get node ") {
		if name != "server-1" || !active {
			return nil, evidence, fmt.Errorf("API query needs an active reviewed first server")
		}
		_, after, _ := strings.Cut(cmd, " get node ")
		node := strings.Fields(after)[0]
		s.mu.Lock()
		registered := s.active[node]
		s.mu.Unlock()
		if !registered {
			return nil, evidence, nil
		}
		ip, role := "192.0.2.20", ""
		if node == "server-1" {
			ip, role = "192.0.2.10", `"node-role.kubernetes.io/control-plane":"true"`
		}
		body := fmt.Sprintf(`{"kind":"Node","metadata":{"name":%q,"uid":%q,"labels":{%s}},"status":{"addresses":[{"type":"InternalIP","address":%q}]}}`, node, "uid-"+node, role, ip)
		return []byte(body), evidence, nil
	}
	if active && strings.HasPrefix(cmd, "sudo -n systemctl show -p LoadState -p ActiveState rke2-") {
		service := "rke2-agent"
		if name == "server-1" {
			service = "rke2-server"
		}
		if strings.HasSuffix(cmd, service) {
			return []byte("LoadState=loaded\nActiveState=active\n"), evidence, nil
		}
	}
	body, err := s.hostSSHStub.RunCapped(ctx, host, cmd, limit)
	return body, evidence, err
}

func (s *journeySSH) RunCapped(ctx context.Context, host ports.SSHHost, cmd string, limit int) ([]byte, error) {
	body, _, err := s.RunCappedWithHostKey(ctx, host, cmd, limit)
	return body, err
}

func (s *journeySSH) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := s.hostName(host)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(cmd, "systemctl is-active rke2-") {
		service := "rke2-agent.service"
		if name == "server-1" {
			service = "rke2-server.service"
		}
		if s.active[name] && strings.Contains(cmd, service) {
			return []byte("active"), nil
		}
		return []byte("inactive"), nil
	}
	script := "bash /tmp/kube-dc-rke2-install-agent.sh"
	if name == "server-1" {
		script = "bash /tmp/kube-dc-rke2-install-server.sh"
	}
	if strings.Contains(cmd, script) {
		if name == "worker-1" && !strings.Contains(cmd, "'fixture-join-secret' '192.0.2.10' '192.0.2.20'") {
			return nil, fmt.Errorf("worker join used the wrong credentials or addresses")
		}
		s.active[name] = true
		s.events = append(s.events, "install:"+name)
		return []byte("installed"), nil
	}
	return nil, fmt.Errorf("unexpected legacy command")
}

func (s *journeySSH) Put(ctx context.Context, host ports.SSHHost, path string, body []byte, mode uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := s.hostName(host)
	wantPath := "/tmp/kube-dc-rke2-install-agent.sh"
	if name == "server-1" {
		wantPath = "/tmp/kube-dc-rke2-install-server.sh"
	}
	if err != nil || len(body) == 0 || mode != 0o755 || path != wantPath {
		return fmt.Errorf("unsafe installer write")
	}
	s.mu.Lock()
	s.events = append(s.events, "put:"+name)
	s.mu.Unlock()
	return nil
}

func (s *journeySSH) Fetch(ctx context.Context, host ports.SSHHost, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := s.hostName(host)
	if err != nil {
		return nil, err
	}
	if name != "server-1" {
		return nil, fmt.Errorf("unreviewed credential source")
	}
	s.mu.Lock()
	active := s.active[name]
	s.mu.Unlock()
	if !active {
		return nil, fmt.Errorf("first server is not active")
	}
	switch path {
	case "/var/lib/rancher/rke2/server/node-token":
		return []byte("fixture-join-secret\n"), nil
	case "/etc/rancher/rke2/rke2.yaml":
		return s.config, nil
	default:
		return nil, fmt.Errorf("unexpected credential path")
	}
}

func TestRunHostPhaseWithRKE2AdapterAndPrivateHandoff(t *testing.T) {
	c, _, base := rke2PermitFixture(t)
	spec := c.Spec
	spec.Hosts[1].HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	var err error
	c, err = Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	worker := c.Spec.Hosts[1]
	base.answers["worker-1"] = hostAnswers(strings.Repeat("b", 32), worker.ManagementAddress, "inactive")
	base.answers["worker-1"][freeSpaceCommand] = base.answers["server-1"][freeSpaceCommand]
	ssh := &journeySSH{hostSSHStub: base.hostSSHStub, active: make(map[string]bool),
		pins: map[string]string{"server-1": c.Spec.Hosts[0].HostKeySHA256, "worker-1": worker.HostKeySHA256}}
	remote := childConfig(t, c)
	remote.Clusters["default"] = remote.Clusters[c.Spec.Name]
	remote.AuthInfos["default"] = remote.AuthInfos[c.Spec.Name]
	remote.Contexts["default"] = &clientcmdapi.Context{Cluster: "default", AuthInfo: "default"}
	delete(remote.Clusters, c.Spec.Name)
	delete(remote.AuthInfos, c.Spec.Name)
	delete(remote.Contexts, c.Spec.Name)
	remote.Clusters["default"].Server = "https://127.0.0.1:6443"
	remote.CurrentContext = "default"
	ssh.config, err = clientcmd.Write(*remote)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := InspectHosts(context.Background(), c, ssh)
	if err != nil || inventory.State != "observed" || len(inventory.Hosts) != 2 {
		t.Fatalf("fixture hosts are not fully observed: %+v, %v", inventory.Unresolved, err)
	}
	// The production reducer cannot approve apply yet. This test supplies
	// synthetic qualification to exercise the downstream host engine.
	inventory.Unresolved = nil
	readiness := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, State: "ready", ReadyToApply: true,
		InputHash: c.InputHash, PlanHash: plan.PlanHash, ReleaseSHA256: c.ReleaseSHA256}
	path := filepath.Join(t.TempDir(), "private", "child.yaml")
	handoff, err := NewExclusiveKubeconfigHandoff(c, path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewRKE2HostOperations(c, ssh, handoff)
	if err != nil {
		t.Fatal(err)
	}
	engine.verifyAttempts = 1
	result, err := RunHostPhase(context.Background(), c, plan, readiness, inventory, engine)
	if err != nil || result.State != "complete" || !reflect.DeepEqual(result.Completed, []string{"rke2-first-server", "rke2-join-worker-1", "fetch-kubeconfig"}) {
		t.Fatalf("guarded RKE2 journey failed: %+v, %v", result, err)
	}
	if !reflect.DeepEqual(ssh.events, []string{"put:server-1", "install:server-1", "put:worker-1", "install:worker-1"}) {
		t.Fatalf("host writes did not follow the reviewed order: %v", ssh.events)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("child kubeconfig was not persisted privately: %v, %v", info, err)
	}
}
