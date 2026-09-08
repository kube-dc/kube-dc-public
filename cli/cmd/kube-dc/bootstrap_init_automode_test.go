package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// --mode DEFAULTS to auto. Typing `install` used to be the operator's own
// acknowledgement of the target; the default removes that keystroke, so these
// tests pin the machinery that puts it back.
// --mode stays REQUIRED. Defaulting it to auto was attempted and rejected four
// times; see docs/prd/installer-agentic-tracker.md for the standing blockers.
// This asserts the default so a future flip has to come with that work.
func TestInitModeHasNoDefault(t *testing.T) {
	repo := ""
	f := bootstrapInitCmd(&repo).Flags().Lookup("mode")
	if f == nil {
		t.Fatal("--mode is not registered")
	}
	if f.DefValue != "" {
		t.Fatalf("--mode default = %q, want empty (required) — flipping it to auto needs the tracker's blocker list closed first", f.DefValue)
	}
}

// WIRING TEST — the gap codex called out last time: helper-only tests pass even
// if the RunE call is deleted. This drives the real cobra command so removing
// `guardAutoDetectedMode` from RunE makes it fail.
//
// KUBE_DC_MOCK selects the mock prober, so no real kubeconfig is touched; the
// fixture resolves a mode, and with --no-tty the guard must refuse before the
// engine does anything.
func TestRunE_AutoDetectedModeIsRefusedNonInteractively(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "fresh")

	repo := t.TempDir()
	cmd := bootstrapInitCmd(&repo)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--name", "acme/edge", "--domain", "kdc.example.test",
		"--node-external-ip", "203.0.113.10", "--email", "ops@example.test",
		"--preset", "single-node", "--fleet-mode", "existing",
		"--object-storage-mode", "disabled",
		"--mode", "auto",
		"--no-tty", "--yes",
	})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected the auto-detected mode to be refused non-interactively; output:\n%s", out.String())
	}
	// It must be OUR refusal, not an unrelated validation error — otherwise
	// this test would keep passing with the guard deleted.
	if !errors.Is(err, ErrAutoModeNeedsAcknowledgement) && !errors.Is(err, ErrAutoModeNeedsFetchedTarget) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}

// An explicit --mode is the operator's own word, so the guard must not fire.
// (The run still fails later on other grounds; we assert only that it is not
// OUR error.)
func TestRunE_ExplicitModeIsNotGated(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "fresh")

	repo := t.TempDir()
	cmd := bootstrapInitCmd(&repo)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--mode", "install",
		"--name", "acme/edge", "--domain", "kdc.example.test",
		"--node-external-ip", "203.0.113.10", "--email", "ops@example.test",
		"--preset", "single-node", "--fleet-mode", "existing",
		"--object-storage-mode", "disabled",
		"--no-tty", "--yes", "--dry-run",
	})
	err := cmd.ExecuteContext(context.Background())
	if errors.Is(err, ErrAutoModeNeedsAcknowledgement) || errors.Is(err, ErrAutoModeNeedsFetchedTarget) {
		t.Fatalf("explicit --mode must never be gated: %v", err)
	}
}

// The guard's decision table. Every auto-detected mode is gated, not just
// install: all three share an apply engine that labels nodes, creates repos and
// runs flux-install, and parts of that run before the adopt gate.
func TestGuardAutoDetectedMode_GatesEveryMode(t *testing.T) {
	for _, m := range []clusterinit.Mode{clusterinit.ModeInstall, clusterinit.ModeAdopt, clusterinit.ModeResume} {
		t.Run(string(m), func(t *testing.T) {
			var out bytes.Buffer
			o := clusterinit.InitOptions{NoTTY: true}
			err := guardAutoDetectedMode(&out, &o, modeResolution{
				Mode: m, AutoDetected: true, Reason: "probe",
				Identity: clusterIdentity{Endpoint: "https://10.0.0.1:6443", UID: "uid-abc"},
			})
			if !errors.Is(err, ErrAutoModeNeedsAcknowledgement) {
				t.Fatalf("mode %s must be gated; got %v", m, err)
			}
			if !strings.Contains(err.Error(), "uid-abc") || !strings.Contains(err.Error(), "10.0.0.1") {
				t.Errorf("refusal must name the cluster it was decided against; got %v", err)
			}
		})
	}
}

