package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	tea "charm.land/bubbletea/v2"
	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setupdemo"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/setupcheck"
)

// bootstrapSetupCmd reserves the guided-setup command boundary. The first
// exposed operation only compiles local inputs; execution awaits the shared
// coordinator, effects plan, and live readiness gates.
func bootstrapSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Review guided setup inputs and checks",
		Long:  "Setup validates local specifications, checks tools and the local release record, reads host facts, and shows a local plan. Use demo for an isolated TUI walkthrough. Production apply remains unavailable until live target, independent release qualification, and exact effect checks are implemented.",
	}
	var demoDraftPath string
	demo := &cobra.Command{
		Use:          "demo",
		Short:        "Try the full guided setup TUI with synthetic data",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			coord, err := setupdemo.NewCoordinator()
			if err != nil {
				return err
			}
			defer coord.Close()
			root, err := screens.NewDemoRootModel(cmd.Context(), coord, demoDraftPath)
			if err != nil {
				return err
			}
			defer root.Close()
			_, err = tea.NewProgram(root, tea.WithContext(cmd.Context())).Run()
			if root.Stopped() {
				return screens.ErrDemoStopped
			}
			return err
		},
	}
	demo.Flags().StringVar(&demoDraftPath, "draft", "", "Local demo draft path for save and reload")
	cmd.AddCommand(demo)
	cmd.AddCommand(bootstrapSetupReviewCmd(), bootstrapSetupRecheckCmd(), bootstrapSetupSessionCmd())
	var configPath string
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate and compile a local setup specification",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(configPath) == "" {
				return fmt.Errorf("--config is required")
			}
			spec, err := setup.Load(configPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Local setup specification is valid.\nCluster: %s\nProfile: %s\nHosts: %d\nInput hash: %s\n", spec.Name, spec.Profile, len(spec.Hosts), compiled.InputHash)
			if len(compiled.IgnoredKeys) != 0 {
				fmt.Fprintf(out, "Ignored init keys: %s\n", strings.Join(compiled.IgnoredKeys, ", "))
			}
			fmt.Fprintln(out, "Live readiness, release qualification, and effects planning have not run.")
			return nil
		},
	}
	validate.Flags().StringVar(&configPath, "config", "", "Path to a version 1 setup JSON file")
	cmd.AddCommand(validate)
	var releaseConfigPath, releaseFormat string
	release := &cobra.Command{
		Use:          "release",
		Short:        "Check the local installer release record and selected profile",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(releaseConfigPath) == "" {
				return fmt.Errorf("--config is required")
			}
			if releaseFormat != "text" && releaseFormat != "json" {
				return fmt.Errorf("--format must be text or json")
			}
			spec, err := setup.Load(releaseConfigPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			report, err := setup.InspectRelease(compiled)
			if err != nil {
				return err
			}
			report, err = checkSetupCLIArtifact(compiled, report)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if releaseFormat == "json" {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(out, "Installer release record: %s\nProfile: %s\nState: %s\nRecord SHA-256: %s\n", report.ReleaseID, report.Profile, report.State, report.ReleaseSHA256)
				for _, check := range report.Checks {
					fmt.Fprintf(out, "%s: %s - %s\n", check.ID, check.Status, check.Detail)
					if check.Status == "blocked" && check.NextAction != "" {
						fmt.Fprintf(out, "  Next: %s\n", check.NextAction)
					}
				}
				fmt.Fprintln(out, "Only the current CLI binary bytes were checked. Other artifacts, qualification evidence, live pins, hosts, and target identity remain open. This does not authorize installation.")
			}
			if report.State == "blocked" {
				return &doctorExitCodeErr{code: 2}
			}
			return nil
		},
	}
	release.Flags().StringVar(&releaseConfigPath, "config", "", "Path to a version 1 setup JSON file")
	release.Flags().StringVar(&releaseFormat, "format", "text", "Output format: text or json")
	cmd.AddCommand(release)
	var checkConfigPath, checkFormat, checkReportPath string
	var localOnly bool
	check := &cobra.Command{
		Use:          "check",
		Short:        "Show setup blockers and open checks in one report",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(checkConfigPath) == "" {
				return fmt.Errorf("--config is required")
			}
			if checkFormat != "text" && checkFormat != "json" && checkFormat != "tui" {
				return fmt.Errorf("--format must be text, json, or tui")
			}
			if checkFormat == "tui" {
				report, err := setupcheck.Run(cmd.Context(), func(ctx context.Context) (setup.ReadinessReport, error) {
					return collectSetupReadiness(ctx, checkConfigPath, localOnly)
				})
				if err != nil {
					return err
				}
				if checkReportPath != "" {
					if err := setup.SaveReadinessReport(checkReportPath, report); err != nil {
						return err
					}
				}
				if report.BlockedCount != 0 {
					return &doctorExitCodeErr{code: 2}
				}
				return nil
			}
			report, err := collectSetupReadiness(cmd.Context(), checkConfigPath, localOnly)
			if err != nil {
				return err
			}
			if checkReportPath != "" {
				if err := setup.SaveReadinessReport(checkReportPath, report); err != nil {
					return err
				}
			}
			return writeSetupReadiness(cmd, report, checkFormat)
		},
	}
	check.Flags().StringVar(&checkConfigPath, "config", "", "Path to a version 1 setup JSON file")
	check.Flags().StringVar(&checkFormat, "format", "text", "Output format: text, json, or tui")
	check.Flags().StringVar(&checkReportPath, "save-report", "", "Save the read-only report as a private JSON file")
	check.Flags().BoolVar(&localOnly, "local-only", false, "Skip SSH host checks and keep host readiness unresolved")
	cmd.AddCommand(check)
	var toolsConfigPath string
	toolCheck := &cobra.Command{
		Use:   "tools",
		Short: "Check tools required for the selected setup operation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(toolsConfigPath) == "" {
				return fmt.Errorf("--config is required")
			}
			spec, err := setup.Load(toolsConfigPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			checks, err := initWorkstationChecks(cmd.Context(), &compiled.Init)
			if err != nil {
				return err
			}
			missing := 0
			for _, check := range checks {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s", check.Requirement.Name, check.Result.Status)
				if check.Result.Version != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " (%s)", check.Result.Version)
				}
				fmt.Fprintf(cmd.OutOrStdout(), " - %s\n", check.Requirement.Reason)
				if check.Result.Status != ports.StatusInstalled && check.Result.Status != ports.StatusManaged {
					missing++
					if check.Result.Status == ports.StatusMissing && check.Requirement.AutoInstall {
						fmt.Fprintln(cmd.OutOrStdout(), "  Fleet prerequisite script can install this tool.")
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "  Install or upgrade this tool before setup.")
					}
					if check.Result.Detail != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", check.Result.Detail)
					}
				}
			}
			if missing != 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%d required tool(s) need attention.\n", missing)
				return &doctorExitCodeErr{code: 2}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Required local tools are available. Release and host checks have not run.")
			return nil
		},
	}
	toolCheck.Flags().StringVar(&toolsConfigPath, "config", "", "Path to a version 1 setup JSON file")
	cmd.AddCommand(toolCheck)
	var planConfigPath, planFormat string
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Show local setup steps and unresolved checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(planConfigPath) == "" {
				return fmt.Errorf("--config is required")
			}
			if planFormat != "text" && planFormat != "json" {
				return fmt.Errorf("--format must be text or json")
			}
			spec, err := setup.Load(planConfigPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			preview, err := setup.BuildPreview(compiled)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if planFormat == "json" {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(preview)
			}
			fmt.Fprintf(out, "Local setup plan for %s (%s)\n", preview.Cluster, preview.Profile)
			fmt.Fprintf(out, "Target intent: %s\nRelease record SHA-256: %s\nInput hash: %s\nPlan hash: %s\n", preview.Target.Intent, preview.ReleaseSHA256, preview.InputHash, preview.PlanHash)
			for _, stage := range preview.Stages {
				kind := "check"
				if stage.PotentialWrite {
					kind = "possible change"
				}
				fmt.Fprintf(out, "- %s [%s; %s]: %s", stage.ID, kind, strings.Join(stage.Scopes, ", "), stage.Title)
				if stage.Target != "" {
					fmt.Fprintf(out, " (%s)", stage.Target)
				}
				if stage.Condition != "" {
					fmt.Fprintf(out, " - %s", stage.Condition)
				}
				fmt.Fprintln(out)
			}
			fmt.Fprintln(out, "Checks still needed before apply:")
			for _, check := range preview.Unresolved {
				fmt.Fprintf(out, "- %s\n", check)
			}
			fmt.Fprintln(out, "This local plan cannot be applied. No host, repository, package, or cluster was changed.")
			return nil
		},
	}
	plan.Flags().StringVar(&planConfigPath, "config", "", "Path to a version 1 setup JSON file")
	plan.Flags().StringVar(&planFormat, "format", "text", "Output format: text or json")
	cmd.AddCommand(plan)
	var hostsConfigPath, hostsFormat string
	hosts := &cobra.Command{
		Use:          "hosts",
		Short:        "Check host facts and installer access over SSH",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(hostsConfigPath) == "" {
				return fmt.Errorf("--config is required")
			}
			if hostsFormat != "text" && hostsFormat != "json" {
				return fmt.Errorf("--format must be text or json")
			}
			spec, err := setup.Load(hostsConfigPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			report, err := setup.InspectHosts(cmd.Context(), compiled, sshadapter.New())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if hostsFormat == "json" {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else {
				for _, host := range report.Hosts {
					fmt.Fprintf(out, "%s (%s): %s\n", host.ID, host.Role, host.State)
					if host.Facts.MachineID != "" {
						fmt.Fprintf(out, "  Machine ID: %s\n", host.Facts.MachineID)
					}
					if host.Facts.HostKey != nil {
						fmt.Fprintf(out, "  SSH host key: %s (%s; %s)\n", host.Facts.HostKey.FingerprintSHA256, host.Facts.HostKey.Algorithm, host.Facts.HostKey.Address)
					}
					if host.Facts.OSID != "" {
						fmt.Fprintf(out, "  OS: %s %s; architecture: %s; CPUs: %d; memory: %d KiB\n", host.Facts.OSID, host.Facts.OSVersion, host.Facts.Architecture, host.Facts.CPUs, host.Facts.MemoryKiB)
					}
					if host.Facts.SelectedDisk != nil {
						disk := host.Facts.SelectedDisk
						fmt.Fprintf(out, "  Selected disk: %s", disk.SelectedPath)
						if disk.ResolvedPath != "" {
							fmt.Fprintf(out, " (resolves to %s)", disk.ResolvedPath)
						}
						if disk.SizeBytes != 0 {
							fmt.Fprintf(out, "; %.1f GiB", float64(disk.SizeBytes)/(1<<30))
						}
						if disk.DeviceNumber != "" {
							fmt.Fprintf(out, "; device %s", disk.DeviceNumber)
						}
						if disk.StableID != "" {
							fmt.Fprintf(out, "; ID %s", disk.StableID)
						}
						if disk.HolderCount != 0 {
							fmt.Fprintf(out, "; %d holder(s)", disk.HolderCount)
						}
						fmt.Fprintln(out)
					}
					for _, check := range host.Checks {
						fmt.Fprintf(out, "  %s: %s - %s\n", check.ID, check.Status, check.Detail)
						if check.NextAction != "" {
							fmt.Fprintf(out, "    Next: %s\n", check.NextAction)
						}
					}
				}
				for _, check := range report.Checks {
					fmt.Fprintf(out, "%s: %s - %s\n", check.ID, check.Status, check.Detail)
					if check.NextAction != "" {
						fmt.Fprintf(out, "  Next: %s\n", check.NextAction)
					}
				}
				fmt.Fprintln(out, "Host inventory does not authorize installation or disk preparation. Release, profile, device, and target checks remain.")
			}
			if report.State == "blocked" {
				return &doctorExitCodeErr{code: 2}
			}
			return nil
		},
	}
	hosts.Flags().StringVar(&hostsConfigPath, "config", "", "Path to a version 1 setup JSON file")
	hosts.Flags().StringVar(&hostsFormat, "format", "text", "Output format: text or json")
	cmd.AddCommand(hosts)
	return cmd
}

