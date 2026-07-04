package ports

import "context"

// SystemctlClient is the contract for systemd-unit operations on the host
// the CLI is running on. Real adapter shells out to `systemctl` via
// os/exec; mock adapter returns fixture state from scenario YAML.
//
// Used by M1-T02 (host probes — `is-active rke2-server`) and M4-T07
// (auto-install prereqs may restart units). Restart isn't called in v1
// MVP outside specific kubelet/rke2 paths — listed for v2 completeness.
type SystemctlClient interface {
	// IsActive returns true when `systemctl is-active <unit>` exits 0
	// AND prints "active" to stdout (matches the conventional check).
	// Inactive / failed / activating all return false; the adapter
	// surfaces the precise sub-state via the second return for callers
	// that care (M1 doctor host-probe).
	IsActive(ctx context.Context, unit string) (active bool, state string, err error)

	// Restart issues `systemctl restart <unit>`. Used sparingly in v1.
	// Requires sudo on the host; the adapter surfaces ErrNeedsSudo
	// when invoked unprivileged so the CLI can prompt before retrying.
	Restart(ctx context.Context, unit string) error
}
