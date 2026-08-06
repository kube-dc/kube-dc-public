package clusterinit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// ACME DNS-01 via Route53 (TLS_MODE=acme-dns01-route53).
//
// The default ACME path solves HTTP-01 through the Envoy Gateway, which
// requires Let's Encrypt to REACH the platform on :80 — impossible when
// NODE_EXTERNAL_IP is a private address (internal DC, VPN-only pilot). The
// byo-wildcard mode covers that but trades away renewal: someone must
// re-issue and re-commit the certificate every ~60 days.
//
// This mode keeps cert-manager as the issuer and swaps only HOW it proves
// domain control: the platform ClusterIssuer's solvers are patched to
// dns01/route53, and the AWS secret access key rides a SOPS-encrypted
// Secret next to it. Auto-renewal survives; nothing about the platform
// Certificates changes (they all reference the issuer by NAME).
//
// The CLI itself never talks to AWS: it validates shapes locally and
// scaffolds fleet files. cert-manager, in-cluster, is the only component
// that ever holds or uses the credentials — which is exactly the trust
// envelope the rest of the GitOps repo already lives in.
//
// The issuer keeps its historical name `letsencrypt-prod-http` even though
// it no longer solves HTTP-01: every platform Certificate hard-references
// that name, and renaming would mean patching them all in lockstep.

// TLSModeACMEDNS01Route53 solves ACME via Route53 DNS-01 records.
const TLSModeACMEDNS01Route53 = "acme-dns01-route53"

const (
	dns01SecretName      = "route53-dns01-credentials"
	dns01SecretNamespace = "cert-manager"
	dns01SecretKeyField  = "secret-access-key"
	// dns01SecretsFileName must match .sops.yaml's \.enc\.yaml$ creation rule.
	dns01SecretsFileName = "dns01-route53-credentials.enc.yaml"
	dns01DefaultRegion   = "us-east-1"
	// dns01ClusterIssuerName is the platform issuer every Certificate
	// references; see the package comment for why the name is historical.
	dns01ClusterIssuerName = "letsencrypt-prod-http"
)

// dns01Route53Marker identifies the solver patch this writer emits. Present in
// the patched file so re-runs are idempotent, and listed in
// ownedPlatformPatchMarkers so the other platform.yaml patch writers compose
// with it instead of aborting on an "unexpected" patches: block.
const dns01Route53Marker = "TLS_MODE=acme-dns01-route53: ClusterIssuer solves via Route53 DNS-01"

// KubeDCDNS01SecretKeyEnv is the environment variable consulted when
// --dns01-route53-secret-key-file is not given. An env var (or a file) keeps
// the secret out of argv, shell history and the prefill config format.
//
// Deliberately OUTSIDE the KUBE_DC_INIT_ namespace: the generic env-prefill
// scanner strips that prefix and imports the remainder as a config key, and
// a secret must never enter that flow (codex review 2026-08-06, P0 — the
// scanner would have persisted the raw credential into cluster-config.env).
// The scanner additionally hard-skips SECRET-shaped names as defense in
// depth.
const KubeDCDNS01SecretKeyEnv = "KUBE_DC_DNS01_ROUTE53_SECRET_KEY"

// DNS01Route53Material is validated solver configuration ready to scaffold.
// ZoneID/Region/AccessKeyID are configuration, not secrets — they appear in
// the ClusterIssuer patch in cleartext (an AWS access key ID identifies a
// credential; only the secret half authenticates). SecretKey is the secret
// half and only ever lands SOPS-encrypted.
type DNS01Route53Material struct {
	ZoneID      string
	Region      string
	AccessKeyID string
	SecretKey   []byte
	// Fingerprint is the hex SHA-256 of the secret key bytes. It binds the
	// reviewed plan to the credential that ships, exactly as
	// TLSCertFingerprint does for byo-wildcard: without it a different
	// credential could ship at apply under the identical plan hash. An AWS
	// secret access key is 40 chars of high-entropy material, so the digest
	// reveals nothing recoverable.
	Fingerprint string
}

