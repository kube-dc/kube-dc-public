package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A wrong organization must fail HERE, with an explanation, rather than as a
// bare 404 in the user's browser. The mistake is not a typo: `login` asks for
// the ORGANIZATION while tenants live in a PROJECT, and project namespaces are
// `<org>-<project>` — so someone in org "test2" on project "test2-2" naturally
// enters "test2-2" (reported 2026-07-20).
func TestVerifyRealmExists_UnknownRealmExplainsOrgVsProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := verifyRealmExists(srv.URL, "test2-2", "")
	if err == nil {
		t.Fatal("want an error for an unknown realm, got nil — the user would land on a browser 404")
	}
	msg := err.Error()
	for _, want := range []string{"test2-2", "ORGANIZATION", "<org>-<project>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must mention %q so the user can self-serve; got:\n%s", want, msg)
		}
	}
}

func TestVerifyRealmExists_ExistingRealmPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/realms/test2/.well-known/openid-configuration") {
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := verifyRealmExists(srv.URL, "test2", ""); err != nil {
		t.Fatalf("a valid realm must not be blocked: %v", err)
	}
}

// Fails OPEN on anything that is not a definitive 404. A proxy, captive portal,
// offline resolver or self-signed chain must never be able to block a login
// that would otherwise have succeeded — this check is a convenience, not an
// authorization gate.
func TestVerifyRealmExists_UnreachableFailsOpen(t *testing.T) {
	// Port 1 on loopback: connection refused, not a 404.
	if err := verifyRealmExists("https://127.0.0.1:1", "anything", ""); err != nil {
		t.Fatalf("must fail open when Keycloak is unreachable, got: %v", err)
	}
}

// Any non-404 status (500, 502 from a gateway, 403 from a WAF) is inconclusive
// about whether the realm exists, so it must not block either.
func TestVerifyRealmExists_ServerErrorFailsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	if err := verifyRealmExists(srv.URL, "test2", ""); err != nil {
		t.Fatalf("a 502 says nothing about the realm; must fail open, got: %v", err)
	}
}
