package oidccutover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeSSH records every command and serves scripted answers per host.
type fakeSSH struct {
	config   map[string]string // host -> config.yaml body
	portUser map[string]string // host -> process name holding :6443
	readyz   map[string]string // host -> readyz code
	apiFlags map[string]string // host -> `ps` output
	kubecfg  map[string]bool   // host -> webhook kubeconfig present
	cmds     []string
	written  map[string]string
	failPut  map[string]bool
}

func newFakeSSH(hosts ...string) *fakeSSH {
	f := &fakeSSH{
		config: map[string]string{}, portUser: map[string]string{},
		readyz: map[string]string{}, apiFlags: map[string]string{},
		kubecfg: map[string]bool{}, written: map[string]string{},
		failPut: map[string]bool{},
	}
	for _, h := range hosts {
		f.config[h] = "cni: none\n"
		f.readyz[h] = "ok" // authenticated readyz answers the body, not a status code
		f.apiFlags[h] = "authentication-token-webhook-config-file=" + WebhookKubeconfigPath
		f.kubecfg[h] = true
	}
	return f
}

func (f *fakeSSH) Run(_ context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	f.cmds = append(f.cmds, host.Hostname+": "+cmd)
	switch {
	case strings.Contains(cmd, WebhookKubeconfigPath) && strings.Contains(cmd, "test -s"):
		if f.kubecfg[host.Hostname] {
			return []byte("present\n"), nil
		}
		return []byte("absent\n"), nil
	case strings.Contains(cmd, "ss -tlpnH"):
		if p := f.portUser[host.Hostname]; p != "" {
			return []byte(fmt.Sprintf(
				`LISTEN 0 4096 *:6443 *:* users:(("%s",pid=99,fd=20))`+"\n", p)), nil
		}
		return []byte(""), nil
	case strings.Contains(cmd, "readyz"):
		return []byte(f.readyz[host.Hostname]), nil
	// Match the real command's shape: it uses a `[k]ube-apiserver` grep guard,
	// so matching on "kube-apiserver" would MISS it and silently return the
	// zero value — which reads as "the flag never reached the process".
	case strings.Contains(cmd, "ps -eo args="):
		return []byte(f.apiFlags[host.Hostname]), nil
	case strings.Contains(cmd, "cp -n"), strings.Contains(cmd, "systemctl restart"),
		strings.Contains(cmd, "test -s "+RKE2ConfigPath), strings.Contains(cmd, "cp "+RKE2ConfigPath):
		return []byte(""), nil
	}
	return []byte(""), nil
}

func (f *fakeSSH) Fetch(_ context.Context, host ports.SSHHost, path string) ([]byte, error) {
	if path != RKE2ConfigPath {
		return nil, errors.New("unexpected fetch " + path)
	}
	return []byte(f.config[host.Hostname]), nil
}

func (f *fakeSSH) Put(_ context.Context, host ports.SSHHost, path string, body []byte, _ uint32) error {
	if f.failPut[host.Hostname] {
		return errors.New("write refused")
	}
	f.written[host.Hostname] = string(body)
	f.config[host.Hostname] = string(body)
	return nil
}

func nodes(hosts ...string) []Node {
	var out []Node
	for i, h := range hosts {
		out = append(out, Node{Name: fmt.Sprintf("cp-%d", i+1), Host: ports.SSHHost{Hostname: h}})
	}
	return out
}

func opts(f *fakeSSH, ns []Node) Options {
	return Options{SSH: f, Nodes: ns, ReadyTimeout: 300 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, Out: io.Discard}
}

// The happy path: every node gets wired, one at a time, and each restart is
// followed by a readiness check BEFORE the next node is touched.
func TestRun_WiresEveryNodeAndGatesEachRestart(t *testing.T) {
	f := newFakeSSH("a", "b", "c")
	res, err := Run(context.Background(), opts(f, nodes("a", "b", "c")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Wired) != 3 {
		t.Errorf("expected 3 nodes wired, got %v", res.Wired)
	}
	for _, h := range []string{"a", "b", "c"} {
		if !IsCutOver(f.written[h]) {
			t.Errorf("%s was not wired: %q", h, f.written[h])
		}
	}
	// Ordering contract: for each node, restart must precede that node's
	// readyz probe, and the NEXT node's restart must come after it.
	var order []string
	for _, c := range f.cmds {
		switch {
		case strings.Contains(c, "systemctl restart"):
			order = append(order, "restart:"+c[:1])
		case strings.Contains(c, "readyz"):
			order = append(order, "ready:"+c[:1])
		}
	}
	want := []string{"restart:a", "ready:a", "restart:b", "ready:b", "restart:c", "ready:c"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("restarts must be serialised and gated:\n got %v\nwant %v", order, want)
	}
}

// Idempotence: a second run must not restart a single apiserver.
func TestRun_SecondRunRestartsNothing(t *testing.T) {
	f := newFakeSSH("a", "b")
	if _, err := Run(context.Background(), opts(f, nodes("a", "b"))); err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	res, err := Run(context.Background(), opts(f, nodes("a", "b")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AlreadyWired) != 2 || len(res.Wired) != 0 {
		t.Errorf("re-run should be a no-op, got wired=%v already=%v", res.Wired, res.AlreadyWired)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "systemctl restart") {
			t.Errorf("re-run must not restart anything, but ran: %s", c)
		}
	}
}

// The :6443 conflict that killed a production node. A hostNetwork listener on a
// control-plane node means the apiserver cannot re-bind after the restart, so
// the node must be refused BEFORE anything is written.
func TestRun_RefusesWhenSomethingElseHolds6443(t *testing.T) {
	f := newFakeSSH("a")
	f.portUser["a"] = "envoy"
	_, err := Run(context.Background(), opts(f, nodes("a")))
	if err == nil {
		t.Fatal("a foreign :6443 listener must abort the node")
	}
	for _, want := range []string{"envoy", "cordon", "SO_REUSEPORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q, got: %v", want, err)
		}
	}
	if len(f.written) != 0 {
		t.Error("nothing may be written when the port check fails")
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "systemctl restart") {
			t.Error("must not restart after refusing")
		}
	}
}

