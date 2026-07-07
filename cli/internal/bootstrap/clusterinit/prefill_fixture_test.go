package clusterinit

import (
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// TestImportMap_FullFixture is the recurrence guard (reviewer P3): a
// sanitized full cluster-config.env fixture parsed through the REAL loader,
// asserting the deny-list model preserves every operator key and drops only
// scaffold/preset-owned keys. If someone later mis-categorizes a key, this
// fails loudly.
func TestImportMap_FullFixture(t *testing.T) {
	env, err := config.LoadEnv("testdata/sibling-full.env")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	o := &InitOptions{}
	ignored := ImportMap(o, env.AsMap(), noFlagsChanged)
	ignoredSet := map[string]bool{}
	for _, k := range ignored {
		ignoredSet[k] = true
	}

	// PRESERVE — operator topology + feature keys land in o.Sets.
	preserve := []string{
		"EXT_NET_VLAN_ID", "EXT_NET_INTERFACE", "EXT_NET_MTU", "KUBE_OVN_MASTER_NODES",
		"KUBE_OVN_GW_NODES", "KUBE_OVN_GW_TYPE",
		"EXT_PUBLIC_VLAN_ID", "EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY",
		"EXT_PUBLIC_EXCLUDE_IPS_1", "EXT_PUBLIC_EXCLUDE_IPS_2",
		"EXT_NET_ANCHOR_IPS", "EXT_NET_ANCHOR_INTERFACE", "EXT_NET_ANCHOR_REQUIRED", "EXT_NET_ANCHOR_SSH_HOSTS",
		"METALLB_INTERFACE", "METALLB_FLOATING_IP", "CEPH_REPLICATION_SIZE",
		"KUBE_API_INTERNAL_VIP", "PLATFORM_ENDPOINT_KUBE_API_ENABLED",
		"ENVOY_GATEWAY_INTERNAL_VIP", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED",
		"INGRESS_HOST_CIDR", "INGRESS_GLOBAL_ALLOWLIST", "EGRESS_GLOBAL_ALLOWLIST",
		"OPENBAO_ENABLED", "OPENBAO_REPLICAS", "OPENBAO_STORAGE_SIZE",
		"PROM_MEM_LIMIT", "SYSTEM_QUOTA_MIMIR_BLOCKS", "SYSTEM_QUOTA_LOKI_CHUNKS",
		"SMTP_ENABLED", "SMTP_HOST", "SMTP_PORT", "BILLING_PROVIDER", "SSO_ENABLED",
	}
	for _, k := range preserve {
		if _, ok := o.Sets[k]; !ok {
			t.Errorf("PRESERVE key %q missing from o.Sets (operator config dropped)", k)
		}
		if ignoredSet[k] {
			t.Errorf("PRESERVE key %q was ignored", k)
		}
	}

	// DROP — scaffold/preset-owned: derived, preset defaults, versions/images.
	drop := []string{
		"KUBE_API_EXTERNAL_URL", "KEYCLOAK_HOSTNAME", "OVN_DB_IPS",
		"EXT_NET_NAME", "EXT_NET_TYPE", "EXT_NET_CIDR", "EXT_NET_GATEWAY", "EXT_NET_EXCLUDE_IPS",
		"POD_CIDR", "SVC_CIDR", "CLUSTER_DNS", "JOIN_CIDR",
		"DEFAULT_GW_NETWORK_TYPE", "DEFAULT_EIP_NETWORK_TYPE",
		"KUBE_DC_VERSION", "KUBE_DC_MANAGER_TAG", "KUBE_OVN_VERSION",
		"CERT_MANAGER_VERSION", "CEPH_IMAGE", "KMS_PLUGIN_IMAGE",
	}
	for _, k := range drop {
		if _, leaked := o.Sets[k]; leaked {
			t.Errorf("DROP key %q leaked into o.Sets (would corrupt a clone)", k)
		}
		if !ignoredSet[k] {
			t.Errorf("DROP key %q not reported as ignored", k)
		}
	}

	// PROMOTED — dedicated InitOptions fields, not o.Sets.
	if o.Name != "dc1" || o.Domain != "kdc.example.com" || o.NodeExternalIP != "203.0.113.10" || o.Email != "ops@example.com" {
		t.Errorf("identity not promoted: %+v", o)
	}
	if o.RookMode != RookCephLocal || o.RookOSDNode != "dc1-m1" || o.RookOSDSizeGB != 500 || o.S3Hostname != "s3.kdc.example.com" {
		t.Errorf("object storage not promoted: %+v", o)
	}
}
