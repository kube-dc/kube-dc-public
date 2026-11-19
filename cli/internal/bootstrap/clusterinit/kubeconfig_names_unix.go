//go:build !windows

package clusterinit

import (
	"os"
	"syscall"
)

// hasOtherNames reports whether the file at path has more than one name (hard
// links). kubectl rewrites a kubeconfig in place, so writing through one name
// changes the file under every other one, wherever that is.
func hasOtherNames(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
