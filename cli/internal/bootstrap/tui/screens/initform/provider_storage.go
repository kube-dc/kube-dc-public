package initform

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func (m *PanelModel) inspectProviderStorage() tea.Cmd {
	w := m.workflow
	if w == nil || w.busy || w.services.InspectProviderStorage == nil {
		m.notice = "StorageClass inspection is unavailable. Capacity and block support remain unverified."
		return nil
	}
	var options clusterinit.InitOptions
	if err := m.st.Apply(&options); err != nil || options.CephStorageClass == "" {
		m.notice = "Enter the provider StorageClass first."
		return nil
	}
	m.Close()
	w.busy = true
	ctx, cancel := context.WithTimeout(w.services.Context, 10*time.Second)
	w.cancel = cancel
	e := PanelEvent{Owner: m, generation: w.generation, kind: "provider-storage", hash: m.stateHash()}
	inspect := w.services.InspectProviderStorage
	return func() tea.Msg {
		e.providerStorage, e.err = inspect(ctx, options)
		return e
	}
}
