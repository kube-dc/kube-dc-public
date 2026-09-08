package clusterinit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// ACME DNS-01 via Cloudflare — the second DNS-01 provider on the rails
// dns01.go laid down for Route53. Same trust envelope: the CLI validates
// shapes locally and scaffolds fleet files; cert-manager, in-cluster, is the
// only component that ever holds or uses the API token (SOPS-encrypted Secret
// in cert-manager's namespace, referenced by the ClusterIssuer's
// apiTokenSecretRef).
//
// Two scopes, chosen by --tls-mode:
//
//   - TLS_MODE=acme-dns01-cloudflare — EVERY platform certificate proves
//     control via Cloudflare DNS records. For private/VPN-only clusters, where
//     Let's Encrypt can never reach :80 for HTTP-01. The http01 solver is
//     dropped (it would otherwise keep winning: solvers match in order and it
//     carries no selector).
//   - TLS_MODE=acme + --dns01-cloudflare-zone — public cluster, HTTP-01 keeps
//     issuing every per-host certificate; the Cloudflare solver is added with
//     a selector for the platform WILDCARD names only ("*.<domain>",
//     "<domain>"), which HTTP-01 can never validate. This is what makes
//     `wildcard-tls` issue on a public cluster and is the shape the per-org
//     wildcard listeners need.
//
// Unlike Route53 there is no zone ID / region / access key ID: cert-manager
// resolves the zone through the token. The optional zone name becomes a
// solver selector (dnsZones) in full scope so the token can never be asked to
// solve a name outside the zone it was scoped to, and it is REQUIRED in
// wildcard scope, where it is the explicit, non-secret opt-in (the token
// alone may legitimately sit in an operator's environment for other reasons
// and must not silently turn a plain `acme` run into a hybrid one).
//
// Token scope to prescribe: Zone → DNS → Edit and Zone → Zone → Read,
// restricted to the one zone. The legacy account-wide Global API Key is
// refused outright.

// TLSModeACMEDNS01Cloudflare solves ACME via Cloudflare DNS-01 records for
// every platform certificate.
const TLSModeACMEDNS01Cloudflare = "acme-dns01-cloudflare"

const (
	dns01CFSecretName     = "cloudflare-dns01-credentials"
	dns01CFSecretKeyField = "api-token"
	// dns01CFSecretsFileName must match .sops.yaml's \.enc\.yaml$ creation rule.
	dns01CFSecretsFileName = "dns01-cloudflare-credentials.enc.yaml"

	// DNS01CloudflareScopeAll: the Cloudflare solver is the ONLY solver.
	DNS01CloudflareScopeAll = "all"
	// DNS01CloudflareScopeWildcard: the Cloudflare solver handles the platform
	// wildcard names; HTTP-01 stays for everything else.
	DNS01CloudflareScopeWildcard = "wildcard"
)

// dns01CloudflareMarker identifies the solver patch this writer emits (see
// dns01Route53Marker for the contract: present in the patched file for
// idempotence, listed in ownedPlatformPatchMarkers for composition).
const dns01CloudflareMarker = "DNS-01 cloudflare: ClusterIssuer solves via Cloudflare DNS-01"

// KubeDCDNS01CloudflareTokenEnv is the environment variable consulted when
// --dns01-cloudflare-api-token-file is not given. Deliberately OUTSIDE the
// KUBE_DC_INIT_ namespace for the same reason as KubeDCDNS01SecretKeyEnv: the
// env-prefill scanner must never import a secret as a config key.
const KubeDCDNS01CloudflareTokenEnv = "KUBE_DC_DNS01_CLOUDFLARE_API_TOKEN"

// DNS01CloudflareMaterial is validated solver configuration ready to scaffold.
// Zone and Scope are configuration (they appear in the ClusterIssuer patch and
// cluster-config.env in cleartext); Token is the secret and only ever lands
// SOPS-encrypted. Fingerprint (hex SHA-256 of the token) binds the reviewed
// plan to the exact credential that ships.
type DNS01CloudflareMaterial struct {
	Zone        string
	Scope       string
	Token       []byte
	Fingerprint string
}

