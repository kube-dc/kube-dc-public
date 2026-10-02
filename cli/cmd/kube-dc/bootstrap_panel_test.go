package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
)

func TestAcceptedPanelOptionsContinueThroughInitWithoutRecollection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBE_DC_INIT_DOMAIN", "ignored.example.test")
	o := &clusterinit.InitOptions{Name: "reviewed", Domain: "reviewed.example.test", NodeExternalIP: "192.0.2.10", Email: "ops@example.test", Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo, Provider: clusterinit.ProviderGitHub, GitHubOwner: "kube-dc", GitHubRepo: "reviewed-fleet", Preset: clusterinit.PresetInternalOnly, RookMode: clusterinit.RookDisabled, InstallationKind: "kube-dc", IngressAddressLayer: "none", SSHHost: "admin@server-1", SSHHostKeySHA256: setupdemo.HostKey, NoTTY: true, DryRun: true, Sets: map[string]string{"KUBE_OVN_MASTER_NODES": "192.0.2.10", "EXT_NET_INTERFACE": "eth0", "EXT_NET_VLAN_ID": "0"}, NodeNICs: map[string]string{"server-1": "eth1"}}
	var out bytes.Buffer
	repo := ""
	cmd := bootstrapInitCmdWithOptions(&repo, o)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("handoff failed: %v\n%s", err, out.String())
	}
	if o.Yes || !strings.Contains(out.String(), "reviewed") || strings.Contains(out.String(), "ignored.example.test") {
		t.Fatalf("handoff changed reviewed values: %s", out.String())
	}
	if !strings.Contains(out.String(), filepath.Join(os.Getenv("HOME"), ".kube-dc", "fleet")) {
		t.Fatal("accepted blank repo lost default")
	}
	endpoint := initSSHHost(o)
	if endpoint.ExpectedHostKeySHA256 != setupdemo.HostKey {
		t.Fatal("install dropped reviewed pin")
	}
	// The same accepted form cannot enable unattended apply without consent.
	o.DryRun = false
	cmd = bootstrapInitCmdWithOptions(&repo, o)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("handoff implicitly authorized unattended apply")
	}
}
