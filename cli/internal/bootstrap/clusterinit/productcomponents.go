package clusterinit

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Product components are starter Kustomize Components that init attaches to a
// NEW overlay from inputs it already has, so an installation needs no
// hand-written site overrides for them. A resumed run never adds them: an
// existing installation's volumes are already bound and its data placed.

const (
	// ManagerNodeLabel marks the servers `bootstrap install` creates.
	ManagerNodeLabel = "kube-dc-manager"

	// ExtraExcludeKey lists further reserved ranges of the external network,
	// beyond EXT_NET_EXCLUDE_IPS (which holds the block with the gateway and
	// the management SNAT address).
	ExtraExcludeKey = "EXT_NET_EXTRA_EXCLUDE_IPS"

	platformS3CASecret = "platform-s3-ca"
	platformS3CAFile   = "platform-s3-ca.yaml"
	extNetExcludesDir  = "ext-net-exclusions"
)

// platformBackupNamespaces hold the platform databases whose backups go to the
// installation's S3 hostname.
var platformBackupNamespaces = []string{"monitoring", "keycloak"}

// ProductComponentOptions carries what WriteProductComponents decides from.
type ProductComponentOptions struct {
	FleetRepo   string
	ClusterName string
	// Sets are the operator's input keys (EXT_NET_EXTRA_EXCLUDE_IPS).
	Sets map[string]string
	// TrustedCA is the validated private CA, nil without one.
	TrustedCA *TrustedCAMaterial
	// K8s counts the manager nodes; nil skips the placement components.
	K8s nodeLabelReader
	// PlannedManagers is the reviewed plan's manager count; a live count that
	// differs is reported.
	PlannedManagers int
	// WildcardCertPEM is the operator-supplied TLS chain (byo-wildcard), which
	// must chain to TrustedCA before backups trust only that CA.
	WildcardCertPEM []byte
}

type nodeLabelReader interface {
	NodeLabels(ctx context.Context) (map[string]map[string]string, error)
}

