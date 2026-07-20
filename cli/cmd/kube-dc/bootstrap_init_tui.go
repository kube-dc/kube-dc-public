package main

import (
	"context"
	"fmt"
	"io"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/installrun"
)

// runInitInstallTUI runs the mutating apply inside the live install
// screen (milestone checklist + colorized log pane) instead of
// streaming raw script output to stdout. The engine closure is the
// same runInit apply path used by --no-tty; installrun supplies the log
// writer + StepReporter and renders them. Cancellation ('q' / ctrl+c)
// propagates to the engine via the context installrun.Run derives.
func runInitInstallTUI(ctx context.Context, postOut io.Writer, o *clusterinit.InitOptions, modeRes modeResolution) error {
	res, err := installrun.RunWithOptions(ctx, installrun.Options{
		RunName: "bootstrap-init-" + o.Name,
	}, func(ctx context.Context, out io.Writer, rep clusterinit.StepReporter) error {
		return runInit(ctx, out, o, modeRes, rep)
	})
	if res != nil && res.LogPath != "" {
		if err != nil {
			fmt.Fprintf(postOut, "Install failed. Log: %s\n", res.LogPath)
		} else {
			fmt.Fprintf(postOut, "Install complete. Log: %s\n", res.LogPath)
		}
	}
	// Access block (Keycloak admin password + SSO URLs) prints to the
	// real terminal AFTER the alt-screen closes — deliberately NOT
	// through the install screen's redacted log/transcript.
	if o.AccessSummary != "" {
		fmt.Fprint(postOut, o.AccessSummary)
	}
	return err
}
