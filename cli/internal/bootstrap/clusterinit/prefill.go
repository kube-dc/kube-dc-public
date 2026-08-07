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
	// KeyNodeEgress is install-only by design: live cluster-config.env uses
	// EXT_NET_NODE_EGRESS_ENABLED, but clone-from-sibling must never inherit this
	// site-specific internet-gateway escape hatch.
	KeyNodeEgress = InitPrefix + "NODE_EGRESS_ENABLED"
	KeyNodeNICs   = InitPrefix + "NODE_NICS"
	// KeyIngressNodes is install-only on purpose: it is a list of node
	// NAMES, so a clone-from-sibling that inherited them would label
	// nodes that do not exist in the new cluster.
	KeyIngressNodes = InitPrefix + "INGRESS_NODES"
	// VM root-disk storage (install-only: selects which rbd-vm fleet
	// manifests get scaffolded — never reconciled into cluster-config.env).
	// Goldens are comma-joined lists.
	KeyVMStorageMode      = InitPrefix + "VM_STORAGE_MODE"
	KeyVMGolden           = InitPrefix + "VM_GOLDEN"
	KeyVMGoldenBlock      = InitPrefix + "VM_GOLDEN_BLOCK"
	KeyGPUPlatform        = InitPrefix + "GPU_PLATFORM"
	KeyGPUAllowUnassigned = InitPrefix + "GPU_ALLOW_UNASSIGNED"
	KeyVGPUSecretReady    = InitPrefix + "VGPU_SECRET_READY"
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
	// Tenant Networking v2. NODE_CIDR is the sibling's node LAN and
	// INFRA_ATTACHMENT_ROUTES is built from it, so importing either puts the
	// WRONG node subnet into the new cluster's injected routes.
	//
	// This one does not fail loudly. A sibling's CIDR is still a well-formed
	// CIDR, so it passes every validation and the manager starts happily; what
	// breaks is asymmetric routing — a dual-homed pod answers kubelet probes over
	// the wrong NIC and never reaches Ready, on a cluster where nothing is red.
	// Strictly worse than a crash. The rest of the INFRA_ATTACHMENT_* keys are
	// universal and safe to carry.
	"NODE_CIDR": true, "INFRA_ATTACHMENT_ROUTES": true,
	// Secret material has dedicated channels (file flag or the
	// KUBE_DC_DNS01_ROUTE53_SECRET_KEY env var) — a config file carrying it
	// must never prefill Sets, where it would be re-persisted in cleartext.
	"DNS01_ROUTE53_SECRET_KEY": true, "DNS01_ROUTE53_SECRET_ACCESS_KEY": true,
	// Public-anchor map is derived from THIS cluster's VIP + gateway-node
	// names (derivePublicAnchorEnv); a sibling's map carries the sibling's
	// node names and VIP block, so a clone must re-derive, not inherit.
	"EXT_NET_PUBLIC_ANCHOR_IPS": true, "EXT_NET_PUBLIC_ANCHOR_VLAN": true,
	"EXT_NET_NAME": true, "EXT_NET_TYPE": true, "EXT_NET_CIDR": true,
	"EXT_NET_NODE_EGRESS_ENABLED": true,
	"EXT_NET_GATEWAY":             true, "EXT_NET_EXCLUDE_IPS": true,
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
	// Tenant Networking v2. NODE_CIDR and INFRA_ATTACHMENT_ROUTES are
	// deny-imported (they are node-specific), but they still belong in the
	// ordering so --save-config output is complete and stable for the cluster
	// it was taken from.
	"MANAGEMENT_API_MODE", "NODE_CIDR", "INFRA_ATTACHMENT_ENABLED", "INFRA_ATTACHMENT_SUBNET",
	"INFRA_ATTACHMENT_CIDR", "INFRA_ATTACHMENT_GATEWAY",
	"INFRA_ATTACHMENT_SECURITY_GROUP", "INFRA_ATTACHMENT_ROUTES",

	"CLUSTER_NAME", "DOMAIN", "NODE_EXTERNAL_IP", "EMAIL",
	"EXT_NET_VLAN_ID", "EXT_NET_INTERFACE", "EXT_NET_MTU", "KUBE_OVN_MASTER_NODES",
	"KUBE_OVN_GW_NODES", "EXT_NET_ANCHOR_IPS", "EXT_NET_ANCHOR_INTERFACE",
	"EXT_NET_ANCHOR_REQUIRED", "EXT_NET_ANCHOR_SSH_HOSTS", KeyNodeEgress,
	KeyNodeNICs, KeyIngressNodes,
	"EXT_PUBLIC_VLAN_ID", "EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY",
	"EXT_NET_PUBLIC_ANCHOR_INTERFACE",
	"OBJECT_STORAGE_MODE",
	"CEPH_LOCAL_OSD_NODE", "CEPH_LOCAL_OSD_SIZE_GB", "CEPH_LOCAL_OSD_DEVICE",
	"CEPH_NODE_1", "CEPH_NODE_1_DEVICE", "CEPH_NODE_2", "CEPH_NODE_2_DEVICE",
	"CEPH_NODE_3", "CEPH_NODE_3_DEVICE",
	"CEPH_OSD_STORAGE_CLASS", "CEPH_OSD_COUNT", "CEPH_OSD_VOLUME_SIZE_GB",
	"S3_HOSTNAME",
	"INGRESS_ADDRESS_LAYER", "INGRESS_NODE_LABEL",
	"TLS_MODE", "DNS01_ROUTE53_ZONE_ID", "DNS01_ROUTE53_REGION", "DNS01_ROUTE53_ACCESS_KEY_ID",
	KeyVMStorageMode, KeyVMGolden, KeyVMGoldenBlock,
	KeyGPUPlatform, "GPU_DRIVER_SOURCE", "GPU_OPERATOR_VERSION",
	"NVIDIA_DRIVER_VERSION", "NVIDIA_TOOLKIT_VERSION", "HAMI_ENABLED", "GPU_SHARED_ALLOCATOR",
	"HAMI_VERSION", "HAMI_KUBE_SCHEDULER_VERSION", "GPU_NODE_MODES", "GPU_PROFILES",
	KeyGPUAllowUnassigned, KeyVGPUSecretReady,
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
	// TLS_MODE + the dns01 solver config are DEDICATED fields, not generic
	// Sets: left generic, a cloned/sibling env claiming acme-dns01-route53
	// would ride through without the mode's validation or scaffolding ever
	// firing (codex review 2026-08-06, P2). Promoted, a re-run against a
	// dns01 cluster restores the mode and Validate demands coherent flags
	// (and the secret via its dedicated channel) — loud, not silent.
	// Front door: the address layer is a DEDICATED field, not a generic
	// Set. Left generic, a --save-config of a flag-chosen layer would be
	// dropped (the flag never reaches o.Sets) and the next run would
	// silently fall back to "none" — turning an intended VIP cluster into
	// a ClusterIP one.
	str("INGRESS_ADDRESS_LAYER", "ingress-address-layer", &o.IngressAddressLayer)
	str("TLS_MODE", "tls-mode", &o.TLSMode)
	str("DNS01_ROUTE53_ZONE_ID", "dns01-route53-zone-id", &o.DNS01Route53ZoneID)
	str("DNS01_ROUTE53_REGION", "dns01-route53-region", &o.DNS01Route53Region)
	str("DNS01_ROUTE53_ACCESS_KEY_ID", "dns01-route53-access-key-id", &o.DNS01Route53AccessKeyID)
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
	if v, ok := src[KeyIngressNodes]; ok {
		seen[KeyIngressNodes] = true
		if !flagChanged("ingress-node") {
			o.IngressNodes = splitCSVList(v)
		}
	}
	if v, ok := src[KeyNodeNICs]; ok {
		seen[KeyNodeNICs] = true
		if !flagChanged("node-nic") {
			if parsed, err := ParseSetPairs(splitCSVList(v)); err == nil {
				o.NodeNICs = parsed
			}
		}
	}
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
	if v, ok := src[KeyNodeEgress]; ok {
		seen[KeyNodeEgress] = true
		if _, explicitlySet := o.Sets["EXT_NET_NODE_EGRESS_ENABLED"]; !explicitlySet {
			o.Sets["EXT_NET_NODE_EGRESS_ENABLED"] = strings.TrimSpace(v)
		}
	}
	if v, ok := src[KeyGPUPlatform]; ok {
		seen[KeyGPUPlatform] = true
		if _, ok := src["GPU_ENABLED"]; ok {
			seen["GPU_ENABLED"] = true
		}
		if _, ok := src["GPU_CATALOG_ENABLED"]; ok {
			seen["GPU_CATALOG_ENABLED"] = true
		}
		if !flagChanged("gpu-platform") && strings.TrimSpace(v) != "" {
			o.GPUPlatform = GPUPlatformMode(strings.TrimSpace(v))
		}
	} else if v, ok := src["GPU_ENABLED"]; ok {
		seen["GPU_ENABLED"] = true
		if !flagChanged("gpu-platform") {
			if parsePrefillBool(v) {
				o.GPUPlatform = GPUPlatformEnabled
			} else {
				o.GPUPlatform = GPUPlatformDisabled
			}
		}
	} else if v, ok := src["GPU_CATALOG_ENABLED"]; ok {
		seen["GPU_CATALOG_ENABLED"] = true
		if !flagChanged("gpu-platform") {
			if parsePrefillBool(v) {
				o.GPUPlatform = GPUPlatformEnabled
			} else {
				o.GPUPlatform = GPUPlatformDisabled
			}
		}
	}
	if v, ok := src["GPU_DRIVER_SOURCE"]; ok {
		seen["GPU_DRIVER_SOURCE"] = true
		if !flagChanged("gpu-driver-source") {
			o.GPUDriverSource = GPUDriverSource(strings.TrimSpace(v))
		}
	}
	str("GPU_OPERATOR_VERSION", "gpu-operator-version", &o.GPUOperatorVersion)
	str("NVIDIA_DRIVER_VERSION", "nvidia-driver-version", &o.NVIDIADriverVersion)
	str("NVIDIA_TOOLKIT_VERSION", "nvidia-toolkit-version", &o.NVIDIAToolkitVersion)
	boolean("HAMI_ENABLED", "hami-enabled", &o.HAMiEnabled)
	if v, ok := src["GPU_SHARED_ALLOCATOR"]; ok {
		seen["GPU_SHARED_ALLOCATOR"] = true
		if !flagChanged("gpu-shared-allocator") {
			o.GPUSharedAllocator = GPUSharedAllocator(strings.TrimSpace(v))
		}
	}
	str("HAMI_VERSION", "hami-version", &o.HAMiVersion)
	str("HAMI_KUBE_SCHEDULER_VERSION", "hami-scheduler-version", &o.HAMiSchedulerVersion)
	if v, ok := src["GPU_NODE_MODES"]; ok {
		seen["GPU_NODE_MODES"] = true
		if !flagChanged("gpu-node-mode") {
			if modes, err := ParseGPUNodeModes([]string{v}); err == nil {
				o.GPUNodeModes = modes
			}
		}
	}
	if v, ok := src["GPU_PROFILES"]; ok {
		seen["GPU_PROFILES"] = true
		if !flagChanged("gpu-profile") {
			o.GPUProfiles = canonicalGPUProfiles([]string{v})
		}
	}
	boolean(KeyGPUAllowUnassigned, "allow-unassigned-gpus", &o.AllowUnassignedGPUs)
	boolean(KeyVGPUSecretReady, "vgpu-secret-ready", &o.VGPUSecretReady)

	// VM root-disk storage (install-only). Goldens are comma-joined lists.
	if v, ok := src[KeyVMStorageMode]; ok {
		seen[KeyVMStorageMode] = true
		if !flagChanged("vm-storage-mode") && strings.TrimSpace(v) != "" {
			o.VMStorageMode = VMStorageMode(strings.TrimSpace(v))
		}
	}
	if v, ok := src[KeyVMGolden]; ok {
		seen[KeyVMGolden] = true
		if !flagChanged("vm-golden") {
			o.VMGoldens = splitCSVList(v)
		}
	}
	if v, ok := src[KeyVMGoldenBlock]; ok {
		seen[KeyVMGoldenBlock] = true
		if !flagChanged("vm-golden-block") {
			o.VMGoldensBlock = splitCSVList(v)
		}
	}

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
func promotedTLS(k string) bool {
	for _, p := range promotedTLSKeys {
		if k == p {
			return true
		}
	}
	return false
}

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
	// TLS mode + dns01 solver config (promoted fields; the secret key has
	// no field and no export — its channels are the file flag and the
	// KUBE_DC_DNS01_ROUTE53_SECRET_KEY env var). "acme" is the flag default,
	// canonicalized away so a default spec stays minimal.
	put("INGRESS_ADDRESS_LAYER", o.IngressAddressLayer)
	put("TLS_MODE", canonicalTLSMode(o.TLSMode))
	put("DNS01_ROUTE53_ZONE_ID", o.DNS01Route53ZoneID)
	put("DNS01_ROUTE53_REGION", o.DNS01Route53Region)
	put("DNS01_ROUTE53_ACCESS_KEY_ID", o.DNS01Route53AccessKeyID)
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
		if k == "EXT_NET_NODE_EGRESS_ENABLED" {
			put(KeyNodeEgress, v)
			continue
		}
		if promotedTLS(k) {
			// Owned by dedicated fields (exported above); Validate rejects
			// these in --set, this guard keeps a programmatic caller from
			// clobbering the dedicated export (codex pass-2, P2).
			continue
		}
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
	if len(o.NodeNICs) > 0 {
		nodes := make([]string, 0, len(o.NodeNICs))
		for node := range o.NodeNICs {
			nodes = append(nodes, node)
		}
		sort.Strings(nodes)
		pairs := make([]string, 0, len(nodes))
		for _, node := range nodes {
			pairs = append(pairs, node+"="+o.NodeNICs[node])
		}
		put(KeyNodeNICs, strings.Join(pairs, ","))
	}
	if len(o.IngressNodes) > 0 {
		put(KeyIngressNodes, strings.Join(canonicalIngressNodes(o.IngressNodes), ","))
	}
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
	if o.GPUPlatform != "" {
		put(KeyGPUPlatform, string(o.GPUPlatform))
		for k, v := range GPUConfigEnv(o.GPU()) {
			put(k, v)
		}
	}
	if o.AllowUnassignedGPUs {
		m[KeyGPUAllowUnassigned] = "true"
	}
	if o.VGPUSecretReady {
		m[KeyVGPUSecretReady] = "true"
	}
	// VM root-disk storage — only when non-default (local == the default;
	// omitting the keys reproduces it). Goldens canonicalized (deduped +
	// sorted) so the saved spec is order-stable.
	if o.VMStorageMode != "" && o.VMStorageMode != VMStorageLocal {
		put(KeyVMStorageMode, string(o.VMStorageMode))
		if g := canonicalGoldens(o.VMGoldens); len(g) > 0 {
			put(KeyVMGolden, strings.Join(g, ","))
		}
		if g := canonicalGoldens(o.VMGoldensBlock); len(g) > 0 {
			put(KeyVMGoldenBlock, strings.Join(g, ","))
		}
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