// WriteProductComponents attaches the product components that apply to this
// new overlay, in order:
//
//	infra-core:           generated ext-net-exclusions  EXT_NET_EXTRA_EXCLUDE_IPS set
//	infra-core:           local-path not-default        multi-node ceph-block + VM disks on shared RBD
//	infra-object-storage: ceph-block-default            (same)
//	platform:             platform-backups              Ceph object storage exposed + Keycloak 26
//	platform:             platform-backup-ca            Ceph object storage exposed + private CA + BYO TLS
//	platform:             platform-state-on-managers    three or more manager nodes
//	platform:             openbao-on-managers           + three or more OpenBao members
//
// A starter without a component is left as it is (older starters).
func WriteProductComponents(ctx context.Context, opts ProductComponentOptions, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName)
	var comps []SiteComponent
	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(opts.FleetRepo, rel, "kustomization.yaml"))
		return err == nil
	}
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(clusterDir, name))
		if os.IsNotExist(err) {
			return "", nil
		}
		return string(b), err
	}
	objectStorage, err := read(filepath.Join("object-storage", "kustomization.yaml"))
	if err != nil {
		return err
	}
	platform, err := read("platform.yaml")
	if err != nil {
		return err
	}
	envBody, err := read("cluster-config.env")
	if err != nil {
		return err
	}
	env := func(key string) string { return strings.TrimSpace(envValue(envBody, key)) }
	_, statErr := os.Stat(filepath.Join(clusterDir, "rbd-vm.yaml"))
	vmOnSharedRBD := statErr == nil
	s3Reachable := strings.Contains(objectStorage, "object-storage/bucket-provisioning") &&
		strings.Contains(objectStorage, "object-storage/exposure")

	if extra := strings.TrimSpace(opts.Sets[ExtraExcludeKey]); extra != "" {
		rel, err := writeExtNetExclusions(opts.FleetRepo, opts.ClusterName, extra)
		if err != nil {
			return err
		}
		comps = append(comps, SiteComponent{Layer: "infra-core", Path: rel})
		fmt.Fprintf(out, "[scaffold] external network: also excluding %s from tenant allocation\n", extra)
	}

	// Volumes that name no class move to replicated storage only where VM
	// disks already live there; with local VM disks local-path stays default.
	if strings.Contains(objectStorage, "object-storage/block-class") && vmOnSharedRBD &&
		has("infrastructure/local-path/components/not-default") && has("infrastructure/object-storage/components/ceph-block-default") {
		comps = append(comps,
			SiteComponent{Layer: "infra-core", Path: "infrastructure/local-path/components/not-default"},
			SiteComponent{Layer: "infra-object-storage", Path: "infrastructure/object-storage/components/ceph-block-default"})
		fmt.Fprintln(out, "[scaffold] default StorageClass: ceph-block (replicated); local-path stays available by name")
	}

	// Platform backups go to https://<S3 hostname>: only with the S3 endpoint
	// exposed. The CA component trusts the private CA, which signs that
	// endpoint only when the operator supplied the TLS certificate.
	keycloak26 := strings.Contains(platform, "keycloak/components/keycloak-26")
	backups := false
	switch {
	case !s3Reachable:
		fmt.Fprintln(out, "[scaffold] platform backups: Keycloak PostgreSQL backups skipped, the S3 endpoint is not exposed")
	case keycloak26 && has("platform/components/platform-backups"):
		comps = append(comps, SiteComponent{Layer: "platform", Path: "platform/components/platform-backups"})
		backups = true
		fmt.Fprintln(out, "[scaffold] platform backups: Keycloak PostgreSQL archived and backed up daily; RKE2 etcd bucket created")
	}
	// The CA component also patches keycloak-db's backup section, which exists
	// only with platform-backups (without Keycloak 26 there is no keycloak-db).
	if s3Reachable && opts.TrustedCA != nil && env("TLS_MODE") == TLSModeBYOWildcard && (backups || !keycloak26) &&
		has("platform/components/platform-backup-ca") {
		if err := certChainsTo(opts.WildcardCertPEM, opts.TrustedCA.PEM); err != nil {
			fmt.Fprintf(out, "[scaffold] platform backups keep the system trust store: the TLS certificate does not chain to the private CA (%v)\n", err)
		} else {
			if err := writePlatformS3CA(clusterDir, opts.TrustedCA); err != nil {
				return err
			}
			comps = append(comps, SiteComponent{Layer: "platform", Path: "platform/components/platform-backup-ca"})
			fmt.Fprintln(out, "[scaffold] platform backups trust the private CA (Secret platform-s3-ca)")
		}
	}

	if has("platform/components/platform-state-on-managers") {
		managers, err := countManagerNodes(ctx, opts.K8s)
		switch {
		case err != nil:
			fmt.Fprintf(out, "[scaffold] platform state placement: manager nodes not readable (%v); left to the scheduler\n", err)
		case managers >= 3:
			if opts.PlannedManagers > 0 && opts.PlannedManagers != managers {
				fmt.Fprintf(out, "[scaffold] WARNING: %d nodes carry %s=true but the plan counted %d manager nodes\n", managers, ManagerNodeLabel, opts.PlannedManagers)
			}
			comps = append(comps, SiteComponent{Layer: "platform", Path: "platform/components/platform-state-on-managers"})
			fmt.Fprintf(out, "[scaffold] platform databases (Grafana and Keycloak PostgreSQL) on the %d manager nodes, one instance per node, local-path\n", managers)
			if mon := env("MONITORING_STORAGE_CLASS"); mon != "" && mon != "local-path" {
				fmt.Fprintf(out, "[scaffold]   MONITORING_STORAGE_CLASS=%s still applies to Mimir\n", mon)
			}
			replicas, _ := strconv.Atoi(env("OPENBAO_REPLICAS"))
			switch {
			case replicas < 3:
			case replicas > managers:
				fmt.Fprintf(out, "[scaffold] OpenBao placement: %d members do not fit one per manager node (%d); left to the scheduler\n", replicas, managers)
			case has("platform/components/openbao-on-managers"):
				comps = append(comps, SiteComponent{Layer: "platform", Path: "platform/components/openbao-on-managers"})
				fmt.Fprintf(out, "[scaffold] OpenBao's %d members on the manager nodes, one per node, local-path\n", replicas)
			}
		default:
			fmt.Fprintf(out, "[scaffold] platform state placement: %d manager node(s), fewer than three; left to the scheduler\n", managers)
		}
	}

	if len(comps) == 0 {
		return nil
	}
	return WriteSiteComponents(opts.FleetRepo, opts.ClusterName, comps, out)
}

