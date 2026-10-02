package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
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

func TestRequireGreenfieldStarterDigest(t *testing.T) {
	o := &clusterinit.InitOptions{FleetMode: clusterinit.FleetNewRepo, StarterRef: "oci://ghcr.io/kube-dc/fleet-starter:v1"}
	if err := requireGreenfieldStarterDigest(o); err == nil {
		t.Fatal("mutable greenfield starter accepted")
	}
	o.StarterRef += "@sha256:" + strings.Repeat("a", 64)
	if err := requireGreenfieldStarterDigest(o); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"garbage@sha256:" + strings.Repeat("a", 64),
		"oci://ghcr.io/x@sha256:" + strings.Repeat("A", 64),
		"oci://ghcr.io/x?query@sha256:" + strings.Repeat("a", 64),
	} {
		o.StarterRef = bad
		if err := requireGreenfieldStarterDigest(o); err == nil {
			t.Errorf("invalid starter ref %q accepted", bad)
		}
	}
	o.FleetMode = clusterinit.FleetExistingFleet
	o.StarterRef = "oci://ghcr.io/kube-dc/fleet-starter:v1"
	if err := requireGreenfieldStarterDigest(o); err != nil {
		t.Fatal(err)
	}
}

func TestExistingStarterDoesNotRequireRegistryPin(t *testing.T) {
	repo := t.TempDir()
	for _, marker := range []string{
		"bootstrap/add-cluster.sh",
		"infrastructure/kube-ovn-network-public/kustomization.yaml",
		"infrastructure/ext-net-bridge-tag/kustomization.yaml",
		"platform/kustomization.yaml",
		"addons/metallb/kustomization.yaml",
		"addons/metallb-config/kustomization.yaml",
		"addons/metallb-config-bgp/kustomization.yaml",
		"scripts/install-prerequisites.sh",
	} {
		path := filepath.Join(repo, marker)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []clusterinit.FleetMode{clusterinit.FleetExistingRepo, clusterinit.FleetNewRepo} {
		o := &clusterinit.InitOptions{FleetMode: mode, Repo: repo, StarterRef: "oci://127.0.0.1:1/unavailable:v1"}
		var out strings.Builder
		if ref := starterRefForPlan(&out, o); ref != o.StarterRef {
			t.Fatalf("%s: changed local starter ref to %q", mode, ref)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: contacted registry for local starter: %q", mode, out.String())
		}
		if err := requireGreenfieldStarterDigest(o); err != nil {
			t.Fatalf("%s: rejected local starter: %v", mode, err)
		}
		reviewed := o.StarterRef + "@sha256:" + strings.Repeat("a", 64)
		if got := starterRefForReplay(&out, o, reviewed); got != reviewed {
			t.Fatalf("%s: resumed plan changed reviewed ref to %q", mode, got)
		}
	}
	// The default starter ref must also replay with its saved digest when a
	// first attempt has already extracted the starter.
	o := &clusterinit.InitOptions{FleetMode: clusterinit.FleetNewRepo, Repo: repo}
	reviewed := resolveStarterRef("") + "@sha256:" + strings.Repeat("b", 64)
	if got := starterRefForReplay(io.Discard, o, reviewed); got != reviewed {
		t.Fatalf("default ref replay changed reviewed digest to %q", got)
	}
	o.StarterRef = "oci://ghcr.io/other/starter@sha256:" + strings.Repeat("c", 64)
	if got := starterRefForReplay(io.Discard, o, reviewed); got != o.StarterRef {
		t.Fatalf("explicit ref change was hidden by replay: %q", got)
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
