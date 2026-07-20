package clusterinit

import (
	"reflect"
	"testing"
)

func noFlagsChanged(string) bool { return false }

// A representative source: native config keys + overlay + KUBE_DC_INIT_*
// orchestration + some keys the importer must IGNORE (versions/derived).
func sampleSrc() map[string]string {
	return map[string]string{
		"CLUSTER_NAME":           "dc1",
		"DOMAIN":                 "kdc.example.com",
		"NODE_EXTERNAL_IP":       "203.0.113.10",
		"EMAIL":                  "ops@example.com",
		"EXT_NET_INTERFACE":      "enp1s0",
		"EXT_NET_VLAN_ID":        "0",
		"KUBE_OVN_MASTER_NODES":  "10.0.0.5",
		"OBJECT_STORAGE_MODE":    "rook-ceph-local",
		"CEPH_LOCAL_OSD_NODE":    "dc1-master-1",
		"CEPH_LOCAL_OSD_SIZE_GB": "40",
		"S3_HOSTNAME":            "s3.kdc.example.com",
		KeyMode:                  "install",
		KeyFleetMode:             "new-repo",
		KeyPreset:                "internal-only",
		KeyGitHubOwner:           "acme",
		KeyGitHubRepo:            "acme-fleet",
		KeySSHHost:               "ubuntu@203.0.113.10",
		KeyAllowNoKVM:            "true",
		// must be IGNORED (not part of the init input surface):
		"KUBE_DC_VERSION":       "v0.4.0",
		"KUBE_API_EXTERNAL_URL": "https://kube-api.kdc.example.com:6443",
		"KUBEVIRT_VERSION":      "v1.8.1",
	}
}

func TestImportMap_MapsInputSurfaceAndIgnoresDerived(t *testing.T) {
	o := &InitOptions{}
	ignored := ImportMap(o, sampleSrc(), noFlagsChanged)

	if o.Name != "dc1" || o.Domain != "kdc.example.com" || o.NodeExternalIP != "203.0.113.10" || o.Email != "ops@example.com" {
		t.Errorf("identity not mapped: %+v", o)
	}
	if o.Sets["EXT_NET_INTERFACE"] != "enp1s0" || o.Sets["KUBE_OVN_MASTER_NODES"] != "10.0.0.5" || o.Sets["EXT_NET_VLAN_ID"] != "0" {
		t.Errorf("overlay not mapped: %v", o.Sets)
	}
	if o.RookMode != RookCephLocal || o.RookOSDNode != "dc1-master-1" || o.RookOSDSizeGB != 40 {
		t.Errorf("object storage not mapped: mode=%q node=%q size=%d", o.RookMode, o.RookOSDNode, o.RookOSDSizeGB)
	}
	if o.Mode != ModeInstall || o.FleetMode != FleetNewRepo || o.Preset != PresetInternalOnly {
		t.Errorf("orchestration enums not mapped: mode=%q fleet=%q preset=%q", o.Mode, o.FleetMode, o.Preset)
	}
	if o.GitHubOwner != "acme" || o.GitHubRepo != "acme-fleet" || o.SSHHost != "ubuntu@203.0.113.10" {
		t.Errorf("fleet/ssh not mapped: %+v", o)
	}
	if !o.AllowNoKubevirtEligible {
		t.Error("KUBE_DC_INIT_ALLOW_NO_KVM should map to AllowNoKubevirtEligible")
	}
	// The version/derived keys are ignored, not dumped into o.Sets.
	for _, k := range []string{"KUBE_DC_VERSION", "KUBE_API_EXTERNAL_URL", "KUBEVIRT_VERSION"} {
		if _, leaked := o.Sets[k]; leaked {
			t.Errorf("derived/version key %q must NOT leak into o.Sets", k)
		}
	}
	// byte-wise sort: '_' (0x5F) > 'V' (0x56), so KUBEVIRT_ sorts before KUBE_.
	want := []string{"KUBEVIRT_VERSION", "KUBE_API_EXTERNAL_URL", "KUBE_DC_VERSION"}
	if !reflect.DeepEqual(ignored, want) {
		t.Errorf("ignored keys = %v, want %v", ignored, want)
	}
}

