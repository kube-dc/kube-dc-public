package clusterinit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func TestSavedInputSpecRestoresOverridesAndMatchesPreview(t *testing.T) {
	o := validBase()
	o.Sets = map[string]string{"NODE_CIDR": "192.0.2.0/24", "POD_CIDR": "10.42.0.0/16", "INFRA_ATTACHMENT_ROUTES": "192.0.2.0/24", "EXT_NET_MTU": "1400", "KUBE_DC_BACKEND_TAG": "reviewed", "KUBE_DC_BACKEND_DIGEST": "", "CUSTOM_SETTING": ""}
	o.TLSMode, o.TLSCert, o.TLSKey, o.TrustedCABundle = "byo-wildcard", "/certs/tls.crt", "/certs/tls.key", "/certs/ca.pem"
	o.StarterRef = "oci://example.test/starter:v1"
	o.PrimaryNode = "node-1"
	o.NodeSSHHosts = map[string]string{"node-1": "root@192.0.2.10", "node-2": "admin@192.0.2.11"}
	o.NodeSSHHostKeys = map[string]string{"node-2": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	o.GitHubToken = "never-write-this-token"
	path := filepath.Join(t.TempDir(), "install.env")
	preview, err := RenderSpec(&o)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSpec(&o, path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != preview || strings.Contains(preview, o.GitHubToken) {
		t.Fatal("preview/save mismatch or token disclosed")
	}
	for _, line := range strings.Split(preview, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
			t.Fatalf("invalid environment line %q", line)
		}
	}
	env, err := config.LoadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	var restored InitOptions
	if ignored := ImportMap(&restored, env.AsMap(), noFlagsChanged); len(ignored) != 0 {
		t.Fatal(ignored)
	}
	if !reflect.DeepEqual(o.Sets, restored.Sets) {
		t.Fatalf("overrides changed: %#v", restored.Sets)
	}
	if restored.TLSKey != o.TLSKey || restored.TrustedCABundle != o.TrustedCABundle || restored.StarterRef != o.StarterRef {
		t.Fatal("local input references lost")
	}
	if restored.PrimaryNode != o.PrimaryNode || !reflect.DeepEqual(restored.NodeSSHHosts, o.NodeSSHHosts) || !reflect.DeepEqual(restored.NodeSSHHostKeys, o.NodeSSHHostKeys) {
		t.Fatal("node SSH identity mapping lost")
	}
	// The same bare live cluster config is still a clone, not a restore.
	cloneMap := env.AsMap()
	delete(cloneMap, KeySpecVersion)
	var cloned InitOptions
	ImportMap(&cloned, cloneMap, noFlagsChanged)
	for _, k := range []string{"NODE_CIDR", "POD_CIDR", "INFRA_ATTACHMENT_ROUTES", "KUBE_DC_BACKEND_TAG", "KUBE_DC_BACKEND_DIGEST"} {
		if _, ok := cloned.Sets[k]; ok {
			t.Fatalf("clone inherited site or pin key %s", k)
		}
	}
}

func TestInputSpecRejectsInvalidAndSecretValuesWithoutDisclosure(t *testing.T) {
	for _, values := range []map[string]string{
		{KeySpecVersion: "future"}, {"SMTP_PASSWORD": "private-value"}, {"CUSTOM": "private-value\nINJECTED=true"},
		{InitPrefix + "YES": "true"}, {"CEPH_OSD_COUNT": "private-value"}, {KeyAllowDNS: "private-value"},
		{KeyNodeSSHHosts: "bad node=host"}, {KeyNodeSSHHostKeys: "node-1=wrong"},
	} {
		err := ValidateInputSpec(values)
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe validation result: %v", err)
		}
	}
	if err := ValidateInputSpec(map[string]string{InitPrefix + "TLS_KEY": "/certs/key.pem", "SSO_BROKER_SECRET_REF": "sso-secret"}); err != nil {
		t.Fatal(err)
	}
}

func TestKnownFleetSettingsValidateThroughFileAndFlags(t *testing.T) {
	for key, value := range map[string]string{
		"EXT_NET_MTU": "ten-gigabit", "EXT_NET_VLAN_ID": "4095",
		"POD_CIDR": "10.1.2.3", "NODE_EXTERNAL_IP": "not-an-address",
		"SMTP_PORT": "70000", "METALLB_MODE": "arp",
	} {
		if err := ValidateInputSpec(map[string]string{key: value}); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("input file accepted invalid %s: %v", key, err)
		}
		o := validBase()
		o.Sets = map[string]string{key: value}
		if key != "EXT_NET_VLAN_ID" {
			if err := o.ValidateFields(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("flag path accepted invalid %s: %v", key, err)
			}
		}
	}
	for key, value := range map[string]string{
		"EXT_NET_MTU": "1400", "EXT_NET_VLAN_ID": "0", "POD_CIDR": "10.1.0.0/16",
		"NODE_EXTERNAL_IP": "192.0.2.1", "SMTP_PORT": "587", "METALLB_MODE": "bgp",
	} {
		if err := ValidateInputSpec(map[string]string{key: value}); err != nil {
			t.Fatalf("valid %s: %v", key, err)
		}
	}
}

func TestImagePinInheritanceDoesNotMixSiblings(t *testing.T) {
	old := SiblingCluster{Name: "old", ModTime: time.Unix(1, 0), Env: map[string]string{"APP_TAG": "old", "APP_DIGEST": "sha256:old"}}
	newer := SiblingCluster{Name: "new", ModTime: time.Unix(2, 0), Env: map[string]string{"APP_TAG": "new"}}
	result := InheritFromSiblings([]SiblingCluster{old, newer})
	if result.Defaults["APP_TAG"] != "new" {
		t.Fatal(result)
	}
	if _, ok := result.Defaults["APP_DIGEST"]; ok {
		t.Fatal("old digest overrode the selected sibling tag")
	}
	newer.Env["APP_DIGEST"] = "sha256:new"
	result = InheritFromSiblings([]SiblingCluster{old, newer})
	if result.Defaults["APP_DIGEST"] != "sha256:new" {
		t.Fatal("active digest not inherited")
	}
}

func TestScaffoldDoesNotKeepDigestBehindTagOverride(t *testing.T) {
	for _, explicitDigest := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "cluster-config.env")
		if err := os.WriteFile(path, []byte("KUBE_DC_BACKEND_TAG=starter\nKUBE_DC_BACKEND_DIGEST=sha256:starter\n"), 0600); err != nil {
			t.Fatal(err)
		}
		plan := &Plan{Preset: PresetInternalOnly, ClusterName: "test", IngressAddressLayer: AddressLayerNone, InheritedDefaults: map[string]string{"KUBE_DC_BACKEND_TAG": "sibling", "KUBE_DC_BACKEND_DIGEST": "sha256:sibling"}}
		sets := map[string]string{"KUBE_DC_BACKEND_TAG": "selected", "EXT_NET_INTERFACE": "eth0", "EXT_NET_VLAN_ID": "0"}
		if explicitDigest {
			sets["KUBE_DC_BACKEND_DIGEST"] = "sha256:selected"
		}
		if err := postProcessClusterConfig(path, plan, sets, ""); err != nil {
			t.Fatal(err)
		}
		env, _ := config.LoadEnv(path)
		want := ""
		if explicitDigest {
			want = "sha256:selected"
		}
		if env.GetOr("KUBE_DC_BACKEND_DIGEST", "missing") != want {
			t.Fatal("generated environment selected the wrong active image")
		}
	}
}
