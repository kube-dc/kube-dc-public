package initform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func rowByLabel(t *testing.T, m *PanelModel, section, label string) panelField {
	t.Helper()
	for _, f := range m.fields {
		if f.Section == section && f.Label == label {
			return f
		}
	}
	t.Fatalf("no %s/%s row", section, label)
	return panelField{}
}

func TestPanel_CertificatesRowsFollowTheTLSMode(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	labels := fieldLabels(m, "Certificates")
	if !hasLabel(labels, "TLS mode") || !hasLabel(labels, "Private CA bundle file") || hasLabel(labels, "Wildcard certificate file") || hasLabel(labels, "Route53 region") {
		t.Fatalf("acme default rows: %v", labels)
	}
	mode := rowByLabel(t, m, "Certificates", "TLS mode")
	if got := mode.Get(m.st); got != clusterinit.TLSModeACME {
		t.Fatalf("default mode %q", got)
	}
	mode.Set(m.st, clusterinit.TLSModeBYOWildcard)
	labels = fieldLabels(m, "Certificates")
	if !hasLabel(labels, "Wildcard certificate file") || !hasLabel(labels, "Wildcard key file") {
		t.Fatalf("byo-wildcard rows: %v", labels)
	}
	if errs := strings.Join(m.validationErrors(), " "); !strings.Contains(errs, "Certificates/Wildcard certificate file: required") {
		t.Fatalf("missing certificate must block Review: %s", errs)
	}
	mode.Set(m.st, clusterinit.TLSModeACMEDNS01Route53)
	labels = fieldLabels(m, "Certificates")
	if hasLabel(labels, "Wildcard certificate file") || !hasLabel(labels, "Route53 hosted zone ID") || hasLabel(labels, "Cloudflare zone") {
		t.Fatalf("route53 rows: %v", labels)
	}
	mode.Set(m.st, clusterinit.TLSModeACMEDNS01Cloudflare)
	if labels = fieldLabels(m, "Certificates"); !hasLabel(labels, "Cloudflare API token file") || hasLabel(labels, "Route53 region") {
		t.Fatalf("cloudflare rows: %v", labels)
	}
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Fatalf("cloudflare needs no required row (token may come from the environment): %v", errs)
	}
}

func TestPanel_CertificatesRowsReachInitOptions(t *testing.T) {
	sec := secureDir(t)
	m := NewPanelModel(validE2EState(), "")
	rowByLabel(t, m, "Certificates", "TLS mode").Set(m.st, clusterinit.TLSModeBYOWildcard)
	rowByLabel(t, m, "Certificates", "Wildcard certificate file").Set(m.st, sec+"/wildcard.crt")
	rowByLabel(t, m, "Certificates", "Wildcard key file").Set(m.st, sec+"/wildcard.key")
	rowByLabel(t, m, "Certificates", "Private CA bundle file").Set(m.st, sec+"/ca.pem")
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Fatalf("complete byo-wildcard selection: %v", errs)
	}
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.TLSMode != clusterinit.TLSModeBYOWildcard || o.TLSCert != sec+"/wildcard.crt" || o.TLSKey != sec+"/wildcard.key" || o.TrustedCABundle != sec+"/ca.pem" {
		t.Fatalf("options: mode=%q cert=%q key=%q ca=%q", o.TLSMode, o.TLSCert, o.TLSKey, o.TrustedCABundle)
	}
	flags := m.st.EquivalentFlags(o)
	for _, want := range []string{"--tls-mode=byo-wildcard", "--tls-cert=" + sec + "/wildcard.crt", "--tls-key=" + sec + "/wildcard.key", "--trusted-ca-bundle=" + sec + "/ca.pem"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("equivalent flags miss %s: %s", want, flags)
		}
	}
	// The environment editor shows the same values: one input, two views.
	values, err := m.st.configMap()
	if err != nil || values[keyTLSMode] != clusterinit.TLSModeBYOWildcard || values[keyTLSCert] != sec+"/wildcard.crt" {
		t.Fatalf("config map: %v %v", values, err)
	}
}

