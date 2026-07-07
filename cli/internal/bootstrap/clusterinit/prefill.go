package clusterinit

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// prefill.go is the bidirectional map between a cluster-config.env-native
// KEY=VALUE surface and InitOptions — the substrate for `bootstrap init
// --config <file.env>` / `KUBE_DC_INIT_*` env / `--save-config` and the
// TUI's 'S' save-draft.
//
// Design (see docs/prd/installer-agentic-tracker.md): the prefill format
// is UNIFIED on the fleet's own cluster-config.env, not a parallel YAML —
//   - config keys use the SAME names cluster-config.env uses, so an
//     existing cluster's file is a valid prefill (clone-from-sibling);
//   - install-only ORCHESTRATION inputs (mode, fleet-mode, git repo,
//     ssh-host, gates — things that must NEVER be reconciled into a live
//     cluster) use a reserved KUBE_DC_INIT_ prefix and are stripped before
//     the scaffold writes the real cluster-config.env.
//
// The importer only recognizes the operator-INPUT key set; version /
// derived / feature keys in a sibling's file are ignored (returned for a
// "N ignored" log) so a clone pulls topology, not stale version pins.

// InitPrefix namespaces the install-only orchestration keys.
const InitPrefix = "KUBE_DC_INIT_"

// Orchestration (install-only) canonical keys.
const (
	KeyMode         = InitPrefix + "MODE"
	KeyFleetMode    = InitPrefix + "FLEET_MODE"
	KeyPreset       = InitPrefix + "PRESET"
	KeyProvider     = InitPrefix + "PROVIDER"
	KeyGitHubOwner  = InitPrefix + "GITHUB_OWNER"
	KeyGitHubRepo   = InitPrefix + "GITHUB_REPO"
	KeyRepo         = InitPrefix + "REPO"
	KeySSHHost      = InitPrefix + "SSH_HOST"
	KeyAllowDNS     = InitPrefix + "ALLOW_DNS_NOT_READY"
	KeyAllowNoKVM   = InitPrefix + "ALLOW_NO_KVM"
	KeyAllowUnpin   = InitPrefix + "ALLOW_UNPINNED_ADOPT"
	KeyNoS3Exposure = InitPrefix + "NO_S3_EXPOSURE"
)

// denyImportExact are the keys the scaffold/preset OWNS or recomputes:
// domain-derived endpoints, the OVN DB IPs, and the universal + preset
// network defaults. A clone must NOT carry these — they'd override the new
// cluster's computed/preset values (e.g. a sibling's KUBE_API_EXTERNAL_URL
// points at the SIBLING's domain). Everything NOT denied and NOT a
// dedicated field is operator config → carried into o.Sets, so
// clone-from-sibling is lossless for the operator's topology + features.
var denyImportExact = map[string]bool{
	"KUBE_DC_VERSION": true, "CEPH_IMAGE": true, "KMS_PLUGIN_IMAGE": true,
	"KUBE_API_EXTERNAL_URL": true, "KEYCLOAK_HOSTNAME": true, "OVN_DB_IPS": true,
	"POD_CIDR": true, "POD_GATEWAY": true, "SVC_CIDR": true, "K8S_SERVICE_IP": true,
	"CLUSTER_DNS": true, "JOIN_CIDR": true,
	"EXT_NET_NAME": true, "EXT_NET_TYPE": true, "EXT_NET_CIDR": true,
	"EXT_NET_GATEWAY": true, "EXT_NET_EXCLUDE_IPS": true,
	"DEFAULT_GW_NETWORK_TYPE": true, "DEFAULT_EIP_NETWORK_TYPE": true,
	"DEFAULT_FIP_NETWORK_TYPE": true, "DEFAULT_SVC_LB_NETWORK_TYPE": true,
}

// denyImport reports whether a source key is scaffold/preset-owned and must
// not ride into a clone: the exact set above, plus any version/image tag
// (suffix _VERSION / _TAG — every component pin).
func denyImport(k string) bool {
	return denyImportExact[k] ||
		strings.HasSuffix(k, "_VERSION") || strings.HasSuffix(k, "_TAG")
}

// maxCephSlots is the fixed multi-node OSD slot count (v1 fleet template).
// The scaffold writes each slot as TWO keys — CEPH_NODE_N (host) +
// CEPH_NODE_N_DEVICE (device) — so the prefill must match that shape
// exactly (not a combined "node=device"), or clone-from-sibling silently
// drops the device mapping.
const maxCephSlots = 3

