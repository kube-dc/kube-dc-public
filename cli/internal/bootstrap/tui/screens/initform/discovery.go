package initform

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

type hostSnapshot struct {
	Request   setup.ResourceRequest
	Resources setup.HostResources
	Err       error
}

func (m *PanelModel) resourceRequests() ([]setup.ResourceRequest, error) {
	if m.st.HostID == "" || m.st.SSHHost == "" {
		return nil, fmt.Errorf("Enter the primary node name and SSH host first.")
	}
	targets, err := clusterinit.ParseSetPairs(splitComma(m.st.NodeSSHHosts))
	if err != nil {
		return nil, err
	}
	if _, err := clusterinit.ParseSetPairs(splitComma(m.st.NodeSSHHostKeys)); err != nil {
		return nil, err
	}
	if targets == nil {
		targets = map[string]string{}
	}
	names := []string{m.st.HostID}
	nics, err := clusterinit.ParseSetPairs(splitComma(m.st.NodeNICs))
	if err != nil {
		return nil, err
	}
	for node := range nics {
		if _, exists := targets[node]; !exists {
			targets[node] = ""
		}
	}
	// Include assigned nodes even when their SSH target is missing. Discovery
	// and the handoff gate must not silently omit a selected storage server.
	if m.st.OSMode == string(clusterinit.RookCephLocal) && m.st.OSDNode != "" {
		if _, ok := targets[m.st.OSDNode]; !ok {
			targets[m.st.OSDNode] = ""
		}
	}
	if m.st.OSMode == string(clusterinit.RookCephMultiNode) {
		for _, pair := range []string{m.st.CephNode1, m.st.CephNode2, m.st.CephNode3} {
			if node, _, ok := strings.Cut(pair, "="); ok {
				if _, exists := targets[node]; !exists {
					targets[node] = ""
				}
			}
		}
	}
	for node := range targets {
		if node != m.st.HostID {
			names = append(names, node)
		}
	}
	if len(names) > 32 {
		return nil, fmt.Errorf("Inspect at most 32 hosts in one discovery run.")
	}
	sort.Strings(names)
	var requests []setup.ResourceRequest
	for _, node := range names {
		if err := clusterinit.ValidateK8sNodeNameField(node); err != nil {
			return nil, err
		}
		requests = append(requests, m.requestForHost(node))
	}
	return requests, nil
}

func (m *PanelModel) restoreInspectedHost() {
	w := m.workflow
	if w == nil {
		return
	}
	req := m.request()
	if snapshot, ok := w.hostSnapshots[req.Host.ID]; ok && fingerprint(snapshot.Request) == fingerprint(req) {
		w.resources, w.resourceErr, w.resourceHash = snapshot.Resources, snapshot.Err, fingerprint(req)
	}
}

func (m *PanelModel) selectInspectedHost(node string) {
	m.Close()
	w := m.workflow
	w.inspectedHost = node
	w.resources, w.resourceHash, w.resourceErr = setup.HostResources{}, "", nil
	m.restoreInspectedHost()
	m.rebuildWorkflowFields()
}

// Hardware suggestions may reuse a fresh inventory after a disk or NIC
// choice changes. Readiness still requires the full, exact request to match.
func resourceIdentity(req setup.ResourceRequest) string {
	req.Host.Disk, req.Host.NIC = "", ""
	return fingerprint(req)
}

func (m *PanelModel) startHostDiscovery() tea.Cmd {
	w := m.workflow
	if w == nil || w.busy || w.services.Discover == nil {
		return nil
	}
	requests, err := m.resourceRequests()
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	m.Close()
	w.busy, w.attempted = true, true
	w.hostSnapshots = map[string]hostSnapshot{}
	w.resources, w.resourceHash, w.reviewHash, w.reviewedHash = setup.HostResources{}, "", "", ""
	ctx, cancel := context.WithTimeout(w.services.Context, 2*time.Minute)
	w.cancel = cancel
	e := PanelEvent{Owner: m, generation: w.generation, kind: "discover-hosts", hash: fingerprint(requests)}
	discover := w.services.Discover
	return func() tea.Msg {
		results := make(chan hostSnapshot, len(requests))
		limit := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, request := range requests {
			wg.Add(1)
			go func(req setup.ResourceRequest) {
				defer wg.Done()
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					results <- hostSnapshot{Request: req, Err: ctx.Err()}
					return
				}
				defer func() { <-limit }()
				resources, err := discover(ctx, req)
				results <- hostSnapshot{Request: req, Resources: resources, Err: err}
			}(request)
		}
		wg.Wait()
		close(results)
		e.hostSnapshots = map[string]hostSnapshot{}
		for result := range results {
			e.hostSnapshots[result.Request.Host.ID] = result
		}
		e.err = ctx.Err()
		return e
	}
}
