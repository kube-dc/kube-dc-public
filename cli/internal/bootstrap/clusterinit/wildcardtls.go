package clusterinit

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// Bring-your-own wildcard TLS (TLS_MODE=byo-wildcard).
//
// A cluster whose domain ACME cannot validate — internal DNS, no inbound :80,
// corporate PKI — serves an operator-supplied wildcard instead. Getting that
// right by hand is deceptively hard, and the failure is silent, so `init`
// owns the whole shape:
//
//  1. the material is written ONCE, SOPS-encrypted, under EVERY platform
//     secret name the Gateway listeners reference; and
//  2. every platform ACME Certificate is suppressed.
//
// (2) is not optional. cert-manager reports a Secret it did not issue as
// material to REPLACE ("Issuing certificate as Secret was previously issued
// by \"Issuer.cert-manager.io/\""). On an internal domain the challenge never
// validates, so those Certificates merely sit False — until an issuer some day
// succeeds and overwrites the operator's certificate. Both halves live in Git,
// which is what makes the certificate survive a reinstall.
//
// See docs/platform/certificates.md.

// TLSModeACME is the default: cert-manager issues via the ACME ClusterIssuer.
const TLSModeACME = "acme"

// TLSModeBYOWildcard serves an operator-supplied wildcard certificate.
const TLSModeBYOWildcard = "byo-wildcard"

// wildcardSecretTarget is one Secret the platform expects to exist. Every
// Gateway listener certificateRef must be covered, or that listener serves
// nothing.
type wildcardSecretTarget struct {
	Name      string
	Namespace string
	// DomainTemplated marks a name that embeds the cluster domain
	// (keycloak's "login.<DOMAIN>-tls"), expanded per cluster.
	DomainTemplated bool
}

// wildcardSecretTargets is the authoritative platform TLS secret set, kept in
// step with platform/certificates/certificates.yaml plus the two Certificates
// declared outside ./platform (registry-depot, infra-object-storage).
func wildcardSecretTargets() []wildcardSecretTarget {
	return []wildcardSecretTarget{
		{Name: "wildcard-tls", Namespace: "envoy-gateway-system"},
		{Name: "login.%s-tls", Namespace: "keycloak", DomainTemplated: true},
		{Name: "kube-dc-frontend-tls", Namespace: "kube-dc"},
		{Name: "kube-dc-backend-tls", Namespace: "kube-dc"},
		{Name: "kube-dc-admin-frontend-tls", Namespace: "kube-dc"},
		{Name: "tls-grafana", Namespace: "monitoring"},
		{Name: "tls-mimir-query", Namespace: "monitoring"},
		{Name: "tls-loki-query", Namespace: "monitoring"},
		{Name: "tls-mimir-ruler", Namespace: "monitoring"},
		{Name: "tls-mimir-alertmanager", Namespace: "monitoring"},
		{Name: "tls-flux", Namespace: "flux-system"},
		{Name: "tls-openbao", Namespace: "openbao"},
		{Name: "registry-server-tls", Namespace: "kube-dc"},
		{Name: "s3-server-tls", Namespace: "rook-ceph"},
	}
}

// suppressedCertificate is one ACME Certificate that must not exist in
// byo-wildcard mode, and the Flux Kustomization that DECLARES it.
//
// The declaring Kustomization matters: a patch can only delete a resource from
// the render of the Kustomization that produces it. Suppressing only
// ./platform leaves registry-tls and s3-tls alive, and Flux recreates them
// after every manual delete — observed on a private-CA install.
type suppressedCertificate struct {
	Name          string
	Namespace     string
	Kustomization string // fleet file stem under clusters/<name>/
}