var (
	dns01ZoneIDRe = regexp.MustCompile(`^Z[0-9A-Z]{4,31}$`)
	dns01RegionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-\d$`)
	dns01AccessRe = regexp.MustCompile(`^[A-Z0-9]{16,32}$`)
)

// LoadDNS01Route53 validates the operator's solver inputs. secretKeyEnv is the
// raw value of KubeDCDNS01SecretKeyEnv (passed in, not read here, so tests
// and the TUI stay hermetic). Every check maps to a runtime failure that is
// otherwise silent or misleading: a swapped key pair or wrong zone yields
// nothing but Challenge resources stuck in "pending" with an opaque AWS error
// deep in the cert-manager logs.
func LoadDNS01Route53(zoneID, region, accessKeyID, secretKeyFile, secretKeyEnv string) (*DNS01Route53Material, error) {
	zoneID = strings.TrimSpace(zoneID)
	region = strings.TrimSpace(region)
	accessKeyID = strings.TrimSpace(accessKeyID)
	if zoneID == "" || accessKeyID == "" {
		return nil, fmt.Errorf("--tls-mode=%s requires --dns01-route53-zone-id and --dns01-route53-access-key-id", TLSModeACMEDNS01Route53)
	}
	if !dns01ZoneIDRe.MatchString(zoneID) {
		return nil, fmt.Errorf("--dns01-route53-zone-id %q does not look like a Route53 hosted-zone ID (Z…, uppercase alphanumeric — the ID, not the zone NAME)", zoneID)
	}
	if region == "" {
		region = dns01DefaultRegion
	}
	if !dns01RegionRe.MatchString(region) {
		return nil, fmt.Errorf("--dns01-route53-region %q does not look like an AWS region (e.g. %s)", region, dns01DefaultRegion)
	}
	if strings.HasPrefix(accessKeyID, "ASIA") {
		// Temporary STS credentials also need a session token, which the
		// cert-manager route53 solver's static-secret shape cannot carry —
		// the resulting failure is a slow drip of SignatureDoesNotMatch.
		return nil, fmt.Errorf("--dns01-route53-access-key-id is a temporary STS key (ASIA…) — cert-manager needs a static IAM access key (AKIA…)")
	}
	if !dns01AccessRe.MatchString(accessKeyID) {
		return nil, fmt.Errorf("--dns01-route53-access-key-id %q does not look like an AWS access key ID", accessKeyID)
	}

	var raw []byte
	switch {
	case secretKeyFile != "":
		b, err := os.ReadFile(secretKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read --dns01-route53-secret-key-file %s: %w", secretKeyFile, err)
		}
		raw = b
	case secretKeyEnv != "":
		raw = []byte(secretKeyEnv)
	default:
		return nil, fmt.Errorf("--tls-mode=%s needs the AWS secret access key: --dns01-route53-secret-key-file <path> or %s in the environment",
			TLSModeACMEDNS01Route53, KubeDCDNS01SecretKeyEnv)
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) < 30 {
		return nil, fmt.Errorf("the supplied AWS secret access key is too short (%d chars; a real key is 40) — wrong file?", len(secret))
	}
	if strings.HasPrefix(secret, "AKIA") {
		// No real secret access key starts with the access-key-ID prefix;
		// this is the two values swapped or the ID pasted twice.
		return nil, fmt.Errorf("the supplied secret access key looks like an access key ID (AKIA…) — the two are swapped")
	}

	sum := sha256.Sum256([]byte(secret))
	return &DNS01Route53Material{
		ZoneID:      zoneID,
		Region:      region,
		AccessKeyID: accessKeyID,
		SecretKey:   []byte(secret),
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// RenderDNS01Secret produces the PLAINTEXT Secret manifest. The caller
// encrypts it with SOPS before it is ever committed.
func RenderDNS01Secret(m *DNS01Route53Material) string {
	var b strings.Builder
	b.WriteString("# AWS secret access key for the Route53 DNS-01 solver.\n")
	b.WriteString("# TLS_MODE=acme-dns01-route53. Generated by `kube-dc bootstrap init`.\n")
	b.WriteString("#\n")
	b.WriteString("# Consumed by cert-manager via the " + dns01ClusterIssuerName + "\n")
	b.WriteString("# ClusterIssuer's secretAccessKeySecretRef. Scope the IAM user to the one\n")
	b.WriteString("# zone: route53:ChangeResourceRecordSets + ListResourceRecordSets on it,\n")
	b.WriteString("# route53:GetChange on *. Rotation: replace the key in AWS, re-run init\n")
	b.WriteString("# with the new material (or `sops edit` this file), commit.\n")
	b.WriteString("# Access key ID (identifies the credential): " + m.AccessKeyID + "\n")
	b.WriteString("---\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("    name: " + dns01SecretName + "\n")
	b.WriteString("    namespace: " + dns01SecretNamespace + "\n")
	b.WriteString("    labels:\n")
	b.WriteString("        kube-dc.com/acme-dns01: \"route53\"\n")
	b.WriteString("type: Opaque\n")
	b.WriteString("data:\n")
	b.WriteString("    " + dns01SecretKeyField + ": " + base64.StdEncoding.EncodeToString(m.SecretKey) + "\n")
	return b.String()
}

// RenderDNS01IssuerPatch returns the platform.yaml spec.patches entry that
// swaps the ClusterIssuer's solvers to dns01/route53. A strategic-merge patch
// on a list REPLACES it wholesale, which is exactly right here: the http01
// solver must not remain as a sibling (cert-manager would keep choosing it —
// solvers are matched in order and the http01 entry has no selector).
func RenderDNS01IssuerPatch(m *DNS01Route53Material) string {
	var b strings.Builder
	b.WriteString("    - target:\n")
	b.WriteString("        group: cert-manager.io\n")
	b.WriteString("        version: v1\n")
	b.WriteString("        kind: ClusterIssuer\n")
	b.WriteString("        name: " + dns01ClusterIssuerName + "\n")
	b.WriteString("      patch: |\n")
	b.WriteString("        apiVersion: cert-manager.io/v1\n")
	b.WriteString("        kind: ClusterIssuer\n")
	b.WriteString("        metadata:\n")
	b.WriteString("          name: " + dns01ClusterIssuerName + "\n")
	b.WriteString("        spec:\n")
	b.WriteString("          acme:\n")
	b.WriteString("            solvers:\n")
	b.WriteString("              - dns01:\n")
	b.WriteString("                  route53:\n")
	b.WriteString("                    region: " + m.Region + "\n")
	b.WriteString("                    hostedZoneID: " + m.ZoneID + "\n")
	b.WriteString("                    accessKeyID: " + m.AccessKeyID + "\n")
	b.WriteString("                    secretAccessKeySecretRef:\n")
	b.WriteString("                      name: " + dns01SecretName + "\n")
	b.WriteString("                      key: " + dns01SecretKeyField + "\n")
	return b.String()
}

// WriteDNS01Route53 scaffolds the whole acme-dns01-route53 shape for one
// cluster, mirroring WriteWildcardTLS's staged all-or-nothing flow:
//
//  1. clusters/<name>/dns01-route53-credentials.enc.yaml — the secret access
//     key, SOPS-encrypted via the hardened temp-file flow;
//  2. that file added to the cluster kustomization's resources;
//  3. the ClusterIssuer solver patch appended to platform.yaml (the Flux
//     Kustomization that renders platform/certificates/);
//  4. TLS_MODE + the non-secret solver config recorded in cluster-config.env.
//
// The secret key itself never appears in logs — only the access key ID.
func WriteDNS01Route53(fleetRepo, clusterName string, m *DNS01Route53Material, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if m == nil {
		return nil // other TLS modes: nothing to scaffold
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	// PHASE 1 — validate + stage everything in memory before touching a file
	// (same rationale as WriteWildcardTLS: no partially-wired overlay).
	plain := RenderDNS01Secret(m)
	secretB64 := base64.StdEncoding.EncodeToString(m.SecretKey)
	secretPath := filepath.Join(clusterDir, dns01SecretsFileName)
	if fi, err := os.Lstat(secretPath); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("dns01-route53: %s exists but is not a regular file", secretPath)
		}
		// Rotation re-runs are expected: overwrite via the same safe flow.
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("dns01-route53: stat %s: %w", secretPath, err)
	}

	type stagedWrite struct {
		path     string
		original []byte
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
		patchKustomizationResource(dns01SecretsFileName)); err != nil {
		return fmt.Errorf("dns01-route53: kustomization.yaml: %w", err)
	}
	platformPath := filepath.Join(clusterDir, "platform.yaml")
	if _, err := os.Stat(platformPath); err != nil {
		// platform.yaml is written unconditionally by add-cluster.sh; its
		// absence means the scaffold shape changed under us.
		return fmt.Errorf("dns01-route53: %s does not exist — cannot patch the ClusterIssuer solvers", platformPath)
	}
	if err := stage(platformPath, patchPlatformDNS01Solver(RenderDNS01IssuerPatch(m))); err != nil {
		return fmt.Errorf("dns01-route53: %w", err)
	}
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return fmt.Errorf("dns01-route53: %w", err)
	}

	// PHASE 2 — the SOPS artifact, skipped when the existing ciphertext
	// already decrypts to exactly this plaintext (sops output is randomized
	// per run; an unconditional rewrite turns a no-change re-run into a diff
	// that reads like a rotation).
	if !sopsFileDecryptsTo(fleetRepo, secretPath, plain, []string{string(m.SecretKey), secretB64}) {
		if err := sopsEncryptToFile(fleetRepo, secretPath, plain, []string{string(m.SecretKey), secretB64}); err != nil {
			return fmt.Errorf("dns01-route53: %w", err)
		}
	}

	// PHASE 3 — the staged plain-file writes, re-read first so a cooperating
	// writer that changed a target during the SOPS shell-out surfaces as an
	// error instead of being silently overwritten.
	for _, w := range writes {
		current, err := os.ReadFile(w.path)
		if err != nil {
			return fmt.Errorf("dns01-route53: re-read %s: %w", w.path, err)
		}
		if string(current) != string(w.original) {
			return fmt.Errorf("dns01-route53: %s changed while init was running — re-run init against the current state", filepath.Base(w.path))
		}
		if err := os.WriteFile(w.path, []byte(w.content), 0o644); err != nil {
			return fmt.Errorf("dns01-route53: write %s: %w", w.path, err)
		}
	}
	env.Set("TLS_MODE", TLSModeACMEDNS01Route53)
	env.Set("DNS01_ROUTE53_ZONE_ID", m.ZoneID)
	env.Set("DNS01_ROUTE53_REGION", m.Region)
	env.Set("DNS01_ROUTE53_ACCESS_KEY_ID", m.AccessKeyID)
	if err := env.Write(""); err != nil {
		return fmt.Errorf("dns01-route53: record TLS_MODE: %w", err)
	}

	fmt.Fprintf(out, "[scaffold] acme-dns01-route53 TLS wired: ClusterIssuer %s → Route53 DNS-01 (zone %s, key %s, SOPS secret); cert-manager renews automatically — Let's Encrypt never needs to reach the cluster\n",
		dns01ClusterIssuerName, m.ZoneID, m.AccessKeyID)
	return nil
}

// validateDNS01Flags enforces flag coherence at Validate time (before any
// material is read): the dns01 trio belongs to its mode, and the mode needs
// at least the non-secret trio to be plannable. The secret key itself is
// checked by LoadDNS01Route53 in the cmd layer — it may legitimately arrive
// only via environment at apply time.
// promotedTLSKeys are owned by dedicated flags/fields; accepting them via
// --set would let the generic Sets export overwrite the dedicated export and
// break ImportMap/ExportMap symmetry (codex pass-2, P2).
var promotedTLSKeys = []string{
	"TLS_MODE", "DNS01_ROUTE53_ZONE_ID", "DNS01_ROUTE53_REGION", "DNS01_ROUTE53_ACCESS_KEY_ID",
}

func validateDNS01Flags(o *InitOptions) error {
	for _, k := range promotedTLSKeys {
		if _, ok := o.Sets[k]; ok {
			return fmt.Errorf("--set %s is not accepted — use --tls-mode / --dns01-route53-* flags (dedicated, validated fields)", k)
		}
	}
	dns01 := canonicalTLSMode(o.TLSMode) == TLSModeACMEDNS01Route53
	anySet := o.DNS01Route53ZoneID != "" || o.DNS01Route53Region != "" ||
		o.DNS01Route53AccessKeyID != "" || o.DNS01Route53SecretKeyFile != ""
	if !dns01 {
		if anySet {
			return fmt.Errorf("--dns01-route53-* flags are only valid with --tls-mode=%s", TLSModeACMEDNS01Route53)
		}
		return nil
	}
	if o.DNS01Route53ZoneID == "" || o.DNS01Route53AccessKeyID == "" {
		return fmt.Errorf("--tls-mode=%s requires --dns01-route53-zone-id and --dns01-route53-access-key-id", TLSModeACMEDNS01Route53)
	}
	return nil
}

// patchPlatformDNS01Solver appends the solver patch to platform.yaml's
// spec.patches block, creating the block when absent. Same structural
// assumptions and composition rules as the byo-wildcard suppression writer:
// composition with the other kube-dc writers by the shared
// ownedPlatformPatchMarkers acceptance list, refusal on a hand-edited
// patches: block.
//
// Idempotence is by CONTENT, not by marker presence: unlike the suppression
// block (whose body never varies), this block embeds zone/region/access-key
// values that legitimately change on rotation. A marker-only check made a
// re-run with new material update the SOPS secret and env while platform.yaml
// silently kept the OLD access-key ID — split-brain that breaks AWS auth
// (codex review 2026-08-06, P1). When the marker is found, the owned block is
// located structurally and replaced in place if it differs.
func patchPlatformDNS01Solver(entries string) func([]string) ([]string, bool, error) {
	return func(lines []string) ([]string, bool, error) {
		hasPatchesKey := false
		markerAt := -1
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if markerAt < 0 && strings.Contains(t, dns01Route53Marker) {
				markerAt = i
			}
			if t == "patches:" {
				hasPatchesKey = true
			}
		}
		newBlock := strings.Split(dns01BlockBody(entries), "\n")
		if markerAt >= 0 {
			end, err := dns01OwnedBlockEnd(lines, markerAt)
			if err != nil {
				return nil, false, err
			}
			if end-markerAt == len(newBlock) && strings.Join(lines[markerAt:end], "\n") == strings.Join(newBlock, "\n") {
				return lines, false, nil // already wired with THIS config
			}
			outLines := make([]string, 0, len(lines)+len(newBlock))
			outLines = append(outLines, lines[:markerAt]...)
			outLines = append(outLines, newBlock...)
			outLines = append(outLines, lines[end:]...)
			return outLines, true, nil
		}
		if hasPatchesKey {
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
				return nil, false, fmt.Errorf("platform.yaml already has a patches: block (hand-edited?) — add the DNS-01 solver patch manually (docs/platform/certificates.md, marker: %q)", dns01Route53Marker)
			}
		}

		end := len(lines)
		for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		block := dns01BlockBody(entries)
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

// dns01BlockBody renders the owned block: marker header + the solver patch
// entry, WITHOUT the "  patches:" key. One renderer for both the append and
// the in-place-replace paths so content comparison is exact.
func dns01BlockBody(entries string) string {
	return "    # --- " + dns01Route53Marker + " ---\n" +
		"    # The default issuer solves HTTP-01 through the Gateway, which requires\n" +
		"    # Let's Encrypt to reach this cluster on :80 — impossible on a private\n" +
		"    # NODE_EXTERNAL_IP. DNS-01 proves control via Route53 records instead;\n" +
		"    # the secret access key lives in " + dns01SecretsFileName + ".\n" +
		"    # Generated by kube-dc bootstrap init --tls-mode acme-dns01-route53.\n" +
		strings.TrimRight(entries, "\n")
}

// dns01OwnedBlockEnd locates the end (exclusive line index) of the owned
// block that starts at the marker header line. The block is: consecutive
// "    #" comment lines, then exactly ONE "    - " list entry with all its
// deeper-indented lines. Anything structurally different means a human
// rearranged the file — refuse rather than guess.
func dns01OwnedBlockEnd(lines []string, markerAt int) (int, error) {
	j := markerAt
	for j < len(lines) && strings.HasPrefix(lines[j], "    #") {
		j++
	}
	if j >= len(lines) || !strings.HasPrefix(lines[j], "    - ") {
		return 0, fmt.Errorf("platform.yaml: the DNS-01 marker is present but its patch entry is not where the writer left it — fix the block manually (marker: %q)", dns01Route53Marker)
	}
	j++ // past the "    - target:" line
	// Deeper-indented, non-blank lines belong to our entry; the block ends at
	// the first sibling entry ("    - "), another writer's comment ("    #"),
	// a blank, or any dedent.
	for j < len(lines) && strings.HasPrefix(lines[j], "      ") && strings.TrimSpace(lines[j]) != "" {
		j++
	}
	return j, nil
}
