//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package setup

import (
	"fmt"
	"os"
	"syscall"
)

func lockSessionFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("session writer lock is unavailable: %w", err)
	}
	return nil
}