var (
	// Scoped API tokens are 40 chars of [A-Za-z0-9_-]; newer tokens may carry
	// a short prefix. Anything in that alphabet from 40 chars up is accepted.
	dns01CFTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{40,128}$`)
	// The legacy Global API Key is exactly 37 lowercase hex chars — and is
	// account-wide, which is precisely what we refuse to put in a cluster.
	dns01CFGlobalKeyRe = regexp.MustCompile(`^[0-9a-f]{37}$`)
	dns01CFZoneRe      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// DNS01CloudflareScopeForMode maps the TLS mode to the solver scope.
func DNS01CloudflareScopeForMode(mode string) string {
	if canonicalTLSMode(mode) == TLSModeACMEDNS01Cloudflare {
		return DNS01CloudflareScopeAll
	}
	return DNS01CloudflareScopeWildcard
}

// LoadDNS01Cloudflare validates the operator's solver inputs. It returns
// (nil, nil) when Cloudflare DNS-01 is not requested at all: the mode is not
// acme-dns01-cloudflare and neither --dns01-cloudflare-zone nor
// --dns01-cloudflare-api-token-file was given. tokenEnv is the raw value of
// KubeDCDNS01CloudflareTokenEnv (passed in, not read here, so tests and the
// TUI stay hermetic). domain is the cluster domain, used to check that the
// zone actually contains it — a token for the wrong zone yields nothing but
// Challenge resources stuck in "pending" with an opaque API error deep in the
// cert-manager logs.
func LoadDNS01Cloudflare(mode, domain, zone, tokenFile, tokenEnv string) (*DNS01CloudflareMaterial, error) {
	mode = canonicalTLSMode(strings.TrimSpace(mode))
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	full := mode == TLSModeACMEDNS01Cloudflare
	if !full && zone == "" && tokenFile == "" {
		return nil, nil // not requested
	}
	if !full && mode != "" {
		return nil, fmt.Errorf("--dns01-cloudflare-* flags are only valid with --tls-mode=%s (every certificate via Cloudflare DNS-01) or --tls-mode=%s (wildcards only, HTTP-01 stays for the rest)",
			TLSModeACMEDNS01Cloudflare, TLSModeACME)
	}
	scope := DNS01CloudflareScopeForMode(mode)
	if scope == DNS01CloudflareScopeWildcard && zone == "" {
		return nil, fmt.Errorf("Cloudflare DNS-01 for wildcards (--tls-mode=%s) needs --dns01-cloudflare-zone <apex zone the token is scoped to>", TLSModeACME)
	}
	if zone != "" {
		if !dns01CFZoneRe.MatchString(zone) {
			return nil, fmt.Errorf("--dns01-cloudflare-zone %q does not look like a DNS zone name (the apex, e.g. example.org — not a hostname with a scheme or path)", zone)
		}
		d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if d != "" && d != zone && !strings.HasSuffix(d, "."+zone) {
			return nil, fmt.Errorf("--dns01-cloudflare-zone %q does not contain the cluster domain %q — the solver could never write the challenge records", zone, d)
		}
	}

	var raw string
	fromFile := tokenFile != ""
	switch {
	case tokenFile != "":
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			// Never echo the path: an operator who pastes the TOKEN where the
			// path belongs would otherwise see it in stderr / CI logs
			// (codex review 2026-08-31, P2).
			return nil, fmt.Errorf("read --dns01-cloudflare-api-token-file: %w (path withheld)", sanitizedReadErr(err))
		}
		raw = string(b)
	case tokenEnv != "":
		raw = tokenEnv
	default:
		return nil, fmt.Errorf("Cloudflare DNS-01 needs the API token: --dns01-cloudflare-api-token-file <path> or %s in the environment", KubeDCDNS01CloudflareTokenEnv)
	}
	// Exactly one line. A FILE may end in a single LF or CRLF (every editor
	// adds one); anything else — a leading blank line, several trailing
	// ones, a lone CR — is a pasted file, not a token file. An ENVIRONMENT
	// value has no reason to carry any line ending at all. Checked BEFORE
	// trimming so TrimSpace cannot hide it (codex review 2026-08-31, P3).
	if fromFile {
		switch {
		case strings.HasSuffix(raw, "\r\n"):
			raw = raw[:len(raw)-2]
		case strings.HasSuffix(raw, "\n"):
			raw = raw[:len(raw)-1]
		}
	}
	if strings.ContainsAny(raw, "\r\n") {
		if fromFile {
			return nil, fmt.Errorf("the supplied Cloudflare token file has more than one line — it must contain only the token")
		}
		return nil, fmt.Errorf("%s contains a line break — it must hold only the token", KubeDCDNS01CloudflareTokenEnv)
	}
	token := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	if strings.Contains(token, "@") {
		return nil, fmt.Errorf("the supplied Cloudflare credential looks like an email address, not an API token")
	}
	if dns01CFGlobalKeyRe.MatchString(token) {
		return nil, fmt.Errorf("the supplied Cloudflare credential is the legacy Global API Key (37 hex chars) — it is ACCOUNT-WIDE and is refused; create a scoped API token (Zone:DNS:Edit + Zone:Zone:Read on the one zone)")
	}
	if len(token) < 40 {
		return nil, fmt.Errorf("the supplied Cloudflare API token is too short (%d chars; a real token is 40) — wrong file?", len(token))
	}
	if !dns01CFTokenRe.MatchString(token) {
		return nil, fmt.Errorf("the supplied Cloudflare API token does not look like one (expected 40+ chars of letters, digits, '_' or '-')")
	}

	sum := sha256.Sum256([]byte(token))
	return &DNS01CloudflareMaterial{
		Zone:        zone,
		Scope:       scope,
		Token:       []byte(token),
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// sanitizedReadErr strips the path from a file-read error so a credential
// pasted in place of a path can never surface through the error text.
func sanitizedReadErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return errors.New("read failed")
}

// RenderDNS01CloudflareSecret produces the PLAINTEXT Secret manifest. The
// caller encrypts it with SOPS before it is ever committed.
func RenderDNS01CloudflareSecret(m *DNS01CloudflareMaterial) string {
	var b strings.Builder
	b.WriteString("# Cloudflare API token for the cert-manager DNS-01 solver.\n")
	b.WriteString("# Generated by `kube-dc bootstrap init` (scope: " + m.Scope + ").\n")
	b.WriteString("#\n")
	b.WriteString("# Consumed by cert-manager via the " + dns01ClusterIssuerName + "\n")
	b.WriteString("# ClusterIssuer's apiTokenSecretRef. Scope the token to the one zone:\n")
	b.WriteString("# Zone:DNS:Edit + Zone:Zone:Read. Rotation: create a new token in\n")
	b.WriteString("# Cloudflare, re-run init with the new material (or `sops edit` this\n")
	b.WriteString("# file), commit, then revoke the old token.\n")
	if m.Zone != "" {
		b.WriteString("# Zone: " + m.Zone + "\n")
	}
	b.WriteString("---\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("    name: " + dns01CFSecretName + "\n")
	b.WriteString("    namespace: " + dns01SecretNamespace + "\n")
	b.WriteString("    labels:\n")
	b.WriteString("        kube-dc.com/acme-dns01: \"cloudflare\"\n")
	b.WriteString("type: Opaque\n")
	b.WriteString("data:\n")
	b.WriteString("    " + dns01CFSecretKeyField + ": " + base64.StdEncoding.EncodeToString(m.Token) + "\n")
	return b.String()
}

// RenderDNS01CloudflareIssuerPatch returns the platform.yaml spec.patches
// entry for the ClusterIssuer. A strategic-merge patch on a list REPLACES it
// wholesale, so the patch carries the complete solvers list for the scope:
// Cloudflare alone (all), or Cloudflare-for-wildcards followed by the
// unchanged http01 solver (wildcard). The http01 entry must stay byte-for-byte
// what platform/certificates/clusterissuer.yaml declares.
func RenderDNS01CloudflareIssuerPatch(m *DNS01CloudflareMaterial, domain string) string {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
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
	switch m.Scope {
	case DNS01CloudflareScopeWildcard:
		b.WriteString("              - selector:\n")
		b.WriteString("                  dnsNames:\n")
		b.WriteString("                    - \"*." + domain + "\"\n")
		b.WriteString("                    - \"" + domain + "\"\n")
		b.WriteString("                dns01:\n")
	default:
		if m.Zone != "" {
			b.WriteString("              - selector:\n")
			b.WriteString("                  dnsZones:\n")
			b.WriteString("                    - \"" + m.Zone + "\"\n")
			b.WriteString("                dns01:\n")
		} else {
			b.WriteString("              - dns01:\n")
		}
	}
	b.WriteString("                  cloudflare:\n")
	b.WriteString("                    apiTokenSecretRef:\n")
	b.WriteString("                      name: " + dns01CFSecretName + "\n")
	b.WriteString("                      key: " + dns01CFSecretKeyField + "\n")
	if m.Scope == DNS01CloudflareScopeWildcard {
		b.WriteString("              - http01:\n")
		b.WriteString("                  gatewayHTTPRoute:\n")
		b.WriteString("                    parentRefs:\n")
		b.WriteString("                      - name: eg\n")
		b.WriteString("                        namespace: envoy-gateway-system\n")
		b.WriteString("                        kind: Gateway\n")
	}
	return b.String()
}

// dns01CloudflareBlockHeader is the comment header of the owned block; it
// names the scope so a wildcard→all switch is a content change the writer
// replaces in place (idempotence is by content, as for Route53).
func dns01CloudflareBlockHeader(m *DNS01CloudflareMaterial) string {
	switch m.Scope {
	case DNS01CloudflareScopeWildcard:
		return "    # HTTP-01 through the Gateway keeps issuing every per-host certificate;\n" +
			"    # the Cloudflare DNS-01 solver below handles ONLY the platform wildcard\n" +
			"    # names, which HTTP-01 can never validate. Token: " + dns01CFSecretsFileName + ".\n" +
			"    # Generated by kube-dc bootstrap init --tls-mode acme --dns01-cloudflare-zone.\n"
	default:
		return "    # The default issuer solves HTTP-01 through the Gateway, which requires\n" +
			"    # Let's Encrypt to reach this cluster on :80 — impossible on a private\n" +
			"    # NODE_EXTERNAL_IP. DNS-01 proves control via Cloudflare records instead;\n" +
			"    # the API token lives in " + dns01CFSecretsFileName + ".\n" +
			"    # Generated by kube-dc bootstrap init --tls-mode acme-dns01-cloudflare.\n"
	}
}

// WriteDNS01Cloudflare scaffolds the Cloudflare DNS-01 shape for one cluster,
// mirroring WriteDNS01Route53's staged all-or-nothing flow:
//
//  1. clusters/<name>/dns01-cloudflare-credentials.enc.yaml — the API token,
//     SOPS-encrypted via the hardened temp-file flow;
//  2. that file added to the cluster kustomization's resources;
//  3. the ClusterIssuer solver patch appended to (or replaced in) platform.yaml;
//  4. TLS_MODE + DNS01_CLOUDFLARE_ZONE/SCOPE recorded in cluster-config.env.
//
// A platform.yaml that already carries the Route53 solver block is refused:
// two writers patching the same ClusterIssuer's solvers would leave whichever
// kustomize applies last in charge, silently. Remove the other block first.
// The token itself never appears in logs.
func WriteDNS01Cloudflare(fleetRepo, clusterName, domain string, m *DNS01CloudflareMaterial, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if m == nil {
		return nil // Cloudflare DNS-01 not requested: nothing to scaffold
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	plain := RenderDNS01CloudflareSecret(m)
	tokenB64 := base64.StdEncoding.EncodeToString(m.Token)
	secretPath := filepath.Join(clusterDir, dns01CFSecretsFileName)
	if fi, err := os.Lstat(secretPath); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("dns01-cloudflare: %s exists but is not a regular file", secretPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("dns01-cloudflare: stat %s: %w", secretPath, err)
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
		patchKustomizationResource(dns01CFSecretsFileName)); err != nil {
		return fmt.Errorf("dns01-cloudflare: kustomization.yaml: %w", err)
	}
	platformPath := filepath.Join(clusterDir, "platform.yaml")
	if _, err := os.Stat(platformPath); err != nil {
		return fmt.Errorf("dns01-cloudflare: %s does not exist — cannot patch the ClusterIssuer solvers", platformPath)
	}
	if err := stage(platformPath, patchPlatformDNS01SolverWith(dns01CloudflareMarker,
		dns01CloudflareBlockHeader(m), RenderDNS01CloudflareIssuerPatch(m, domain))); err != nil {
		return fmt.Errorf("dns01-cloudflare: %w", err)
	}
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return fmt.Errorf("dns01-cloudflare: %w", err)
	}

	if !sopsFileDecryptsTo(fleetRepo, secretPath, plain, []string{string(m.Token), tokenB64}) {
		if err := sopsEncryptToFile(fleetRepo, secretPath, plain, []string{string(m.Token), tokenB64}); err != nil {
			return fmt.Errorf("dns01-cloudflare: %w", err)
		}
	}

	for _, w := range writes {
		current, err := os.ReadFile(w.path)
		if err != nil {
			return fmt.Errorf("dns01-cloudflare: re-read %s: %w", w.path, err)
		}
		if string(current) != string(w.original) {
			return fmt.Errorf("dns01-cloudflare: %s changed while init was running — re-run init against the current state", filepath.Base(w.path))
		}
		if err := os.WriteFile(w.path, []byte(w.content), 0o644); err != nil {
			return fmt.Errorf("dns01-cloudflare: write %s: %w", w.path, err)
		}
	}
	if m.Scope == DNS01CloudflareScopeAll {
		env.Set("TLS_MODE", TLSModeACMEDNS01Cloudflare)
	} else {
		env.Set("TLS_MODE", TLSModeACME)
	}
	env.Set("DNS01_CLOUDFLARE_ZONE", m.Zone)
	env.Set("DNS01_CLOUDFLARE_SCOPE", m.Scope)
	if err := env.Write(""); err != nil {
		return fmt.Errorf("dns01-cloudflare: record TLS_MODE: %w", err)
	}

	switch m.Scope {
	case DNS01CloudflareScopeWildcard:
		fmt.Fprintf(out, "[scaffold] Cloudflare DNS-01 wired for the platform wildcard (*.%s) on ClusterIssuer %s; HTTP-01 stays for per-host certificates (zone %s, SOPS token); cert-manager renews automatically\n",
			domain, dns01ClusterIssuerName, m.Zone)
	default:
		fmt.Fprintf(out, "[scaffold] acme-dns01-cloudflare TLS wired: ClusterIssuer %s → Cloudflare DNS-01 (zone %q, SOPS token); cert-manager renews automatically — Let's Encrypt never needs to reach the cluster\n",
			dns01ClusterIssuerName, m.Zone)
	}
	return nil
}
