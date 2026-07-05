package rke2

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const testToken = "K10secret::server:deadbeef" // fixture, not a real token

func joinBase(ssh ports.SSHClient, out *bytes.Buffer) JoinWorkerOptions {
	return JoinWorkerOptions{
		SSH:          ssh,
		Worker:       ports.SSHHost{User: "ubuntu", Hostname: "192.0.2.20"},
		WorkerName:   "worker-1",
		ControlPlane: ports.SSHHost{User: "ubuntu", Hostname: "192.0.2.10"},
		Out:          out,
	}
}

// A control-plane fake: node-token at the well-known path, route-src for
// CP + worker, rke2-agent inactive, agent installer succeeds.
func joinSSH() *fakeSSH {
	return &fakeSSH{
		runs: map[string]string{
			"ip -4 route get":                "1.1.1.1 dev eth0 src 198.51.100.5",
			"systemctl is-active rke2-agent": "inactive",
			"bash " + remoteAgentScriptPath:  "agent joined",
		},
		fetch: map[string]string{nodeTokenPath: testToken + "\n"},
	}
}

func TestRemoteAgentCmd(t *testing.T) {
	got := remoteAgentCmd(map[string]string{"NODE_NAME": "w1"}, "/tmp/a.sh", testToken, "10.0.0.3", "10.0.0.9")
	if !strings.HasPrefix(got, "sudo -n env ") || !strings.Contains(got, "bash /tmp/a.sh ") {
		t.Errorf("shape: %q", got)
	}
	// token, cp-host, node-ip appended as quoted positional args in order.
	if !strings.Contains(got, "bash /tmp/a.sh '"+testToken+"' '10.0.0.3' '10.0.0.9'") {
		t.Errorf("positional args wrong: %q", got)
	}
}