func TestPanel_ClearingAPrefilledCertificateInputTakesEffect(t *testing.T) {
	old := secureDir(t) + "/ca.pem"
	prefilled := &clusterinit.InitOptions{TrustedCABundle: old}
	st := &State{}
	st.FromOptions(prefilled)
	m := NewPanelModel(st, "")
	ca := rowByLabel(t, m, "Certificates", "Private CA bundle file")
	if ca.Get(m.st) != old {
		t.Fatalf("prefill not shown: %q", ca.Get(m.st))
	}
	before := m.st.RetainedInputs
	ca.Set(m.st, "")
	if before[keyTrustedCA] != old {
		t.Fatal("an earlier State copy changed with the edit")
	}
	if err := m.st.Apply(prefilled); err != nil {
		t.Fatal(err)
	}
	if prefilled.TrustedCABundle != "" {
		t.Fatalf("cleared value kept: %q", prefilled.TrustedCABundle)
	}
}

func TestPanel_SwitchingTLSModeDropsTheOtherModesInputs(t *testing.T) {
	sec := secureDir(t)
	m := NewPanelModel(validE2EState(), "")
	mode := rowByLabel(t, m, "Certificates", "TLS mode")
	mode.Set(m.st, clusterinit.TLSModeBYOWildcard)
	rowByLabel(t, m, "Certificates", "Wildcard certificate file").Set(m.st, sec+"/wildcard.crt")
	rowByLabel(t, m, "Certificates", "Wildcard key file").Set(m.st, sec+"/wildcard.key")
	rowByLabel(t, m, "Certificates", "Private CA bundle file").Set(m.st, sec+"/ca.pem")
	mode.Set(m.st, clusterinit.TLSModeACMEDNS01Route53)
	rowByLabel(t, m, "Certificates", "Route53 hosted zone ID").Set(m.st, "Z0123456789ABCDEFGHIJ")
	rowByLabel(t, m, "Certificates", "Route53 access key ID").Set(m.st, "AKIAEXAMPLEEXAMPLE12")
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Fatalf("route53 with the default region must pass, and the hidden BYO paths must be gone: %v", errs)
	}
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.TLSCert != "" || o.TLSKey != "" || o.TrustedCABundle != sec+"/ca.pem" || o.DNS01Route53Region != "" {
		t.Fatalf("after switch: cert=%q key=%q ca=%q region=%q", o.TLSCert, o.TLSKey, o.TrustedCABundle, o.DNS01Route53Region)
	}
	mode.Set(m.st, clusterinit.TLSModeACMEDNS01Cloudflare)
	if o := m.st.RetainedInputs; o[keyRoute53Zone] != "" || o[keyRoute53AccessKey] != "" {
		t.Fatalf("route53 inputs survived the switch: %v", o)
	}
	rowByLabel(t, m, "Certificates", "Cloudflare zone").Set(m.st, "example.com")
	// acme keeps the Cloudflare inputs: they add DNS-01 for the wildcard.
	mode.Set(m.st, clusterinit.TLSModeACME)
	if m.st.RetainedInputs[keyTLSMode] != "" || m.st.RetainedInputs[keyCloudflareZone] != "example.com" {
		t.Fatalf("acme canonicalisation or Cloudflare retention: %v", m.st.RetainedInputs)
	}
	if !hasLabel(fieldLabels(m, "Certificates"), "Cloudflare API token file") {
		t.Fatal("Cloudflare rows hidden under acme")
	}
	mode.Set(m.st, clusterinit.TLSModeBYOWildcard)
	if m.st.RetainedInputs[keyCloudflareZone] != "" {
		t.Fatal("Cloudflare zone survived byo-wildcard")
	}
}

func TestPanel_CloudflareWildcardUnderACMENeedsTheZone(t *testing.T) {
	sec := secureDir(t)
	m := NewPanelModel(validE2EState(), "")
	rowByLabel(t, m, "Certificates", "Cloudflare API token file").Set(m.st, sec+"/cf.token")
	if errs := strings.Join(m.validationErrors(), " "); !strings.Contains(errs, "needs the Cloudflare zone") {
		t.Fatalf("missing zone not reported: %s", errs)
	}
	rowByLabel(t, m, "Certificates", "Cloudflare zone").Set(m.st, "kube-dc.cloud")
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Fatalf("hybrid acme + Cloudflare wildcard: %v", errs)
	}
	o := &clusterinit.InitOptions{}
	if err := m.st.Apply(o); err != nil || o.DNS01CloudflareZone != "kube-dc.cloud" || o.DNS01CloudflareAPITokenFile != sec+"/cf.token" || o.TLSMode != "" {
		t.Fatalf("options: %+v %v", o, err)
	}
}

