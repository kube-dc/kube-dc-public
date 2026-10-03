package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func baseConfig(name string) string {
	return "CLUSTER_NAME=" + name + "\nDOMAIN=example.test\nNODE_EXTERNAL_IP=192.0.2.10\nEMAIL=ops@example.test\nKUBE_DC_INIT_PRESET=internal-only\nKUBE_DC_INIT_FLEET_MODE=new-repo\nOBJECT_STORAGE_MODE=disabled\n"
}

func fixture(t *testing.T) (Spec, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cluster.env"), []byte(baseConfig("demo")+"KUBE_DC_INIT_NODE_NICS=server-1=eth0\nEXTRA_SETTING=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), []byte(`{"version":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := Spec{
		SchemaVersion: SchemaVersion,
		Name:          "demo", Profile: "evaluation@v1",
		Release:      Release{RecordFile: filepath.Join(dir, "release.json"), StarterRef: "oci://example.test/starter@sha256:" + strings.Repeat("a", 64), RKE2Version: "v1.33.0+rke2r1"},
		Target:       Target{Intent: clusterinit.ModeAuto},
		Hosts:        []Host{{ID: "server-1", SSHAlias: "admin@server-1", Role: "server", ManagementAddress: "192.0.2.10", NIC: "eth0"}},
		Platform:     Platform{ConfigFile: filepath.Join(dir, "cluster.env")},
		Verification: Verification{Capabilities: []string{"containers", "volumes"}},
	}
	return s, dir
}

func TestCompileImportsConfigAndBindsRelease(t *testing.T) {
	s, _ := fixture(t)
	got, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Init.Name != "demo" || got.Init.Domain != "example.test" || got.Init.SSHHost != "admin@server-1" || got.Init.NodeNICs["server-1"] != "eth0" {
		t.Fatalf("host/config mapping failed: %+v", got.Init)
	}
	if got.Init.Sets["EXTRA_SETTING"] != "keep" {
		t.Fatalf("advanced config dropped: %v", got.Init.Sets)
	}
	if len(got.InputHash) != 64 {
		t.Fatalf("invalid hash: %q", got.InputHash)
	}
	before := got.InputHash
	if err := os.WriteFile(s.Release.RecordFile, []byte(`{"version":"v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if before == after.InputHash {
		t.Fatal("release record drift did not change hash")
	}
}

func TestCompileRejectsMalformedSSHTarget(t *testing.T) {
	for _, target := range []string{"admin@server-1:abc", "admin@server-1:0", "admin@server-1:65536", "admin@@server-1"} {
		t.Run(target, func(t *testing.T) {
			s, _ := fixture(t)
			s.Hosts[0].SSHAlias = target
			if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "hosts[0].sshAlias") {
				t.Fatalf("invalid SSH target was accepted: %v", err)
			}
		})
	}
}

func TestCompileRejectsVMVerificationWithoutKVMGate(t *testing.T) {
	for _, setting := range []string{"KUBE_DC_INIT_NO_KUBEVIRT=true", "KUBE_DC_INIT_ALLOW_NO_KVM=true"} {
		t.Run(setting, func(t *testing.T) {
			s, _ := fixture(t)
			s.Verification.Capabilities = []string{"virtual-machines"}
			if err := os.WriteFile(s.Platform.ConfigFile, []byte(baseConfig("demo")+setting+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "virtual-machines requires KubeVirt") {
				t.Fatalf("VM verification bypass accepted: %v", err)
			}
		})
	}
}

func TestCompileHashIgnoresHostOrderAndCredentialLocation(t *testing.T) {
	s, dir := fixture(t)
	s.Hosts = append(s.Hosts, Host{ID: "worker-1", SSHAlias: "admin@worker-1", Role: "agent", ManagementAddress: "192.0.2.11"})
	firstCA, secondCA := filepath.Join(dir, "first-ca.pem"), filepath.Join(dir, "second-ca.pem")
	writeTestCA(t, firstCA)
	data, err := os.ReadFile(firstCA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondCA, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.CredentialRefs = map[string]string{"trusted-ca-bundle": "file:" + firstCA}
	a, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Hosts[0], s.Hosts[1] = s.Hosts[1], s.Hosts[0]
	s.Verification.Capabilities[0], s.Verification.Capabilities[1] = s.Verification.Capabilities[1], s.Verification.Capabilities[0]
	s.CredentialRefs["trusted-ca-bundle"] = "file:" + secondCA
	b, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.InputHash != b.InputHash {
		t.Fatalf("non-semantic ordering/location changed hash: %s != %s", a.InputHash, b.InputHash)
	}
}

func TestCompilePrimaryServerDoesNotDependOnInventoryOrder(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].Primary = true
	s.Hosts = append(s.Hosts, Host{ID: "server-2", SSHAlias: "admin@server-2", Role: "server", ManagementAddress: "192.0.2.11"})
	a, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Hosts[0], s.Hosts[1] = s.Hosts[1], s.Hosts[0]
	b, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Init.SSHHost != "admin@server-1" || a.InputHash != b.InputHash {
		t.Fatalf("primary selection changed with order: %q, %q != %q", a.Init.SSHHost, a.InputHash, b.InputHash)
	}
	s.Hosts[0].Primary = true
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "primary") {
		t.Fatalf("expected duplicate primary error, got %v", err)
	}
}

