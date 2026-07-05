package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// V4 stub contract (plan §13): discoverable command, prints the
// CURRENT manual join path, exits 64 (EX_USAGE) so scripts probing
// for the v2 feature never mistake the instructions for a join.
func TestBootstrapAddNode_WorkerPrintsManualPathAndExits64(t *testing.T) {
	var out bytes.Buffer
	cmd := bootstrapAddNodeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--worker", "192.0.2.10"})
	err := cmd.Execute()

	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() != 64 {
		t.Fatalf("expected exit code 64, got %v", err)
	}
	for _, want := range []string{
		"v2 feature",
		"add-worker.sh",        // the CURRENT canonical join script
		"SERVER_TOKEN",         // its env contract, not the stale one-liner
		"kubectl get nodes -w", // verification step
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q\nFULL:\n%s", want, out.String())
		}
	}
}

func TestBootstrapAddNode_MasterHasNoManualShortcut(t *testing.T) {
	var out bytes.Buffer
	cmd := bootstrapAddNodeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--master", "192.0.2.10"})
	err := cmd.Execute()

	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() != 64 {
		t.Fatalf("expected exit code 64, got %v", err)
	}
	if !strings.Contains(out.String(), "etcd quorum") {
		t.Errorf("master path should explain WHY there's no shortcut\nFULL:\n%s", out.String())
	}
	if strings.Contains(out.String(), "add-worker.sh") {
		t.Errorf("master path must not print the worker join recipe\nFULL:\n%s", out.String())
	}
}
