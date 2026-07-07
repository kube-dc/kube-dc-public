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

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// State collects the raw form answers. Kept as a separate struct
// (rather than writing into InitOptions mid-form) so Apply() is a
// pure, testable mapping and a cancelled form leaves the options
// untouched.
type State struct {
	// basics
	Name, Domain, NodeIP, Email string
	// SSHHost (user@host) enables the same NAT-topology detection the
	// flag path does: init SSH-probes whether NodeIP is actually bound on
	// the node and, behind a 1:1 NAT / cloud FIP, writes the arriving
	// (internal) IP into NODE_EXTERNAL_IP + drops the 6443 listener
	// (findings 17/17b). Empty = skip (a node with its public IP bound
	// locally needs none).
	SSHHost string
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
	// KubeOVNMasterNodes → KUBE_OVN_MASTER_NODES: the control-plane
	// INTERNAL IPs (comma-separated) kube-ovn binds its master on. Not
	// in any preset default + not caught by preset validation, but the
	// install can't come up without it — so the wizard must collect it
	// (every preset). IPs, not hostnames.
	KubeOVNMasterNodes string
	PubVLANID          string
	PubCIDR            string
	PubGateway         string
	// network POINTING (operator hardware/topology → o.Sets). GWNodes is
	// the OVN external-gateway node list (KUBE_OVN_GW_NODES; anchors must be
	// a subset — the preset cross-checks it); GWType is centralized|
	// distributed (KUBE_OVN_GW_TYPE).
	GWNodes string
	GWType  string
	// object storage (OS-5)
	OSMode    string
	OSDNode   string
	OSDSizeGB string
	OSDDevice string
	CephNode1 string
	CephNode2 string
	CephNode3 string
	// CephReplicationSize → CEPH_REPLICATION_SIZE: OSD replica count (disk
	// durability pointer — 1 = no redundancy dev, 2/3 = HA). Empty = fleet default.
	CephReplicationSize string
	StorageClass        string
	// rook-ceph-pvc OSD sizing → CEPH_OSD_COUNT / CEPH_OSD_VOLUME_SIZE_GB.
	// Disk pointers made operator-visible so a prefill/clone round-trips
	// them through the panel (they were InitOptions-only before). Empty =
	// fleet default.
	CephOSDCount      string
	CephOSDVolumeSize string
	S3Hostname        string
	NoS3Exposure      bool
	DisabledConsent   bool
	// gates
	AllowDNSNotReady bool
	// AllowNoKubevirtEligible → --allow-no-kubevirt-eligible: proceed when
	// no node exposes /dev/kvm (the M6-T05 NFD gate would otherwise block).
	// Common for nested/cloud VMs without nested virt — a first-time user on
	// such a host needs this to complete an install. Default false (the gate
	// enforces); VM workloads won't schedule until a KVM node exists.
	AllowNoKubevirtEligible bool
	// adopt-only: the --allow-unpinned-adopt bypass. Default false
	// (the SAFE path — pin versions first). Collected only when
	// Mode==adopt, behind an explicit danger confirmation.
	AllowUnpinnedAdopt bool
	// ExtraSets carries every --set overlay key WITHOUT a dedicated field
	// (EXT_NET_MTU, MetalLB, EXT_NET_ANCHOR_*, EXT_PUBLIC_EXCLUDE_IPS_*,
	// platform-endpoints, SMTP, quotas, feature flags). Populated by
	// FromOptions from a prefill/clone and merged back in Apply, so a
	// cloned config survives the panel round-trip untouched ("host all
	// variants"). Keyed by the cluster-config.env key.
	ExtraSets map[string]string
}

// fieldBackedOverlayKeys are the o.Sets/--set keys that HAVE a dedicated
// panel field (so FromOptions maps them to State fields, not ExtraSets).
// Every other overlay key round-trips through ExtraSets.
var fieldBackedOverlayKeys = map[string]bool{
	"EXT_NET_VLAN_ID": true, "EXT_NET_INTERFACE": true, "KUBE_OVN_MASTER_NODES": true,
	"EXT_PUBLIC_VLAN_ID": true, "EXT_PUBLIC_CIDR": true, "EXT_PUBLIC_GATEWAY": true,
	"KUBE_OVN_GW_NODES": true, "KUBE_OVN_GW_TYPE": true, "CEPH_REPLICATION_SIZE": true,
}