// Precedence: an explicitly-set flag wins over the prefill source.
func TestImportMap_FlagWins(t *testing.T) {
	o := &InitOptions{Name: "from-flag", Sets: map[string]string{"EXT_NET_INTERFACE": "bond0"}}
	changed := func(f string) bool { return f == "name" }
	ImportMap(o, sampleSrc(), changed)
	if o.Name != "from-flag" {
		t.Errorf("--name flag must win over prefill CLUSTER_NAME, got %q", o.Name)
	}
	// overlay key already set via --set (present in o.Sets) is not overwritten.
	if o.Sets["EXT_NET_INTERFACE"] != "bond0" {
		t.Errorf("explicit --set EXT_NET_INTERFACE must win, got %q", o.Sets["EXT_NET_INTERFACE"])
	}
	// a field NOT flagged still imports.
	if o.Domain != "kdc.example.com" {
		t.Errorf("non-flagged field should import, got Domain=%q", o.Domain)
	}
}

// The core invariant for --save-config → --config: Export then Import
// reproduces the same InitOptions input surface.
func TestExportImport_RoundTrip(t *testing.T) {
	orig := &InitOptions{
		Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10",
		Email: "ops@example.com", Mode: ModeInstall, FleetMode: FleetNewRepo,
		Preset: PresetInternalOnly, GitHubOwner: "acme", GitHubRepo: "acme-fleet",
		SSHHost: "ubuntu@203.0.113.10", RookMode: RookCephLocal,
		RookOSDNode: "dc1-master-1", RookOSDSizeGB: 40, S3Hostname: "s3.kdc.example.com",
		AllowNoKubevirtEligible: true,
		Sets:                    map[string]string{"EXT_NET_INTERFACE": "enp1s0", "EXT_NET_VLAN_ID": "0", "KUBE_OVN_MASTER_NODES": "10.0.0.5"},
	}
	m := ExportMap(orig)
	got := &InitOptions{}
	if ignored := ImportMap(got, m, noFlagsChanged); len(ignored) != 0 {
		t.Errorf("round-trip should ignore nothing, got %v", ignored)
	}
	// compare the mapped surface
	if got.Name != orig.Name || got.Domain != orig.Domain || got.NodeExternalIP != orig.NodeExternalIP ||
		got.Email != orig.Email || got.Mode != orig.Mode || got.FleetMode != orig.FleetMode ||
		got.Preset != orig.Preset || got.GitHubOwner != orig.GitHubOwner || got.GitHubRepo != orig.GitHubRepo ||
		got.SSHHost != orig.SSHHost || got.RookMode != orig.RookMode || got.RookOSDNode != orig.RookOSDNode ||
		got.RookOSDSizeGB != orig.RookOSDSizeGB || got.S3Hostname != orig.S3Hostname ||
		got.AllowNoKubevirtEligible != orig.AllowNoKubevirtEligible {
		t.Errorf("round-trip scalar mismatch:\n orig=%+v\n got =%+v", orig, got)
	}
	if !reflect.DeepEqual(got.Sets, orig.Sets) {
		t.Errorf("round-trip overlay mismatch: got %v want %v", got.Sets, orig.Sets)
	}
}

