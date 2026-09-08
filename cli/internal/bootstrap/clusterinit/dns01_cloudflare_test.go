package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are deliberately fake: token-shaped strings that authenticate
// nothing (the no-real-infra lint applies to cli/ — see internal/lint).
const (
	testCFToken     = "kdcTESTtokenEXAMPLEonly0000000000000000000000"
	testCFGlobalKey = "0123456789abcdef0123456789abcdef01234"
	testCFZone      = "example.org"
	testCFDomain    = "dc1.example.org"
)

func validCFFull(t *testing.T) *DNS01CloudflareMaterial {
	t.Helper()
	m, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, "", testCFToken)
	if err != nil {
		t.Fatalf("valid full-scope inputs rejected: %v", err)
	}
	return m
}

func validCFWildcard(t *testing.T) *DNS01CloudflareMaterial {
	t.Helper()
	m, err := LoadDNS01Cloudflare(TLSModeACME, testCFDomain, testCFZone, "", testCFToken)
	if err != nil {
		t.Fatalf("valid wildcard-scope inputs rejected: %v", err)
	}
	return m
}

func TestLoadDNS01Cloudflare_ScopesAndFingerprint(t *testing.T) {
	full := validCFFull(t)
	if full.Scope != DNS01CloudflareScopeAll || full.Zone != testCFZone {
		t.Errorf("full mode: scope=%q zone=%q", full.Scope, full.Zone)
	}
	if len(full.Fingerprint) != 64 || string(full.Token) != testCFToken {
		t.Errorf("fingerprint/token wrong: %q / %d bytes", full.Fingerprint, len(full.Token))
	}
	wc := validCFWildcard(t)
	if wc.Scope != DNS01CloudflareScopeWildcard {
		t.Errorf("acme + zone must be wildcard scope, got %q", wc.Scope)
	}
	// Full scope without a zone is allowed (cert-manager resolves the zone
	// through the token); the selector is simply omitted.
	m, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", "", testCFToken)
	if err != nil || m.Zone != "" {
		t.Errorf("full scope without zone must load: err=%v zone=%q", err, m.Zone)
	}
}

func TestLoadDNS01Cloudflare_NotRequestedIsNil(t *testing.T) {
	for _, mode := range []string{"", TLSModeACME, TLSModeBYOWildcard, TLSModeACMEDNS01Route53} {
		// The token alone in the environment must NOT turn a run into a
		// Cloudflare one — the zone flag is the explicit opt-in.
		m, err := LoadDNS01Cloudflare(mode, testCFDomain, "", "", testCFToken)
		if err != nil || m != nil {
			t.Errorf("mode %q with no cloudflare flags: want (nil,nil), got (%v,%v)", mode, m, err)
		}
	}
}

