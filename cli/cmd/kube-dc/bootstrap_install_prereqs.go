package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// bootstrapInstallPrereqsCmd registers `kube-dc bootstrap
// install-prereqs` (M4-T07). Wraps the auto-install engine as a
// standalone command; the init auto-run wire-in is a follow-up.
//
// Behaviour:
//
//   - Runs the doctor's tool-probe surface (kubectl/flux/sops/age/
//     git/gh/ssh/bao) to find missing binaries.
//   - Prompts the operator on a TTY (`Install these tools? [Enter/y/n]`)
//     unless `--yes` is set.
//   - Shells out to `scripts/install-prerequisites.sh` via the
//     fleet's ScriptRunner. The script owns the platform-specific
//     install commands (apt-get / dnf / brew / pacman detection +
//     package names) — this cobra layer is a thin wrapper.
//   - Re-probes after install to confirm nothing was left behind.
//
// Exits 0 on all-installed, 1 on consent-refused, 2 on
// structural failure (script missing, runner error, incomplete
// install).
func bootstrapInstallPrereqsCmd(fleetRepo *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:           "install-prereqs",
		Short:         "Detect and install missing CLI prerequisites (M4-T07)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Runs the doctor's tool-probe surface, identifies missing binaries,
and installs them via 'scripts/install-prerequisites.sh' from the
fleet repo.

The install script owns the platform-specific commands
(apt-get / dnf / brew / pacman detection + package names). This
command is a thin wrapper: enumerate, prompt, shell out, re-probe.

Consent: on a TTY the command prompts before installing. Pass
--yes to skip the prompt (CI / runbook automation). Refusing the
prompt exits 1 without touching anything.

Exit code:
  0  no tools missing OR all missing tools installed successfully
  1  consent refused
  2  structural failure (script missing, runner error, incomplete
     install)`,
		Example: `  # Interactive
  kube-dc bootstrap install-prereqs --repo ~/projects/kube-dc-fleet

  # CI / runbook
  kube-dc bootstrap install-prereqs --repo ./fleet --yes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}

			// Script-only session — no K8s dependency, so a bare
			// laptop with no kubeconfig can still run this. The
			// engine below only requires the ScriptRunner IF it
			// discovers missing tools (no-op contract), so an empty
			// --repo is fine for the "verify my laptop is clean"
			// case; we DO need --repo when a subsequent install is
			// needed (engine surfaces `ErrInstallPrereqsMissingDependency`
			// on that path with the missing-tools list in the wrap).
			runner, err := bootstrap.NewScriptOnly(repo)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "bootstrap install-prereqs: build script runner: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}

			out := cmd.OutOrStdout()
			res, err := clusterinit.InstallPrereqs(cmd.Context(), clusterinit.InstallPrereqsOptions{
				Runner: runner,
				Assume: yes,
				// Use cmd.InOrStdin so cobra test harnesses can
				// inject stdin content — reviewer P3.
				Prompt: promptForInstallConsent(cmd.InOrStdin(), cmd.OutOrStdout()),
				Out:    out,
			})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "bootstrap install-prereqs: %v\n", err)
				// Reviewer P3: use errors.Is on the typed sentinel,
				// not string-matching, so a future wrap-message
				// change can't silently break the exit-code contract.
				if errors.Is(err, clusterinit.ErrInstallPrereqsConsentRequired) {
					return &doctorExitCodeErr{code: 1}
				}
				return &doctorExitCodeErr{code: 2}
			}
			if res.InstallRan {
				fmt.Fprintf(out, "[install-prereqs] installed %d tool(s): %v\n",
					len(res.MissingBefore), res.MissingBefore)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false,
		"Skip the interactive consent prompt (accept the install)")
	return cmd
}

// promptForInstallConsent reads Y/n from the supplied reader
// (cobra's `cmd.InOrStdin()` in production; a bytes.Buffer in
// tests). Empty input (bare Enter) counts as YES since that matches
// the operator's usual interpretation of the [Enter] prompt in
// doctor's next-command suggestions. Any other input (n / no /
// anything non-affirmative) is treated as NO — the operator has to
// type SOMETHING to opt out.
//
// Reviewer P3: previously read os.Stdin directly; that made cobra
// tests brittle (couldn't inject canned input). Reading via
// cmd.InOrStdin lets a test seed the response.
func promptForInstallConsent(in io.Reader, out io.Writer) func([]string) bool {
	return func(missing []string) bool {
		fmt.Fprintf(out, "\nInstall %d missing tools: %v ?\n[Enter/y] proceed, [n] skip: ",
			len(missing), missing)
		reader := bufio.NewReader(in)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			// EOF with no chars → treat as no-consent (safer default
			// for non-interactive contexts where a caller forgot --yes).
			return false
		}
		line = strings.TrimSpace(strings.ToLower(line))
		switch line {
		case "", "y", "yes":
			return true
		default:
			return false
		}
	}
}
