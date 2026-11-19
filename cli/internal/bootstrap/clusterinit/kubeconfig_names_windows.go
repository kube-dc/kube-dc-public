//go:build windows

package clusterinit

// hasOtherNames: link counts are not read on Windows.
func hasOtherNames(string) bool { return false }