// --ssh-host means the run will fetch ANOTHER cluster's kubeconfig and make it
// current, so the local kubeconfig is not evidence about the target. Refuse
// rather than decide from the wrong cluster.
func TestGuardAutoDetectedMode_RefusesWhenAnSSHTargetWillReplaceTheKubeconfig(t *testing.T) {
	var out bytes.Buffer
	o := clusterinit.InitOptions{SSHHost: "root@newnode"}
	err := guardAutoDetectedMode(&out, &o, modeResolution{
		Mode: clusterinit.ModeInstall, AutoDetected: true,
		Identity: clusterIdentity{Endpoint: "https://10.0.0.1:6443", UID: "uid-local"},
	})
	if !errors.Is(err, ErrAutoModeNeedsFetchedTarget) {
		t.Fatalf("auto + --ssh-host must be refused; got %v", err)
	}
	if !strings.Contains(err.Error(), "fetch-kubeconfig") {
		t.Errorf("refusal must state the remedy; got %v", err)
	}
}

// Explicit modes and dry-runs are not gated.
func TestGuardAutoDetectedMode_NoOps(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    clusterinit.InitOptions
		res  modeResolution
	}{
		{"explicit mode", clusterinit.InitOptions{NoTTY: true}, modeResolution{Mode: clusterinit.ModeInstall}},
		{"dry-run mutates nothing remote", clusterinit.InitOptions{NoTTY: true, DryRun: true}, modeResolution{Mode: clusterinit.ModeInstall, AutoDetected: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			o := tc.o
			if err := guardAutoDetectedMode(&out, &o, tc.res); err != nil {
				t.Fatalf("guard must not fire: %v", err)
			}
		})
	}
}

// Two clusters can classify identically ("reachable, no flux-system" is true of
// every fresh cluster), so mode agreement is not cluster agreement. Only a UID
// match proves sameness, and two unknown UIDs must NOT count as equal.
func TestClusterIdentity_SameClusterNeedsAUID(t *testing.T) {
	a := clusterIdentity{Endpoint: "https://a:6443", UID: "uid-1"}
	if !a.sameCluster(clusterIdentity{Endpoint: "https://tunnel:16443", UID: "uid-1"}) {
		t.Error("same UID behind a different endpoint is the same cluster")
	}
	if a.sameCluster(clusterIdentity{Endpoint: "https://a:6443", UID: "uid-2"}) {
		t.Error("same endpoint with a different UID is NOT the same cluster")
	}
	if (clusterIdentity{}).sameCluster(clusterIdentity{}) {
		t.Error("two unknown identities must not be treated as proven-equal")
	}
}

// Mode provenance: what gets persisted must be the operator's REQUEST. A spec
// that froze the resolved verdict reads back as an explicit decision on the
// next run, against whatever cluster the kubeconfig then points at.
func TestPersistedModeIsTheRequestNotTheVerdict(t *testing.T) {
	o := clusterinit.InitOptions{Mode: clusterinit.ModeAuto}
	_, _, err := clusterinit.ResolveMode(context.Background(), &o, fakeProber{
		in: clusterinit.ModeProbeInputs{K8sReachable: true, ClusterUID: "u1"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if o.Mode != clusterinit.ModeInstall {
		t.Fatalf("effective mode should be the verdict, got %s", o.Mode)
	}
	if o.RequestedMode != clusterinit.ModeAuto {
		t.Fatalf("RequestedMode = %q, want auto — the request must survive resolution", o.RequestedMode)
	}
}

type fakeProber struct{ in clusterinit.ModeProbeInputs }

func (f fakeProber) Probe(context.Context) (clusterinit.ModeProbeInputs, error) { return f.in, nil }