func countManagerNodes(ctx context.Context, k8s nodeLabelReader) (int, error) {
	if k8s == nil {
		return 0, fmt.Errorf("no cluster connection")
	}
	labels, err := k8s.NodeLabels(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range labels {
		if l[ManagerNodeLabel] == "true" {
			n++
		}
	}
	return n, nil
}

// writeExtNetExclusions generates the component that lists the external
// network's reserved ranges: EXT_NET_EXCLUDE_IPS (substituted by Flux, after
// init widened it for the management SNAT address) plus the extra ones.
func writeExtNetExclusions(fleetRepo, clusterName, extra string) (string, error) {
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	envBody, err := os.ReadFile(filepath.Join(clusterDir, "cluster-config.env"))
	if err != nil {
		return "", fmt.Errorf("%s: read cluster-config.env: %w", ExtraExcludeKey, err)
	}
	subnets, err := os.ReadFile(filepath.Join(fleetRepo, "infrastructure", "kube-ovn-network", "subnets.yaml"))
	if err != nil || !strings.Contains(string(subnets), "network.kube-dc.com/default-external") {
		return "", fmt.Errorf("%s: this starter's external Subnet carries no network.kube-dc.com/default-external label to attach the exclusions to", ExtraExcludeKey)
	}
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(envValue(string(envBody), keyExtCIDR)))
	if err != nil {
		return "", fmt.Errorf("%s needs a valid %s", ExtraExcludeKey, keyExtCIDR)
	}
	var b strings.Builder
	b.WriteString(`# Generated by kube-dc bootstrap init from ` + ExtraExcludeKey + `.
# Addresses of the external network that Kube-OVN must never hand to tenants.
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
patches:
  - target:
      group: kubeovn.io
      version: v1
      kind: Subnet
      labelSelector: network.kube-dc.com/default-external=true
    patch: |-
      - op: replace
        path: /spec/excludeIps
        value:
          - "${EXT_NET_EXCLUDE_IPS}"
`)
	for _, entry := range strings.Split(extra, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		a, z, ok := parseExcludeEntry(entry)
		if !ok || !ipnet.Contains(a) || !ipnet.Contains(z) || ip4ToU32(a) > ip4ToU32(z) {
			return "", fmt.Errorf("%s entry %q is not an address or an a..b range inside %s", ExtraExcludeKey, entry, ipnet)
		}
		fmt.Fprintf(&b, "          - %q\n", entry)
	}
	dir := filepath.Join(clusterDir, extNetExcludesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(dir, "kustomization.yaml"), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("clusters", clusterName, extNetExcludesDir)), nil
}

// writePlatformS3CA writes the private CA as Secret platform-s3-ca in each
// platform backup namespace (CNPG reads endpointCA from a Secret). The CA is
// public material.
func writePlatformS3CA(clusterDir string, m *TrustedCAMaterial) error {
	var indented strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(m.PEM), "\n"), "\n") {
		indented.WriteString("    ")
		indented.WriteString(line)
		indented.WriteByte('\n')
	}
	var b strings.Builder
	for i, ns := range platformBackupNamespaces {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(`apiVersion: v1
kind: Secret
metadata:
  name: ` + platformS3CASecret + `
  namespace: ` + ns + `
  labels:
    kube-dc.com/system: "true"
    kube-dc.com/system-component: private-ca-trust
type: Opaque
stringData:
  ca.crt: |
` + indented.String())
	}
	if err := atomicWrite(filepath.Join(clusterDir, platformS3CAFile), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("platform-s3-ca: %w", err)
	}
	if err := ensureRootNamespaces(clusterDir, platformBackupNamespaces); err != nil {
		return fmt.Errorf("platform-s3-ca: %w", err)
	}
	if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"), patchKustomizationResource(platformS3CAFile)); err != nil {
		return fmt.Errorf("platform-s3-ca: patch kustomization.yaml: %w", err)
	}
	return nil
}

// nodeLabelsOf keeps a nil K8sClient a nil reader.
func nodeLabelsOf(k ports.K8sClient) nodeLabelReader {
	if k == nil {
		return nil
	}
	return k
}

// validateExtraExcludes checks EXT_NET_EXTRA_EXCLUDE_IPS at plan time, against
// EXT_NET_CIDR when the input names it (a preset default is checked again when
// the component is written).
func validateExtraExcludes(sets map[string]string) []string {
	extra := strings.TrimSpace(sets[ExtraExcludeKey])
	if extra == "" {
		return nil
	}
	var ipnet *net.IPNet
	if cidr := strings.TrimSpace(sets[keyExtCIDR]); cidr != "" {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			ipnet = n
		}
	}
	var errs []string
	for _, entry := range strings.Split(extra, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		a, z, ok := parseExcludeEntry(entry)
		switch {
		case !ok || ip4ToU32(a) > ip4ToU32(z):
			errs = append(errs, fmt.Sprintf("%s entry %q is not an address or an a..b range", ExtraExcludeKey, entry))
		case ipnet != nil && (!ipnet.Contains(a) || !ipnet.Contains(z)):
			errs = append(errs, fmt.Sprintf("%s entry %q is outside %s %s", ExtraExcludeKey, entry, keyExtCIDR, ipnet))
		}
	}
	return errs
}

// certChainsTo verifies a PEM chain (leaf first) against a PEM CA bundle used
// as the only roots.
func certChainsTo(chainPEM, caPEM []byte) error {
	var certs []*x509.Certificate
	for rest := chainPEM; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return fmt.Errorf("no certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("no CA certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

func wildcardCertPEM(m *WildcardTLSMaterial) []byte {
	if m == nil {
		return nil
	}
	return m.CertPEM
}