func TestPanel_CredentialRowsAcceptOnlyAFilePath(t *testing.T) {
	sec := secureDir(t)
	m := NewPanelModel(validE2EState(), "")
	for _, label := range []string{"Private CA bundle file", "Cloudflare API token file"} {
		row := rowByLabel(t, m, "Certificates", label)
		for _, pasted := range []string{"-----BEGIN CERTIFICATE-----", "Bearer abc", "abcdefghijklmnopqrstuvwxyz0123456789ABCD", "relative/path.pem"} {
			if row.Validate(pasted) == nil {
				t.Fatalf("%s accepted %q", label, pasted)
			}
		}
		if err := row.Validate(sec + "/file.pem"); err != nil {
			t.Fatalf("%s refused a path: %v", label, err)
		}
	}
}

func TestPanel_EditorAndRowsAreOneInput(t *testing.T) {
	sec := secureDir(t)
	m := NewPanelModel(validE2EState(), "")
	rowByLabel(t, m, "Certificates", "Private CA bundle file").Set(m.st, sec+"/ca.pem")
	next, err := m.st.editedConfig(keyTLSMode, clusterinit.TLSModeBYOWildcard, false)
	if err != nil {
		t.Fatal(err)
	}
	*m.st = next
	if got := rowByLabel(t, m, "Certificates", "TLS mode").Get(m.st); got != clusterinit.TLSModeBYOWildcard {
		t.Fatalf("editor value not shown in the row: %q", got)
	}
	if got := rowByLabel(t, m, "Certificates", "Private CA bundle file").Get(m.st); got != sec+"/ca.pem" {
		t.Fatalf("row value lost by an editor change: %q", got)
	}
}

func TestPanel_ClearingWithoutPrefilledInputsTakesEffect(t *testing.T) {
	o := &clusterinit.InitOptions{TrustedCABundle: "/old/ca.pem"}
	m := NewPanelModel(validE2EState(), "")
	m.st.RetainedInputs = nil
	ca := rowByLabel(t, m, "Certificates", "Private CA bundle file")
	ca.Set(m.st, "/new/ca.pem")
	ca.Set(m.st, "")
	if len(m.st.RetainedInputs) != 0 {
		t.Fatalf("retained map not empty: %v", m.st.RetainedInputs)
	}
	if err := m.st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.TrustedCABundle != "" {
		t.Fatalf("cleared value kept: %q", o.TrustedCABundle)
	}
}

func TestPanel_SixteenCephNodesSurviveTheRoundTrip(t *testing.T) {
	in := &clusterinit.InitOptions{RookMode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{}}
	for i := 1; i <= 16; i++ {
		in.CephNodes[fmt.Sprintf("node-%02d", i)] = fmt.Sprintf("/dev/disk/by-id/nvme-%02d", i)
	}
	st := &State{}
	st.FromOptions(in)
	if len(st.CephNodes) != 16 {
		t.Fatalf("servers 4 to 16 not kept: %q", st.CephNodes)
	}
	out := &clusterinit.InitOptions{}
	if err := st.Apply(out); err != nil {
		t.Fatal(err)
	}
	if len(out.CephNodes) != 16 || out.CephNodes["node-16"] != "disk/by-id/nvme-16" {
		t.Fatalf("nodes after round trip: %d %v", len(out.CephNodes), out.CephNodes)
	}
	st.CephNodes = append(st.CephNodes, "node-01=/dev/sdz")
	if err := st.Apply(&clusterinit.InitOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate storage node") {
		t.Fatalf("duplicate node accepted: %v", err)
	}
}