func suppressedCertificates() []suppressedCertificate {
	p := func(n, ns string) suppressedCertificate {
		return suppressedCertificate{Name: n, Namespace: ns, Kustomization: "platform"}
	}
	return []suppressedCertificate{
		p("wildcard-tls", "envoy-gateway-system"),
		p("login-tls", "keycloak"),
		p("kube-dc-frontend-tls", "kube-dc"),
		p("kube-dc-backend-tls", "kube-dc"),
		p("kube-dc-admin-frontend-tls", "kube-dc"),
		p("tls-grafana", "monitoring"),
		p("tls-mimir-query", "monitoring"),
		p("tls-loki-query", "monitoring"),
		p("tls-mimir-ruler", "monitoring"),
		p("tls-mimir-alertmanager", "monitoring"),
		p("tls-flux", "flux-system"),
		p("tls-openbao", "openbao"),
		{Name: "registry-tls", Namespace: "kube-dc", Kustomization: "registry-depot"},
		{Name: "s3-tls", Namespace: "rook-ceph", Kustomization: "infra-object-storage"},
	}
}

// WildcardTLSMaterial is validated certificate/key material ready to write.
type WildcardTLSMaterial struct {
	CertPEM []byte
	KeyPEM  []byte
	// NotAfter is surfaced so the caller can warn operators: nothing renews a
	// BYO certificate automatically.
	NotAfter time.Time
	Subject  string
	// Fingerprint is the hex SHA-256 of the LEAF certificate DER. It is
	// public material, and it is what binds the plan to the certificate the
	// operator reviewed: without it, pair A could pass dry-run and a
	// different-but-also-valid pair B could ship at apply under the identical
	// plan hash (codex review 2026-07-27, HIGH). The plan carries it; apply
	// refuses material whose fingerprint differs.
	Fingerprint string
}

