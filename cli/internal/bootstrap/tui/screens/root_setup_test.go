package screens

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
)

func TestConfiguredRootRetainsSiteInputsAcrossTabsAndDiscard(t *testing.T) {
	o := &clusterinit.InitOptions{Name: "site-ten", Repo: t.TempDir(), SiteComponents: "platform=components/endpoints", PAYG: true, CephNodes: map[string]string{"one": "disk/by-id/example"}, Sets: map[string]string{"EXT_NET_NODE_EGRESS_ENABLED": "false", "CEPH_MON_NODES": "one"}}
	r := NewConfiguredRootModel(o.Repo, o)
	defer r.Close()
	r.ConfigureInitServices(initform.PanelServices{})
	r.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if r.active != int(RootTabInit) || len(r.tabs) != 3 {
		t.Fatal("config did not open in existing root navigation")
	}
	// Mutating the caller's map cannot change the reset snapshot.
	o.CephNodes["one"] = "sdc"
	r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if r.initOpts.CephNodes["one"] != "disk/by-id/example" || r.initOpts.SiteComponents == "" || !r.initOpts.PAYG || r.initOpts.Sets["EXT_NET_NODE_EGRESS_ENABLED"] != "false" {
		t.Fatal("discard reset lost the imported inputs")
	}
	if _, accepted := r.AcceptedInit(); accepted {
		t.Fatal("opening or resetting a config approved installation")
	}
}

func TestIntegratedRootOwnsDiscoveryAcrossTabsAndDiscard(t *testing.T) {
	d, err := setupdemo.NewCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r, err := NewDemoRootModel(context.Background(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, index := range []int{0, 1} {
		if _, ok := r.tabs[index].model.(fixtureTab); !ok {
			t.Fatal("demo constructed live tab")
		}
	}
	r.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	r.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Basics -> Hosts
	_, cmd := r.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd == nil {
		t.Fatal("no discovery command")
	}
	r.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	event := cmd()
	r.Update(event)
	r.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if !strings.Contains(ansi.Strip(r.View().Content), "Host observations updated") {
		t.Fatal("inactive-tab result was lost")
	}
	_, cmd = r.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd == nil {
		t.Fatal("no refresh")
	}
	r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	fresh := r.initPanel
	r.Update(cmd())
	if r.initPanel != fresh || r.active != int(RootTabFleet) {
		t.Fatal("stale result changed discarded form")
	}
	if _, ok := r.AcceptedInit(); ok {
		t.Fatal("demo allowed production handoff")
	}
}