func TestPanel_PAYGAndSiteComponentsRoundTrip(t *testing.T) {
	in := &clusterinit.InitOptions{PAYG: true, SiteComponents: "platform=clusters/x/site"}
	st := &State{}
	st.FromOptions(in)
	out := &clusterinit.InitOptions{}
	if err := st.Apply(out); err != nil || !out.PAYG || out.SiteComponents != "platform=clusters/x/site" {
		t.Fatalf("round trip: payg=%v site=%q err=%v", out.PAYG, out.SiteComponents, err)
	}
	if flags := st.EquivalentFlags(out); !strings.Contains(flags, "--payg") || !strings.Contains(flags, "--site-components=") {
		t.Fatalf("equivalent flags: %s", flags)
	}
	next, err := st.editedConfig(clusterinit.InitPrefix+"SITE_COMPONENTS", "", true)
	if err != nil {
		t.Fatal(err)
	}
	cleared := &clusterinit.InitOptions{SiteComponents: "platform=clusters/x/site"}
	if err := next.Apply(cleared); err != nil || cleared.SiteComponents != "" {
		t.Fatalf("editor removal ignored: %q %v", cleared.SiteComponents, err)
	}
}

func TestPanel_EditorRefusesCredentialContentsInFileKeys(t *testing.T) {
	m := NewPanelModel(validE2EState(), "")
	if _, err := m.st.editedConfig(keyCloudflareTokenFile, "abcdefghijklmnopqrstuvwxyz0123456789ABCD", false); err == nil {
		t.Fatal("editor accepted a pasted token for a file key")
	}
	if _, err := m.st.editedConfig(keyTLSKey, "-----BEGIN PRIVATE KEY-----", false); err == nil {
		t.Fatal("editor accepted a pasted key")
	}
	// A Route53 secret access key contains "/" but is never an existing file.
	if _, err := m.st.editedConfig(keyRoute53SecretFile, "/K7MDENG/bPxRfiCYEXAMPLEKEY", false); err == nil {
		t.Fatal("editor accepted a slash-containing secret")
	}
	if _, err := m.st.editedConfig(keyRoute53SecretFile, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", false); err == nil {
		t.Fatal("editor accepted a Route53 secret")
	}
	sec := secureDir(t)
	if _, err := m.st.editedConfig(keyRoute53SecretFile, sec+"/file.pem", false); err != nil {
		t.Fatalf("editor refused an existing file: %v", err)
	}
}

func TestPanel_StorageEditsReachEveryCephNode(t *testing.T) {
	st := validE2EState()
	st.OSMode = string(clusterinit.RookCephMultiNode)
	var pairs []string
	for i := 1; i <= 5; i++ {
		pairs = append(pairs, fmt.Sprintf("node-%d=nvme%dn1", i, i))
	}
	st.CephNodes = pairs
	if got := st.storageDevices(); len(got) != 5 || got["node-5"] != "nvme5n1" {
		t.Fatalf("storage view: %v", got)
	}
	if err := st.setStorageDevice("node-5", ""); err != nil {
		t.Fatal(err)
	}
	if _, kept := st.storageDevices()["node-5"]; kept {
		t.Fatalf("clearing node 5 left it selected: %q", st.CephNodes)
	}
	if err := st.setStorageDevice("node-6", "nvme6n1"); err != nil || st.storageDevices()["node-6"] != "nvme6n1" {
		t.Fatalf("adding a fifth server: %v %v", err, st.storageDevices())
	}
	for i := 7; i <= 17; i++ {
		_ = st.setStorageDevice(fmt.Sprintf("node-%d", i), "sdb")
	}
	if n := len(st.storageDevices()); n != clusterinit.MaxCephSlots {
		t.Fatalf("limit not applied: %d", n)
	}
}

