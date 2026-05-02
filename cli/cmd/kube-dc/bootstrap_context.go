package main

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	bttui "github.com/shalb/kube-dc/cli/internal/bootstrap/tui"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens"
)

// bootstrapContextCmd registers `kube-dc bootstrap context` — the
// kubectx-aware context picker described in installer-prd.md §16.6.
//
// Reads ~/.kube/config (or $KUBECONFIG), classifies every context as
// ADMIN / TENANT / BREAK-GLASS / EXTERNAL, lets the operator activate
// or delete one, and shows JWT introspection (email + group claims +
// expiry) for kube-dc OIDC contexts so "who am I on this cluster?" is
// answerable without leaving the TUI.
//
// Lives under `bootstrap` because it composes with the rest of the
// bootstrap surface (the fleet view's `[c]onfig`, the `kubeconfig`
// command, the `--admin` login flag); standalone-discoverable would
// have meant duplicating subcommand boilerplate without clear gain.
func bootstrapContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Browse and switch kubeconfig contexts (kubectx + identity-aware)",
		Long: `Open the Kube-DC context manager: a kubectx-aware TUI that lists every
context in ~/.kube/config (or whatever KUBECONFIG resolves to), tags each
with its identity (ADMIN, TENANT, BREAK-GLASS, EXTERNAL), shows the
operator their effective identity (email + groups from the cached JWT)
on the selected cluster, and lets you switch contexts with [enter].

The screen never modifies external (non-kube-dc) contexts beyond
setting current-context. To delete an external context, use
'kubectl config delete-context'.`,
		Example: `  # Open the context manager
  kube-dc bootstrap context`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// `bootstrap context` opens the integrated TUI on the
			// Contexts tab; once inside, `]` cycles to Fleet. We pass a
			// best-effort fleet repo path so the Fleet tab is usable
			// from the same session, but we don't error out when it
			// can't be resolved — the user explicitly asked for the
			// context manager.
			repo := bestEffortFleetRepo()
			return bttui.RunRoot(func() tea.Model {
				return screens.NewRootModel(repo, screens.RootTabContext)
			})
		},
	}
}

// bestEffortFleetRepo returns a fleet repo path for the Fleet tab when
// the user didn't pass --repo / $KUBE_DC_FLEET. The Fleet tab will
// render a "no clusters" state for an empty / missing repo, which is
// fine for the contexts-first flow.
func bestEffortFleetRepo() string {
	if env := os.Getenv("KUBE_DC_FLEET"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube-dc", "fleet")
	}
	return ""
}

