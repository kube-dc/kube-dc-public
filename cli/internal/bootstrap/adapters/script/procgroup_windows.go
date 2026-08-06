//go:build windows

package script

import "os/exec"

// configureProcessGroup is a compile-only fallback on Windows: there is
// no Setpgid / POSIX process group, so ctx-cancel keeps exec.Cmd's
// default behavior (Kill the direct child). The bootstrap scripts this
// runner executes are bash and never run on Windows anyway — the
// windows/amd64+arm64 CLI binaries exist for kubectl/login workflows,
// and this seam only keeps them building (the unix-only syscalls broke
// the v0.5.0 GoReleaser cross-compile).
func configureProcessGroup(_ *exec.Cmd) {}

// killProcessGroup has no group to target on Windows; kill the direct child,
// which is the same scope configureProcessGroup manages there.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
