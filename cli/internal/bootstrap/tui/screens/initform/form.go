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
	// adopt-only: the --allow-unpinned-adopt bypass. Default false
	// (the SAFE path — pin versions first). Collected only when
	// Mode==adopt, behind an explicit danger confirmation.
	AllowUnpinnedAdopt bool
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
	o.SSHHost = strings.TrimSpace(s.SSHHost)
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
	setIf("KUBE_OVN_MASTER_NODES", s.KubeOVNMasterNodes)
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
	// The bypass only carries meaning in adopt mode; never emit it for
	// install/resume (keeps the plan hash + equivalent flags honest).
	o.AllowUnpinnedAdopt = s.AllowUnpinnedAdopt && o.Mode == clusterinit.ModeAdopt
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
	add("s3-hostname", o.S3Hostname)
	if o.NoS3Exposure {
		b.WriteString("  --no-s3-exposure \\\n")
	}
	if o.AllowDNSNotReady {
		b.WriteString("  --allow-dns-not-ready \\\n")
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
