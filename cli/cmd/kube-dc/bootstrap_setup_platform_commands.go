package main

import (
	"encoding/json"
	"fmt"

	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/spf13/cobra"
)

func platformSessionCommands() []*cobra.Command {
	var directory string
	review := &cobra.Command{Use: "review-platform", Short: "Refresh execution review without changing earlier proofs", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if directory == "" {
			return fmt.Errorf("--session-dir is required")
		}
		c, err := setup.OpenSessionCoordinator(directory, sshadapter.New(), runningCLIPath())
		if err != nil {
			return err
		}
		defer c.CloseEvidence()
		report, err := c.RefreshPlatformReview(cmd.Context())
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}}
	review.Flags().StringVar(&directory, "session-dir", "", "Existing private installation session directory")
	var recoveryDirectory string
	var record bool
	inspect := &cobra.Command{Use: "inspect-effects", Short: "Inspect uncertain effects and retain verified outcomes", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if recoveryDirectory == "" {
			return fmt.Errorf("--session-dir is required")
		}
		c, err := setup.OpenSessionCoordinator(recoveryDirectory, sshadapter.New(), runningCLIPath())
		if err != nil {
			return err
		}
		defer c.CloseEvidence()
		report, err := c.ReconcilePlatform(cmd.Context(), guidedPlatformEngine{}, record)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}}
	inspect.Flags().StringVar(&recoveryDirectory, "session-dir", "", "Existing private installation session directory")
	inspect.Flags().BoolVar(&record, "record-verified", false, "Record freshly verified positive outcomes; never retry or restore write authority")
	var runDirectory, reviewHash string
	run := &cobra.Command{Use: "run-platform", Short: "Run the reviewed platform path after qualified readiness", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if runDirectory == "" || reviewHash == "" {
			return fmt.Errorf("--session-dir and --review-hash are required")
		}
		record, err := setup.ReadSession(runDirectory)
		if err != nil {
			return err
		}
		readiness, err := collectSetupReadiness(cmd.Context(), record.SpecFile, true)
		if err != nil {
			return err
		}
		// The public reducer retains qualification/workload gates. There is no
		// force flag, readiness-file import, or restored prior-run authority.
		if !readiness.ReadyToApply {
			return fmt.Errorf("platform execution is blocked by setup readiness; review the release, workload verification, and live qualification gates")
		}
		c, err := setup.OpenSessionCoordinator(runDirectory, sshadapter.New(), runningCLIPath())
		if err != nil {
			return err
		}
		defer c.CloseEvidence()
		o := c.Inputs()
		token := resolveGitHubToken(&o, cmd.OutOrStdout())
		return c.RunPlatform(cmd.Context(), readiness, reviewHash, guidedPlatformEngine{token: token}, cmd.OutOrStdout(), nil)
	}}
	run.Flags().StringVar(&runDirectory, "session-dir", "", "Existing private installation session directory")
	run.Flags().StringVar(&reviewHash, "review-hash", "", "Hash of the retained platform execution review")
	return []*cobra.Command{review, inspect, run}
}