// Apply maps the collected answers onto InitOptions. Pure — no I/O.
// Numeric/pair parsing errors are returned with the field named so
// the cobra layer can surface them cleanly (the per-field validators
// make them near-impossible, but Apply must not trust that).
func (s *State) Apply(o *clusterinit.InitOptions) error {
	// REPLACEMENT model (reviewer P1): State is the complete post-panel
	// truth, so Apply REBUILDS every panel-owned field rather than layering
	// onto whatever was prefilled — a cleared field, or a switched
	// storage/network mode, must not leak the old value. (Non-panel fields
	// like GitHubToken / NodeNICs / Addons are untouched.)
	o.Name = strings.TrimSpace(s.Name)
	o.Domain = strings.TrimSpace(s.Domain)
	o.NodeExternalIP = strings.TrimSpace(s.NodeIP)
	o.Email = strings.TrimSpace(s.Email)
	o.SSHHost = strings.TrimSpace(s.SSHHost)
	o.Mode = clusterinit.Mode(s.Mode)
	o.FleetMode = clusterinit.FleetMode(s.FleetMode)
	o.Repo = strings.TrimSpace(s.Repo)
	o.Provider = clusterinit.Provider(s.Provider)
	o.GitHubOwner = strings.TrimSpace(s.Owner)
	o.GitHubRepo = strings.TrimSpace(s.RepoName)
	o.Preset = clusterinit.Preset(s.Preset)

	// Rebuild the --set overlay from scratch: preserved advanced keys first,
	// then the field-backed keys on top (a dedicated field always wins over
	// a stale ExtraSets entry). A cleared field simply isn't written.
	o.Sets = map[string]string{}
	setIf := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			o.Sets[k] = v
		}
	}
	for k, v := range s.ExtraSets {
		setIf(k, v)
	}
	setIf("EXT_NET_VLAN_ID", s.NetVLANID)
	setIf("EXT_NET_INTERFACE", s.NetInterface)
	setIf("KUBE_OVN_MASTER_NODES", s.KubeOVNMasterNodes)
	setIf("KUBE_OVN_GW_NODES", s.GWNodes)
	setIf("KUBE_OVN_GW_TYPE", s.GWType)
	setIf("CEPH_REPLICATION_SIZE", s.CephReplicationSize)
	// Public-VLAN keys ONLY for the public preset — switching away drops the
	// stale EXT_PUBLIC_* values (o.Sets was rebuilt fresh).
	if s.Preset == string(clusterinit.PresetCloudPublicVLAN) {
		setIf("EXT_PUBLIC_VLAN_ID", s.PubVLANID)
		setIf("EXT_PUBLIC_CIDR", s.PubCIDR)
		setIf("EXT_PUBLIC_GATEWAY", s.PubGateway)
	}

	// Reset ALL mode-specific storage fields, then set for the selected mode
	// — switching e.g. multi-node → local must not leave stale CephNodes.
	o.RookMode = clusterinit.RookMode(s.OSMode)
	o.RookOSDNode, o.RookOSDDevice, o.RookOSDSizeGB = "", "", 0
	o.CephNodes = nil
	o.CephStorageClass, o.CephOSDCount, o.CephOSDVolumeSizeGB = "", 0, 0
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
		if v := strings.TrimSpace(s.CephOSDCount); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("ceph osd count: %w", err)
			}
			o.CephOSDCount = n
		}
		if v := strings.TrimSpace(s.CephOSDVolumeSize); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("ceph osd volume size: %w", err)
			}
			o.CephOSDVolumeSizeGB = n
		}
	}
	o.S3Hostname = strings.TrimSpace(s.S3Hostname)
	o.NoS3Exposure = s.NoS3Exposure
	o.AllowDNSNotReady = s.AllowDNSNotReady
	o.AllowNoKubevirtEligible = s.AllowNoKubevirtEligible
	// The bypass only carries meaning in adopt mode; never emit it for
	// install/resume (keeps the plan hash + equivalent flags honest).
	o.AllowUnpinnedAdopt = s.AllowUnpinnedAdopt && o.Mode == clusterinit.ModeAdopt
	// Consent gate LAST (reviewer P2): the disabled-consequence consent is
	// load-bearing — the panel's validationErrors blocks Apply before here
	// and the huh Confirm blocks declining — but it must run AFTER all field
	// mapping so the draft-save path (saveDraft → Apply into a scratch,
	// error ignored) still captures every field, incl. gates, of an
	// unconsented disabled-mode work-in-progress.
	if o.RookMode == clusterinit.RookDisabled && !s.DisabledConsent {
		return fmt.Errorf("object-storage disabled requires the degraded-mode consent (DisabledConsent)")
	}
	return nil
}

