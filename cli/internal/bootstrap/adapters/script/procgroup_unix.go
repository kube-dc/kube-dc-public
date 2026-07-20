//go:build unix

package script

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup runs the script in its OWN process group so
// ctx-cancel can signal the entire tree (bash + any children it
// spawned: `sleep & wait $!` in a trap pattern is a classic source of
// "child holds stdout after parent exits" hangs). Without Setpgid +
// group-targeted signaling, an orphaned grandchild can keep our
// drainer blocked on Read indefinitely.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid = signal the entire process group (POSIX).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
