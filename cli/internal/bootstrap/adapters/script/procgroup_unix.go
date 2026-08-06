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

// killProcessGroup SIGKILLs the whole group. Used as the escalation when
// cancellation's SIGTERM did not get every child to release our stdout/stderr
// pipes: a surviving grandchild keeps the write end open, the drainers never
// see EOF, and supervise would otherwise block before it ever reaches
// cmd.Wait() — where WaitDelay would have bounded it.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
