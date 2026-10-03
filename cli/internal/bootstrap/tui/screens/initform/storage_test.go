package initform

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
)

func threeStorageHosts(m *PanelModel) {
	m.st.NodeSSHHosts = "server-2=admin@two,server-3=admin@three"
	m.st.NodeSSHHostKeys = "server-2=" + setupdemo.HostKey + ",server-3=" + setupdemo.HostKey
	m.changed()
}

func selectInspectorWithKeys(t *testing.T, m *PanelModel, want string) {
	t.Helper()
	m.openSection("Storage")
	for i, f := range m.currentFields() {
		if f.Label == "Inspect server" {
			m.fieldCursor = i
			for n := 0; n < 4 && m.request().Host.ID != want; n++ {
				m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			}
			if m.request().Host.ID != want || !m.resourcesCurrent() {
				t.Fatalf("could not browse cached host %s", want)
			}
			return
		}
	}
	t.Fatal("missing host selector")
}

func TestStorageGuideUsesPerServerInventoryAndExportsAssignments(t *testing.T) {
	m := demoPanel(t)
	threeStorageHosts(m)
	complete(t, m, m.startHostDiscovery())
	m.openSection("Storage")
	body, _ := m.renderFieldsBody(94)
	for _, title := range []string{"Server disks (default)", "Evaluation file", "Provider volumes"} {
		if !strings.Contains(body, title) {
			t.Fatalf("missing visible choice %s", title)
		}
	}
	if m.st.OSMode != "rook-ceph-local" || m.st.OSDDevice != "loop0" {
		t.Fatal("discovery changed configuration")
	}
	selectInspectorWithKeys(t, m, "server-2")
	if m.workflow.resources.Host.ID != "server-2" {
		t.Fatal("host selector kept another host's disks")
	}
	m.selectStorageLayout("raw")
	if m.st.CephNode1 != "server-1=sdb" || m.st.CephNode2 != "server-2=sdb" || m.st.CephNode3 != "server-3=sdb" {
		t.Fatalf("wrong assignments %+v", m.st)
	}
	if m.discoveryBlocker() == "" {
		t.Fatal("new assignments reused readiness")
	}
	values, err := m.st.configMap()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"OBJECT_STORAGE_MODE": "rook-ceph-multi-node", "CEPH_NODE_1": "server-1", "CEPH_NODE_1_DEVICE": "sdb", "CEPH_NODE_2": "server-2", "CEPH_NODE_3": "server-3"} {
		if values[key] != want {
			t.Fatalf("%s: got %q, want %q", key, values[key], want)
		}
	}
	complete(t, m, m.startHostDiscovery())
	if blocker := m.discoveryBlocker(); blocker != "" {
		t.Fatal(blocker)
	}
	// A pin change invalidates the corresponding layout evidence immediately.
	m.st.NodeSSHHostKeys = ""
	m.changed()
	m.selectStorageLayout("raw")
	if len(m.st.storageDevices()) != 1 || m.st.storageDevices()["server-1"] != "sdb" {
		t.Fatal("changed host pins reused untrusted assignments")
	}
}

func TestStorageCheckDoesNotDependOnDisplayedHost(t *testing.T) {
	m := demoPanel(t)
	threeStorageHosts(m)
	complete(t, m, m.startHostDiscovery())
	m.selectStorageLayout("raw")
	discover := m.workflow.services.Discover
	m.workflow.services.Discover = func(ctx context.Context, req setup.ResourceRequest) (setup.HostResources, error) {
		r, err := discover(ctx, req)
		if req.Host.ID == "server-1" {
			r.Host.Checks = append(r.Host.Checks, setup.HostCheck{ID: "selected-disk", Status: "blocked", Detail: "occupied"})
		}
		return r, err
	}
	complete(t, m, m.startHostDiscovery())
	selectInspectorWithKeys(t, m, "server-3")
	if blocker := m.discoveryBlocker(); !strings.Contains(blocker, "server-1") || !strings.Contains(blocker, "occupied") {
		t.Fatalf("hidden host blocker: %q", blocker)
	}
}

