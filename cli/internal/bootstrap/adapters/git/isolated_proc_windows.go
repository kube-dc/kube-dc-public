//go:build windows

package git

import "os"

// lockRun has no advisory lock to take here, so a second attempt never
// "acquires" an existing run's lock and no run is ever treated as abandoned:
// a private checkout left by a killed run stays until removed by hand.
func lockRun(path string) (*os.File, bool) {
	if _, err := os.Stat(path); err == nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	return f, err == nil
}
