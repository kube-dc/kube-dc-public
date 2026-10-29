package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/shalb/kube-dc/cli/internal/selfupdate"
	"github.com/spf13/cobra"
)

// autoUpdateWait is how long a finished command waits for a self-update that
// is still downloading. An unfinished one is abandoned and retried later.
const autoUpdateWait = 10 * time.Second

func terminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// startAutoUpdate checks for a newer release in the background (at most once
// a day, see internal/selfupdate) and installs it. The returned function is
// called when the command has finished: it waits briefly for an install in
// progress and tells the user on stderr what changed.
func startAutoUpdate(args []string) func() {
	noop := func() {}
	u, err := selfupdate.New(version)
	if err != nil {
		return noop
	}
	interactive := terminal(os.Stdout) && terminal(os.Stderr)
	if selfupdate.SkipReason(version, args, os.Getenv, interactive, u.Executable) != "" || !u.Due() {
		return noop
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan selfupdate.Result, 1)
	go func() {
		res, _ := u.Run(ctx, false, false)
		done <- res
	}()
	return func() {
		defer cancel()
		select {
		case res := <-done:
			if res.Installed {
				fmt.Fprintf(os.Stderr, "\nkube-dc updated %s -> %s; it takes effect on the next run. Release notes: %s\n(turn this off with KUBE_DC_NO_UPDATE=1)\n", res.From, res.To, res.URL)
			}
		case <-time.After(autoUpdateWait):
		}
	}
}

func updateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the kube-dc CLI to the latest release",
		Long: `Update the kube-dc CLI to the latest release.

The CLI also does this by itself, at most once a day, when it runs in an
interactive terminal: it downloads the new release, checks the binary's
SHA-256 against the release's checksums, and replaces itself. Set
KUBE_DC_NO_UPDATE=1 to turn that off. Builds installed by a package manager
(Homebrew, Nix, Snap) are updated by that package manager instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !selfupdate.Released(version) {
				return fmt.Errorf("this is a development build (%s); install a release from https://github.com/kube-dc/kube-dc-public/releases", version)
			}
			u, err := selfupdate.New(version)
			if err != nil {
				return err
			}
			if !check {
				if why := selfupdate.SkipReason(version, []string{"secrets"}, func(string) string { return "" }, true, u.Executable); why != "" {
					return fmt.Errorf("cannot update %s: %s", u.Executable, why)
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			res, err := u.Run(ctx, true, check)
			if err != nil {
				return fmt.Errorf("update: %w", err)
			}
			out := cmd.OutOrStdout()
			switch {
			case res.Installed:
				fmt.Fprintf(out, "Updated %s -> %s (%s)\n", res.From, res.To, u.Executable)
			case selfupdate.Newer(res.To, version):
				fmt.Fprintf(out, "%s is available (this is %s). Run kube-dc update to install it.\n", res.To, version)
			default:
				fmt.Fprintf(out, "kube-dc %s is the latest release.\n", version)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only report whether a newer release exists")
	return cmd
}
