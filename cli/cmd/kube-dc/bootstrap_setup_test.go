package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func TestBootstrapSetupValidateNeedsNoFleetOrKubeconfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBE_DC_FLEET", filepath.Join(dir, "absent-fleet"))
	t.Setenv("KUBECONFIG", filepath.Join(dir, "absent-kubeconfig"))
	const validConfig = "CLUSTER_NAME=demo\nDOMAIN=example.test\nNODE_EXTERNAL_IP=192.0.2.10\nEMAIL=ops@example.test\nKUBE_DC_INIT_PRESET=internal-only\nKUBE_DC_INIT_FLEET_MODE=new-repo\nOBJECT_STORAGE_MODE=disabled\n"
	if err := os.WriteFile(filepath.Join(dir, "cluster.env"), []byte(validConfig), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), []byte(`{"version":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := setup.Spec{
		SchemaVersion: setup.SchemaVersion, Name: "demo", Profile: "evaluation@v1",
		Release:      setup.Release{RecordFile: "release.json", StarterRef: "oci://ghcr.io/kube-dc/fleet-starter@sha256:" + strings.Repeat("a", 64), RKE2Version: "v1.33.0+rke2r1"},
		Target:       setup.Target{Intent: clusterinit.ModeAuto},
		Hosts:        []setup.Host{{ID: "server-1", SSHAlias: "admin@server-1", Role: "server", ManagementAddress: "192.0.2.10"}},
		Platform:     setup.Platform{ConfigFile: "cluster.env"},
		Verification: setup.Verification{Capabilities: []string{"containers"}},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := bootstrapCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"setup", "validate", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Local setup specification is valid", "Cluster: demo", "Input hash:", "Live readiness, release qualification, and effects planning have not run"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %q", want, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "absent-fleet")); !os.IsNotExist(err) {
		t.Fatalf("validation wrote Fleet path: %v", err)
	}
	planCmd := bootstrapCmd()
	var planOutput bytes.Buffer
	planCmd.SetOut(&planOutput)
	planCmd.SetErr(&planOutput)
	planCmd.SetArgs([]string{"setup", "plan", "--config", path, "--format", "json"})
	if err := planCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var preview setup.Preview
	if err := json.Unmarshal(planOutput.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Operation != "plan" || preview.State != "planned" || preview.ReadyToApply || len(preview.Stages) == 0 || len(preview.Unresolved) == 0 {
		t.Fatalf("local plan claimed readiness or omitted stages: %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent-fleet")); !os.IsNotExist(err) {
		t.Fatalf("planning wrote Fleet path: %v", err)
	}
	releaseCmd := bootstrapCmd()
	var releaseOutput bytes.Buffer
	releaseCmd.SetOut(&releaseOutput)
	releaseCmd.SetErr(&bytes.Buffer{})
	releaseCmd.SetArgs([]string{"setup", "release", "--config", path, "--format", "json"})
	if err := releaseCmd.Execute(); err == nil {
		t.Fatal("incomplete release record accepted")
	}
	var releaseReport setup.ReleaseReport
	if err := json.Unmarshal(releaseOutput.Bytes(), &releaseReport); err != nil {
		t.Fatalf("invalid release JSON: %v: %s", err, releaseOutput.String())
	}
	if releaseReport.State != "blocked" || releaseReport.ReadyToApply || len(releaseReport.Checks) == 0 {
		t.Fatalf("incorrect release status: %+v", releaseReport)
	}
	t.Setenv("PATH", filepath.Join(dir, "empty-path"))
	checkCmd := bootstrapCmd()
	var checkOutput bytes.Buffer
	checkCmd.SetOut(&checkOutput)
	checkCmd.SetErr(&bytes.Buffer{})
	checkReportPath := filepath.Join(dir, "private-reports", "setup.json")
	checkCmd.SetArgs([]string{"setup", "check", "--config", path, "--format", "json", "--local-only", "--save-report", checkReportPath})
	if err := checkCmd.Execute(); err == nil {
		t.Fatal("blocked local readiness returned success")
	} else {
		var exit *doctorExitCodeErr
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("blocked local readiness returned %v, want exit code 2", err)
		}
	}
	var readiness setup.ReadinessReport
	if err := json.Unmarshal(checkOutput.Bytes(), &readiness); err != nil {
		t.Fatalf("invalid check JSON: %v: %s", err, checkOutput.String())
	}
	if readiness.State != "blocked" || readiness.ReadyToApply || readiness.HostsChecked || !readiness.ToolsChecked || readiness.BlockedCount == 0 {
		t.Fatalf("incorrect local check status: %+v", readiness)
	}
	stored, err := os.ReadFile(checkReportPath)
	if err != nil {
		t.Fatalf("blocked check did not save its report: %v", err)
	}
	var storedReport setup.ReadinessReport
	if err := json.Unmarshal(stored, &storedReport); err != nil || storedReport.InputHash != readiness.InputHash || storedReport.BlockedCount != readiness.BlockedCount {
		t.Fatalf("stored check report differs from output: %+v, %v", storedReport, err)
	}
	if info, err := os.Stat(checkReportPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("saved report permissions: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent-fleet")); !os.IsNotExist(err) {
		t.Fatalf("local check wrote Fleet path: %v", err)
	}
	textCmd := bootstrapCmd()
	var textOutput bytes.Buffer
	textCmd.SetOut(&textOutput)
	textCmd.SetErr(&textOutput)
	textCmd.SetArgs([]string{"setup", "plan", "--config", path})
	if err := textCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rke2-first-server", "git-provider", "Checks still needed before apply", "This local plan cannot be applied"} {
		if !strings.Contains(textOutput.String(), want) {
			t.Errorf("text plan missing %q in %q", want, textOutput.String())
		}
	}
	for _, tc := range []struct{ from, to, want string }{
		{"DOMAIN=example.test", "DOMAIN=not-a-domain", "--domain"},
		{"KUBE_DC_INIT_PRESET=internal-only", "KUBE_DC_INIT_PRESET=bad-preset", "--preset"},
	} {
		bad := strings.Replace(validConfig, tc.from, tc.to, 1)
		if err := os.WriteFile(filepath.Join(dir, "cluster.env"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		badCmd := bootstrapCmd()
		badCmd.SetOut(&bytes.Buffer{})
		badCmd.SetErr(&bytes.Buffer{})
		badCmd.SetArgs([]string{"setup", "validate", "--config", path})
		if err := badCmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("edit %q: expected %s error, got %v", tc.to, tc.want, err)
		}
	}
}

func TestWriteSetupReadinessLeavesUnblockedReportActionRequired(t *testing.T) {
	report := setup.ReadinessReport{
		SchemaVersion: setup.ReadinessSchemaVersion, Cluster: "demo", Profile: "evaluation@v1",
		State: "action-required", ReadyToApply: false, HostsChecked: false,
		Findings:   []setup.ReadinessFinding{{ID: "release.kind", Status: "pass", Detail: "versioned installer record"}},
		Unresolved: []string{"host-inventory-not-run", "exact-effects-diff"},
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := writeSetupReadiness(cmd, report, "json"); err != nil {
		t.Fatalf("unblocked report should return success: %v", err)
	}
	var decoded setup.ReadinessReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State != "action-required" || decoded.ReadyToApply || decoded.HostsChecked || len(decoded.Unresolved) != 2 {
		t.Fatalf("unblocked output claimed readiness: %+v", decoded)
	}
}

func TestBootstrapSetupValidateRequiresConfig(t *testing.T) {
	cmd := bootstrapSetupCmd()
	cmd.SetArgs([]string{"validate"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--config is required") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestBootstrapSetupDemoEntryIsDiscoverable(t *testing.T) {
	cmd := bootstrapSetupCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"demo", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"synthetic data", "--draft"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("demo help missing %q: %s", want, output.String())
		}
	}
}

func TestBootstrapSetupToolsWorksWithoutFleetOrKubeconfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "empty-path"))
	t.Setenv("KUBECONFIG", filepath.Join(dir, "absent-kubeconfig"))
	config := "CLUSTER_NAME=demo\nDOMAIN=example.test\nNODE_EXTERNAL_IP=192.0.2.10\nEMAIL=ops@example.test\nKUBE_DC_INIT_PRESET=internal-only\nKUBE_DC_INIT_FLEET_MODE=new-repo\nOBJECT_STORAGE_MODE=disabled\n"
	if err := os.WriteFile(filepath.Join(dir, "cluster.env"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), []byte(`{"version":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := setup.Spec{
		SchemaVersion: setup.SchemaVersion, Name: "demo", Profile: "evaluation@v1",
		Release:      setup.Release{RecordFile: "release.json", StarterRef: "oci://ghcr.io/kube-dc/fleet-starter@sha256:" + strings.Repeat("a", 64), RKE2Version: "v1.33.0+rke2r1"},
		Target:       setup.Target{Intent: clusterinit.ModeAuto},
		Hosts:        []setup.Host{{ID: "server-1", SSHAlias: "admin@server-1", Role: "server", ManagementAddress: "192.0.2.10"}},
		Platform:     setup.Platform{ConfigFile: "cluster.env"},
		Verification: setup.Verification{Capabilities: []string{"containers"}},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := bootstrapSetupCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"tools", "--config", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing required tools accepted")
	}
	for _, want := range []string{"age: missing", "helm: missing", "gh: missing", "required tool(s) need attention"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %q", want, output.String())
		}
	}
}

