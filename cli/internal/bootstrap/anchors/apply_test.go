package anchors

import (
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/mock"
)

func TestApply_HappyPath(t *testing.T) {
	ssh := mock.NewSSHClient(nil)
	anchors := []Entry{
		{Host: "host5-a", CIDR: "100.64.0.11/16"},
		{Host: "host6-a", CIDR: "100.64.0.12/16"},
	}
	res, err := Apply(context.Background(), ssh, ApplyOptions{
		Anchors: anchors,
		Iface:   "br-ext-cloud",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Failed != 0 {
		t.Errorf("Failed=%d, want 0; nodes=%+v", res.Failed, res.Nodes)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("len(Nodes)=%d want 2", len(res.Nodes))
	}
	for _, n := range res.Nodes {
		if !n.Wrote {
			t.Errorf("node %s: Wrote=false, err=%v", n.Host, n.Err)
		}
	}

	// Each node should have received the bind script + a node-specific
	// unit. Assert the unit content carries that node's CIDR — guards
	// against a bug where every node writes srv5's IP.
	for _, e := range anchors {
		script := ssh.PutCapture(e.Host, RemoteScriptPath)
		if len(script) == 0 {
			t.Errorf("node %s: no bind script written", e.Host)
		}
		unit := ssh.PutCapture(e.Host, RemoteUnitPath)
		if len(unit) == 0 {
			t.Errorf("node %s: no unit written", e.Host)
		}
		if !strings.Contains(string(unit), e.CIDR) {
			t.Errorf("node %s: unit doesn't carry CIDR %s; got:\n%s", e.Host, e.CIDR, unit)
		}
	}

	// Verify the daemon-reload + enable --now Run calls landed on
	// every node.
	runs := ssh.RunCaptures()
	for _, e := range anchors {
		var sawEnable, sawVerify bool
		for _, r := range runs {
			if r.Host != e.Host {
				continue
			}
			if strings.Contains(r.Cmd, "daemon-reload") && strings.Contains(r.Cmd, "enable --now") {
				sawEnable = true
			}
			if strings.Contains(r.Cmd, "is-active") && strings.Contains(r.Cmd, e.CIDR) {
				sawVerify = true
			}
		}
		if !sawEnable {
			t.Errorf("node %s: never saw daemon-reload+enable Run", e.Host)
		}
		if !sawVerify {
			t.Errorf("node %s: never saw verify Run with CIDR %s", e.Host, e.CIDR)
		}
	}
}

func TestApply_DryRun_NoSideEffects(t *testing.T) {
	ssh := mock.NewSSHClient(nil)
	res, err := Apply(context.Background(), ssh, ApplyOptions{
		Anchors: []Entry{{Host: "host5-a", CIDR: "100.64.0.11/16"}},
		Iface:   "br-ext-cloud",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Failed != 0 {
		t.Errorf("dry-run shouldn't fail; res=%+v", res)
	}
	if got := ssh.PutCapture("host5-a", RemoteScriptPath); got != nil {
		t.Errorf("dry-run wrote script (len=%d)", len(got))
	}
	if got := ssh.RunCaptures(); len(got) != 0 {
		t.Errorf("dry-run issued Run calls: %+v", got)
	}
}

func TestApply_InputValidation(t *testing.T) {
	ssh := mock.NewSSHClient(nil)
	cases := []struct {
		name string
		opts ApplyOptions
		want string
	}{
		{"empty iface", ApplyOptions{Anchors: []Entry{{"srv5", "100.64.0.11/16"}}, Iface: ""}, "empty interface"},
		{"no anchors", ApplyOptions{Anchors: nil, Iface: "br-ext-cloud"}, "no anchor entries"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := Apply(context.Background(), ssh, tc.opts)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %q missing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestApply_NilSSH_Rejected(t *testing.T) {
	_, err := Apply(context.Background(), nil, ApplyOptions{
		Anchors: []Entry{{Host: "srv5", CIDR: "100.64.0.11/16"}},
		Iface:   "br-ext-cloud",
	})
	if err == nil || !strings.Contains(err.Error(), "nil SSH client") {
		t.Errorf("expected nil-ssh error, got %v", err)
	}
}
