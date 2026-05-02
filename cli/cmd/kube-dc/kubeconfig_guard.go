package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// warnIfNonDefaultKubeconfig prints a clear destination notice and asks
// the operator to confirm when $KUBECONFIG points at something other
// than ~/.kube/config (or its first segment in a colon-list does).
//
// Why: once-bitten footgun where `export KUBECONFIG=~/.kube/cloud_tunnel`
// from a bootstrap step leaked into a follow-up `kube-dc login --admin`,
// causing the admin context to land in cloud_tunnel. kubectx reads
// ~/.kube/config by default and didn't show the new context, leading
// the operator to believe the kubeconfig had been wiped.
//
// Returns nil to proceed; returns a non-nil error to abort the calling
// command. When stdin isn't a TTY (CI, pipes, IDE shells) we print the
// warning but don't prompt — silent failure is worse than a noisy log.
func warnIfNonDefaultKubeconfig() error {
	target := kubeconfigTarget()
	defaultPath := defaultKubeconfigPath()

	if target == defaultPath {
		return nil // common case — silent
	}

	// Always tell the operator where the write is going. This banner
	// is the no-op-on-default but loud-on-deviation contract.
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  ┌─ kubeconfig destination ─")
	fmt.Fprintf(os.Stderr,  "  │  $KUBECONFIG = %s\n", os.Getenv("KUBECONFIG"))
	fmt.Fprintf(os.Stderr,  "  │  → writing to: %s\n", target)
	fmt.Fprintf(os.Stderr,  "  │  (default would be %s — kubectx reads from there)\n", defaultPath)
	fmt.Fprintln(os.Stderr, "  └──")

	// Only prompt when stdin is a TTY. Pipes / non-interactive shells
	// (CI, IDE-launched processes) skip the prompt — the banner above
	// is enough audit trail.
	st, err := os.Stdin.Stat()
	if err != nil || (st.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintln(os.Stderr, "  (non-interactive shell — proceeding without confirmation)")
		fmt.Fprintln(os.Stderr)
		return nil
	}

	fmt.Fprint(os.Stderr, "  Continue writing to this file? [y/N] ")
	r := bufio.NewReader(os.Stdin)
	answer, _ := r.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("aborted by operator (run `unset KUBECONFIG` to write to %s)", defaultPath)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// kubeconfigTarget mirrors kubeconfig.NewManager's resolution order:
// $KUBECONFIG (first segment of a colon-list) wins, then ~/.kube/config.
func kubeconfigTarget() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		// kubectl semantics: colon-separated list, first non-empty
		// segment is the write target.
		for _, p := range strings.Split(v, string(os.PathListSeparator)) {
			if p != "" {
				return p
			}
		}
	}
	return defaultKubeconfigPath()
}

func defaultKubeconfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.kube/config"
	}
	return filepath.Join(home, ".kube", "config")
}
