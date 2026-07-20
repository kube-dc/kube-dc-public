package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveStarterDigest exercises the manifest-HEAD path including
// the anonymous bearer-token dance (the ghcr shape) against a local
// httptest registry.
func TestResolveStarterDigest(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var tokenIssued bool
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenIssued = true
		fmt.Fprintf(w, `{"token":"anon-tok"}`)
	})
	var srvURL string
	mux.HandleFunc("/v2/kube-dc/fleet-starter/manifests/v0.5.1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer anon-tok" {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer realm="%s/token",service="reg",scope="repository:kube-dc/fleet-starter:pull"`, srvURL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	host := strings.TrimPrefix(srv.URL, "http://") // 127.0.0.1:port → http path in resolver
	got, err := resolveStarterDigest("oci://" + host + "/kube-dc/fleet-starter:v0.5.1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != digest {
		t.Errorf("digest = %q, want %q", got, digest)
	}
	if !tokenIssued {
		t.Errorf("expected the anonymous token dance to run")
	}
}

func TestPinStarterDigest_PassthroughAndWarn(t *testing.T) {
	// Already-pinned refs pass through untouched.
	var buf strings.Builder
	pinned := "oci://ghcr.io/kube-dc/fleet-starter:v1@sha256:abc"
	if got := pinStarterDigest(&buf, pinned); got != pinned {
		t.Errorf("pinned ref mutated: %q", got)
	}
	// Unresolvable registry → keep the tag + warn (never brick init).
	buf.Reset()
	tag := "oci://127.0.0.1:1/nope/nope:v1"
	if got := pinStarterDigest(&buf, tag); got != tag {
		t.Errorf("unresolvable ref must stay tagged: %q", got)
	}
	if !strings.Contains(buf.String(), "WARNING") {
		t.Errorf("expected a warning on failed resolution, got %q", buf.String())
	}
}
