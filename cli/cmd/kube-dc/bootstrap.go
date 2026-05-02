package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
)

// bootstrapCmd registers `kube-dc bootstrap` and its subcommands. The
// no-arg form lands on the multi-cluster fleet view; everything else
// (install, adopt, status, config, doctor, add-node) jumps into the
// same Bubble Tea screen graph at a different entry point.
//
// See docs/prd/installer-prd.md §4.1 for the full surface; v1 ships
// just the fleet landing.
func bootstrapCmd() *cobra.Command {
	var fleetRepo string

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap, adopt, and manage Kube-DC clusters via the kube-dc-fleet GitOps repo",
		Long: `Bootstrap is a Bubble Tea TUI front-end over the kube-dc-fleet
bootstrap suite (bootstrap/{flux-install,add-cluster,...}.sh). Running
it without arguments lands on the multi-cluster fleet view, listing
every cluster overlay in the configured fleet repo with status pills.

The fleet repo path defaults to ~/.kube-dc/fleet (set up by future
'fleet init'); for now point at a local clone with --repo or the
KUBE_DC_FLEET environment variable.`,
		Example: `  # Show the fleet landing screen against a local clone
  kube-dc bootstrap --repo ~/projects/kube-dc-fleet

  # Same, via env var
  KUBE_DC_FLEET=~/projects/kube-dc-fleet kube-dc bootstrap`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(fleetRepo)
			if err != nil {
				return err
			}
			model := screens.NewFleetModel(repo)
			return bttui.RunFleet(repo, func(string) tea.Model { return model })
		},
	}

	cmd.PersistentFlags().StringVar(&fleetRepo, "repo", "",
		"Path to a local kube-dc-fleet checkout (defaults to $KUBE_DC_FLEET, then ~/.kube-dc/fleet)")

	cmd.AddCommand(bootstrapKubeconfigCmd(&fleetRepo))
	cmd.AddCommand(bootstrapContextCmd())
	cmd.AddCommand(bootstrapBreakGlassCmd(&fleetRepo))

	return cmd
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
