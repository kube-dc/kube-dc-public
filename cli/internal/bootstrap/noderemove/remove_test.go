package noderemove

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeKubectl answers Run by first-substring match on the joined args
// and records every call (for order assertions).
type fakeKubectl struct {
	responses map[string]string
	errs      map[string]error
	calls     [][]string
}

func (f *fakeKubectl) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	j := strings.Join(args, " ")
	for k, v := range f.responses {
		if strings.Contains(j, k) {
			return []byte(v), f.errs[k]
		}
	}
	return nil, nil
}

func (f *fakeKubectl) firstIndex(sub string) int {
	for i, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return i
		}
	}
	return -1
}
func (f *fakeKubectl) ran(sub string) bool { return f.firstIndex(sub) >= 0 }

// fakeSSH records the teardown command.
type fakeSSH struct {
	ran     []string
	runErr  error
	putErr  error
	fetched []byte
}

func (f *fakeSSH) Run(_ context.Context, _ ports.SSHHost, cmd string) ([]byte, error) {
	f.ran = append(f.ran, cmd)
	return []byte("ok"), f.runErr
}
func (f *fakeSSH) Fetch(_ context.Context, _ ports.SSHHost, _ string) ([]byte, error) {
	return f.fetched, nil
}
func (f *fakeSSH) Put(_ context.Context, _ ports.SSHHost, _ string, _ []byte, _ uint32) error {
	return f.putErr
}

const (
	twoMastersNodesJSON = `{"items":[
	 {"metadata":{"name":"master-1","labels":{"node-role.kubernetes.io/etcd":"true","node-role.kubernetes.io/control-plane":"true"}}},
	 {"metadata":{"name":"master-2","labels":{"node-role.kubernetes.io/etcd":"true","node-role.kubernetes.io/control-plane":"true"}}},
	 {"metadata":{"name":"worker-1","labels":{"kubernetes.io/os":"linux"}}}
	]}`
	oneMasterNodesJSON = `{"items":[
	 {"metadata":{"name":"master-1","labels":{"node-role.kubernetes.io/etcd":"true","node-role.kubernetes.io/control-plane":"true"}}}
	]}`
	etcdPodsJSON = `{"items":[
	 {"metadata":{"name":"etcd-master-1"},"spec":{"nodeName":"master-1"}},
	 {"metadata":{"name":"etcd-master-2"},"spec":{"nodeName":"master-2"}}
	]}`
	memberListSimple = "aaa111, started, master-1-abc, https://10.0.0.1:2380, https://10.0.0.1:2379, false\n" +
		"bbb222, started, master-2-def, https://10.0.0.2:2380, https://10.0.0.2:2379, false\n"
)

func cpKubectl() *fakeKubectl {
	return &fakeKubectl{responses: map[string]string{
		"get nodes":     twoMastersNodesJSON,
		"get pods":      etcdPodsJSON,
		"member list":   memberListSimple,
		"member remove": "Member bbb222 removed",
		"cordon":        "node/master-2 cordoned",
		"drain":         "node/master-2 drained",
		"delete node":   "node \"master-2\" deleted",
	}}
}

func TestRemove_ControlPlane_MemberRemovedBeforeDrainDelete(t *testing.T) {
	k := cpKubectl()
	ssh := &fakeSSH{}
	var out bytes.Buffer
	err := Remove(context.Background(), Options{
		Kubectl: k, SSH: ssh, Node: ports.SSHHost{Hostname: "10.0.0.2"},
		NodeName: "master-2", Out: &out,
	})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	// etcd member removed with the RIGHT id...
	if !k.ran("member remove bbb222") {
		t.Errorf("expected member remove bbb222, calls: %v", k.calls)
	}
	// ...and BEFORE cordon/drain/delete (the safe order).
	mr, cordon, del := k.firstIndex("member remove"), k.firstIndex("cordon"), k.firstIndex("delete node")
	if !(mr < cordon && cordon < del) {
		t.Errorf("order wrong: member-remove=%d cordon=%d delete=%d", mr, cordon, del)
	}
	// member-list ran from the PEER pod (etcd-master-1), not the target's.
	if !k.ran("etcd-master-1 -- etcdctl") {
		t.Errorf("member ops should run via the peer pod etcd-master-1: %v", k.calls)
	}
	// node-side teardown ran (killall by default).
	if len(ssh.ran) != 1 || !strings.Contains(ssh.ran[0], "rke2-killall.sh") {
		t.Errorf("expected rke2-killall teardown, got %v", ssh.ran)
	}
}

func TestRemove_Worker_NoEtcdMemberRemoval(t *testing.T) {
	k := cpKubectl()
	ssh := &fakeSSH{}
	var out bytes.Buffer
	err := Remove(context.Background(), Options{
		Kubectl: k, SSH: ssh, Node: ports.SSHHost{Hostname: "10.0.0.9"},
		NodeName: "worker-1", Out: &out,
	})
	if err != nil {
		t.Fatalf("remove worker: %v", err)
	}
	if k.ran("member remove") || k.ran("get pods") {
		t.Errorf("worker removal must not touch etcd: %v", k.calls)
	}
	if !k.ran("cordon") || !k.ran("delete node") {
		t.Errorf("worker should still cordon+delete: %v", k.calls)
	}
}