// specOrder is the canonical write order for a saved spec (identity →
// network → storage → orchestration), so `--save-config` diffs are stable.
var specOrder = []string{
	"CLUSTER_NAME", "DOMAIN", "NODE_EXTERNAL_IP", "EMAIL",
	"EXT_NET_VLAN_ID", "EXT_NET_INTERFACE", "EXT_NET_MTU", "KUBE_OVN_MASTER_NODES",
	"EXT_PUBLIC_VLAN_ID", "EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY",
	"OBJECT_STORAGE_MODE",
	"CEPH_LOCAL_OSD_NODE", "CEPH_LOCAL_OSD_SIZE_GB", "CEPH_LOCAL_OSD_DEVICE",
	"CEPH_NODE_1", "CEPH_NODE_1_DEVICE", "CEPH_NODE_2", "CEPH_NODE_2_DEVICE",
	"CEPH_NODE_3", "CEPH_NODE_3_DEVICE",
	"CEPH_OSD_STORAGE_CLASS", "CEPH_OSD_COUNT", "CEPH_OSD_VOLUME_SIZE_GB",
	"S3_HOSTNAME",
	KeyMode, KeyFleetMode, KeyPreset, KeyProvider,
	KeyGitHubOwner, KeyGitHubRepo, KeyRepo, KeySSHHost,
	KeyAllowDNS, KeyAllowNoKVM, KeyAllowUnpin, KeyNoS3Exposure,
}

