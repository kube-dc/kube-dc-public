package initform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func TestConfigurationEditSaveReloadPreservesAllInputKinds(t *testing.T) {
	s := validE2EState()
	for key, value := range map[string]string{
		"NODE_CIDR": "192.0.2.0/24", "SMTP_HOST": "smtp.example.test", "EXT_NET_MTU": "1400",
		"KUBE_DC_BACKEND_DIGEST": "", "METALLB_INTERFACE": "", "TLS_MODE": "byo-wildcard",
		clusterinit.InitPrefix + "TLS_CERT": "/certs/cert.pem", clusterinit.InitPrefix + "TLS_KEY": "/certs/key.pem",
		clusterinit.InitPrefix + "TRUSTED_CA_BUNDLE": "/certs/ca.pem", clusterinit.InitPrefix + "STARTER_REF": "oci://example.test/starter:v1",
	} {
		next, err := s.editedConfig(key, value, false)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		*s = next
	}
	m := NewPanelModel(s, "")
	m.draftPath = filepath.Join(t.TempDir(), "install.env")
	m.saveDraft()
	env, err := config.LoadEnv(m.draftPath)
	if err != nil {
		t.Fatalf("save: %s: %v", m.notice, err)
	}
	for _, key := range []string{"METALLB_INTERFACE", "KUBE_DC_BACKEND_DIGEST"} {
		if v, ok := env.Get(key); !ok || v != "" {
			t.Fatalf("explicit empty override %s lost", key)
		}
	}
	var restored clusterinit.InitOptions
	clusterinit.ImportMap(&restored, env.AsMap(), func(string) bool { return false })
	if restored.TLSKey != "/certs/key.pem" || restored.Sets["NODE_CIDR"] != "192.0.2.0/24" || restored.StarterRef == "" {
		t.Fatal("configuration lost on reload")
	}
	preview, err := clusterinit.RenderSpec(&restored)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.draftPath)
	if string(data) != preview {
		t.Fatal("saved configuration changed on reload")
	}
	next, err := s.editedConfig("METALLB_INTERFACE", "", true)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := next.configMap()
	if _, ok := values["METALLB_INTERFACE"]; ok {
		t.Fatal("remove override retained explicit empty")
	}
	next, err = next.editedConfig(clusterinit.InitPrefix+"TLS_KEY", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Apply(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.TLSKey != "" {
		t.Fatal("removed path leaked from original options during handoff")
	}
}

func TestConfigurationEditorRejectsInvalidInputWithoutChangingState(t *testing.T) {
	s := validE2EState()
	before := fingerprint(s)
	for key, value := range map[string]string{"SMTP_PASSWORD": "private-value", "CEPH_OSD_COUNT": "private-value", clusterinit.InitPrefix + "YES": "true", "EXT_NET_MTU": "1500\nINJECTED=yes", clusterinit.KeyNodeNICs: "missing-pair", "GPU_NODE_MODES": "missing-pair"} {
		_, err := s.editedConfig(key, value, false)
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe validation for %s: %v", key, err)
		}
		if fingerprint(s) != before {
			t.Fatal("invalid edit changed form")
		}
	}
}

func TestConfigurationEditorClearsPrimaryNodeOverride(t *testing.T) {
	for _, remove := range []bool{false, true} {
		s := validE2EState()
		s.HostID = "server-1"
		next, err := s.editedConfig(clusterinit.KeyPrimaryNode, "", remove)
		if err != nil {
			t.Fatal(err)
		}
		if next.HostID != "" {
			t.Fatalf("remove=%t retained primary node %q", remove, next.HostID)
		}
		values, err := next.configMap()
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := values[clusterinit.KeyPrimaryNode]; exists {
			t.Fatalf("remove=%t re-exported old primary node", remove)
		}
	}
}

func TestConfigurationFilterAndSaveDoesNotDiscardMalformedDraft(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	m.configFilter = "MTU"
	for _, f := range m.configurationFields()[4:] {
		if !strings.Contains(f.Label, "MTU") {
			t.Fatal("filter did not constrain keys")
		}
	}
	m.draftPath = filepath.Join(t.TempDir(), "install.env")
	if err := os.WriteFile(m.draftPath, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.st.OSMode, m.st.OSDSizeGB = "rook-ceph-local", "not-a-number"
	m.saveDraft()
	data, _ := os.ReadFile(m.draftPath)
	if string(data) != "original\n" || !strings.Contains(m.notice, "save failed") {
		t.Fatal("malformed partial state overwrote draft")
	}
}
