package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T06 cobra surface — argument + flag validation. End-to-end
// coverage of the SSH+merge path lives in the engine tests
// (ssh_kubeconfig_test.go); this file locks the cobra layer's
// required-arg + required-flag contract so a future refactor
// can't silently drop --ssh-host / --domain / <cluster>.

func runFetchKubeconfigCmd(t *testing.T, args []string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapFetchKubeconfigCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestBootstrapFetchKubeconfig_RequiresClusterName(t *testing.T) {
	_, err := runFetchKubeconfigCmd(t, []string{"--ssh-host=master", "--domain=example.com"})
	if err == nil {
		t.Fatal("expected error when cluster name omitted")
	}
}

func TestBootstrapFetchKubeconfig_RequiresSSHHost(t *testing.T) {
	_, err := runFetchKubeconfigCmd(t, []string{"acme", "--domain=example.com"})
	if err == nil {
		t.Fatal("expected error when --ssh-host omitted")
	}
	if !strings.Contains(err.Error(), "--ssh-host") {
		t.Errorf("error should mention --ssh-host: %v", err)
	}
}

func TestBootstrapFetchKubeconfig_RequiresDomain(t *testing.T) {
	_, err := runFetchKubeconfigCmd(t, []string{"acme", "--ssh-host=master"})
	if err == nil {
		t.Fatal("expected error when --domain omitted")
	}
	if !strings.Contains(err.Error(), "--domain") {
		t.Errorf("error should mention --domain: %v", err)
	}
}

// TestBootstrapFetchKubeconfig_NoKubeconfig_Boundary — reviewer
// P1 regression guard. The canonical use case is a laptop that
// has NO kubeconfig yet (the command's purpose is to CREATE one).
// Under the pre-P1-fix code this failed at `bootstrap.NewSession`
// via k8s.New(); with the fix (NewSSHOnly) it proceeds to SSH
// dial and fails there instead (no reachable host in a test env).
func TestBootstrapFetchKubeconfig_NoKubeconfig_Boundary(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-m4t06-p1")
	t.Setenv("HOME", t.TempDir())
	// Point at a fake alias so the SSH adapter tries to dial then
	// fails at the transport layer. The important assertion is
	// that we DON'T fail at session-build with a k8s / kubeconfig
	// error before reaching SSH — that was the P1 regression.
	body, err := runFetchKubeconfigCmd(t, []string{
		"acme",
		"--ssh-host=nonexistent-host-for-boundary-test",
		"--domain=acme.example.com",
	})
	if err == nil {
		t.Fatalf("expected SSH-dial failure, got nil; output:\n%s", body)
	}
	// The error MUST NOT be a session-build failure. Under the
	// pre-P1-fix code it said "build session: ... no kubeconfig".
	if strings.Contains(err.Error(), "build session") {
		t.Errorf("regression: fetch-kubeconfig hitting session build error instead of SSH path\n%v", err)
	}
	if strings.Contains(err.Error(), "cannot construct real session") {
		t.Errorf("regression: k8s adapter failing before SSH-only path reached\n%v", err)
	}
}

func TestBootstrapFetchKubeconfig_LongHelp_DocumentsShape(t *testing.T) {
	empty := ""
	cmd := bootstrapFetchKubeconfigCmd(&empty)
	for _, want := range []string{
		"rke2.yaml",
		"kube-api",
		"--ssh-host",
		"--domain",
		"--set-current",
		"~/.kube/config",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long missing %q\nFULL:\n%s", want, cmd.Long)
		}
	}
	for _, flag := range []string{"ssh-host", "domain", "remote-path", "kubeconfig", "set-current"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("--%s flag not registered", flag)
		}
	}
}

// TestParseSSHHostArg_TruthTable — a small helper but the split
// contract matters for downstream SSHClient behaviour (Alias vs
// explicit user@host).
func TestParseSSHHostArg_TruthTable(t *testing.T) {
	cases := []struct {
		in       string
		wantHost ports.SSHHost
	}{
		{"master", ports.SSHHost{Alias: "master"}},
		{"acme-master", ports.SSHHost{Alias: "acme-master"}},
		{"operator@master.acme.com", ports.SSHHost{User: "operator", Hostname: "master.acme.com"}},
		{"root@10.0.0.1", ports.SSHHost{User: "root", Hostname: "10.0.0.1"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := parseSSHHostArg(c.in)
			if got != c.wantHost {
				t.Errorf("parseSSHHostArg(%q) = %+v, want %+v", c.in, got, c.wantHost)
			}
		})
	}
}
