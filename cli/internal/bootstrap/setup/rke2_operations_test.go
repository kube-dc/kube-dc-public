package setup

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type kubeconfigSSHStub struct {
	*evidenceHostSSHStub
	config  []byte
	fetched bool
}

func (s *kubeconfigSSHStub) Fetch(_ context.Context, host ports.SSHHost, path string) ([]byte, error) {
	if host.ExpectedHostKeySHA256 == "" || path != "/etc/rancher/rke2/rke2.yaml" {
		return nil, fmt.Errorf("kubeconfig source is not pinned")
	}
	s.fetched = true
	return s.config, nil
}

func rke2OperationsFixture(t *testing.T) (Compiled, *evidenceHostSSHStub) {
	t.Helper()
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].HostKeySHA256 = "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	s.Hosts = append(s.Hosts, Host{ID: "worker-1", Role: "agent", SSHAlias: "admin@worker-1", ManagementAddress: "192.0.2.20", HostKeySHA256: s.Hosts[0].HostKeySHA256})
	config := baseConfig("demo") + "KUBE_DC_INIT_NODE_NICS=server-1=eth0\nEXT_NET_VLAN_ID=0\nEXT_NET_INTERFACE=eth0\n"
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	stub := &evidenceHostSSHStub{hostSSHStub: &hostSSHStub{answers: map[string]map[string]string{"server-1": {}}},
		evidence: ports.SSHHostKeyEvidence{Address: "server-1:22", Algorithm: "ssh-ed25519", FingerprintSHA256: s.Hosts[0].HostKeySHA256}}
	return c, stub
}

func rke2PermitFixture(t *testing.T) (Compiled, HostObservation, *evidenceHostSSHStub) {
	t.Helper()
	c, stub := rke2OperationsFixture(t)
	record := completeReleaseRecord(c.Spec)
	record.Profiles[0].MaxAgents = 1
	writeReleaseTestRecord(t, c.Spec, record)
	var err error
	c, err = Compile(c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	answers := hostAnswers(strings.Repeat("a", 32), "192.0.2.10", "inactive")
	answers[freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 200000000000 20000000000 180000000000 10% /\n"
	stub.answers["server-1"] = answers
	primary := primaryServer(c.Spec.Hosts)
	reviewed := inspectHost(context.Background(), stub, primary, clusterinit.ModeInstall, true)
	if reviewed.State != "observed" || reviewed.Facts.HostKey == nil {
		t.Fatalf("permit fixture has no reviewed host: %+v", reviewed)
	}
	return c, reviewed, stub
}

func TestRKE2HostOperationsMapsReviewedNetworkAndPins(t *testing.T) {
	c, stub := rke2OperationsFixture(t)
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.Spec.Hosts)
	options := engine.serverOptions(primary)
	if options.NodeName != primary.ID || options.NodeIP != primary.ManagementAddress || options.Host.ExpectedHostKeySHA256 != primary.HostKeySHA256 ||
		options.Domain != c.Init.Domain || options.RKE2Version != c.Spec.Release.RKE2Version ||
		options.PodCIDR != "10.100.0.0/16" || options.ServiceCIDR != "10.101.0.0/16" || options.ClusterDNS != "10.101.0.11" || options.Force || options.DryRun {
		t.Fatalf("RKE2 options differ from reviewed setup: %+v", options)
	}
	if _, err := NewRKE2HostOperations(c, stub, nil); err == nil {
		t.Fatal("missing protected kubeconfig handoff was accepted")
	}
}

func TestRKE2HostWritePermitIsExclusiveUntilWriteFinishes(t *testing.T) {
	c, reviewed, stub := rke2PermitFixture(t)
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.Spec.Hosts)
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.takePermit(primary); err != nil {
		t.Fatal(err)
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err == nil || !strings.Contains(err.Error(), "active installation write") {
		t.Fatalf("second recheck could supersede an active write: %v", err)
	}
	if _, err := engine.takePermit(primary); err == nil || !strings.Contains(err.Error(), "active installation write") {
		t.Fatalf("second writer entered the same host: %v", err)
	}
	worker := c.Spec.Hosts[1]
	engine.mu.Lock()
	engine.permits[worker.ID] = hostWritePermit{host: worker, issuedAt: time.Now()}
	engine.mu.Unlock()
	if _, err := engine.takePermit(worker); err != nil {
		t.Fatalf("independent worker write was blocked: %v", err)
	}
	engine.finishWrite(primary)
	if _, err := engine.takePermit(primary); err == nil {
		t.Fatal("consumed primary permit was reused")
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatalf("primary could not be rechecked after its write ended: %v", err)
	}
	engine.finishWrite(worker)
}

func TestRKE2HostOperationsBlocksExistingNodeBeforeJoin(t *testing.T) {
	c, stub := rke2OperationsFixture(t)
	primary := primaryServer(c.Spec.Hosts)
	worker := c.Spec.Hosts[1]
	query := "sudo -n /var/lib/rancher/rke2/bin/kubectl --kubeconfig /etc/rancher/rke2/rke2.yaml get node worker-1 --ignore-not-found -o json"
	stub.answers["server-1"][query] = `{"kind":"Node","metadata":{"name":"worker-1","uid":"old-node"}}`
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.JoinWorker(context.Background(), primary, worker); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing node name did not block worker join: %v", err)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("existing node was followed by an installer write: %+v", stub.hostSSHStub)
	}
	worker.ManagementAddress = "192.0.2.21"
	runs := stub.runs
	if err := engine.JoinWorker(context.Background(), primary, worker); err == nil || stub.runs != runs {
		t.Fatalf("unreviewed host reached the API or installer: %v", err)
	}
}