func collectSetupReadiness(ctx context.Context, configPath string, localOnly bool) (setup.ReadinessReport, error) {
	spec, err := setup.Load(configPath)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	compiled, err := setup.Compile(spec)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	plan, err := setup.BuildPreview(compiled)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	releaseReport, err := setup.InspectRelease(compiled)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	releaseReport, err = checkSetupCLIArtifact(compiled, releaseReport)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	toolChecks, err := initWorkstationChecks(ctx, &compiled.Init)
	if err != nil {
		return setup.ReadinessReport{}, err
	}
	tools := setupToolFacts(toolChecks, !compiled.Init.NoInstallPrereqs)
	var hostReport *setup.HostInventory
	if !localOnly {
		observed, err := setup.InspectHosts(ctx, compiled, sshadapter.New())
		if err != nil {
			return setup.ReadinessReport{}, err
		}
		hostReport = &observed
	}
	return setup.BuildReadiness(compiled, plan, releaseReport, tools, hostReport)
}

func checkSetupCLIArtifact(c setup.Compiled, report setup.ReleaseReport) (setup.ReleaseReport, error) {
	return setup.CheckCLIArtifactBytes(c, report, runningCLIPath())
}

// runningCLIPath names the kernel-held executable inode, not the pathname
// that a package upgrade can replace while this process is still running.
func runningCLIPath() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe"
	}
	return ""
}

