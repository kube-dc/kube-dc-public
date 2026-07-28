//go:build !windows

package clusterinit

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory flock on path (creating it), so two
// processes cannot interleave a sweep-write-encrypt-rename sequence on the
// same artifact: without it, one run's stale-temp sweep can delete the OTHER
// run's live temp, and two completing runs race last-rename-wins (codex
// pass-5, HIGH). flock releases on process death, so a crash never wedges
// later runs the way an O_EXCL lock file would.
//
// The returned release func closes (and thereby unlocks) the handle; the lock
// file itself is left in place — removing it would reopen the race for a
// third process that opened it between our unlock and unlink.
func lockFile(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}
