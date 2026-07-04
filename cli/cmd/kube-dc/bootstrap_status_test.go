package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// writeFleetFixture builds a minimal fleet-repo shape on disk so
// the status cobra command can exercise ListClusters end-to-end.
// Each entry is (path-under-clusters, env-body).
func writeFleetFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range entries {
		dir := filepath.Join(root, "clusters", path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		envFile := filepath.Join(dir, "cluster-config.env")
		if err := os.WriteFile(envFile, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const minimalEnv = `DOMAIN=acme.example.com
NODE_EXTERNAL_IP=213.111.154.233
KUBE_API_EXTERNAL_URL=https://kube-api.acme.example.com:6443
KEYCLOAK_HOSTNAME=login.acme.example.com
EXT_NET_NAME=ext-cloud
`

// invalidAPIEnv has a syntactically-valid kubeAPIURL pointing at an
// address that won't ever respond — drives the probe to
// Unreachable in tests.
const invalidAPIEnv = `DOMAIN=invalid.example.com
NODE_EXTERNAL_IP=0.0.0.0
KUBE_API_EXTERNAL_URL=https://127.0.0.1:1
KEYCLOAK_HOSTNAME=login.invalid.example.com
EXT_NET_NAME=ext-cloud
`

// missingAPIEnv drops kubeAPIURL entirely → probe returns Unknown
// with the "no KUBE_API_EXTERNAL_URL" detail.
const missingAPIEnv = `DOMAIN=noapi.example.com
NODE_EXTERNAL_IP=0.0.0.0
KEYCLOAK_HOSTNAME=login.noapi.example.com
EXT_NET_NAME=ext-cloud
`

func TestBootstrapStatusCmd_ListAllClusters(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{
		"cloud":  minimalEnv,
		"stage":  missingAPIEnv,
		"cs/zrh": missingAPIEnv,
	})
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	_ = cmd.Execute() // exit code may be non-zero — driven by probe outcomes; we just check shape

	body := out.String()
	mustContainAll_status(t, body, "cloud", "stage", "cs/zrh")
}

func TestBootstrapStatusCmd_EmptyFleet_NoError(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("empty fleet should not error: %v", err)
	}
	if !strings.Contains(out.String(), "no clusters found") {
		t.Errorf("expected 'no clusters found' message, got: %q", out.String())
	}
}

func TestBootstrapStatusCmd_DeepView_KnownCluster(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": missingAPIEnv})
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})

	_ = cmd.Execute()

	body := out.String()
	mustContainAll_status(t, body,
		"Cluster: cloud",
		"Domain:",
		"Status:",
	)
}

func TestBootstrapStatusCmd_DeepView_UnknownCluster_Errors(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": missingAPIEnv})

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"does-not-exist", "--no-tty"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown cluster name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not-found: %v", err)
	}
	if !strings.Contains(err.Error(), "cloud") {
		t.Errorf("error should list known clusters: %v", err)
	}
}

func TestBootstrapStatusCmd_MissingAPI_ReturnsUnknown(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": missingAPIEnv})

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	if !strings.Contains(body, "Unknown") {
		t.Errorf("missing kubeAPIURL should produce Unknown status: %q", body)
	}
	if !strings.Contains(body, "no KUBE_API_EXTERNAL_URL") {
		t.Errorf("Detail should explain the missing KUBE_API_EXTERNAL_URL: %q", body)
	}
}

func TestExitCodeForClusterStatus(t *testing.T) {
	cases := []struct {
		status discover.ClusterStatus
		want   int
	}{
		{discover.StatusReady, 0},
		{discover.StatusReconciling, 1},
		{discover.StatusDrifted, 1},
		{discover.StatusFailed, 2},
		{discover.StatusUnreachable, 2},
		{discover.StatusUnknown, 2},
	}
	for _, c := range cases {
		if got := exitCodeForClusterStatus(c.status); got != c.want {
			t.Errorf("exitCodeForClusterStatus(%v) = %d, want %d", c.status, got, c.want)
		}
	}
}