// parsePrefillBool accepts the env-file truthy spellings.
func parsePrefillBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// ImportMap seeds o from a prefill source map (native cluster-config.env
// keys + KUBE_DC_INIT_* orchestration keys), skipping any field whose flag
// the operator already set explicitly (flagChanged) so precedence stays
// defaults < prefill < flags. Overlay keys already present in o.Sets (from
// an explicit --set) are left untouched. Returns the source keys it did
// not recognize, sorted (for a "N ignored" log). Pure.
func ImportMap(o *InitOptions, src map[string]string, flagChanged func(flag string) bool) []string {
	if o.Sets == nil {
		o.Sets = map[string]string{}
	}
	if o.CephNodes == nil {
		o.CephNodes = map[string]string{}
	}
	seen := map[string]bool{}

	str := func(key, flag string, dst *string) {
		v, ok := src[key]
		if !ok {
			return
		}
		seen[key] = true
		if !flagChanged(flag) && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	boolean := func(key, flag string, dst *bool) {
		v, ok := src[key]
		if !ok {
			return
		}
		seen[key] = true
		if !flagChanged(flag) {
			*dst = parsePrefillBool(v)
		}
	}

	// --- promoted config keys → dedicated fields ---
	str("CLUSTER_NAME", "name", &o.Name)
	str("DOMAIN", "domain", &o.Domain)
	str("NODE_EXTERNAL_IP", "node-external-ip", &o.NodeExternalIP)
	str("EMAIL", "email", &o.Email)
	str("CEPH_LOCAL_OSD_NODE", "rook-osd-node", &o.RookOSDNode)
	str("CEPH_LOCAL_OSD_DEVICE", "rook-osd-device", &o.RookOSDDevice)
	str("CEPH_OSD_STORAGE_CLASS", "ceph-storage-class", &o.CephStorageClass)
	str("S3_HOSTNAME", "s3-hostname", &o.S3Hostname)
	intKey := func(key, flag string, dst *int) {
		v, ok := src[key]
		if !ok {
			return
		}
		seen[key] = true
		if !flagChanged(flag) {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
	}
	if v, ok := src["OBJECT_STORAGE_MODE"]; ok {
		seen["OBJECT_STORAGE_MODE"] = true
		// Docs promise flags win: honour BOTH the canonical flag and the
		// deprecated --rook-mode alias so `--rook-mode=X --config f` keeps X.
		if !flagChanged("object-storage-mode") && !flagChanged("rook-mode") && strings.TrimSpace(v) != "" {
			o.RookMode = RookMode(strings.TrimSpace(v))
		}
	}
	intKey("CEPH_LOCAL_OSD_SIZE_GB", "rook-osd-size-gb", &o.RookOSDSizeGB)
	// multi-node OSD slots: CEPH_NODE_N (host) + CEPH_NODE_N_DEVICE (device),
	// matching the scaffold writer's shape exactly (objectstorage.go).
	for i := 1; i <= maxCephSlots; i++ {
		slot := strconv.Itoa(i)
		nodeKey, devKey := "CEPH_NODE_"+slot, "CEPH_NODE_"+slot+"_DEVICE"
		host, hasHost := src[nodeKey]
		if _, hasDev := src[devKey]; hasDev {
			seen[devKey] = true
		}
		if hasHost {
			seen[nodeKey] = true
			if strings.TrimSpace(host) != "" && !flagChanged("ceph-node") {
				o.CephNodes[strings.TrimSpace(host)] = strings.TrimSpace(src[devKey])
			}
		}
	}
	// rook-ceph-pvc OSD sizing.
	intKey("CEPH_OSD_COUNT", "ceph-osd-count", &o.CephOSDCount)
	intKey("CEPH_OSD_VOLUME_SIZE_GB", "ceph-osd-volume-size-gb", &o.CephOSDVolumeSizeGB)

	// --- orchestration (install-only) ---
	str(KeySSHHost, "ssh-host", &o.SSHHost)
	str(KeyRepo, "repo", &o.Repo)
	str(KeyGitHubOwner, "github-owner", &o.GitHubOwner)
	str(KeyGitHubRepo, "github-repo", &o.GitHubRepo)
	if v, ok := src[KeyMode]; ok {
		seen[KeyMode] = true
		if !flagChanged("mode") && strings.TrimSpace(v) != "" {
			o.Mode = Mode(strings.TrimSpace(v))
		}
	}
	if v, ok := src[KeyFleetMode]; ok {
		seen[KeyFleetMode] = true
		if !flagChanged("fleet-mode") && strings.TrimSpace(v) != "" {
			o.FleetMode = FleetMode(strings.TrimSpace(v))
		}
	}
	if v, ok := src[KeyPreset]; ok {
		seen[KeyPreset] = true
		if !flagChanged("preset") && strings.TrimSpace(v) != "" {
			o.Preset = Preset(strings.TrimSpace(v))
		}
	}
	if v, ok := src[KeyProvider]; ok {
		seen[KeyProvider] = true
		if !flagChanged("provider") && strings.TrimSpace(v) != "" {
			o.Provider = Provider(strings.TrimSpace(v))
		}
	}
	boolean(KeyAllowDNS, "allow-dns-not-ready", &o.AllowDNSNotReady)
	boolean(KeyAllowNoKVM, "allow-no-kubevirt-eligible", &o.AllowNoKubevirtEligible)
	boolean(KeyAllowUnpin, "allow-unpinned-adopt", &o.AllowUnpinnedAdopt)
	boolean(KeyNoS3Exposure, "no-s3-exposure", &o.NoS3Exposure)

	// --- everything else → o.Sets overlay (deny-list) ---
	// Any remaining key that the scaffold/preset doesn't OWN (denyImport)
	// is operator config — carry it so a clone-from-sibling keeps the
	// operator's topology + features (gateway nodes, MetalLB, anchors,
	// platform-endpoints, SMTP, quotas, feature flags). An explicit --set
	// already in o.Sets wins. Denied keys (versions/derived) fall through
	// to `ignored`.
	var ignored []string
	for k, v := range src {
		if seen[k] {
			continue
		}
		if denyImport(k) {
			ignored = append(ignored, k)
			continue
		}
		if _, already := o.Sets[k]; !already && strings.TrimSpace(v) != "" {
			o.Sets[k] = strings.TrimSpace(v)
		}
	}
	sort.Strings(ignored)
	return ignored
}

// ExportMap renders o's operator-input surface as a prefill map (native
// config keys + KUBE_DC_INIT_* orchestration). Only non-empty values are
// emitted, so a partial draft (save-to-decide-later) stays partial. The
// git TOKEN is never exported (it comes from gh/glab auth). Pure.
func ExportMap(o *InitOptions) map[string]string {
	m := map[string]string{}
	put := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			m[k] = strings.TrimSpace(v)
		}
	}
	put("CLUSTER_NAME", o.Name)
	put("DOMAIN", o.Domain)
	put("NODE_EXTERNAL_IP", o.NodeExternalIP)
	put("EMAIL", o.Email)
	put("OBJECT_STORAGE_MODE", string(o.RookMode))
	put("CEPH_LOCAL_OSD_NODE", o.RookOSDNode)
	if o.RookOSDSizeGB > 0 {
		put("CEPH_LOCAL_OSD_SIZE_GB", strconv.Itoa(o.RookOSDSizeGB))
	}
	put("CEPH_LOCAL_OSD_DEVICE", o.RookOSDDevice)
	put("CEPH_OSD_STORAGE_CLASS", o.CephStorageClass)
	if o.CephOSDCount > 0 {
		put("CEPH_OSD_COUNT", strconv.Itoa(o.CephOSDCount))
	}
	if o.CephOSDVolumeSizeGB > 0 {
		put("CEPH_OSD_VOLUME_SIZE_GB", strconv.Itoa(o.CephOSDVolumeSizeGB))
	}
	put("S3_HOSTNAME", o.S3Hostname)
	// multi-node slots → CEPH_NODE_N (host) + CEPH_NODE_N_DEVICE (device),
	// deterministic by sorted node name — matches the scaffold writer.
	nodes := make([]string, 0, len(o.CephNodes))
	for n := range o.CephNodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for i, n := range nodes {
		if i >= maxCephSlots {
			break
		}
		slot := strconv.Itoa(i + 1)
		put("CEPH_NODE_"+slot, n)
		put("CEPH_NODE_"+slot+"_DEVICE", o.CephNodes[n])
	}
	// The full --set overlay: network + gateway + MetalLB + anchors +
	// platform-endpoints + feature keys all live in o.Sets (deny-list
	// model), so emit every one — that's what makes a saved/cloned spec
	// carry all operator config, not just a curated subset.
	for k, v := range o.Sets {
		put(k, v)
	}
	put(KeyMode, string(o.Mode))
	put(KeyFleetMode, string(o.FleetMode))
	put(KeyPreset, string(o.Preset))
	if o.Provider != "" && o.Provider != ProviderGitHub {
		put(KeyProvider, string(o.Provider))
	}
	put(KeyGitHubOwner, o.GitHubOwner)
	put(KeyGitHubRepo, o.GitHubRepo)
	put(KeyRepo, o.Repo)
	put(KeySSHHost, o.SSHHost)
	if o.AllowDNSNotReady {
		m[KeyAllowDNS] = "true"
	}
	if o.AllowNoKubevirtEligible {
		m[KeyAllowNoKVM] = "true"
	}
	if o.AllowUnpinnedAdopt {
		m[KeyAllowUnpin] = "true"
	}
	if o.NoS3Exposure {
		m[KeyNoS3Exposure] = "true"
	}
	return m
}

