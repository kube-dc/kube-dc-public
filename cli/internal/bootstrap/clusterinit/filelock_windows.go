//go:build windows

package clusterinit

// lockFile is a no-op on Windows. The scaffold pipeline (bash scripts, sops,
// flux) is not supported from a Windows host; the Windows binary exists for
// client-side commands only, so the cross-process guarantee the unix flock
// provides has no call sites to protect here.
func lockFile(string) (func(), error) {
	return func() {}, nil
}