func TestCompileRejectsHostConflictAndSecret(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].NIC = "ens3"
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "hosts[].nic") {
		t.Fatalf("expected NIC conflict, got %v", err)
	}
	s.Hosts[0].NIC = "eth0"
	if err := os.WriteFile(s.Platform.ConfigFile, []byte("CLUSTER_NAME=demo\nGITHUB_TOKEN=top-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "credentialRefs") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("expected safe secret error, got %v", err)
	}
}

func TestCompileRejectsUnsupportedCredentialRef(t *testing.T) {
	s, _ := fixture(t)
	s.CredentialRefs = map[string]string{"git": "agent:private-location"}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "credentialRefs.git: this credential is not supported") || strings.Contains(err.Error(), "private-location") {
		t.Fatalf("unsupported credential reference was accepted or disclosed: %v", err)
	}
}

func TestCompileRejectsControlCharactersInSSHAlias(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].SSHAlias = "admin@server-1\x1b[31m"
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "hosts[0].sshAlias") {
		t.Fatalf("unsafe SSH alias was accepted: %v", err)
	}
}

func TestCompileRejectsUnsealKeyAndHostDerivedConflicts(t *testing.T) {
	s, _ := fixture(t)
	cases := []struct{ line, want string }{
		{"OPENBAO_UNSEAL_KEY_1=share\n", "credentialRefs"},
		{"KUBE_OVN_MASTER_NODES=192.0.2.99\n", "KUBE_OVN_MASTER_NODES"},
		{"KUBE_OVN_GW_NODES=old-server\n", "KUBE_OVN_GW_NODES"},
		{"GPU_NODE_MODES=old-server=pod-hami\n", "GPU_NODE_MODES"},
		{"GPU_NODE_MODES=server-1=\n", "GPU_NODE_MODES"},
		{"GPU_NODE_MODES=server-1=pod-hami,server-1=vm-passthrough\n", "GPU_NODE_MODES"},
		{"OBJECT_STORAGE_MODE=rook-ceph-local\nCEPH_LOCAL_OSD_NODE=server-2\n", "CEPH_LOCAL_OSD_NODE"},
	}
	s.Hosts[0].Disk = "/dev/sdb"
	for _, c := range cases {
		if err := os.WriteFile(s.Platform.ConfigFile, []byte("CLUSTER_NAME=demo\n"+c.line), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("input %q: expected %s, got %v", c.line, c.want, err)
		}
	}
}

func TestCompileLocalOSDUsesInventory(t *testing.T) {
	s, _ := fixture(t)
	s.Hosts[0].Disk = "/dev/sdb"
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(baseConfig("demo")+"OBJECT_STORAGE_MODE=rook-ceph-local\nCEPH_LOCAL_OSD_NODE=server-1\nCEPH_LOCAL_OSD_DEVICE=sdb\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Init.RookOSDNode != "server-1" || got.Init.RookOSDDevice != "sdb" {
		t.Fatalf("local OSD not mapped: %+v", got.Init)
	}
}

