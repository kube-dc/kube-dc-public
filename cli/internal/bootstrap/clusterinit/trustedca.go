package clusterinit

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

const (
	trustedCAFileName  = "trusted-ca.yaml"
	trustedCAConfigMap = "kube-dc-private-ca"
	trustedCAKey       = "ca.pem"
)

// TrustedCAMaterial is a validated, certificate-only CA bundle. It is public
// trust material, not a secret; private keys and leaf certificates are refused.
type TrustedCAMaterial struct {
	PEM         []byte
	Fingerprint string
	CertCount   int
}

// LoadTrustedCABundle validates and canonicalizes a PEM CA bundle. The
// fingerprint binds a reviewed plan to the exact trust roots applied later.
func LoadTrustedCABundle(path string) (*TrustedCAMaterial, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--trusted-ca-bundle: read %s: %w", path, err)
	}
	remaining := body
	var canonical bytes.Buffer
	count := 0
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			if len(bytes.TrimSpace(remaining)) != 0 {
				return nil, fmt.Errorf("--trusted-ca-bundle: %s contains non-PEM data", path)
			}
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("--trusted-ca-bundle: %s contains forbidden PEM block %q (certificates only; never a private key)", path, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("--trusted-ca-bundle: parse certificate %d in %s: %w", count+1, path, err)
		}
		if !cert.IsCA || !cert.BasicConstraintsValid {
			return nil, fmt.Errorf("--trusted-ca-bundle: certificate %q is not a CA", cert.Subject.String())
		}
		now := time.Now()
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return nil, fmt.Errorf("--trusted-ca-bundle: CA certificate %q is not currently valid (%s to %s)",
				cert.Subject.String(), cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339))
		}
		if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("--trusted-ca-bundle: CA certificate %q cannot sign certificates", cert.Subject.String())
		}
		if err := pem.Encode(&canonical, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
			return nil, fmt.Errorf("--trusted-ca-bundle: canonicalize certificate: %w", err)
		}
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("--trusted-ca-bundle: %s contains no CA certificates", path)
	}
	sum := sha256.Sum256(canonical.Bytes())
	return &TrustedCAMaterial{
		PEM:         canonical.Bytes(),
		Fingerprint: hex.EncodeToString(sum[:]),
		CertCount:   count,
	}, nil
}

func renderTrustedCA(m *TrustedCAMaterial) string {
	var indented strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(m.PEM), "\n"), "\n") {
		indented.WriteString("    ")
		indented.WriteString(line)
		indented.WriteByte('\n')
	}
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + trustedCAConfigMap + `
  namespace: kube-dc
  labels:
    kube-dc.com/system: "true"
    kube-dc.com/system-component: private-ca-trust
data:
  ` + trustedCAKey + `: |
` + indented.String()
}

// WriteTrustedCA persists the one private-CA trust source used by the manager
// and backend. The manager in turn copies the validated bundle into per-org
// OpenIDConnect CRs and OpenBao OIDC configuration.
func WriteTrustedCA(fleetRepo, clusterName string, m *TrustedCAMaterial, out io.Writer) error {
	if m == nil {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	path := filepath.Join(clusterDir, trustedCAFileName)
	body := renderTrustedCA(m)
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != body {
			return fmt.Errorf("trusted-ca: %s already exists with different content; refuse to overwrite trust roots", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("trusted-ca: read %s: %w", path, err)
	} else if err := atomicWrite(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("trusted-ca: write %s: %w", path, err)
	}
	if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"), patchKustomizationResource(trustedCAFileName)); err != nil {
		return fmt.Errorf("trusted-ca: patch kustomization.yaml: %w", err)
	}
	env, err := config.LoadEnv(filepath.Join(clusterDir, "cluster-config.env"))
	if err != nil {
		return fmt.Errorf("trusted-ca: load cluster-config.env: %w", err)
	}
	env.Set("MANAGER_TRUSTED_CA_CONFIGMAP", trustedCAConfigMap)
	env.Set("BACKEND_TRUSTED_CA_CONFIGMAP", trustedCAConfigMap)
	env.Set("BACKEND_TRUSTED_CA_FILENAME", trustedCAKey)
	if err := env.Write(""); err != nil {
		return fmt.Errorf("trusted-ca: update cluster-config.env: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] private-CA trust wired (%d certificate(s), sha256=%s)\n", m.CertCount, m.Fingerprint)
	return nil
}