// P1 regression: multi-node Ceph must use the SCAFFOLD'S shape —
// CEPH_NODE_N (host) + CEPH_NODE_N_DEVICE (device), NOT a combined
// "node=device" — so cloning a sibling cluster-config.env keeps devices.
func TestImportExport_MultiNodeCephSplitKeys(t *testing.T) {
	// Import the scaffold's split-key shape.
	src := map[string]string{
		"OBJECT_STORAGE_MODE": "rook-ceph-multi-node",
		"CEPH_NODE_1":         "host5-a", "CEPH_NODE_1_DEVICE": "sdb",
		"CEPH_NODE_2": "host6-a", "CEPH_NODE_2_DEVICE": "sdc",
		"CEPH_NODE_3": "host7-a", "CEPH_NODE_3_DEVICE": "sdb",
	}
	o := &InitOptions{}
	if ignored := ImportMap(o, src, noFlagsChanged); len(ignored) != 0 {
		t.Fatalf("split ceph keys should all be recognized, ignored=%v", ignored)
	}
	want := map[string]string{"host5-a": "sdb", "host6-a": "sdc", "host7-a": "sdb"}
	if !reflect.DeepEqual(o.CephNodes, want) {
		t.Fatalf("multi-node devices lost on import: got %v want %v", o.CephNodes, want)
	}
	// Export round-trips back to the split-key shape (matches the scaffold).
	m := ExportMap(o)
	for k, v := range src {
		if m[k] != v {
			t.Errorf("export missing/incorrect %s: got %q want %q", k, m[k], v)
		}
	}
	// And re-import equals the original mapping (full round-trip).
	o2 := &InitOptions{}
	ImportMap(o2, m, noFlagsChanged)
	if !reflect.DeepEqual(o2.CephNodes, want) {
		t.Errorf("multi-node round-trip mismatch: %v", o2.CephNodes)
	}
}

// P2 regression: rook-ceph-pvc OSD count + volume size must survive
// --config / --save-config (previously only the storage class did).
func TestImportExport_CephPVCSizing(t *testing.T) {
	src := map[string]string{
		"OBJECT_STORAGE_MODE":     "rook-ceph-pvc",
		"CEPH_OSD_STORAGE_CLASS":  "fast-nvme",
		"CEPH_OSD_COUNT":          "4",
		"CEPH_OSD_VOLUME_SIZE_GB": "300",
	}
	o := &InitOptions{}
	if ignored := ImportMap(o, src, noFlagsChanged); len(ignored) != 0 {
		t.Fatalf("pvc keys should all be recognized, ignored=%v", ignored)
	}
	if o.CephStorageClass != "fast-nvme" || o.CephOSDCount != 4 || o.CephOSDVolumeSizeGB != 300 {
		t.Fatalf("pvc sizing lost: class=%q count=%d size=%d", o.CephStorageClass, o.CephOSDCount, o.CephOSDVolumeSizeGB)
	}
	m := ExportMap(o)
	if m["CEPH_OSD_COUNT"] != "4" || m["CEPH_OSD_VOLUME_SIZE_GB"] != "300" || m["CEPH_OSD_STORAGE_CLASS"] != "fast-nvme" {
		t.Errorf("pvc sizing not exported: %v", m)
	}
}

// P3 regression: the deprecated --rook-mode alias must also win over a
// prefilled OBJECT_STORAGE_MODE (docs promise flags win).
func TestImportMap_RookModeAliasWins(t *testing.T) {
	o := &InitOptions{RookMode: RookCephLocal} // set as if by --rook-mode
	changed := func(f string) bool { return f == "rook-mode" }
	ImportMap(o, map[string]string{"OBJECT_STORAGE_MODE": "disabled"}, changed)
	if o.RookMode != RookCephLocal {
		t.Errorf("--rook-mode alias must win over config OBJECT_STORAGE_MODE, got %q", o.RookMode)
	}
}