func TestRenderStatusPill_NoTTYIsPlain(t *testing.T) {
	got := renderStatusPill("Ready", discover.StatusReady, true)
	if got != "Ready" {
		t.Errorf("no-tty pill = %q, want plain 'Ready'", got)
	}
}

func TestRenderStatusPill_TTYAddsLipgloss(t *testing.T) {
	// We can't reliably assert on ANSI codes against a buffer
	// (lipgloss auto-detects writer + may strip), but the function
	// should at least not panic and return a non-empty string.
	got := renderStatusPill("Failed", discover.StatusFailed, false)
	if got == "" {
		t.Error("TTY pill should not be empty")
	}
}

func TestBootstrapStatusCmd_NoRepoErrors(t *testing.T) {
	// Empty repo flag + no $KUBE_DC_FLEET + no ~/.kube-dc/fleet
	// → resolveFleetRepo returns an error.
	t.Setenv("KUBE_DC_FLEET", "")
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no fleet repo can be resolved")
	}
	if !strings.Contains(err.Error(), "no fleet repo") {
		t.Errorf("error should mention 'no fleet repo': %v", err)
	}
}

func TestBootstrapStatusCmd_DoctorExitCodeBubble(t *testing.T) {
	// One cluster pointing at an address that resolves but doesn't
	// listen — the probe should surface Unreachable (status code 2)
	// and the cobra command should return a doctorExitCodeErr with
	// code 2. This is the critical CI contract — assert it hard.
	//
	// Earlier iteration of this test had an "if err == nil, log and
	// skip" branch which let a regression where missing kubeAPIURL
	// silently routed to Unknown→exit 0 slip through. M2 review-pass
	// removed that escape: any non-2 outcome here is a real bug.
	repo := writeFleetFixture(t, map[string]string{"down": invalidAPIEnv})

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected Unreachable to bubble up as doctorExitCodeErr, got nil err; output:\n%s", out.String())
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 2 {
		t.Fatalf("Unreachable cluster exit code = %d, want 2; output:\n%s", de.ExitCode(), out.String())
	}
	// And the table must actually say Unreachable — guard against a
	// future regression that swaps Unreachable→Unknown silently.
	body := out.String()
	if !strings.Contains(body, "Unreachable") {
		t.Fatalf("status output should contain 'Unreachable', got:\n%s", body)
	}
}

// ---------- mock acceptance ----------
//
// installer-agentic-implementation-plan's M2 acceptance contract:
//
//	KUBE_DC_MOCK=cloud           bootstrap status --no-tty   → exit 0
//	KUBE_DC_MOCK=openbao-sealed  bootstrap status --no-tty   → exit 1
//	KUBE_DC_MOCK=fresh           bootstrap status --no-tty   → exit 0 (no clusters)
//
// These tests exercise the cobra surface end-to-end via the embedded
// scenarios in cli/internal/bootstrap/mock/scenarios/. The
// scenario→Probes wiring lives in bootstrap.NewSession; if a future
// refactor breaks it (e.g. status command silently bypasses
// session.Probes), these tests fail loudly.
//
// Caveat: when KUBE_DC_MOCK is set, bootstrap.NewSession does NOT
// touch any kubeconfig (the mock adapters are pure scenario data),
// so these tests don't need t.Setenv("KUBECONFIG", ...).

func TestBootstrapStatusCmd_MockCloud_ExitZero(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("KUBE_DC_MOCK=cloud should exit 0, got err=%v; output:\n%s", err, out.String())
	}
	body := out.String()
	mustContainAll_status(t, body, "cloud", "stage", "cs/zrh", "Ready")
}

func TestBootstrapStatusCmd_MockOpenBaoSealed_ExitOne(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "openbao-sealed")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("KUBE_DC_MOCK=openbao-sealed should exit non-zero, got nil; output:\n%s", out.String())
	}
	var de *doctorExitCodeErr
	if !errors.As(err, &de) {
		t.Fatalf("expected doctorExitCodeErr, got %T: %v", err, err)
	}
	if de.ExitCode() != 1 {
		t.Fatalf("openbao-sealed exit code = %d, want 1 (Reconciling); output:\n%s", de.ExitCode(), out.String())
	}
	body := out.String()
	mustContainAll_status(t, body, "cloud", "Reconciling")
}