func TestLocalDefaultDeviceStillRequiresSecondaryHostEvidence(t *testing.T) {
	m := demoPanel(t)
	complete(t, m, m.startDiscovery())
	m.st.OSDNode, m.st.OSDDevice = "server-2", ""
	m.changed()
	complete(t, m, m.startDiscovery())
	if req := m.requestForHost("server-2"); req.Host.Disk != "/dev/loop0" {
		t.Fatalf("default device not normalized: %+v", req)
	}
	if blocker := m.discoveryBlocker(); !strings.Contains(blocker, "server-2") {
		t.Fatalf("missing storage target allowed: %q", blocker)
	}
	m.st.NodeSSHHosts = "server-2=admin@two"
	m.st.NodeSSHHostKeys = "server-2=" + setupdemo.HostKey
	m.changed()
	if blocker := m.discoveryBlocker(); !strings.Contains(blocker, "server-2") {
		t.Fatalf("missing storage observation allowed: %q", blocker)
	}
	complete(t, m, m.startHostDiscovery())
	snapshot := m.workflow.hostSnapshots["server-2"]
	snapshot.Resources.Host.Checks = append(snapshot.Resources.Host.Checks, setup.HostCheck{ID: "sudo", Status: "blocked", Detail: "no sudo"})
	m.workflow.hostSnapshots["server-2"] = snapshot
	if blocker := m.discoveryBlocker(); !strings.Contains(blocker, "no sudo") {
		t.Fatalf("secondary storage blocker hidden: %q", blocker)
	}
}

