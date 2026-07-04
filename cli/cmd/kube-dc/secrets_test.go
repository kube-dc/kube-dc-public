// Pure-function tests for the `kube-dc secrets put` flag parser
// and the small output helpers. The cobra commands themselves are
// exercised by the live stage smoke; this file pins the parsing
// contract for --from-literal / --from-file because a regression
// there is hard to spot (silent data corruption in the KV write).

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonMarshal is a tiny adapter for tests so they don't need to
// import encoding/json themselves.
func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func TestBuildPutData_FromLiteral(t *testing.T) {
	d, err := buildPutData([]string{"K1=v1", "K2=v2", "EMPTY="}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d["K1"] != "v1" || d["K2"] != "v2" || d["EMPTY"] != "" {
		t.Errorf("data = %v; want K1=v1 K2=v2 EMPTY=''", d)
	}
}

func TestBuildPutData_FromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tls.crt")
	if err := os.WriteFile(p, []byte("-----BEGIN-----\ncontent\n-----END-----\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	d, err := buildPutData(nil, []string{"tls.crt=" + p})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := d["tls.crt"], "-----BEGIN-----\ncontent\n-----END-----\n"; got != want {
		t.Errorf("file value = %q; want %q", got, want)
	}
}

func TestBuildPutData_RejectMalformedLiteral(t *testing.T) {
	cases := []string{"justKey", "=novalue", ""}
	for _, c := range cases {
		if _, err := buildPutData([]string{c}, nil); err == nil {
			t.Errorf("expected error for malformed --from-literal %q", c)
		}
	}
}

func TestBuildPutData_RejectMalformedFile(t *testing.T) {
	cases := []string{"justKey", "=path", "K="}
	for _, c := range cases {
		if _, err := buildPutData(nil, []string{c}); err == nil {
			t.Errorf("expected error for malformed --from-file %q", c)
		}
	}
}

func TestBuildPutData_MissingFile(t *testing.T) {
	if _, err := buildPutData(nil, []string{"k=/nonexistent/path/that/should/not/exist"}); err == nil {
		t.Errorf("expected error when --from-file path is missing")
	}
}

func TestBuildPutData_ValueContainsEquals(t *testing.T) {
	// strings.Cut splits on the FIRST '=' so values with '=' inside
	// are preserved (postgres://user:pass@host/db?sslmode=require).
	d, err := buildPutData([]string{"DSN=postgres://u:p@h/d?sslmode=require"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := d["DSN"], "postgres://u:p@h/d?sslmode=require"; got != want {
		t.Errorf("DSN = %q; want %q", got, want)
	}
}

func TestParseOutput(t *testing.T) {
	for _, in := range []string{"", "table"} {
		if f, err := parseOutput(in); err != nil || f != outTable {
			t.Errorf("parseOutput(%q) = (%v, %v); want (table, nil)", in, f, err)
		}
	}
	for _, in := range []string{"json", "JSON"} {
		if f, err := parseOutput(in); err != nil || f != outJSON {
			t.Errorf("parseOutput(%q) = (%v, %v); want (json, nil)", in, f, err)
		}
	}
	for _, in := range []string{"yaml", "yml", "YAML"} {
		if f, err := parseOutput(in); err != nil || f != outYAML {
			t.Errorf("parseOutput(%q) = (%v, %v); want (yaml, nil)", in, f, err)
		}
	}
	if _, err := parseOutput("xml"); err == nil {
		t.Errorf("parseOutput(xml) should error")
	}
}

func TestTruncCLI(t *testing.T) {
	if got := truncCLI("short", 10); got != "short" {
		t.Errorf("short unchanged: %q", got)
	}
	if got := truncCLI("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncate: %q", got)
	}
	if got := truncCLI("ab", 1); got != "a" {
		t.Errorf("n=1 returns first char: %q", got)
	}
}

// buildSyncPatch must preserve false / explicit values that the
// previous typed-struct version dropped via omitempty. Pins the
// fix for M1-T07 first-review-pass P1.
func TestBuildSyncPatch_EnabledFalseIsPreserved(t *testing.T) {
	p, err := buildSyncPatch("false", "", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got, ok := p["enabled"]
	if !ok {
		t.Fatalf("enabled key missing from patch: %v", p)
	}
	if got != false {
		t.Errorf("enabled = %v; want false", got)
	}
	// Make sure no other fields snuck in.
	if len(p) != 1 {
		t.Errorf("patch has %d keys; want 1: %v", len(p), p)
	}
}

func TestBuildSyncPatch_EnabledTrueAndAliases(t *testing.T) {
	for _, in := range []string{"true", "TRUE", "yes", "on"} {
		p, err := buildSyncPatch(in, "", "", "")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if p["enabled"] != true {
			t.Errorf("%q: enabled = %v; want true", in, p["enabled"])
		}
	}
	for _, in := range []string{"false", "FALSE", "no", "off"} {
		p, err := buildSyncPatch(in, "", "", "")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if p["enabled"] != false {
			t.Errorf("%q: enabled = %v; want false", in, p["enabled"])
		}
	}
	if _, err := buildSyncPatch("maybe", "", "", ""); err == nil {
		t.Errorf("expected error for --enabled=maybe")
	}
}

func TestBuildSyncPatch_AllFields(t *testing.T) {
	p, err := buildSyncPatch("true", "my-secret", "30s", "API_KEY,DB_URL")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p["enabled"] != true {
		t.Errorf("enabled wrong: %v", p)
	}
	if p["targetSecretName"] != "my-secret" {
		t.Errorf("target wrong: %v", p)
	}
	if p["refreshInterval"] != "30s" {
		t.Errorf("refresh wrong: %v", p)
	}
	keys, ok := p["keys"].([]string)
	if !ok || len(keys) != 2 || keys[0] != "API_KEY" || keys[1] != "DB_URL" {
		t.Errorf("keys wrong: %v", p["keys"])
	}
}

func TestBuildSyncPatch_NoFieldsErrors(t *testing.T) {
	if _, err := buildSyncPatch("", "", "", ""); err == nil {
		t.Errorf("expected error when no fields are set")
	}
}

func TestBuildSyncPatch_OmitempyContractForK8sSerialise(t *testing.T) {
	// Regression test for the P1 bug: the patch payload that gets
	// marshalled into the K8s API call MUST contain {"enabled":false}
	// when the user passes --enabled=false. Encoding the helper's
	// output verifies the fix end-to-end.
	p, err := buildSyncPatch("false", "", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	encoded, err := jsonMarshal(map[string]any{"spec": map[string]any{"sync": p}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"spec":{"sync":{"enabled":false}}}`
	if encoded != want {
		t.Errorf("encoded = %s; want %s", encoded, want)
	}
}

func TestRealmFromContext(t *testing.T) {
	for ctx, want := range map[string]string{
		"kube-dc/kube-dc.cloud/admin":             "master",
		"kube-dc/kube-dc.cloud/shalb/envoy":       "shalb",
		"kube-dc/kdc.zrh.cloudsigma.com/cs/demo":      "cs",
		"kube-dc/cluster.example/org/proj-with-hyph":  "org",
		"my-non-kubedc-context":                       "",
		"kube-dc/only-one-level":                      "",
		"":                                            "",
	} {
		if got := realmFromContext(ctx); got != want {
			t.Errorf("realmFromContext(%q) = %q; want %q", ctx, got, want)
		}
	}
}

func TestFmtCoalesce(t *testing.T) {
	if got := fmtCoalesce("", "", "third"); got != "third" {
		t.Errorf("got %q; want third", got)
	}
	if got := fmtCoalesce("first", "second"); got != "first" {
		t.Errorf("got %q; want first", got)
	}
	if got := fmtCoalesce(); got != "" {
		t.Errorf("got %q; want empty", got)
	}
}

// ---- M1-T07a reviewer P2/P3: buildCreateManagedSecret -------------

// Default create: sync enabled, target defaults to name, refresh empty
// so the API webhook applies its 1h default. Type 'opaque' allowed.
func TestBuildCreateManagedSecret_Defaults(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "app-config", Namespace: "shalb-docs", Type: "opaque",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ms.Spec.Sync.Enabled != true {
		t.Errorf("Enabled = %v; want true", ms.Spec.Sync.Enabled)
	}
	if ms.Spec.Sync.TargetSecretName != "app-config" {
		t.Errorf("target = %q; want app-config", ms.Spec.Sync.TargetSecretName)
	}
	if ms.Spec.Sync.RefreshInterval != "" {
		t.Errorf("refresh = %q; want '' (let API default to 1h)", ms.Spec.Sync.RefreshInterval)
	}
	if len(ms.Spec.Sync.Keys) != 0 {
		t.Errorf("keys = %v; want empty", ms.Spec.Sync.Keys)
	}
}

// Reviewer P2: --sync-disabled must NOT carry target/refresh/keys
// alongside enabled=false. The disabled object stays clean.
func TestBuildCreateManagedSecret_SyncDisabledOmitsRest(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "no-sync", Namespace: "shalb-docs", Type: "opaque",
		SyncDisabled: true,
		SyncTarget:   "should-not-appear",
		SyncRefresh:  "1m",
		SyncKeysCSV:  "A,B",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ms.Spec.Sync.Enabled != false {
		t.Errorf("Enabled = %v; want false", ms.Spec.Sync.Enabled)
	}
	if ms.Spec.Sync.TargetSecretName != "" {
		t.Errorf("target = %q; want empty when sync disabled", ms.Spec.Sync.TargetSecretName)
	}
	if ms.Spec.Sync.RefreshInterval != "" {
		t.Errorf("refresh = %q; want empty when sync disabled", ms.Spec.Sync.RefreshInterval)
	}
	if len(ms.Spec.Sync.Keys) != 0 {
		t.Errorf("keys = %v; want empty when sync disabled", ms.Spec.Sync.Keys)
	}
}

// Reviewer P2: end-to-end JSON shape — `enabled:false` MUST appear on
// the wire so the kube-apiserver records the user's explicit intent.
// The earlier `omitempty` on Enabled silently dropped it.
func TestBuildCreateManagedSecret_EnabledFalseSerializesOnWire(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "no-sync", Namespace: "shalb-docs", Type: "opaque",
		SyncDisabled: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	encoded, err := jsonMarshal(ms.Spec.Sync)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// We don't pin the full string (key order is map-ish) — we just
	// require "enabled":false to be present.
	if !strings.Contains(encoded, `"enabled":false`) {
		t.Errorf("sync JSON = %s; missing \"enabled\":false", encoded)
	}
	// And that the disabled-omits-rest cleanup actually keeps the
	// wire payload minimal.
	if strings.Contains(encoded, "targetSecretName") {
		t.Errorf("sync JSON = %s; should not carry targetSecretName when disabled", encoded)
	}
	if strings.Contains(encoded, "refreshInterval") {
		t.Errorf("sync JSON = %s; should not carry refreshInterval when disabled", encoded)
	}
}

func TestBuildCreateManagedSecret_SyncKeysTrimAndDrop(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "x", Namespace: "ns", Type: "opaque",
		SyncKeysCSV: "  A_KEY ,, B_KEY ,",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := ms.Spec.Sync.Keys
	if len(got) != 2 || got[0] != "A_KEY" || got[1] != "B_KEY" {
		t.Errorf("keys = %v; want [A_KEY B_KEY]", got)
	}
}

func TestBuildCreateManagedSecret_RejectsInvalidType(t *testing.T) {
	_, err := buildCreateManagedSecret(createOpts{
		Name: "x", Namespace: "ns", Type: "bogus",
	})
	if err == nil {
		t.Errorf("expected error for --type bogus")
	}
}

func TestBuildCreateManagedSecret_AcceptsAllEnumTypes(t *testing.T) {
	for _, ty := range []string{"opaque", "password", "api-key", "tls", "db-static"} {
		if _, err := buildCreateManagedSecret(createOpts{
			Name: "x", Namespace: "ns", Type: ty,
		}); err != nil {
			t.Errorf("--type %s rejected: %v", ty, err)
		}
	}
}

// T07a second-pass reviewer P2: the CLI must not send
// rotation:{enabled:false} on every create. CRD defaulting already
// gives rotation.enabled=false when the block is absent, so the wire
// payload stays clean and we don't permanently record a "no, really,
// off" flag for tenants who never asked for rotation.
func TestBuildCreateManagedSecret_OmitsRotation(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "x", Namespace: "ns", Type: "opaque",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ms.Spec.Rotation != nil {
		t.Errorf("Rotation = %v; want nil so JSON marshal drops the field", ms.Spec.Rotation)
	}
	// End-to-end: the encoded body must NOT contain a `rotation` key.
	encoded, err := jsonMarshal(ms.Spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(encoded, `"rotation"`) {
		t.Errorf("create payload carries rotation key: %s", encoded)
	}
}

// Custom sync-target wins over the name fallback.
func TestBuildCreateManagedSecret_CustomTarget(t *testing.T) {
	ms, err := buildCreateManagedSecret(createOpts{
		Name: "ms-name", Namespace: "ns", Type: "opaque",
		SyncTarget: "custom-k8s-secret",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ms.Spec.Sync.TargetSecretName != "custom-k8s-secret" {
		t.Errorf("target = %q; want custom-k8s-secret", ms.Spec.Sync.TargetSecretName)
	}
}

// ---- M1-T07a UX-PRD alignment: parseEnvFile ------------------------

func TestParseEnvFile_BasicAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.env")
	body := "# top comment\n" +
		"\n" +
		"DATABASE_URL=postgres://u:p@h/d?sslmode=require\n" +
		"  API_KEY  =xyz\n" + // key whitespace trimmed
		"NOTE=hello # not a comment, lives in value\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got["DATABASE_URL"] != "postgres://u:p@h/d?sslmode=require" {
		t.Errorf("DATABASE_URL = %q", got["DATABASE_URL"])
	}
	if got["API_KEY"] != "xyz" {
		t.Errorf("API_KEY = %q (key whitespace should be trimmed)", got["API_KEY"])
	}
	// Inline '#' is NOT a comment marker — it's part of the value. We
	// keep this behaviour because escaping it would conflict with real
	// password material containing '#'.
	if got["NOTE"] != "hello # not a comment, lives in value" {
		t.Errorf("NOTE = %q", got["NOTE"])
	}
}

func TestParseEnvFile_RejectsDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dup.env")
	if err := os.WriteFile(p, []byte("K=1\nK=2\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := parseEnvFile(p); err == nil {
		t.Errorf("expected error on duplicate key")
	}
}

func TestParseEnvFile_RejectsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(p, []byte("no_equals_sign\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := parseEnvFile(p); err == nil {
		t.Errorf("expected error on malformed line")
	}
}

func TestParseEnvFile_MissingFile(t *testing.T) {
	if _, err := parseEnvFile("/nonexistent/path/that/should/not/exist.env"); err == nil {
		t.Errorf("expected error when file missing")
	}
}

// T07a second-pass reviewer P2: the original parser called
// strings.TrimSpace on the whole line BEFORE Cut(), which silently
// dropped trailing whitespace in the value. Pin the corrected
// behaviour so we don't regress.
func TestParseEnvFile_PreservesTrailingWhitespaceInValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ws.env")
	// Note the trailing spaces after "abc" — the file ends with
	// "PASSWORD=abc   \n". A whitespace-sensitive value (HMAC keys
	// stored as raw bytes, intentionally-padded passphrases) must
	// survive verbatim.
	body := "PASSWORD=abc   \nLEAD=   leading-ok\nMIDDLE=in middle\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got["PASSWORD"] != "abc   " {
		t.Errorf("PASSWORD = %q; want %q (trailing whitespace preserved)", got["PASSWORD"], "abc   ")
	}
	if got["LEAD"] != "   leading-ok" {
		t.Errorf("LEAD = %q; want %q (leading whitespace preserved)", got["LEAD"], "   leading-ok")
	}
	if got["MIDDLE"] != "in middle" {
		t.Errorf("MIDDLE = %q", got["MIDDLE"])
	}
}

// CRLF endings (a Windows-edited .env) must classify blank/comment
// lines correctly AND not leak \r into values.
func TestParseEnvFile_HandlesCRLF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "crlf.env")
	body := "# comment\r\nK=v\r\n\r\nK2=value with spaces\r\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got["K"] != "v" {
		t.Errorf("K = %q; want %q", got["K"], "v")
	}
	if got["K2"] != "value with spaces" {
		t.Errorf("K2 = %q (CR should be stripped)", got["K2"])
	}
}