// WriteSpec persists o's operator-input surface as a reusable
// cluster-config.env-format spec file (config keys + KUBE_DC_INIT_*),
// canonically ordered for a stable diff. The git token is never written
// (ExportMap omits it). Shared by `init --save-config` and the TUI's 'S'
// save-draft so both produce an identical, re-loadable file.
func WriteSpec(o *InitOptions, path string) error {
	m := ExportMap(o)
	e := config.NewEnv()
	e.AppendComment("kube-dc bootstrap init spec")
	e.AppendComment("Config keys mirror cluster-config.env; KUBE_DC_INIT_* are install-only (stripped on scaffold).")
	e.AppendComment("Reuse: kube-dc bootstrap init --config " + filepath.Base(path))
	e.AppendBlank()
	for _, k := range SpecOrderedKeys(m) {
		e.Set(k, m[k])
	}
	return e.Write(path)
}

// SpecOrderedKeys returns the canonical write order for the keys present
// in m (identity → network → storage → orchestration), for a stable
// `--save-config` file. Keys not in the canonical order (none, by
// construction) sort last alphabetically.
func SpecOrderedKeys(m map[string]string) []string {
	pos := make(map[string]int, len(specOrder))
	for i, k := range specOrder {
		pos[k] = i
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, oi := pos[keys[i]]
		pj, oj := pos[keys[j]]
		if oi && oj {
			return pi < pj
		}
		if oi != oj {
			return oi // known keys before unknown
		}
		return keys[i] < keys[j]
	})
	return keys
}
