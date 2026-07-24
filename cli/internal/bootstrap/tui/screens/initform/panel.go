package initform

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
)

// panelKeyMap drives the help bar (bubbles/help). The keys mirror what
// updateNav/updateEditing actually handle; this exists so the footer's
// short help + the '?' full-help overlay stay in sync with the bindings.
type panelKeyMap struct {
	Nav   key.Binding
	Pane  key.Binding
	Edit  key.Binding
	Cycle key.Binding
	Save  key.Binding
	Help  key.Binding
	Back  key.Binding
	Quit  key.Binding
}

func (k panelKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Nav, k.Pane, k.Edit, k.Save, k.Help, k.Quit}
}

func (k panelKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Nav, k.Pane},
		{k.Edit, k.Cycle, k.Save},
		{k.Back, k.Help, k.Quit},
	}
}

func defaultPanelKeys() panelKeyMap {
	return panelKeyMap{
		Nav:   key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "move")),
		Pane:  key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch pane")),
		Edit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "edit/apply")),
		Cycle: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←→", "cycle option")),
		Save:  key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "save draft")),
		Help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// --- small layout helpers (local to initform; the screens package has
// its own copies, but that's a different package) ---

func joinSpaced(width int, left, right string) string {
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func colorWarnFG() color.Color { return lipgloss.Color("#FF9830") }

// panel.go is the Proxmox/ESXi-style install panel — a two-pane
// (sections | settings) random-access settings screen over the SAME
// initform.State + Apply + validators the huh wizard used, styled to
// match the Fleet dashboard (bttui). It replaces the one-question-at-a-
// time huh form: you jump to any section, edit any field in place, and
// Apply when the required fields validate. Thin-generator contract holds
// — the output is the same InitOptions, still expressible as flags.
//
// Data-driven: the whole form is a []panelField list (label + kind +
// get/set/validate/visible closures over State). View/Update are generic
// over that list, so the field set + the pure logic (visibility,
// validation, apply→InitOptions) are unit-tested independently of the
// Bubble Tea plumbing (which needs a TTY).

// defaultDraftPath is where the 'S' save-draft writes when the panel
// wasn't told otherwise (CWD-relative). "decide later" → reload with
// `kube-dc bootstrap init --config kube-dc-init.draft.env`.
const defaultDraftPath = "kube-dc-init.draft.env"

type panelKind int

const (
	panelText   panelKind = iota // free-text input
	panelSelect                  // one of Options (enter cycles)
	panelToggle                  // bool (enter flips)
	panelAction                  // e.g. Apply (enter triggers)
)

// panelField is one editable setting (or an action row).
type panelField struct {
	Section  string
	Label    string
	Desc     string
	Kind     panelKind
	Options  []string             // panelSelect: allowed values
	Required bool                 // panelText: must be non-empty
	Get      func(*State) string  // display/edit value
	Set      func(*State, string) // commit
	Validate func(string) error   // optional, panelText
	Visible  func(*State) bool    // nil = always visible
}

func (f panelField) visible(st *State) bool { return f.Visible == nil || f.Visible(st) }

// panelFields is the declarative form definition. Order defines section
// order + within-section field order.
func panelFields() []panelField {
	txt := func(sec, label, desc string, req bool, get func(*State) string, set func(*State, string), val func(string) error) panelField {
		return panelField{Section: sec, Label: label, Desc: desc, Kind: panelText, Required: req, Get: get, Set: set, Validate: val}
	}
	sel := func(sec, label, desc string, opts []string, get func(*State) string, set func(*State, string)) panelField {
		return panelField{Section: sec, Label: label, Desc: desc, Kind: panelSelect, Options: opts, Get: get, Set: set}
	}
	toggle := func(sec, label, desc string, get func(*State) bool, set func(*State, bool)) panelField {
		return panelField{Section: sec, Label: label, Desc: desc, Kind: panelToggle,
			Get: func(s *State) string { return boolStr(get(s)) },
			Set: func(s *State, v string) { set(s, v == "yes") }}
	}
	isPublic := func(s *State) bool { return s.Preset == string(clusterinit.PresetCloudPublicVLAN) }
	osIs := func(mode string) func(*State) bool {
		return func(s *State) bool { return s.OSMode == mode }
	}
	isNewRepo := func(s *State) bool { return s.FleetMode == string(clusterinit.FleetNewRepo) }
	isAdopt := func(s *State) bool { return s.Mode == string(clusterinit.ModeAdopt) }
	gpuConfigured := func(s *State) bool { return s.GPUPlatform != string(clusterinit.GPUPlatformDisabled) }
	gpuEnabled := func(s *State) bool { return s.GPUPlatform == string(clusterinit.GPUPlatformEnabled) }
	hamiSelected := func(s *State) bool { return gpuEnabled(s) && strings.Contains(s.GPUNodeModes, "=pod-hami") }
	vgpuSelected := func(s *State) bool { return gpuEnabled(s) && strings.Contains(s.GPUNodeModes, "=vm-vgpu") }

	fields := []panelField{
		// --- Basics ---
		txt("Basics", "Cluster name", "lowercase; nested allowed (eu/dc1)", true,
			func(s *State) string { return s.Name }, func(s *State, v string) { s.Name = v }, clusterinit.ValidateClusterNameField),
		txt("Basics", "Domain", "bare FQDN, e.g. kdc.example.com", true,
			func(s *State) string { return s.Domain }, func(s *State, v string) { s.Domain = v }, clusterinit.ValidateDomainField),
		txt("Basics", "Node external IP", "public IP the wildcard DNS points at", true,
			func(s *State) string { return s.NodeIP }, func(s *State, v string) { s.NodeIP = v }, clusterinit.ValidateNodeIPField),
		txt("Basics", "SSH host", "user@host — enables NAT detection (empty if the node has its public IP bound)", false,
			func(s *State) string { return s.SSHHost }, func(s *State, v string) { s.SSHHost = v }, nil),
		txt("Basics", "Operator email", "cert-manager / Let's Encrypt", true,
			func(s *State) string { return s.Email }, func(s *State, v string) { s.Email = v }, clusterinit.ValidateEmailField),
		sel("Basics", "Mode", "install (fresh) / adopt (existing overlay) / resume", []string{"install", "adopt", "resume"},
			func(s *State) string { return s.Mode }, func(s *State, v string) { s.Mode = v }),

		// --- Fleet ---
		sel("Fleet", "Fleet mode", "how the CLI relates to the fleet repo", []string{"existing-fleet", "new-repo", "existing-repo"},
			func(s *State) string { return s.FleetMode }, func(s *State, v string) { s.FleetMode = v }),
		txt("Fleet", "Fleet repo path", "local checkout (empty = $KUBE_DC_FLEET / ~/.kube-dc/fleet)", false,
			func(s *State) string { return s.Repo }, func(s *State, v string) { s.Repo = v }, nil),
		sel("Fleet", "Git provider", "for new-repo create + push", []string{"github", "gitlab"},
			func(s *State) string { return s.Provider }, func(s *State, v string) { s.Provider = v }).with(isNewRepo),
		txt("Fleet", "Owner / group", "token resolves via gh/glab auth", false,
			func(s *State) string { return s.Owner }, func(s *State, v string) { s.Owner = v }, nil).with(isNewRepo),
		txt("Fleet", "Repo name", "new fleet repo name", false,
			func(s *State) string { return s.RepoName }, func(s *State, v string) { s.RepoName = v }, nil).with(isNewRepo),

		// --- Network ---
		sel("Network", "Preset", "network topology", []string{"cloud-vlan", "cloud+public-vlan", "internal-only", "custom"},
			func(s *State) string { return s.Preset }, func(s *State, v string) { s.Preset = v }),
		txt("Network", "EXT_NET_VLAN_ID", "provider VLAN id (0 if none)", false,
			func(s *State) string { return s.NetVLANID }, func(s *State, v string) { s.NetVLANID = v }, nil),
		txt("Network", "EXT_NET_INTERFACE", "trunk NIC, e.g. bond0 / enp1s0", false,
			func(s *State) string { return s.NetInterface }, func(s *State, v string) { s.NetInterface = v }, nil),
		txt("Network", "KUBE_OVN_MASTER_NODES", "control-plane INTERNAL IP(s), comma-separated — required", true,
			func(s *State) string { return s.KubeOVNMasterNodes }, func(s *State, v string) { s.KubeOVNMasterNodes = v }, nil),
		txt("Network", "Gateway nodes", "KUBE_OVN_GW_NODES — OVN external-gateway node names, comma-separated (empty = default)", false,
			func(s *State) string { return s.GWNodes }, func(s *State, v string) { s.GWNodes = v }, nil),
		txt("Network", "Gateway type", "KUBE_OVN_GW_TYPE — centralized | distributed (empty = fleet default)", false,
			func(s *State) string { return s.GWType }, func(s *State, v string) { s.GWType = v }, nil),
		txt("Network", "EXT_PUBLIC_VLAN_ID", "public VLAN id", false,
			func(s *State) string { return s.PubVLANID }, func(s *State, v string) { s.PubVLANID = v }, nil).with(isPublic),
		txt("Network", "EXT_PUBLIC_CIDR", "e.g. 203.0.113.48/29", false,
			func(s *State) string { return s.PubCIDR }, func(s *State, v string) { s.PubCIDR = v }, nil).with(isPublic),
		txt("Network", "EXT_PUBLIC_GATEWAY", "public gateway IP", false,
			func(s *State) string { return s.PubGateway }, func(s *State, v string) { s.PubGateway = v }, nil).with(isPublic),

		// --- Object storage ---
		sel("Storage", "Object storage", "REQUIRED — Mimir/Loki/tenant buckets depend on it", []string{"rook-ceph-multi-node", "rook-ceph-local", "rook-ceph-pvc", "disabled"},
			func(s *State) string { return s.OSMode }, func(s *State, v string) { s.OSMode = v }),
		txt("Storage", "OSD node", "node hosting the OSD", false,
			func(s *State) string { return s.OSDNode }, func(s *State, v string) { s.OSDNode = v }, clusterinit.ValidateK8sNodeNameField).with(osIs("rook-ceph-local")),
		txt("Storage", "OSD size (GB)", "default 500", false,
			func(s *State) string { return s.OSDSizeGB }, func(s *State, v string) { s.OSDSizeGB = v }, validateOptionalInt).with(osIs("rook-ceph-local")),
		txt("Storage", "OSD device", "empty = loop0 (loop file)", false,
			func(s *State) string { return s.OSDDevice }, func(s *State, v string) { s.OSDDevice = v }, clusterinit.ValidateDeviceNameField).with(osIs("rook-ceph-local")),
		txt("Storage", "Ceph node 1", "NODE=DEVICE, e.g. host5-a=sdb", false,
			func(s *State) string { return s.CephNode1 }, func(s *State, v string) { s.CephNode1 = v }, clusterinit.ValidateNodeDevicePairField).with(osIs("rook-ceph-multi-node")),
		txt("Storage", "Ceph node 2", "NODE=DEVICE", false,
			func(s *State) string { return s.CephNode2 }, func(s *State, v string) { s.CephNode2 = v }, clusterinit.ValidateNodeDevicePairField).with(osIs("rook-ceph-multi-node")),
		txt("Storage", "Ceph node 3", "NODE=DEVICE (exactly 3 in v1)", false,
			func(s *State) string { return s.CephNode3 }, func(s *State, v string) { s.CephNode3 = v }, clusterinit.ValidateNodeDevicePairField).with(osIs("rook-ceph-multi-node")),
		txt("Storage", "StorageClass", "backing SC for OSD PVCs", false,
			func(s *State) string { return s.StorageClass }, func(s *State, v string) { s.StorageClass = v }, clusterinit.ValidateStorageClassField).with(osIs("rook-ceph-pvc")),
		txt("Storage", "OSD count", "number of PVC-backed OSDs (empty = fleet default)", false,
			func(s *State) string { return s.CephOSDCount }, func(s *State, v string) { s.CephOSDCount = v }, validateOptionalInt).with(osIs("rook-ceph-pvc")),
		txt("Storage", "OSD volume size (GB)", "size of each PVC-backed OSD (empty = fleet default)", false,
			func(s *State) string { return s.CephOSDVolumeSize }, func(s *State, v string) { s.CephOSDVolumeSize = v }, validateOptionalInt).with(osIs("rook-ceph-pvc")),
		txt("Storage", "Ceph replication size", "CEPH_REPLICATION_SIZE — OSD replica count (1 = dev/no-redundancy, 2-3 = HA; empty = default)", false,
			func(s *State) string { return s.CephReplicationSize }, func(s *State, v string) { s.CephReplicationSize = v }, validateOptionalInt).with(func(s *State) bool {
			return s.OSMode != "" && s.OSMode != string(clusterinit.RookDisabled)
		}),
		toggle("Storage", "Object storage DISABLED consent", "REQUIRED to proceed with disabled (no metrics/logs storage)",
			func(s *State) bool { return s.DisabledConsent }, func(s *State, v bool) { s.DisabledConsent = v }).with(osIs("disabled")),

		// VM root-disk storage — optional. Shown only when object storage is a
		// rook-ceph-* mode (shared-rbd needs the rbd-pool CephBlockPool those
		// modes ship); otherwise VMs use local-path (the default). Goldens are
		// CLI-only (--vm-golden). shared-rbd-live-migration is NOT offered in
		// the wizard — it fails closed at install time (runtime egress + Block
		// catalog); advanced operators use the --vm-storage-mode CLI flag.
		sel("Storage", "VM root disk", "optional — local (default) or shared Ceph RBD; goldens via --vm-golden",
			[]string{"local", "shared-rbd"},
			func(s *State) string { return s.VMStorageMode }, func(s *State, v string) { s.VMStorageMode = v }).with(func(s *State) bool {
			return s.OSMode == string(clusterinit.RookCephLocal) ||
				s.OSMode == string(clusterinit.RookCephMultiNode) ||
				s.OSMode == string(clusterinit.RookCephPVC)
		}),

		// --- Accelerators ---
		sel("Accelerators", "GPU platform", "disabled / inventory only / install products", []string{"disabled", "detect-only", "enabled"},
			func(s *State) string { return s.GPUPlatform }, func(s *State, v string) { s.GPUPlatform = v }),
		sel("Accelerators", "Driver source", "GPU Operator or an already managed host driver", []string{"gpu-operator", "preinstalled"},
			func(s *State) string { return s.GPUDriverSource }, func(s *State, v string) { s.GPUDriverSource = v }).with(gpuConfigured),
		txt("Accelerators", "GPU Operator version", "qualified chart pin", false,
			func(s *State) string { return s.GPUOperatorVersion }, func(s *State, v string) { s.GPUOperatorVersion = v }, nil).with(func(s *State) bool { return gpuConfigured(s) && s.GPUDriverSource == "gpu-operator" }),
		txt("Accelerators", "NVIDIA driver version", "qualified branch; upgrades require a canary", false,
			func(s *State) string { return s.NVIDIADriverVersion }, func(s *State, v string) { s.NVIDIADriverVersion = v }, nil).with(gpuConfigured),
		txt("Accelerators", "Toolkit version", "qualified container-toolkit pin", false,
			func(s *State) string { return s.NVIDIAToolkitVersion }, func(s *State, v string) { s.NVIDIAToolkitVersion = v }, nil).with(gpuConfigured),
		txt("Accelerators", "Node mode assignments", "NODE is also the default ~/.ssh/config alias; CLI --gpu-ssh-host-map overrides it", false,
			func(s *State) string { return s.GPUNodeModes }, func(s *State, v string) { s.GPUNodeModes = v }, validateGPUNodeModesField).with(gpuEnabled),
		txt("Accelerators", "Enabled profile IDs", "stable IDs, comma-separated; no PCI/resource names", false,
			func(s *State) string { return s.GPUProfiles }, func(s *State, v string) { s.GPUProfiles = v }, nil).with(gpuEnabled),
		toggle("Accelerators", "Shared GPU runtime", "required when any node uses pod-hami",
			func(s *State) bool { return s.HAMiEnabled }, func(s *State, v bool) { s.HAMiEnabled = v }).with(hamiSelected),
		txt("Accelerators", "Shared GPU version", "qualified runtime chart pin", false,
			func(s *State) string { return s.HAMiVersion }, func(s *State, v string) { s.HAMiVersion = v }, nil).with(hamiSelected),
		txt("Accelerators", "Scheduler version", "must match the cluster Kubernetes minor", false,
			func(s *State) string { return s.HAMiSchedulerVersion }, func(s *State, v string) { s.HAMiSchedulerVersion = v }, nil).with(hamiSelected),
		toggle("Accelerators", "vGPU secrets ready", "readiness only; license/private registry values stay encrypted in SOPS",
			func(s *State) bool { return s.VGPUSecretReady }, func(s *State, v bool) { s.VGPUSecretReady = v }).with(vgpuSelected),
		toggle("Accelerators", "Allow unassigned GPUs", "required for detect-only; creates no product or entitlement",
			func(s *State) bool { return s.AllowUnassignedGPUs }, func(s *State, v bool) { s.AllowUnassignedGPUs = v }).with(func(s *State) bool { return s.GPUPlatform == "detect-only" }),

		// --- Adopt (only in adopt mode) ---
		toggle("Adopt", "Bypass version-pin gate", "RISKY — proceed unpinned (default: pin first)",
			func(s *State) bool { return s.AllowUnpinnedAdopt }, func(s *State, v bool) { s.AllowUnpinnedAdopt = v }).with(isAdopt),

		// --- Gates ---
		toggle("Gates", "Allow DNS not ready", "proceed even if wildcard DNS isn't wired (certs stay Pending)",
			func(s *State) bool { return s.AllowDNSNotReady }, func(s *State, v bool) { s.AllowDNSNotReady = v }),
		toggle("Gates", "Allow node without /dev/kvm", "proceed when no node exposes /dev/kvm (nested/cloud VMs); VM workloads (KubeVirt / managed-K8s) won't schedule until one does",
			func(s *State) bool { return s.AllowNoKubevirtEligible }, func(s *State, v bool) { s.AllowNoKubevirtEligible = v }),

		// --- Review ---
		{Section: "Review", Label: "Apply this configuration", Desc: "validate + build the plan + install", Kind: panelAction},
	}
	return fields
}

func validateGPUNodeModesField(s string) error {
	_, err := clusterinit.ParseGPUNodeModes([]string{s})
	return err
}

// with attaches a Visible predicate (fluent helper for panelFields).
func (f panelField) with(vis func(*State) bool) panelField { f.Visible = vis; return f }

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// sectionsOf returns the ordered unique section names from the field set.
func sectionsOf(fields []panelField) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if !seen[f.Section] {
			seen[f.Section] = true
			out = append(out, f.Section)
		}
	}
	return out
}