func TestStorageDiscoveryConcurrencyAndCancellation(t *testing.T) {
	m := demoPanel(t)
	for i := 2; i <= 9; i++ {
		m.st.NodeSSHHosts += fmt.Sprintf("server-%d=host-%d,", i, i)
	}
	m.st.NodeSSHHosts = strings.TrimRight(m.st.NodeSSHHosts, ",")
	var active, maximum, calls atomic.Int32
	m.workflow.services.Discover = func(ctx context.Context, req setup.ResourceRequest) (setup.HostResources, error) {
		n := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		select {
		case <-ctx.Done():
			return setup.HostResources{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		return setup.HostResources{Host: setup.HostObservation{ID: req.Host.ID}}, nil
	}
	complete(t, m, m.startHostDiscovery())
	if calls.Load() != 9 || maximum.Load() > 4 || maximum.Load() < 2 {
		t.Fatalf("calls=%d, concurrent=%d", calls.Load(), maximum.Load())
	}
	cmd := m.startHostDiscovery()
	m.Close()
	m.Update(cmd())
	if len(m.workflow.hostSnapshots) != 0 || m.Busy() {
		t.Fatal("cancelled batch restored observations")
	}
}

func TestStorageProposalDisabledIsNotANormalChoice(t *testing.T) {
	m := demoPanel(t)
	for _, f := range m.fields {
		if f.Label == "Object storage" {
			for _, opt := range f.Options {
				if opt == string(clusterinit.RookDisabled) {
					t.Fatal("disabled in normal choices")
				}
			}
		}
	}
	complete(t, m, m.startHostDiscovery())
	m.selectStorageLayout("raw")
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.RookMode != clusterinit.RookCephMultiNode || o.CephNodes["server-1"] != "sdb" || o.RookOSDSizeGB != 0 || o.Sets["CEPH_REPLICATION_SIZE"] != "1" {
		t.Fatal("raw layout kept file-backed settings")
	}
}

func TestStorageRadiosAreExclusiveAndDetailsAreOptional(t *testing.T) {
	m := demoPanel(t)
	m.st.OSMode = string(clusterinit.RookCephMultiNode)
	m.st.CephNode1 = "server-1=sdb"
	m.changed()
	complete(t, m, m.startHostDiscovery())
	m.openSection("Storage")
	check := func(want string) {
		t.Helper()
		selected, total := 0, 0
		for _, f := range m.currentFields() {
			if f.Kind == panelRadio {
				total++
				if f.Get(m.st) == "yes" {
					selected++
					if f.Action != "storage-mode:"+want {
						t.Fatal("wrong selected radio")
					}
				}
			}
		}
		if selected != 1 || total != 3 {
			t.Fatalf("selected=%d total=%d", selected, total)
		}
	}
	check("raw")
	for i, f := range m.currentFields() {
		if f.Action == "storage-mode:file-one" {
			m.fieldCursor = i
			break
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	check("file-one")
	for _, f := range m.currentFields() {
		if strings.HasPrefix(f.Label, "Disk ·") || strings.HasPrefix(f.Action, "disk:") {
			t.Fatal("raw controls shown for evaluation layout")
		}
	}
	for i, f := range m.currentFields() {
		if f.Action == "storage-mode:raw" {
			m.fieldCursor = i
			break
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	check("raw")
	complete(t, m, m.startHostDiscovery())
	if text := strings.Join(m.workflowText(), "\n"); strings.Contains(text, "Machine:") || strings.Contains(text, "Checks") || len(text) > 150 {
		t.Fatalf("collapsed hint is overloaded: %s", text)
	}
	for _, f := range m.currentFields() {
		if strings.HasPrefix(f.Action, "disk:") {
			t.Fatal("individual disk choices shown before opening details")
		}
	}
	m.activateWorkflow("storage-details")
	if !strings.Contains(strings.Join(m.workflowText(), "\n"), "Disks on server-1") {
		t.Fatal("explicit details missing")
	}
	if !strings.Contains(m.View().Content, "Reserved /dev/sda") {
		t.Fatal("individual disk choices missing from details")
	}
	if strings.Count(m.View().Content, "(●)") != 1 {
		t.Fatal("radio marker not rendered exclusively")
	}
}

func TestDefaultRawStorageReplicationFollowsAssignments(t *testing.T) {
	st := initialState(&clusterinit.InitOptions{})
	if st.OSMode != string(clusterinit.RookCephMultiNode) || st.storageReplication() != "2" {
		t.Fatal("wrong new-cluster storage defaults")
	}
	m := demoPanel(t)
	m.st.OSMode = string(clusterinit.RookCephMultiNode)
	m.st.CephReplicationSize = ""
	for i, want := range []string{"1", "2", "2"} {
		if err := m.st.setStorageDevice(fmt.Sprintf("server-%d", i+1), "sdb"); err != nil {
			t.Fatal(err)
		}
		values, err := m.st.configMap()
		if err != nil {
			t.Fatal(err)
		}
		if m.st.storageReplication() != want || values["CEPH_REPLICATION_SIZE"] != want {
			t.Fatalf("%d servers: display=%s env=%s", i+1, m.st.storageReplication(), values["CEPH_REPLICATION_SIZE"])
		}
	}
	m.st.CephReplicationSize = "3"
	values, err := m.st.configMap()
	if err != nil || values["CEPH_REPLICATION_SIZE"] != "3" {
		t.Fatal("explicit three-copy choice lost")
	}
}

func TestSuggestedDiskMappingPreservesExplicitCopies(t *testing.T) {
	m := demoPanel(t)
	threeStorageHosts(m)
	m.st.OSMode = string(clusterinit.RookCephMultiNode)
	m.st.CephReplicationSize = "3"
	m.changed()
	complete(t, m, m.startHostDiscovery())
	m.selectStorageLayout("raw")
	values, err := m.st.configMap()
	if err != nil {
		t.Fatal(err)
	}
	if values["CEPH_REPLICATION_SIZE"] != "3" {
		t.Fatal("disk mapping reduced explicit replica count")
	}
	for _, layout := range m.storageLayouts() {
		if layout.ID == "raw" && layout.Replicas != 3 {
			t.Fatal("suggestion hides explicit replica count")
		}
	}
	if err := m.st.setStorageDevice("server-3", ""); err != nil {
		t.Fatal(err)
	}
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "replication") && !strings.Contains(err.Error(), "REPLICATION") {
		t.Fatalf("too few servers silently changed durability: %v", err)
	}
}