func TestInitToolSelectionMatchesRemoteEffects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  clusterinit.InitOptions
		wantPush bool
		wantRepo bool
	}{
		{"new-repo", clusterinit.InitOptions{FleetMode: clusterinit.FleetNewRepo}, true, true},
		{"existing-repo", clusterinit.InitOptions{FleetMode: clusterinit.FleetExistingRepo}, true, false},
		{"no-create", clusterinit.InitOptions{FleetMode: clusterinit.FleetNewRepo, NoCreateRepo: true}, true, false},
		{"no-push", clusterinit.InitOptions{FleetMode: clusterinit.FleetNewRepo, NoPush: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := initToolSelection(&tc.options)
			if selection.Operation != discover.ToolInit || selection.Push != tc.wantPush || selection.CreateRepo != tc.wantRepo {
				t.Fatalf("selection = %+v", selection)
			}
		})
	}
}

func TestManualToolBlockersBeforeStarter(t *testing.T) {
	checks := []discover.ToolCheck{
		{Requirement: discover.ToolRequirement{Name: "helm", AutoInstall: true}, Result: ports.Result{Status: ports.StatusMissing}},
		{Requirement: discover.ToolRequirement{Name: "git", AutoInstall: false}, Result: ports.Result{Status: ports.StatusMissing}},
		{Requirement: discover.ToolRequirement{Name: "sops", AutoInstall: true}, Result: ports.Result{Status: ports.StatusPartial}},
	}
	if got := manualToolBlockers(checks, true); strings.Join(got, ",") != "git,sops" {
		t.Fatalf("with installer, manual tools = %v", got)
	}
	if got := manualToolBlockers(checks, false); strings.Join(got, ",") != "helm,git,sops" {
		t.Fatalf("without installer, manual tools = %v", got)
	}
	if !needsFleetInstaller(checks) {
		t.Fatal("missing Helm should require the Fleet installer")
	}
	checks[0].Result.Status = ports.StatusInstalled
	if needsFleetInstaller(checks) {
		t.Fatal("missing manual tool and old SOPS should not invoke Fleet installer")
	}
}

func TestSetupReviewAndRecheckHaveNoImplicitWritePath(t *testing.T) {
	for _, name := range []string{"review", "recheck"} {
		cmd := bootstrapCmd()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs([]string{"setup", name})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("%s accepts implicit targets: %v", name, err)
		}
	}
	cmd := bootstrapCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"setup", "review", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--artifact-cache", "--prepare-dir", "--save-review", "does not authorize production setup"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("review help missing %q", want)
		}
	}
}

func TestSetupSessionCommandsDoNotClaimTargetsOrApply(t *testing.T) {
	for _, name := range []string{"create", "status", "inspect", "open"} {
		cmd := bootstrapCmd()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs([]string{"setup", "session", name})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("%s accepts implicit session paths: %v", name, err)
		}
	}
	cmd := bootstrapCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"setup", "session", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "do not claim remote targets") || !strings.Contains(output.String(), "status") {
		t.Fatal("session help implies installation ownership")
	}
}
