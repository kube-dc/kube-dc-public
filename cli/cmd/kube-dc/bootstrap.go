package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
	"github.com/shalb/kube-dc/cli/internal/telemetry"
)

// bootstrapCmd registers `kube-dc bootstrap` and its subcommands. The
// no-arg form opens the integrated Fleet, Contexts, and New Cluster screens.
// Subcommands also provide standalone installation and management operations.
func bootstrapCmd() *cobra.Command {
	var fleetRepo string

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap, adopt, and manage Kube-DC clusters via the kube-dc-fleet GitOps repo",
		Long: `Bootstrap is a Bubble Tea TUI front-end over the kube-dc-fleet
bootstrap suite (bootstrap/{flux-install,add-cluster,...}.sh). Running
it without arguments lands on the multi-cluster fleet view, listing
every cluster overlay in the configured fleet repo with status pills.

The fleet repo path defaults to ~/.kube-dc/fleet. On a clean workstation,
Bootstrap opens the new-cluster form. To inspect an existing fleet, pass
--repo or set KUBE_DC_FLEET.`,
		Example: `  # Show the fleet landing screen against a local clone
  kube-dc bootstrap --repo ~/projects/kube-dc-fleet

  # Same, via env var
  KUBE_DC_FLEET=~/projects/kube-dc-fleet kube-dc bootstrap`,
		// C2 telemetry seam: one hook covers every bootstrap
		// subcommand. The label is the static command path ONLY
		// ("kube-dc bootstrap init") — never arguments or flag
		// values, which can carry domains/cluster names. No-op
		// unless the operator set KUBE_DC_TELEMETRY=1; v1 sink is a
		// local counter file (nothing leaves the machine).
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			telemetry.Count(cmd.CommandPath())
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, startTab, err := bootstrapLandingRepo(fleetRepo)
			if err != nil {
				return err
			}
			root := screens.NewRootModel(repo, startTab)
			root.ConfigureInitServices(bootstrapPanelServices(cmd.Context()))
			defer root.Close()
			if err := bttui.RunRoot(func() tea.Model { return root }); err != nil {
				return err
			}
			if options, ok := root.AcceptedInit(); ok {
				init := bootstrapInitCmdWithOptions(&repo, options)
				init.SetContext(cmd.Context())
				init.SetIn(cmd.InOrStdin())
				init.SetOut(cmd.OutOrStdout())
				init.SetErr(cmd.ErrOrStderr())
				return init.RunE(init, nil)
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&fleetRepo, "repo", "",
		"Path to a local kube-dc-fleet checkout (defaults to $KUBE_DC_FLEET, then ~/.kube-dc/fleet)")

	cmd.AddCommand(bootstrapKubeconfigCmd(&fleetRepo))
	cmd.AddCommand(bootstrapInstallCmd(&fleetRepo))
	cmd.AddCommand(bootstrapRemoveNodeCmd(&fleetRepo))
	cmd.AddCommand(bootstrapConnectCmd(&fleetRepo))
	cmd.AddCommand(bootstrapConfigCmd(&fleetRepo))
	cmd.AddCommand(bootstrapAdoptCmd(&fleetRepo))
	cmd.AddCommand(bootstrapFetchKubeconfigCmd(&fleetRepo))
	cmd.AddCommand(bootstrapInstallPrereqsCmd(&fleetRepo))
	cmd.AddCommand(bootstrapContextCmd())
	cmd.AddCommand(bootstrapBreakGlassCmd(&fleetRepo))
	cmd.AddCommand(bootstrapDoctorCmd(&fleetRepo))
	cmd.AddCommand(bootstrapStatusCmd(&fleetRepo))
	cmd.AddCommand(bootstrapInitCmd(&fleetRepo))
	cmd.AddCommand(bootstrapSetupCmd())
	cmd.AddCommand(bootstrapOpenBaoCmd(&fleetRepo))
	cmd.AddCommand(bootstrapKeycloakCmd(&fleetRepo))
	cmd.AddCommand(bootstrapAccessCmd(&fleetRepo))
	cmd.AddCommand(bootstrapOIDCCutoverCmd(&fleetRepo))
	cmd.AddCommand(bootstrapAcceptCmd(&fleetRepo))
	cmd.AddCommand(bootstrapAnchorsCmd(&fleetRepo))
	cmd.AddCommand(bootstrapAddNodeCmd())
	cmd.AddCommand(bootstrapGPUCmd(&fleetRepo))
	cmd.AddCommand(bootstrapServicesCmd(&fleetRepo))

	return cmd
}

// bootstrapLandingRepo keeps the no-argument TUI usable on a clean workstation.
// An absent target is a prospective Fleet path, not a broken checkout. Other
// subcommands still use resolveFleetRepo when they require an existing Fleet.
func bootstrapLandingRepo(flag string) (string, screens.RootTab, error) {
	candidate := flag
	if candidate == "" {
		candidate = os.Getenv("KUBE_DC_FLEET")
	}
	if candidate == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", screens.RootTabInit, fmt.Errorf("resolve default Fleet path: %w", homeErr)
		}
		candidate = filepath.Join(home, ".kube-dc", "fleet")
	}
	if strings.HasPrefix(candidate, "~/") {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", screens.RootTabInit, fmt.Errorf("expand Fleet path: %w", homeErr)
		}
		candidate = filepath.Join(home, candidate[2:])
	}
	if candidate == "~" {
		return "", screens.RootTabInit, fmt.Errorf("Fleet path must be a directory for the cluster, not the home directory")
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", screens.RootTabInit, fmt.Errorf("resolve Fleet path: %w", err)
	}
	if st, statErr := os.Stat(abs); statErr == nil && !st.IsDir() {
		return "", screens.RootTabInit, fmt.Errorf("Fleet path %s is a file", abs)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", screens.RootTabInit, fmt.Errorf("inspect Fleet path: %w", statErr)
	}
	if clusters, statErr := os.Stat(filepath.Join(abs, "clusters")); statErr == nil && clusters.IsDir() {
		return abs, screens.RootTabFleet, nil
	}
	return abs, screens.RootTabInit, nil
}

// resolveFleetRepo picks the fleet repo path from (in order): the --repo
// flag, the KUBE_DC_FLEET environment variable, and ~/.kube-dc/fleet.
// Returns an error if no path resolves to an existing directory.
func resolveFleetRepo(flag string) (string, error) {
	candidates := []string{flag, os.Getenv("KUBE_DC_FLEET")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".kube-dc", "fleet"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		// Tilde expansion for the flag value (env vars are pre-expanded by shell).
		if len(c) > 1 && c[0] == '~' {
			if home, err := os.UserHomeDir(); err == nil {
				c = filepath.Join(home, c[1:])
			}
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		st, err := os.Stat(abs)
		if err == nil && st.IsDir() {
			return abs, nil
		}
	}
	return "", fmt.Errorf("no fleet repo found — pass --repo or set $KUBE_DC_FLEET, or clone kube-dc/kube-dc-fleet to ~/.kube-dc/fleet")
}