// PanelModel is the Bubble Tea model for the install settings panel.
type PanelModel struct {
	st     *State
	fields []panelField
	hint   string // sibling object-storage hint (rendered in Review)

	secCursor   int
	fieldCursor int // index into the CURRENT section's visible fields
	focus       panelFocus

	editing bool
	input   textinput.Model
	// editError is the current text-field validation error. It is rendered
	// inline while editing so Enter never appears to do nothing.
	editError string

	// fieldsVP scrolls the fields pane so a section with many fields (e.g.
	// Network / Storage) stays fully reachable on any terminal height.
	fieldsVP viewport.Model
	// fieldCursorLine is the content line of the focused field, so View can
	// scroll fieldsVP to keep the cursor visible.
	fieldCursorLine int
	// help + keys render the footer help bar; showHelp toggles the '?'
	// full-help overlay.
	help     help.Model
	keys     panelKeyMap
	showHelp bool

	// draftPath is where 'S' writes the save-draft spec (decide later).
	draftPath string
	// notice is a transient footer message (e.g. "saved draft → …").
	notice string

	width, height int

	// Outcomes read by RunPanel after the program exits.
	applied   bool
	cancelled bool
}

type panelFocus int

const (
	focusSections panelFocus = iota
	focusFields
)

// NewPanelModel builds the panel over st. siblingHint (may be "") is
// shown in the Review section.
func NewPanelModel(st *State, siblingHint string) *PanelModel {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 512

	h := help.New()
	h.Styles.ShortKey = bttui.KeyLabel
	h.Styles.ShortDesc = bttui.Muted
	h.Styles.ShortSeparator = bttui.Muted
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc

	return &PanelModel{
		st: st, fields: panelFields(), hint: siblingHint, input: ti,
		fieldsVP: viewport.New(), help: h, keys: defaultPanelKeys(),
		draftPath: defaultDraftPath,
	}
}

