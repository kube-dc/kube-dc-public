package clusterinit

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func productFleet(t *testing.T, objectStorage string) (repo, dir string) {
	t.Helper()
	repo, dir = siteFleet(t)
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	component := "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"
	for _, c := range []string{
		"infrastructure/local-path/components/not-default",
		"infrastructure/object-storage/components/ceph-block-default",
		"platform/components/platform-backups",
		"platform/components/platform-backup-ca",
		"platform/components/platform-state-on-managers",
		"platform/components/openbao-on-managers",
	} {
		write(filepath.Join(repo, c, "kustomization.yaml"), component)
	}
	write(filepath.Join(dir, "platform.yaml"), strings.Replace(sitePlatformYAML,
		"    - ./gateway-config/components/host-bind\n",
		"    - ./gateway-config/components/host-bind\n    - ./keycloak/components/keycloak-26\n", 1))
	write(filepath.Join(repo, "infrastructure", "kube-ovn-network", "subnets.yaml"), "metadata:\n  labels:\n    network.kube-dc.com/default-external: \"true\"\n")
	write(filepath.Join(dir, "cluster-config.env"), "EXT_NET_CIDR=192.0.2.0/24\nEXT_NET_EXCLUDE_IPS=192.0.2.1..192.0.2.64\nTLS_MODE=byo-wildcard\nOPENBAO_REPLICAS=3\n")
	write(filepath.Join(dir, "infra-object-storage.yaml"), "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-object-storage\n  namespace: flux-system\nspec:\n  path: ./clusters/site/object-storage\n  prune: false\n")
	write(filepath.Join(dir, "rbd-vm.yaml"), "# VM disks on shared RBD\n")
	write(filepath.Join(dir, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - platform.yaml\n")
	if objectStorage != "" {
		write(filepath.Join(dir, "object-storage", "kustomization.yaml"), objectStorage)
	}
	return repo, dir
}

// testCAAndLeaf returns a CA certificate and a leaf it signed, both PEM.
func testCAAndLeaf(t *testing.T) (caPEM, leafPEM []byte) {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey, leafKey := key(), key()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "*.example.test"},
		DNSNames: []string{"*.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
}

const cephOverlay = "resources:\n  - ../../../infrastructure/object-storage/bucket-provisioning\n  - ../../../infrastructure/object-storage/exposure\n  - ../../../infrastructure/object-storage/block-class\n"

func managers(n int) *fakeNodeLabeler {
	l := map[string]map[string]string{"worker": {}}
	for i := 0; i < n; i++ {
		l[string(rune('a'+i))] = map[string]string{ManagerNodeLabel: "true"}
	}
	return &fakeNodeLabeler{labels: l}
}

// A ten-node bare-metal installation with Ceph, a private CA, three managers
// and a second reserved range gets every product component and no override.
func TestWriteProductComponentsFullInstallation(t *testing.T) {
	repo, dir := productFleet(t, cephOverlay)
	caPEM, leafPEM := testCAAndLeaf(t)
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		Sets:            map[string]string{ExtraExcludeKey: "192.0.2.240..192.0.2.254"},
		TrustedCA:       &TrustedCAMaterial{PEM: caPEM},
		WildcardCertPEM: leafPEM,
		K8s:             managers(3),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
	for _, want := range []string{"- ../../clusters/site/ext-net-exclusions", "- ../local-path/components/not-default"} {
		if !strings.Contains(string(infra), want) {
			t.Fatalf("infra-core lacks %q:\n%s", want, infra)
		}
	}
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	p := string(platform)
	backups, ca, state := strings.Index(p, "components/platform-backups"), strings.Index(p, "components/platform-backup-ca"), strings.Index(p, "components/platform-state-on-managers")
	if backups < 0 || ca < backups || state < 0 || !strings.Contains(p, "components/openbao-on-managers") {
		t.Fatalf("platform components missing or misordered:\n%s", p)
	}
	osLayer, _ := os.ReadFile(filepath.Join(dir, "infra-object-storage.yaml"))
	if !strings.Contains(string(osLayer), "components/ceph-block-default") {
		t.Fatalf("infra-object-storage lacks the default class:\n%s", osLayer)
	}
	ex, _ := os.ReadFile(filepath.Join(dir, "ext-net-exclusions", "kustomization.yaml"))
	if !strings.Contains(string(ex), `- "${EXT_NET_EXCLUDE_IPS}"`) || !strings.Contains(string(ex), `- "192.0.2.240..192.0.2.254"`) ||
		!strings.Contains(string(ex), "network.kube-dc.com/default-external=true") {
		t.Fatalf("exclusions component:\n%s", ex)
	}
	secret, _ := os.ReadFile(filepath.Join(dir, "platform-s3-ca.yaml"))
	if strings.Count(string(secret), "name: platform-s3-ca") != 2 || strings.Contains(string(secret), "PRIVATE") {
		t.Fatalf("platform-s3-ca:\n%s", secret)
	}
	kz, _ := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if !strings.Contains(string(kz), "platform-s3-ca.yaml") {
		t.Fatalf("root kustomization lacks the Secret file:\n%s", kz)
	}
}

// Without Ceph, a private CA or three managers nothing is attached, and the
// backup CA component never appears without the backups it patches.
func TestWriteProductComponentsMinimalInstallation(t *testing.T) {
	repo, dir := productFleet(t, "")
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		TrustedCA: &TrustedCAMaterial{PEM: []byte("x")},
		K8s:       managers(1),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	if strings.Contains(string(platform), "platform/components") {
		t.Fatalf("unexpected components:\n%s", platform)
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-s3-ca.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Secret written without backups: %v", err)
	}
}

func TestExtraExcludesRejectAddressesOutsideTheNetwork(t *testing.T) {
	repo, _ := productFleet(t, "")
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		Sets: map[string]string{ExtraExcludeKey: "198.51.100.1..198.51.100.9"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), ExtraExcludeKey) {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// Each gate the review asked for: no S3 exposure means no platform backups;
// local VM disks keep local-path default; ACME TLS gets no private-CA trust;
// one OpenBao member stays off the managers' local disks.
func TestWriteProductComponentsGates(t *testing.T) {
	repo, dir := productFleet(t, "resources:\n  - ../../../infrastructure/object-storage/bucket-provisioning\n  - ../../../infrastructure/object-storage/block-class\n")
	if err := os.Remove(filepath.Join(dir, "rbd-vm.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte("EXT_NET_CIDR=192.0.2.0/24\nTLS_MODE=acme\nOPENBAO_REPLICAS=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		TrustedCA: &TrustedCAMaterial{PEM: []byte("x")},
		K8s:       managers(3),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
	for _, unwanted := range []string{"platform-backups", "platform-backup-ca", "openbao-on-managers"} {
		if strings.Contains(string(platform), unwanted) {
			t.Fatalf("%s attached:\n%s", unwanted, platform)
		}
	}
	if !strings.Contains(string(platform), "platform-state-on-managers") || strings.Contains(string(infra), "not-default") {
		t.Fatalf("platform:\n%s\ninfra:\n%s", platform, infra)
	}
}

func TestValidateExtraExcludes(t *testing.T) {
	ok := map[string]string{ExtraExcludeKey: "192.0.2.240..192.0.2.254, 192.0.2.200", keyExtCIDR: "192.0.2.0/24"}
	if errs := validateExtraExcludes(ok); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, bad := range []string{"198.51.100.1", "192.0.2.9..192.0.2.2", "nope"} {
		if errs := validateExtraExcludes(map[string]string{ExtraExcludeKey: bad, keyExtCIDR: "192.0.2.0/24"}); len(errs) != 1 {
			t.Fatalf("%q: %v", bad, errs)
		}
	}
}

// A starter whose external Subnet lacks the label refuses the extra ranges
// instead of silently not applying them.
func TestExtraExcludesRequireTheSubnetLabel(t *testing.T) {
	repo, _ := productFleet(t, "")
	if err := os.WriteFile(filepath.Join(repo, "infrastructure", "kube-ovn-network", "subnets.yaml"), []byte("metadata: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		Sets: map[string]string{ExtraExcludeKey: "192.0.2.240..192.0.2.254"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "default-external") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// A TLS certificate that the bundle did not sign keeps the system trust
// store; more OpenBao members than managers keep the scheduler's placement.
func TestWriteProductComponentsSkipsUnchainedCAAndOversizedOpenBao(t *testing.T) {
	repo, dir := productFleet(t, cephOverlay)
	caPEM, _ := testCAAndLeaf(t)
	_, otherLeaf := testCAAndLeaf(t)
	if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"), []byte("EXT_NET_CIDR=192.0.2.0/24\nTLS_MODE=byo-wildcard\nOPENBAO_REPLICAS=5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := WriteProductComponents(context.Background(), ProductComponentOptions{
		FleetRepo: repo, ClusterName: "site",
		TrustedCA: &TrustedCAMaterial{PEM: caPEM}, WildcardCertPEM: otherLeaf,
		K8s: managers(3),
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	if strings.Contains(string(platform), "platform-backup-ca") || strings.Contains(string(platform), "openbao-on-managers") ||
		!strings.Contains(string(platform), "platform-backups") {
		t.Fatalf("platform:\n%s", platform)
	}
	if !strings.Contains(out.String(), "does not chain") || !strings.Contains(out.String(), "do not fit") {
		t.Fatalf("output:\n%s", out.String())
	}
}
