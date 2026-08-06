package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test fixture values are deliberately fake: RFC-shaped AWS identifiers that
// authenticate nothing (the no-real-infra lint applies to cli/ — see
// internal/lint).
const (
	testZoneID    = "Z0EXAMPLE12345TEST"
	testAccessKey = "AKIAEXAMPLETEST00000"
	testSecretKey = "wJalrXUtnFEMIexamplekeyEXAMPLEKEY0000000"
)

func validDNS01(t *testing.T) *DNS01Route53Material {
	t.Helper()
	m, err := LoadDNS01Route53(testZoneID, "", testAccessKey, "", testSecretKey)
	if err != nil {
		t.Fatalf("valid inputs rejected: %v", err)
	}
	return m
}

func TestLoadDNS01Route53_ValidatesAndCanonicalizes(t *testing.T) {
	m := validDNS01(t)
	if m.Region != dns01DefaultRegion {
		t.Errorf("empty region must default to %s, got %q", dns01DefaultRegion, m.Region)
	}
	if len(m.Fingerprint) != 64 {
		t.Errorf("fingerprint must be hex sha256, got %q", m.Fingerprint)
	}
	if string(m.SecretKey) != testSecretKey {
		t.Errorf("secret key mutated")
	}
}

func TestLoadDNS01Route53_SecretFromFileTrimmed(t *testing.T) {
	f := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(f, []byte(testSecretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadDNS01Route53(testZoneID, "eu-central-1", testAccessKey, f, "")
	if err != nil {
		t.Fatalf("file-sourced secret rejected: %v", err)
	}
	if string(m.SecretKey) != testSecretKey {
		t.Errorf("trailing newline must be trimmed, got %d bytes", len(m.SecretKey))
	}
	if m.Region != "eu-central-1" {
		t.Errorf("explicit region lost: %q", m.Region)
	}
}

func TestLoadDNS01Route53_Rejections(t *testing.T) {
	cases := []struct {
		name                                  string
		zone, region, access, keyFile, keyEnv string
		wantSubstr                            string
	}{
		{"missing zone", "", "", testAccessKey, "", testSecretKey, "requires"},
		{"zone NAME not ID", "proto.example.org", "", testAccessKey, "", testSecretKey, "hosted-zone ID"},
		{"lowercase zone", "z0example12345test", "", testAccessKey, "", testSecretKey, "hosted-zone ID"},
		{"bad region", testZoneID, "US-EAST-1", testAccessKey, "", testSecretKey, "AWS region"},
		{"STS temporary key", testZoneID, "", "ASIAEXAMPLETEST00000", "", testSecretKey, "temporary STS"},
		{"garbage access key", testZoneID, "", "not-a-key", "", testSecretKey, "access key ID"},
		{"no secret anywhere", testZoneID, "", testAccessKey, "", "", "secret access key"},
		{"secret too short", testZoneID, "", testAccessKey, "", "tooshort", "too short"},
		{"swapped id for secret", testZoneID, "", testAccessKey, "", "AKIAEXAMPLETEST11111AKIAEXAMPLETEST11111", "swapped"},
	}
	for _, c := range cases {
		_, err := LoadDNS01Route53(c.zone, c.region, c.access, c.keyFile, c.keyEnv)
		if err == nil {
			t.Errorf("%s: want error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("%s: error %q should mention %q", c.name, err, c.wantSubstr)
		}
	}
}

func TestRenderDNS01Secret_ShapeAndNoPlaintextLeak(t *testing.T) {
	m := validDNS01(t)
	y := RenderDNS01Secret(m)
	for _, want := range []string{
		"name: " + dns01SecretName,
		"namespace: " + dns01SecretNamespace,
		dns01SecretKeyField + ": ",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("secret manifest missing %q", want)
		}
	}
	// The manifest is base64 (sops encrypts data values); the RAW secret
	// string must not appear — that would bypass the mustNotLeak guard's
	// assumptions about which encodings to check.
	if strings.Contains(y, testSecretKey) {
		t.Error("raw secret key appears unencoded in the manifest")
	}
}

func TestRenderDNS01IssuerPatch_TargetsPlatformIssuer(t *testing.T) {
	m := validDNS01(t)
	p := RenderDNS01IssuerPatch(m)
	for _, want := range []string{
		"kind: ClusterIssuer",
		"name: " + dns01ClusterIssuerName,
		"hostedZoneID: " + testZoneID,
		"accessKeyID: " + testAccessKey,
		"region: " + dns01DefaultRegion,
		"name: " + dns01SecretName,
		"key: " + dns01SecretKeyField,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("issuer patch missing %q", want)
		}
	}
	if strings.Contains(p, testSecretKey) {
		t.Error("secret key must never appear in the issuer patch")
	}
	// The solvers list must be REPLACED (leaf under spec.acme), not appended:
	// the http01 solver has no selector and would keep winning.
	if !strings.Contains(p, "solvers:\n              - dns01:") {
		t.Error("patch must replace spec.acme.solvers with the dns01 entry")
	}
}

// platformYAML returns a minimal generated-shape platform.yaml body.
func platformYAML(extra string) []string {
	base := `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
spec:
  path: ./platform
` + extra
	return strings.Split(base, "\n")
}

func TestPatchPlatformDNS01Solver_CreatesAndIdempotent(t *testing.T) {
	m := validDNS01(t)
	patch := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(m))

	out, changed, err := patch(platformYAML(""))
	if err != nil || !changed {
		t.Fatalf("first application: changed=%v err=%v", changed, err)
	}
	body := strings.Join(out, "\n")
	if !strings.Contains(body, "  patches:\n") {
		t.Error("patches: key must be created when absent")
	}
	if !strings.Contains(body, dns01Route53Marker) {
		t.Error("marker missing from patched body")
	}

	// Re-run over the patched body: no change, no error.
	out2, changed2, err := patch(out)
	if err != nil || changed2 {
		t.Fatalf("re-run must be a no-op: changed=%v err=%v", changed2, err)
	}
	if strings.Join(out2, "\n") != body {
		t.Error("re-run mutated the body")
	}
}