func TestLoadDNS01Cloudflare_TokenFromFileTrimmed(t *testing.T) {
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte("Bearer "+testCFToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", f, "")
	if err != nil {
		t.Fatalf("file-sourced token rejected: %v", err)
	}
	if string(m.Token) != testCFToken {
		t.Errorf("Bearer prefix / newline must be stripped, got %q", m.Token)
	}
	// A single terminal CRLF is fine too (Windows editors).
	if err := os.WriteFile(f, []byte(testCFToken+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", f, ""); err != nil || string(m.Token) != testCFToken {
		t.Errorf("single terminal CRLF must be accepted: err=%v", err)
	}
	// Anything with more than one line is not a token file — including blank
	// lines around the token, which TrimSpace would otherwise hide (codex
	// 2026-08-31, P3).
	// The environment channel gets NO newline allowance at all (codex pass 2).
	for name, env := range map[string]string{
		"env var with two newlines": testCFToken + "\n\n",
		"env var with one LF":       testCFToken + "\n",
		"env var with CRLF":         testCFToken + "\r\n",
	} {
		if _, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", "", env); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	for name, body := range map[string]string{
		"env-style":            "TOKEN=" + testCFToken + "\nOTHER=x\n",
		"leading blank line":   "\n" + testCFToken + "\n",
		"trailing blank lines": testCFToken + "\n\n\n",
		"lone terminal CR":     testCFToken + "\r",
		"CR then LF LF":        testCFToken + "\r\n\n",
	} {
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", f, ""); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestLoadDNS01Cloudflare_ReadErrorNeverEchoesPath(t *testing.T) {
	// codex 2026-08-31 P2: the token pasted where the PATH belongs must not
	// come back in the error text (stderr, CI logs).
	_, err := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", testCFToken, "")
	if err == nil {
		t.Fatal("unreadable token file must error")
	}
	if strings.Contains(err.Error(), testCFToken) {
		t.Errorf("error echoes the supplied path/token: %q", err)
	}
	// Same guard on the Route53 loader.
	_, err = LoadDNS01Route53(testZoneID, "", testAccessKey, testSecretKey, "")
	if err == nil || strings.Contains(err.Error(), testSecretKey) {
		t.Errorf("route53 read error must not echo the path: %v", err)
	}
}

func TestLoadDNS01Cloudflare_Rejections(t *testing.T) {
	cases := []struct {
		name                     string
		mode, domain, zone, tokE string
		wantSubstr               string
	}{
		{"byo mode with cf flags", TLSModeBYOWildcard, testCFDomain, testCFZone, testCFToken, "only valid with"},
		{"route53 mode with cf flags", TLSModeACMEDNS01Route53, testCFDomain, testCFZone, testCFToken, "only valid with"},
		{"zone is a URL", TLSModeACMEDNS01Cloudflare, testCFDomain, "https://example.org", testCFToken, "zone name"},
		{"domain outside zone", TLSModeACMEDNS01Cloudflare, "dc1.other.example", testCFZone, testCFToken, "does not contain"},
		{"no token anywhere", TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, "", "needs the API token"},
		{"global api key", TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, testCFGlobalKey, "Global API Key"},
		{"email pasted", TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, "ops@example.org", "email"},
		{"too short", TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, "tooshort", "too short"},
		{"bad charset", TLSModeACMEDNS01Cloudflare, testCFDomain, testCFZone, strings.Repeat("x", 39) + "!!", "does not look like"},
	}
	for _, c := range cases {
		_, err := LoadDNS01Cloudflare(c.mode, c.domain, c.zone, "", c.tokE)
		if err == nil {
			t.Errorf("%s: want error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("%s: error %q should mention %q", c.name, err, c.wantSubstr)
		}
		if strings.Contains(err.Error(), testCFToken) || strings.Contains(err.Error(), testCFGlobalKey) {
			t.Errorf("%s: error message leaks the credential: %q", c.name, err)
		}
	}
	// The zone check is applied in wildcard scope too.
	if _, err := LoadDNS01Cloudflare(TLSModeACME, "dc1.other.example", testCFZone, "", testCFToken); err == nil {
		t.Error("wildcard scope with a domain outside the zone must be rejected")
	}
	// A token FILE with --tls-mode=acme is a request (unlike the bare env
	// var) — and the wildcard scope then demands the zone opt-in.
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte(testCFToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDNS01Cloudflare(TLSModeACME, testCFDomain, "", f, ""); err == nil || !strings.Contains(err.Error(), "--dns01-cloudflare-zone") {
		t.Errorf("acme + token file without zone must demand --dns01-cloudflare-zone, got %v", err)
	}
}

func TestRenderDNS01CloudflareSecret_ShapeAndNoPlaintextLeak(t *testing.T) {
	m := validCFFull(t)
	y := RenderDNS01CloudflareSecret(m)
	for _, want := range []string{
		"name: " + dns01CFSecretName,
		"namespace: " + dns01SecretNamespace,
		dns01CFSecretKeyField + ": ",
		`kube-dc.com/acme-dns01: "cloudflare"`,
	} {
		if !strings.Contains(y, want) {
			t.Errorf("secret manifest missing %q", want)
		}
	}
	if strings.Contains(y, testCFToken) {
		t.Error("raw token appears unencoded in the manifest")
	}
}

func TestRenderDNS01CloudflareIssuerPatch_FullScope(t *testing.T) {
	p := RenderDNS01CloudflareIssuerPatch(validCFFull(t), testCFDomain)
	for _, want := range []string{
		"kind: ClusterIssuer",
		"name: " + dns01ClusterIssuerName,
		"dnsZones:\n                    - \"" + testCFZone + "\"",
		"cloudflare:\n                    apiTokenSecretRef:",
		"name: " + dns01CFSecretName,
		"key: " + dns01CFSecretKeyField,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("issuer patch missing %q\n%s", want, p)
		}
	}
	if strings.Contains(p, "http01") {
		t.Error("full scope must DROP the http01 solver (it would keep winning)")
	}
	if strings.Contains(p, testCFToken) {
		t.Error("token must never appear in the issuer patch")
	}
	// No zone → no selector at all.
	m, _ := LoadDNS01Cloudflare(TLSModeACMEDNS01Cloudflare, testCFDomain, "", "", testCFToken)
	if p2 := RenderDNS01CloudflareIssuerPatch(m, testCFDomain); strings.Contains(p2, "selector") {
		t.Error("full scope without a zone must not render a selector")
	}
}

func TestRenderDNS01CloudflareIssuerPatch_WildcardScopeKeepsHTTP01(t *testing.T) {
	p := RenderDNS01CloudflareIssuerPatch(validCFWildcard(t), testCFDomain)
	for _, want := range []string{
		"dnsNames:\n                    - \"*." + testCFDomain + "\"\n                    - \"" + testCFDomain + "\"",
		"cloudflare:",
		"- http01:\n                  gatewayHTTPRoute:",
		"name: eg\n                        namespace: envoy-gateway-system\n                        kind: Gateway",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("wildcard patch missing %q\n%s", want, p)
		}
	}
	// The selector-bearing dns01 solver must come FIRST; cert-manager picks
	// the most specific match, but order is the tie-break humans read.
	if strings.Index(p, "cloudflare:") > strings.Index(p, "http01:") {
		t.Error("dns01 solver must precede the http01 solver")
	}
	if strings.Contains(p, "dnsZones") {
		t.Error("wildcard scope selects by dnsNames, not dnsZones (a zone selector would pull EVERY certificate onto DNS-01)")
	}
}

func TestPatchPlatformDNS01Solver_CloudflareCreatesIdempotentAndRotates(t *testing.T) {
	wc := validCFWildcard(t)
	patch := patchPlatformDNS01SolverWith(dns01CloudflareMarker, dns01CloudflareBlockHeader(wc),
		RenderDNS01CloudflareIssuerPatch(wc, testCFDomain))
	out, changed, err := patch(platformYAML(""))
	if err != nil || !changed {
		t.Fatalf("first application: changed=%v err=%v", changed, err)
	}
	body := strings.Join(out, "\n")
	if !strings.Contains(body, dns01CloudflareMarker) || !strings.Contains(body, "  patches:\n") {
		t.Errorf("marker/patches key missing:\n%s", body)
	}
	if _, changed, err := patch(out); err != nil || changed {
		t.Fatalf("re-run must be a no-op: changed=%v err=%v", changed, err)
	}
	// Scope switch wildcard → all is a content change: replaced in place,
	// exactly one entry survives.
	full := validCFFull(t)
	out2, changed, err := patchPlatformDNS01SolverWith(dns01CloudflareMarker, dns01CloudflareBlockHeader(full),
		RenderDNS01CloudflareIssuerPatch(full, testCFDomain))(out)
	if err != nil || !changed {
		t.Fatalf("scope switch must rewrite the block: changed=%v err=%v", changed, err)
	}
	body2 := strings.Join(out2, "\n")
	if strings.Contains(body2, "http01") || !strings.Contains(body2, "dnsZones") {
		t.Errorf("old wildcard block survived the switch:\n%s", body2)
	}
	if n := strings.Count(body2, "kind: ClusterIssuer"); n != 2 {
		t.Errorf("want exactly one patch entry (2 kind: occurrences), got %d", n)
	}
}

func TestPatchPlatformDNS01Solver_ProvidersAreMutuallyExclusive(t *testing.T) {
	r53 := validDNS01(t)
	withR53, _, err := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(r53))(platformYAML(""))
	if err != nil {
		t.Fatal(err)
	}
	cf := validCFFull(t)
	if _, _, err := patchPlatformDNS01SolverWith(dns01CloudflareMarker, dns01CloudflareBlockHeader(cf),
		RenderDNS01CloudflareIssuerPatch(cf, testCFDomain))(withR53); err == nil {
		t.Error("a platform.yaml already carrying the Route53 solver block must be refused")
	}
	withCF, _, err := patchPlatformDNS01SolverWith(dns01CloudflareMarker, dns01CloudflareBlockHeader(cf),
		RenderDNS01CloudflareIssuerPatch(cf, testCFDomain))(platformYAML(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(r53))(withCF); err == nil {
		t.Error("a platform.yaml already carrying the Cloudflare solver block must be refused by the Route53 writer")
	}
}

func TestValidateDNS01Flags_Cloudflare(t *testing.T) {
	ok := []*InitOptions{
		{TLSMode: TLSModeACMEDNS01Cloudflare},
		{TLSMode: TLSModeACMEDNS01Cloudflare, DNS01CloudflareZone: testCFZone},
		{TLSMode: TLSModeACME, DNS01CloudflareZone: testCFZone},
		{TLSMode: TLSModeACME, DNS01CloudflareZone: testCFZone, DNS01CloudflareAPITokenFile: "/x"},
	}
	for i, o := range ok {
		if err := validateDNS01Flags(o); err != nil {
			t.Errorf("case %d must pass: %v", i, err)
		}
	}
	bad := []*InitOptions{
		{TLSMode: TLSModeACMEDNS01Cloudflare, DNS01Route53ZoneID: testZoneID},
		{TLSMode: TLSModeACMEDNS01Route53, DNS01Route53ZoneID: testZoneID, DNS01Route53AccessKeyID: testAccessKey, DNS01CloudflareZone: testCFZone},
		{TLSMode: TLSModeBYOWildcard, DNS01CloudflareZone: testCFZone},
		{TLSMode: TLSModeACME, DNS01CloudflareZone: testCFZone, DNS01Route53ZoneID: testZoneID},
		{TLSMode: TLSModeACME, Sets: map[string]string{"DNS01_CLOUDFLARE_ZONE": testCFZone}},
		{TLSMode: TLSModeACME, Sets: map[string]string{"DNS01_CLOUDFLARE_SCOPE": "all"}},
		// codex 2026-08-31 P1: a secret via --set would be echoed in the
		// options summary and persisted in cleartext — refused outright.
		{TLSMode: TLSModeACME, Sets: map[string]string{"DNS01_CLOUDFLARE_API_TOKEN": testCFToken}},
		{TLSMode: TLSModeACMEDNS01Cloudflare, Sets: map[string]string{"CLOUDFLARE_API_TOKEN": testCFToken}},
		{TLSMode: TLSModeACMEDNS01Route53, DNS01Route53ZoneID: testZoneID, DNS01Route53AccessKeyID: testAccessKey,
			Sets: map[string]string{"DNS01_ROUTE53_SECRET_KEY": testSecretKey}},
	}
	for i, o := range bad {
		err := validateDNS01Flags(o)
		if err == nil {
			t.Errorf("bad case %d must be rejected", i)
			continue
		}
		if strings.Contains(err.Error(), testCFToken) || strings.Contains(err.Error(), testSecretKey) {
			t.Errorf("bad case %d: error echoes the secret: %q", i, err)
		}
	}
}

func TestValidateTLSMode_AcceptsCloudflare(t *testing.T) {
	if err := ValidateTLSMode(TLSModeACMEDNS01Cloudflare, "", ""); err != nil {
		t.Errorf("cloudflare mode rejected: %v", err)
	}
	if err := ValidateTLSMode(TLSModeACMEDNS01Cloudflare, "c", "k"); err == nil {
		t.Error("byo material with cloudflare mode must be rejected")
	}
}

func TestWriteDNS01Cloudflare_NilIsNoop(t *testing.T) {
	if err := WriteDNS01Cloudflare(t.TempDir(), "x", testCFDomain, nil, nil); err != nil {
		t.Fatalf("nil material must be a no-op: %v", err)
	}
}

func TestImportExportMap_Cloudflare(t *testing.T) {
	o := &InitOptions{Sets: map[string]string{}}
	ignored := ImportMap(o, map[string]string{
		"TLS_MODE":                   TLSModeACMEDNS01Cloudflare,
		"DNS01_CLOUDFLARE_ZONE":      testCFZone,
		"DNS01_CLOUDFLARE_SCOPE":     DNS01CloudflareScopeAll,
		"DNS01_CLOUDFLARE_API_TOKEN": testCFToken,
		"CLOUDFLARE_API_TOKEN":       testCFToken,
	}, func(string) bool { return false })
	if o.TLSMode != TLSModeACMEDNS01Cloudflare || o.DNS01CloudflareZone != testCFZone || o.DNS01CloudflareScope != DNS01CloudflareScopeAll {
		t.Errorf("promoted fields not restored: %+v", o)
	}
	for k, v := range o.Sets {
		if strings.Contains(k, "CLOUDFLARE") || v == testCFToken {
			t.Errorf("key %s must not land in generic Sets", k)
		}
	}
	denied := 0
	for _, k := range ignored {
		if k == "DNS01_CLOUDFLARE_API_TOKEN" || k == "CLOUDFLARE_API_TOKEN" {
			denied++
		}
	}
	if denied != 2 {
		t.Errorf("token keys must be deny-imported (ignored=%v)", ignored)
	}
	m := ExportMap(&InitOptions{TLSMode: TLSModeACME, DNS01CloudflareZone: testCFZone, DNS01CloudflareScope: DNS01CloudflareScopeWildcard})
	if m["DNS01_CLOUDFLARE_ZONE"] != testCFZone || m["DNS01_CLOUDFLARE_SCOPE"] != DNS01CloudflareScopeWildcard {
		t.Errorf("export dropped cloudflare config: %v", m)
	}
	for k := range m {
		if strings.Contains(k, "TOKEN") {
			t.Errorf("token must never be exported (%s)", k)
		}
	}
}
