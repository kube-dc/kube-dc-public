//go:build unix

package git

import (
	"os"
	"syscall"
)

// lockRun takes the exclusive lock that marks a private checkout as in use.
// It is held for the life of the process (or until the returned file is
// closed), and the kernel releases it when the process dies however it dies.
// Unlike a recorded PID, it cannot be mistaken for another process: a
// container that restarts as PID 1 does not hold its predecessor's lock.
func lockRun(path string) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
}