func TestPatchPlatformDNS01Solver_ComposesWithOwnedBlock(t *testing.T) {
	m := validDNS01(t)
	patch := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(m))

	owned := platformYAML("  patches:\n    # --- " + natPlatformPatchesMarker + " ---\n    - target:\n        kind: Gateway\n")
	if _, changed, err := patch(owned); err != nil || !changed {
		t.Errorf("must compose with a kube-dc-owned patches block: changed=%v err=%v", changed, err)
	}

	foreign := platformYAML("  patches:\n    - target:\n        kind: Gateway\n")
	if _, _, err := patch(foreign); err == nil {
		t.Error("hand-edited patches block must be refused")
	}
}

func TestValidateDNS01Flags(t *testing.T) {
	base := func() *InitOptions {
		return &InitOptions{TLSMode: TLSModeACMEDNS01Route53,
			DNS01Route53ZoneID: testZoneID, DNS01Route53AccessKeyID: testAccessKey}
	}
	if err := validateDNS01Flags(base()); err != nil {
		t.Errorf("complete dns01 trio rejected: %v", err)
	}
	o := base()
	o.DNS01Route53ZoneID = ""
	if err := validateDNS01Flags(o); err == nil {
		t.Error("dns01 mode without zone id must be rejected")
	}
	o = &InitOptions{TLSMode: TLSModeACME, DNS01Route53ZoneID: testZoneID}
	if err := validateDNS01Flags(o); err == nil {
		t.Error("dns01 flags outside dns01 mode must be rejected")
	}
	if err := validateDNS01Flags(&InitOptions{TLSMode: TLSModeACME}); err != nil {
		t.Errorf("acme mode without dns01 flags must pass: %v", err)
	}
	// codex pass-2 P2: promoted keys via --set would let the generic Sets
	// export overwrite the dedicated export — rejected outright.
	o = base()
	o.Sets = map[string]string{"TLS_MODE": "acme"}
	if err := validateDNS01Flags(o); err == nil {
		t.Error("--set TLS_MODE must be rejected (dedicated flag exists)")
	}
	o = &InitOptions{TLSMode: TLSModeACME, Sets: map[string]string{"DNS01_ROUTE53_ZONE_ID": testZoneID}}
	if err := validateDNS01Flags(o); err == nil {
		t.Error("--set DNS01_ROUTE53_ZONE_ID must be rejected (dedicated flag exists)")
	}
}

func TestValidateTLSMode_AcceptsDNS01(t *testing.T) {
	if err := ValidateTLSMode(TLSModeACMEDNS01Route53, "", ""); err != nil {
		t.Errorf("dns01 mode rejected: %v", err)
	}
	if err := ValidateTLSMode(TLSModeACMEDNS01Route53, "c", "k"); err == nil {
		t.Error("byo material with dns01 mode must be rejected")
	}
}

