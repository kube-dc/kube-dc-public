package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

const (
	changeOldRef = "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16@sha256:" +
		"e61c7f9d00000000000000000000000000000000000000000000000000000000"
	changeNewRef = "oci://ghcr.io/kube-dc/fleet-starter:v0.9.16@sha256:" +
		"691a397400000000000000000000000000000000000000000000000000000000"
)

func importedStarterRepo(t *testing.T, overlay bool) string {
	t.Helper()
	repo := t.TempDir()
	files := []string{
		"bootstrap/add-cluster.sh",
		"infrastructure/kube-ovn-network-public/kustomization.yaml",
		"infrastructure/ext-net-bridge-tag/kustomization.yaml",
		"platform/kustomization.yaml",
		"addons/metallb/kustomization.yaml",
		"addons/metallb-config/kustomization.yaml",
		"addons/metallb-config-bgp/kustomization.yaml",
		"scripts/install-prerequisites.sh",
		"clusters/.gitkeep",
	}
	if overlay {
		files = append(files, "clusters/site/cluster-config.env")
	}
	for _, f := range files {
		p := filepath.Join(repo, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record := "version: v0.9.16\ninstalledRef: " + changeOldRef + "\ninstalledDigest: sha256:e61c7f9d00000000000000000000000000000000000000000000000000000000\n"
	if err := os.WriteFile(filepath.Join(repo, ".starter-version"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	return repo
}

// fakeRegistry answers manifest HEADs for tag with digest, as a loopback
// registry (plain HTTP).
func fakeRegistry(t *testing.T, tag, digest string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifests/"+tag) {
			w.Header().Set("Docker-Content-Digest", digest)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestStarterRefForPlan(t *testing.T) {
	t.Run("installed checkout keeps its starter whatever the default", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, true), FleetMode: clusterinit.FleetExistingRepo}
		if got := starterRefForPlan(&out, o); got != changeOldRef || out.Len() != 0 {
			t.Fatalf("got %q, output %q", got, out.String())
		}
		o.StarterRef = starterRefForPlan(&out, o)
		if err := checkStarterChange(&out, o); err != nil {
			t.Fatalf("a resumed installation was refused: %v", err)
		}
	})
	t.Run("installed checkout warns about an explicit other starter", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, true), FleetMode: clusterinit.FleetNewRepo, StarterRef: changeNewRef}
		if got := starterRefForPlan(&out, o); got != changeOldRef || !strings.Contains(out.String(), "init keeps the installed starter") {
			t.Fatalf("got %q, output %q", got, out.String())
		}
	})
	t.Run("a saved plan that kept the installed starter replays", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, true), FleetMode: clusterinit.FleetExistingRepo, StarterRef: changeNewRef}
		reviewed := starterRefForPlan(&out, o)
		if got := starterRefForReplay(&out, o, reviewed); got != reviewed || got != changeOldRef {
			t.Fatalf("replay chose %q, the plan reviewed %q", got, reviewed)
		}
	})
	t.Run("moved tag before any overlay replaces the starter", func(t *testing.T) {
		host := fakeRegistry(t, "v0.9.16", "sha256:691a397400000000000000000000000000000000000000000000000000000000")
		repo := importedStarterRepo(t, false)
		old := "oci://" + host + "/kube-dc/fleet-starter:v0.9.16@sha256:e61c7f9d00000000000000000000000000000000000000000000000000000000"
		record := "installedRef: " + old + "\ninstalledDigest: sha256:e61c7f9d00000000000000000000000000000000000000000000000000000000\n"
		if err := os.WriteFile(filepath.Join(repo, ".starter-version"), []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCommitAllT(t, repo)
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: repo, FleetMode: clusterinit.FleetExistingRepo,
			StarterRef: "oci://" + host + "/kube-dc/fleet-starter:v0.9.16"}
		got := starterRefForPlan(&out, o)
		if !strings.HasSuffix(got, "@sha256:691a397400000000000000000000000000000000000000000000000000000000") {
			t.Fatalf("got %q, output %q", got, out.String())
		}
		o.StarterRef = got
		if err := checkStarterChange(&out, o); err != nil || !strings.Contains(out.String(), "the import step replaces it") {
			t.Fatalf("err=%v output %q", err, out.String())
		}
	})
}

func TestCheckStarterChange(t *testing.T) {
	t.Run("safe change is reported", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, false), FleetMode: clusterinit.FleetExistingRepo, StarterRef: changeNewRef}
		if err := checkStarterChange(&out, o); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "the import step replaces it with "+changeNewRef) {
			t.Fatalf("output: %s", out.String())
		}
	})
	t.Run("same starter is silent", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, false), FleetMode: clusterinit.FleetNewRepo, StarterRef: changeOldRef}
		if err := checkStarterChange(&out, o); err != nil || out.Len() != 0 {
			t.Fatalf("err=%v out=%q", err, out.String())
		}
	})
	t.Run("dirty checkout refuses", func(t *testing.T) {
		repo := importedStarterRepo(t, false)
		if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o := &clusterinit.InitOptions{Repo: repo, FleetMode: clusterinit.FleetExistingRepo, StarterRef: changeNewRef}
		if err := checkStarterChange(&bytes.Buffer{}, o); !errors.Is(err, clusterinit.ErrStarterChangeRefused) || !strings.Contains(err.Error(), "notes.txt") {
			t.Fatalf("want refusal naming notes.txt, got %v", err)
		}
	})
	t.Run("overlay refuses", func(t *testing.T) {
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, true), FleetMode: clusterinit.FleetExistingRepo, StarterRef: changeNewRef}
		if err := checkStarterChange(&bytes.Buffer{}, o); !errors.Is(err, clusterinit.ErrStarterChangeRefused) {
			t.Fatalf("want refusal, got %v", err)
		}
	})
	t.Run("unpinned replacement refuses", func(t *testing.T) {
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, false), FleetMode: clusterinit.FleetExistingRepo,
			StarterRef: "oci://ghcr.io/kube-dc/fleet-starter:v0.9.17"}
		if err := checkStarterChange(&bytes.Buffer{}, o); err == nil || !strings.Contains(err.Error(), "pinned by digest") {
			t.Fatalf("want digest refusal, got %v", err)
		}
	})
	t.Run("existing-fleet never changes the starter", func(t *testing.T) {
		var out bytes.Buffer
		o := &clusterinit.InitOptions{Repo: importedStarterRepo(t, true), FleetMode: clusterinit.FleetExistingFleet,
			StarterRef: "oci://ghcr.io/kube-dc/fleet-starter:v0.9.17"}
		if err := checkStarterChange(&out, o); err != nil || out.Len() != 0 {
			t.Fatalf("existing-fleet: err=%v out=%q", err, out.String())
		}
		if got := starterRefForPlan(&out, o); got != o.StarterRef || out.Len() != 0 {
			t.Fatalf("starterRefForPlan pinned %q for an existing fleet: %s", got, out.String())
		}
	})
}

func gitCommitAllT(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "update")
}