func TestBootstrapStatusCmd_MockFresh_NoClustersExitZero(t *testing.T) {
	// fresh scenario has `fleet: null` (no fleet repo on disk yet) —
	// status should print the "no clusters found" message and exit 0.
	t.Setenv("KUBE_DC_MOCK", "fresh")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("KUBE_DC_MOCK=fresh with no fleet should exit 0, got err=%v; output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "no clusters found") {
		t.Errorf("expected 'no clusters found' message, got:\n%s", out.String())
	}
}

func TestBootstrapStatusCmd_MockCloudDeepView_PrintsReconcilers(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("KUBE_DC_MOCK=cloud deep view should exit 0, got err=%v; output:\n%s", err, out.String())
	}
	body := out.String()
	mustContainAll_status(t, body,
		"Cluster: cloud",
		"Domain:  kube-dc.cloud",
		"API:     https://kube-api.kube-dc.cloud:6443",
		"Reconcilers:",
		"infra-cni",
		"platform",
	)
}

// M6-T04: deep view renders a Nodes: section fed by the M1-T04
// NFDDetect reader against the session's K8sClient. Mock cloud has
// 4 nodes with M6-T02 composites (3 kubevirt-eligible + 1 NVIDIA GPU).
// This test locks the section headers, counts, and node lists so a
// future regression that silently drops the Nodes wiring or renames
// a label constant surfaces here rather than through a live smoke.
func TestBootstrapStatusCmd_MockCloudDeepView_PrintsNodesSection(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("mock cloud deep view should exit 0, got %v; output:\n%s", err, out.String())
	}

	body := out.String()
	mustContainAll_status(t, body,
		"Nodes:",
		"Total:             4",
		"KubeVirt-eligible: 3",
		"ams1-blade179-8",
		"ams1-blade184-5",
		"ams1-blade58-2",
		"NVIDIA GPU:        1",
		"ams1-blade187-2",
		"AMD GPU:           0",
		"Source:            M6-T02 composites",
	)
}

// M6-T04: openbao-sealed scenario has NFD installed via infra-core
// but represents the sealed-cascade state; Nodes section should
// still render because NFDDetect is orthogonal to reconciler
// readiness. Also proves the section renders for a Reconciling
// (exit 1) cluster, not just Ready.
func TestBootstrapStatusCmd_MockOpenBaoSealedDeepView_RendersNodes(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "openbao-sealed")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	empty := ""
	cmd := bootstrapStatusCmd(&empty)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})

	_ = cmd.Execute() // Reconciling → exit 1, expected; we assert body

	body := out.String()
	mustContainAll_status(t, body,
		"Nodes:",
		"Total:             4",
		"KubeVirt-eligible: 3",
		"NVIDIA GPU:        1",
	)
}

// M6-T04: real-mode deep view against a resolved fleet entry where
// the session's K8sClient is nil (no kubeconfig) — Nodes section must
// be silently absent, not print an error or an empty block. This
// mirrors the doctor's NFD-skipped path and is the shape a status
// deep view runs in when the operator inspects a cluster from a
// laptop without kubeconfig loaded.
func TestBootstrapStatusCmd_NoKubeconfig_NodesSectionAbsent(t *testing.T) {
	repo := writeFleetFixture(t, map[string]string{"cloud": missingAPIEnv})
	t.Setenv("KUBE_DC_MOCK", "")
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-status-nodes")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := repo
	cmd := bootstrapStatusCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cloud", "--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	if strings.Contains(body, "Nodes:") {
		t.Errorf("Nodes: section should be absent without a K8sClient\nOUTPUT:\n%s", body)
	}
}

// ---------- helpers ----------

func mustContainAll_status(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(body, f) {
			t.Errorf("output missing %q\nFULL:\n%s", f, body)
		}
	}
}