// visibleSections returns the section names that currently have ≥1
// visible field (so e.g. "Adopt" only appears in adopt mode). Review is
// always shown (it holds the Apply action). Order follows the field set.
func (m *PanelModel) visibleSections() []string {
	var out []string
	for _, s := range sectionsOf(m.fields) {
		if s == "Review" || len(m.visibleInSection(s)) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// clampCursors keeps secCursor/fieldCursor in range after a visibility
// change (e.g. cycling Mode adds/removes the Adopt section).
func (m *PanelModel) clampCursors() {
	if n := len(m.visibleSections()); m.secCursor >= n {
		m.secCursor = maxInt0(n-1, 0)
	}
	if m.secCursor < 0 {
		m.secCursor = 0
	}
	m.clampFieldCursor()
}

func (m *PanelModel) Init() tea.Cmd { return nil }

// visibleInSection returns the currently-visible fields of section i.
func (m *PanelModel) visibleInSection(section string) []panelField {
	var out []panelField
	for _, f := range m.fields {
		if f.Section == section && f.visible(m.st) {
			out = append(out, f)
		}
	}
	return out
}

func (m *PanelModel) currentSection() string {
	secs := m.visibleSections()
	if m.secCursor < 0 || m.secCursor >= len(secs) {
		return ""
	}
	return secs[m.secCursor]
}

func (m *PanelModel) currentFields() []panelField { return m.visibleInSection(m.currentSection()) }

func (m *PanelModel) clampFieldCursor() {
	n := len(m.currentFields())
	if m.fieldCursor >= n {
		m.fieldCursor = maxInt0(n-1, 0)
	}
	if m.fieldCursor < 0 {
		m.fieldCursor = 0
	}
}

func maxInt0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// validationErrors returns one message per unsatisfied REQUIRED/invalid
// visible field — the Apply gate + footer summary. Pure.
func (m *PanelModel) validationErrors() []string {
	var errs []string
	for _, f := range m.fields {
		if f.Kind != panelText || !f.visible(m.st) {
			continue
		}
		v := strings.TrimSpace(f.Get(m.st))
		if f.Required && v == "" {
			errs = append(errs, f.Section+"/"+f.Label+": required")
			continue
		}
		if v != "" && f.Validate != nil {
			if err := f.Validate(v); err != nil {
				errs = append(errs, f.Section+"/"+f.Label+": "+err.Error())
			}
		}
	}
	// Object-storage disabled needs explicit consent.
	if m.st.OSMode == string(clusterinit.RookDisabled) && !m.st.DisabledConsent {
		errs = append(errs, "Storage: disabled requires the consent toggle")
	}
	// Preset-required keys the panel has no dedicated Required flag for
	// (e.g. EXT_PUBLIC_* for cloud+public-vlan, EXT_NET_* for cloud-vlan) —
	// surface them here so "ready" is HONEST and Apply won't fail later at
	// ValidatePresetRequiredKeys. Best-effort: Apply's consent error is
	// tolerated; scratch is fully populated regardless.
	scratch := &clusterinit.InitOptions{}
	if err := m.st.Apply(scratch); err != nil {
		errs = append(errs, "Accelerators: "+err.Error())
	} else if err := clusterinit.ValidateGPUConfig(scratch); err != nil {
		errs = append(errs, "Accelerators: "+err.Error())
	}
	if err := clusterinit.ValidatePresetRequiredKeys(scratch); err != nil {
		errs = append(errs, "Network: "+err.Error())
	}
	return errs
}

// Applied / Cancelled expose the outcome to RunPanel.
func (m *PanelModel) Applied() bool   { return m.applied }
func (m *PanelModel) Cancelled() bool { return m.cancelled }

// Editing reports whether a text field currently owns the keyboard.
// The root-router uses this for DYNAMIC modality: while editing, every
// key (incl. digits/brackets/'q') is typed input; in nav mode the root's
// tab-switch keys work normally (manual TTY finding 2026-07-20 — the
// static always-modal gate trapped operators on the tab).
func (m *PanelModel) Editing() bool { return m.editing }

// MarkEmbedded adjusts help text for the root-router embed, where 'q'
// in nav mode DISCARDS the form and returns to the Fleet tab instead
// of quitting a standalone program.
func (m *PanelModel) MarkEmbedded() {
	m.keys.Quit = key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "discard + back"))
}

// saveDraft writes the current answers as a reusable spec (the 'S' key —
// "save values, decide later"). Best-effort: a work-in-progress draft may
// be incomplete or have object-storage disabled without consent, so
// Apply's consent error is tolerated — the draft captures whatever is
// filled and reloads via `init --config <draftPath>`.
func (m *PanelModel) saveDraft() {
	scratch := &clusterinit.InitOptions{}
	_ = m.st.Apply(scratch)
	if err := clusterinit.WriteSpec(scratch, m.draftPath); err != nil {
		m.notice = "save failed: " + err.Error()
		return
	}
	m.notice = "saved draft → " + m.draftPath + "  (reload: init --config " + m.draftPath + ")"
}
