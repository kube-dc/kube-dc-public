//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// privateDirectory refuses a directory that is a link, belongs to another
// user, or that anyone but its owner can enter or write to.
func privateDirectory(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to other users (mode %04o); make it 0700", dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	return nil
}

// processGone reports whether no process has this ID any more.
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