func TestLoadStrictAndRelativePaths(t *testing.T) {
	s, dir := fixture(t)
	s.Platform.ConfigFile = "cluster.env"
	s.Release.RecordFile = "release.json"
	s.CredentialRefs = map[string]string{"tls-key": "file:auth/tls.key"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Platform.ConfigFile != filepath.Join(dir, "cluster.env") || loaded.Release.RecordFile != filepath.Join(dir, "release.json") || loaded.CredentialRefs["tls-key"] != "file:"+filepath.Join(dir, "auth/tls.key") {
		t.Fatalf("relative paths not resolved: %+v", loaded)
	}
	bad := strings.Replace(string(data), `"configFile":"cluster.env"`, `"configFile":"cluster.env","surprise":true`, 1)
	bad = strings.Replace(bad, `"role":"server"`, `"role":"server","typo":true`, 1)
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "platform.surprise") || !strings.Contains(err.Error(), "hosts[0].typo") {
		t.Fatalf("expected field paths, got %v", err)
	}
}

func TestLoadRepoPathRelativeToSpec(t *testing.T) {
	s, dir := fixture(t)
	if err := os.Mkdir(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	s.Platform.ConfigFile = "config/cluster.env"
	s.Release.RecordFile = "release.json"
	if err := os.WriteFile(filepath.Join(dir, "config", "cluster.env"), []byte(baseConfig("demo")+"KUBE_DC_INIT_REPO=fleet\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Compile(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Init.Repo != filepath.Join(dir, "fleet") {
		t.Fatalf("repo path %q was not spec-relative", got.Init.Repo)
	}
}

func TestCompileAcceptsExistingClusterNameAndRejectsBadDigest(t *testing.T) {
	s, _ := fixture(t)
	s.Name = "eu/dc1"
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(baseConfig("eu/dc1")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(s); err != nil {
		t.Fatal(err)
	}
	s.Release.StarterRef = "oci://x@sha256:garbage"
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "release.starterRef") {
		t.Fatalf("expected bad digest error, got %v", err)
	}
}

func TestCompileBYOWildcardMapsProtectedRefsAndBindsCertificate(t *testing.T) {
	s, dir := fixture(t)
	config := strings.Replace(baseConfig("demo"), "OBJECT_STORAGE_MODE=disabled", "OBJECT_STORAGE_MODE=disabled\nTLS_MODE=byo-wildcard", 1)
	if err := os.WriteFile(s.Platform.ConfigFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "credentialRefs.tls-cert") {
		t.Fatalf("expected missing refs error, got %v", err)
	}
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeWildcardPair(t, certPath, keyPath)
	s.CredentialRefs = map[string]string{"tls-cert": "file:" + certPath, "tls-key": "file:" + keyPath}
	a, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Init.TLSCert != certPath || a.Init.TLSKey != keyPath || a.Init.TLSCertFingerprint == "" {
		t.Fatalf("TLS mapping/fingerprint missing: %+v", a.Init)
	}
	writeWildcardPair(t, certPath, keyPath)
	b, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.InputHash == b.InputHash {
		t.Fatal("changed certificate did not change setup hash")
	}
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "readable by group") {
		t.Fatalf("expected protected key error, got %v", err)
	}
}

func writeWildcardPair(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCompileTrustedCABindsValidatedRoots(t *testing.T) {
	s, dir := fixture(t)
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("not a CA"), 0600); err != nil {
		t.Fatal(err)
	}
	s.CredentialRefs = map[string]string{"trusted-ca-bundle": "file:" + path}
	if _, err := Compile(s); err == nil || !strings.Contains(err.Error(), "trusted-ca-bundle") {
		t.Fatalf("expected CA error, got %v", err)
	}
	writeTestCA(t, path)
	a, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Init.TrustedCAFingerprint == "" {
		t.Fatal("missing trust fingerprint")
	}
	writeTestCA(t, path)
	b, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.InputHash == b.InputHash {
		t.Fatal("CA rotation did not change hash")
	}
}

func writeTestCA(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCompileReportsFieldErrors(t *testing.T) {
	s, _ := fixture(t)
	s.SchemaVersion = 99
	s.Profile = "evaluation"
	s.Hosts = append(s.Hosts, s.Hosts[0])
	_, err := Compile(s)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"schemaVersion", "profile", "hosts[1].id", "hosts[1].sshAlias", "hosts[1].managementAddress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in %v", want, err)
		}
	}
}
