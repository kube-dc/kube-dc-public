// Package initform is the T6 Phase-1 wizard for `kube-dc bootstrap
// init` (installer-agentic-implementation-plan §11.T6 + the OS-5
// object-storage screen from installer-object-storage-scaffold.md §7).
//
// **Thin-generator contract** (the load-bearing rule): the form
// populates the SAME `clusterinit.InitOptions` fields the flags do —
// per-field validators delegate to the exported clusterinit checks,
// and after submit the normal `Validate → BuildPlan → Render →
// confirm → Apply` path runs unchanged. No TUI-only semantics;
// anything the wizard produces is expressible as flags (the final
// group shows the equivalent command line for scripting).
//
// v1 scope note: this is the STANDALONE huh runner launched by the
// cobra layer when `init` starts in a TTY with no --name. Embedding
// into the root screen router (formDoneMsg dispatch) + teatest
// snapshots are the T6 polish half, tracked in the tracker row.
// Probe-driven pre-fill (node list / StorageClass picker) is OS-5
// §7.2's "when a kubeconfig is available" enhancement — free-text
// with the shared validators is the designed degraded path and what
// v1 ships.
package initform

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// State collects the raw form answers. Kept as a separate struct
// (rather than writing into InitOptions mid-form) so Apply() is a
// pure, testable mapping and a cancelled form leaves the options
// untouched.
type State struct {
	// basics
	Name, Domain, NodeIP, Email string
	// mode + fleet
	Mode      string
	FleetMode string
	Repo      string
	Provider  string
	Owner     string
	RepoName  string
	// network
	Preset       string
	NetVLANID    string
	NetInterface string
	PubVLANID    string
	PubCIDR      string
	PubGateway   string
	// object storage (OS-5)
	OSMode          string
	OSDNode         string
	OSDSizeGB       string
	OSDDevice       string
	CephNode1       string
	CephNode2       string
	CephNode3       string
	StorageClass    string
	S3Hostname      string
	NoS3Exposure    bool
	DisabledConsent bool
	// gates
	AllowDNSNotReady bool
}

// Apply maps the collected answers onto InitOptions. Pure — no I/O.
// Numeric/pair parsing errors are returned with the field named so
// the cobra layer can surface them cleanly (the per-field validators
// make them near-impossible, but Apply must not trust that).
func (s *State) Apply(o *clusterinit.InitOptions) error {
	o.Name = strings.TrimSpace(s.Name)
	o.Domain = strings.TrimSpace(s.Domain)
	o.NodeExternalIP = strings.TrimSpace(s.NodeIP)
	o.Email = strings.TrimSpace(s.Email)
	o.Mode = clusterinit.Mode(s.Mode)
	o.FleetMode = clusterinit.FleetMode(s.FleetMode)
	if r := strings.TrimSpace(s.Repo); r != "" {
		o.Repo = r
	}
	o.Provider = clusterinit.Provider(s.Provider)
	o.GitHubOwner = strings.TrimSpace(s.Owner)
	o.GitHubRepo = strings.TrimSpace(s.RepoName)
	o.Preset = clusterinit.Preset(s.Preset)

	if o.Sets == nil {
		o.Sets = map[string]string{}
	}
	setIf := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			o.Sets[k] = v
		}
	}
	setIf("EXT_NET_VLAN_ID", s.NetVLANID)
	setIf("EXT_NET_INTERFACE", s.NetInterface)
	if s.Preset == string(clusterinit.PresetCloudPublicVLAN) {
		setIf("EXT_PUBLIC_VLAN_ID", s.PubVLANID)
		setIf("EXT_PUBLIC_CIDR", s.PubCIDR)
		setIf("EXT_PUBLIC_GATEWAY", s.PubGateway)
	}

	o.RookMode = clusterinit.RookMode(s.OSMode)
	// Reviewer P2: the disabled-consequence consent is load-bearing.
	// The Confirm's Validate blocks declining interactively; this
	// guard covers programmatic State construction (tests, future
	// embeddings) so unconsented disabled can never slip through.
	if o.RookMode == clusterinit.RookDisabled && !s.DisabledConsent {
		return fmt.Errorf("object-storage disabled requires the degraded-mode consent (DisabledConsent)")
	}
	switch o.RookMode {
	case clusterinit.RookCephLocal:
		o.RookOSDNode = strings.TrimSpace(s.OSDNode)
		if v := strings.TrimSpace(s.OSDSizeGB); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("osd size: %w", err)
			}
			o.RookOSDSizeGB = n
		}
		o.RookOSDDevice = strings.TrimSpace(s.OSDDevice)
	case clusterinit.RookCephMultiNode:
		nodes := map[string]string{}
		for i, raw := range []string{s.CephNode1, s.CephNode2, s.CephNode3} {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			node, dev, ok := strings.Cut(raw, "=")
			if !ok {
				return fmt.Errorf("ceph node %d: expected NODE=DEVICE", i+1)
			}
			nodes[strings.TrimSpace(node)] = strings.TrimSpace(dev)
		}
		if len(nodes) > 0 {
			o.CephNodes = nodes
		}
	case clusterinit.RookCephPVC:
		o.CephStorageClass = strings.TrimSpace(s.StorageClass)
	}
	o.S3Hostname = strings.TrimSpace(s.S3Hostname)
	o.NoS3Exposure = s.NoS3Exposure
	o.AllowDNSNotReady = s.AllowDNSNotReady
	return nil
}

