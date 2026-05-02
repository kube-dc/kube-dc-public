package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestManager builds a CredentialsManager rooted in a tmp dir so the
// test never touches a real ~/.kube-dc/credentials/.
func newTestManager(t *testing.T) *CredentialsManager {
	t.Helper()
	return &CredentialsManager{baseDir: t.TempDir()}
}

func sampleCreds(server, realm string) *Credentials {
	return &Credentials{
		Server:             server,
		KeycloakURL:        "https://login.example.com",
		Realm:              realm,
		ClientID:           "kube-dc",
		AccessToken:        "at-" + realm,
		RefreshToken:       "rt-" + realm,
		AccessTokenExpiry:  time.Now().Add(1 * time.Hour),
		RefreshTokenExpiry: time.Now().Add(24 * time.Hour),
	}
}

// TestSaveLoad_RealmCoexistence is the load-bearing guarantee for the
// admin identity: two logins (tenant + admin) on the same cluster
// produce two separate cache files, both readable by their realm.
func TestSaveLoad_RealmCoexistence(t *testing.T) {
	m := newTestManager(t)
	const server = "https://kube-api.example.com:6443"

	tenant := sampleCreds(server, "shalb")
	admin := sampleCreds(server, "master")

	if err := m.Save(tenant); err != nil {
		t.Fatalf("save tenant: %v", err)
	}
	if err := m.Save(admin); err != nil {
		t.Fatalf("save admin: %v", err)
	}

	// Each LoadForRealm call must return the right token.
	gotTenant, err := m.LoadForRealm(server, "shalb")
	if err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if gotTenant.AccessToken != "at-shalb" {
		t.Errorf("tenant token = %q, want at-shalb (admin file leaked into tenant slot?)", gotTenant.AccessToken)
	}

	gotAdmin, err := m.LoadForRealm(server, "master")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if gotAdmin.AccessToken != "at-master" {
		t.Errorf("admin token = %q, want at-master (tenant file leaked into admin slot?)", gotAdmin.AccessToken)
	}
}

// TestLoad_LegacyFallback proves a pre-realm-aware credential file
// (single <hash>.json with no realm suffix) keeps working after the
// upgrade. Critical for an in-place rollout where operators have
// already-cached tenant tokens.
func TestLoad_LegacyFallback(t *testing.T) {
	m := newTestManager(t)
	const server = "https://kube-api.legacy.example.com:6443"

	// Manually write the pre-existing legacy file shape: <hash>.json
	// with no realm suffix. Mirrors what the old Save used to produce.
	legacy := sampleCreds(server, "shalb")
	data, err := encodeCreds(legacy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.baseDir, serverHash(server)+".json"), data, 0600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	// Load() with no realm hint must find the legacy file.
	got, err := m.Load(server)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.AccessToken != "at-shalb" {
		t.Errorf("legacy token = %q, want at-shalb", got.AccessToken)
	}

	// LoadForRealm(server, "shalb") must also find it (realm matches
	// the embedded creds.Realm, so legacy fallback applies).
	got, err = m.LoadForRealm(server, "shalb")
	if err != nil {
		t.Fatalf("LoadForRealm matching legacy: %v", err)
	}
	if got.AccessToken != "at-shalb" {
		t.Errorf("LoadForRealm token = %q, want at-shalb", got.AccessToken)
	}

	// LoadForRealm(server, "master") must NOT silently hand the tenant
	// token to an admin context — that would be a privilege downgrade
	// (or upgrade, depending on perspective) bug.
	if _, err := m.LoadForRealm(server, "master"); err == nil {
		t.Error("LoadForRealm(master) returned the tenant legacy file — it must refuse on realm mismatch")
	}
}

// TestDeleteForRealm leaves other realms intact.
func TestDeleteForRealm(t *testing.T) {
	m := newTestManager(t)
	const server = "https://kube-api.example.com:6443"

	for _, r := range []string{"shalb", "master"} {
		if err := m.Save(sampleCreds(server, r)); err != nil {
			t.Fatalf("save %s: %v", r, err)
		}
	}
	if err := m.DeleteForRealm(server, "shalb"); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}

	if _, err := m.LoadForRealm(server, "shalb"); err == nil {
		t.Error("tenant creds still loadable after DeleteForRealm(shalb)")
	}
	if _, err := m.LoadForRealm(server, "master"); err != nil {
		t.Errorf("admin creds gone after DeleteForRealm(shalb): %v", err)
	}
}

// TestSlugify guards the realm → filename mapping. Realm names from
// Keycloak are always simple identifiers, but a regression here would
// silently put two realms in the same file.
func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"master":      "master",
		"shalb":       "shalb",
		"demo-org":    "demo-org",
		"":            "default",
		"weird/name":  "weirdname",
		"a..b__c":     "a..b__c",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// encodeCreds duplicates Save's marshal step so the legacy-fallback
// test can produce a byte-for-byte compatible legacy file without
// going through a public API.
func encodeCreds(c *Credentials) ([]byte, error) {
	c.UpdatedAt = time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = c.UpdatedAt
	}
	return json.MarshalIndent(c, "", "  ")
}
