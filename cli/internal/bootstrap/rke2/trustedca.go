package rke2

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"
)

// Node-layer private-CA trust (docs/prd/platform-trust-bundle.md).
//
// THERE ARE TWO TRUST LAYERS AND THIS IS THE OTHER ONE
// ====================================================
// The manager distributes the platform CA into ConfigMaps so PODS can verify
// internal endpoints. That does nothing for the NODE: containerd, kubelet and
// RKE2 itself read the host OS trust store, and a ConfigMap is not in it.
//
// On a connected cluster the gap is invisible because container images come
// from public registries over public TLS. In an AIR-GAPPED install the registry
// is internal and served by the organization's own CA, so without this the very
// first image pull fails — before any pod exists, before the manager runs, and
// therefore before any amount of pod-layer trust could help.
//
// This has to happen at bootstrap, ahead of RKE2 starting, which is why it lives
// in the installer rather than in a controller.

const (
	// remoteTrustedCAPath is where the installer stages the bundle before the
	// script moves it into the distribution's anchor directory.
	remoteTrustedCAPath = "/tmp/kube-dc-trusted-ca.pem"

	// trustedCAEnv tells install-server.sh / install-agent.sh that a bundle was
	// staged. Empty means no private CA, which is the correct default.
	trustedCAEnv = "TRUSTED_CA_FILE"

	// trustedCASHAEnv carries the SHA-256 of the validated bundle so the script
	// can refuse anything that changed between validation here and install
	// there. See the staging note in install.go.
	trustedCASHAEnv = "TRUSTED_CA_SHA256"

	// maxTrustedCABytes bounds what we will push to a host. A legitimate root +
	// intermediate chain is a few KB; anything approaching this is a mistake
	// (a whole system bundle, a log file, a truncated binary) and pushing it
	// into a node's trust store is not a mistake worth making silently.
	maxTrustedCABytes = 256 * 1024
)

// TrustedCAMaterial is a validated, certificate-only CA bundle destined for the
// node OS trust store.
type TrustedCAMaterial struct {
	PEM         []byte
	Fingerprint string
	Subjects    []string
}

// LoadTrustedCAForNodes reads and validates a PEM bundle for node installation.
//
// Deliberately stricter than "does it parse". This content is about to be
// installed as a trust anchor on every node of the cluster, which means anything
// it certifies is trusted by containerd, kubelet and every process on the host.
// The checks below are the ones whose absence would be actively dangerous or
// merely useless:
//
//   - CERTIFICATE blocks only. A PRIVATE KEY that reached the file must never be
//     copied to a fleet of hosts, and finding one means the operator handed us
//     the wrong file.
//   - Every certificate must BE a CA with cert-signing usage. A leaf installed
//     as an anchor does not do what the operator expects and quietly widens
//     trust to whatever that leaf's key can sign.
//   - Expiry is reported, not enforced: an expired root in a rotation bundle is
//     normal and refusing it would break the overlap period that makes rotation
//     safe.
func LoadTrustedCAForNodes(path string) (*TrustedCAMaterial, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("--trusted-ca-bundle: %w", err)
	}
	if info.Size() > maxTrustedCABytes {
		return nil, fmt.Errorf("--trusted-ca-bundle: %s is %d bytes; a CA bundle should be a few KB — refusing to install this on every node",
			path, info.Size())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--trusted-ca-bundle: read %s: %w", path, err)
	}

	var canonical []byte
	var subjects []string
	rest := body
	count := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("--trusted-ca-bundle: %s contains a %q block; certificates only (never a private key)",
				path, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("--trusted-ca-bundle: parse certificate %d in %s: %w", count+1, path, err)
		}
		if !cert.IsCA {
			return nil, fmt.Errorf("--trusted-ca-bundle: %q is not a CA certificate; installing a leaf as a node trust anchor would widen trust to anything its key signs",
				cert.Subject.String())
		}
		if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("--trusted-ca-bundle: CA certificate %q cannot sign certificates", cert.Subject.String())
		}
		canonical = append(canonical, pem.EncodeToMemory(block)...)
		subjects = append(subjects, cert.Subject.String())
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("--trusted-ca-bundle: %s contains no PEM certificates", path)
	}
	// pem.Decode stops at the first byte it cannot read, so ignoring the tail
	// silently ACCEPTS a shorter bundle than the operator supplied. In a rotation
	// bundle — old CA followed by a truncated new one — that installs only the
	// OUTGOING CA and strands every node from the registry when it expires. Same
	// rule the controller applies in trustbundle.SanitizeBundle.
	if rest := strings.TrimSpace(string(rest)); rest != "" {
		return nil, fmt.Errorf("--trusted-ca-bundle: %s has %d trailing bytes that are not a PEM certificate; "+
			"refusing to install a partial bundle", path, len(rest))
	}

	sum := sha256.Sum256(canonical)
	return &TrustedCAMaterial{
		PEM:         canonical,
		Fingerprint: hex.EncodeToString(sum[:]),
		Subjects:    subjects,
	}, nil
}

// Summary is the operator-facing description printed before install. Node trust
// is a fleet-wide, security-relevant change, so it is shown rather than applied
// silently.
func (m *TrustedCAMaterial) Summary() string {
	if m == nil {
		return "none (public CAs only)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d CA certificate(s), sha256:%s", len(m.Subjects), m.Fingerprint[:16])
	for _, s := range m.Subjects {
		fmt.Fprintf(&b, "\n      - %s", s)
	}
	return b.String()
}

// ExpiringSoon reports CAs close to expiry, for a warning rather than a refusal.
// An operator installing a root that dies in a month should hear about it at
// install time, not when every node stops trusting the registry.
func (m *TrustedCAMaterial) ExpiringSoon(within time.Duration, now time.Time) []string {
	if m == nil {
		return nil
	}
	var out []string
	rest := m.PEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if cert.NotAfter.Sub(now) < within {
			out = append(out, fmt.Sprintf("%s (expires %s)", cert.Subject.String(), cert.NotAfter.Format("2006-01-02")))
		}
	}
	return out
}