// EquivalentFlags renders the flag invocation that reproduces this
// form state — printed after submit so a reviewed interactive run is
// trivially scriptable (thin-generator contract made visible).
func (s *State) EquivalentFlags(o *clusterinit.InitOptions) string {
	var b strings.Builder
	b.WriteString("kube-dc bootstrap init \\\n")
	add := func(flag, val string) {
		if val != "" {
			fmt.Fprintf(&b, "  --%s=%s \\\n", flag, shellQuote(val))
		}
	}
	add("name", o.Name)
	add("domain", o.Domain)
	add("node-external-ip", o.NodeExternalIP)
	add("email", o.Email)
	add("mode", string(o.Mode))
	add("fleet-mode", string(o.FleetMode))
	add("repo", o.Repo)
	if o.Provider != "" && o.Provider != clusterinit.ProviderGitHub {
		add("provider", string(o.Provider))
	}
	add("github-owner", o.GitHubOwner)
	add("github-repo", o.GitHubRepo)
	add("preset", string(o.Preset))
	keys := make([]string, 0, len(o.Sets))
	for k := range o.Sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add("set", k+"="+o.Sets[k])
	}
	add("object-storage-mode", string(o.RookMode))
	if o.RookMode == clusterinit.RookCephLocal {
		add("rook-osd-node", o.RookOSDNode)
		if o.RookOSDSizeGB > 0 {
			add("rook-osd-size-gb", strconv.Itoa(o.RookOSDSizeGB))
		}
		add("rook-osd-device", o.RookOSDDevice)
	}
	nodeKeys := make([]string, 0, len(o.CephNodes))
	for k := range o.CephNodes {
		nodeKeys = append(nodeKeys, k)
	}
	sort.Strings(nodeKeys)
	for _, k := range nodeKeys {
		add("ceph-node", k+"="+o.CephNodes[k])
	}
	add("ceph-storage-class", o.CephStorageClass)
	add("s3-hostname", o.S3Hostname)
	if o.NoS3Exposure {
		b.WriteString("  --no-s3-exposure \\\n")
	}
	if o.AllowDNSNotReady {
		b.WriteString("  --allow-dns-not-ready \\\n")
	}
	b.WriteString("  --dry-run")
	return b.String()
}

