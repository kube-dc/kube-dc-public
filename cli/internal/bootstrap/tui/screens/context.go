package screens

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/config"
	"github.com/shalb/kube-dc/cli/internal/jwt"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/pkg/credential"
)

// Identity tags every kubeconfig context the model surfaces. The
// distinction is what makes this screen kube-dc-aware (vs kubectx,
// which treats every context as opaque).
type Identity string

const (
	IdentityAdmin      Identity = "ADMIN"       // kube-dc/<domain>/admin via master realm
	IdentityTenant     Identity = "TENANT"      // kube-dc/<domain>/<org>/<project>
	IdentityBreakGlass Identity = "BREAK-GLASS" // static token, kube-dc-* cluster, no exec plugin
	IdentityExternal   Identity = "EXTERNAL"    // anything else (kubectx, cloud-tunnel, default, …)
)

// ContextEntry is the model's view of one row in the kubeconfig context
// list. It's a flattened projection of kubeconfig.Config — everything
// the table + details pane need without re-walking the file each render.
type ContextEntry struct {
	Name      string
	Cluster   string
	User      string
	Server    string
	Namespace string
	Identity  Identity
	Realm     string // populated for OIDC contexts (ADMIN / TENANT)
	IsCurrent bool
	UsesExec  bool // true when the user entry carries an exec plugin
}

// ContextModel is the `kube-dc bootstrap context` screen — kubectx-like
// list/switch UX with kube-dc identity badges and JWT introspection
// for the selected row. See installer-prd §16.6.
//
// Layout shares the fleet view's vocabulary (§9.9.1): top list pane,
// bottom details pane, Tab cycles focus, arrows scope to the focused
// pane, focused pane border switches to the active style.
type ContextModel struct {
	mgr *kubeconfig.Manager

	width, height int
	entries       []ContextEntry
	selected      int
	err           error
	notice        string // transient banner — "switched to X", "deleted Y"

	// authTest is the latest /readyz probe result for the selected
	// row (cleared when selection moves). Populated by the `t` key.
	authTest *bttui.AuthTestDoneMsg

	// Pane focus — same enum as fleet.go (paneFocusList / paneFocusDetails).
	// The drill-down value is never used here.
	focus fleetPaneFocus

	details viewport.Model
	help    help.Model
	keys    bttui.KeyMap
}