func writeSetupReadiness(cmd *cobra.Command, report setup.ReadinessReport, format string) error {
	out := cmd.OutOrStdout()
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "Setup checks for %s (%s)\nState: %s; blocked: %d; pending: %d; passed: %d\n", report.Cluster, report.Profile, report.State, report.BlockedCount, report.PendingCount, report.PassedCount)
		for _, finding := range report.Findings {
			if finding.Status == "pass" || finding.Status == "not-applicable" {
				continue
			}
			fmt.Fprintf(out, "- %s [%s]: %s\n", finding.ID, finding.Status, finding.Detail)
			if finding.NextAction != "" {
				fmt.Fprintf(out, "  Next: %s\n", finding.NextAction)
			}
		}
		fmt.Fprintln(out, "Checks still needed before apply:")
		for _, id := range report.Unresolved {
			fmt.Fprintf(out, "- %s\n", id)
		}
		fmt.Fprintln(out, "This report does not authorize installation. No host, package, repository, or cluster was changed.")
	}
	if report.BlockedCount != 0 {
		return &doctorExitCodeErr{code: 2}
	}
	return nil
}

func setupToolFacts(checks []discover.ToolCheck, installPrereqs bool) []setup.ToolFact {
	facts := make([]setup.ToolFact, 0, len(checks))
	for _, check := range checks {
		fact := setup.ToolFact{
			Name: check.Requirement.Name, Status: string(check.Result.Status),
			Reason: check.Requirement.Reason, Detail: check.Result.Detail,
		}
		if check.Result.Status != ports.StatusInstalled && check.Result.Status != ports.StatusManaged {
			if installPrereqs && check.Result.Status == ports.StatusMissing && check.Requirement.AutoInstall {
				fact.NextAction = "Review and run the Fleet prerequisite script before setup."
			} else {
				fact.NextAction = "Install or upgrade this tool before setup."
			}
		}
		facts = append(facts, fact)
	}
	return facts
}