func TestPanel_LongCephDevicePathOpensWhole(t *testing.T) {
	st := validE2EState()
	st.OSMode = string(clusterinit.RookCephMultiNode)
	var pairs []string
	for i := 1; i <= 16; i++ {
		pairs = append(pairs, fmt.Sprintf("storage-server-%02d=/dev/disk/by-id/nvme-INTEL_SSDPED1D480GA_%02dEXAMPLE", i, i))
	}
	st.CephNodes = pairs
	st.CephNodes[15] += strings.Repeat("x", 512)
	if len(st.CephNodes[15]) <= 512 {
		t.Fatalf("fixture too short: %d", len(st.CephNodes[15]))
	}
	m := NewPanelModel(st, "")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.secCursor = sectionIndex(m, "Storage")
	m.focus = focusFields
	for i, f := range m.currentFields() {
		if f.Label == "Ceph node 16" {
			m.fieldCursor = i
		}
	}
	_, _ = m.activate()
	if !m.editing || m.input.Value() != st.CephNodes[15] {
		t.Fatalf("editor truncated the list: editing=%v len=%d want %d", m.editing, len(m.input.Value()), len(st.CephNodes[15]))
	}
}

// secureDir holds real credential files: the panel accepts only existing
// files, never pasted contents.
func secureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"wildcard.crt", "wildcard.key", "ca.pem", "cf.token", "file.pem", "cert.pem", "key.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPanel_PrefilledCredentialThatIsNotAFileIsNeverShownOrSaved(t *testing.T) {
	const secret = "wJalrXUtnFEMI/KAMDENG/bPxRfiCYEXAMPLEKEY"
	in := &clusterinit.InitOptions{TLSMode: clusterinit.TLSModeACMEDNS01Route53, DNS01Route53ZoneID: "Z0123456789ABCDEFGHIJ",
		DNS01Route53AccessKeyID: "AKIAEXAMPLEEXAMPLE12", DNS01Route53SecretKeyFile: secret}
	st := validE2EState()
	st.FromOptions(in)
	m := NewPanelModel(st, "")
	if got := rowByLabel(t, m, "Certificates", "Route53 secret key file").Get(m.st); got != "" {
		t.Fatalf("prefilled secret shown: %q", got)
	}
	values, err := m.st.configMap()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range values {
		if strings.Contains(v, "bPxRfiCY") {
			t.Fatalf("secret reaches the saved input under %s", k)
		}
	}
	if errs := strings.Join(m.validationErrors(), " "); !strings.Contains(errs, keyRoute53SecretFile+" from the prefill is not a file") {
		t.Fatalf("drop not reported: %s", errs)
	}
	sec := secureDir(t)
	rowByLabel(t, m, "Certificates", "Route53 secret key file").Set(m.st, sec+"/file.pem")
	if errs := m.validationErrors(); len(errs) != 0 {
		t.Fatalf("re-entered path: %v", errs)
	}
}

func TestPanel_DroppedCredentialMarkersFollowTheModeAndSurviveEditorEdits(t *testing.T) {
	in := &clusterinit.InitOptions{TLSMode: clusterinit.TLSModeBYOWildcard, TLSCert: "/gone/cert.pem", TLSKey: "/gone/key.pem", TrustedCABundle: "/gone/ca.pem"}
	st := validE2EState()
	st.FromOptions(in)
	m := NewPanelModel(st, "")
	errs := strings.Join(m.validationErrors(), " ")
	for _, k := range []string{keyTLSCert, keyTLSKey, keyTrustedCA} {
		if !strings.Contains(errs, k+" from the prefill") {
			t.Fatalf("%s not reported: %s", k, errs)
		}
	}
	rowByLabel(t, m, "Certificates", "TLS mode").Set(m.st, clusterinit.TLSModeACMEDNS01Route53)
	rowByLabel(t, m, "Certificates", "Route53 hosted zone ID").Set(m.st, "Z0123456789ABCDEFGHIJ")
	rowByLabel(t, m, "Certificates", "Route53 access key ID").Set(m.st, "AKIAEXAMPLEEXAMPLE12")
	errs = strings.Join(m.validationErrors(), " ")
	if strings.Contains(errs, keyTLSCert) || strings.Contains(errs, keyTLSKey) || !strings.Contains(errs, keyTrustedCA) {
		t.Fatalf("after leaving byo-wildcard: %s", errs)
	}
	// An unrelated editor change keeps the unresolved CA marker.
	next, err := m.st.editedConfig("EXT_NET_MTU", "1400", false)
	if err != nil {
		t.Fatal(err)
	}
	*m.st = next
	if !strings.Contains(strings.Join(m.validationErrors(), " "), keyTrustedCA+" from the prefill") {
		t.Fatal("CA marker lost by an editor rebuild")
	}
	// Removing the key in the editor resolves it explicitly.
	next, err = m.st.editedConfig(keyTrustedCA, "", true)
	if err != nil {
		t.Fatal(err)
	}
	*m.st = next
	if strings.Contains(strings.Join(m.validationErrors(), " "), keyTrustedCA) {
		t.Fatal("explicit removal did not resolve the CA marker")
	}
}

