package screens

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// openbao.go is the T8 "OpenBao" tab: a thin live shell over the M5
// openbao.Status engine (the same one `bootstrap openbao status` uses).
// Like the Reconcile tab it reflects the CURRENT kubeconfig context and
// fetches on open + on 'r' (no auto-tick). The render is a pure function
// (RenderOpenBaoStatus) so it's unit-tested; the fetch is the live shell.

// openbaoLoadedMsg carries one status fetch.
type openbaoLoadedMsg struct {
	res openbao.StatusResult
	err error
	at  time.Time
}

// OpenBaoModel is the T8 tab model.
type OpenBaoModel struct {
	width, height int
	res           openbao.StatusResult
	loaded        bool
	err           error
	lastAt        time.Time
	vp            viewport.Model
	keys          bttui.KeyMap
}

func NewOpenBaoModel() *OpenBaoModel {
	return &OpenBaoModel{vp: viewport.New(0, 0), keys: bttui.DefaultKeyMap()}
}

func (m *OpenBaoModel) Init() tea.Cmd { return fetchOpenBaoStatus() }

func fetchOpenBaoStatus() tea.Cmd {
	return func() tea.Msg {
		session, err := bootstrap.NewSession(bootstrap.Options{})
		if session != nil {
			defer session.Close()
		}
		if err != nil || session.OpenBao == nil {
			return openbaoLoadedMsg{err: fmt.Errorf("no cluster client — set KUBECONFIG to the target context (%v)", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		res, serr := openbao.Status(ctx, openbao.StatusOptions{OpenBao: session.OpenBao, Out: io.Discard})
		return openbaoLoadedMsg{res: res, err: serr, at: time.Now()}
	}
}

func (m *OpenBaoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width = maxInt(msg.Width-4, 1)
		m.vp.Height = maxInt(msg.Height-4, 1)
		m.refresh()
	case openbaoLoadedMsg:
		m.loaded = true
		m.res = msg.res
		m.err = msg.err
		m.lastAt = msg.at
		m.refresh()
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.Refresh) {
			m.loaded = false
			m.refresh()
			return m, fetchOpenBaoStatus()
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *OpenBaoModel) refresh() { m.vp.SetContent(m.body()) }

func (m *OpenBaoModel) body() string {
	switch {
	case !m.loaded:
		return bttui.Muted.Render("loading OpenBao status from the current kubeconfig context…")
	case m.err != nil:
		return bttui.WarnBox.Render("could not read OpenBao status:\n" + m.err.Error())
	default:
		return RenderOpenBaoStatus(m.res)
	}
}

func (m *OpenBaoModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	w := m.width - 2
	right := bttui.Muted.Render("press r to load")
	if !m.lastAt.IsZero() {
		right = bttui.Muted.Render(fmt.Sprintf("updated %ds ago", int(time.Since(m.lastAt).Seconds())))
	}
	title := joinSpaced(w, bttui.Title.Render(" OpenBao ")+"  "+
		bttui.Muted.Render("current kubeconfig context"), right)
	help := bttui.HelpBar.Render(m.keys.Refresh.Help().Key + " refresh · ] / [ tabs · q quit")
	body := lipgloss.JoinVertical(lipgloss.Left, title, m.vp.View(), help)
	return bttui.AppStyle.Render(body)
}

// RenderOpenBaoStatus is the pure view over an openbao.StatusResult —
// per-pod seal/init/HA line + the bootstrap markers + policy-generation
// drift. Kept pure so it's unit-tested independently of a live cluster.
func RenderOpenBaoStatus(res openbao.StatusResult) string {
	var b strings.Builder
	if len(res.Pods) == 0 {
		b.WriteString(bttui.Muted.Render("no OpenBao pods found (namespace absent, or not yet deployed)"))
		return b.String()
	}

	sealed, uninit := 0, 0
	for _, p := range res.Pods {
		seal := "unsealed"
		if p.Sealed {
			seal = "SEALED"
			sealed++
		}
		initd := "initialized"
		if !p.Initialized {
			initd = "UNINITIALIZED"
			uninit++
		}
		ha := p.HAMode
		if ha == "" {
			ha = "-"
		}
		fmt.Fprintf(&b, "  %-12s %-13s %-8s %-8s %s\n", p.Pod, initd, seal, ha, p.Version)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  seal:              %d/%d unsealed\n", len(res.Pods)-sealed, len(res.Pods))
	fmt.Fprintf(&b, "  bootstrap-finalized: %s\n", yesNo(res.BootstrapFinalized != ""))
	fmt.Fprintf(&b, "  controller-auth:     %s\n", yesNo(res.ControllerAuthInstalled != ""))
	fmt.Fprintf(&b, "  policy-generation:   %d (expected %d)", res.PolicyGenerationInstalled, res.PolicyGenerationExpected)
	if res.HasPolicyGenerationDrift() {
		b.WriteString(bttui.Muted.Render("  ⚠ drift — re-run setup-controller-auth"))
	}
	if sealed > 0 {
		fmt.Fprintf(&b, "\n  ⚠ %d pod(s) SEALED — run `kube-dc bootstrap openbao unseal`", sealed)
	}
	if uninit > 0 {
		fmt.Fprintf(&b, "\n  ⚠ %d pod(s) UNINITIALIZED — run `kube-dc bootstrap openbao init` (or `unseal` to raft-join a fresh follower)", uninit)
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