// Pointing the apiserver at a webhook whose kubeconfig is missing makes it
// reject EVERY token, including its own controllers' — far worse than the 401s
// this fixes. So a missing file aborts before any write.
func TestRun_RefusesWhenTheWebhookKubeconfigIsMissing(t *testing.T) {
	f := newFakeSSH("a", "b")
	f.kubecfg["b"] = false
	_, err := Run(context.Background(), opts(f, nodes("a", "b")))
	if err == nil {
		t.Fatal("a missing webhook kubeconfig must abort")
	}
	if !strings.Contains(err.Error(), "cp-2") || !strings.Contains(err.Error(), "infra-core") {
		t.Errorf("the error must name the node and the cause, got: %v", err)
	}
	if len(f.written) != 0 {
		t.Error("preflight must run BEFORE any node is modified")
	}
}

// A node whose apiserver comes back without the flag is a silent 401 factory:
// RKE2 accepts a malformed config and drops the arg. Verify the live process.
func TestRun_FailsWhenTheFlagDidNotReachTheProcess(t *testing.T) {
	f := newFakeSSH("a")
	f.apiFlags["a"] = "" // came back with no webhook flag
	_, err := Run(context.Background(), opts(f, nodes("a")))
	if err == nil {
		t.Fatal("a missing flag on the live process must fail")
	}
	if !strings.Contains(err.Error(), "still returns 401") {
		t.Errorf("the error must say what it means for users, got: %v", err)
	}
}

// An apiserver that never returns must fail with the rollback instruction, and
// must NOT continue to the next node (that is how a whole control plane dies).
func TestRun_StopsAndNamesRollbackWhenAPIServerDoesNotReturn(t *testing.T) {
	f := newFakeSSH("a", "b")
	f.readyz["a"] = `The connection to the server 127.0.0.1:6443 was refused`
	_, err := Run(context.Background(), opts(f, nodes("a", "b")))
	if err == nil {
		t.Fatal("an apiserver that never becomes ready must fail")
	}
	if !strings.Contains(err.Error(), BackupSuffix) {
		t.Errorf("the error must name the snapshot to restore, got: %v", err)
	}
	if _, touched := f.written["b"]; touched {
		t.Error("must NOT proceed to the next control-plane node after a failure")
	}
}

// Dry run changes nothing anywhere.
func TestRun_DryRunWritesNothing(t *testing.T) {
	f := newFakeSSH("a", "b")
	o := opts(f, nodes("a", "b"))
	o.DryRun = true
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(f.written) != 0 {
		t.Errorf("dry run wrote %v", f.written)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "systemctl restart") || strings.Contains(c, "cp -n") {
			t.Errorf("dry run must not mutate: %s", c)
		}
	}
}

// The backup must use `cp -n` so a second run cannot overwrite the known-good
// original with an already-patched copy — that would make rollback restore the
// very config it is meant to undo.
func TestRun_SnapshotNeverClobbers(t *testing.T) {
	f := newFakeSSH("a")
	if _, err := Run(context.Background(), opts(f, nodes("a"))); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range f.cmds {
		if strings.Contains(c, "cp -n "+RKE2ConfigPath) {
			found = true
		}
		if strings.Contains(c, "cp "+RKE2ConfigPath+" ") && !strings.Contains(c, "cp -n") {
			t.Errorf("snapshot must be non-clobbering, got: %s", c)
		}
	}
	if !found {
		t.Error("no snapshot was taken before writing")
	}
}

func TestRun_RefusesAnEmptyNodeSet(t *testing.T) {
	if _, err := Run(context.Background(), opts(newFakeSSH(), nil)); err == nil {
		t.Fatal("an empty node set must be refused")
	}
}

// Rollback restores the snapshot and gates on the apiserver returning.
func TestRun_Rollback(t *testing.T) {
	f := newFakeSSH("a")
	o := opts(f, nodes("a"))
	o.Rollback = true
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	var restored, restarted bool
	for _, c := range f.cmds {
		if strings.Contains(c, "cp "+RKE2ConfigPath+BackupSuffix) {
			restored = true
		}
		if strings.Contains(c, "systemctl restart") {
			restarted = true
		}
	}
	if !restored || !restarted {
		t.Errorf("rollback must restore and restart (restored=%v restarted=%v)", restored, restarted)
	}
}