// FromOptions overlays a (possibly prefilled) InitOptions onto the State
// — the inverse of Apply, used to open the wizard PRE-FILLED from
// --config / KUBE_DC_INIT_* env / flags. Only non-empty values overlay, so
// it composes over the wizard's defaults (defaults < prefill) without
// clobbering them with zero values. Pure.
func (s *State) FromOptions(o *clusterinit.InitOptions) {
	set := func(dst *string, v string) {
		if strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	set(&s.Name, o.Name)
	set(&s.Domain, o.Domain)
	set(&s.NodeIP, o.NodeExternalIP)
	set(&s.Email, o.Email)
	set(&s.SSHHost, o.SSHHost)
	set(&s.Mode, string(o.Mode))
	set(&s.FleetMode, string(o.FleetMode))
	set(&s.Repo, o.Repo)
	set(&s.Provider, string(o.Provider))
	set(&s.Owner, o.GitHubOwner)
	set(&s.RepoName, o.GitHubRepo)
	set(&s.Preset, string(o.Preset))
	if o.Sets != nil {
		set(&s.NetVLANID, o.Sets["EXT_NET_VLAN_ID"])
		set(&s.NetInterface, o.Sets["EXT_NET_INTERFACE"])
		set(&s.KubeOVNMasterNodes, o.Sets["KUBE_OVN_MASTER_NODES"])
		set(&s.GWNodes, o.Sets["KUBE_OVN_GW_NODES"])
		set(&s.GWType, o.Sets["KUBE_OVN_GW_TYPE"])
		set(&s.CephReplicationSize, o.Sets["CEPH_REPLICATION_SIZE"])
		set(&s.PubVLANID, o.Sets["EXT_PUBLIC_VLAN_ID"])
		set(&s.PubCIDR, o.Sets["EXT_PUBLIC_CIDR"])
		set(&s.PubGateway, o.Sets["EXT_PUBLIC_GATEWAY"])
		// Every other overlay key (no dedicated field) → ExtraSets, so a
		// prefilled/cloned config survives the panel untouched.
		for k, v := range o.Sets {
			if fieldBackedOverlayKeys[k] {
				continue
			}
			if s.ExtraSets == nil {
				s.ExtraSets = map[string]string{}
			}
			s.ExtraSets[k] = v
		}
	}
	set(&s.OSMode, string(o.RookMode))
	set(&s.OSDNode, o.RookOSDNode)
	if o.RookOSDSizeGB > 0 {
		s.OSDSizeGB = strconv.Itoa(o.RookOSDSizeGB)
	}
	set(&s.OSDDevice, o.RookOSDDevice)
	if len(o.CephNodes) > 0 {
		nodes := make([]string, 0, len(o.CephNodes))
		for n := range o.CephNodes {
			nodes = append(nodes, n)
		}
		sort.Strings(nodes)
		slots := []*string{&s.CephNode1, &s.CephNode2, &s.CephNode3}
		for i, n := range nodes {
			if i < len(slots) {
				*slots[i] = n + "=" + o.CephNodes[n]
			}
		}
	}
	set(&s.StorageClass, o.CephStorageClass)
	if o.CephOSDCount > 0 {
		s.CephOSDCount = strconv.Itoa(o.CephOSDCount)
	}
	if o.CephOSDVolumeSizeGB > 0 {
		s.CephOSDVolumeSize = strconv.Itoa(o.CephOSDVolumeSizeGB)
	}
	set(&s.S3Hostname, o.S3Hostname)
	if o.NoS3Exposure {
		s.NoS3Exposure = true
	}
	if o.AllowDNSNotReady {
		s.AllowDNSNotReady = true
	}
	if o.AllowNoKubevirtEligible {
		s.AllowNoKubevirtEligible = true
	}
	if o.AllowUnpinnedAdopt {
		s.AllowUnpinnedAdopt = true
	}
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
	add("ssh-host", o.SSHHost)
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
	if o.CephOSDCount > 0 {
		add("ceph-osd-count", strconv.Itoa(o.CephOSDCount))
	}
	if o.CephOSDVolumeSizeGB > 0 {
		add("ceph-osd-volume-size-gb", strconv.Itoa(o.CephOSDVolumeSizeGB))
	}
	add("s3-hostname", o.S3Hostname)
	if o.NoS3Exposure {
		b.WriteString("  --no-s3-exposure \\\n")
	}
	if o.AllowDNSNotReady {
		b.WriteString("  --allow-dns-not-ready \\\n")
	}
	if o.AllowNoKubevirtEligible {
		b.WriteString("  --allow-no-kubevirt-eligible \\\n")
	}
	if o.AllowUnpinnedAdopt {
		b.WriteString("  --allow-unpinned-adopt \\\n")
	}
	b.WriteString("  --dry-run")
	return b.String()
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