// LoadWildcardTLS reads and validates the operator's material for domain.
//
// Every check here maps to a failure that is silent or misleading at runtime:
// a key that does not match the certificate yields TLS handshake errors with no
// useful message; an expired certificate is refused by browsers only; and a
// certificate that does not actually cover *.<domain> fails just for the
// hostnames nobody tested.
func LoadWildcardTLS(certPath, keyPath, domain string) (*WildcardTLSMaterial, error) {
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("--tls-mode=%s requires --tls-cert and --tls-key", TLSModeBYOWildcard)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read --tls-cert %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read --tls-key %s: %w", keyPath, err)
	}

	// Proves the pair matches AND that both parse.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("--tls-cert and --tls-key do not form a valid pair: %w", err)
	}

	// Parse EVERY certificate block, not just the leaf. tls.X509KeyPair only
	// parses Certificate[0], so a leaf followed by a malformed or unrelated
	// intermediate passes it — and then Envoy rejects the Secret, or clients
	// cannot build a chain, long after `init` reported success.
	var chain []*x509.Certificate
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue // a key in the same file is handled by X509KeyPair above
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate block %d of --tls-cert %s: %w", len(chain)+1, certPath, err)
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("--tls-cert %s contains no CERTIFICATE block", certPath)
	}
	// TLS requires the leaf first. An intermediate-first file is a common
	// mistake and produces handshake failures that look like a client problem.
	leaf := chain[0]
	if leaf.IsCA && len(chain) > 1 {
		return nil, fmt.Errorf(
			"--tls-cert %s starts with a CA certificate (subject %q) — the chain must be leaf-first, then intermediates",
			certPath, leaf.Subject.String())
	}
	// An EKU-restricted certificate that cannot do server auth passes key, date
	// and hostname checks and is then rejected by every client.
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, u := range leaf.ExtKeyUsage {
			if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf(
				"--tls-cert %s is not valid for TLS server authentication (extended key usage %v)",
				certPath, leaf.ExtKeyUsage)
		}
	}
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("--tls-cert expired on %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("--tls-cert is not valid until %s", leaf.NotBefore.Format(time.RFC3339))
	}
	if domain != "" {
		// Verifying ONE hostname does not prove wildcard coverage: a certificate
		// whose only SAN is console.<domain> would pass, get replicated to all
		// platform secrets, and then fail on every OTHER hostname (codex review
		// 2026-07-26, HIGH). Require the wildcard SAN itself, and additionally
		// verify a representative set so Go's matching rules — not our string
		// comparison — have the final say.
		if !hasWildcardSAN(leaf, domain) {
			return nil, fmt.Errorf(
				"--tls-cert must carry the wildcard SAN *.%s (the platform serves many hostnames); subject=%q dnsNames=%v",
				domain, leaf.Subject.String(), leaf.DNSNames)
		}
		for _, h := range []string{"console", "login", "backend", "grafana"} {
			if err := leaf.VerifyHostname(h + "." + domain); err != nil {
				return nil, fmt.Errorf(
					"--tls-cert does not cover %s.%s: %w; dnsNames=%v",
					h, domain, err, leaf.DNSNames)
			}
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	return &WildcardTLSMaterial{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		NotAfter:    leaf.NotAfter,
		Subject:     leaf.Subject.String(),
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// hasWildcardSAN reports whether the certificate carries the exact "*.domain"
// DNS SAN. Compared case-insensitively because DNS names are case-insensitive
// and issuers are inconsistent about it.
func hasWildcardSAN(c *x509.Certificate, domain string) bool {
	want := "*." + strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, n := range c.DNSNames {
		if strings.ToLower(strings.TrimSuffix(n, ".")) == want {
			return true
		}
	}
	return false
}

// RenderWildcardSecrets produces the PLAINTEXT multi-document Secret manifest.
// The caller encrypts it with SOPS before it is ever committed.
func RenderWildcardSecrets(m *WildcardTLSMaterial, domain string) string {
	crt := base64.StdEncoding.EncodeToString(m.CertPEM)
	key := base64.StdEncoding.EncodeToString(m.KeyPEM)

	var b strings.Builder
	b.WriteString("# Operator-supplied wildcard certificate for *." + domain + ".\n")
	b.WriteString("# TLS_MODE=byo-wildcard. Generated by `kube-dc bootstrap init`.\n")
	b.WriteString("#\n")
	b.WriteString("# One certificate, replicated under every platform secret name a Gateway\n")
	b.WriteString("# listener references. The matching ACME Certificates are suppressed in the\n")
	b.WriteString("# cluster's Flux Kustomizations so cert-manager cannot overwrite this\n")
	b.WriteString("# material. Both halves live in Git, so this survives a reinstall.\n")
	b.WriteString("#\n")
	b.WriteString("# Rotation: docs/platform/certificates.md — nothing renews this for you.\n")
	b.WriteString("# notAfter: " + m.NotAfter.Format(time.RFC3339) + "\n")

	for _, t := range wildcardSecretTargets() {
		name := t.Name
		if t.DomainTemplated {
			name = fmt.Sprintf(t.Name, domain)
		}
		b.WriteString("---\n")
		b.WriteString("apiVersion: v1\n")
		b.WriteString("kind: Secret\n")
		b.WriteString("metadata:\n")
		b.WriteString("    name: " + name + "\n")
		b.WriteString("    namespace: " + t.Namespace + "\n")
		b.WriteString("    labels:\n")
		b.WriteString("        kube-dc.com/byo-wildcard-tls: \"true\"\n")
		b.WriteString("type: kubernetes.io/tls\n")
		b.WriteString("data:\n")
		b.WriteString("    tls.crt: " + crt + "\n")
		b.WriteString("    tls.key: " + key + "\n")
	}
	return b.String()
}

// RenderCertificateSuppressionPatches returns, per Flux Kustomization file
// stem, the YAML patch entries that delete its ACME Certificates.
func RenderCertificateSuppressionPatches() map[string]string {
	out := map[string]strings.Builder{}
	for _, c := range suppressedCertificates() {
		b := out[c.Kustomization]
		b.WriteString("    - target:\n")
		b.WriteString("        group: cert-manager.io\n")
		b.WriteString("        version: v1\n")
		b.WriteString("        kind: Certificate\n")
		b.WriteString("        name: " + c.Name + "\n")
		b.WriteString("        namespace: " + c.Namespace + "\n")
		b.WriteString("      patch: |\n")
		b.WriteString("        apiVersion: cert-manager.io/v1\n")
		b.WriteString("        kind: Certificate\n")
		b.WriteString("        metadata:\n")
		b.WriteString("          name: " + c.Name + "\n")
		b.WriteString("          namespace: " + c.Namespace + "\n")
		b.WriteString("        $patch: delete\n")
		out[c.Kustomization] = b
	}
	rendered := make(map[string]string, len(out))
	for k, b := range out {
		rendered[k] = b.String()
	}
	return rendered
}

// byoWildcardTLSMarker identifies the suppression block this writer emits.
// Present in every patched file so re-runs are idempotent, and listed in
// ownedPlatformPatchMarkers so the other platform.yaml patch writers compose
// with it instead of aborting on an "unexpected" patches: block.
const byoWildcardTLSMarker = "TLS_MODE=byo-wildcard: suppress every platform ACME Certificate"

// wildcardSecretsFileName is the committed SOPS artifact. The name must match
// .sops.yaml's \.enc\.yaml$ creation rule.
const wildcardSecretsFileName = "wildcard-tls-secrets.enc.yaml"

// WriteWildcardTLS scaffolds the whole byo-wildcard shape for one cluster:
//
//  1. clusters/<name>/wildcard-tls-secrets.enc.yaml — the material under every
//     platform secret name, SOPS-encrypted via the hardened temp-file flow
//     (plaintext never lands on the tracked path; output verified; the private
//     key must not appear in the ciphertext);
//  2. that file added to the cluster kustomization's resources;
//  3. the ACME Certificate suppression patches appended to each declaring
//     Flux Kustomization that exists in this overlay (platform.yaml always;
//     registry-depot.yaml / infra-object-storage.yaml only when the install
//     scaffolds them — when absent, the Certificates they would declare are
//     absent too, so skipping is correct, and it is REPORTED so an operator
//     adding those layers later knows to re-run or hand-add the patch);
//  4. TLS_MODE=byo-wildcard recorded in cluster-config.env.
//
// The material itself never appears in logs — only subject + notAfter.
func WriteWildcardTLS(fleetRepo, clusterName, domain string, m *WildcardTLSMaterial, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if m == nil {
		return nil // acme mode: nothing to scaffold
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	// PHASE 1 — validate + stage EVERYTHING in memory before touching a file.
	// A failure discovered on the LAST patch target after the first ones were
	// already written would leave a mixed overlay: secrets replaced, platform
	// suppressed, but (say) registry-depot's ACME Certificate still armed —
	// with no completed-mode marker to say so (codex review 2026-07-27,
	// MEDIUM). Staging first makes step 11 all-or-nothing up to the final
	// writes.
	plain := RenderWildcardSecrets(m, domain)
	keyB64 := base64.StdEncoding.EncodeToString(m.KeyPEM)
	secretPath := filepath.Join(clusterDir, wildcardSecretsFileName)
	if fi, err := os.Lstat(secretPath); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("wildcard-tls: %s exists but is not a regular file", secretPath)
		}
		// Rotation re-runs are expected: overwrite via the same safe flow.
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("wildcard-tls: stat %s: %w", secretPath, err)
	}

	type stagedWrite struct {
		path     string
		original []byte // exact bytes phase 1 read — phase 3 refuses to clobber drift
		content  string
	}
	var writes []stagedWrite
	stage := func(path string, patch func([]string) ([]string, bool, error)) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		patched, changed, err := patch(strings.Split(string(body), "\n"))
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if changed {
			writes = append(writes, stagedWrite{path, body, strings.Join(patched, "\n")})
		}
		return nil
	}

	if err := stage(filepath.Join(clusterDir, "kustomization.yaml"),
		patchKustomizationResource(wildcardSecretsFileName)); err != nil {
		return fmt.Errorf("wildcard-tls: kustomization.yaml: %w", err)
	}
	patches := RenderCertificateSuppressionPatches()
	skipped := []string{}
	for _, stem := range []string{"platform", "registry-depot", "infra-object-storage"} {
		entries, ok := patches[stem]
		if !ok {
			continue
		}
		path := filepath.Join(clusterDir, stem+".yaml")
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("wildcard-tls: stat %s: %w", path, err)
			}
			if stem == "platform" {
				// platform.yaml is written unconditionally by add-cluster.sh;
				// its absence means the scaffold shape changed under us.
				return fmt.Errorf("wildcard-tls: %s does not exist — cannot suppress the platform ACME Certificates", path)
			}
			skipped = append(skipped, stem)
			continue
		}
		if err := stage(path, patchKustomizationSuppression(stem, entries)); err != nil {
			return fmt.Errorf("wildcard-tls: %w", err)
		}
	}
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return fmt.Errorf("wildcard-tls: %w", err)
	}

	// PHASE 2 — the SOPS artifact. Skipped when the existing ciphertext
	// decrypts to EXACTLY this plaintext: sops output is randomized per run,
	// so an unconditional rewrite turns a no-change re-run into a tracked
	// diff that reads like a rotation (codex review 2026-07-27, MEDIUM).
	if !sopsFileDecryptsTo(fleetRepo, secretPath, plain, []string{keyB64}) {
		if err := sopsEncryptToFile(fleetRepo, secretPath, plain, []string{keyB64}); err != nil {
			return fmt.Errorf("wildcard-tls: %w", err)
		}
	}

	// PHASE 3 — the staged plain-file writes (already validated). Each target
	// is re-read first: the SOPS phase shells out and can be slow, and a
	// cooperating writer that changed a target meanwhile must surface as an
	// error, not be silently overwritten with phase-1-derived content (codex
	// pass-5, MEDIUM).
	for _, w := range writes {
		current, err := os.ReadFile(w.path)
		if err != nil {
			return fmt.Errorf("wildcard-tls: re-read %s: %w", w.path, err)
		}
		if string(current) != string(w.original) {
			return fmt.Errorf("wildcard-tls: %s changed while init was running — re-run init against the current state", filepath.Base(w.path))
		}
		if err := os.WriteFile(w.path, []byte(w.content), 0o644); err != nil {
			return fmt.Errorf("wildcard-tls: write %s: %w", w.path, err)
		}
	}
	for _, stem := range skipped {
		fmt.Fprintf(out, "[scaffold] wildcard-tls: %s.yaml absent — its ACME Certificates are not scaffolded either; if you add that layer later, re-run init or add the suppression patch by hand (docs/platform/certificates.md)\n", stem)
	}
	env.Set("TLS_MODE", TLSModeBYOWildcard)
	if err := env.Write(""); err != nil {
		return fmt.Errorf("wildcard-tls: record TLS_MODE: %w", err)
	}

	fmt.Fprintf(out, "[scaffold] byo-wildcard TLS wired: %d platform secrets (SOPS), ACME Certificates suppressed; subject=%s notAfter=%s — nothing renews this for you (docs/platform/certificates.md)\n",
		len(wildcardSecretTargets()), m.Subject, m.NotAfter.Format("2006-01-02"))
	return nil
}