// Build assembles the huh form over `st`. `siblingHint` (may be "")
// renders next to the object-storage mode select in existing-fleet
// runs — hint, never inherit (OS-5 §7.3).
func Build(st *State, siblingHint string) *huh.Form {
	osDescription := "REQUIRED — Mimir, Loki, Velero and tenant buckets depend on it.\n" +
		"external-ceph / external-s3 are fleet stubs (flag path fails closed)."
	if siblingHint != "" {
		osDescription += "\n" + siblingHint
	}

	return huh.NewForm(
		// --- Group 1: cluster basics ---
		huh.NewGroup(
			huh.NewInput().Title("Cluster name").
				Description("lowercase; nested allowed (eu/dc1)").
				Value(&st.Name).Validate(clusterinit.ValidateClusterNameField),
			huh.NewInput().Title("Domain").
				Description("bare FQDN, e.g. kdc.example.com").
				Value(&st.Domain).Validate(clusterinit.ValidateDomainField),
			huh.NewInput().Title("Node external IP").
				Description("public IP of any cluster node (wildcard DNS target)").
				Value(&st.NodeIP).Validate(clusterinit.ValidateNodeIPField),
			huh.NewInput().Title("Operator email").
				Description("cert-manager / Let's Encrypt registration").
				Value(&st.Email).Validate(clusterinit.ValidateEmailField),
			huh.NewSelect[string]().Title("Mode").
				Options(
					huh.NewOption("install — fresh cluster", "install"),
					huh.NewOption("adopt — existing components present", "adopt"),
					huh.NewOption("resume — continue an interrupted init", "resume"),
				).Value(&st.Mode),
		).Title("Cluster basics"),

		// --- Group 2: fleet repo ---
		huh.NewGroup(
			huh.NewSelect[string]().Title("Fleet mode").
				Options(
					huh.NewOption("existing-fleet — add to a fleet with sibling clusters", "existing-fleet"),
					huh.NewOption("new-repo — create a brand-new fleet repo", "new-repo"),
					huh.NewOption("existing-repo — adopt a repo without fleet structure", "existing-repo"),
				).Value(&st.FleetMode),
			huh.NewInput().Title("Fleet repo path").
				Description("local checkout (empty = $KUBE_DC_FLEET / ~/.kube-dc/fleet)").
				Value(&st.Repo),
			huh.NewSelect[string]().Title("Git provider").
				Options(
					huh.NewOption("github", "github"),
					huh.NewOption("gitlab", "gitlab"),
				).Value(&st.Provider),
			huh.NewInput().Title("Owner / group").
				Description("token resolves via gh/glab auth — never typed here").
				Value(&st.Owner),
			huh.NewInput().Title("Repo name").Value(&st.RepoName),
		).Title("Fleet repo"),

		// --- Group 3: network preset ---
		huh.NewGroup(
			huh.NewSelect[string]().Title("Network preset").
				Options(
					huh.NewOption("cloud-vlan — shared-NAT tenant networking", "cloud-vlan"),
					huh.NewOption("cloud+public-vlan — adds a routed public VLAN", "cloud+public-vlan"),
					huh.NewOption("internal-only — no external networks", "internal-only"),
					huh.NewOption("custom — bring your own --set keys", "custom"),
				).Value(&st.Preset),
			huh.NewInput().Title("EXT_NET_VLAN_ID").Value(&st.NetVLANID),
			huh.NewInput().Title("EXT_NET_INTERFACE").Description("e.g. bond0").Value(&st.NetInterface),
		).Title("Network"),

		// Conditional: public-VLAN keys only for cloud+public-vlan
		// (the M4-T04 preset gate would demand them anyway — asking
		// up front beats a validation bounce).
		huh.NewGroup(
			huh.NewInput().Title("EXT_PUBLIC_VLAN_ID").Value(&st.PubVLANID),
			huh.NewInput().Title("EXT_PUBLIC_CIDR").Description("e.g. 203.0.113.48/29").Value(&st.PubCIDR),
			huh.NewInput().Title("EXT_PUBLIC_GATEWAY").Value(&st.PubGateway),
		).Title("Public VLAN (cloud+public-vlan)").
			WithHideFunc(func() bool { return st.Preset != string(clusterinit.PresetCloudPublicVLAN) }),

		// --- Group 4: object storage (OS-5 §7.2) ---
		huh.NewGroup(
			huh.NewSelect[string]().Title("Object storage").
				Description(osDescription).
				Options(
					huh.NewOption("rook-ceph-multi-node — 3+ nodes, raw devices (production HA)", "rook-ceph-multi-node"),
					huh.NewOption("rook-ceph-local — single node, loop file (dev/small)", "rook-ceph-local"),
					huh.NewOption("rook-ceph-pvc — OSDs on PVCs (CSI clouds)", "rook-ceph-pvc"),
					huh.NewOption("disabled — NO S3; degraded/dev only", "disabled"),
				).Value(&st.OSMode),
		).Title("Object storage"),

		huh.NewGroup(
			huh.NewInput().Title("OSD node").
				Value(&st.OSDNode).Validate(clusterinit.ValidateK8sNodeNameField),
			huh.NewInput().Title("OSD size (GB)").Description("default 500").
				Value(&st.OSDSizeGB).Validate(validateOptionalInt),
			huh.NewInput().Title("OSD device").Description("empty = loop0 (loop file)").
				Value(&st.OSDDevice).Validate(clusterinit.ValidateDeviceNameField),
		).Title("rook-ceph-local").
			WithHideFunc(func() bool { return st.OSMode != string(clusterinit.RookCephLocal) }),

		huh.NewGroup(
			huh.NewInput().Title("Ceph node 1").Description("NODE=DEVICE, e.g. host5-a=sdb").
				Value(&st.CephNode1).Validate(clusterinit.ValidateNodeDevicePairField),
			huh.NewInput().Title("Ceph node 2").
				Value(&st.CephNode2).Validate(clusterinit.ValidateNodeDevicePairField),
			huh.NewInput().Title("Ceph node 3").
				Description("exactly 3 in v1 — 2-host topologies hand-patch slot 3").
				Value(&st.CephNode3).Validate(clusterinit.ValidateNodeDevicePairField),
		).Title("rook-ceph-multi-node").
			WithHideFunc(func() bool { return st.OSMode != string(clusterinit.RookCephMultiNode) }),

		huh.NewGroup(
			huh.NewInput().Title("StorageClass").
				Value(&st.StorageClass).Validate(clusterinit.ValidateStorageClassField),
		).Title("rook-ceph-pvc").
			WithHideFunc(func() bool { return st.OSMode != string(clusterinit.RookCephPVC) }),

		huh.NewGroup(
			huh.NewInput().Title("S3 hostname").Description("empty = s3.<domain>").
				Value(&st.S3Hostname).Validate(clusterinit.ValidateOptionalDomainField),
			huh.NewConfirm().Title("Expose S3 publicly (HTTPRoute + TLS)?").
				Value(&st.NoS3Exposure).
				Affirmative("No — internal only").Negative("Yes — expose"),
		).Title("S3 endpoint").
			WithHideFunc(func() bool {
				return st.OSMode == string(clusterinit.RookDisabled) || st.OSMode == ""
			}),

		// disabled consequence confirm (OS-5 §7.4: the TUI equivalent
		// of "the explicit mode is the consent").
		huh.NewGroup(
			huh.NewConfirm().
				Title("Object storage DISABLED — confirm the consequences").
				Description("Mimir + Loki SUSPENDED (no metrics/logs storage);\ngrafana-pg backups + WAL archiving OFF (DB unprotected);\nNEVER for customer-ready clusters.").
				Value(&st.DisabledConsent).
				Affirmative("I understand — dev cluster").Negative("Go back").
				// Reviewer P2: consent is ENFORCED, not decorative —
				// declining blocks here (navigate back and pick a
				// real mode instead); Apply double-checks (below).
				Validate(func(ok bool) error {
					if !ok {
						return fmt.Errorf("declined — go back (shift+tab) and choose a rook-ceph-* mode, or confirm the degraded consequences")
					}
					return nil
				}),
		).Title("Degraded mode").
			WithHideFunc(func() bool { return st.OSMode != string(clusterinit.RookDisabled) }),

		// --- Group 5: gates ---
		huh.NewGroup(
			huh.NewConfirm().Title("Proceed even if wildcard DNS isn't wired yet?").
				Description("TLS certs stay Pending until *.domain resolves").
				Value(&st.AllowDNSNotReady).
				Affirmative("Yes (--allow-dns-not-ready)").Negative("No — gate on DNS"),
		).Title("Gates"),
	)
}

