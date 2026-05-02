package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

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
			model, err := screens.NewContextModel()
			if err != nil {
				return err
			}
			p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
			_, err = p.Run()
			return err
		},
	}
}
