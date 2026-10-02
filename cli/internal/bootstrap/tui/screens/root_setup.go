package screens

import (
	"context"
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
)

var ErrDemoStopped = errors.New("setup demo stopped")

func (m *RootModel) ConfigureInitServices(s initform.PanelServices) {
	m.panelServices = &s
	m.initPanel.ConfigureServices(s)
}
func (m *RootModel) Close() {
	if m.initPanel != nil {
		m.initPanel.Close()
	}
}
func (m *RootModel) Stopped() bool { return m.stopped }

// AcceptedInit returns the same typed options used by standalone init. No shell
// command parsing and no implicit --yes approval is involved in the handoff.
func (m *RootModel) AcceptedInit() (*clusterinit.InitOptions, bool) {
	if m.initPanel == nil || m.simulated || (m.panelServices != nil && m.panelServices.Session != nil) {
		return nil, false
	}
	_, err := m.initPanel.Result(m.initOpts)
	if err != nil {
		return nil, false
	}
	return m.initOpts, true
}

// NewDemoRootModel keeps the production root router and New Cluster panel, but
// injects fixture-only operations and tabs. It never constructs live Fleet or
// Context models (their constructors can read local cluster credentials).
func NewDemoRootModel(ctx context.Context, demo *setupdemo.Coordinator, draftPath string) (*RootModel, error) {
	makePanel := func() (*clusterinit.InitOptions, *initform.PanelModel) {
		o := &clusterinit.InitOptions{Repo: "demo-fleet", Name: "demo", Domain: "demo.example.test", NodeExternalIP: "192.0.2.10", Email: "ops@example.test", SSHHost: "admin@server-1", Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo, Provider: clusterinit.ProviderGitHub, GitHubOwner: "kube-dc", GitHubRepo: "demo-fleet", Preset: clusterinit.PresetInternalOnly, RookMode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{"server-1": "sdb"}, IngressAddressLayer: "none", Sets: map[string]string{"KUBE_OVN_MASTER_NODES": "192.0.2.10", "EXT_NET_INTERFACE": "eth0", "EXT_NET_VLAN_ID": "0"}}
		panel := initform.NewEmbeddedPanel(o, "", nil)
		panel.SetDemoHostDefaults()
		return o, panel
	}
	o, p := makePanel()
	m := &RootModel{keys: bttui.DefaultKeyMap(), active: int(RootTabInit), initOpts: o, initPanel: p, resetInit: makePanel, simulated: true,
		tabs: []tabSpec{{name: "Fleet", model: fixtureTab("Fleet")}, {name: "Contexts", model: fixtureTab("Contexts")}, {name: "New Cluster", model: p, modal: true}}}
	m.ConfigureInitServices(initform.PanelServices{Context: ctx, Discover: demo.Discover, Demo: demo, DraftPath: draftPath})
	if draftPath != "" {
		if err := p.LoadDemoDraft(draftPath); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return m, nil
}

type fixtureTab string

func (f fixtureTab) Init() tea.Cmd                       { return nil }
func (f fixtureTab) Update(tea.Msg) (tea.Model, tea.Cmd) { return f, nil }
func (f fixtureTab) View() tea.View {
	return tea.NewView("\n SIMULATED · " + string(f) + "\n\n No live data is loaded. Press 3 to return to New Cluster.")
}

// NewSessionRootModel opens protected continuation in the existing router and
// New Cluster form. Its Review action cannot escape to legacy init Apply.
func NewSessionRootModel(ctx context.Context, coordinator *setup.SessionCoordinator, directory string) *RootModel {
	makePanel := func() (*clusterinit.InitOptions, *initform.PanelModel) {
		o := coordinator.Inputs()
		return &o, initform.NewEmbeddedPanel(&o, "", nil)
	}
	o, p := makePanel()
	m := NewRootModel(o.Repo, RootTabInit)
	m.initPanel.Close()
	m.initOpts, m.initPanel, m.resetInit = o, p, makePanel
	m.tabs[int(RootTabInit)].model = p
	m.ConfigureInitServices(initform.PanelServices{Context: ctx, Session: coordinator, SessionDirectory: directory})
	p.OpenContinue()
	return m
}
