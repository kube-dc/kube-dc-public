package anchors

import (
	"strings"
	"testing"
)

func TestRenderUnit_Substitution(t *testing.T) {
	out, err := RenderUnit("br-ext-cloud", "100.64.0.11/16")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Both placeholders must be gone — a stray {{IFACE}} would mean
	// the unit fails to parse as systemd writes it to disk.
	if strings.Contains(out, "{{IFACE}}") {
		t.Errorf("{{IFACE}} not substituted: %s", out)
	}
	if strings.Contains(out, "{{CIDR}}") {
		t.Errorf("{{CIDR}} not substituted: %s", out)
	}
	// And the substituted values must appear in the ExecStart line.
	wantExec := "ExecStart=/usr/local/sbin/kube-dc-anchor-bind br-ext-cloud 100.64.0.11/16"
	if !strings.Contains(out, wantExec) {
		t.Errorf("missing %q in rendered unit:\n%s", wantExec, out)
	}
}

func TestRenderUnit_RejectsEmptyInputs(t *testing.T) {
	cases := []struct {
		name, iface, cidr, wantSub string
	}{
		{"empty iface", "", "100.64.0.11/16", "empty interface"},
		{"empty cidr", "br-ext-cloud", "", "empty CIDR"},
		{"both empty", "", "", "empty interface"},
		{"whitespace iface", "  ", "100.64.0.11/16", "empty interface"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderUnit(tc.iface, tc.cidr)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestRenderUnit_ContainsExpectedDirectives(t *testing.T) {
	out, _ := RenderUnit("br-ext-cloud", "100.64.0.11/16")
	// Each directive matters — losing one means failure mode
	// captured in the unit comment regresses.
	wants := []string{
		"After=systemd-networkd.service rke2-server.service openvswitch-switch.service",
		"Type=oneshot",
		"RemainAfterExit=yes",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered unit missing directive %q\nfull:\n%s", w, out)
		}
	}
}

func TestRenderBindScript_PollsForBridge(t *testing.T) {
	s := RenderBindScript()
	// The bridge-existence polling loop is the load-bearing piece —
	// without it, systemd starts the unit before kube-ovn-cni
	// finishes creating br-ext-cloud and `ip addr replace` fails on
	// a missing interface.
	wants := []string{
		"until ip link show",
		"ip addr replace",
		"timeout waiting for",
	}
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("bind script missing %q\nfull:\n%s", w, s)
		}
	}
}

func TestRemotePathConstants(t *testing.T) {
	// The unit ExecStart hardcodes the script path. If RemoteScriptPath
	// is changed without updating UnitTemplate, the unit silently
	// invokes a missing file. This guard catches the drift.
	if !strings.Contains(UnitTemplate, RemoteScriptPath) {
		t.Errorf("UnitTemplate doesn't reference RemoteScriptPath %q", RemoteScriptPath)
	}
	if !strings.HasPrefix(RemoteScriptPath, "/") {
		t.Errorf("RemoteScriptPath %q not absolute", RemoteScriptPath)
	}
	if !strings.HasPrefix(RemoteUnitPath, "/") {
		t.Errorf("RemoteUnitPath %q not absolute", RemoteUnitPath)
	}
}