// validateOptionalInt accepts empty (defaulted) or a positive integer.
func validateOptionalInt(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return fmt.Errorf("positive integer (or empty for the default)")
	}
	return nil
}

// Run builds + runs the wizard interactively, applies the answers to
// `o`, and returns the equivalent-flags rendering for the cobra layer
// to print. A cancelled form (Ctrl+C / Esc) returns huh's abort error
// and leaves `o` untouched.
func Run(o *clusterinit.InitOptions, siblingHint string) (string, error) {
	st := &State{
		Mode:      string(clusterinit.ModeInstall),
		FleetMode: string(o.FleetMode),
		Provider:  "github",
		Preset:    string(clusterinit.PresetCloudVLAN),
		Repo:      o.Repo,
	}
	if st.FleetMode == "" {
		st.FleetMode = string(clusterinit.FleetExistingFleet)
	}
	if err := Build(st, siblingHint).Run(); err != nil {
		return "", err
	}
	if err := st.Apply(o); err != nil {
		return "", err
	}
	return st.EquivalentFlags(o), nil
}

// shellQuote makes a flag value safe to paste into a POSIX shell
// (reviewer P3: "script-ready" must survive repo paths with spaces).
// Values made purely of safe chars pass through untouched; anything
// else is single-quoted with embedded quotes escaped.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case strings.ContainsRune("@%_+=:,./-", r):
			return false
		}
		return true
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