func TestPanel_ClearingADroppedRowResolvesItAndSaveWaitsForIt(t *testing.T) {
	st := validE2EState()
	st.FromOptions(&clusterinit.InitOptions{TrustedCABundle: "/gone/ca.pem"})
	m := NewPanelModel(st, "")
	m.draftPath = filepath.Join(t.TempDir(), "install.env")
	m.saveDraft()
	if !strings.HasPrefix(m.notice, "save refused") {
		t.Fatalf("draft saved with an unresolved marker: %q", m.notice)
	}
	if _, err := os.Stat(m.draftPath); err == nil {
		t.Fatal("draft file written")
	}
	rowByLabel(t, m, "Certificates", "Private CA bundle file").Set(m.st, "")
	if errs := strings.Join(m.validationErrors(), " "); strings.Contains(errs, keyTrustedCA) {
		t.Fatalf("explicit clear did not resolve: %s", errs)
	}
	m.saveDraft()
	if !strings.HasPrefix(m.notice, "saved draft") {
		t.Fatalf("save after resolving: %q", m.notice)
	}
}

// A ten-server layout named by stable /dev/disk/by-id paths (as an input file
// carries them) shows no Ceph row error in Review and keeps every node.
func TestPanel_StableDevicePathsPassReview(t *testing.T) {
	in := &clusterinit.InitOptions{RookMode: clusterinit.RookCephMultiNode, CephNodes: map[string]string{}}
	for i := 1; i <= 10; i++ {
		in.CephNodes[fmt.Sprintf("node-%02d", i)] = fmt.Sprintf("/dev/disk/by-id/nvme-EXAMPLE_%02d", i)
	}
	st := validE2EState()
	st.FromOptions(in)
	m := NewPanelModel(st, "")
	for _, e := range m.validationErrors() {
		if strings.Contains(e, "Ceph node") {
			t.Fatalf("stable device path refused in Review: %v", m.validationErrors())
		}
	}
	out := &clusterinit.InitOptions{}
	if err := m.st.Apply(out); err != nil || len(out.CephNodes) != 10 || out.CephNodes["node-10"] != "disk/by-id/nvme-EXAMPLE_10" {
		t.Fatalf("apply: %v %v", err, out.CephNodes)
	}
}

// Device values with a /dev/ prefix reach host discovery with one /dev/, and
// a /dev/loop backing file still counts as file-backed storage.
func TestPanel_PrefixedDevicesInDiscoveryAndStorageChoice(t *testing.T) {
	st := validE2EState()
	st.OSMode = string(clusterinit.RookCephMultiNode)
	st.CephNodes = []string{"node-01=/dev/disk/by-id/nvme-EXAMPLE_01", "node-02=sdb"}
	m := NewPanelModel(st, "")
	if got := m.requestForHost("node-01").Host.Disk; got != "/dev/disk/by-id/nvme-EXAMPLE_01" {
		t.Fatalf("prefixed device: %q", got)
	}
	if got := m.requestForHost("node-02").Host.Disk; got != "/dev/sdb" {
		t.Fatalf("short device: %q", got)
	}
	local := &State{OSMode: string(clusterinit.RookCephLocal), OSDDevice: "/dev/loop0"}
	if got := local.storageChoice(); got != "file-one" {
		t.Fatalf("/dev/loop0 classified as %q", got)
	}
	local.OSDDevice = "/dev/nvme1n1"
	if got := local.storageChoice(); got != "raw" {
		t.Fatalf("/dev/nvme1n1 classified as %q", got)
	}
}
