package config

import (
	"strings"
	"testing"
)

func TestLoadRealmSpecificCredentialsRequiresRealm(t *testing.T) {
	const server = "https://kube-api.example.com:6443"

	// Only AMBIGUITY is refused. A single cached identity is unambiguous and must
	// still resolve: refusing it would break every kubeconfig context written
	// before contexts carried --realm, failing each kubectl call after a CLI
	// upgrade until the user logged in again.
	tests := []struct {
		name   string
		realms []string
	}{
		{name: "tenant and admin identities", realms: []string{"acme", "master"}},
		{name: "three identities", realms: []string{"acme", "beta", "master"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(t)
			for _, realm := range tt.realms {
				if err := m.Save(sampleCreds(server, realm)); err != nil {
					t.Fatalf("save %s: %v", realm, err)
				}
			}

			_, err := m.Load(server)
			if err == nil {
				t.Fatal("unqualified Load selected a realm-specific identity; want a fail-closed error")
			}
			if !strings.Contains(err.Error(), "this context names none") {
				t.Fatalf("unqualified load error = %q, want realm guidance", err)
			}
		})
	}
}

// The gaps the test above leaves: it proves an UNQUALIFIED load fails closed,
// but not that a QUALIFIED one picks the right identity, nor how the legacy
// single-file entry coexists with realm-keyed ones. Both are where an
// Organization context could silently run as admin, so both are pinned here.

func TestLoadForRealmSelectsTheMatchingIdentity(t *testing.T) {
	const server = "https://kube-api.example.com:6443"
	m := newTestManager(t)
	for _, realm := range []string{"acme", "master"} {
		if err := m.Save(sampleCreds(server, realm)); err != nil {
			t.Fatalf("save %s: %v", realm, err)
		}
	}
	for _, want := range []string{"acme", "master"} {
		got, err := m.LoadForRealm(server, want)
		if err != nil {
			t.Fatalf("LoadForRealm(%q): %v", want, err)
		}
		if got.Realm != want {
			t.Fatalf("LoadForRealm(%q) returned realm %q — kubectl would run as the wrong identity", want, got.Realm)
		}
	}
}

// A pre-realm login left one file with no realm recorded, because the field did
// not exist. That must keep working, and must not shadow realm-keyed entries.
func TestLegacyEntryCoexistsWithRealmKeyedOnes(t *testing.T) {
	const server = "https://kube-api.example.com:6443"
	m := newTestManager(t)
	if err := m.Save(sampleCreds(server, "")); err != nil { // legacy: no realm
		t.Fatalf("save legacy: %v", err)
	}
	if err := m.Save(sampleCreds(server, "acme")); err != nil {
		t.Fatalf("save acme: %v", err)
	}

	legacy, err := m.Load(server)
	if err != nil {
		t.Fatalf("a genuine pre-realm entry must still load: %v", err)
	}
	if legacy.Realm != "" {
		t.Fatalf("unqualified Load returned realm %q; it must return the legacy entry", legacy.Realm)
	}
	acme, err := m.LoadForRealm(server, "acme")
	if err != nil || acme.Realm != "acme" {
		t.Fatalf("a realm-keyed load must not be shadowed by the legacy entry: %v / %+v", err, acme)
	}
}

// The guarantee that actually prevents identity confusion, and which had no
// test: a realm-KEYED load may fall back to the legacy file only when that
// file's embedded realm matches. Without this, an admin context on a host
// carrying a pre-upgrade Organization token would run as that Organization —
// or the reverse.
//
// Note the legacy file DOES record its realm; only the filename lacked it. So
// refusing every realm-naming legacy file would break the in-place upgrade this
// fallback exists for (see TestLoad_LegacyFallback) — the match is the fix, not
// a refusal.
func TestLoadForRealmRefusesAMismatchedLegacyFile(t *testing.T) {
	const server = "https://kube-api.example.com:6443"
	m := newTestManager(t)
	if err := m.Save(sampleCreds(server, "")); err != nil { // legacy slot
		t.Fatalf("save legacy: %v", err)
	}
	// Give the legacy entry an Organization identity.
	legacy := sampleCreds(server, "")
	legacy.Realm = "acme"
	if err := m.Save(legacy); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := m.LoadForRealm(server, "master"); err == nil {
		t.Fatal("an admin context must not fall back to an Organization's legacy token")
	}
	got, err := m.LoadForRealm(server, "acme")
	if err != nil {
		t.Fatalf("the matching realm must still resolve: %v", err)
	}
	if got.Realm != "acme" {
		t.Fatalf("got realm %q, want acme", got.Realm)
	}
}

// The upgrade path. A context written before --realm existed has no realm, but
// the user's single login was saved under a realm-keyed file. Refusing that
// combination fails every kubectl call until they log in again — a regression
// the release-gate review caught before it shipped.
func TestLoadResolvesASingleCachedIdentityWithoutARealm(t *testing.T) {
	const server = "https://kube-api.example.com:6443"
	m := newTestManager(t)
	if err := m.Save(sampleCreds(server, "acme")); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := m.Load(server)
	if err != nil {
		t.Fatalf("one cached identity is unambiguous and must resolve: %v", err)
	}
	if got.Realm != "acme" {
		t.Fatalf("got realm %q, want acme", got.Realm)
	}
}
