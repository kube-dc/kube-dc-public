package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
)

func TestBootstrapLandingRepoCleanWorkstation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBE_DC_FLEET", "")
	want := filepath.Join(home, ".kube-dc", "fleet")
	repo, tab, err := bootstrapLandingRepo("")
	if err != nil || repo != want || tab != screens.RootTabInit {
		t.Fatalf("repo=%q tab=%v err=%v", repo, tab, err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("landing created files before review: %v", err)
	}
}

func TestBootstrapLandingRepoExistingFleet(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "clusters"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, tab, err := bootstrapLandingRepo(repo)
	if err != nil || got != repo || tab != screens.RootTabFleet {
		t.Fatalf("repo=%q tab=%v err=%v", got, tab, err)
	}
}

func TestBootstrapLandingRepoExplicitMissingPathWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := filepath.Join(home, "old-fleet")
	if err := os.MkdirAll(filepath.Join(old, "clusters"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_DC_FLEET", old)
	want := filepath.Join(home, "new-fleet")
	got, tab, err := bootstrapLandingRepo(want)
	if err != nil || got != want || tab != screens.RootTabInit {
		t.Fatalf("repo=%q tab=%v err=%v", got, tab, err)
	}
}