// NewContextModel constructs the screen rooted at the operator's
// existing kubeconfig (whatever `kubeconfig.NewManager` resolves to —
// $KUBECONFIG > ~/.kube/config).
func NewContextModel() (*ContextModel, error) {
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		return nil, fmt.Errorf("init kubeconfig manager: %w", err)
	}

	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Bold(true)
	h.Styles.ShortDesc = bttui.Muted
	h.Styles.ShortSeparator = bttui.Muted
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc

	m := &ContextModel{
		mgr:     mgr,
		details: viewport.New(0, 0),
		help:    h,
		keys:    bttui.DefaultKeyMap(),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Init is a no-op: load() runs synchronously in NewContextModel because
// reading kubeconfig is local and cheap. No need to defer it through a
// tea.Cmd.
func (m *ContextModel) Init() tea.Cmd { return nil }

// load re-reads the kubeconfig file and rebuilds the entry list.
// Called on construction, on `r` refresh, and after every mutation
// (activate, delete) so the screen always reflects on-disk truth.
func (m *ContextModel) load() error {
	cfg, err := m.mgr.Load()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	// Index clusters and users by name so we can flatten one entry per
	// context without an N×M scan per row.
	clusters := make(map[string]kubeconfig.Cluster, len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		clusters[c.Name] = c.Cluster
	}
	users := make(map[string]kubeconfig.User, len(cfg.Users))
	for _, u := range cfg.Users {
		users[u.Name] = u.User
	}

	entries := make([]ContextEntry, 0, len(cfg.Contexts))
	for _, c := range cfg.Contexts {
		cl := clusters[c.Context.Cluster]
		us := users[c.Context.User]
		ident, realm := classifyContext(c.Name, cl, us)
		entries = append(entries, ContextEntry{
			Name:      c.Name,
			Cluster:   c.Context.Cluster,
			User:      c.Context.User,
			Server:    cl.Server,
			Namespace: c.Context.Namespace,
			Identity:  ident,
			Realm:     realm,
			IsCurrent: c.Name == cfg.CurrentContext,
			UsesExec:  us.Exec != nil,
		})
	}

	// Stable order: kube-dc contexts first (grouped by domain), then
	// everything else alphabetically. Mirrors what an operator running
	// `kx` then visually-grouping by hand would produce.
	sort.SliceStable(entries, func(i, j int) bool {
		ii := strings.HasPrefix(entries[i].Name, "kube-dc/")
		jj := strings.HasPrefix(entries[j].Name, "kube-dc/")
		if ii != jj {
			return ii
		}
		return entries[i].Name < entries[j].Name
	})

	m.entries = entries
	if m.selected >= len(entries) {
		m.selected = 0
	}
	m.refreshDetails()
	return nil
}

// classifyContext maps a (name, cluster, user) triple to an Identity.
// The classification is deliberately conservative: we only tag a row
// ADMIN or TENANT when the kube-dc-aware exec-plugin pattern is
// present, never on name alone.
func classifyContext(name string, cl kubeconfig.Cluster, us kubeconfig.User) (Identity, string) {
	hasKubeDCExec := us.Exec != nil &&
		us.Exec.Command == "kube-dc" &&
		len(us.Exec.Args) > 0 &&
		us.Exec.Args[0] == "credential"

	// Pull --realm out of the exec args if present (admin contexts
	// always set it; tenant contexts predating Slice 13 don't).
	var realm string
	if hasKubeDCExec {
		for i := 0; i+1 < len(us.Exec.Args); i++ {
			if us.Exec.Args[i] == "--realm" {
				realm = us.Exec.Args[i+1]
				break
			}
		}
	}

	// Canonical break-glass shape produced by `bootstrap break-glass adopt`:
	// context "break-glass/<cluster>", static token, no exec plugin.
	// We match on the context name prefix so the row is tagged correctly
	// regardless of apiserver URL convention (the adopted file's user
	// and cluster names follow the same "break-glass" naming, but the
	// classifier only has the context name + Cluster/User structs in
	// scope here).
	isBreakGlassShape := us.Exec == nil && strings.HasPrefix(name, "break-glass/")

	switch {
	case hasKubeDCExec && (realm == "master" || strings.HasSuffix(name, "/admin")):
		return IdentityAdmin, "master"
	case hasKubeDCExec && strings.HasPrefix(name, "kube-dc/"):
		// Tenant contexts emit args without --realm in legacy logins;
		// the realm is the org name embedded in the context name. We
		// pull it from the kubeconfig path so the right pane can
		// display it consistently.
		if realm == "" {
			realm = tenantRealmFromName(name)
		}
		return IdentityTenant, realm
	case isBreakGlassShape:
		return IdentityBreakGlass, ""
	case strings.HasPrefix(cl.Server, "https://kube-api.") && us.Exec == nil:
		// Static-token kubeconfig pointing at a kube-dc cluster but
		// with non-canonical names — likely a hand-crafted break-glass
		// or an old export. Flag visually as the legitimate exception.
		return IdentityBreakGlass, ""
	default:
		return IdentityExternal, ""
	}
}

// tenantRealmFromName extracts the org/realm from a tenant context
// like "kube-dc/<domain>/<org>/<project>". Returns "" when the shape
// doesn't match.
func tenantRealmFromName(name string) string {
	parts := strings.Split(name, "/")
	// kube-dc / <domain> / <org> / <project>
	if len(parts) >= 3 && parts[0] == "kube-dc" {
		return parts[2]
	}
	return ""
}

// Update routes messages and key events.
func (m *ContextModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.refreshDetails()
	case bttui.LoginDoneMsg:
		// Subprocess returned. Re-load kubeconfig (a successful login
		// added/refreshed entries) and surface the outcome.
		if msg.Err != nil {
			m.err = fmt.Errorf("login %s failed: %w", msg.Cluster, msg.Err)
		} else {
			m.err = nil
			m.notice = "logged in: " + msg.Cluster
		}
		_ = m.load()
		return m, nil
	case bttui.AuthTestDoneMsg:
		// Stash for the right pane to render. Cleared on next selection.
		copy := msg
		m.authTest = &copy
		m.refreshDetails()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	// Forward viewport messages (mouse wheel etc.) to the details
	// viewport when it has focus — matches FleetModel.
	if m.focus == paneFocusDetails {
		var cmd tea.Cmd
		m.details, cmd = m.details.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *ContextModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Tab):
		// Toggle focus list ↔ details — same primitive as fleet.go (§9.9.1).
		if m.focus == paneFocusList {
			m.focus = paneFocusDetails
		} else {
			m.focus = paneFocusList
		}
		m.refreshDetails()
		return m, nil
	case key.Matches(msg, m.keys.ShiftTab):
		// Two-pane screen: shift-tab is the same toggle.
		if m.focus == paneFocusList {
			m.focus = paneFocusDetails
		} else {
			m.focus = paneFocusList
		}
		m.refreshDetails()
		return m, nil
	case key.Matches(msg, m.keys.Esc):
		// Esc returns focus to the list (mirrors fleet.go).
		if m.focus != paneFocusList {
			m.focus = paneFocusList
			m.refreshDetails()
		}
		return m, nil
	case key.Matches(msg, m.keys.Up):
		return m.handleArrow(-1)
	case key.Matches(msg, m.keys.Down):
		return m.handleArrow(+1)
	case key.Matches(msg, m.keys.PageUp), key.Matches(msg, m.keys.PageDown),
		key.Matches(msg, m.keys.Home), key.Matches(msg, m.keys.End):
		// Page/Home/End forward to the details viewport when focused;
		// on the list pane they currently no-op (the list fits one page).
		if m.focus == paneFocusDetails {
			var cmd tea.Cmd
			m.details, cmd = m.details.Update(msg)
			return m, cmd
		}
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		if err := m.load(); err != nil {
			m.err = err
		} else {
			m.notice = "refreshed"
		}
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Enter):
		m.activate()
	case key.Matches(msg, m.keys.LoginAdmin):
		if cmd := m.execLoginCmd(true); cmd != nil {
			return m, cmd
		}
	case key.Matches(msg, m.keys.LoginOrg):
		if cmd := m.execLoginCmd(false); cmd != nil {
			return m, cmd
		}
	case key.Matches(msg, m.keys.TestAuth):
		if cmd := m.testAuthCmd(); cmd != nil {
			m.notice = "testing auth…"
			return m, cmd
		}
	case key.Matches(msg, m.keys.Delete):
		m.deleteSelected()
	}
	return m, nil
}

