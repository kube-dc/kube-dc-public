package main

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"fmt"
	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/spf13/cobra"
)

func bootstrapSetupSessionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Keep protected local installation evidence", Long: "A session records reviewed identities and stage evidence. Creation and status do not claim remote targets. Use open for explicit host reservations in the existing New Cluster TUI, or inspect for read-only remote evidence. Production apply remains blocked."}
	var configFile, reviewFile, directory string
	create := &cobra.Command{Use: "create", Short: "Create a private session from a saved safety review", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if configFile == "" || reviewFile == "" || directory == "" {
			return fmt.Errorf("--config, --review, and --session-dir are required")
		}
		spec, err := setup.Load(configFile)
		if err != nil {
			return err
		}
		c, err := setup.Compile(spec)
		if err != nil {
			return err
		}
		review, err := setup.LoadSafetyReview(reviewFile)
		if err != nil {
			return err
		}
		if err := setup.RecheckReviewFiles(cmd.Context(), c, review, runningCLIPath()); err != nil {
			return err
		}
		writer, err := setup.CreateSession(c, review, directory, configFile, reviewFile)
		if err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Private session created: %s\nNo targets were claimed. Production installation remains blocked.\n", directory)
		return nil
	}}
	create.Flags().StringVar(&configFile, "config", "", "Path to the reviewed setup JSON file")
	create.Flags().StringVar(&reviewFile, "review", "", "Private JSON file produced by setup review")
	create.Flags().StringVar(&directory, "session-dir", "", "New private directory outside the selected Git checkout")
	var statusDirectory, format string
	status := &cobra.Command{Use: "status", Short: "Read saved stage evidence without changing the session", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if statusDirectory == "" {
			return fmt.Errorf("--session-dir is required")
		}
		if format != "text" && format != "json" {
			return fmt.Errorf("--format must be text or json")
		}
		record, err := setup.ReadSession(statusDirectory)
		if err != nil {
			return err
		}
		summary := setup.SummarizeSession(record)
		if format == "json" {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(summary)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\nState: %s\nVerified stages: %d\nSaved events: %d\n", summary.ID, summary.State, summary.CompletedStages, summary.EventCount)
		for _, event := range summary.NeedsInspection {
			fmt.Fprintf(cmd.OutOrStdout(), "Inspect live state: %s, attempt %d\n", event.Stage, event.Attempt)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "This is saved evidence. Fresh target checks and remote ownership are still required; installation remains blocked.")
		return nil
	}}
	status.Flags().StringVar(&statusDirectory, "session-dir", "", "Existing private installation session directory")
	status.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	var inspectDirectory, inspectFormat string
	inspect := &cobra.Command{Use: "inspect", Short: "Read current remote target ownership", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if inspectDirectory == "" {
			return fmt.Errorf("--session-dir is required")
		}
		if inspectFormat != "text" && inspectFormat != "json" {
			return fmt.Errorf("--format must be text or json")
		}
		result, err := setup.InspectSessionTargets(cmd.Context(), inspectDirectory, sshadapter.New())
		if err != nil {
			return err
		}
		if inspectFormat == "json" {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\nSaved ownership: %s\n", result.Session.ID, result.Session.OwnershipPhase)
		for _, target := range result.Targets {
			fmt.Fprintf(cmd.OutOrStdout(), "Host %s: %s\n", target.HostID, target.State)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Unknown is not proof of absence. No targets were claimed. Installation remains blocked.")
		return nil
	}}
	inspect.Flags().StringVar(&inspectDirectory, "session-dir", "", "Existing private installation session directory")
	inspect.Flags().StringVar(&inspectFormat, "format", "text", "Output format: text or json")
	var openDirectory string
	open := &cobra.Command{Use: "open", Short: "Open session ownership in the existing New Cluster TUI", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
		if openDirectory == "" {
			return fmt.Errorf("--session-dir is required")
		}
		coordinator, err := setup.OpenSessionCoordinator(openDirectory, sshadapter.New(), runningCLIPath())
		if err != nil {
			return err
		}
		defer func() {
			if err := coordinator.Close(); err != nil {
				if runErr == nil {
					runErr = err
				} else {
					runErr = fmt.Errorf("%v; close: %w", runErr, err)
				}
			}
		}()
		root := screens.NewSessionRootModel(cmd.Context(), coordinator, openDirectory)
		defer root.Close()
		_, runErr = tea.NewProgram(root, tea.WithContext(cmd.Context())).Run()
		return runErr
	}}
	open.Flags().StringVar(&openDirectory, "session-dir", "", "Existing private installation session directory")
	cmd.AddCommand(create, status, inspect, open)
	return cmd
}