func TestRKE2HostOperationsJoinNeedsFreshRecheckAfterNodeAbsence(t *testing.T) {
	c, stub := rke2OperationsFixture(t)
	primary := primaryServer(c.Spec.Hosts)
	worker := c.Spec.Hosts[1]
	query := "sudo -n /var/lib/rancher/rke2/bin/kubectl --kubeconfig /etc/rancher/rke2/rke2.yaml get node worker-1 --ignore-not-found -o json"
	stub.answers["server-1"][query] = ""
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.JoinWorker(context.Background(), primary, worker); err == nil || !strings.Contains(err.Error(), "one-use pre-write recheck") {
		t.Fatalf("worker join without a recheck was accepted: %v", err)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("worker join reached credentials or installer: %+v", stub.hostSSHStub)
	}
}

func TestRKE2HostOperationsVerifiesFirstServerWithoutNodeReady(t *testing.T) {
	c, stub := rke2OperationsFixture(t)
	primary := primaryServer(c.Spec.Hosts)
	stub.answers["server-1"]["sudo -n systemctl show -p LoadState -p ActiveState rke2-server"] = "LoadState=loaded\nActiveState=active\n"
	query := "sudo -n /var/lib/rancher/rke2/bin/kubectl --kubeconfig /etc/rancher/rke2/rke2.yaml get node server-1 --ignore-not-found -o json"
	stub.answers["server-1"][query] = `{"kind":"Node","metadata":{"name":"server-1","uid":"new-server","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}],"conditions":[{"type":"Ready","status":"False"}]}}`
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	engine.verifyAttempts = 1
	if _, err := engine.FirstServerEvidence(); err == nil {
		t.Fatal("first-server UID was available before verification")
	}
	if err := engine.VerifyFirstServer(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	verified, err := engine.FirstServerEvidence()
	if err != nil || verified.UID != "new-server" || verified.Name != primary.ID || verified.InternalIP != primary.ManagementAddress {
		t.Fatalf("first-server identity was not retained: %+v, %v", verified, err)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("verification made an unsafe SSH call: %+v", stub.hostSSHStub)
	}
	stub.answers["server-1"][query] = ""
	if err := engine.VerifyFirstServer(context.Background(), primary); err == nil {
		t.Fatal("missing first-server node was accepted on re-verification")
	}
	if _, err := engine.FirstServerEvidence(); err == nil {
		t.Fatal("failed re-verification retained stale node identity")
	}
}

func TestRKE2HostOperationsHandsOffKubeconfigInMemory(t *testing.T) {
	c, base := rke2OperationsFixture(t)
	config := clientcmdapi.NewConfig()
	config.Clusters["default"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:6443"}
	config.AuthInfos["default"] = &clientcmdapi.AuthInfo{Token: "synthetic-secret"}
	config.Contexts["default"] = &clientcmdapi.Context{Cluster: "default", AuthInfo: "default"}
	config.CurrentContext = "default"
	body, err := clientcmd.Write(*config)
	if err != nil {
		t.Fatal(err)
	}
	ssh := &kubeconfigSSHStub{evidenceHostSSHStub: base, config: body}
	var received *clientcmdapi.Config
	engine, err := NewRKE2HostOperations(c, ssh, func(cfg *clientcmdapi.Config) error { received = cfg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.FetchKubeconfig(context.Background(), primaryServer(c.Spec.Hosts)); err != nil {
		t.Fatal(err)
	}
	if !ssh.fetched || received == nil || received.CurrentContext != c.Spec.Name || received.Clusters[c.Spec.Name].Server != "https://kube-api.example.test:6443" ||
		received.AuthInfos[c.Spec.Name].Token != "synthetic-secret" || base.writeCalls != 0 {
		t.Fatalf("kubeconfig was not handed off privately: fetched=%v received=%v", ssh.fetched, received != nil)
	}
}

func TestRKE2HostOperationsRequiresOneUseFreshRecheck(t *testing.T) {
	c, reviewed, stub := rke2PermitFixture(t)
	engine, err := NewRKE2HostOperations(c, stub, func(*clientcmdapi.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.Spec.Hosts)
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("direct install bypassed the pre-write recheck: %v", err)
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	permit := engine.permits[primary.ID]
	permit.issuedAt = time.Now().Add(-maxHostWritePermitAge - time.Second)
	engine.permits[primary.ID] = permit
	engine.mu.Unlock()
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("expired permit reached installer: %v", err)
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatal(err)
	}
	stub.evidence.FingerprintSHA256 = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err == nil {
		t.Fatal("changed host key passed a repeated pre-write check")
	}
	stub.evidence.FingerprintSHA256 = primary.HostKeySHA256
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("failed recheck left a usable permit: %v", err)
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatal(err)
	}
	stub.evidence.FingerprintSHA256 = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("host key drift after permit reached installer: %v", err)
	}
	stub.evidence.FingerprintSHA256 = primary.HostKeySHA256
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatalf("failed pre-write recheck left a usable permit: %v", err)
	}
	if err := engine.RecheckHost(context.Background(), primary, reviewed); err != nil {
		t.Fatal(err)
	}
	// The fake refuses the legacy install write. Reaching it once proves the
	// permit was consumed; another call must stop before any legacy operation.
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.writeCalls != 1 {
		t.Fatalf("fresh permit did not reach exactly one installer attempt: %v, writes=%d", err, stub.writeCalls)
	}
	runs, writes := stub.uncappedRuns, stub.writeCalls
	if err := engine.InstallFirstServer(context.Background(), primary); err == nil || stub.uncappedRuns != runs || stub.writeCalls != writes {
		t.Fatalf("consumed permit was reused: %v, runs=%d, writes=%d", err, stub.uncappedRuns, stub.writeCalls)
	}
}
