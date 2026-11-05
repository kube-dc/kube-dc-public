package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func TestMissingPyYAMLBlocksInitBeforeStarterOrKeyWrites(t *testing.T) {
	directory := t.TempDir()
	python := filepath.Join(directory, "python3")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'Python 3.12.3'; exit 0; fi\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("KUBE_DC_MOCK", "")
	repo := filepath.Join(directory, "must-not-exist")
	o := &clusterinit.InitOptions{Repo: repo, FleetMode: clusterinit.FleetNewRepo, NoPush: true, NoSSH: true, NoInstallPrereqs: true}
	err := runApplyEngine(context.Background(), io.Discard, o, &clusterinit.Plan{FleetMode: clusterinit.FleetNewRepo}, clusterinit.NopReporter{}, false, clusterIdentity{})
	if err == nil || !strings.Contains(err.Error(), "PyYAML") {
		t.Fatalf("missing PyYAML repair: %v", err)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatal("starter or key preparation occurred before tool blocker")
	}
}
