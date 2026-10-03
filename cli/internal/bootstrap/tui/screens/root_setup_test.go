package screens

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
)

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