func TestJoinWorker_DryRun_RedactsTokenNoMutation(t *testing.T) {
	var out bytes.Buffer
	ssh := joinSSH()
	o := joinBase(ssh, &out)
	o.DryRun = true
	if err := JoinWorker(context.Background(), o); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if ssh.putCalls != 0 || ssh.ranAny("bash "+remoteAgentScriptPath) {
		t.Error("dry-run must not push/run the agent installer")
	}
	// The token must NEVER appear in operator-facing output.
	if strings.Contains(out.String(), testToken) {
		t.Errorf("token leaked into output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "redacted") {
		t.Error("plan should say the token is redacted")
	}
}

func TestJoinWorker_HappyPath(t *testing.T) {
	var out bytes.Buffer
	ssh := joinSSH()
	if err := JoinWorker(context.Background(), joinBase(ssh, &out)); err != nil {
		t.Fatalf("join: %v", err)
	}
	if ssh.putCalls != 1 || ssh.putPath != remoteAgentScriptPath || ssh.putMode != 0o755 {
		t.Errorf("agent installer not pushed: calls=%d path=%s mode=%o", ssh.putCalls, ssh.putPath, ssh.putMode)
	}
	if !strings.Contains(string(ssh.putBody), "rke2-agent") {
		t.Error("pushed body should be the agent installer")
	}
	// The agent command must carry the token as a positional arg...
	if !ssh.ranAny("bash " + remoteAgentScriptPath + " '" + testToken + "'") {
		t.Errorf("agent cmd missing token positional: %v", ssh.ranCmds)
	}
	// ...but the token must not be in operator output.
	if strings.Contains(out.String(), testToken) {
		t.Errorf("token leaked into output:\n%s", out.String())
	}
}

// The real SSH adapter embeds the full command — token included — in the
// error it returns on a failed Run (see adapters/ssh/client.go). A join
// failure must therefore NEVER surface the token in err.Error().
func TestJoinWorker_FailurePathRedactsToken(t *testing.T) {
	var out bytes.Buffer
	ssh := joinSSH()
	// Simulate the adapter leaking the whole command into the error.
	leakedCmd := "sudo -n env NODE_NAME='worker-1' bash " + remoteAgentScriptPath +
		" '" + testToken + "' '198.51.100.5' '198.51.100.5'"
	ssh.runErr = map[string]error{
		"bash " + remoteAgentScriptPath: fmt.Errorf("ssh: run %q on host: exit status 1", leakedCmd),
	}
	err := JoinWorker(context.Background(), joinBase(ssh, &out))
	if err == nil {
		t.Fatal("expected install failure")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("error should show the <redacted> placeholder: %v", err)
	}
	if strings.Contains(out.String(), testToken) {
		t.Errorf("token leaked into output:\n%s", out.String())
	}
}

// A non-default --cp-port must flow through env to the agent script
// (which reads CP_PORT); the default port must NOT be injected (the
// script defaults it), and an out-of-range port must be rejected.
func TestJoinWorker_CPPort(t *testing.T) {
	// Non-default → CP_PORT in the ran command.
	var out bytes.Buffer
	ssh := joinSSH()
	o := joinBase(ssh, &out)
	o.CPPort = 9999
	if err := JoinWorker(context.Background(), o); err != nil {
		t.Fatalf("join: %v", err)
	}
	if !ssh.ranAny("CP_PORT='9999'") {
		t.Errorf("non-default --cp-port must flow to env: %v", ssh.ranCmds)
	}

	// Default (0 → 9345) → no CP_PORT injected.
	var out2 bytes.Buffer
	ssh2 := joinSSH()
	if err := JoinWorker(context.Background(), joinBase(ssh2, &out2)); err != nil {
		t.Fatalf("join default: %v", err)
	}
	if ssh2.ranAny("CP_PORT=") {
		t.Error("default cp-port must not be injected (script defaults it)")
	}

	// Out of range → rejected before any mutation.
	var out3 bytes.Buffer
	ssh3 := joinSSH()
	bad := joinBase(ssh3, &out3)
	bad.CPPort = 70000
	if err := JoinWorker(context.Background(), bad); err == nil {
		t.Error("out-of-range --cp-port must be rejected")
	} else if ssh3.putCalls != 0 {
		t.Error("must not push the installer when --cp-port is invalid")
	}
}

func TestJoinWorker_IdempotentSkipWhenActive(t *testing.T) {
	var out bytes.Buffer
	ssh := joinSSH()
	ssh.runs["systemctl is-active rke2-agent"] = "active"
	if err := JoinWorker(context.Background(), joinBase(ssh, &out)); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if ssh.putCalls != 0 {
		t.Error("must not re-join when rke2-agent already active")
	}
}

func TestJoinWorker_ExplicitTokenSkipsFetch(t *testing.T) {
	var out bytes.Buffer
	ssh := joinSSH()
	delete(ssh.fetch, nodeTokenPath) // no token on the CP
	o := joinBase(ssh, &out)
	o.JoinToken = testToken
	o.CPHost = "10.0.0.3" // also skip CP IP detection
	if err := JoinWorker(context.Background(), o); err != nil {
		t.Fatalf("explicit token: %v", err)
	}
	if ssh.putCalls != 1 {
		t.Error("should install with an explicitly-supplied token")
	}
}

func TestJoinWorker_Validation(t *testing.T) {
	var out bytes.Buffer
	// No control-plane and no token/cp-host → error.
	o := JoinWorkerOptions{SSH: joinSSH(), Worker: ports.SSHHost{Hostname: "192.0.2.20"}, WorkerName: "w1", Out: &out}
	if err := JoinWorker(context.Background(), o); err == nil {
		t.Error("missing control-plane + token/cp-host must error")
	}
	// Bad worker name.
	o2 := joinBase(joinSSH(), &out)
	o2.WorkerName = "bad name"
	if err := JoinWorker(context.Background(), o2); err == nil {
		t.Error("whitespace node name must be rejected")
	}
}

func TestRedactToken(t *testing.T) {
	if got := redactToken("cmd carrying "+testToken+" as an arg", testToken); strings.Contains(got, testToken) {
		t.Errorf("token not redacted: %q", got)
	}
	if got := redactToken("nothing to redact", ""); got != "nothing to redact" {
		t.Errorf("empty token must be a no-op: %q", got)
	}
}

func TestServiceActive(t *testing.T) {
	ssh := &fakeSSH{runs: map[string]string{"is-active rke2-agent": "active\n"}}
	if !serviceActive(context.Background(), ssh, ports.SSHHost{}, "rke2-agent") {
		t.Error("should report active")
	}
	ssh2 := &fakeSSH{runs: map[string]string{"is-active rke2-agent": "inactive"}}
	if serviceActive(context.Background(), ssh2, ports.SSHHost{}, "rke2-agent") {
		t.Error("should report inactive")
	}
}

func TestEmbeddedAgent_EnvContractAndInvariants(t *testing.T) {
	s := string(installAgentScript)
	if len(s) == 0 {
		t.Fatal("install-agent.sh did not embed")
	}
	for _, k := range []string{"SERVER_TOKEN", "NODE_NAME", "NODE_IP", "rke2-agent", "INSTALL_RKE2_TYPE"} {
		if !strings.Contains(s, k) {
			t.Errorf("agent installer missing %q", k)
		}
	}
}