func TestRemove_Uninstall_UsesUninstallScript(t *testing.T) {
	k := cpKubectl()
	ssh := &fakeSSH{}
	var out bytes.Buffer
	if err := Remove(context.Background(), Options{
		Kubectl: k, SSH: ssh, Node: ports.SSHHost{Hostname: "10.0.0.9"},
		NodeName: "worker-1", Uninstall: true, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if len(ssh.ran) != 1 || !strings.Contains(ssh.ran[0], "rke2-uninstall.sh") {
		t.Errorf("--uninstall should run rke2-uninstall.sh, got %v", ssh.ran)
	}
}

func TestRemove_LastControlPlane_Refused(t *testing.T) {
	k := &fakeKubectl{responses: map[string]string{"get nodes": oneMasterNodesJSON}}
	err := Remove(context.Background(), Options{Kubectl: k, NodeName: "master-1"})
	if !errors.Is(err, ErrLastControlPlane) {
		t.Errorf("want ErrLastControlPlane, got %v", err)
	}
	if k.ran("member remove") || k.ran("delete node") {
		t.Error("must not mutate when refusing the last control-plane")
	}
}

func TestRemove_NodeNotFound(t *testing.T) {
	k := cpKubectl()
	err := Remove(context.Background(), Options{Kubectl: k, NodeName: "ghost"})
	if !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("want ErrNodeNotFound, got %v", err)
	}
}

func TestRemove_NoEtcdPeerPod(t *testing.T) {
	k := cpKubectl()
	// Only the target's own etcd pod exists → no peer to remove from.
	k.responses["get pods"] = `{"items":[{"metadata":{"name":"etcd-master-2"},"spec":{"nodeName":"master-2"}}]}`
	err := Remove(context.Background(), Options{Kubectl: k, NodeName: "master-2"})
	if !errors.Is(err, ErrNoEtcdPeer) {
		t.Errorf("want ErrNoEtcdPeer, got %v", err)
	}
}

func TestRemove_EtcdMemberMissing(t *testing.T) {
	k := cpKubectl()
	k.responses["member list"] = "aaa111, started, master-1-abc, https://10.0.0.1:2380, https://10.0.0.1:2379, false\n"
	err := Remove(context.Background(), Options{Kubectl: k, NodeName: "master-2"})
	if !errors.Is(err, ErrEtcdMemberMissing) {
		t.Errorf("want ErrEtcdMemberMissing, got %v", err)
	}
	if k.ran("member remove") {
		t.Error("must not remove a member it couldn't identify")
	}
}

func TestRemove_DryRun_NoMutation(t *testing.T) {
	k := cpKubectl()
	ssh := &fakeSSH{}
	var out bytes.Buffer
	if err := Remove(context.Background(), Options{
		Kubectl: k, SSH: ssh, Node: ports.SSHHost{Hostname: "10.0.0.2"},
		NodeName: "master-2", DryRun: true, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []string{"member remove", "cordon", "drain", "delete node"} {
		if k.ran(mut) {
			t.Errorf("dry-run must not run %q", mut)
		}
	}
	if len(ssh.ran) != 0 {
		t.Errorf("dry-run must not SSH: %v", ssh.ran)
	}
	// Plan should still show the resolved etcd member + safe-order note.
	if !strings.Contains(out.String(), "bbb222") || !strings.Contains(out.String(), "removed FIRST") {
		t.Errorf("plan should resolve+announce the etcd member:\n%s", out.String())
	}
}

func TestRemove_SkipDrain(t *testing.T) {
	k := cpKubectl()
	var out bytes.Buffer
	if err := Remove(context.Background(), Options{
		Kubectl: k, NodeName: "worker-1", SkipDrain: true, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if k.ran("cordon") || k.ran("drain") {
		t.Errorf("--skip-drain must not cordon/drain: %v", k.calls)
	}
	if !k.ran("delete node") {
		t.Error("--skip-drain still deletes the node object")
	}
}

func TestRemove_TeardownFailureIsNonFatal(t *testing.T) {
	k := cpKubectl()
	ssh := &fakeSSH{runErr: errors.New("host unreachable")}
	var out bytes.Buffer
	// The cluster-side removal already succeeded; a teardown failure must
	// NOT fail the command (the node is already gone from the cluster).
	if err := Remove(context.Background(), Options{
		Kubectl: k, SSH: ssh, Node: ports.SSHHost{Hostname: "10.0.0.9"},
		NodeName: "worker-1", Out: &out,
	}); err != nil {
		t.Fatalf("teardown failure should be non-fatal: %v", err)
	}
	if !strings.Contains(out.String(), "WARNING") {
		t.Error("expected a teardown WARNING in output")
	}
}

func TestRemove_MissingDeps(t *testing.T) {
	if err := Remove(context.Background(), Options{NodeName: "x"}); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("nil Kubectl → ErrMissingDependency, got %v", err)
	}
	if err := Remove(context.Background(), Options{Kubectl: cpKubectl()}); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("empty NodeName → ErrMissingDependency, got %v", err)
	}
}