// handleArrow routes Up/Down to whichever pane has focus — same shape
// as FleetModel.handleArrow.
func (m *ContextModel) handleArrow(delta int) (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneFocusList:
		next := m.selected + delta
		if next >= 0 && next < len(m.entries) {
			m.selected = next
			m.authTest = nil // stale for the new selection
			m.refreshDetails()
		}
	case paneFocusDetails:
		// No selectable sub-rows in the details pane today (no drill-down
		// in the contexts screen), so arrows just scroll the viewport.
		if delta < 0 {
			m.details.LineUp(1)
		} else {
			m.details.LineDown(1)
		}
	}
	return m, nil
}

// execLoginCmd suspends the TUI and re-logs-in for the selected
// context. Admin contexts → `kube-dc login --admin`; tenant contexts →
// `kube-dc login --org <realm>`. EXTERNAL / BREAK-GLASS contexts
// surface a noticeable refusal — re-logging in there is meaningless
// (no kube-dc OIDC client involved).
func (m *ContextModel) execLoginCmd(admin bool) tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	e := m.entries[m.selected]
	domain := domainFromAPI(e.Server)
	if domain == "" {
		m.err = fmt.Errorf("can't extract domain from server %q", e.Server)
		return nil
	}
	var args []string
	switch {
	case admin:
		args = []string{"login", "--domain", domain, "--admin"}
	case e.Identity == IdentityTenant && e.Realm != "":
		args = []string{"login", "--domain", domain, "--org", e.Realm}
	default:
		m.err = fmt.Errorf("can't run tenant login for %q (Identity=%s, Realm=%q) — pick a TENANT row or use --admin", e.Name, e.Identity, e.Realm)
		return nil
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return bttui.LoginDoneMsg{Cluster: e.Name, Admin: admin, Err: err}
	})
}