func TestWriteDNS01Route53_NilIsNoop(t *testing.T) {
	if err := WriteDNS01Route53(t.TempDir(), "x", nil, nil); err != nil {
		t.Fatalf("nil material must be a no-op: %v", err)
	}
}

func TestPatchPlatformDNS01Solver_RotationReplacesInPlace(t *testing.T) {
	// codex 2026-08-06 P1: a marker-only idempotence check let the SOPS
	// secret rotate while platform.yaml kept the OLD access key. The block
	// must be REPLACED when the rendered config differs.
	old := validDNS01(t)
	patched, changed, err := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(old))(platformYAML(""))
	if err != nil || !changed {
		t.Fatalf("seed patch: changed=%v err=%v", changed, err)
	}

	rotated, err := LoadDNS01Route53(testZoneID, "eu-west-1", "AKIAEXAMPLETEST11111", "", testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	out, changed, err := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(rotated))(patched)
	if err != nil || !changed {
		t.Fatalf("rotation must rewrite the block: changed=%v err=%v", changed, err)
	}
	body := strings.Join(out, "\n")
	if strings.Contains(body, testAccessKey) || strings.Contains(body, dns01DefaultRegion) {
		t.Errorf("old solver config survived rotation:\n%s", body)
	}
	if !strings.Contains(body, "AKIAEXAMPLETEST11111") || !strings.Contains(body, "eu-west-1") {
		t.Errorf("new solver config missing after rotation")
	}
	// One entry = exactly two occurrences ("kind:" in the target selector +
	// in the SMP body). Four would mean the old entry survived alongside.
	if n := strings.Count(body, "kind: ClusterIssuer"); n != 2 {
		t.Errorf("want exactly one patch entry (2 kind: occurrences), got %d", n)
	}
	// And rotating BACK to identical config is a no-op.
	if _, changed, _ := patchPlatformDNS01Solver(RenderDNS01IssuerPatch(rotated))(out); changed {
		t.Error("identical config re-run must be a no-op")
	}
}

func TestImportMap_PromotesDNS01AndDeniesSecret(t *testing.T) {
	// codex 2026-08-06 P2/P0: TLS_MODE + DNS01_* must land in the DEDICATED
	// fields (not generic Sets), and a secret key in a config file must be
	// deny-imported, never persisted.
	o := &InitOptions{Sets: map[string]string{}}
	ignored := ImportMap(o, map[string]string{
		"TLS_MODE":                    TLSModeACMEDNS01Route53,
		"DNS01_ROUTE53_ZONE_ID":       testZoneID,
		"DNS01_ROUTE53_REGION":        "eu-west-1",
		"DNS01_ROUTE53_ACCESS_KEY_ID": testAccessKey,
		"DNS01_ROUTE53_SECRET_KEY":    testSecretKey,
	}, func(string) bool { return false })

	if o.TLSMode != TLSModeACMEDNS01Route53 || o.DNS01Route53ZoneID != testZoneID ||
		o.DNS01Route53Region != "eu-west-1" || o.DNS01Route53AccessKeyID != testAccessKey {
		t.Errorf("promoted fields not restored: %+v", o)
	}
	for k := range o.Sets {
		if strings.HasPrefix(k, "DNS01_") || k == "TLS_MODE" {
			t.Errorf("key %s must not land in generic Sets", k)
		}
	}
	found := false
	for _, k := range ignored {
		if k == "DNS01_ROUTE53_SECRET_KEY" {
			found = true
		}
	}
	if !found {
		t.Errorf("secret key must be deny-imported (ignored=%v)", ignored)
	}
}

func TestExportMap_RoundTripsDNS01(t *testing.T) {
	o := &InitOptions{TLSMode: TLSModeACMEDNS01Route53,
		DNS01Route53ZoneID: testZoneID, DNS01Route53Region: "eu-west-1",
		DNS01Route53AccessKeyID: testAccessKey}
	m := ExportMap(o)
	if m["TLS_MODE"] != TLSModeACMEDNS01Route53 || m["DNS01_ROUTE53_ZONE_ID"] != testZoneID {
		t.Errorf("export dropped dns01 config: %v", m)
	}
	if _, ok := m["DNS01_ROUTE53_SECRET_KEY"]; ok {
		t.Error("secret key must never be exported")
	}
	// Default acme mode stays minimal: no TLS_MODE key at all.
	if m2 := ExportMap(&InitOptions{TLSMode: TLSModeACME}); m2["TLS_MODE"] != "" {
		t.Errorf("default acme must be canonicalized away, got %q", m2["TLS_MODE"])
	}
}