// Deny-list model: a realistic sibling cluster-config.env slice — every
// operator topology/feature key is CARRIED into o.Sets (lossless clone),
// while scaffold/preset-OWNED keys (versions, derived endpoints, universal
// + preset network defaults) are dropped. This is the "host all variants"
// guarantee.
func TestImportMap_DenyListClonesOperatorDropsOwned(t *testing.T) {
	operator := map[string]string{
		"KUBE_OVN_GW_NODES": "m1,w1,w2", "KUBE_OVN_GW_TYPE": "centralized",
		"METALLB_INTERFACE": "bond0", "METALLB_FLOATING_IP": "203.0.113.9",
		"EXT_NET_ANCHOR_IPS": "m1=203.0.113.0/24", "EXT_NET_MTU": "1400",
		"EXT_PUBLIC_EXCLUDE_IPS_1": "203.0.113.1..203.0.113.5",
		"CEPH_REPLICATION_SIZE":    "2", "SMTP_HOST": "smtp.example.com",
		"BILLING_PROVIDER": "whmcs", "SYSTEM_QUOTA_MIMIR_BLOCKS": "50Gi",
		"PLATFORM_ENDPOINT_KUBE_API_ENABLED": "true",
	}
	owned := map[string]string{
		"KUBE_DC_VERSION": "v0.4.0", "KUBE_OVN_VERSION": "v1.15.10",
		"KUBE_DC_MANAGER_TAG": "v0.4.0-1", "CEPH_IMAGE": "quay.io/ceph:v19",
		"KUBE_API_EXTERNAL_URL": "https://kube-api.sibling.example.com:6443",
		"KEYCLOAK_HOSTNAME":     "login.sibling.example.com", "OVN_DB_IPS": "tcp:10.0.0.5:6641",
		"POD_CIDR": "10.100.0.0/16", "EXT_NET_CIDR": "100.65.0.0/16",
		"DEFAULT_GW_NETWORK_TYPE": "cloud",
	}
	src := map[string]string{}
	for k, v := range operator {
		src[k] = v
	}
	for k, v := range owned {
		src[k] = v
	}

	o := &InitOptions{}
	ignored := ImportMap(o, src, noFlagsChanged)

	for k, v := range operator {
		if o.Sets[k] != v {
			t.Errorf("operator key %q DROPPED (got %q want %q)", k, o.Sets[k], v)
		}
	}
	for k := range owned {
		if _, leaked := o.Sets[k]; leaked {
			t.Errorf("scaffold-owned key %q must NOT be carried into a clone", k)
		}
	}
	if len(ignored) != len(owned) {
		t.Errorf("ignored=%d want %d (all scaffold-owned): %v", len(ignored), len(owned), ignored)
	}
	// Export then re-import is lossless for the operator overlay.
	o2 := &InitOptions{}
	ImportMap(o2, ExportMap(o), noFlagsChanged)
	for k, v := range operator {
		if o2.Sets[k] != v {
			t.Errorf("round-trip lost operator key %q: got %q want %q", k, o2.Sets[k], v)
		}
	}
}

// A real sibling cluster-config.env is a valid prefill: identity + network
// + storage import; the ~70 version/derived/feature keys are ignored.
func TestImportMap_CloneFromSiblingIgnoresBulk(t *testing.T) {
	o := &InitOptions{}
	ignored := ImportMap(o, sampleSrc(), noFlagsChanged)
	if len(ignored) == 0 {
		t.Fatal("expected the derived/version keys to be reported as ignored")
	}
	if o.Name == "" || len(o.Sets) == 0 {
		t.Error("clone-from-sibling should still import the input surface")
	}
}

func TestImportMap_PreservesIngressAndBGPModeKeys(t *testing.T) {
	// Regression for the BGP slice: a sibling cluster running
	// METALLB_MODE=bgp (routed/L3 fabric) or INGRESS_MODE=hostnetwork
	// must keep those modes when cloned — silently dropping them would
	// downgrade the clone to l2/ARP and break a no-shared-L2 datacenter.
	src := map[string]string{
		"INGRESS_MODE":             "hostnetwork",
		"METALLB_MODE":             "bgp",
		"METALLB_BGP_LOCAL_ASN":    "64512",
		"METALLB_BGP_PEER_ASN":     "64513",
		"METALLB_BGP_PEER_ADDRESS": "192.0.2.1",
		"METALLB_BGP_PEER_PORT":    "179",
	}
	o := &InitOptions{}
	ignored := ImportMap(o, src, func(string) bool { return false })
	if len(ignored) != 0 {
		t.Fatalf("expected no ignored keys, got %v", ignored)
	}
	for k, want := range src {
		if got := o.Sets[k]; got != want {
			t.Errorf("Sets[%s] = %q, want %q (mode key dropped on clone)", k, got, want)
		}
	}
}