// sopsFileDecryptsTo reports whether path exists, decrypts to exactly
// plaintext, AND its raw ciphertext passes the same checks a fresh encrypt
// must pass (sops metadata present, no mustNotLeak string in cleartext).
// Decrypt-equality alone is NOT sufficient to keep a file: a document written
// under a mis-scoped encrypted_regex can decrypt correctly while carrying the
// private key unencrypted, and the fast path would then preserve the exposure
// (codex pass-5, HIGH). Any failure returns false, which makes the caller
// RE-ENCRYPT: fail toward rewriting, never toward keeping suspect ciphertext.
func sopsFileDecryptsTo(fleetRepo, path, plaintext string, mustNotLeak []string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if !strings.Contains(string(raw), "sops") {
		return false
	}
	for _, leak := range mustNotLeak {
		if leak != "" && strings.Contains(string(raw), leak) {
			return false
		}
	}
	cmd := exec.Command("sops", "--decrypt", path)
	cmd.Dir = fleetRepo
	outB, err := cmd.Output()
	if err != nil {
		return false
	}
	return string(outB) == plaintext
}

// patchKustomizationSuppression appends the suppression entries to the
// Kustomization file's spec.patches block, creating the block when absent.
//
// Appending at EOF is the same structural assumption every other kube-dc
// patch writer makes (natgate, object-storage): the generated Flux
// Kustomization files end inside spec, so a trailing `  patches:` list is
// valid. Idempotence is by marker; composition with the OTHER owned patch
// writers is by the shared ownedPlatformPatchMarkers acceptance list.
func patchKustomizationSuppression(stem, entries string) func([]string) ([]string, bool, error) {
	return func(lines []string) ([]string, bool, error) {
		hasPatchesKey := false
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if strings.Contains(t, byoWildcardTLSMarker) {
				return lines, false, nil // already wired
			}
			if t == "patches:" {
				hasPatchesKey = true
			}
		}
		if hasPatchesKey && stem == "platform" {
			// platform.yaml patches: may be written by several kube-dc writers;
			// compose only with a block that carries a known marker, exactly as
			// natgate does. registry-depot / infra-object-storage are generated
			// without patches:, so an existing key there is also hand-edited.
			ours := false
			for _, l := range lines {
				t := strings.TrimSpace(l)
				for _, m := range ownedPlatformPatchMarkers {
					if t == m || strings.Contains(t, m) {
						ours = true
						break
					}
				}
				if ours {
					break
				}
			}
			if !ours {
				return nil, false, fmt.Errorf("platform.yaml already has a patches: block (hand-edited?) — add the ACME-suppression patches manually (docs/platform/certificates.md, marker: %q)", byoWildcardTLSMarker)
			}
		} else if hasPatchesKey {
			return nil, false, fmt.Errorf("%s.yaml already has a patches: block (hand-edited?) — add the ACME-suppression patches manually (docs/platform/certificates.md)", stem)
		}

		end := len(lines)
		for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		block := "    # --- " + byoWildcardTLSMarker + " ---\n" +
			"    # cert-manager treats an existing Secret it did not issue as material\n" +
			"    # to REPLACE; deleting the Certificates from the render is what makes\n" +
			"    # the operator wildcard authoritative. Generated by kube-dc bootstrap\n" +
			"    # init --tls-mode byo-wildcard; see docs/platform/certificates.md.\n" +
			strings.TrimRight(entries, "\n")
		if !hasPatchesKey {
			block = "  patches:\n" + block
		}
		outLines := make([]string, 0, end+40)
		outLines = append(outLines, lines[:end]...)
		outLines = append(outLines, strings.Split(block, "\n")...)
		outLines = append(outLines, "")
		return outLines, true, nil
	}
}

// canonicalTLSMode maps the default spelling to the canonical empty value so
// the plan hash is stable across "--tls-mode acme", "--tls-mode", and no flag.
func canonicalTLSMode(mode string) string {
	if mode == TLSModeACME {
		return ""
	}
	return mode
}

// ValidateTLSMode rejects an unknown mode and the missing-material case.
func ValidateTLSMode(mode, certPath, keyPath string) error {
	switch mode {
	case "", TLSModeACME:
		if certPath != "" || keyPath != "" {
			return fmt.Errorf("--tls-cert/--tls-key are only valid with --tls-mode=%s", TLSModeBYOWildcard)
		}
		return nil
	case TLSModeBYOWildcard:
		if certPath == "" || keyPath == "" {
			return fmt.Errorf("--tls-mode=%s requires --tls-cert and --tls-key", TLSModeBYOWildcard)
		}
		return nil
	default:
		return fmt.Errorf("invalid --tls-mode %q (valid: %s, %s)", mode, TLSModeACME, TLSModeBYOWildcard)
	}
}