// testAuthCmd issues a single GET /readyz against the selected
// context's apiserver using the operator's currently-cached creds.
// Reports inline in the right pane. Doesn't run kubectl — direct HTTP
// keeps the failure modes legible (no `unable to get nodes` wrappers).
func (m *ContextModel) testAuthCmd() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	e := m.entries[m.selected]
	if e.Identity == IdentityExternal {
		m.err = fmt.Errorf("auth test only meaningful for kube-dc / break-glass identities — this is EXTERNAL")
		return nil
	}
	server, ctxName := e.Server, e.Name
	realm := e.Realm
	usesExec := e.UsesExec
	return func() tea.Msg {
		ok, detail := runReadyzProbe(server, realm, usesExec)
		return bttui.AuthTestDoneMsg{Context: ctxName, OK: ok, Detail: detail}
	}
}

// runReadyzProbe makes a single GET /readyz call to server using the
// operator's cached OIDC token (when usesExec) or no token (BREAK-GLASS;
// kubectl would supply the static one but we don't have it here, so we
// rely on the apiserver returning 200 from /readyz — which is unauth'd
// for liveness anyway, so this is also a reachability check).
func runReadyzProbe(server, realm string, usesExec bool) (bool, string) {
	tlsCfg, err := readyzTLS(server)
	if err != nil {
		return false, "tls setup: " + err.Error()
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, server+"/readyz", nil)
	if err != nil {
		return false, err.Error()
	}
	if usesExec {
		// Mint a token via the same exec-plugin path kubectl would use.
		prov, perr := credential.NewProvider()
		if perr == nil {
			cred, gerr := prov.GetCredentialForRealm(server, realm)
			if gerr == nil && cred != nil {
				req.Header.Set("Authorization", "Bearer "+cred.Status.Token)
			} else if gerr != nil {
				return false, "no cached creds: " + gerr.Error()
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, "200 OK · readyz reports healthy"
	case http.StatusUnauthorized:
		return false, "401 Unauthorized · token expired or invalid"
	case http.StatusForbidden:
		return false, "403 Forbidden · authenticated but RBAC denied (rare for readyz)"
	default:
		return false, fmt.Sprintf("%d %s", resp.StatusCode, resp.Status)
	}
}

// readyzTLS builds the http.Client TLS config for the auth-test probe.
// Reuses discover.FetchCA so a self-signed cluster CA is pinned.
func readyzTLS(server string) (*tls.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	caPEM, err := discover.FetchCA(ctx, server, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if caPEM == "" {
		return &tls.Config{}, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("failed to add CA")
	}
	return &tls.Config{RootCAs: pool}, nil
}

// domainFromAPI strips "https://kube-api." and the trailing port off
// the apiserver URL to get the domain we pass to `kube-dc login`.
// Mirrors the helper in cmd/kube-dc/bootstrap_kubeconfig.go.
func domainFromAPI(api string) string {
	const prefix = "https://kube-api."
	s := api
	if strings.HasPrefix(s, prefix) {
		s = s[len(prefix):]
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '/' {
			return s[:i]
		}
	}
	return s
}

// activate sets current-context to the selected row.
func (m *ContextModel) activate() {
	if len(m.entries) == 0 {
		return
	}
	target := m.entries[m.selected]
	if target.IsCurrent {
		m.notice = "already current: " + target.Name
		return
	}
	if err := m.mgr.SetCurrentContext(target.Name); err != nil {
		m.err = err
		return
	}
	m.err = nil
	m.notice = "switched to " + target.Name
	_ = m.load() // re-read to pick up the new current marker
}

// deleteSelected removes exactly the selected context. Mirrors `kx -d`
// semantics: one row → one delete. Cluster and user entries the context
// pointed at are GC'd only when no surviving context still references
// them, so deleting `kube-dc/stage/admin` won't take down the operator's
// other tenant contexts (`kube-dc/stage.kube-dc.com/shalb/dev`, etc.)
// that share a cluster or user entry.
//
// No confirmation modal — `r` reloads from disk and undo is
// re-running the appropriate `kube-dc bootstrap kubeconfig` /
// `kube-dc login` command. The earlier "refuse to delete EXTERNAL"
// guard was a band-aid for the over-eager RemoveKubeDCContexts
// behaviour; with surgical delete the guard is no longer needed.
func (m *ContextModel) deleteSelected() {
	if len(m.entries) == 0 {
		return
	}
	target := m.entries[m.selected]
	if err := m.mgr.RemoveContext(target.Name); err != nil {
		m.err = err
		return
	}
	m.err = nil
	m.notice = "deleted " + target.Name
	// Keep the selection on a sensible row after the list shrinks.
	if m.selected > 0 {
		m.selected--
	}
	if err := m.load(); err != nil {
		m.err = err
	}
}

// View renders the screen — same chrome as FleetModel.View() so the
// two screens look like siblings (§9.9.1 + §9.9.2).
func (m *ContextModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing…"
	}
	// AppStyle padding is 0 vertical, 1 horizontal — full m.height is
	// ours for the body stack; subtracting from height was a leftover
	// that left dead rows below the help bar (matches fleet.go fix).
	w := m.width - 2
	h := m.height

	// Header row mirrors fleet.go: title pill + path on the left, the
	// transient notice on the right. Notice uses Muted so it doesn't
	// fight with badges — the eye snaps to changes in the list, not
	// to a wall of green.
	titleRow := joinSpaced(w,
		bttui.Title.Render(" Kube-DC Contexts ")+"  "+bttui.Muted.Render(m.mgr.Path()),
		bttui.Muted.Render(m.notice))

	// Reserve exactly: 1 title + 1 help bar + 1 error row when present.
	chrome := 2
	if m.err != nil {
		chrome++
	}
	bodyH := h - chrome
	if bodyH < 8 {
		bodyH = 8
	}
	listH := len(m.entries) + 2
	if listH < 5 {
		listH = 5
	}
	if listH > bodyH/2 {
		listH = bodyH / 2
	}
	detailsH := bodyH - listH

	// Focus-aware borders: focused pane uses the *Focused style; the
	// other pane drops back to its idle border so there's a single
	// visual cursor on screen at any time.
	topStyle := bttui.ListPaneFocused
	if m.focus != paneFocusList {
		topStyle = bttui.ListPane
	}
	top := topStyle.
		Width(w - 2).
		Height(listH - 2).
		Render(m.renderList(w - 6))

	// Viewport's renderable width is the pane's content area (outer
	// width - 2 border - 2 padding). Earlier code used w-4 here which
	// is wider than the pane's content area (w-6) and made lipgloss
	// expand the pane beyond Width(w-2), visually offsetting the bottom
	// pane from the top one.
	m.details.Width = w - 6
	m.details.Height = detailsH - 2
	detailsStyle := bttui.DetailsPane
	if m.focus == paneFocusDetails {
		detailsStyle = bttui.DetailsPaneFocused
	}
	bottom := detailsStyle.
		Width(w - 2).
		Height(detailsH - 2).
		Render(m.details.View())

	body := lipgloss.JoinVertical(lipgloss.Left, top, bottom)

	// Footer: error + active-only help. Mirrors fleet.go's pattern so
	// the help bar shrinks to keys the operator can actually press
	// right now (§9.9.2).
	var footer []string
	if m.err != nil {
		footer = append(footer, bttui.ErrorBox.Width(w).Render("error: "+m.err.Error()))
	}
	if m.help.ShowAll {
		footer = append(footer, bttui.HelpBar.Render(m.help.FullHelpView(m.activeFullHelp())))
	} else {
		footer = append(footer, bttui.HelpBar.Render(m.help.ShortHelpView(m.activeShortHelp())))
	}

	parts := append([]string{titleRow, body}, footer...)
	return bttui.AppStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// activeShortHelp / activeFullHelp implement the active-only help rule
// (§9.9.2). Tenant-login (`l`) and test-auth (`t`) only render when the
// selected row supports them; admin-login (`L`) only when the row's
// server gives us a domain to log in to.
func (m *ContextModel) activeShortHelp() []key.Binding {
	keys := []key.Binding{m.keys.Up, m.keys.Down, m.keys.Tab, m.keys.Enter}
	if m.canAdminLogin() {
		keys = append(keys, m.keys.LoginAdmin)
	}
	if m.canTenantLogin() {
		keys = append(keys, m.keys.LoginOrg)
	}
	if m.canTestAuth() {
		keys = append(keys, m.keys.TestAuth)
	}
	if len(m.entries) > 0 {
		keys = append(keys, m.keys.Delete)
	}
	keys = append(keys, m.keys.Refresh, m.keys.Help, m.keys.Quit)
	return keys
}

func (m *ContextModel) activeFullHelp() [][]key.Binding {
	rows := [][]key.Binding{
		{m.keys.Up, m.keys.Down, m.keys.PageUp, m.keys.PageDown, m.keys.Home, m.keys.End},
		{m.keys.Tab, m.keys.ShiftTab, m.keys.Enter, m.keys.Esc},
	}
	actions := []key.Binding{}
	if m.canAdminLogin() {
		actions = append(actions, m.keys.LoginAdmin)
	}
	if m.canTenantLogin() {
		actions = append(actions, m.keys.LoginOrg)
	}
	if m.canTestAuth() {
		actions = append(actions, m.keys.TestAuth)
	}
	if len(m.entries) > 0 {
		actions = append(actions, m.keys.Delete)
	}
	if len(actions) > 0 {
		rows = append(rows, actions)
	}
	rows = append(rows, []key.Binding{m.keys.Refresh, m.keys.Help, m.keys.Quit})
	return rows
}

func (m *ContextModel) canAdminLogin() bool {
	if len(m.entries) == 0 {
		return false
	}
	return domainFromAPI(m.entries[m.selected].Server) != ""
}

func (m *ContextModel) canTenantLogin() bool {
	if len(m.entries) == 0 {
		return false
	}
	e := m.entries[m.selected]
	return e.Identity == IdentityTenant && e.Realm != ""
}

func (m *ContextModel) canTestAuth() bool {
	if len(m.entries) == 0 {
		return false
	}
	e := m.entries[m.selected]
	// Auth-test only meaningful when there's an exec plugin or a static
	// token to actually test — EXTERNAL contexts opt out.
	return e.Identity != IdentityExternal
}

func (m *ContextModel) renderList(maxW int) string {
	if len(m.entries) == 0 {
		return bttui.Muted.Render("no contexts found in kubeconfig")
	}

	// Tight columns: marker + ★ + name + identity badge + namespace.
	// Name column auto-fits the widest entry, capped so kubectx-style
	// long names (kube-dc/kube-dc.cloud/shalb/jumbolot) don't push the
	// identity badge off-screen.
	nameW := 4
	for _, e := range m.entries {
		if n := lipgloss.Width(e.Name); n > nameW {
			nameW = n
		}
	}
	if nameW > 50 {
		nameW = 50
	}
	rowStyle := lipgloss.NewStyle().MaxWidth(maxW)

	var b strings.Builder
	for i, e := range m.entries {
		// Marker shows which row Enter/L/l/d would act on. Highlighted
		// only when the list pane has focus — same visual cue as fleet.go,
		// so the operator can tell at a glance which pane "owns" the
		// arrows right now (§9.9.1).
		marker := "  "
		if i == m.selected {
			if m.focus == paneFocusList {
				marker = bttui.KeyLabel.Render("▸ ")
			} else {
				marker = bttui.Muted.Render("▸ ")
			}
		}
		current := "  "
		if e.IsCurrent {
			current = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9830")).Bold(true).Render("★ ")
		}
		row := marker + current +
			bttui.Text.Render(padRight(e.Name, nameW)) + "  " +
			identityBadge(e.Identity) + "  " +
			bttui.Muted.Render(e.Namespace)
		b.WriteString(rowStyle.Render(row))
		b.WriteByte('\n')
	}
	return b.String()
}

// identityBadge wraps the identity label in a colored bg pill. Same
// visual vocabulary as the alert TUI's severity badges.
func identityBadge(id Identity) string {
	switch id {
	case IdentityAdmin:
		return bttui.Badge(lipgloss.Color("#7E5CAD"), string(id)) // purple
	case IdentityTenant:
		return bttui.Badge(lipgloss.Color("#5794F2"), string(id)) // blue
	case IdentityBreakGlass:
		return bttui.Badge(lipgloss.Color("#F2495C"), string(id)) // red
	default:
		return bttui.Muted.Render("EXTERNAL")
	}
}

// refreshDetails renders the right pane for the selected context. For
// OIDC contexts (ADMIN / TENANT) it parses the cached JWT so the
// operator sees who they actually are before they touch the cluster.
func (m *ContextModel) refreshDetails() {
	if len(m.entries) == 0 {
		m.details.SetContent(bttui.Muted.Render("No context selected."))
		return
	}
	e := m.entries[m.selected]
	var b strings.Builder

	b.WriteString(bttui.Title.Render(" "+e.Name+" "))
	b.WriteString("  ")
	b.WriteString(identityBadge(e.Identity))
	if e.IsCurrent {
		b.WriteString("  ")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9830")).Bold(true).Render("★ current"))
	}
	b.WriteString("\n\n")

	b.WriteString(bttui.Pill(lipgloss.Color("#5794F2"), "cluster", e.Cluster) + "\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#2F9E72"), "server ", e.Server) + "\n")
	b.WriteString(bttui.Pill(lipgloss.Color("#A98BD8"), "user   ", e.User) + "\n")
	if e.Namespace != "" {
		b.WriteString(bttui.Pill(lipgloss.Color("#FF9830"), "ns     ", e.Namespace) + "\n")
	}
	if e.Realm != "" {
		b.WriteString(bttui.Pill(lipgloss.Color("#7E5CAD"), "realm  ", e.Realm) + "\n")
	}
	b.WriteString("\n")

	if e.UsesExec {
		b.WriteString(bttui.Muted.Render("auth: kube-dc OIDC exec plugin (refresh-driven)") + "\n")
	} else {
		b.WriteString(bttui.Muted.Render("auth: static token / cert (kubeconfig)") + "\n")
	}

	// JWT introspection for OIDC identities. Surfaced inline so an
	// operator can answer "who am I on this cluster?" without leaving
	// the TUI to run kubectl + jq + cut.
	if e.Identity == IdentityAdmin || e.Identity == IdentityTenant {
		writeJWTBlock(&b, e)
	}

	if e.Identity == IdentityBreakGlass {
		b.WriteString("\n")
		b.WriteString(bttui.WarnBox.Render(
			"break-glass-style context (static token, no exec plugin)\n" +
				"prefer `kube-dc login --admin` for daily admin work; this should\n" +
				"only be the active context during cluster recovery"))
	}

	// Auth-test result for the selected row, if the operator pressed `t`.
	if m.authTest != nil && m.authTest.Context == e.Name {
		b.WriteString("\n")
		label := "Auth test"
		if m.authTest.OK {
			b.WriteString(bttui.Badge(lipgloss.Color("#2F9E72"), label+"  ✓"))
		} else {
			b.WriteString(bttui.Badge(lipgloss.Color("#F2495C"), label+"  ✗"))
		}
		b.WriteString("  ")
		b.WriteString(bttui.Text.Render(m.authTest.Detail))
		b.WriteString("\n")
	}

	m.details.SetContent(b.String())
	m.details.GotoTop()
}

// writeJWTBlock looks up the cached creds for (server, realm), parses
// the access token, and prints the audit identity + group claims. It's
// best-effort — a missing or expired creds file just renders a hint
// rather than failing the screen.
func writeJWTBlock(b *strings.Builder, e ContextEntry) {
	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		b.WriteString("\n" + bttui.Muted.Render("token cache: "+err.Error()) + "\n")
		return
	}
	creds, err := credMgr.LoadForRealm(e.Server, e.Realm)
	if err != nil {
		b.WriteString("\n" + bttui.Muted.Render("token cache: not logged in (run `kube-dc login` for this cluster)") + "\n")
		return
	}

	claims, err := jwt.ParseToken(creds.AccessToken)
	if err != nil {
		b.WriteString("\n" + bttui.Muted.Render("token parse: "+err.Error()) + "\n")
		return
	}

	b.WriteString("\n")
	b.WriteString(bttui.Muted.Render("Cached identity") + "\n")
	if claims.Email != "" {
		b.WriteString("  email   " + bttui.Text.Render(claims.Email) + "\n")
	}
	if len(claims.Groups) > 0 {
		b.WriteString("  groups  " + bttui.Text.Render(strings.Join(claims.Groups, ", ")) + "\n")
	}
	if len(claims.Namespaces) > 0 {
		b.WriteString("  ns      " + bttui.Text.Render(strings.Join(claims.Namespaces, ", ")) + "\n")
	}
	b.WriteString("  expires " + tokenExpiryLine(creds) + "\n")
}

func tokenExpiryLine(c *config.Credentials) string {
	access := time.Until(c.AccessTokenExpiry).Round(time.Second)
	refresh := time.Until(c.RefreshTokenExpiry).Round(time.Second)
	switch {
	case access > 0:
		return fmt.Sprintf("access in %s · refresh in %s", access, refresh)
	case refresh > 0:
		return fmt.Sprintf("access EXPIRED · refresh in %s (will auto-renew on next kubectl)", refresh)
	default:
		return "EXPIRED — run `kube-dc login` again"
	}
}