func initToolSelection(o *clusterinit.InitOptions) discover.ToolSelection {
	push := !o.NoPush
	return discover.ToolSelection{
		Operation:  discover.ToolInit,
		Push:       push,
		CreateRepo: push && o.FleetMode == clusterinit.FleetNewRepo && !o.NoCreateRepo,
		Provider:   string(o.Provider),
		// The host adapter uses Go SSH. An external ssh binary is only
		// required for a Git remote that explicitly uses SSH.
		SSH: false,
	}
}

// Tools that the current Fleet script cannot repair must be addressed before
// starter extraction. A present but unsupported tool also needs a manual
// upgrade because the script skips binaries already in PATH.
func manualToolBlockers(checks []discover.ToolCheck, installPrereqs bool) []string {
	var blocked []string
	for _, check := range checks {
		switch check.Result.Status {
		case ports.StatusInstalled, ports.StatusManaged:
			continue
		case ports.StatusMissing:
			if installPrereqs && check.Requirement.AutoInstall {
				continue
			}
		}
		blocked = append(blocked, check.Requirement.Name)
	}
	return blocked
}

func needsFleetInstaller(checks []discover.ToolCheck) bool {
	for _, check := range checks {
		if check.Result.Status == ports.StatusMissing && check.Requirement.AutoInstall {
			return true
		}
	}
	return false
}

func initWorkstationChecks(ctx context.Context, o *clusterinit.InitOptions) ([]discover.ToolCheck, error) {
	checks, err := discover.CheckRequiredTools(ctx, initToolSelection(o))
	if err != nil || o.NoInstallPrereqs || !needsFleetInstaller(checks) {
		return checks, err
	}
	prepare, err := discover.CheckRequiredTools(ctx, discover.ToolSelection{Operation: discover.ToolPrepare})
	if err != nil {
		return nil, err
	}
	return append(checks, prepare...), nil
}
